package tuipick

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/envvars"
)

type TextInputResultMsg struct {
	Tag       string
	Value     string
	Cancelled bool
}

type TextInputModal struct {
	tag    string
	title  string
	prompt string
	input  string
	mask   bool
	paths  bool
	done   bool
	width  int
}

func NewTextInputModal(prompt, tag string) TextInputModal {
	return TextInputModal{prompt: prompt, tag: tag}
}

func NewTextInputModalWithTitle(title, prompt, tag string) TextInputModal {
	return TextInputModal{title: title, prompt: prompt, tag: tag, width: 60}
}

func NewTextInputModalWithInput(prompt, tag, input string) TextInputModal {
	return TextInputModal{prompt: prompt, tag: tag, input: input}
}

func NewPathTextInputModal(prompt, tag, input string) TextInputModal {
	return TextInputModal{prompt: prompt, tag: tag, input: input, paths: true}
}

func NewTextInputModalMasked(prompt, tag string) TextInputModal {
	return TextInputModal{prompt: prompt, tag: tag, mask: true}
}

func (m TextInputModal) Init() tea.Cmd { return nil }

func (m TextInputModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if v, ok := msg.(tea.KeyMsg); ok {
		switch v.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			return m, func() tea.Msg { return TextInputResultMsg{Tag: m.tag, Cancelled: true} }
		case tea.KeyEnter:
			m.done = true
			return m, func() tea.Msg { return TextInputResultMsg{Tag: m.tag, Value: m.input} }
		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		case tea.KeyTab:
			if m.paths {
				m.input = CompleteLastPathSegment(m.input, nil)
			}
		case tea.KeyRunes:
			m.input += string(v.Runes)
		}
	}
	return m, nil
}

func (m TextInputModal) inputView() string {
	display := m.input
	if m.mask {
		display = ""
		var displaySb80 strings.Builder
		for range m.input {
			displaySb80.WriteString("•")
		}
		display += displaySb80.String()
	}
	return "> " + display + "_"
}

// Done reports whether the modal has been dismissed (submitted or cancelled).
func (m TextInputModal) Done() bool { return m.done }

func (m TextInputModal) View() string {
	if m.title != "" {
		body := m.prompt + "\n\n" + m.inputView()
		width := m.width
		if width <= 0 {
			width = 60
		}
		hints := []string{tuiprim.KbdHint("enter", "confirm"), tuiprim.KbdHint("esc", "cancel")}
		if m.paths {
			hints = append([]string{tuiprim.KbdHint("tab", "complete path")}, hints...)
		}
		footer := tuiprim.ActionBarForWidth(width, hints...)
		return tuiprim.Overlay(tuiprim.OverlayOpts{Title: m.title, Width: width, Body: body, Footer: footer})
	}
	// Legacy plain view (no title set).
	help := "[Enter] confirm  [Esc] cancel"
	if m.paths {
		help = "[Tab] complete path  " + help
	}
	return m.prompt + "\n" + m.inputView() + "\n" + help
}

// CompleteLastPathSegment completes the last comma-separated path-like
// segment of input by reading the parent directory and returning the first
// entry whose name has the segment as a prefix. Hidden entries are skipped
// when there's no prefix filter. accept, when non-nil, gates which entries
// are eligible — pass nil to accept any entry.
func CompleteLastPathSegment(input string, accept func(os.DirEntry) bool) string {
	start := strings.LastIndex(input, ",") + 1
	prefix := input[start:]
	leading := prefix[:len(prefix)-len(strings.TrimLeft(prefix, " \t"))]
	raw := strings.TrimSpace(prefix)
	if raw == "" {
		raw = envvars.Home.Getenv()
	}
	if strings.HasPrefix(raw, "~/") || raw == "~" {
		raw = filepath.Join(envvars.Home.Getenv(), strings.TrimPrefix(raw, "~"))
	}
	var listDir, filter string
	if strings.HasSuffix(raw, string(filepath.Separator)) {
		listDir = raw
	} else {
		listDir = filepath.Dir(raw)
		filter = filepath.Base(raw)
	}
	if listDir == "." {
		if cwd, err := os.Getwd(); err == nil {
			listDir = cwd
		}
	}
	entries, err := os.ReadDir(listDir)
	if err != nil {
		return input
	}
	for _, entry := range entries {
		if filter != "" && !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(filter)) {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") && filter == "" {
			continue
		}
		if accept != nil && !accept(entry) {
			continue
		}
		full := filepath.Join(listDir, entry.Name())
		if entry.IsDir() {
			full += string(filepath.Separator)
		}
		return input[:start] + leading + full
	}
	return input
}

// DirEntry returns a predicate suitable for CompleteLastPathSegment that
// only accepts directory entries. Used by the spawn-form working-dir field
// where files would land in a field that later rejects them on submit.
func DirEntry() func(os.DirEntry) bool {
	return func(e os.DirEntry) bool { return e.IsDir() }
}
