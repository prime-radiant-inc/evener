package launchconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

func loadedCredentials(instances ...appwire.InstanceEntry) CredentialsPanel {
	p := NewCredentialsPanel()
	updated, _ := p.Update(InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: instances}})
	return updated.(CredentialsPanel)
}

// --- Init / Done ---

func TestCovCredentialsInit(t *testing.T) {
	p := NewCredentialsPanel()
	if p.Init() != nil {
		t.Fatal("Init should return nil")
	}
}

func TestCovCredentialsDone(t *testing.T) {
	p := CredentialsPanel{done: true}
	if !p.Done() {
		t.Fatal("Done should be true")
	}
	p2 := NewCredentialsPanel()
	if p2.Done() {
		t.Fatal("Done should be false on new panel")
	}
}

// --- selectedInstance ---

func TestCovSelectedInstanceEmpty(t *testing.T) {
	p := CredentialsPanel{}
	if inst := p.selectedInstance(); inst != nil {
		t.Fatal("selectedInstance with no rows should return nil")
	}
}

func TestCovSelectedInstanceOutOfRange(t *testing.T) {
	p := CredentialsPanel{cursor: 5, rows: []panelRow{{entry: &appwire.InstanceEntry{Name: "x"}}}}
	if inst := p.selectedInstance(); inst != nil {
		t.Fatalf("selectedInstance out of range should return nil, got %+v", inst)
	}
}

func TestCovSelectedInstanceHeaderRow(t *testing.T) {
	p := CredentialsPanel{cursor: 0, rows: []panelRow{{header: true, typeName: "type"}}}
	if got := p.selectedInstance(); got != nil {
		t.Fatal("selectedInstance on header row should return nil")
	}
}

// --- nextSelectableRow ---

func TestCovNextSelectableRowEmpty(t *testing.T) {
	if idx := nextSelectableRow(nil, 0, 1); idx != -1 {
		t.Fatalf("nextSelectableRow(nil) = %d, want -1", idx)
	}
}

func TestCovNextSelectableRowNoMatch(t *testing.T) {
	// Only header rows, no selectable
	rows := []panelRow{{header: true}, {header: true}}
	if idx := nextSelectableRow(rows, 0, 1); idx != -1 {
		t.Fatalf("nextSelectableRow all-headers = %d, want -1", idx)
	}
}

// --- firstSelectableRow ---

func TestCovFirstSelectableRowEmpty(t *testing.T) {
	if idx := firstSelectableRow(nil); idx != -1 {
		t.Fatalf("firstSelectableRow(nil) = %d, want -1", idx)
	}
}

func TestCovFirstSelectableRowAllHeaders(t *testing.T) {
	rows := []panelRow{{header: true}, {header: true}}
	if idx := firstSelectableRow(rows); idx != -1 {
		t.Fatalf("firstSelectableRow all-headers = %d, want -1", idx)
	}
}

// --- Update: InstanceListResultMsg error ---

func TestCovCredentialsUpdateListError(t *testing.T) {
	p := NewCredentialsPanel()
	updated, _ := p.Update(InstanceListResultMsg{Err: errors.New("load fail")})
	p2 := updated.(CredentialsPanel)
	if p2.err == nil {
		t.Fatal("err should be set")
	}
	if p2.loading {
		t.Fatal("loading should be false after error")
	}
}

// --- Update: AuthTestResultMsg generation mismatch ---

func TestCovCredentialsAuthTestGenerationMismatch(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai"})
	wantResult := appwire.AuthTestResponse{Provider: "existing", Status: appwire.AuthTestStatusSuccess, Message: "keep me"}
	p.testResults = map[string]appwire.AuthTestResponse{"existing": wantResult}
	p.testPending = map[string]bool{"x": true}
	updated, _ := p.Update(AuthTestResultMsg{Generation: 999})
	p2 := updated.(CredentialsPanel)
	if !reflect.DeepEqual(p2.testResults, map[string]appwire.AuthTestResponse{"existing": wantResult}) {
		t.Fatalf("generation mismatch changed results to %+v", p2.testResults)
	}
	if !reflect.DeepEqual(p2.testPending, map[string]bool{"x": true}) {
		t.Fatalf("generation mismatch changed pending state to %+v", p2.testPending)
	}
}

// --- Update: AuthTestResultMsg empty provider name ---

func TestCovCredentialsAuthTestEmptyProvider(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai"})
	wantResult := appwire.AuthTestResponse{Provider: "existing", Status: appwire.AuthTestStatusSuccess, Message: "keep me"}
	p.testResults = map[string]appwire.AuthTestResponse{"existing": wantResult}
	p.testPending = map[string]bool{"x": true}
	// Both Provider and Response.Provider empty
	updated, _ := p.Update(AuthTestResultMsg{Generation: 1, Response: appwire.AuthTestResponse{}})
	p2 := updated.(CredentialsPanel)
	if !reflect.DeepEqual(p2.testResults, map[string]appwire.AuthTestResponse{"existing": wantResult}) {
		t.Fatalf("empty provider changed results to %+v", p2.testResults)
	}
	if !reflect.DeepEqual(p2.testPending, map[string]bool{"x": true}) {
		t.Fatalf("empty provider changed pending state to %+v", p2.testPending)
	}
}

// --- Update: AuthTestResultMsg with error ---

func TestCovCredentialsAuthTestWithError(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai"})
	// Need to match generation; loadedCredentials increments to 1
	updated, _ := p.Update(AuthTestResultMsg{
		Generation: 1,
		Provider:   "x",
		Err:        errors.New("test fail"),
	})
	p2 := updated.(CredentialsPanel)
	if p2.testResults == nil {
		t.Fatal("test results should be set even on error")
	}
	result := p2.testResults["x"]
	want := appwire.AuthTestResponse{
		Provider: "x",
		Status:   appwire.AuthTestStatusEndpointFailure,
		Message:  "The provider endpoint could not be reached. Check the endpoint and network connection.",
	}
	if result != want {
		t.Fatalf("error result = %+v, want %+v", result, want)
	}
}

// --- Update: AuthTestResultMsg success ---

func TestCovCredentialsAuthTestSuccess(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai"})
	updated, _ := p.Update(AuthTestResultMsg{
		Generation: 1,
		Provider:   "x",
		Response:   appwire.AuthTestResponse{Provider: "x", Status: appwire.AuthTestStatusSuccess, Message: "ok"},
	})
	p2 := updated.(CredentialsPanel)
	if p2.testResults == nil {
		t.Fatal("test results should be set on success")
	}
	want := appwire.AuthTestResponse{Provider: "x", Status: appwire.AuthTestStatusSuccess, Message: "Credentials verified."}
	if got := p2.testResults["x"]; got != want {
		t.Fatalf("success result = %+v, want %+v", got, want)
	}
	if p2.testPending["x"] {
		t.Fatal("pending should be cleared on success")
	}
}

// --- updateList: Enter on oauth-only instance ---

func TestCovCredentialsEnterOAuthOnly(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai", AuthModes: []string{"oauth"}})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on oauth instance should produce cmd")
	}
	msg := cmd().(CredentialsActionMsg)
	if msg.Action != "oauth" {
		t.Fatalf("action = %q, want oauth", msg.Action)
	}
}

// --- updateList: Enter on no matching auth mode ---

func TestCovCredentialsEnterNoAuthMode(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai", AuthModes: []string{}})
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter on instance with no matching auth mode should return nil cmd")
	}
}

// --- updateList: Enter on nil selection ---

func TestCovCredentialsEnterNilSelection(t *testing.T) {
	p := CredentialsPanel{}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter with no selection should return nil cmd")
	}
}

// --- updateList: 't' test when already pending ---

func TestCovCredentialsTestAlreadyPending(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai", AuthModes: []string{"apiKey"}})
	p.testPending = map[string]bool{"x": true}
	_, cmd := p.Update(runeKey("t"))
	if cmd != nil {
		t.Fatal("t on already-pending test should return nil cmd")
	}
}

// --- updateList: 'c' clear with nil selection ---

func TestCovCredentialsClearNilSelection(t *testing.T) {
	p := CredentialsPanel{}
	_, cmd := p.Update(runeKey("c"))
	if cmd != nil {
		t.Fatal("c with no selection should return nil cmd")
	}
}

// --- updateList: 'o' oauth with nil selection ---

func TestCovCredentialsOAuthNilSelection(t *testing.T) {
	p := CredentialsPanel{}
	_, cmd := p.Update(runeKey("o"))
	if cmd != nil {
		t.Fatal("o with no selection should return nil cmd")
	}
}

// --- updateList: '*' default with nil selection ---

func TestCovCredentialsDefaultNilSelection(t *testing.T) {
	p := CredentialsPanel{}
	_, cmd := p.Update(runeKey("*"))
	if cmd != nil {
		t.Fatal("* with no selection should return nil cmd")
	}
}

// --- updateList: 'x' remove with nil selection ---

func TestCovCredentialsRemoveNilSelection(t *testing.T) {
	p := CredentialsPanel{}
	_, cmd := p.Update(runeKey("x"))
	if cmd != nil {
		t.Fatal("x with no selection should return nil cmd")
	}
}

// --- updateList: 'e' edit with nil selection ---

func TestCovCredentialsEditNilSelection(t *testing.T) {
	p := CredentialsPanel{}
	_, cmd := p.Update(runeKey("e"))
	if cmd != nil {
		t.Fatal("e with no selection should return nil cmd")
	}
}

// --- updateList: 'n' opens create form ---

func TestCovCredentialsNewOpensForm(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai"})
	updated, _ := p.Update(runeKey("n"))
	p2 := updated.(CredentialsPanel)
	if !p2.formOpen {
		t.Fatal("n should open form")
	}
	if p2.formEditing {
		t.Fatal("form should not be in edit mode")
	}
}

// --- updateList: 'e' opens edit form ---

func TestCovCredentialsEditOpensForm(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "x", Type: "openai", APIStyle: "responses", BaseURL: "http://x"})
	updated, _ := p.Update(runeKey("e"))
	p2 := updated.(CredentialsPanel)
	if !p2.formOpen {
		t.Fatal("e should open form")
	}
	if !p2.formEditing {
		t.Fatal("form should be in edit mode")
	}
	if p2.formName != "x" {
		t.Fatalf("formName = %q, want x", p2.formName)
	}
}

// --- updateList: Down navigation ---

func TestCovCredentialsDown(t *testing.T) {
	p := loadedCredentials(
		appwire.InstanceEntry{Name: "a", Type: "openai"},
		appwire.InstanceEntry{Name: "b", Type: "openai"},
	)
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p2 := updated.(CredentialsPanel)
	if p2.cursor != 2 {
		t.Fatalf("Down should move to row 2 (first inst of type b is at 2: header+inst+header+inst), got %d", p2.cursor)
	}
}

// --- updateList: Up at top is no-op ---

func TestCovCredentialsUpAtTop(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "a", Type: "openai"})
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p2 := updated.(CredentialsPanel)
	if p2.cursor != 1 {
		t.Fatalf("Up at top should stay at 1, got %d", p2.cursor)
	}
}

// --- updateList: CtrlC closes ---

func TestCovCredentialsCtrlC(t *testing.T) {
	p := loadedCredentials(appwire.InstanceEntry{Name: "a", Type: "openai"})
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	p2 := updated.(CredentialsPanel)
	if !p2.done || !p2.cancelled {
		t.Fatal("CtrlC should set done and cancelled")
	}
}

// --- updateForm: Escape ---

func TestCovCredentialsFormEscape(t *testing.T) {
	p := CredentialsPanel{formOpen: true}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p2 := updated.(CredentialsPanel)
	if p2.formOpen {
		t.Fatal("Esc should close form")
	}
}

// --- updateForm: Enter advances create form fields ---

func TestCovCredentialsFormEnterAdvanceCreate(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formField: 0}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p2 := updated.(CredentialsPanel)
	if p2.formField != 1 {
		t.Fatalf("Enter should advance to field 1, got %d", p2.formField)
	}
}

// --- updateForm: Enter on last field submits create ---

func TestCovCredentialsFormSubmitCreate(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formField: 3, formType: "openai", formName: "x", formAPIStyle: "responses"}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on last field should submit")
	}
	msg := cmd().(InstanceCreateSubmitMsg)
	want := appwire.InstanceCreateParams{Type: "openai", Name: "x", APIStyle: "responses"}
	if msg.Params != want {
		t.Fatalf("params = %+v, want %+v", msg.Params, want)
	}
}

// --- updateForm: Enter on last field submits edit ---

func TestCovCredentialsFormSubmitEdit(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formEditing: true, formField: 1, formName: "x", formAPIStyle: "responses", formBaseURL: "http://x"}
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on last edit field should submit")
	}
	msg := cmd().(InstanceEditSubmitMsg)
	want := appwire.InstanceEditParams{Name: "x", APIStyle: "responses", BaseURL: "http://x"}
	if msg.Params != want {
		t.Fatalf("params = %+v, want %+v", msg.Params, want)
	}
}

// --- updateForm: Enter advances edit form ---

func TestCovCredentialsFormEnterAdvanceEdit(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formEditing: true, formField: 0}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p2 := updated.(CredentialsPanel)
	if p2.formField != 1 {
		t.Fatalf("Enter should advance edit to field 1, got %d", p2.formField)
	}
}

// --- updateForm: Backspace ---

func TestCovCredentialsFormBackspace(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formField: 1, formName: "abc"}
	updated, _ := p.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	p2 := updated.(CredentialsPanel)
	if p2.formName != "ab" {
		t.Fatalf("backspace should delete last char, got %q", p2.formName)
	}
}

// --- updateForm: Runes on apiStyle field (edit mode) toggles ---

func TestCovCredentialsFormEditAPIStyleToggle(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formEditing: true, formField: 0, formAPIStyle: ""}
	updated, _ := p.Update(runeKey(" "))
	p2 := updated.(CredentialsPanel)
	if p2.formAPIStyle != "chat-completions" {
		t.Fatalf("space on apiStyle should toggle to chat-completions, got %q", p2.formAPIStyle)
	}
}

// --- updateForm: Runes on apiStyle field (create mode) toggles ---

func TestCovCredentialsFormCreateAPIStyleToggle(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formField: 2, formAPIStyle: "chat-completions"}
	updated, _ := p.Update(runeKey(" "))
	p2 := updated.(CredentialsPanel)
	if p2.formAPIStyle != "responses" {
		t.Fatalf("space on apiStyle should toggle to responses, got %q", p2.formAPIStyle)
	}
}

// --- updateForm: Runes on apiStyle field non-space is ignored ---

func TestCovCredentialsFormAPIStyleNonSpaceIgnored(t *testing.T) {
	p := CredentialsPanel{formOpen: true, formField: 2, formAPIStyle: "responses"}
	updated, _ := p.Update(runeKey("x"))
	p2 := updated.(CredentialsPanel)
	if p2.formAPIStyle != "responses" {
		t.Fatalf("non-space on apiStyle should be ignored, got %q", p2.formAPIStyle)
	}
}

// --- safeCredentialTestResult ---

func TestCovSafeCredentialTestResultUnknownStatus(t *testing.T) {
	resp := safeCredentialTestResult("x", appwire.AuthTestResponse{Provider: "x", Status: "unknown-status"})
	want := appwire.AuthTestResponse{
		Provider: "x",
		Status:   appwire.AuthTestStatusEndpointFailure,
		Message:  "The provider endpoint could not be reached. Check the endpoint and network connection.",
	}
	if resp != want {
		t.Fatalf("unknown status result = %+v, want %+v", resp, want)
	}
}

func TestCovSafeCredentialTestResultAllKnownStatuses(t *testing.T) {
	cases := []struct {
		status  string
		message string
	}{
		{appwire.AuthTestStatusSuccess, "Credentials verified."},
		{appwire.AuthTestStatusMissing, "No credentials are configured for this instance. Add a key or sign in first."},
		{appwire.AuthTestStatusAuthRejected, "The provider rejected these credentials. Replace the key or sign in again."},
		{appwire.AuthTestStatusEndpointFailure, "The provider endpoint could not be reached. Check the endpoint and network connection."},
		{appwire.AuthTestStatusConfigurationFailure, "Provider configuration could not be loaded. Check the instance settings."},
		{appwire.AuthTestStatusUnsupported, "This provider does not support harmless credential verification."},
	}
	for _, tc := range cases {
		resp := safeCredentialTestResult("x", appwire.AuthTestResponse{Provider: "ignored", Status: tc.status, Message: "untrusted wire text"})
		want := appwire.AuthTestResponse{Provider: "x", Status: tc.status, Message: tc.message}
		if resp != want {
			t.Errorf("status %q result = %+v, want %+v", tc.status, resp, want)
		}
	}
}

// --- formView ---

func TestCovCredentialsFormViewCreate(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{formOpen: true, formEditing: false, formType: "openai", formName: "x", formAPIStyle: "responses"}
	v := p.formView()
	if !strings.Contains(v, "New instance") {
		t.Fatalf("create formView should show 'New instance': %q", v)
	}
}

func TestCovCredentialsFormViewEdit(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{formOpen: true, formEditing: true, formName: "x", formAPIStyle: "responses", formBaseURL: "http://x"}
	v := p.formView()
	if !strings.Contains(v, "Edit instance: x") {
		t.Fatalf("edit formView should show 'Edit instance: x': %q", v)
	}
}

// --- formFieldLine ---

func TestCovCredentialsFormFieldLine(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{formOpen: true, formField: 0}
	line := p.formFieldLine("Test", "type", "val", 0)
	if !strings.Contains(line, "Test") {
		t.Fatalf("formFieldLine should contain label: %q", line)
	}
}

// --- apiStyleDisplay ---

func TestCovCredentialsAPIStyleDisplay(t *testing.T) {
	p := CredentialsPanel{formAPIStyle: ""}
	if got := p.apiStyleDisplay(); got != "(default)" {
		t.Fatalf("empty apiStyle display = %q, want (default)", got)
	}
	p2 := CredentialsPanel{formAPIStyle: "responses"}
	if got := p2.apiStyleDisplay(); got != "responses" {
		t.Fatalf("responses apiStyle display = %q, want responses", got)
	}
}

// --- View: loading ---

func TestCovCredentialsViewLoading(t *testing.T) {
	withTestColorProfile(t)
	p := NewCredentialsPanel()
	v := p.View()
	if !strings.Contains(v, "Loading") {
		t.Fatalf("loading view should show Loading: %q", v)
	}
}

// --- View: error ---

func TestCovCredentialsViewError(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{err: errors.New("load fail")}
	v := p.View()
	if !strings.Contains(v, "Error: load fail") {
		t.Fatalf("error view should show error: %q", v)
	}
}

// --- View: form open ---

func TestCovCredentialsViewForm(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{formOpen: true, formEditing: false}
	v := p.View()
	if !strings.Contains(v, "New instance") {
		t.Fatalf("view with form should show form: %q", v)
	}
}

// --- View: test pending and results ---

func TestCovCredentialsViewTestPending(t *testing.T) {
	withTestColorProfile(t)
	inst := appwire.InstanceEntry{Name: "x", Type: "openai", ActiveSource: "oauth"}
	p := CredentialsPanel{
		rows:        buildPanelRows([]appwire.InstanceEntry{inst}),
		cursor:      0,
		testPending: map[string]bool{"x": true},
	}
	v := p.View()
	if !strings.Contains(v, "Testing credentials") {
		t.Fatalf("view should show testing status: %q", v)
	}
}

func TestCovCredentialsViewTestResult(t *testing.T) {
	withTestColorProfile(t)
	inst := appwire.InstanceEntry{Name: "x", Type: "openai", ActiveSource: "oauth"}
	p := CredentialsPanel{
		rows:   buildPanelRows([]appwire.InstanceEntry{inst}),
		cursor: 0,
		testResults: map[string]appwire.AuthTestResponse{
			"x": {Provider: "x", Status: "success", Message: "ok"},
		},
	}
	v := p.View()
	if !strings.Contains(v, "success: ok") {
		t.Fatalf("view should show test result: %q", v)
	}
}

// --- View: with APIStyle/BaseURL hint ---

func TestCovCredentialsViewHint(t *testing.T) {
	withTestColorProfile(t)
	inst := appwire.InstanceEntry{Name: "x", Type: "openai", ActiveSource: "oauth", APIStyle: "responses", BaseURL: "http://x"}
	p := CredentialsPanel{rows: buildPanelRows([]appwire.InstanceEntry{inst}), cursor: 0}
	v := p.View()
	if !strings.Contains(v, "responses") || !strings.Contains(v, "http://x") {
		t.Fatalf("view should show hint: %q", v)
	}
}

// --- Update: unknown message type ---

func TestCovCredentialsUpdateUnknownMsg(t *testing.T) {
	p := NewCredentialsPanel()
	p.formName = "keep"
	p.testGeneration = 7
	updated, cmd := p.Update("unknown msg")
	if cmd != nil {
		t.Fatal("unknown msg should return nil cmd")
	}
	if got := updated.(CredentialsPanel); !reflect.DeepEqual(got, p) {
		t.Fatalf("unknown msg changed panel to %+v, want %+v", got, p)
	}
}

// --- credentialBadge ---

func TestCovCredentialBadgeOptional(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{}
	badge := p.credentialBadge(appwire.InstanceEntry{ActiveSource: "absent", CredentialRequired: false})
	if !strings.Contains(badge, "OPTIONAL") {
		t.Fatalf("absent non-required credential should show optional: %q", badge)
	}
}
