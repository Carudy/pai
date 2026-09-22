package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/core"
)

// LineObserver renders core events as styled lines to a writer. It is the
// line-mode adapter for core.Observer.
type LineObserver struct {
	W           io.Writer
	inReasoning bool
}

// NewLineObserver returns a line-mode observer writing to w.
func NewLineObserver(w io.Writer) *LineObserver { return &LineObserver{W: w} }

func (o *LineObserver) Reason(text string)      { o.pair("TagAgent", "[PAI 🤖]", "Info", text) }
func (o *LineObserver) Done(summary string)     { o.pair("TagAgent", "[PAI ✅]", "Success", summary) }
func (o *LineObserver) Terminate(reason string) { o.pair("TagAgent", "[PAI 💔]", "Warn", reason) }
func (o *LineObserver) Ask(question string)     { o.pair("TagAgent", "[PAI 🙋]", "Warn", question) }
func (o *LineObserver) User(text string)        { o.pair("TagUser", "[User]", "Info", text) }

func (o *LineObserver) Awaiting() {
	o.closeReasoning()
	o.pair("TagAgent", "[PAI]", "Info", "[Awaiting for new instructions.]")
}

func (o *LineObserver) Usage(u core.Usage) {
	o.closeReasoning()
	fmt.Fprintf(o.W, "%s\n", RenderStr("Token", fmt.Sprintf("[token: %s in, %s out, %s total]",
		humanCount(u.Prompt), humanCount(u.Completion), humanCount(u.Total))))
}

func (o *LineObserver) Notice(text string) {
	o.closeReasoning()
	o.pair("TagSystem", "[SYS] ⚠️", "Warn", text)
}

func (o *LineObserver) Separator() {
	o.closeReasoning()
	fmt.Fprintf(o.W, "%s\n", Styles["Separator"].Render(strings.Repeat("─", 40)))
}

// Reasoning streams thinking tokens, opening a reasoning block on the first one
// and closing it when the next event arrives.
func (o *LineObserver) Reasoning(delta string) {
	if !o.inReasoning {
		fmt.Fprintf(o.W, "\n%s ", RenderStr("Reasoning", "[PAI 🤔]"))
		o.inReasoning = true
	}
	fmt.Fprint(o.W, Styles["Thinking"].Render(delta))
}

func (o *LineObserver) ToolCall(c core.ToolCall) {
	o.closeReasoning()
	switch c.Name {
	case "execute":
		o.pair("TagAgent", "[CMD 💬]", "Help", c.Reason)
		o.pair("TagExec", fmt.Sprintf("[CMD 💻 %s]", c.Target), "Info", c.Detail)
	case "remote":
		o.pair("TagAgent", "[RMT 💬]", "Help", c.Reason)
		o.pair("TagExec", fmt.Sprintf("[RMT 💻 @%s]", c.Target), "Info", c.Detail)
	case "websearch":
		o.pair("TagAgent", "[WEB 🔍]", "Help", c.Reason)
		o.pair("TagExec", "[WEB]", "Info", c.Detail)
	default:
		o.pair("TagAgent", "[TOOL 💬]", "Help", c.Reason)
		o.pair("TagExec", fmt.Sprintf("[TOOL %s]", c.Name), "Info", c.Detail)
	}
	if c.Trusted {
		fmt.Fprintf(o.W, "%s\n", RenderStr("Trusted", "  ⚡ executing trusted command"))
	}
}

// ToolOutput writes streamed command output verbatim.
func (o *LineObserver) ToolOutput(chunk string) {
	o.closeReasoning()
	fmt.Fprint(o.W, chunk)
}

func (o *LineObserver) ToolResult(r core.ToolResult) {
	o.closeReasoning()
	switch {
	case r.Skipped:
		o.pair("TagSystem", "[SYS]", "Subdued", "Skipped")
	case r.OK:
		o.pair("TagSystem", "[SYS]", "Success", r.Message)
	default:
		o.pair("TagSystem", "[SYS] ❌", "Warn", r.Message)
	}
	if r.Detail != "" {
		fmt.Fprintf(o.W, "%s\n", RenderStr("Content", r.Detail))
	}
}

func (o *LineObserver) closeReasoning() {
	if o.inReasoning {
		fmt.Fprintf(o.W, "\n%s\n", Styles["Separator"].Render(strings.Repeat("─", 40)))
		o.inReasoning = false
	}
}

// pair prints two styled segments on one line: "<a> <b>".
func (o *LineObserver) pair(styleA, textA, styleB, textB string) {
	fmt.Fprintf(o.W, "%s %s\n", RenderStr(styleA, textA), RenderStr(styleB, textB))
}

// humanCount renders token counts compactly (e.g. 870, 1.3K).
func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
