package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// Message.Kind is PAI-internal bookkeeping for context compaction. If it ever
// leaked into the request body, providers would reject the unknown field.
func TestMessageKindIsNotSerialized(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleUser, Content: "hi", Kind: "tool_result"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"role":"user","content":"hi"}`; got != want {
		t.Errorf("serialized message = %s, want %s", got, want)
	}
	if strings.Contains(string(b), "tool_result") {
		t.Errorf("Kind leaked onto the wire: %s", b)
	}
}
