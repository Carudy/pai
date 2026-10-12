//go:build unix

package role

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/Carudy/pai/internal/config"
)

func TestRunReadRejectsFIFOReplacementDuringApproval(t *testing.T) {
	path := writeTemp(t, "original")
	rt := testEditRuntime(true)
	rt.Prompter = fileApproval{approve: func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}}
	raw, _ := json.Marshal(readPayload{Path: path})
	out, err := runRead(context.Background(), &config.UserConfig{ConfirmRead: true}, rt, "", raw)
	if err == nil || !strings.Contains(err.Error(), "changed during approval") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
