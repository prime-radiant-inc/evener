package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/internal/appserver"
)

// Tests for the hub's applied-write discriminators on provider-instance
// mutations: a removal whose credential deletion applied, or a rename whose
// new name reached providers.toml, is a standing write, not a failed one. The
// model has to reconcile it - refresh the listing, move or follow the panel's
// selection, and warn - instead of leaving the panel stale under the error
// line while it waits for a passive notification that may never arrive.

// instanceListForTest builds the listing the panel renders for a set of names
// that all resolve to one provider, so the group header is stable.
func instanceListForTest(names ...string) appwire.InstanceListResponse {
	instances := make([]appwire.InstanceEntry, 0, len(names))
	for _, name := range names {
		instances = append(instances, appwire.InstanceEntry{Name: name, ProviderID: "openai"})
	}
	return appwire.InstanceListResponse{Instances: instances}
}

// panelCursorOn reports whether the rendered cursor ("> ") sits on the row
// naming `name`. The panel's selection is only observable through its view.
func panelCursorOn(p *launchconfig.CredentialsPanel, name string) bool {
	if p == nil {
		return false
	}
	for line := range strings.SplitSeq(ansi.Strip(p.View()), "\n") {
		if strings.Contains(line, name) {
			return strings.Contains(line, "> ")
		}
	}
	return false
}

// instanceWarningNotice returns the warning notice an applied write leaves,
// failing the test when the model carries none.
func instanceWarningNotice(t *testing.T, m hubModel) noticePanel {
	t.Helper()
	for _, notice := range m.notices {
		if notice.State == "warning" && strings.HasPrefix(notice.Title, "Instance") {
			return notice
		}
	}
	t.Fatalf("no instance warning notice after an applied write; notices = %#v", m.notices)
	return noticePanel{}
}

// TestInstanceRemoveAppliedReconcilesAndWarns: the hub answers a removal whose
// credential deletion stood with ErrorInstanceRemoveApplied and no listing.
// The model must not put that on the error line; it refreshes the instance
// list (the passive evener/auth/updated notification may be late or lost),
// lets the panel clamp its cursor off the removed row, and warns.
func TestInstanceRemoveAppliedReconcilesAndWarns(t *testing.T) {
	applied := appwire.InstanceRemoveApplied("removed the stored key for old; providers.toml could not be rewritten")
	listCalls := 0
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerInstanceRemove, func(_ context.Context, _ appwire.InstanceRemoveParams) (appwire.InstanceListResponse, error) {
			return appwire.InstanceListResponse{}, applied
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerInstanceList, func(context.Context, appwire.EmptyParams) (appwire.InstanceListResponse, error) {
			listCalls++
			return instanceListForTest("other"), nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	panel := newCredentialsPanelForTest()
	m.credentialsPanel = panel
	loaded, _ := m.credentialsPanel.Update(launchconfig.InstanceListResultMsg{List: instanceListForTest("old", "other")})
	loadedPanel := loaded.(launchconfig.CredentialsPanel)
	m.credentialsPanel = &loadedPanel
	if !panelCursorOn(m.credentialsPanel, "old") {
		t.Fatalf("fixture: cursor should start on old:\n%s", ansi.Strip(m.credentialsPanel.View()))
	}

	_, cmd := m.handleInstanceRemove(launchconfig.InstanceRemoveMsg{Name: "old", EndpointFingerprint: "fp-old"})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	result, ok := cmd().(launchconfig.InstanceMutateResultMsg)
	if !ok || result.Err == nil {
		t.Fatalf("remove result = %#v, want the applied-write error carried", result)
	}

	updated, refresh := m.handleInstanceMutateResult(result)
	after := updated.(hubModel)
	if after.err != nil {
		t.Fatalf("an applied removal must not surface as a plain failure: %v", after.err)
	}
	if refresh == nil {
		t.Fatal("an applied removal must refresh the instance list instead of waiting for the notification")
	}
	notice := instanceWarningNotice(t, after)
	if !strings.Contains(notice.Summary, "removal") {
		t.Fatalf("warning summary = %q, want the standing removal named", notice.Summary)
	}
	if !strings.Contains(notice.Reason, "stored key for old") {
		t.Fatalf("warning reason = %q, want the hub's own account of what was left behind", notice.Reason)
	}

	refreshed, ok := refresh().(launchconfig.InstanceListResultMsg)
	if !ok || refreshed.Err != nil || listCalls != 1 {
		t.Fatalf("refresh result = %#v, listCalls = %d", refreshed, listCalls)
	}
	withList, _ := after.handleInstanceList(refreshed)
	if !panelCursorOn(withList.(hubModel).credentialsPanel, "other") {
		t.Fatalf("the panel must clamp its selection as a removal does:\n%s", ansi.Strip(withList.(hubModel).credentialsPanel.View()))
	}
}

// TestInstanceRenamePersistedFollowsTheNewNameAndRefreshes: the hub answers a
// rename that stood with ErrorInstanceRenamePersisted and no listing. The
// model already knows the new name (the params' newName) and must follow the
// instance to it - even when the hub's refreshed registry lags and omits the
// new row - rather than leave the cursor and the error line on the failed save.
func TestInstanceRenamePersistedFollowsTheNewNameAndRefreshes(t *testing.T) {
	persisted := appwire.InstanceRenamePersisted("moved the stored key to new; the old record could not be removed")
	listCalls := 0
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerInstanceEdit, func(_ context.Context, got appwire.InstanceEditParams) (appwire.InstanceListResponse, error) {
			if got.Name != "old" || got.NewName != "new" {
				t.Errorf("edit params = %#v, want the rename old -> new", got)
			}
			return appwire.InstanceListResponse{}, persisted
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerInstanceList, func(context.Context, appwire.EmptyParams) (appwire.InstanceListResponse, error) {
			listCalls++
			if listCalls == 1 {
				// The hub's registry has not caught up: the new row is missing
				// from the listing the applied rename triggered.
				return instanceListForTest("aaa", "other"), nil
			}
			return instanceListForTest("aaa", "new", "other"), nil
		})
	})
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	panel := newCredentialsPanelForTest()
	m.credentialsPanel = panel
	loaded, _ := m.credentialsPanel.Update(launchconfig.InstanceListResultMsg{List: instanceListForTest("old", "other")})
	loadedPanel := loaded.(launchconfig.CredentialsPanel)
	m.credentialsPanel = &loadedPanel

	_, cmd := m.handleInstanceEditSubmit(launchconfig.InstanceEditSubmitMsg{
		Params: appwire.InstanceEditParams{Name: "old", NewName: "new"},
	})
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	result, ok := cmd().(launchconfig.InstanceMutateResultMsg)
	if !ok || result.Err == nil {
		t.Fatalf("edit result = %#v, want the persisted-rename error carried", result)
	}
	if result.RenameTo != "new" {
		t.Fatalf("result.RenameTo = %q, want the submitted new name carried with the result", result.RenameTo)
	}

	updated, refresh := m.handleInstanceMutateResult(result)
	after := updated.(hubModel)
	if after.err != nil {
		t.Fatalf("a persisted rename must not surface as a plain failure: %v", after.err)
	}
	if refresh == nil {
		t.Fatal("a persisted rename must refresh the instance list")
	}
	notice := instanceWarningNotice(t, after)
	if !strings.Contains(notice.Summary, "new") {
		t.Fatalf("warning summary = %q, want the standing rename to its new name", notice.Summary)
	}

	first, ok := refresh().(launchconfig.InstanceListResultMsg)
	if !ok || first.Err != nil || listCalls != 1 {
		t.Fatalf("refresh result = %#v, listCalls = %d", first, listCalls)
	}
	// The first listing omits the new name, so the cursor stays where the
	// clamp put it; the follow is not gated on one listing's verdict.
	withFirst, _ := after.handleInstanceList(first)
	if panelCursorOn(withFirst.(hubModel).credentialsPanel, "new") {
		t.Fatalf("fixture: the first listing omits the new row, cursor cannot be on it")
	}
	// The registry catches up: the panel must follow the standing rename to
	// its new name.
	second := launchconfig.InstanceListResultMsg{List: instanceListForTest("aaa", "new", "other")}
	withSecond, _ := withFirst.(hubModel).handleInstanceList(second)
	if !panelCursorOn(withSecond.(hubModel).credentialsPanel, "new") {
		t.Fatalf("the panel must follow the rename to its new name:\n%s", ansi.Strip(withSecond.(hubModel).credentialsPanel.View()))
	}
	// Once followed, the selection is the user's again: a later listing that
	// still names the instance must not keep moving the cursor.
	withThird, _ := withSecond.(hubModel).handleInstanceList(launchconfig.InstanceListResultMsg{List: instanceListForTest("aaa", "new", "other")})
	if !panelCursorOn(withThird.(hubModel).credentialsPanel, "new") {
		t.Fatalf("the followed selection should stay on the name it followed")
	}
}

// TestInstanceMutatePlainFailureKeepsTheErrorPath: an ordinary refusal (a
// missing instance, a stale listing) keeps today's behavior - the error line,
// no refresh, no warning - so the reconciliation above stays scoped to the
// applied-write discriminators and the endpoint-conflict class.
func TestInstanceMutatePlainFailureKeepsTheErrorPath(t *testing.T) {
	refused := appwire.InvalidParams(`instance "old" not found`)
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	panel := newCredentialsPanelForTest()
	m.credentialsPanel = panel

	updated, cmd := m.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: refused})
	after := updated.(hubModel)
	if !errors.Is(after.err, refused) {
		t.Fatalf("model err = %v, want the refusal on the error line", after.err)
	}
	if cmd != nil {
		t.Fatal("a plain refusal must not refresh the instance list")
	}
	if len(after.notices) != 0 {
		t.Fatalf("a plain refusal must not be warned: %#v", after.notices)
	}
}

// TestInstanceAppliedClassifierReadsBothWireShapes: the client decodes a hub
// error's data as a map, while locally constructed errors carry the typed
// ErrorData; both must classify, and an unrelated wire error must not.
func TestInstanceAppliedClassifierReadsBothWireShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want appwire.ErrorInfo
	}{
		{"typed remove", appwire.InstanceRemoveApplied("x"), appwire.ErrorInstanceRemoveApplied},
		{"typed rename", appwire.InstanceRenamePersisted("x"), appwire.ErrorInstanceRenamePersisted},
		{"decoded remove", appwire.WireError{Code: appwire.CodeInternalError, Message: "x", Data: map[string]any{"evenerErrorInfo": string(appwire.ErrorInstanceRemoveApplied)}}, appwire.ErrorInstanceRemoveApplied},
		{"decoded rename", appwire.WireError{Code: appwire.CodeInternalError, Message: "x", Data: map[string]any{"evenerErrorInfo": string(appwire.ErrorInstanceRenamePersisted)}}, appwire.ErrorInstanceRenamePersisted},
		{"other wire error", appwire.Conflict("x"), ""},
		{"plain error", errors.New("x"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := instanceAppliedErrorInfo(tc.err); got != tc.want {
				t.Fatalf("instanceAppliedErrorInfo(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

var _ tea.Msg = launchconfig.InstanceMutateResultMsg{}
