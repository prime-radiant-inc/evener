package hub

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

// TestRunMainProbesACredentialASessionWasRefused pins the startup wiring of
// the session credential watch (#3539): runMain hooks the watch onto the
// hub's roster and hands it the background runner, so a live daemon
// whose turn the provider refused makes the hub check that instance's
// credential without anyone pressing Test. The daemon is a real AppWire
// server the hub's own status prober reads; only the credential check's model
// listing is scripted.
func TestRunMainProbesACredentialASessionWasRefused(t *testing.T) {
	root, cfg, deps := newTraceMainTestDeps(t)
	providers := filepath.Join(root, "config", "evener", "providers.toml")
	if err := os.MkdirAll(filepath.Dir(providers), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMinimalProvidersToml(t, providers)

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	entry := residentEntryForTest(t, os.Getpid())
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
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
	// runMain waits for its background runner before it returns, and the
	// probe runs there.
	if got := client.callCount(); got != 1 {
		t.Fatalf("credential checks = %d, want one for the instance the session's turn was refused on", got)
	}
}
