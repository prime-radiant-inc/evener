package main

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// theme holds all color tokens for one mode (dark or light).
type colorTheme struct {
	// Chrome / background
	statusBarBg lipgloss.Color
	statusBarFg lipgloss.Color
	viewportBg  lipgloss.Color
	inputBg     lipgloss.Color

	// Status indicators
	connected    lipgloss.Color
	disconnected lipgloss.Color

	// Message kinds
	userBlockBg   lipgloss.Color
	userBlockFg   lipgloss.Color
	thinkingFg    lipgloss.Color
	communicateFg lipgloss.Color
	systemFg      lipgloss.Color

	// Tool calls
	toolFg       lipgloss.Color
	toolBorderFg lipgloss.Color
	toolNameFg   lipgloss.Color
	toolDurFg    lipgloss.Color

	// Input / borders
	inputBorderFg lipgloss.Color
	inputPromptFg lipgloss.Color
	inputFg       lipgloss.Color

	// Pickers
	pickerTitleFg    lipgloss.Color
	pickerSelectedFg lipgloss.Color
	pickerNormalFg   lipgloss.Color
	pickerDimFg      lipgloss.Color
	pickerActiveFg   lipgloss.Color
}

var darkTheme = colorTheme{
	statusBarBg: lipgloss.Color("235"),
	statusBarFg: lipgloss.Color("252"),
	viewportBg:  lipgloss.Color("233"),
	inputBg:     lipgloss.Color("233"),

	connected:    lipgloss.Color("42"),
	disconnected: lipgloss.Color("196"),

	userBlockBg:   lipgloss.Color("236"),
	userBlockFg:   lipgloss.Color("252"),
	thinkingFg:    lipgloss.Color("244"),
	communicateFg: lipgloss.Color("255"),
	systemFg:      lipgloss.Color("240"),

	toolFg:       lipgloss.Color("244"),
	toolBorderFg: lipgloss.Color("238"),
	toolNameFg:   lipgloss.Color("179"),
	toolDurFg:    lipgloss.Color("240"),

	inputBorderFg: lipgloss.Color("238"),
	inputPromptFg: lipgloss.Color("42"),
	inputFg:       lipgloss.Color("252"),

	pickerTitleFg:    lipgloss.Color("42"),
	pickerSelectedFg: lipgloss.Color("42"),
	pickerNormalFg:   lipgloss.Color("252"),
	pickerDimFg:      lipgloss.Color("240"),
	pickerActiveFg:   lipgloss.Color("240"),
}

var lightTheme = colorTheme{
	statusBarBg: lipgloss.Color("254"),
	statusBarFg: lipgloss.Color("236"),
	viewportBg:  lipgloss.Color("231"),
	inputBg:     lipgloss.Color("231"),

	connected:    lipgloss.Color("28"),
	disconnected: lipgloss.Color("160"),

	userBlockBg:   lipgloss.Color("253"),
	userBlockFg:   lipgloss.Color("236"),
	thinkingFg:    lipgloss.Color("244"),
	communicateFg: lipgloss.Color("236"),
	systemFg:      lipgloss.Color("244"),

	toolFg:       lipgloss.Color("243"),
	toolBorderFg: lipgloss.Color("250"),
	toolNameFg:   lipgloss.Color("130"),
	toolDurFg:    lipgloss.Color("246"),

	inputBorderFg: lipgloss.Color("250"),
	inputPromptFg: lipgloss.Color("28"),
	inputFg:       lipgloss.Color("236"),

	pickerTitleFg:    lipgloss.Color("28"),
	pickerSelectedFg: lipgloss.Color("28"),
	pickerNormalFg:   lipgloss.Color("236"),
	pickerDimFg:      lipgloss.Color("244"),
	pickerActiveFg:   lipgloss.Color("244"),
}

// activeTheme is set once at startup by initTheme().
var activeTheme colorTheme

// activeThemeName tracks the name of the active theme ("dark" or "light").
var activeThemeName string

// Derived style vars — re-initialized by initTheme().
var (
	statusBarStyle     lipgloss.Style
	statusConnected    lipgloss.Style
	statusDisconnected lipgloss.Style
	scrollModeStyle    lipgloss.Style

	userBlockStyle    lipgloss.Style
	thinkingStyle     lipgloss.Style
	communicateStyle  lipgloss.Style
	systemStyle       lipgloss.Style

	toolCollapsedStyle lipgloss.Style
	toolExpandedStyle  lipgloss.Style
	toolNameStyle      lipgloss.Style
	toolDurationStyle  lipgloss.Style

	inputBorderStyle lipgloss.Style
	inputPromptStyle lipgloss.Style

	viewportStyle lipgloss.Style

	pickerTitle    lipgloss.Style
	pickerSelected lipgloss.Style
	pickerNormal   lipgloss.Style
	pickerDim      lipgloss.Style

	// model_picker specific
	mpTitleStyle  lipgloss.Style
	mpFilterStyle lipgloss.Style
	mpActiveStyle lipgloss.Style
	mpNormalStyle lipgloss.Style
	mpCursorStyle lipgloss.Style
	mpDimStyle    lipgloss.Style
	mpActiveTag   lipgloss.Style
)

// initTheme detects the terminal background and populates all style vars.
// Call once at program startup before creating the bubbletea program.
func initTheme() {
	if termenv.HasDarkBackground() {
		activeTheme = darkTheme
		activeThemeName = "dark"
	} else {
		activeTheme = lightTheme
		activeThemeName = "light"
	}
	applyTheme(activeTheme)
}

// setTheme switches to the named theme ("dark" or "light"). Returns false if
// the name is unrecognised.
func setTheme(name string) bool {
	switch name {
	case "dark":
		activeTheme = darkTheme
		activeThemeName = "dark"
	case "light":
		activeTheme = lightTheme
		activeThemeName = "light"
	default:
		return false
	}
	applyTheme(activeTheme)
	return true
}

// currentThemeName returns the name of the active theme.
func currentThemeName() string {
	return activeThemeName
}

func applyTheme(t colorTheme) {
	statusBarStyle = lipgloss.NewStyle().
		Background(t.statusBarBg).
		Foreground(t.statusBarFg)

	statusConnected = lipgloss.NewStyle().
		Foreground(t.connected).
		Bold(true)

	statusDisconnected = lipgloss.NewStyle().
		Foreground(t.disconnected).
		Bold(true)

	scrollModeStyle = lipgloss.NewStyle().
		Foreground(t.statusBarBg).
		Background(t.connected).
		Bold(true)

	userBlockStyle = lipgloss.NewStyle().
		Background(t.userBlockBg).
		Foreground(t.userBlockFg).
		PaddingLeft(1).
		PaddingRight(1)

	thinkingStyle = lipgloss.NewStyle().
		Foreground(t.thinkingFg).
		Italic(true)

	communicateStyle = lipgloss.NewStyle().
		Foreground(t.communicateFg)

	systemStyle = lipgloss.NewStyle().
		Foreground(t.systemFg).
		Italic(true)

	toolCollapsedStyle = lipgloss.NewStyle().
		Foreground(t.toolFg)

	toolExpandedStyle = lipgloss.NewStyle().
		Foreground(t.toolFg).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(t.toolBorderFg).
		PaddingLeft(1)

	toolNameStyle = lipgloss.NewStyle().
		Foreground(t.toolNameFg).
		Bold(true)

	toolDurationStyle = lipgloss.NewStyle().
		Foreground(t.toolDurFg)

	inputBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(t.inputBorderFg)

	inputPromptStyle = lipgloss.NewStyle().
		Foreground(t.inputPromptFg)

	viewportStyle = lipgloss.NewStyle().
		Background(t.viewportBg)

	pickerTitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(t.pickerTitleFg).
		MarginBottom(1)

	pickerSelected = lipgloss.NewStyle().
		Foreground(t.pickerSelectedFg).
		Bold(true)

	pickerNormal = lipgloss.NewStyle().
		Foreground(t.pickerNormalFg)

	pickerDim = lipgloss.NewStyle().
		Foreground(t.pickerDimFg)

	mpTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(t.pickerTitleFg)
	mpFilterStyle = lipgloss.NewStyle().Foreground(t.pickerNormalFg)
	mpActiveStyle = lipgloss.NewStyle().Foreground(t.pickerSelectedFg).Bold(true)
	mpNormalStyle = lipgloss.NewStyle().Foreground(t.pickerNormalFg)
	mpCursorStyle = lipgloss.NewStyle().Foreground(t.pickerSelectedFg).Bold(true)
	mpDimStyle = lipgloss.NewStyle().Foreground(t.pickerDimFg)
	mpActiveTag = lipgloss.NewStyle().Foreground(t.pickerActiveFg)
}
