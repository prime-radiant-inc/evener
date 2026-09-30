package hub

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

// TestRunMainProbesACredentialASessionWasRefused pins the startup wiring of
// the session credential watch (#3539): runMain hooks the watch onto the
// hub's roster and hands it the background runner, so a live daemon
// whose turn the provider refused makes the hub check that instance's
// credential without anyone pressing Test.
func TestRunMainProbesACredentialASessionWasRefused(t *testing.T) {
	if got := runMainWithARefusedSession(t, 0); got != 1 {
		t.Fatalf("credential checks = %d, want one for the instance the session's turn was refused on", got)
	}
}

// The roster's status probe timeout is a runMain seam: production keeps
// 500ms, and a test that reads a real daemon through it sets a generous one,
// so a loaded runner cannot time the probe out. A daemon slower than
// production's 500ms default is read under the test's timeout.
func TestRunMainRosterProbeTimeoutIsASeam(t *testing.T) {
	if got := runMainWithARefusedSession(t, 800*time.Millisecond); got != 1 {
		t.Fatalf("credential checks = %d: a daemon answering in 800ms was not read under the test's probe timeout", got)
	}
}

// runMainWithARefusedSession runs the hub against one live daemon whose root
// thread rests on a turn the provider refused (a 401 on "base"), answering
// the hub's status probe after listDelay, and returns how many credential
// checks the hub ran before runMain returned. The daemon is a real AppWire
// server the hub's own status prober reads; only the credential check's model
// listing is scripted. runMain waits for its background runner before it
// returns, and the check runs there.
func runMainWithARefusedSession(t *testing.T, listDelay time.Duration) int {
	t.Helper()
	root, cfg, deps := newTraceMainTestDeps(t)
	deps.rosterProbeTimeout = 30 * time.Second
	providers := filepath.Join(root, "config", "evener", "providers.toml")
	if err := os.MkdirAll(filepath.Dir(providers), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMinimalProvidersToml(t, providers)

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	entry := residentEntryForTest(t, os.Getpid())
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		time.Sleep(listDelay)
		return appwire.ThreadListResponse{Data: []appwire.Thread{{
			ID: entry.SessionID, SessionID: entry.SessionID, Source: "local",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusSystemError},
			Evener: appwire.EvenerThread{Failure: &appwire.ThreadFailure{
				Title: "Turn failed",
				Cause: providerCause("base", http.StatusUnauthorized),
			}},
		}}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry.Endpoint = "ws" + daemonHTTP.URL[len("http"):]
	writeRendezvous(t, cfg.RunDir, entry)

	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("base", http.StatusUnauthorized, "refused", nil, nil)}
	deps.afterWeb = func(web *WebServer) {
		web.auth.credentialTestLoader = func(string, bool) (credentialProbeClient, error) { return client, nil }
	}
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	return client.callCount()
}
