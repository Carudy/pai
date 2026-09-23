package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/tool"
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
	o.pair("TagAgent", "[PAI 💬]", "Info", "[Awaiting for new instructions.]")
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

// Output prints informational text from a command, without a tag.
func (o *LineObserver) Output(text string) {
	o.closeReasoning()
	fmt.Fprintf(o.W, "%s\n", RenderStr("Content", text))
}

// Session reports a change to the conversation's storage.
func (o *LineObserver) Session(name string) {
	o.closeReasoning()
	o.pair("TagSystem", "[SYS] 🗂", "Info", "session: "+SessionLabel(name))
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
		o.command("TagExec", fmt.Sprintf("[CMD 💻 %s]", c.Target), c.Detail)
	case "remote":
		o.pair("TagAgent", "[RMT 💬]", "Help", c.Reason)
		o.command("TagExec", fmt.Sprintf("[RMT 💻 @%s]", c.Target), c.Detail)
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

// maxDisplaySegments bounds how much of a very long chain is echoed; the rest is
// summarised rather than flooding the transcript.
const maxDisplaySegments = 12

// command prints a tool's command. A chained command ("aa && bb | cc") is broken
// at its operators and numbered: one long wrapped line is hard to read, and the
// operators decide what still runs if an earlier command fails. Each command is
// printed verbatim and syntax-highlighted.
//
// The split comes from tool.SplitSegments, the same parser the trust check uses,
// so the count shown here can never disagree with what trust saw.
func (o *LineObserver) command(tag, label, cmd string) {
	segs := tool.SplitSegments(cmd)
	if len(segs) <= 1 {
		fmt.Fprintf(o.W, "%s %s\n", RenderStr(tag, label), highlightCommand(cmd))
		return
	}

	fmt.Fprintf(o.W, "%s %s\n", RenderStr(tag, label),
		RenderStr("Info", fmt.Sprintf("%d commands:", len(segs))))
	for i, seg := range segs {
		if i == maxDisplaySegments {
			fmt.Fprintf(o.W, "%s\n", RenderStr("Help", fmt.Sprintf("        … +%d more commands", len(segs)-i)))
			break
		}
		fmt.Fprintf(o.W, "%s %s\n", RenderStr("Help", fmt.Sprintf("  %2d", i+1)), highlightCommand(seg.Src))
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
