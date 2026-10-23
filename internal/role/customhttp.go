package role

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/tool"
)

// runCustomHTTP executes a definition-owned HTTP call. The URL and body are
// rendered from validated parameters before approval, so what the user approves
// is exactly what is sent. Secrets never appear in the approval card: only the
// method and URL are shown, and auth headers come from the environment.
func runCustomHTTP(ctx context.Context, cfg *config.UserConfig, rt *Runtime, t prompts.ResolvedTool, reason string, payload json.RawMessage) (string, error) {
	if t.HTTP == nil {
		return "", fmt.Errorf("tool %q has no HTTP specification", t.Name)
	}
	values, err := t.ValidatePayload(payload)
	if err != nil {
		return "", err
	}
	target, err := t.RenderURL(values)
	if err != nil {
		return "", err
	}
	body, err := t.RenderBody(values)
	if err != nil {
		return "", err
	}
	headers, err := resolveHTTPHeaders(t.HTTP)
	if err != nil {
		return "", err
	}
	if len(body) > 0 && headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}

	call := core.ToolCall{
		Name:    t.Name,
		Target:  httpTarget(target),
		Detail:  strings.ToUpper(t.HTTP.Method) + " " + target,
		Body:    bodyPreview(body),
		Reason:  reason,
		Trusted: toolTrusted(cfg, rt, t),
		Source:  t.Source,
	}
	rt.Observer.ToolCall(call)
	if !call.Trusted {
		ok, err := authorizeTool(rt, t)
		if err != nil {
			return "", fmt.Errorf("user interaction error: %w", err)
		}
		if !ok {
			rt.Observer.ToolResult(core.ToolResult{Skipped: true, Message: "Skipped"})
			return fmt.Sprintf("[%s result]: skipped\n%s", t.Name, tool.CancelledOutput), nil
		}
	}

	result, execErr := tool.ExecuteHTTP(ctx, tool.HTTPRequest{
		Method:   strings.ToUpper(t.HTTP.Method),
		URL:      target,
		Header:   flattenHeader(headers),
		Body:     body,
		MaxBytes: t.HTTP.MaxBytes,
	}, toolStream(rt))

	visible := tool.FormatHTTPBody(result.Body, t.HTTP.Format, t.HTTP.MaxBytes)
	truncate := execTruncate(cfg)
	if t.OutputLimit > 0 {
		truncate.MaxBytes = t.OutputLimit
	}

	status := fmt.Sprintf("HTTP %d", result.Status)
	switch {
	case result.TimedOut:
		status = "timed out"
	case execErr != nil:
		status = "error: " + execErr.Error()
	}
	switch {
	case errors.Is(execErr, context.Canceled):
		rt.Observer.ToolResult(core.ToolResult{Message: "Interrupted"})
	case execErr != nil:
		rt.Observer.ToolResult(core.ToolResult{
			Message: fmt.Sprintf("Tool %s failed: %v", t.Name, execErr),
			Detail:  visible,
		})
	case result.TimedOut:
		rt.Observer.ToolResult(core.ToolResult{
			Message: fmt.Sprintf("Tool %s: timed out", t.Name),
			Detail:  visible,
		})
	default:
		rt.Observer.ToolResult(core.ToolResult{
			OK:      result.Status >= 200 && result.Status < 300,
			Message: fmt.Sprintf("Tool %s: %s", t.Name, status),
		})
	}
	return fmt.Sprintf("[%s result]: %s\n%s", t.Name, status, truncate.Apply(visible)), nil
}

// maxHTTPBodyPreview bounds the body shown on the approval card. The full body
// is still what gets sent; this is review text only.
const maxHTTPBodyPreview = 512

func bodyPreview(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if len(body) <= maxHTTPBodyPreview {
		return string(body)
	}
	cut := body[:maxHTTPBodyPreview]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return string(cut) + "…"
}

// resolveHTTPHeaders applies the fixed headers and the auth reference. The
// secret is read from the environment at run time and must not contain a
// newline, or it would be a header injection.
func resolveHTTPHeaders(spec *prompts.HTTPSpec) (http.Header, error) {
	headers := make(http.Header, len(spec.Headers)+1)
	for key, value := range spec.Headers {
		headers.Set(key, value)
	}
	fields := strings.Fields(spec.Auth)
	switch {
	case len(fields) == 0:
		return headers, nil
	case fields[0] == "bearer" && len(fields) == 2:
		secret, err := lookupSecret(fields[1])
		if err != nil {
			return nil, err
		}
		headers.Set("Authorization", "Bearer "+secret)
	case fields[0] == "header" && len(fields) == 3:
		secret, err := lookupSecret(fields[2])
		if err != nil {
			return nil, err
		}
		headers.Set(fields[1], secret)
	default:
		return nil, fmt.Errorf("invalid auth %q; use 'bearer ENV' or 'header NAME ENV'", spec.Auth)
	}
	return headers, nil
}

func lookupSecret(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable %q is not set", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("environment variable %q contains a newline", name)
	}
	return value, nil
}

// flattenHeader keeps one value per name; the definition sets one.
func flattenHeader(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for key := range headers {
		out[key] = headers.Get(key)
	}
	return out
}

func httpTarget(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}
