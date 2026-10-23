package prompts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidatePayload decodes one JSON object into the tool's declared parameters.
// Defaults are applied, unknown fields are rejected, and every value is checked
// against the same Param rules the prompt schema was generated from. The caller
// runs this before approval, so the rendered argv is exactly what will run.
func (t ResolvedTool) ValidatePayload(raw json.RawMessage) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return nil, fmt.Errorf("payload must be a JSON object: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("payload must be a JSON object")
	}

	params := make(map[string]Param, len(t.Params))
	for _, p := range t.Params {
		params[p.Name] = p
	}
	for name := range fields {
		if _, ok := params[name]; !ok {
			return nil, fmt.Errorf("unknown parameter %q", name)
		}
	}

	values := make(map[string]any, len(t.Params))
	for _, p := range t.Params {
		field, present := fields[p.Name]
		switch {
		case present:
			if strings.TrimSpace(string(field)) == "null" {
				return nil, fmt.Errorf("parameter %q must not be null", p.Name)
			}
			v, err := decodeParam(p, field)
			if err != nil {
				return nil, err
			}
			values[p.Name] = v
		case p.Required:
			return nil, fmt.Errorf("missing required parameter %q", p.Name)
		case p.Default != nil:
			values[p.Name] = p.Default
		}
	}
	return values, nil
}

func decodeParam(p Param, raw json.RawMessage) (any, error) {
	switch p.Type {
	case ParamString, ParamEnum:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("parameter %q must be a string", p.Name)
		}
		if p.Type == ParamEnum {
			found := false
			for _, v := range p.Values {
				if v == s {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("parameter %q must be one of: %s", p.Name, strings.Join(p.Values, ", "))
			}
		}
		if p.MaxLength > 0 && utf8.RuneCountInString(s) > p.MaxLength {
			return nil, fmt.Errorf("parameter %q is longer than %d characters", p.Name, p.MaxLength)
		}
		if p.Pattern != "" {
			re, err := regexp.Compile(p.Pattern)
			if err != nil {
				return nil, fmt.Errorf("parameter %q has an invalid pattern", p.Name)
			}
			if !re.MatchString(s) {
				return nil, fmt.Errorf("parameter %q does not match %s", p.Name, p.Pattern)
			}
		}
		return s, nil
	case ParamInteger:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("parameter %q must be an integer", p.Name)
		}
		if err := checkRange(p, float64(n)); err != nil {
			return nil, err
		}
		return n, nil
	case ParamNumber:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("parameter %q must be a number", p.Name)
		}
		if err := checkRange(p, f); err != nil {
			return nil, err
		}
		return f, nil
	case ParamBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("parameter %q must be a boolean", p.Name)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("parameter %q has unsupported type %q", p.Name, p.Type)
	}
}

func checkRange(p Param, v float64) error {
	if p.Min != nil && v < *p.Min {
		return fmt.Errorf("parameter %q must be at least %v", p.Name, *p.Min)
	}
	if p.Max != nil && v > *p.Max {
		return fmt.Errorf("parameter %q must be at most %v", p.Name, *p.Max)
	}
	return nil
}

// RenderArgs substitutes validated values into the CLI argv. Substitution is
// verbatim: spaces, quotes, ';', '$()' and newlines stay inside their argument
// and are never interpreted by a shell.
func (t ResolvedTool) RenderArgs(values map[string]any) ([]string, error) {
	if t.CLI == nil {
		return nil, fmt.Errorf("tool %q has no CLI specification", t.Name)
	}
	for _, arg := range t.CLI.Args {
		if err := requireValues(arg, values); err != nil {
			return nil, err
		}
	}
	args := make([]string, len(t.CLI.Args))
	for i, arg := range t.CLI.Args {
		args[i] = placeholderRe.ReplaceAllStringFunc(arg, func(m string) string {
			return formatValue(values[m[1:len(m)-1]])
		})
	}
	return args, nil
}

// RenderURL substitutes validated values into the definition's HTTP URL. A
// value in the path is path-escaped and a value in the query is query-escaped,
// so a parameter can never add a path segment, a query pair, or a fragment.
// The scheme and host are definition-owned and were checked at load.
func (t ResolvedTool) RenderURL(values map[string]any) (string, error) {
	if t.HTTP == nil {
		return "", fmt.Errorf("tool %q has no HTTP specification", t.Name)
	}
	if err := requireValues(t.HTTP.URL, values); err != nil {
		return "", err
	}
	base, query := t.HTTP.URL, ""
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base, query = base[:i], base[i+1:]
	}
	base = placeholderRe.ReplaceAllStringFunc(base, func(m string) string {
		return url.PathEscape(formatValue(values[m[1:len(m)-1]]))
	})
	if query == "" {
		return base, nil
	}
	query = placeholderRe.ReplaceAllStringFunc(query, func(m string) string {
		return url.QueryEscape(formatValue(values[m[1:len(m)-1]]))
	})
	return base + "?" + query, nil
}

// RenderBody builds a JSON request body from declared body_params and the
// definition-owned body_extra values. Encoding an object means a string value
// can never change the shape of the JSON document.
func (t ResolvedTool) RenderBody(values map[string]any) ([]byte, error) {
	if t.HTTP == nil {
		return nil, fmt.Errorf("tool %q has no HTTP specification", t.Name)
	}
	if strings.EqualFold(t.HTTP.Method, "GET") {
		return nil, nil
	}
	body := make(map[string]any, len(t.HTTP.BodyParams)+len(t.HTTP.BodyExtra))
	for _, name := range t.HTTP.BodyParams {
		v, ok := values[name]
		if !ok {
			return nil, fmt.Errorf("parameter %q has no value", name)
		}
		body[name] = v
	}
	for key, value := range t.HTTP.BodyExtra {
		body[key] = value
	}
	if len(body) == 0 {
		return nil, nil
	}
	return json.Marshal(body)
}

// requireValues reports the first placeholder in s that has no value, so a
// renderer fails before approval or execution can start.
func requireValues(s string, values map[string]any) error {
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		if _, ok := values[m[1]]; !ok {
			return fmt.Errorf("parameter %q has no value", m[1])
		}
	}
	return nil
}

// FormatArgv renders an exact argv for display or a dry run. It quotes only
// values that would otherwise read as a different argument; execution never uses
// this string.
func FormatArgv(argv []string) string {
	rendered := make([]string, len(argv))
	for i, arg := range argv {
		if arg == "" || strings.ContainsAny(arg, " \t\r\n\"'\\$;|&<>()*?[]{}") {
			rendered[i] = strconv.Quote(arg)
			continue
		}
		rendered[i] = arg
	}
	return strings.Join(rendered, " ")
}

func formatValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}
