package launchconfig

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuiprim"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

// CredentialsActionMsg carries a credential operation for a specific instance.
// Instance holds the instance name (not the provider type).
type CredentialsActionMsg struct {
	Action     string // "set" | "logout" | "oauth" | "test"
	Instance   string
	Generation uint64
}

// panelRow is a flat list entry used internally for rendering and cursor
// movement. Header rows carry no instance; instance rows do.
type panelRow struct {
	header    bool
	groupName string
	entry     *appwire.InstanceEntry
}

// CredentialsPanel is the overlay model for managing provider instances.
// It groups instances by the registry provider they resolve to and supports
// full CRUD keybindings.
type CredentialsPanel struct {
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
	formField    int  // create: 0=base, 1=name, 2=protocol, 3=baseURL; edit: 0=protocol, 1=baseURL
	formName     string
	formBase     string
	formProtocol string
	formBaseURL  string
	// formBaseURLWas is the base URL the edited instance already had, so the
	// form can tell "left blank" from "cleared".
	formBaseURLWas string

	testPending    map[string]bool
	testResults    map[string]appwire.AuthTestResponse
	testGeneration uint64
}

func NewCredentialsPanel() CredentialsPanel {
	return CredentialsPanel{loading: true}
}

func (p CredentialsPanel) Init() tea.Cmd { return nil }

// buildRows constructs the flat header+instance row list, grouping by the
// registry provider each instance resolves to. The registry ranks instances by
// default order and then by name, which interleaves providers; a header is
// emitted whenever the provider changes from one row to the next, so the rows
// are sorted by (provider, name) first or a provider gets two headers.
func buildPanelRows(instances []appwire.InstanceEntry) []panelRow {
	ordered := slices.Clone(instances)
	slices.SortStableFunc(ordered, func(a, b appwire.InstanceEntry) int {
		return cmp.Or(cmp.Compare(a.ProviderID, b.ProviderID), cmp.Compare(a.Name, b.Name))
	})
	var rows []panelRow
	seenProvider := ""
	for i := range ordered {
		inst := &ordered[i]
		if inst.ProviderID != seenProvider || i == 0 {
			seenProvider = inst.ProviderID
			rows = append(rows, panelRow{header: true, groupName: inst.ProviderID})
		}
		rows = append(rows, panelRow{entry: inst})
	}
	return rows
}

// selectedInstance returns a pointer to the instance at the current cursor
// position, or nil when no selectable row exists.
func (p CredentialsPanel) selectedInstance() *appwire.InstanceEntry {
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

func (p CredentialsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case InstanceListResultMsg:
		p.testGeneration++
		p.testPending = nil
		p.testResults = nil
		p.loading = false
		p.err = m.Err
		if m.Err == nil {
			p.instances = m.List.Instances
		}
		p.rows = buildPanelRows(p.instances)
		// Clamp or reset cursor to a selectable row.
		if p.cursor >= len(p.rows) || (len(p.rows) > 0 && p.rows[p.cursor].header) {
			p.cursor = max(firstSelectableRow(p.rows), 0)
		}
		return p, nil

	case AuthTestResultMsg:
		if m.Generation != p.testGeneration {
			return p, nil
		}
		name := strings.TrimSpace(m.Provider)
		if name == "" {
			name = strings.TrimSpace(m.Response.Provider)
		}
		if name == "" {
			return p, nil
		}
		p.testPending = cloneCredentialTestPending(p.testPending)
		delete(p.testPending, name)
		if p.testResults == nil {
			p.testResults = map[string]appwire.AuthTestResponse{}
		} else {
			p.testResults = cloneCredentialTestResults(p.testResults)
		}
		if m.Err != nil {
			p.testResults[name] = safeCredentialTestResult(name, appwire.AuthTestResponse{Provider: name, Status: appwire.AuthTestStatusEndpointFailure})
		} else {
			p.testResults[name] = safeCredentialTestResult(name, m.Response)
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

func (p CredentialsPanel) updateList(m tea.KeyMsg) (tea.Model, tea.Cmd) {
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
			return p, func() tea.Msg { return CredentialsActionMsg{Action: "set", Instance: cur.Name} }
		}
		if strings.Contains(modes, "oauth") {
			return p, func() tea.Msg { return CredentialsActionMsg{Action: "oauth", Instance: cur.Name} }
		}
		// A gcp-adc instance takes a pasted service-account key or
		// application-default JSON, into the same store the web hub's
		// Providers & credentials writes.
		if strings.Contains(modes, "credentialJson") {
			return p, func() tea.Msg { return CredentialsActionMsg{Action: "setCredentialJson", Instance: cur.Name} }
		}
	case tea.KeyRunes:
		s := string(m.Runes)
		switch strings.ToLower(s) {
		case "t":
			cur := p.selectedInstance()
			if cur == nil || p.testPending[cur.Name] {
				return p, nil
			}
			name := cur.Name
			p.testPending = cloneCredentialTestPending(p.testPending)
			p.testPending[name] = true
			p.testResults = cloneCredentialTestResults(p.testResults)
			delete(p.testResults, name)
			return p, func() tea.Msg {
				return CredentialsActionMsg{Action: "test", Instance: name, Generation: p.testGeneration}
			}
		case "c":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			return p, func() tea.Msg { return CredentialsActionMsg{Action: "logout", Instance: cur.Name} }
		case "o":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			return p, func() tea.Msg { return CredentialsActionMsg{Action: "oauth", Instance: cur.Name} }
		case "*":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			name := cur.Name
			return p, func() tea.Msg { return InstanceSetDefaultMsg{Name: name} }
		case "x":
			cur := p.selectedInstance()
			if cur == nil {
				return p, nil
			}
			name := cur.Name
			return p, func() tea.Msg { return InstanceRemoveMsg{Name: name} }
		case "n":
			p.formOpen = true
			p.formEditing = false
			p.formField = 0
			p.formName = ""
			p.formBase = ""
			p.formProtocol = ""
			p.formBaseURL = ""
			p.formBaseURLWas = ""
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
			p.formBase = cur.Base
			p.formProtocol = cur.Protocol
			p.formBaseURL = cur.BaseURL
			p.formBaseURLWas = cur.BaseURL
			return p, nil
		}
	}
	return p, nil
}

// updateForm handles key input while the create/edit form is shown.
// Fields cycle through: for create (base→name→protocol→baseURL→submit);
// for edit (protocol→baseURL→submit). Base and name are not editable for edit.
func (p CredentialsPanel) updateForm(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		p.formOpen = false
		return p, nil
	case tea.KeyEnter:
		// Advance field or submit on the last field.
		maxField := 3 // baseURL is last field (index 3) for create
		if p.formEditing {
			// Edit form: fields are protocol(0) and baseURL(1).
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
				Protocol: p.formProtocol,
			}
			switch {
			case strings.TrimSpace(p.formBaseURL) == strings.TrimSpace(p.formBaseURLWas):
				// Untouched: send neither field. formBaseURLWas is the
				// instance's displayed base URL, which for an implicit
				// instance is its resolved default (not an authored
				// override) — sending it back unedited would author it as a
				// literal one and stop spec §10's credential inheritance.
				// For a hidden or unresolvable instance the display can be
				// empty while a real base_url is still authored underneath,
				// so an untouched empty field must not read as a clear
				// either (#711).
			case p.clearedBaseURL():
				// Deliberately emptied a field that had something displayed.
				params.ClearBaseURL = true
			default:
				params.BaseURL = p.formBaseURL
			}
			return p, func() tea.Msg { return InstanceEditSubmitMsg{Params: params} }
		}
		params := appwire.InstanceCreateParams{
			Base:     p.formBase,
			Name:     p.formName,
			Protocol: p.formProtocol,
			BaseURL:  p.formBaseURL,
		}
		return p, func() tea.Msg { return InstanceCreateSubmitMsg{Params: params} }
	case tea.KeyBackspace:
		p.formDeleteChar()
	case tea.KeyRunes:
		p.formAppendChar(string(m.Runes))
	}
	return p, nil
}

func (p *CredentialsPanel) formDeleteChar() {
	switch p.formActiveField() {
	case "name":
		if len(p.formName) > 0 {
			p.formName = p.formName[:len(p.formName)-1]
		}
	case "base":
		if len(p.formBase) > 0 {
			p.formBase = p.formBase[:len(p.formBase)-1]
		}
	case "protocol":
		if len(p.formProtocol) > 0 {
			p.formProtocol = p.formProtocol[:len(p.formProtocol)-1]
		}
	case "baseURL":
		if len(p.formBaseURL) > 0 {
			p.formBaseURL = p.formBaseURL[:len(p.formBaseURL)-1]
		}
	}
}

func (p *CredentialsPanel) formAppendChar(s string) {
	switch p.formActiveField() {
	case "name":
		p.formName += s
	case "base":
		p.formBase += s
	case "protocol":
		p.formProtocol += s
	case "baseURL":
		p.formBaseURL += s
	}
}

// formActiveField maps the current formField index to a field name.
// For create: 0=base, 1=name, 2=protocol, 3=baseURL.
// For edit:   0=protocol, 1=baseURL.
func (p CredentialsPanel) formActiveField() string {
	if p.formEditing {
		switch p.formField {
		case 0:
			return "protocol"
		default:
			return "baseURL"
		}
	}
	switch p.formField {
	case 0:
		return "base"
	case 1:
		return "name"
	case 2:
		return "protocol"
	default:
		return "baseURL"
	}
}

// InstanceCreateSubmitMsg triggers a create RPC call in hub_model.
type InstanceCreateSubmitMsg struct {
	Params appwire.InstanceCreateParams
}

// InstanceEditSubmitMsg triggers an edit RPC call in hub_model.
type InstanceEditSubmitMsg struct {
	Params appwire.InstanceEditParams
}

// sourceBadgeColor picks the tone for one instance's credential-source badge.
//
// Every registry source but "none" names a credential that resolved — an
// inline key, a credential header, the store, an environment variable, an
// OAuth record, application-default credentials — so all of them wear the
// configured tone. "none" is the one no-credential state; whether that is a
// problem depends on the instance, so credentialBadge decides its tone and
// this function leaves it on the panel's chrome tone.
func (p CredentialsPanel) sourceBadgeColor(source string) lipgloss.Color {
	th := tuitheme.ActiveTheme()
	if source == "" || source == "none" {
		return th.TextDim
	}
	return th.StateIdle
}

// credentialBadge renders one instance's credential badge. A credential that
// did not resolve is only missing when the instance needs one: an auth-none or
// optional-bearer instance gets the neutral "optional" badge rather than the
// ended-tone one that marks a key genuinely gone.
func (p CredentialsPanel) credentialBadge(inst appwire.InstanceEntry) string {
	if inst.ActiveSource == "none" {
		if !inst.CredentialRequired {
			return tuiprim.StatusBadge(tuitheme.ActiveTheme().TextDim, "optional")
		}
		return tuiprim.StatusBadge(tuitheme.ActiveTheme().StateEnded, "none")
	}
	return tuiprim.StatusBadge(p.sourceBadgeColor(inst.ActiveSource), inst.ActiveSource)
}

// Done reports whether the panel has been dismissed.
func (p CredentialsPanel) Done() bool { return p.done }

func (p CredentialsPanel) View() string {
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
				header := lipgloss.NewStyle().Foreground(th.TextDim).Render(row.groupName)
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
			badge := p.credentialBadge(*inst)
			// Optional protocol/baseURL hint
			hint := ""
			if inst.Protocol != "" || inst.BaseURL != "" {
				parts := []string{}
				if inst.Protocol != "" {
					parts = append(parts, inst.Protocol)
				}
				if inst.BaseURL != "" {
					parts = append(parts, inst.BaseURL)
				}
				hint = " " + lipgloss.NewStyle().Foreground(th.TextDim).Render(strings.Join(parts, " "))
			}
			rows = append(rows, cursor+star+" "+name+hint+"  "+badge)
			if p.testPending[inst.Name] {
				rows = append(rows, "    Testing credentials…")
			} else if result, ok := p.testResults[inst.Name]; ok {
				rows = append(rows, "    "+result.Status+": "+result.Message)
			}
		}
		body = strings.Join(rows, "\n")
	}
	width := 60
	var footer string
	if p.formOpen {
		footer = tuiprim.ActionBarForWidth(width, tuiprim.KbdHint("enter", "next/submit"), tuiprim.KbdHint("esc", "cancel"))
	} else {
		footer = tuiprim.ActionBarForWidth(width,
			tuiprim.KbdHint("enter", "set credential"),
			tuiprim.KbdHint("t", "test credentials"),
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

func cloneCredentialTestPending(current map[string]bool) map[string]bool {
	next := make(map[string]bool, len(current)+1)
	maps.Copy(next, current)
	return next
}

func cloneCredentialTestResults(current map[string]appwire.AuthTestResponse) map[string]appwire.AuthTestResponse {
	next := make(map[string]appwire.AuthTestResponse, len(current)+1)
	maps.Copy(next, current)
	return next
}

func safeCredentialTestResult(provider string, response appwire.AuthTestResponse) appwire.AuthTestResponse {
	messages := map[string]string{
		appwire.AuthTestStatusSuccess:              "Credentials verified.",
		appwire.AuthTestStatusMissing:              "No credentials are configured for this instance. Add a key or sign in first.",
		appwire.AuthTestStatusAuthRejected:         "The provider rejected these credentials. Replace the key or sign in again.",
		appwire.AuthTestStatusEndpointFailure:      "The provider endpoint could not be reached. Check the endpoint and network connection.",
		appwire.AuthTestStatusConfigurationFailure: "Provider configuration could not be loaded. Check the instance settings.",
		appwire.AuthTestStatusUnsupported:          "This provider does not support harmless credential verification.",
	}
	status := response.Status
	message, ok := messages[status]
	if !ok {
		status = appwire.AuthTestStatusEndpointFailure
		message = messages[status]
	}
	return appwire.AuthTestResponse{Provider: provider, Status: status, Message: message}
}

// formView renders the in-overlay create/edit form.
func (p CredentialsPanel) formView() string {
	var lines []string
	if p.formEditing {
		lines = append(lines,
			"Edit instance: "+p.formName,
			"",
			p.formFieldLine("Protocol", "protocol", p.protocolDisplay(), 0),
			p.formFieldLine("Base URL", "baseURL", p.formBaseURL, 1),
		)
		if p.clearedBaseURL() {
			lines = append(lines, "", lipgloss.NewStyle().Foreground(tuitheme.ActiveTheme().StateWarning).Render(clearedBaseURLNote))
		}
	} else {
		lines = append(lines,
			"New instance",
			"",
			p.formFieldLine("Base provider", "base", p.formBase, 0),
			p.formFieldLine("Name", "name", p.formName, 1),
			p.formFieldLine("Protocol", "protocol", p.protocolDisplay(), 2),
			p.formFieldLine("Base URL", "baseURL", p.formBaseURL, 3),
		)
	}
	return strings.Join(lines, "\n")
}

// clearedBaseURLNote tells the user what submitting an emptied base URL will
// do: appwire.InstanceEditParams.BaseURL clears the authored override on an
// explicit empty value (#711), so the instance falls back to its provider's
// default endpoint.
const clearedBaseURLNote = "Emptying this resets the endpoint to the provider's default."

// clearedBaseURL reports whether the edit form would submit an emptied base
// URL for an instance that had one.
func (p CredentialsPanel) clearedBaseURL() bool {
	return p.formEditing && strings.TrimSpace(p.formBaseURLWas) != "" && strings.TrimSpace(p.formBaseURL) == ""
}

func (p CredentialsPanel) protocolDisplay() string {
	if p.formProtocol == "" {
		return "(default)"
	}
	return p.formProtocol
}

func (p CredentialsPanel) formFieldLine(label, fieldName, value string, fieldIdx int) string {
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
