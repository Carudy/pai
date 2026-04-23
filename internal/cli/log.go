package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

var DebugStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), false, false, false, true).
	BorderForeground(lipgloss.Color("13")). // Purple border
	PaddingLeft(2).
	Foreground(lipgloss.Color("245")) // Dim gray text

var NormalStyle = lipgloss.NewStyle().
	PaddingLeft(0)

var ErrorStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), false, false, false, true).
	BorderForeground(lipgloss.Color("1")). // Red border
	PaddingLeft(0).
	Foreground(lipgloss.Color("1")) // Red text

var SuccessStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), false, false, false, true).
	BorderForeground(lipgloss.Color("2")). // Green border
	PaddingLeft(0).
	Foreground(lipgloss.Color("2")) // Green text

var InfoStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), false, false, false, true).
	BorderForeground(lipgloss.Color("4")). // Blue border
	PaddingLeft(0).
	Foreground(lipgloss.Color("4")) // Blue text

var WarnStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder(), false, false, false, true).
	BorderForeground(lipgloss.Color("3")). // Yellow border
	PaddingLeft(0).
	Foreground(lipgloss.Color("3")) // Yellow text

func DebugLog(msg string) {
	if !Flags.Debug {
		return
	}
	fmt.Println(DebugStyle.Render("[DEBUG]: " + msg))
}

func NormalLog(msg string) {
	fmt.Println(NormalStyle.Render(msg))
}

func ErrorLog(msg string) {
	fmt.Println(ErrorStyle.Render("Error " + msg))
}

func SuccessLog(msg string) {
	fmt.Println(SuccessStyle.Render("✅ " + msg))
}

func InfoLog(msg string) {
	fmt.Println(InfoStyle.Render(msg))
}

func WarnLog(msg string) {
	fmt.Println(WarnStyle.Render(msg))
}
