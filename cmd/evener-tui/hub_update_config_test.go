package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
)

// TestConfigResultHandlersSurfaceErrorsAndClearThemOnSuccess pins the contract
// every config result handler shares: a result carrying Err puts that error on
// the model and returns WITHOUT refreshing any panel, and a result without Err
// clears a previously displayed error.
//
// The no-refresh half is the one that matters. Each success path feeds the
// message's List into a panel; taking that path on a failed mutation would
// repaint the panel from a list the mutation never produced — typically the
// zero value — so a failed remove would render as a successful one. Handling
// them as a table also keeps the shared shape from drifting apart one handler
// at a time.
func TestConfigResultHandlersSurfaceErrorsAndClearThemOnSuccess(t *testing.T) {
	boom := errors.New("mutation refused by the hub")

	for _, tc := range []struct {
		name string
		call func(m hubModel, err error) (tea.Model, tea.Cmd)
	}{
		{"instance mutate", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: err})
		}},
		{"marketplace mutate", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err})
		}},
		{"plugin mutate", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handlePluginMutateResult(launchconfig.PluginMutateResultMsg{Err: err})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := tc.call(hubModel{}, boom)
			failed, ok := got.(hubModel)
			if !ok {
				t.Fatalf("handler returned %T, want hubModel", got)
			}
			if !errors.Is(failed.err, boom) {
				t.Fatalf("after a failed result, model err = %v, want %v", failed.err, boom)
			}

			// A prior error must not outlive the success that resolves it, or the
			// panel shows fresh data under a stale failure message.
			got, _ = tc.call(hubModel{err: boom}, nil)
			succeeded, ok := got.(hubModel)
			if !ok {
				t.Fatalf("handler returned %T, want hubModel", got)
			}
			if succeeded.err != nil {
				t.Fatalf("after a successful result, model err = %v, want nil", succeeded.err)
			}
		})
	}
}

// TestConfigListHandlersLeaveTheErrorForThePanelToRender pins the other half of
// the split, which is deliberate and easy to "fix" into inconsistency.
//
// A list handler forwards the message ITSELF to the panel, Err and all, so the
// panel renders the failure in place. A mutate handler cannot do that: on
// success it synthesizes a different message to refresh from, so it has to
// intercept Err on the model instead. Copying the mutate handlers' `m.err =
// msg.Err` into a list handler would surface the same failure twice, once on
// the panel and once as a model-level error.
func TestConfigListHandlersLeaveTheErrorForThePanelToRender(t *testing.T) {
	boom := errors.New("list refused by the hub")

	for _, tc := range []struct {
		name string
		call func(m hubModel, err error) (tea.Model, tea.Cmd)
	}{
		{"instance list", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleInstanceList(launchconfig.InstanceListResultMsg{Err: err})
		}},
		{"marketplace list", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{Err: err})
		}},
		{"marketplace browse", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleMarketplaceBrowseResult(launchconfig.MarketplaceBrowseResultMsg{Err: err})
		}},
		{"plugin list", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handlePluginListResult(launchconfig.PluginListResultMsg{Err: err})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := tc.call(hubModel{}, boom)
			after, ok := got.(hubModel)
			if !ok {
				t.Fatalf("handler returned %T, want hubModel", got)
			}
			if after.err != nil {
				t.Fatalf("list handler set model err = %v; the panel owns rendering this failure", after.err)
			}
		})
	}
}

// TestIsInstanceRemovePersisted pins the discriminator read: the wire error's
// Data is an ErrorData in-process and a decoded map over the socket, so both
// carry the flag; the code is never the discriminator and any other error is not
// this one.
func TestIsInstanceRemovePersisted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"plain", errors.New("nope"), false},
		{"errorData", appwire.WireError{Code: appwire.CodeInternalError, Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorInstanceRemovePersisted}}, true},
		{"map", appwire.WireError{Code: appwire.CodeInternalError, Data: map[string]any{"evenerErrorInfo": string(appwire.ErrorInstanceRemovePersisted)}}, true},
		{"otherInfo", appwire.WireError{Code: appwire.CodeInternalError, Data: appwire.ErrorData{EvenerErrorInfo: "somethingElse"}}, false},
		{"otherType", appwire.WireError{Code: appwire.CodeInternalError, Data: "string-data"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInstanceRemovePersisted(tc.err); got != tc.want {
				t.Fatalf("isInstanceRemovePersisted = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHandleInstanceMutateResultRemovalPersistedWarnsAndReconciles: a removal
// that stood but left an OAuth copy on disk carries
// appwire.ErrorInstanceRemovePersisted, so the handler reports the hub's message
// as a warning notice and re-reads the listing instead of showing a failure.
// Without a panel/client there is nothing to re-read; a plain error still takes
// the model-error path (TestConfigResultHandlersSurfaceErrorsAndClearThemOnSuccess).
func TestHandleInstanceMutateResultRemovalPersistedWarnsAndReconciles(t *testing.T) {
	persisted := appwire.InstanceRemovePersisted("removed work, but a credential the removal set aside is still on disk: /state/auth/work.json.removing-1 (delete refused)")

	got, cmd := hubModel{}.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: persisted})
	after := got.(hubModel)
	if after.err != nil {
		t.Fatalf("model err = %v, want nil: the removal stood", after.err)
	}
	if len(after.notices) != 1 {
		t.Fatalf("notices = %d, want one warning notice", len(after.notices))
	}
	if notice := after.notices[0]; notice.State != "warning" || !strings.Contains(notice.Reason, "still on disk") {
		t.Fatalf("notice = %+v, want a warning carrying the hub's message", notice)
	}
	if cmd != nil {
		t.Fatal("no client: a removal warning should not issue a refresh")
	}

	// With a panel and a client the listing is re-read, so the removed row leaves
	// the panel.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.credentialsPanel = newCredentialsPanelForTest()
	got, cmd = m.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: persisted})
	after = got.(hubModel)
	if after.err != nil || cmd == nil {
		t.Fatalf("with a client: err=%v cmd=%v, want a listing refresh", after.err, cmd)
	}
	if msg, ok := cmd().(launchconfig.InstanceListResultMsg); !ok {
		t.Fatalf("cmd msg = %T, want InstanceListResultMsg", msg)
	}

	// The other shape the discriminator covers: a rollback that could not be
	// written, which leaves no file behind. It is the same warning, and the
	// notice's own words must not send the user after a copy this shape never
	// left - the hub's message is what describes what was left unfinished.
	rollback := appwire.InstanceRemovePersisted("removing work was rolled back: write refused; the rollback could not be written, so the removal stands in the config (write refused)")
	got, _ = hubModel{}.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: rollback})
	after = got.(hubModel)
	if len(after.notices) != 1 {
		t.Fatalf("notices = %d, want one warning notice", len(after.notices))
	}
	notice := after.notices[0]
	if notice.State != "warning" || !strings.Contains(notice.Reason, "removal stands in the config") {
		t.Fatalf("notice = %+v, want a warning carrying the hub's message", notice)
	}
	for _, text := range []string{notice.Summary, notice.NextAction} {
		if strings.Contains(text, "OAuth record") || strings.Contains(text, "Delete the file") {
			t.Fatalf("notice text = %q, want no claim about a copy this shape does not leave", text)
		}
	}
}

// TestIsInstanceRenamePersisted pins the rename discriminator the same way
// TestIsInstanceRemovePersisted pins removal's: Data is an ErrorData in-process
// and a decoded map over the socket, the code is never the discriminator, and
// the sibling remove discriminator is not this one.
func TestIsInstanceRenamePersisted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"plain", errors.New("nope"), false},
		{"errorData", appwire.WireError{Code: appwire.CodeInternalError, Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorInstanceRenamePersisted}}, true},
		{"map", appwire.WireError{Code: appwire.CodeInternalError, Data: map[string]any{"evenerErrorInfo": string(appwire.ErrorInstanceRenamePersisted)}}, true},
		{"removeInfo", appwire.WireError{Code: appwire.CodeInternalError, Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorInstanceRemovePersisted}}, false},
		{"otherInfo", appwire.WireError{Code: appwire.CodeInternalError, Data: appwire.ErrorData{EvenerErrorInfo: "somethingElse"}}, false},
		{"otherType", appwire.WireError{Code: appwire.CodeInternalError, Data: "string-data"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInstanceRenamePersisted(tc.err); got != tc.want {
				t.Fatalf("isInstanceRenamePersisted = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHandleInstanceMutateResultRenamePersistedWarnsAndReconciles: a rename
// that stood in providers.toml but could not carry the instance's OAuth record
// carries appwire.ErrorInstanceRenamePersisted, so the handler reports the
// hub's message as a warning notice and re-reads the listing - which is what
// lets the panel follow the instance to its new name - instead of showing a
// save failure for a rename that landed.
func TestHandleInstanceMutateResultRenamePersistedWarnsAndReconciles(t *testing.T) {
	persisted := appwire.InstanceRenamePersisted("renamed work to work-2, but a credential the rename set aside is still on disk: /state/auth/work.json.removing-1 (delete refused)")

	got, cmd := hubModel{}.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: persisted})
	after := got.(hubModel)
	if after.err != nil {
		t.Fatalf("model err = %v, want nil: the rename stood", after.err)
	}
	if len(after.notices) != 1 {
		t.Fatalf("notices = %d, want one warning notice", len(after.notices))
	}
	if notice := after.notices[0]; notice.State != "warning" || !strings.Contains(notice.Reason, "still on disk") {
		t.Fatalf("notice = %+v, want a warning carrying the hub's message", notice)
	}
	if cmd != nil {
		t.Fatal("no client: a rename warning should not issue a refresh")
	}

	// With a panel and a client the listing is re-read, so the panel follows the
	// instance to the new name instead of keeping the old, now-nonexistent one.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.credentialsPanel = newCredentialsPanelForTest()
	got, cmd = m.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: persisted})
	after = got.(hubModel)
	if after.err != nil || cmd == nil {
		t.Fatalf("with a client: err=%v cmd=%v, want a listing refresh", after.err, cmd)
	}
	if msg, ok := cmd().(launchconfig.InstanceListResultMsg); !ok {
		t.Fatalf("cmd msg = %T, want InstanceListResultMsg", msg)
	}
}
