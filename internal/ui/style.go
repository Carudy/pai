package ui

import (
	"charm.land/lipgloss/v2"
)

var Styles = map[string]lipgloss.Style{
	"Title":  lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.BrightCyan).Bold(true),
	"Cmd":    lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Yellow).Bold(true),
	"Choice": lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Magenta),
	"Debug": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("245")),
	"Error": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Red).Bold(true),
	"Success": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Green),
	"Info": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("39")),
	"Warn": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("214")),
	"Help": lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Color("240")).Italic(true),
	"Subdued": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("#ABFAE1")),
	"Separator": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("236")),
	"Speaker": lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color("#C2B9B6")).Background(lipgloss.Color("#3B416F")).
		Padding(0, 0),
	"Content": lipgloss.NewStyle().
		PaddingLeft(0).Foreground(lipgloss.Color("#ddeeff")),
	"ExeAsk": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("#EF65AB")),
	"ExeRes": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("#C1E1C1")),

	// Label tags — used as emoji-free [TAG] prefixes to mark output origin
	"TagSystem": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("243")),
	"TagAgent": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("78")).Bold(true),
	"TagExec": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("#05AB6A")).Bold(true),
	"TagUser": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("147")).Bold(true),
	"TagResult": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("222")).Bold(true),

	// Reasoning style for thinking/reasoning content (warm amber, italic)
	"Reasoning": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("172")).
		Italic(true),
	"Thinking": lipgloss.NewStyle().
		PaddingLeft(0).
		Foreground(lipgloss.Color("#8E857A")).
		Italic(true),
}
