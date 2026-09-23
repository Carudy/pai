package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The starter config has to be real config.toml: valid TOML that parses into the
// same shape the loader reads, or `pai config init` would hand a new user a file
// pai cannot load.
func TestTemplateParsesAndApplies(t *testing.T) {
	var raw tomlConfig
	if _, err := toml.Decode(Template(), &raw); err != nil {
		t.Fatalf("template is not valid TOML: %v", err)
	}

	cfg := defaultConfig()
	cfg.fromTOML(&raw)

	if cfg.DefaultModel == "" || cfg.DefaultRole == "" {
		t.Errorf("template left model/role empty: %q %q", cfg.DefaultModel, cfg.DefaultRole)
	}
	if cfg.Context.ExecLimit <= 0 || cfg.Context.HeadLines <= 0 || cfg.Context.KeepTurns <= 0 {
		t.Errorf("context settings did not apply: %+v", cfg.Context)
	}
}

func TestWriteTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")

	existed, err := WriteTemplate(path, false)
	if err != nil || existed {
		t.Fatalf("first write: existed=%v err=%v", existed, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not written: %v", err)
	}

	// Without overwrite, an existing file is reported and left alone.
	if err := os.WriteFile(path, []byte("custom = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	existed, err = WriteTemplate(path, false)
	if err != nil || !existed {
		t.Fatalf("second write: existed=%v err=%v", existed, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "custom = true\n" {
		t.Errorf("file was clobbered: %q", data)
	}
}

func TestTemplateParsing(t *testing.T) {
	secs := templateSections()
	byName := map[string]templateSection{}
	for _, s := range secs {
		byName[s.name] = s
	}

	app, ok := byName["app"]
	if !ok {
		t.Fatal("no [app] section parsed from the template")
	}
	values := map[string]string{}
	for _, k := range app.keys {
		values[k.key] = k.value
	}
	// A trailing comment must not leak into the value, and a quoted value keeps
	// its quotes.
	if values["default_role"] != `"devops"` {
		t.Errorf("default_role = %q", values["default_role"])
	}
	if values["streaming"] != "true" {
		t.Errorf("streaming = %q", values["streaming"])
	}
	// Commented-out settings are template keys too, flagged as examples so a merge
	// adds them commented rather than turning them on.
	rk, ok := findKey(app.keys, "reasoning")
	if !ok {
		t.Error("commented reasoning should be a template key")
	} else if !rk.commented {
		t.Error("reasoning should be flagged commented")
	} else if len(rk.insert) == 0 {
		t.Error("a commented key should carry the block to insert")
	}
}

func findKey(keys []templateKey, key string) (templateKey, bool) {
	for _, k := range keys {
		if k.key == key {
			return k, true
		}
	}
	return templateKey{}, false
}

// A merge surfaces optional settings an older config predates — as comments, so
// the upgrade adds no behaviour and no active value.
func TestMergeTemplateAddsCommentedExamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[app]\ndefault_role = \"coder\"\n\n[tool]\ntrusted_cmds = [\"ls\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := MergeTemplate(path); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{
		`# trusted_paths = ["~/work/myproject"]`,
		"# confirm_read = false",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("merge did not surface %q:\n%s", want, text)
		}
	}

	// The examples must stay inactive: parsing the merged file leaves the new
	// settings at their defaults.
	var raw tomlConfig
	if err := loadTOML(path, &raw); err != nil {
		t.Fatalf("merged file is not valid TOML: %v", err)
	}
	if len(raw.Tool.TrustedPaths) != 0 || raw.Tool.ConfirmRead {
		t.Errorf("commented examples became active: %+v", raw.Tool)
	}

	// And it is idempotent: the examples count as present next time.
	again, err := MergeTemplate(path)
	if err != nil || again != 0 {
		t.Errorf("second merge added %d (err %v), want 0", again, err)
	}
}

func TestMergeTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "# keep me\n[app]\ndefault_role = \"coder\"\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	added, err := MergeTemplate(path)
	if err != nil {
		t.Fatal(err)
	}
	if added == 0 {
		t.Fatal("expected settings to be added")
	}

	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "# keep me") {
		t.Error("merge dropped an existing comment")
	}
	if !strings.Contains(text, `default_role = "coder"`) {
		t.Error("merge overwrote an existing value")
	}
	if !strings.Contains(text, "[context]") || !strings.Contains(text, "keep_turns") {
		t.Errorf("merge did not add the missing [context] section:\n%s", text)
	}

	var raw tomlConfig
	if _, err := toml.Decode(text, &raw); err != nil {
		t.Fatalf("merged file is not valid TOML: %v", err)
	}
	if raw.App.DefaultRole != "coder" {
		t.Errorf("default_role = %q, want coder", raw.App.DefaultRole)
	}
	if raw.Context.KeepTurns != 8 {
		t.Errorf("keep_turns = %d, want the default 8", raw.Context.KeepTurns)
	}

	// Merging again is a no-op.
	again, err := MergeTemplate(path)
	if err != nil || again != 0 {
		t.Errorf("second merge added %d (err %v), want 0", again, err)
	}
}

// A missing key inside an existing section is filled in; present keys are kept.
func TestMergeTemplateFillsSectionGaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[context]\nexec_limit = 999\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := MergeTemplate(path); err != nil {
		t.Fatal(err)
	}

	var raw tomlConfig
	if err := loadTOML(path, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Context.ExecLimit != 999 {
		t.Errorf("exec_limit = %d, want the existing 999", raw.Context.ExecLimit)
	}
	if raw.Context.TailLines != 40 {
		t.Errorf("tail_lines = %d, want the added default 40", raw.Context.TailLines)
	}
}

// A reset goes back to defaults but must never destroy credentials or the
// hand-edited trusted_cmds.
func TestResetTemplatePreservesSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `[app]
default_role = "coder"

[providers.deepseek]
api_key = "sk-secret"
base_url = "https://example.test/v1"

[tool]
tavily_api_key = "tv-secret"
trusted_cmds = ["ls", "git status"]
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ResetTemplate(path); err != nil {
		t.Fatal(err)
	}

	var raw tomlConfig
	if err := loadTOML(path, &raw); err != nil {
		t.Fatalf("reset file is not valid TOML: %v", err)
	}

	if raw.Providers["deepseek"].APIKey != "sk-secret" {
		t.Errorf("api_key = %q, want it preserved", raw.Providers["deepseek"].APIKey)
	}
	if raw.Providers["deepseek"].BaseURL != "https://example.test/v1" {
		t.Errorf("base_url = %q, want it preserved", raw.Providers["deepseek"].BaseURL)
	}
	if raw.Tool.TavilyAPIKey != "tv-secret" {
		t.Errorf("tavily_api_key = %q, want it preserved", raw.Tool.TavilyAPIKey)
	}
	if len(raw.Tool.TrustedCmds) != 2 || raw.Tool.TrustedCmds[0] != "ls" {
		t.Errorf("trusted_cmds = %v, want it preserved", raw.Tool.TrustedCmds)
	}
	// Everything else returns to the template's default.
	if raw.App.DefaultRole != "devops" {
		t.Errorf("default_role = %q, want the reset default devops", raw.App.DefaultRole)
	}
}
