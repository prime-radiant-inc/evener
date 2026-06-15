package tuiprim

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitext"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

type AppShell struct {
	TopBar  string
	Body    string
	Overlay string
	Footer  string
	Height  int
}

func (s AppShell) View() string {
	styles := tuitheme.DefaultTUIStyles()
	topBar := strings.TrimRight(s.TopBar, "\n")
	if topBar != "" {
		topBar = styles.Title.Render(topBar)
	}
	footer := strings.TrimRight(s.Footer, "\n")

	// Overlay and Body share the region between the (anchored) TopBar and
	// Footer. On a short pane this region can overflow Height and, inline,
	// scroll the TopBar off-screen. Trim it to whatever rows remain so the
	// rendered frame never exceeds Height while keeping both chrome rows.
	innerSections := make([]string, 0, 2)
	if overlay := strings.TrimRight(s.Overlay, "\n"); overlay != "" {
		innerSections = append(innerSections, overlay)
	}
	if body := strings.TrimRight(s.Body, "\n"); body != "" {
		innerSections = append(innerSections, body)
	}
	inner := strings.Join(innerSections, "\n\n")
	if s.Height > 0 {
		inner = boundShellInner(inner, s.Height, topBar, footer)
	}

	contentSections := make([]string, 0, 2)
	if topBar != "" {
		contentSections = append(contentSections, topBar)
	}
	if inner != "" {
		contentSections = append(contentSections, inner)
	}
	if len(contentSections) == 0 && footer == "" {
		return ""
	}
	content := strings.Join(contentSections, "\n\n")
	if footer == "" {
		return content + "\n"
	}
	if s.Height <= 0 {
		if content == "" {
			return footer + "\n"
		}
		return content + "\n\n" + footer + "\n"
	}
	if content == "" {
		gap := max(0, s.Height-tuitext.ShellSectionLineCount(footer))
		return tuitext.LimitFirstLines(strings.Repeat("\n", gap)+footer, s.Height)
	}
	gap := s.Height - tuitext.ShellSectionLineCount(content) - tuitext.ShellSectionLineCount(footer) + 1
	if gap < 2 {
		gap = 2
	}
	// Hard cap to Height, keeping the first lines: when chrome alone meets or
	// exceeds Height the gap/floor math would otherwise overflow and, inline,
	// scroll the TopBar off the top. Keeping the first Height lines preserves the
	// header (the line we most need on screen) at the cost of the footer on a pane
	// too short to hold both.
	return tuitext.LimitFirstLines(content+strings.Repeat("\n", gap)+footer, s.Height)
}

// boundShellInner trims the overlay/body region toward the rows left between the
// TopBar and Footer chrome (plus their blank-line separators). It keeps the first
// lines of inner; callers that need a particular line (such as a palette
// selection) kept in view must window their own content before handing it to
// AppShell. This is the primary bound; View() additionally hard-caps the whole
// frame to Height for the degenerate case where chrome alone exceeds Height.
func boundShellInner(inner string, height int, topBar, footer string) string {
	if inner == "" {
		return inner
	}
	used := 0
	separators := 0
	for _, section := range []string{topBar, inner, footer} {
		if tuitext.ShellSectionLineCount(section) > 0 {
			separators++
		}
	}
	for _, section := range []string{topBar, footer} {
		used += tuitext.ShellSectionLineCount(section)
	}
	if separators > 1 {
		used += 2 * (separators - 1)
	}
	available := height - used
	if available < 1 {
		available = 1
	}
	return tuitext.LimitFirstLines(inner, available)
}

func ActionBar(keys ...string) string {
	return strings.Join(keys, "  ")
}

func ActionBarForWidth(width int, keys ...string) string {
	if width <= 0 {
		return ActionBar(keys...)
	}
	lines := make([]string, 0, 1)
	current := ""
	for _, key := range keys {
		if current == "" {
			current = key
			continue
		}
		next := current + "  " + key
		if lipgloss.Width(next) > width {
			lines = append(lines, current)
			current = key
			continue
		}
		current = next
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n")
}
