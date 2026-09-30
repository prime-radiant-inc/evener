package hub

// A live session whose turn failed because the provider refused its
// credential (a 401 or 403 on the failed turn's cause) makes the hub probe
// that instance right away (#3539), so the Hub shows Error without anyone
// pressing Test. The probe is the hub's own credential check; the session's
// failure only triggers it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/valueexpr"
	"primeradiant.com/evener/llm"
)

// failedOn is a live session resting on a failed turn with cause.
func failedOn(sessionID string, cause *appwire.DiagnosticCause) hubcore.LiveEntry {
	return hubcore.LiveEntry{SessionID: sessionID, Failure: &appwire.ThreadFailure{Title: "Turn failed", Cause: cause}}
}

func providerCause(instance string, status int) *appwire.DiagnosticCause {
	return &appwire.DiagnosticCause{Kind: "provider", Provider: instance, Status: status}
}

// newSessionWatch watches for c with a synchronous runner, so each observe
// has finished its probes when it returns.
func newSessionWatch(c *hubAuthController) *sessionCredentialWatch {
	w := &sessionCredentialWatch{auth: c}
	w.start(context.Background(), func(fn func()) { fn() })
	return w
}

func TestSessionCredentialWatch_ARejectedTurnProbesTheInstance(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", status, "refused", nil, nil)}
			c, _ := newRejectionController(t, client)
			newSessionWatch(c).observe([]hubcore.LiveEntry{failedOn("s1", providerCause("gateway", status))})
			if client.callCount() != 1 {
				t.Fatalf("probes = %d, want one", client.callCount())
			}
			if gatewayError(t, c) == "" {
				t.Fatal("the probe's rejection was not recorded")
			}
		})
	}
}

// A session that stays failed is one failure: it is probed when it fails,
// not again on every roster change while it rests there.
func TestSessionCredentialWatch_ProbesOncePerFailure(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "refused", nil, nil)}
	c, _ := newRejectionController(t, client)
	w := newSessionWatch(c)
	failed := []hubcore.LiveEntry{failedOn("s1", providerCause("gateway", 401))}
	w.observe(failed)
	w.observe(failed)
	if client.callCount() != 1 {
		t.Fatalf("probes = %d after two observations of one failure, want one", client.callCount())
	}
	// The session runs again and fails again: a new failure, a new probe.
	w.observe([]hubcore.LiveEntry{{SessionID: "s1"}})
	w.observe(failed)
	if client.callCount() != 2 {
		t.Fatalf("probes = %d after the session failed a second time, want two", client.callCount())
	}
}

// Two sessions failing on one instance at once need one probe of it.
func TestSessionCredentialWatch_OneProbePerInstance(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "refused", nil, nil)}
	c, _ := newRejectionController(t, client)
	newSessionWatch(c).observe([]hubcore.LiveEntry{
		failedOn("s1", providerCause("gateway", 401)),
		failedOn("s2", providerCause("gateway", 403)),
	})
	if client.callCount() != 1 {
		t.Fatalf("probes = %d, want one for the instance", client.callCount())
	}
}

// Only a refused credential triggers a probe: a rate limit, a server error,
// a sign-in that expired (already needsLogin), a crash or a failure with no
// provider says nothing about the key.
func TestSessionCredentialWatch_OtherFailuresProbeNothing(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "refused", nil, nil)}
	c, _ := newRejectionController(t, client)
	crashed := failedOn("s6", providerCause("gateway", 401))
	crashed.Crashed = true
	newSessionWatch(c).observe([]hubcore.LiveEntry{
		failedOn("s1", providerCause("gateway", 429)),
		failedOn("s2", providerCause("gateway", 500)),
		failedOn("s3", &appwire.DiagnosticCause{Kind: "signInRequired", Provider: "gateway"}),
		failedOn("s4", providerCause("", 401)),
		failedOn("s5", nil),
		crashed,
		{SessionID: "s7"},
	})
	if client.callCount() != 0 {
		t.Fatalf("probes = %d, want none", client.callCount())
	}
}

// The hub mints a credential command only when the user asks it to check the
// credential (spec §10.1, ruled 2026-09-23). A session's failure is not the
// user asking, so a command-credentialed instance is not probed.
func TestSessionCredentialWatch_NeverMintsACredentialCommand(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key = '''$(gw-mint)'''\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestAuthController(t, dir, t.TempDir(), tomlPath)

	newSessionWatch(c).observe([]hubcore.LiveEntry{failedOn("s1", providerCause("gw", 401))})

	if runs != 0 || requests != 0 {
		t.Fatalf("a session failure ran the credential command %d time(s) and sent %d request(s), want none", runs, requests)
	}
}

// A watch the hub has not finished wiring (no runner yet) probes nothing and
// forgets nothing: a failure it sees then is still new once it can probe.
func TestSessionCredentialWatch_WaitsForItsRunner(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "refused", nil, nil)}
	c, _ := newRejectionController(t, client)
	w := &sessionCredentialWatch{auth: c}
	failed := []hubcore.LiveEntry{failedOn("s1", providerCause("gateway", 401))}
	w.observe(failed)
	if client.callCount() != 0 {
		t.Fatalf("probes = %d before the runner was set, want none", client.callCount())
	}
	w.start(context.Background(), func(fn func()) { fn() })
	w.observe(failed)
	if client.callCount() != 1 {
		t.Fatalf("probes = %d once the runner was set, want one", client.callCount())
	}
}
