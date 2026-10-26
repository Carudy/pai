package role

import (
	"context"
	"fmt"

	"github.com/Carudy/pai/internal/chat"
	"github.com/Carudy/pai/internal/config"
	"github.com/Carudy/pai/internal/provider"
)

// conversation is the mutable state of one running conversation: the role
// prompt in force, the turns sent to the model, and where those turns are
// persisted. Both drivers own one and hand it to activate; in-session commands
// are only a second mutator of it, which is why this type lives here rather
// than in command.go — /role and /new swap rp and clear history underneath the
// loop, and the loop re-reads them each activation.
type conversation struct {
	rt      *Runtime
	cfg     *config.UserConfig
	rp      *chat.RolePrompt
	history []provider.Message
	session string          // "" = ephemeral
	ctx     context.Context // the run's context, for commands that call the model
}

func (c *conversation) output(text string) { c.rt.Observer.Output(text) }

func (c *conversation) notice(format string, a ...any) {
	c.rt.Observer.Notice(fmt.Sprintf(format, a...))
}

// endsWithUser reports whether the composed conversation's last message awaits
// a reply from the model.
func (c *conversation) endsWithUser() bool {
	n := len(c.history)
	return n > 0 && c.history[n-1].Role == provider.RoleUser
}
