// Package paths resolves PAI's on-disk directories.
//
// It is a leaf package (no internal dependencies) so any other package can
// depend on it without risking an import cycle.
package paths

import (
	"os"
	"path/filepath"
)

const appName = "pai"

// Home returns the user's home directory, or "" if it cannot be determined.
func Home() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// ConfigDir returns PAI's config directory: $XDG_CONFIG_HOME/pai, or
// ~/.config/pai when XDG_CONFIG_HOME is unset. Returns "" if neither is
// available.
func ConfigDir() string {
	base := configHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, appName)
}

// DataDir returns PAI's data directory: $XDG_DATA_HOME/pai, or
// ~/.local/share/pai when XDG_DATA_HOME is unset. Returns "" if neither is
// available.
func DataDir() string {
	base := dataHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, appName)
}

func configHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	home := Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home := Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}
