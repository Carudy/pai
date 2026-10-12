package role

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/prompts"
	"github.com/Carudy/pai/internal/tool"
)

type paperObserver struct {
	fakeObserver
	calls   []core.ToolCall
	results []core.ToolResult
}

func (o *paperObserver) ToolCall(c core.ToolCall)     { o.calls = append(o.calls, c) }
func (o *paperObserver) ToolResult(r core.ToolResult) { o.results = append(o.results, r) }

func TestPapersearchCapabilities(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range prompts.RoleNames() {
		rp, err := chat.LoadRolePrompt(name, chat.IntroOverride{})
		if err != nil {
			t.Fatal(err)
		}
		if err := checkToolCoverage(rp); err != nil {
			t.Fatal(err)
		}
		switch name {
		case "devops":
			if rp.HasTool("papersearch") {
				t.Error("devops should not have papersearch")
			}
		case "coder", "latex":
			if !rp.HasTool("papersearch") {
				t.Errorf("research-capable role %s lacks papersearch", name)
			}
		}
	}
	definition, _, err := prompts.ReadTool("papersearch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(definition), "payload is an object") {
		t.Fatal("missing object payload guidance")
	}
}

func TestPapersearchInvalidPayloadIsObservation(t *testing.T) {
	for _, payload := range []string{`"keywords"`, `null`, `{}`, `{"query":" "}`, `{"query":"x","source":"other"}`, `{"query":"x","limit":21}`, `{"query":"x","year_from":2025,"year_to":2024}`, `{"query":"x","limit":"5"}`, `{"query":`} {
		t.Run(payload, func(t *testing.T) {
			o := &paperObserver{}
			out, err := runPapersearch(context.Background(), &config.UserConfig{}, &Runtime{Observer: o}, "research", json.RawMessage(payload))
			if err != nil || !strings.HasPrefix(out, "[tool error]") {
				t.Fatalf("out=%q err=%v", out, err)
			}
			if len(o.calls) != 1 || len(o.results) != 1 || o.results[0].OK {
				t.Fatalf("events: %+v %+v", o.calls, o.results)
			}
		})
	}
}

func TestPapersearchHandler(t *testing.T) {
	for _, source := range []string{"", "arxiv"} {
		o := &paperObserver{}
		cfg := &config.UserConfig{PaperSearchSource: "crossref", PaperSearchEmail: "research@example.org", Context: config.ContextConfig{PaperLimit: 1000}}
		payload, _ := json.Marshal(tool.PaperSearchRequest{Query: "  consensus  ", Source: source})
		out, err := runPapersearchWith(context.Background(), cfg, &Runtime{Observer: o}, "find papers", payload,
			func(ctx context.Context, req tool.PaperSearchRequest, email string) (*tool.PaperSearchResult, error) {
				wantSource := source
				if wantSource == "" {
					wantSource = "crossref"
				}
				if req.Source != wantSource || req.Query != "consensus" || req.Limit != 5 || req.Sort != "relevance" || email != cfg.PaperSearchEmail {
					t.Fatalf("req=%+v email=%q", req, email)
				}
				return &tool.PaperSearchResult{Source: req.Source, Query: req.Query, Papers: []tool.Paper{{Title: "Consensus paper", DOI: "10.1234/example", URL: "https://doi.org/10.1234/example", Abstract: "Original abstract"}}}, nil
			})
		if err != nil || !strings.HasPrefix(out, "[papersearch result]") || !strings.Contains(out, "Original abstract") || !strings.Contains(out, "10.1234/example") {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if len(out) > cfg.Context.PaperLimit+len("[papersearch result]\n") {
			t.Fatalf("budget exceeded: %d", len(out))
		}
		if len(o.results) != 1 || !o.results[0].OK || !strings.Contains(o.results[0].Detail, "Consensus paper") || o.calls[0].Reason != "find papers" {
			t.Fatalf("events: %+v %+v", o.calls, o.results)
		}
	}
}

func TestPapersearchFailureIsObservation(t *testing.T) {
	for _, searchErr := range []error{errors.New("provider HTTP 429"), context.Canceled} {
		o := &paperObserver{}
		out, err := runPapersearchWith(context.Background(), &config.UserConfig{}, &Runtime{Observer: o}, "research", json.RawMessage(`{"query":"consensus"}`),
			func(context.Context, tool.PaperSearchRequest, string) (*tool.PaperSearchResult, error) {
				return nil, searchErr
			})
		if err != nil || !strings.HasPrefix(out, "[papersearch error]") || !strings.Contains(out, searchErr.Error()) || o.results[0].OK {
			t.Fatalf("out=%q err=%v events=%+v", out, err, o.results)
		}
	}
}
