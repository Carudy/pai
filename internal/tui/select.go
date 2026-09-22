package tui

import "github.com/charmbracelet/huh"

func GetUserConfirm(prompt string) (bool, error) {
	confirmed := true
	err := huh.NewConfirm().
		Title(prompt).
		Affirmative("Yes").
		Negative("No").
		Value(&confirmed).
		Run()
	if err != nil {
		return false, normalizePromptErr(err)
	}
	return confirmed, nil
}
