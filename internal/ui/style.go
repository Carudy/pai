package ui

import (
	"charm.land/lipgloss/v2"
)

var Styles = map[string]lipgloss.Style{
	"Title":  lipgloss.NewStyle().Foreground(lipgloss.BrightCyan).Bold(true),
	"Cmd":    lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true),
	"Choice": lipgloss.NewStyle().Foreground(lipgloss.Magenta),
	"Debug": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("245")),
	"Error": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Red),
	"Success": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Green),
	"Info": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Blue),
	"Warn": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.BrightMagenta).
		Italic(true).Bold(true),
	"Help": lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true),
	"Speaker": lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color("#C2B9B6")).Background(lipgloss.Color("#3B416F")).
		Padding(0, 0),
	"Content": lipgloss.NewStyle().
		PaddingLeft(0).Foreground(lipgloss.Color("#ddeeff")),
}
