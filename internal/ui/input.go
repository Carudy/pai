package ui

import (
	"strings"

	"github.com/charmbracelet/huh"
)

func GetUserTextInput(prompt string) (string, error) {
	var value string
	err := huh.NewInput().
		Title(prompt).
		Value(&value).
		Validate(func(s string) error { return nil }).
		Run()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
