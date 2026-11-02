package cli

import (
	"context"
	"errors"
	"time"

	"github.com/Carudy/pai/internal/conn"
	"github.com/Carudy/pai/internal/session"
	"github.com/Carudy/pai/internal/tui"
)

// readSource picks where a read-only command gets its data. The store is
// exclusive, so while `pai serve` runs the local open fails and the only correct
// view is the server's. A command that can render both paths calls this, then
// branches on whether a client came back.
//
// ok=false means the error was already reported: either a real open failure, or
// a locked store with no reachable server.
func readSource(log *tui.Logger) (session.Store, *conn.Client, bool) {
	store, err := session.Open()
	if err == nil {
		return store, nil, true
	}
	if !errors.Is(err, session.ErrLocked) {
		log.Errorf("Error opening sessions: %v\n", err)
		return nil, nil, false
	}
	if client, ok := remoteReader(); ok {
		return nil, client, true
	}
	log.Errorf("Error: the session store is owned by a running `pai serve`, and no server is reachable.\n" +
		"Set conn.url and conn.token (or PAI_SERVE_URL/PAI_SERVE_TOKEN), or stop the server.\n")
	return nil, nil, false
}

// remoteReader builds a client for a running server, or reports that none is
// reachable. The target resolves exactly as `pai conn` does: flags (none here),
// then PAI_SERVE_*, then config.toml.
func remoteReader() (*conn.Client, bool) {
	baseURL, token, origin, err := connTarget("", "", "")
	if err != nil {
		return nil, false
	}
	client := conn.New(baseURL, token)
	if origin != "" {
		client.Origin = origin
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		return nil, false
	}
	return client, true
}
