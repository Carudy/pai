package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPapersearchConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.PaperSearchSource != "arxiv" || cfg.PaperSearchEmail != "" || cfg.Context.PaperLimit != 16384 {
		t.Fatalf("defaults: %+v", cfg)
	}
	var raw tomlConfig
	if _, err := toml.Decode(`[tool]
papersearch_source = " CROSSREF "
papersearch_email = " research@example.org "
[context]
paper_limit = 24000
`, &raw); err != nil {
		t.Fatal(err)
	}
	cfg.fromTOML(&raw)
	if cfg.PaperSearchSource != "crossref" || cfg.PaperSearchEmail != "research@example.org" || cfg.Context.PaperLimit != 24000 {
		t.Fatalf("loaded: %+v", cfg)
	}
}

func TestPapersearchTemplateMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[tool]\n[context]\nsearch_limit = 4000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeTemplate(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`# papersearch_source = "arxiv"`, `# papersearch_email = "you@example.org"`, `# paper_limit = 16384`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing commented option %q", want)
		}
	}
	var raw tomlConfig
	if err := loadTOML(path, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Tool.PaperSearchSource != "" || raw.Tool.PaperSearchEmail != "" || raw.Context.PaperLimit != 0 || raw.Context.SearchLimit != 4000 {
		t.Fatalf("merge changed active values: %+v", raw)
	}
	if added, err := MergeTemplate(path); err != nil || added != 0 {
		t.Fatalf("second merge: %d %v", added, err)
	}
}
