package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/launchconfig"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/internal/appwire"
)

func (m hubModel) updateSpawnKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.followupModal != nil && m.launchOverridesModal != nil {
		updated, cmd := m.followupModal.Update(msg)
		modal := updated.(textInputModal)
		m.followupModal = &modal
		if modal.done {
			m.followupModal = nil
		}
		return m, cmd
	}

	if m.launchOverridesModal != nil {
		updated, cmd := m.launchOverridesModal.Update(msg)
		p := updated.(launchconfig.LaunchOverridesModal)
		m.launchOverridesModal = &p
		if p.Done() {
			m.launchOverridesModal = nil
		}
		return m, cmd
	}

	if m.spawnModelPicker != nil {
		updated, cmd := m.spawnModelPicker.Update(msg)
		picker := updated.(modelPicker)
		m.spawnModelPicker = &picker
		if picker.done {
			m.spawnModelPicker = nil
			if picker.selected != "" {
				m.spawnModel = picker.selected
			}
		}
		return m, cmd
	}

	if msg.Type == tea.KeyCtrlL {
		var initial *appwire.LaunchConfigLayer
		if m.spawnLaunchOverrides != nil {
			cp := *m.spawnLaunchOverrides
			initial = &cp
		}
		return m, func() tea.Msg { return launchconfig.LaunchOverridesOpenMsg{Initial: initial} }
	}

	switch msg.String() {
	case "esc":
		m.closeSpawnForm()
		return m, nil
	case "tab":
		if m.spawnFocus == hubSpawnFieldDir {
			current := m.spawnDirInput.Value()
			// Spawn working-dir accepts directories only; without this
			// filter Tab could land a file path in the field which the
			// later submit validation would reject.
			completed := completeLastPathSegment(current, dirEntry())
			if completed != current {
				m.spawnDirInput.SetValue(completed)
				m.spawnDir = strings.TrimSpace(completed)
				return m, nil
			}
		}
		m.advanceSpawnFocus(1)
		return m, nil
	case "shift+tab":
		m.advanceSpawnFocus(-1)
		return m, nil
	case "enter":
		switch m.spawnFocus {
		case hubSpawnFieldHarness:
			m.cycleSpawnHarness()
			return m, nil
		case hubSpawnFieldModel:
			return m.activateSpawnModelField()
		case hubSpawnFieldDir:
			m.advanceSpawnFocus(1)
			return m, nil
		default:
			return m.submitSpawnForm()
		}
	case " ":
		if m.spawnFocus == hubSpawnFieldHarness {
			m.cycleSpawnHarness()
			return m, nil
		}
		if m.spawnFocus == hubSpawnFieldModel {
			return m.activateSpawnModelField()
		}
	}

	if m.spawnFocus == hubSpawnFieldDir {
		if msg.Type == tea.KeyCtrlU {
			m.setSpawnDir("")
			return m, nil
		}
		var cmd tea.Cmd
		m.spawnDirInput, cmd = m.spawnDirInput.Update(msg)
		m.spawnDir = strings.TrimSpace(m.spawnDirInput.Value())
		return m, cmd
	}

	if m.spawnFocus != hubSpawnFieldPrompt {
		return m, nil
	}

	if (msg.Type == tea.KeyEnter && msg.Alt) || msg.Type == tea.KeyCtrlJ {
		m.session.input.InsertString("\n")
		m.resizeSpawnInput()
		return m, nil
	}

	prevHeight := m.session.input.Height()
	var cmd tea.Cmd
	m.session.input, cmd = m.session.input.Update(msg)
	m.resizeSpawnInputFrom(prevHeight)
	return m, cmd
}

func (m hubModel) activateSpawnModelField() (tea.Model, tea.Cmd) {
	models := m.spawnSelectableModels()
	if len(models) == 0 && !m.spawnHarnessUsesSerfModels() && m.client != nil {
		m.err = nil
		return m, fetchHubModelsForHarness(m.client, m.spawnHarness, m.spawnDir)
	}
	if len(models) == 0 {
		if !m.spawnHarnessUsesSerfModels() {
			m.err = fmt.Errorf("no %s models available; using harness default", m.spawnHarness)
		} else {
			m.err = fmt.Errorf("no models available")
		}
		return m, nil
	}
	m.openSpawnModelPicker(models)
	return m, nil
}

func (m hubModel) submitSpawnForm() (tea.Model, tea.Cmd) {
	if m.client == nil || m.spawnSubmitting {
		return m, nil
	}
	prompt := strings.TrimSpace(m.session.input.Value())
	if prompt == "" {
		if reason := m.spawnEmptyTaskUnsupportedReason(); reason != "" {
			m.err = fmt.Errorf("%s", noticePanel{
				Title:      "Spawn unavailable",
				Source:     strings.TrimSpace(m.spawnHarness),
				Reason:     reason,
				NextAction: m.spawnEmptyTaskUnsupportedNextAction(),
			}.Text())
			return m, nil
		}
	}
	if m.spawnHarnessUsesSerfModels() && strings.TrimSpace(m.spawnModel) == "" {
		m.err = fmt.Errorf("choose a model before spawning")
		return m, nil
	}
	if reason := m.spawnModelDisabledReason(strings.TrimSpace(m.spawnModel)); reason != "" {
		m.err = fmt.Errorf("%s", noticePanel{
			Title:      "Spawn unavailable",
			Source:     strings.TrimSpace(m.spawnHarness),
			Reason:     "selected model is not available: " + reason,
			NextAction: "choose an enabled model or resolve the provider requirement",
		}.Text())
		return m, nil
	}
	req := hubSpawnRequest{
		Prompt:          prompt,
		Harness:         strings.TrimSpace(m.spawnHarness),
		Model:           strings.TrimSpace(m.spawnModel),
		WorkingDir:      strings.TrimSpace(m.spawnDir),
		LaunchOverrides: m.spawnLaunchOverrides,
	}
	m.err = nil
	m.spawnSubmitting = true
	m.spawnLaunchOverrides = nil // one-shot: clear after use
	return m, sendHubSpawn(m.client, req)
}

func (m *hubModel) setSpawnFocus(field hubSpawnField) {
	if field < hubSpawnFieldPrompt || field > hubSpawnFieldDir {
		field = hubSpawnFieldPrompt
	}
	m.spawnFocus = field
	if field == hubSpawnFieldPrompt {
		m.session.input.Focus()
		m.spawnDirInput.Blur()
		return
	}
	m.session.input.Blur()
	if field == hubSpawnFieldDir {
		if strings.TrimSpace(m.spawnDirInput.Value()) == "" && strings.TrimSpace(m.spawnDir) != "" {
			m.spawnDirInput.SetValue(strings.TrimSpace(m.spawnDir))
		}
		m.spawnDirInput.Focus()
		return
	}
	m.spawnDirInput.Blur()
}

func (m *hubModel) advanceSpawnFocus(delta int) {
	next := int(m.spawnFocus) + delta
	count := int(hubSpawnFieldDir) + 1
	for next < 0 {
		next += count
	}
	next %= count
	m.setSpawnFocus(hubSpawnField(next))
}

func (m *hubModel) resizeSpawnInput() {
	m.resizeSpawnInputFrom(m.session.input.Height())
}

func (m *hubModel) resizeSpawnInputFrom(prevHeight int) {
	wantHeight := m.session.input.LineCount()
	if wantHeight < 1 {
		wantHeight = 1
	}
	if wantHeight > m.session.input.MaxHeight {
		wantHeight = m.session.input.MaxHeight
	}
	if wantHeight != prevHeight {
		m.session.input.SetHeight(wantHeight)
	}
}

func (m hubModel) spawnFieldPrefix(field hubSpawnField) string {
	if m.spawnFocus == field {
		return ">"
	}
	return " "
}

func (m hubModel) spawnFieldHint() string {
	switch m.spawnFocus {
	case hubSpawnFieldHarness:
		return "enter/space: change harness"
	case hubSpawnFieldModel:
		if !m.spawnHarnessUsesSerfModels() && len(m.spawnSelectableModels()) == 0 {
			return "enter: fetch harness models"
		}
		return "enter: choose model"
	case hubSpawnFieldDir:
		return "type path  tab: complete  enter: next  ctrl+u clear"
	default:
		return "enter: spawn  ctrl+j: newline"
	}
}

func (m *hubModel) openSpawnForm() {
	returnMode := m.mode
	dir := m.spawnWorkingDir()
	project := m.spawnProjectName()
	m.resetSpawnForm()
	m.spawnReturnMode = returnMode
	m.setSpawnDir(dir)
	m.spawnProject = project
	m.mode = hubModeSpawn
	m.err = nil
	m.setSpawnFocus(hubSpawnFieldPrompt)
}

func (m *hubModel) closeSpawnForm() {
	m.resetSpawnForm()
	m.mode = hubModeDashboard
	m.clampSelection()
}

func (m *hubModel) resetSpawnForm() {
	m.spawnReturnMode = hubModeDashboard
	m.setSpawnDir("")
	m.spawnProject = ""
	m.spawnHarness = "serf"
	m.spawnHarnesses = []string{"serf"}
	m.spawnHarnessKinds = map[string]string{"serf": "serf"}
	m.spawnEmptyTaskReasons = nil
	m.spawnEmptyTaskNext = nil
	m.spawnModel = ""
	m.spawnModels = nil
	m.spawnHarnessModels = nil
	m.spawnModelPicker = nil
	m.spawnSubmitting = false
	m.spawnFocus = hubSpawnFieldPrompt
	m.spawnDirInput.Blur()
	m.session.resetInput()
	if envModel := strings.TrimSpace(os.Getenv("SERF_MODEL")); strings.Contains(envModel, "/") {
		m.spawnModel = envModel
	}
}

func (m *hubModel) setSpawnDir(dir string) {
	dir = strings.TrimSpace(dir)
	m.spawnDir = dir
	m.spawnDirInput = newSpawnDirInput()
	m.spawnDirInput.SetValue(dir)
	if m.spawnFocus == hubSpawnFieldDir {
		m.spawnDirInput.Focus()
	}
}

func (m *hubModel) cycleSpawnHarness() {
	if len(m.spawnHarnesses) == 0 {
		m.spawnHarnesses = []string{"serf"}
	}
	for i, harness := range m.spawnHarnesses {
		if harness == m.spawnHarness {
			m.spawnHarness = m.spawnHarnesses[(i+1)%len(m.spawnHarnesses)]
			m.spawnModel = ""
			m.spawnModelPicker = nil
			m.syncSpawnModelWithHarness()
			return
		}
	}
	m.spawnHarness = m.spawnHarnesses[0]
	m.spawnModel = ""
	m.spawnModelPicker = nil
	m.syncSpawnModelWithHarness()
}

func (m hubModel) spawnHarnessKind() string {
	if kind := strings.TrimSpace(m.spawnHarnessKinds[m.spawnHarness]); kind != "" {
		return kind
	}
	return "serf"
}

func (m hubModel) spawnHarnessUsesSerfModels() bool {
	return m.spawnHarnessKind() != "codex"
}

func (m hubModel) spawnSelectableModels() []modelPickerItem {
	if !m.spawnHarnessUsesSerfModels() {
		return m.spawnHarnessModels[m.spawnHarness]
	}
	return m.spawnModels
}

func (m *hubModel) syncSpawnModelWithHarness() {
	if !m.spawnHarnessUsesSerfModels() {
		if strings.Contains(strings.TrimSpace(m.spawnModel), "/") {
			m.spawnModel = ""
		}
		m.spawnModelPicker = nil
		return
	}
	if strings.TrimSpace(m.spawnModel) == "" {
		models := m.spawnSelectableModels()
		if model, ok := firstEnabledModel(models); ok {
			m.spawnModel = model.id
		}
	}
}

func firstEnabledModel(models []modelPickerItem) (modelPickerItem, bool) {
	for _, model := range models {
		if strings.TrimSpace(model.disabledReason) == "" {
			return model, true
		}
	}
	return modelPickerItem{}, false
}

func (m hubModel) spawnModelDisabledReason(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	for _, item := range m.spawnSelectableModels() {
		if strings.TrimSpace(item.id) == model || strings.TrimSpace(item.display) == model {
			return strings.TrimSpace(item.disabledReason)
		}
	}
	return ""
}

func (m hubModel) spawnEmptyTaskUnsupportedReason() string {
	if m.spawnEmptyTaskReasons == nil {
		return ""
	}
	return strings.TrimSpace(m.spawnEmptyTaskReasons[m.spawnHarness])
}

func (m hubModel) spawnEmptyTaskUnsupportedNextAction() string {
	if m.spawnEmptyTaskNext == nil {
		return ""
	}
	return strings.TrimSpace(m.spawnEmptyTaskNext[m.spawnHarness])
}

func (m *hubModel) openSpawnModelPicker(models []modelPickerItem) {
	picker := newModelPicker(models, m.spawnModel, m.width)
	picker.title = m.spawnModelPickerTitle()
	m.spawnModelPicker = &picker
	m.err = nil
}

func (m hubModel) spawnModelPickerTitle() string {
	if m.spawnHarnessUsesSerfModels() {
		return "Select spawn model"
	}
	return "Select " + m.spawnHarness + " model"
}

func (m hubModel) spawnHarnessModelDisplay() string {
	model := strings.TrimSpace(m.spawnModel)
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	return m.spawnHarness + "/" + model
}

func (m hubModel) spawnDirView() string {
	if m.spawnFocus == hubSpawnFieldDir {
		return m.spawnDirInput.View()
	}
	if dir := strings.TrimSpace(m.spawnDir); dir != "" {
		return dir
	}
	return "(hub default)"
}

func stringInSlice(needle string, haystack []string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func (m hubModel) spawnWorkingDir() string {
	row, ok := m.selectedDashboardRow()
	if !ok {
		return ""
	}
	return m.workingDirForProjectKey(row.projectKey)
}

func (m hubModel) spawnProjectName() string {
	row, ok := m.selectedDashboardRow()
	if !ok || row.kind == hubRowLaunch {
		return ""
	}
	return row.project
}

func (m hubModel) spawnView() string {
	var b strings.Builder
	topBar := "serf / new session"
	var overlay string
	if m.spawnModelPicker != nil {
		overlay = m.spawnModelPicker.View()
	}
	if m.launchOverridesModal != nil {
		overlay = m.launchOverridesModal.View()
	}
	if m.followupModal != nil {
		overlay = m.followupModal.View()
	}
	model := m.spawnModel
	models := m.spawnSelectableModels()
	if !m.spawnHarnessUsesSerfModels() {
		if model == "" {
			model = "(harness default)"
		} else {
			model = m.spawnHarnessModelDisplay()
		}
	} else if model == "" && len(models) == 0 {
		model = "(loading models...)"
	} else if model == "" {
		model = "(choose a model)"
	}
	fmt.Fprintf(&b, "%s Harness:  %s\n", m.spawnFieldPrefix(hubSpawnFieldHarness), m.spawnHarness)
	fmt.Fprintf(&b, "%s Model:    %s\n", m.spawnFieldPrefix(hubSpawnFieldModel), model)
	if m.spawnProject != "" {
		fmt.Fprintf(&b, "  Project:  %s\n", m.spawnProject)
	}
	fmt.Fprintf(&b, "%s Dir:      %s\n", m.spawnFieldPrefix(hubSpawnFieldDir), m.spawnDirView())
	fmt.Fprintf(&b, "%s Prompt (optional):\n", m.spawnFieldPrefix(hubSpawnFieldPrompt))
	for _, line := range strings.Split(strings.TrimSuffix(renderComposerDraft(m.session.input.Value(), m.width-2, 0), "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.err)
	}
	if notices := m.renderNotices(); notices != "" {
		b.WriteString("\n")
		b.WriteString(notices)
	}
	if m.spawnSubmitting {
		b.WriteString("\nStarting session...\n")
	}

	var footer strings.Builder
	keys := []string{"tab: next field", "shift+tab: previous", m.spawnFieldHint(), "esc: cancel", "ctrl+o: dashboard"}
	footer.WriteString(tuiprim.ActionBarForWidth(m.width, keys...))
	return tuiprim.AppShell{
		TopBar:  topBar,
		Body:    b.String(),
		Overlay: overlay,
		Footer:  footer.String(),
		Height:  m.height,
	}.View()
}
