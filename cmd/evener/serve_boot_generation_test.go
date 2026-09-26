package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// runServeResumeCountingBoots drives one --resume and returns the session's
// boot counter as it stood when the daemon started listening: the counter a
// start increments must be durable before the daemon serves anything.
func runServeResumeCountingBoots(t *testing.T, stateDir, sessionID string) string {
	t.Helper()
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	var cancel context.CancelFunc
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := context.WithCancel(ctx)
		cancel = stop
		return next, stop
	}
	var atListen string
	listen := deps.listen
	deps.listen = func(ctx context.Context, network, addr string) (net.Listener, error) {
		data, err := os.ReadFile(schema.BootGenerationPath(stateDir, sessionID))
		if err != nil {
			t.Errorf("boot counter at listen: %v", err)
		}
		atListen = strings.TrimSpace(string(data))
		return listen(ctx, network, addr)
	}
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		cancel()
		return http.ErrServerClosed
	}
	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--resume", sessionID,
		"--dir", t.TempDir(),
		"--state-dir", stateDir,
		"--run-dir", t.TempDir(),
		"--no-project-prompts",
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("runServeWithDeps(resume): %v", err)
	}
	return atListen
}

// Two starts over one state dir serve at increasing boot generations, each
// written before the daemon listens.
func TestServeStartsIncrementTheBootGeneration(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	seedResumableSession(t, stateDir, sessionID, sessionID)

	first := runServeResumeCountingBoots(t, stateDir, sessionID)
	second := runServeResumeCountingBoots(t, stateDir, sessionID)
	if first != "1" || second != "2" {
		t.Fatalf("boot generations at listen = %q then %q, want 1 then 2", first, second)
	}
}

// A thread/clear replacement serves the same workspace ref above the
// generation its clients hold: its resync and reads carry a higher one, so a
// client replaces its history rather than ignoring the new session's.
func TestServeClearServesAboveTheReplacedGeneration(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	var readGeneration, resyncGeneration string
	obs := runClearAttempt(t, deps, state, args, func(obs *clearObservation) {
		readGeneration = clearThreadRead(t, state.srv).BootGeneration
		for _, record := range state.srv.AppNotificationsAfter(0, obs.newSessionID) {
			if record.Notification.Method == appwire.NotifyEvenerThreadResync {
				var params appwire.ThreadResyncParams
				if err := json.Unmarshal(record.Notification.Params, &params); err != nil {
					t.Fatal(err)
				}
				resyncGeneration = params.BootGeneration
			}
		}
	})
	if obs.clearErr != nil {
		t.Fatalf("clear: %v", obs.clearErr)
	}
	if readGeneration != "2" || resyncGeneration != "2" {
		t.Fatalf("after clear: read generation %q, resync generation %q; want 2 above the first start's 1", readGeneration, resyncGeneration)
	}
}
