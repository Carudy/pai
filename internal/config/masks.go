package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

var MaskDB map[string]string

func LoadMasksFromYAML(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	var raw struct {
		Mask map[string]any `yaml:"mask"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing yaml: %w", err)
	}

	masks := make(map[string]string, len(raw.Mask))
	for k, v := range raw.Mask {
		masks[fmt.Sprintf("<mask:%s>", k)] = fmt.Sprintf("%v", v)
	}
	return masks, nil
}

func init() {
	mask_dir, err := configDir()
	if err != nil {
		fmt.Printf("failed to get config dir: %v\n", err)
		return
	}
	MaskDB, _ = LoadMasksFromYAML(filepath.Join(mask_dir, "mask.yml"))
}
