package hub

// A sign-in flow the user never finishes must not sit in the controller's flow
// map, PKCE verifier and all, for the life of the process. The fix under test
// gives hubAuthFlow the same window deviceFlows expire on: LoginComplete
// refuses a flow older than hubAuthFlowTTL and drops it, and LoginStart sweeps
// expired entries as it records a new one. These tests pin both halves, moving
// the same `now` seam the device-flow expiry test uses.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
)

// newExpiryTestController is the fixture both tests share: a hermetic Codex
// controller whose clock the test moves through the returned pointer. The
// exchange is scripted to succeed so that, if the expiry guard ever goes
// missing, a completion that should have been refused instead runs to
// completion (rather than failing on a real network round trip) and the test
// fails on the missing refusal.
func newExpiryTestController(t *testing.T) (*hubAuthController, *time.Time) {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	ctrl := newHubAuthController()
	ctrl.stateDir = t.TempDir()
	attachTestRegistry(t, ctrl)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}
	now := time.Now()
	ctrl.now = func() time.Time { return now }
	return ctrl, &now
}

// startExpiryFlow starts a Codex login the way the pasteback test does and
// returns the flow plus the browser callback carrying its own state.
func startExpiryFlow(t *testing.T, ctrl *hubAuthController) (appwire.AuthLoginStartResponse, string) {
	t.Helper()
	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	authorizeURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	state := authorizeURL.Query().Get("state")
	if state == "" || start.FlowID != state {
		t.Fatalf("flow=%q state=%q, want matching non-empty values", start.FlowID, state)
	}
	return start, "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state)
}

func completeFlow(ctrl *hubAuthController, flowID, redirect string) error {
	_, err := ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "openai-codex",
		FlowID:      flowID,
		RedirectURL: redirect,
	})
	return err
}

// TestAuth_LoginFlowOlderThanTheWindowIsRefusedAndDropped pins the completion
// half: a flow left open past hubAuthFlowTTL is refused with the expiry
// Conflict and removed, so a retry is an unknown flow rather than an expired
// one. Without the guard the completion would have been accepted; without the
// delete the entry (and its verifier) would survive the refusal.
func TestAuth_LoginFlowOlderThanTheWindowIsRefusedAndDropped(t *testing.T) {
	ctrl, now := newExpiryTestController(t)
	start, redirect := startExpiryFlow(t, ctrl)

	*now = now.Add(hubAuthFlowTTL + time.Minute)

	err := completeFlow(ctrl, start.FlowID, redirect)
	if err == nil {
		t.Fatal("LoginComplete = nil, want the expired-flow refusal")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("LoginComplete err = %v, want it to say the sign-in flow expired", err)
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
		t.Fatalf("LoginComplete err = %T %v, want an appwire Conflict", err, err)
	}

	// The refusal must drop the flow, not just decline it.
	ctrl.mu.Lock()
	_, stillRecorded := ctrl.flows[start.FlowID]
	ctrl.mu.Unlock()
	if stillRecorded {
		t.Fatalf("flow %q is still recorded after the expiry refusal", start.FlowID)
	}

	err = completeFlow(ctrl, start.FlowID, redirect)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("second LoginComplete err = %v, want the flow-not-found error after the expired entry was deleted", err)
	}
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("second LoginComplete err = %T %v, want an appwire InvalidParams", err, err)
	}
}

// TestAuth_AbandonedLoginFlowIsSweptWhenANewOneStarts pins the start half: a
// new flow's recording reaps entries already past the window, so a sign-in the
// user abandoned is gone before its completion can ever be tried. Flow A must
// therefore be missing (InvalidParams "not found"), not present-and-expired
// (the Conflict) - the distinction is what proves the sweep ran rather than
// just the completion guard. Flow B survives, so the sweep took only the
// abandoned entry.
func TestAuth_AbandonedLoginFlowIsSweptWhenANewOneStarts(t *testing.T) {
	ctrl, now := newExpiryTestController(t)
	flowA, redirectA := startExpiryFlow(t, ctrl)

	*now = now.Add(hubAuthFlowTTL + time.Minute)

	flowB, _ := startExpiryFlow(t, ctrl)
	if flowB.FlowID == flowA.FlowID {
		t.Fatalf("flow B id = %q, want a fresh flow distinct from A", flowB.FlowID)
	}

	err := completeFlow(ctrl, flowA.FlowID, redirectA)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("LoginComplete(A) err = %v, want flow-not-found: B's start must sweep A, not leave it to the expiry refusal", err)
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("LoginComplete(A) err = %T %v, want an appwire InvalidParams", err, err)
	}

	// The map says the same thing directly: the abandoned entry is gone and
	// the new flow is kept.
	ctrl.mu.Lock()
	_, aStillRecorded := ctrl.flows[flowA.FlowID]
	_, bRecorded := ctrl.flows[flowB.FlowID]
	ctrl.mu.Unlock()
	if aStillRecorded {
		t.Fatalf("flow A %q survived B's start, want it swept", flowA.FlowID)
	}
	if !bRecorded {
		t.Fatalf("flow B %q was not recorded, want the new flow kept", flowB.FlowID)
	}
}
