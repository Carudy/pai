package ui

import (
	"charm.land/lipgloss/v2"
)

var Styles = struct {
	Title   lipgloss.Style
	Cmd     lipgloss.Style
	Debug   lipgloss.Style
	Normal  lipgloss.Style
	Error   lipgloss.Style
	Choice  lipgloss.Style
	Success lipgloss.Style
	Info    lipgloss.Style
	Warn    lipgloss.Style
}{
	Title:  lipgloss.NewStyle().Foreground(lipgloss.BrightCyan).Bold(true),
	Cmd:    lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true),
	Choice: lipgloss.NewStyle().Foreground(lipgloss.Magenta),
	Debug: lipgloss.NewStyle().
		PaddingLeft(2).
		Foreground(lipgloss.Color("245")),
	Error: lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Red),
	Success: lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Green),
	Info: lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Blue),
	Warn: lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.BrightMagenta).
		Italic(true).Bold(true),
}
