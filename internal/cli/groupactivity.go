package cli

import (
	"sort"
	"strings"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/core"
	"github.com/Carudy/pai/internal/runner"
	"github.com/Carudy/pai/internal/session"
)

// groupActivityLimit bounds one timeline read. It is a directory view, not an
// archive: the durable events themselves are kept for 30 days.
const groupActivityLimit = 40

// groupActivity merges what a group's members were told with what they reported:
// mail, broadcasts, instructions, escalations, and the inform/done reports they
// wrote back. It lives here rather than in runner because it needs the store's
// two shapes (events by id, turns by name) and the response parser; the runner
// carries only the resulting view.
func groupActivity(store session.Store, members []core.GroupMember, limit int) ([]runner.GroupActivity, error) {
	activity, ok := store.(session.ActivityStore)
	if !ok {
		return nil, nil
	}
	if limit <= 0 {
		limit = groupActivityLimit
	}
	ids := make([]string, 0, len(members))
	names := make([]string, 0, len(members))
	byID := make(map[string]string, len(members))
	for _, member := range members {
		ids = append(ids, member.SessionID)
		names = append(names, member.Name)
		byID[member.SessionID] = member.Name
	}

	out := make([]runner.GroupActivity, 0, limit)
	events, err := activity.GroupEvents(ids, nil, limit)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		out = append(out, runner.GroupActivity{
			At:     ev.At,
			Kind:   ev.Kind,
			From:   strings.TrimPrefix(ev.Source, "session:"),
			To:     byID[ev.Session],
			Trace:  ev.Trace,
			State:  ev.State,
			Detail: clip(strings.TrimSpace(ev.Payload), 200),
		})
	}

	// A report is a turn, not a delivery, so it is read from the transcript. Only
	// the actions that end an activation with something to say are kept: a tool
	// call or a question is not team news.
	turns, err := activity.RecentOutputs(names, limit)
	if err != nil {
		return nil, err
	}
	for _, nt := range turns {
		resp, err := chat.ParseResponse(nt.Turn.Content)
		if err != nil {
			continue
		}
		switch string(resp.Action) {
		case string(chat.ActionInform), string(chat.ActionDone):
		default:
			continue
		}
		out = append(out, runner.GroupActivity{
			At:     nt.Turn.At,
			Kind:   string(resp.Action),
			From:   nt.Name,
			Detail: clip(strings.TrimSpace(resp.GetPayload()), 200),
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
