package prompts

import "testing"

// A session tool is one the host adds for a placement, not a role. It must still
// be attributable, and a role that already declares it must win.
func TestSessionToolsCarryTheirSource(t *testing.T) {
	set, err := ResolveTools(ResolveOptions{
		RoleTools:     []string{"read"},
		SessionTools:  []string{"mail"},
		SessionSource: "group:hydro",
	})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := set.Get("mail")
	if !ok {
		t.Fatal("mail was not resolved from the session layer")
	}
	if tool.Layer != LayerSession || tool.Source != "group:hydro" {
		t.Fatalf("mail = layer %q source %q, want session/group:hydro", tool.Layer, tool.Source)
	}
	if set.Tools[0].Name != "read" || set.Tools[len(set.Tools)-1].Name != "mail" {
		t.Fatalf("order = %v, want role tools then session tools", names(set.Tools))
	}

	// A role that declares mail keeps its own provenance and is not duplicated.
	set, err = ResolveTools(ResolveOptions{
		RoleTools:     []string{"mail"},
		SessionTools:  []string{"mail"},
		SessionSource: "group:hydro",
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tl := range set.Tools {
		if tl.Name == "mail" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("mail appears %d times", count)
	}
	if got, _ := set.Get("mail"); got.Layer != LayerBuiltin {
		t.Fatalf("the role's own declaration was overridden: %q", got.Layer)
	}
}

func names(tools []ResolvedTool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
