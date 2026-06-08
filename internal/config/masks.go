package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

var MaskDB map[string]string

func LoadMasks(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	var raw struct {
		Mask map[string]any `toml:"mask"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing toml: %w", err)
	}

	masks := make(map[string]string, len(raw.Mask))
	for k, v := range raw.Mask {
		masks[fmt.Sprintf("<mask:%s>", k)] = fmt.Sprintf("%v", v)
	}
	return masks, nil
}

func init() {
	maskDir, err := ConfigDir()
	if err != nil {
		fmt.Printf("failed to get config dir: %v\n", err)
		return
	}
	MaskDB, _ = LoadMasks(filepath.Join(maskDir, "mask.toml"))
}
