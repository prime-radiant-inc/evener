package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/internal/appserver"
)

// Tests for the edit endpoint assertion: the edit form was opened on one row
// of the instance list, so the save carries that row's endpoint fingerprint to
// the hub. A refusal that names the endpoint as changed is the endpoint-conflict
// class - the hub's clear account on the error line plus a re-read of the list,
// so a retry asserts the destination now on screen instead of repeating the
// stale assertion - never a plain failure.

// submitEditFromPanel drives the real panel: open the edit form on the selected
// row, walk its fields, and return the submit message the form produced.
func submitEditFromPanel(t *testing.T, p *launchconfig.CredentialsPanel) launchconfig.InstanceEditSubmitMsg {
	t.Helper()
	opened, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	edited := opened.(launchconfig.CredentialsPanel)
	advanced, _ := edited.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, submit := advanced.(launchconfig.CredentialsPanel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if submit == nil {
		t.Fatal("the edit form's last field should submit")
	}
	msg, ok := submit().(launchconfig.InstanceEditSubmitMsg)
	if !ok {
		t.Fatalf("edit submit = %T, want InstanceEditSubmitMsg", submit())
	}
	return msg
}

// TestInstanceEditSubmitsTheShownEndpointFingerprint: the form was opened on a
// listed row, and that row's fingerprint travels with the edit so the hub can
// refuse a name another client has re-pointed since. A row with no fingerprint
// keeps the empty contract - nothing to assert.
func TestInstanceEditSubmitsTheShownEndpointFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name        string
		entry       appwire.InstanceEntry
		wantFingerp string
	}{
		{"fingerprinted row", appwire.InstanceEntry{Name: "old", ProviderID: "openai", EndpointFingerprint: "fp-edit"}, "fp-edit"},
		{"unfingerprinted row", appwire.InstanceEntry{Name: "old", ProviderID: "openai"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var params appwire.InstanceEditParams
			client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
				appserver.HandleTyped(app.Router(), appwire.MethodEvenerInstanceEdit, func(_ context.Context, got appwire.InstanceEditParams) (appwire.InstanceListResponse, error) {
					params = got
					return appwire.InstanceListResponse{}, nil
				})
			})
			defer cleanup()
			m := newHubModel(client, "http://hub.test")
			m.credentialsPanel = newCredentialsPanelForTest()
			loaded, _ := m.credentialsPanel.Update(launchconfig.InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{tc.entry}}})
			panel := loaded.(launchconfig.CredentialsPanel)
			m.credentialsPanel = &panel

			msg := submitEditFromPanel(t, m.credentialsPanel)
			if msg.Params.ExpectedEndpointFingerprint != tc.wantFingerp {
				t.Fatalf("edit params fingerprint = %q, want %q (the row the form was opened on)", msg.Params.ExpectedEndpointFingerprint, tc.wantFingerp)
			}
			_, cmd := m.handleInstanceEditSubmit(msg)
			if cmd == nil {
				t.Fatal("cmd should not be nil with client")
			}
			cmd()
			if params.ExpectedEndpointFingerprint != tc.wantFingerp {
				t.Fatalf("wire params fingerprint = %q, want %q", params.ExpectedEndpointFingerprint, tc.wantFingerp)
			}
		})
	}
}

// TestInstanceEditEndpointConflictRefusalRefreshesTheList: the hub refuses the
// asserted destination because the name moved since the form was read. That is
// its own class: the hub's clear account stays on the error line, and the list
// is re-read so the next save asserts the destination now on screen. A plain
// failure would leave the stale row in the panel and the same refusal waiting
// on the retry.
// TestInstanceEndpointConflictRejectsAGenuineConflict: the assertion refusal has
// its own discriminant because CodeConflict is shared with genuine conflicts. A
// rename onto an occupied name must not be reconciled as a moved endpoint - it
// keeps the hub's own refusal on the error line and leaves the rows alone.
func TestInstanceEndpointConflictRejectsAGenuineConflict(t *testing.T) {
	if instanceEndpointConflict(appwire.Conflict(`instance "personal" already exists`)) {
		t.Fatal("a name-collision conflict must not classify as an endpoint conflict")
	}
	if !instanceEndpointConflict(appwire.EndpointConflict("old no longer resolves to the endpoint this form was opened on")) {
		t.Fatal("an asserted-endpoint refusal must classify as an endpoint conflict")
	}
}

func TestInstanceEditEndpointConflictRefusalRefreshesTheList(t *testing.T) {
	refusal := appwire.EndpointConflict("old no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again")
	listCalls := 0
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerInstanceEdit, func(_ context.Context, _ appwire.InstanceEditParams) (appwire.InstanceListResponse, error) {
			return appwire.InstanceListResponse{}, refusal
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerInstanceList, func(context.Context, appwire.EmptyParams) (appwire.InstanceListResponse, error) {
			listCalls++
			return appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
				{Name: "old", ProviderID: "openai", EndpointFingerprint: "fp-new"},
			}}, nil
		})
	})
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.credentialsPanel = newCredentialsPanelForTest()
	loaded, _ := m.credentialsPanel.Update(launchconfig.InstanceListResultMsg{List: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{
		{Name: "old", ProviderID: "openai", EndpointFingerprint: "fp-old"},
	}}})
	panel := loaded.(launchconfig.CredentialsPanel)
	m.credentialsPanel = &panel

	if !instanceEndpointConflict(refusal) {
		t.Fatal("fixture: the hub's asserted-endpoint refusal must classify as an endpoint conflict")
	}
	msg := submitEditFromPanel(t, m.credentialsPanel)
	if msg.Params.ExpectedEndpointFingerprint != "fp-old" {
		t.Fatalf("edit params fingerprint = %q, want the opened row's fp-old", msg.Params.ExpectedEndpointFingerprint)
	}
	_, cmd := m.handleInstanceEditSubmit(msg)
	if cmd == nil {
		t.Fatal("cmd should not be nil with client")
	}
	result, ok := cmd().(launchconfig.InstanceMutateResultMsg)
	if !ok || result.Err == nil {
		t.Fatalf("edit result = %#v, want the conflict refusal carried", result)
	}

	updated, refresh := m.handleInstanceMutateResult(result)
	after := updated.(hubModel)
	if after.err == nil || !strings.Contains(after.err.Error(), "no longer resolves to the endpoint") {
		t.Fatalf("endpoint-conflict refusal err = %v, want the hub's clear account on the error line", after.err)
	}
	if refresh == nil {
		t.Fatal("an endpoint-conflict refusal must re-read the list, not fail plainly")
	}
	refreshed, ok := refresh().(launchconfig.InstanceListResultMsg)
	if !ok || refreshed.Err != nil || listCalls != 1 {
		t.Fatalf("refresh result = %#v, listCalls = %d", refreshed, listCalls)
	}
	withList, _ := after.handleInstanceList(refreshed)
	retry := submitEditFromPanel(t, withList.(hubModel).credentialsPanel)
	if retry.Params.ExpectedEndpointFingerprint != "fp-new" {
		t.Fatalf("retry fingerprint = %q, want the destination now on screen (fp-new)", retry.Params.ExpectedEndpointFingerprint)
	}
}
