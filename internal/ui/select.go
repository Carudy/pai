package ui

import (
	"github.com/charmbracelet/huh"
)

func GetUserConfirm(prompt string) (bool, error) {
	var confirmed bool
	err := huh.NewConfirm().
		Title(prompt).
		Affirmative("Yes").
		Negative("No").
		Value(&confirmed).
		Run()
	return confirmed, err
}
