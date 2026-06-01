package tuitheme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// tuiStyles holds composed lipgloss.Style values derived from the active
// Theme. DefaultTUIStyles() rebuilds this from ActiveTheme() on each call so
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

// DefaultTUIStyles builds a tuiStyles from the currently active Theme.
func DefaultTUIStyles() tuiStyles {
	th := ActiveTheme()
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

// Derived style vars — re-initialized by SetTheme() via rebuildDerivedStyles().
var (
	statusBarStyle     lipgloss.Style
	statusConnected    lipgloss.Style
	statusDisconnected lipgloss.Style
	scrollModeStyle    lipgloss.Style

	UserBlockStyle   lipgloss.Style
	ThinkingStyle    lipgloss.Style
	CommunicateStyle lipgloss.Style
	SystemStyle      lipgloss.Style

	toolCollapsedStyle lipgloss.Style
	ToolExpandedStyle  lipgloss.Style
	toolNameStyle      lipgloss.Style
	toolDurationStyle  lipgloss.Style

	inputBorderStyle lipgloss.Style

	ViewportStyle lipgloss.Style

	pickerTitle    lipgloss.Style
	pickerSelected lipgloss.Style
	pickerNormal   lipgloss.Style
	pickerDim      lipgloss.Style

	// model_picker specific
	mpTitleStyle  lipgloss.Style
	MpFilterStyle lipgloss.Style
	MpActiveStyle lipgloss.Style
	MpNormalStyle lipgloss.Style
	MpCursorStyle lipgloss.Style
	MpDimStyle    lipgloss.Style
	MpActiveTag   lipgloss.Style
)

// InitTheme detects the terminal background and populates all style vars.
// Call once at program startup before creating the bubbletea program.
func InitTheme() {
	SetTheme("system")
}

func InitThemeFromStateDir(stateDir string) {
	if name, ok := LoadThemePreference(stateDir); ok && SetTheme(name) {
		return
	}
	SetTheme("system")
}

// SetTheme switches to the named theme. Returns false if the name is unrecognised.
// This is the public entry point; internally it routes to ApplyThemeName.
func SetTheme(name string) bool {
	switch name {
	case "system", "dark", "light":
		activeThemeName = name
	default:
		return false
	}
	resolved := name
	if name == "system" {
		// Uses cached probe from ProbeTerminalDefaults() when available;
		// falls back to termenv.HasDarkBackground() otherwise.
		resolved = detectSystemThemeKey()
	}
	ApplyThemeName(resolved)
	rebuildDerivedStyles()
	// Refresh the terminal's default background to match the new theme so
	// runtime theme swaps (e.g. via the theme picker) repaint every cell,
	// not just the ones we re-render.
	ApplyTerminalBg()
	return true
}

func SetThemeAndPersist(stateDir, name string) bool {
	if !SetTheme(name) {
		return false
	}
	_ = saveThemePreference(stateDir, name)
	return true
}

// CurrentThemeName returns the name of the active theme.
func CurrentThemeName() string {
	return activeThemeName
}

// rebuildDerivedStyles rebuilds all derived style vars from the currently active Theme.
func rebuildDerivedStyles() {
	th := ActiveTheme()

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
	UserBlockStyle = lipgloss.NewStyle().
		Foreground(th.Text).
		PaddingLeft(1).
		PaddingRight(1)

	ThinkingStyle = lipgloss.NewStyle().
		Foreground(th.TextDim).
		Italic(true)

	CommunicateStyle = lipgloss.NewStyle().
		Foreground(th.Text)

	SystemStyle = lipgloss.NewStyle().
		Foreground(th.TextMuted).
		Italic(true)

	toolCollapsedStyle = lipgloss.NewStyle().
		Foreground(th.TextDim)

	ToolExpandedStyle = lipgloss.NewStyle().
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
	ViewportStyle = lipgloss.NewStyle()

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
	MpFilterStyle = lipgloss.NewStyle().Foreground(th.Text)
	MpActiveStyle = lipgloss.NewStyle().Foreground(th.Accent).Bold(true)
	MpNormalStyle = lipgloss.NewStyle().Foreground(th.Text)
	MpCursorStyle = lipgloss.NewStyle().Foreground(th.Accent).Bold(true)
	MpDimStyle = lipgloss.NewStyle().Foreground(th.TextDim)
	MpActiveTag = lipgloss.NewStyle().Foreground(th.AccentSecondary)
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

func LoadThemePreference(stateDir string) (string, bool) {
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
