package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
	"primeradiant.com/serf/internal/appwire"
)

// credentialsActionMsg carries a credential operation for a specific instance.
// Instance holds the instance name (not the provider type).
type credentialsActionMsg struct {
	Action   string // "set" | "logout" | "oauth"
	Instance string
}

// panelRow is a flat list entry used internally for rendering and cursor
// movement. Header rows carry no instance; instance rows do.
type panelRow struct {
	header   bool
	typeName string
	entry    *appwire.InstanceEntry
}

// credentialsPanel is the overlay model for managing provider instances.
// It groups instances by type and supports full CRUD keybindings.
type credentialsPanel struct {
	instances []appwire.InstanceEntry
	rows      []panelRow // flattened display rows (headers + instance rows)
	cursor    int        // index into rows (always points to a non-header row)
	err       error
	loading   bool
	done      bool
	cancelled bool

	// create/edit form state
	formOpen     bool
	formEditing  bool // true=edit existing, false=create new
	formField    int  // create: 0=type, 1=name, 2=apiStyle, 3=baseURL; edit: 0=apiStyle, 1=baseURL
	formName     string
	formType     string
	formAPIStyle string
	formBaseURL  string
}

func newCredentialsPanel() credentialsPanel {
	return credentialsPanel{loading: true}
}

func (p credentialsPanel) Init() tea.Cmd { return nil }

// buildRows constructs the flat header+instance row list from the instance
// slice, grouping by type in the order they appear.
func buildPanelRows(instances []appwire.InstanceEntry) []panelRow {
	var rows []panelRow
	seenType := ""
	for i := range instances {
		inst := &instances[i]
		if inst.Type != seenType {
			seenType = inst.Type
			rows = append(rows, panelRow{header: true, typeName: inst.Type})
		}
		rows = append(rows, panelRow{entry: inst})
	}
	return rows
}

// selectedInstance returns a pointer to the instance at the current cursor
// position, or nil when no selectable row exists.
func (p credentialsPanel) selectedInstance() *appwire.InstanceEntry {
	if len(p.rows) == 0 {
		return nil
	}
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return nil
	}
	row := p.rows[p.cursor]
	if row.header {
		return nil
	}
	return row.entry
}

// nextSelectableRow returns the next non-header row index from start in
// direction dir (+1 or -1), wrapping at boundaries. Returns -1 if no
// selectable row exists.
func nextSelectableRow(rows []panelRow, from, dir int) int {
	n := len(rows)
	if n == 0 {
		return -1
	}
	pos := from + dir
	for pos >= 0 && pos < n {
		if !rows[pos].header {
			return pos
		}
		pos += dir
	}
	return -1
}

// firstSelectableRow returns the index of the first non-header row.
func firstSelectableRow(rows []panelRow) int {
	for i, r := range rows {
		if !r.header {
			return i
		}
	}
	return -1
}

func (p credentialsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case instanceListResultMsg:
		p.loading = false
		p.err = m.Err
		if m.Err == nil {
			p.instances = m.List.Instances
		}
		p.rows = buildPanelRows(p.instances)
		// Clamp or reset cursor to a selectable row.
		if p.cursor >= len(p.rows) || (len(p.rows) > 0 && p.rows[p.cursor].header) {
			first := firstSelectableRow(p.rows)
			if first < 0 {
				first = 0
			}
			p.cursor = first
		}
		return p, nil

	case tea.KeyMsg:
		// When a form is open, route keys to the form handler.
		if p.formOpen {
			return p.updateForm(m)
		}
		return p.updateList(m)
	}
	return p, nil
}

func (p credentialsPanel) updateList(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		p.cancelled = true
		p.done = true
		return p, nil
	case tea.KeyUp:
		if next := nextSelectableRow(p.rows, p.cursor, -1); next >= 0 {
			p.cursor = next
		}
	case tea.KeyDown:
		if next := nextSelectableRow(p.rows, p.cursor, +1); next >= 0 {
			p.cursor = next
		}
	case tea.KeyEnter:
		cur := p.selectedInstance()
		if cur == nil {
			return p, nil
		}
		modes := strings.Join(cur.AuthModes, ",")
		if strings.Contains(modes, "apiKey") {
			return p, func() tea.Msg { return credentialsActionMsg{Action: "set", Instance: cur.Name} }
		}
		if strings.Contains(modes, "oauth") {
			return p, func() tea.Msg { return credentialsActionMsg{Action: "oauth", Instance: cur.Name} }
		}
	case tea.KeyRunes:
		s := string(m.Runes)
		switch strings.ToLower(s) {
		case "c":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			return p, func() tea.Msg { return credentialsActionMsg{Action: "logout", Instance: cur.Name} }
		case "o":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			return p, func() tea.Msg { return credentialsActionMsg{Action: "oauth", Instance: cur.Name} }
		case "*":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			name := cur.Name
			return p, func() tea.Msg { return instanceSetDefaultMsg{Name: name} }
		case "x":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			name := cur.Name
			return p, func() tea.Msg { return instanceRemoveMsg{Name: name} }
		case "n":
			p.formOpen = true
			p.formEditing = false
			p.formField = 0
			p.formName = ""
			p.formType = ""
			p.formAPIStyle = ""
			p.formBaseURL = ""
			return p, nil
		case "e":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			p.formOpen = true
			p.formEditing = true
			p.formField = 0
			p.formName = cur.Name
			p.formType = cur.Type
			p.formAPIStyle = cur.APIStyle
			p.formBaseURL = cur.BaseURL
			return p, nil
		}
	}
	return p, nil
}

// updateForm handles key input while the create/edit form is shown.
// Fields cycle through: for create (type→name→apiStyle→baseURL→submit);
// for edit (apiStyle→baseURL→submit). Type and name are not editable for edit.
func (p credentialsPanel) updateForm(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		p.formOpen = false
		return p, nil
	case tea.KeyEnter:
		// Advance field or submit on the last field.
		maxField := 3 // baseURL is last field (index 3) for create
		if p.formEditing {
			// Edit form: fields are apiStyle(0) and baseURL(1).
			maxField = 1
		}
		if p.formField < maxField {
			p.formField++
			return p, nil
		}
		// Submit.
		p.formOpen = false
		if p.formEditing {
			params := appwire.InstanceEditParams{
				Name:     p.formName,
				APIStyle: p.formAPIStyle,
				BaseURL:  p.formBaseURL,
			}
			return p, func() tea.Msg { return instanceEditSubmitMsg{Params: params} }
		}
		params := appwire.InstanceCreateParams{
			Type:     p.formType,
			Name:     p.formName,
			APIStyle: p.formAPIStyle,
			BaseURL:  p.formBaseURL,
		}
		return p, func() tea.Msg { return instanceCreateSubmitMsg{Params: params} }
	case tea.KeyBackspace:
		p.formDeleteChar()
	case tea.KeyRunes:
		s := string(m.Runes)
		// apiStyle field: space/tab toggles between responses and chat-completions.
		// create: apiStyle=2; edit: apiStyle=0.
		apiStyleField := 2
		if p.formEditing {
			apiStyleField = 0
		}
		if p.formField == apiStyleField {
			if s == " " || s == "\t" {
				p.toggleAPIStyle()
				return p, nil
			}
			return p, nil // ignore other input on apiStyle toggle field
		}
		p.formAppendChar(s)
	}
	return p, nil
}

func (p *credentialsPanel) toggleAPIStyle() {
	if p.formAPIStyle == "chat-completions" {
		p.formAPIStyle = "responses"
	} else {
		p.formAPIStyle = "chat-completions"
	}
}

func (p *credentialsPanel) formDeleteChar() {
	switch p.formActiveField() {
	case "name":
		if len(p.formName) > 0 {
			p.formName = p.formName[:len(p.formName)-1]
		}
	case "type":
		if len(p.formType) > 0 {
			p.formType = p.formType[:len(p.formType)-1]
		}
	case "baseURL":
		if len(p.formBaseURL) > 0 {
			p.formBaseURL = p.formBaseURL[:len(p.formBaseURL)-1]
		}
	}
}

func (p *credentialsPanel) formAppendChar(s string) {
	switch p.formActiveField() {
	case "name":
		p.formName += s
	case "type":
		p.formType += s
	case "baseURL":
		p.formBaseURL += s
	}
}

// formActiveField maps the current formField index to a field name.
// For create: 0=type, 1=name, 2=apiStyle, 3=baseURL.
// For edit:   0=apiStyle, 1=baseURL.
func (p credentialsPanel) formActiveField() string {
	if p.formEditing {
		switch p.formField {
		case 0:
			return "apiStyle"
		default:
			return "baseURL"
		}
	}
	switch p.formField {
	case 0:
		return "type"
	case 1:
		return "name"
	case 2:
		return "apiStyle"
	default:
		return "baseURL"
	}
}

// instanceCreateSubmitMsg triggers a create RPC call in hub_model.
type instanceCreateSubmitMsg struct {
	Params appwire.InstanceCreateParams
}

// instanceEditSubmitMsg triggers an edit RPC call in hub_model.
type instanceEditSubmitMsg struct {
	Params appwire.InstanceEditParams
}

func (p credentialsPanel) sourceBadgeColor(source string) lipgloss.Color {
	th := tuitheme.ActiveTheme()
	switch source {
	case "oauth", "env":
		return th.StateIdle
	case "absent":
		return th.StateEnded
	default:
		return th.TextDim
	}
}

func (p credentialsPanel) View() string {
	th := tuitheme.ActiveTheme()
	var body string
	if p.formOpen {
		body = p.formView()
	} else if p.loading {
		body = lipgloss.NewStyle().Foreground(th.TextDim).Render("Loading instances…")
	} else if p.err != nil {
		body = lipgloss.NewStyle().Foreground(th.StateEnded).Render("Error: " + p.err.Error())
	} else {
		var rows []string
		for i, row := range p.rows {
			if row.header {
				header := lipgloss.NewStyle().Foreground(th.TextDim).Render(row.typeName)
				rows = append(rows, "  "+header)
				continue
			}
			inst := row.entry
			cursor := "  "
			if i == p.cursor {
				cursor = "> "
			}
			// Default marker
			star := " "
			if inst.IsDefault {
				star = "★"
			}
			name := lipgloss.NewStyle().Foreground(th.Text).Render(inst.Name)
			badge := tuiprim.StatusBadge(p.sourceBadgeColor(inst.ActiveSource), inst.ActiveSource)
			// Optional apiStyle/baseURL hint
			hint := ""
			if inst.APIStyle != "" || inst.BaseURL != "" {
				parts := []string{}
				if inst.APIStyle != "" {
					parts = append(parts, inst.APIStyle)
				}
				if inst.BaseURL != "" {
					parts = append(parts, inst.BaseURL)
				}
				hint = " " + lipgloss.NewStyle().Foreground(th.TextDim).Render(strings.Join(parts, " "))
			}
			rows = append(rows, cursor+star+" "+name+hint+"  "+badge)
		}
		body = strings.Join(rows, "\n")
	}
	width := 60
	var footer string
	if p.formOpen {
		footer = tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("enter", "next/submit"), tuiprim.KbdHint("esc", "cancel"))
	} else {
		footer = tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("enter", "set key"),
			tuiprim.KbdHint("o", "OAuth"),
			tuiprim.KbdHint("c", "clear"),
			tuiprim.KbdHint("n", "new"),
			tuiprim.KbdHint("e", "edit"),
			tuiprim.KbdHint("x", "remove"),
			tuiprim.KbdHint("*", "default"),
			tuiprim.KbdHint("esc", "close"),
		)
	}
	return tuiprim.Overlay(tuiprim.OverlayOpts{Title: "Instances", Width: width, Body: body, Footer: footer})
}

// formView renders the in-overlay create/edit form.
func (p credentialsPanel) formView() string {
	var lines []string
	if p.formEditing {
		lines = append(lines, "Edit instance: "+p.formName)
		lines = append(lines, "")
		lines = append(lines, p.formFieldLine("API Style", "apiStyle", p.apiStyleDisplay(), 0))
		lines = append(lines, p.formFieldLine("Base URL", "baseURL", p.formBaseURL, 1))
	} else {
		lines = append(lines, "New instance")
		lines = append(lines, "")
		lines = append(lines, p.formFieldLine("Type", "type", p.formType, 0))
		lines = append(lines, p.formFieldLine("Name", "name", p.formName, 1))
		lines = append(lines, p.formFieldLine("API Style", "apiStyle", p.apiStyleDisplay(), 2))
		lines = append(lines, p.formFieldLine("Base URL", "baseURL", p.formBaseURL, 3))
	}
	return strings.Join(lines, "\n")
}

func (p credentialsPanel) apiStyleDisplay() string {
	if p.formAPIStyle == "" {
		return "(default)"
	}
	return p.formAPIStyle
}

func (p credentialsPanel) formFieldLine(label, fieldName, value string, fieldIdx int) string {
	th := tuitheme.ActiveTheme()
	active := p.formActiveField() == fieldName
	cursor := "  "
	if active {
		cursor = "> "
	}
	prompt := lipgloss.NewStyle().Foreground(th.TextDim).Render(label + ":")
	var val string
	if active {
		val = lipgloss.NewStyle().Foreground(th.Text).Render(value + "_")
	} else {
		val = lipgloss.NewStyle().Foreground(th.Text).Render(value)
	}
	_ = fieldIdx
	return cursor + prompt + " " + val
}
