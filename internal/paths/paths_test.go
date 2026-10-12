package paths

import (
	"path/filepath"
	"testing"
)

// abs builds a platform-absolute path from slash-separated elements so the
// expectations read the same on every OS the project targets.
func abs(elem ...string) string {
	return filepath.FromSlash(filepath.Join(append([]string{"/"}, elem...)...))
}

// Every case sets both HOME and the relevant XDG variable so the real user
// home is never consulted. An empty string stands in for "unset", which is
// what os.Getenv/os.UserHomeDir treat as absent.

func TestConfigDir(t *testing.T) {
	home := abs("home", "user")

	tests := []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{
			name: "absolute XDG_CONFIG_HOME wins and gets app subdir",
			xdg:  abs("xdg", "config"),
			home: home,
			want: abs("xdg", "config", appName),
		},
		{
			name: "XDG_CONFIG_HOME takes precedence over HOME",
			xdg:  abs("custom", "cfg"),
			home: abs("other", "home"),
			want: abs("custom", "cfg", appName),
		},
		{
			// Documents observed behaviour: the package does NOT reject a
			// relative XDG_CONFIG_HOME (no filepath.IsAbs check), contrary to
			// the XDG spec. See the report note.
			name: "relative XDG_CONFIG_HOME is used as-is, not ignored",
			xdg:  filepath.FromSlash("relative/config"),
			home: home,
			want: filepath.FromSlash("relative/config/" + appName),
		},
		{
			name: "XDG_CONFIG_HOME unset falls back to HOME/.config",
			xdg:  "",
			home: home,
			want: abs("home", "user", ".config", appName),
		},
		{
			name: "XDG_CONFIG_HOME set with HOME empty still works",
			xdg:  abs("xdg", "config"),
			home: "",
			want: abs("xdg", "config", appName),
		},
		{
			name: "both unset returns empty",
			xdg:  "",
			home: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)

			if got := ConfigDir(); got != tt.want {
				t.Errorf("ConfigDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataDir(t *testing.T) {
	home := abs("home", "user")

	tests := []struct {
		name string
		xdg  string
		home string
		want string
	}{
		{
			name: "absolute XDG_DATA_HOME wins and gets app subdir",
			xdg:  abs("xdg", "data"),
			home: home,
			want: abs("xdg", "data", appName),
		},
		{
			name: "XDG_DATA_HOME takes precedence over HOME",
			xdg:  abs("custom", "data"),
			home: abs("other", "home"),
			want: abs("custom", "data", appName),
		},
		{
			name: "relative XDG_DATA_HOME is used as-is, not ignored",
			xdg:  filepath.FromSlash("relative/data"),
			home: home,
			want: filepath.FromSlash("relative/data/" + appName),
		},
		{
			name: "XDG_DATA_HOME unset falls back to HOME/.local/share",
			xdg:  "",
			home: home,
			want: abs("home", "user", ".local", "share", appName),
		},
		{
			name: "XDG_DATA_HOME set with HOME empty still works",
			xdg:  abs("xdg", "data"),
			home: "",
			want: abs("xdg", "data", appName),
		},
		{
			name: "both unset returns empty",
			xdg:  "",
			home: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)

			if got := DataDir(); got != tt.want {
				t.Errorf("DataDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHome(t *testing.T) {
	tests := []struct {
		name string
		home string
		want string
	}{
		{
			name: "reports the resolved home from HOME",
			home: abs("custom", "home", "dir"),
			want: abs("custom", "home", "dir"),
		},
		{
			name: "relative HOME is returned verbatim",
			home: filepath.FromSlash("not/absolute"),
			want: filepath.FromSlash("not/absolute"),
		},
		{
			name: "empty HOME returns empty (UserHomeDir errors)",
			home: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", tt.home)

			if got := Home(); got != tt.want {
				t.Errorf("Home() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHomeFeedsFallback proves Home() and the fallback path agree: the
// fallback is exactly the resolved home plus the XDG default subdirectory.
func TestHomeFeedsFallback(t *testing.T) {
	t.Setenv("HOME", abs("home", "user"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	home := Home()
	if want := abs("home", "user"); home != want {
		t.Fatalf("Home() = %q, want %q", home, want)
	}
	if got, want := ConfigDir(), filepath.Join(home, ".config", appName); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
	if got, want := DataDir(), filepath.Join(home, ".local", "share", appName); got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
}
