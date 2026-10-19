package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/provider"
)

// trimNoticeBytes is the size below which trailing text is a stray character rather
// than the model running on, so it is not worth a line in the context log.
const trimNoticeBytes = 256

// TrimToAction cuts an assistant response back to the action at its start, returning
// the text to keep and the number of bytes dropped. Anything before the object is
// kept — a model that says "let me check X first" then acts has said something useful
// — and everything after it goes.
//
// The tail is where a long-context model imitates the transcript it was shown: it
// writes more actions, and the `[cmd result]` blocks for them, so the turn ends up
// holding tool output nobody ran. That imitation is only ever replayed as if it were
// real, and it teaches the model to do it again, so it is cut at the point the loop
// stopped reading.
//
// Only a first object that decodes as a known action is trimmed: with several objects
// the accepted one is not necessarily the first, and cutting before it would drop a
// real action from the transcript. Then nothing is trimmed and the turn passes through
// as it always has.
func TrimToAction(content string) (string, int) {
	start := strings.IndexByte(content, '{')
	if start < 0 {
		return content, 0 // prose only; the format retry is what handles that
	}
	end, ok := matchingBrace(content, start)
	if !ok || strings.TrimSpace(content[end+1:]) == "" {
		return content, 0 // truncated, or nothing but whitespace after the object
	}

	var first Response
	if err := json.Unmarshal([]byte(content[start:end+1]), &first); err != nil || !isValidAction(first.Action) {
		return content, 0
	}
	kept := content[:end+1]
	return kept, len(content) - len(kept)
}

// TrimHistory returns a message list in which every assistant turn is cut back to its
// action, and the input unchanged (no copy) when there is nothing to cut.
func TrimHistory(history []provider.Message) []provider.Message {
	out, copied := history, false
	for i, m := range history {
		if m.Role != provider.RoleAssistant || (m.Kind != "" && m.Kind != core.KindOutput) {
			continue
		}
		kept, dropped := TrimToAction(m.Content)
		if dropped == 0 {
			continue
		}
		if !copied {
			out = make([]provider.Message, len(history))
			copy(out, history)
			copied = true
		}
		out[i].Content = kept
	}
	return out
}

// DescribeTrim reports what TrimToAction drops from an accepted response, for the
// context log: a turn that over-ran is worth saying out loud, so it is not discovered
// later in the inspector as a mystery. It returns "" when nothing worth reporting was
// dropped.
func DescribeTrim(content string) string {
	kept, dropped := TrimToAction(content)
	if dropped < trimNoticeBytes {
		return ""
	}
	further := len(jsonCandidates(content[len(kept):]))
	return fmt.Sprintf("context: dropped %d bytes the model wrote past its action (%d further action(s) imitated); only the accepted action is replayed",
		dropped, further)
}
