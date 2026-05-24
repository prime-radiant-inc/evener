package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// tuiStyles holds composed lipgloss.Style values derived from the active
// Theme. defaultTUIStyles() rebuilds this from activeTheme() on each call so
// runtime theme switches take effect immediately.
type tuiStyles struct {
	Title      lipgloss.Style
	Section    lipgloss.Style
	Muted      lipgloss.Style
	Selected   lipgloss.Style
	Pane       lipgloss.Style
	Modal      lipgloss.Style
	Error      lipgloss.Style
	Idle       lipgloss.Style
	Processing lipgloss.Style
	Waiting    lipgloss.Style
	Ended      lipgloss.Style
}

// defaultTUIStyles builds a tuiStyles from the currently active Theme.
func defaultTUIStyles() tuiStyles {
	th := activeTheme()
	return tuiStyles{
		Title:      lipgloss.NewStyle().Bold(true).Foreground(th.Text).Background(th.BgRaised),
		Section:    lipgloss.NewStyle().Bold(true).Foreground(th.Accent),
		Muted:      lipgloss.NewStyle().Foreground(th.TextDim),
		Selected:   lipgloss.NewStyle().Foreground(th.Text).Background(th.SurfaceSecondary).Bold(true),
		Pane:       lipgloss.NewStyle().Foreground(th.Text).Background(th.BgRaised).PaddingLeft(2).PaddingRight(1),
		Modal:      lipgloss.NewStyle().Foreground(th.Text).Background(th.BgRaised).Border(lipgloss.RoundedBorder()).BorderForeground(th.Rule).PaddingLeft(2).PaddingRight(2),
		Error:      lipgloss.NewStyle().Foreground(th.StateAwaiting).Bold(true),
		Idle:       lipgloss.NewStyle().Foreground(th.StateIdle),
		Processing: lipgloss.NewStyle().Foreground(th.StateProcessing),
		Waiting:    lipgloss.NewStyle().Foreground(th.StateWarning),
		Ended:      lipgloss.NewStyle().Foreground(th.StateEnded),
	}
}

// activeThemeName tracks the selected theme ("system", "dark", or "light").
var activeThemeName string

// Derived style vars — re-initialized by setTheme() via rebuildDerivedStyles().
var (
	statusBarStyle     lipgloss.Style
	statusConnected    lipgloss.Style
	statusDisconnected lipgloss.Style
	scrollModeStyle    lipgloss.Style

	userBlockStyle   lipgloss.Style
	thinkingStyle    lipgloss.Style
	communicateStyle lipgloss.Style
	systemStyle      lipgloss.Style

	toolCollapsedStyle lipgloss.Style
	toolExpandedStyle  lipgloss.Style
	toolNameStyle      lipgloss.Style
	toolDurationStyle  lipgloss.Style

	inputBorderStyle lipgloss.Style

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
	setTheme("system")
}

func initThemeFromStateDir(stateDir string) {
	if name, ok := loadThemePreference(stateDir); ok && setTheme(name) {
		return
	}
	setTheme("system")
}

// setTheme switches to the named theme. Returns false if the name is unrecognised.
// This is the public entry point; internally it routes to applyThemeName.
func setTheme(name string) bool {
	switch name {
	case "system", "dark", "light":
		activeThemeName = name
	default:
		return false
	}
	resolved := name
	if name == "system" {
		if termenv.HasDarkBackground() {
			resolved = "dark"
		} else {
			resolved = "light"
		}
	}
	applyThemeName(resolved)
	rebuildDerivedStyles()
	return true
}

func setThemeAndPersist(stateDir, name string) bool {
	if !setTheme(name) {
		return false
	}
	_ = saveThemePreference(stateDir, name)
	return true
}

// currentThemeName returns the name of the active theme.
func currentThemeName() string {
	return activeThemeName
}

// rebuildDerivedStyles rebuilds all derived style vars from the currently active Theme.
func rebuildDerivedStyles() {
	th := activeTheme()

	statusBarStyle = lipgloss.NewStyle().
		Background(th.BgRaised).
		Foreground(th.Text)

	statusConnected = lipgloss.NewStyle().
		Foreground(th.StateIdle).
		Bold(true)

	statusDisconnected = lipgloss.NewStyle().
		Foreground(th.StateAwaiting).
		Bold(true)

	scrollModeStyle = lipgloss.NewStyle().
		Foreground(th.BgRaised).
		Background(th.StateIdle).
		Bold(true)

	// User messages: no background fill — the left bar marker (added in
	// renderMessage) and bold-ish text are sufficient to demarcate, and
	// avoiding a painted block keeps us off the terminal background.
	userBlockStyle = lipgloss.NewStyle().
		Foreground(th.Text).
		PaddingLeft(1).
		PaddingRight(1)

	thinkingStyle = lipgloss.NewStyle().
		Foreground(th.TextDim).
		Italic(true)

	communicateStyle = lipgloss.NewStyle().
		Foreground(th.Text)

	systemStyle = lipgloss.NewStyle().
		Foreground(th.TextMuted).
		Italic(true)

	toolCollapsedStyle = lipgloss.NewStyle().
		Foreground(th.TextDim)

	toolExpandedStyle = lipgloss.NewStyle().
		Foreground(th.TextDim).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(th.Rule).
		PaddingLeft(1)

	toolNameStyle = lipgloss.NewStyle().
		Foreground(th.Accent).
		Bold(true)

	toolDurationStyle = lipgloss.NewStyle().
		Foreground(th.TextGhost)

	inputBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(th.Rule)

	// Viewport: no background fill — let the terminal's own background
	// show through. Painting our Bg over the entire viewport creates a
	// rectangle that fights any non-matching terminal config.
	viewportStyle = lipgloss.NewStyle()

	pickerTitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(th.Accent).
		MarginBottom(1)

	pickerSelected = lipgloss.NewStyle().
		Foreground(th.Accent).
		Bold(true)

	pickerNormal = lipgloss.NewStyle().
		Foreground(th.Text)

	pickerDim = lipgloss.NewStyle().
		Foreground(th.TextDim)

	mpTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(th.Accent)
	mpFilterStyle = lipgloss.NewStyle().Foreground(th.Text)
	mpActiveStyle = lipgloss.NewStyle().Foreground(th.Accent).Bold(true)
	mpNormalStyle = lipgloss.NewStyle().Foreground(th.Text)
	mpCursorStyle = lipgloss.NewStyle().Foreground(th.Accent).Bold(true)
	mpDimStyle = lipgloss.NewStyle().Foreground(th.TextDim)
	mpActiveTag = lipgloss.NewStyle().Foreground(th.AccentSecondary)
}

type tuiPreferences struct {
	Theme string `json:"theme"`
}

func themePreferencePath(stateDir string) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "tui", "preferences.json")
}

func loadThemePreference(stateDir string) (string, bool) {
	path := themePreferencePath(stateDir)
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var prefs tuiPreferences
	if err := json.Unmarshal(data, &prefs); err != nil {
		return "", false
	}
	name := strings.TrimSpace(prefs.Theme)
	if !validThemeName(name) {
		return "", false
	}
	return name, true
}

func saveThemePreference(stateDir, name string) error {
	path := themePreferencePath(stateDir)
	if path == "" || !validThemeName(name) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tuiPreferences{Theme: name}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func validThemeName(name string) bool {
	switch name {
	case "system", "dark", "light":
		return true
	default:
		return false
	}
}
