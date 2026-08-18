package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_StartingASessionThatHasDaemonStartupContext is the whole path a user
// takes when they start a session, against a daemon that injects context of its
// own before the user has said anything.
//
// A SessionStart hook is the deterministic way to produce that state: its stdout
// becomes model context, which the session queues as daemon-authored steering
// during startup. Sessions in the rest of this suite run in bare temp dirs with
// no hooks, so their steering queue is empty at boot and none of them can see
// this.
//
// What must hold: nothing runs a turn to carry that context. thread/start
// issues turn/start for the opening prompt right after the spawn
// (app_threadlifecycle.go), so a turn started for the daemon's own context can
// hold the session's turn identity and refuse it with "turn is already active"
// -- leaving the UI on the spawn screen while the session runs with only the
// daemon's turn in it. The context still has to reach the model, riding the
// user's first turn the way steering is meant to.
func TestE2E_StartingASessionThatHasDaemonStartupContext(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	const contextMarker = "SERF-E2E-DAEMON-STARTUP-CONTEXT"
	pluginDir := writeSessionStartContextPlugin(t, contextMarker)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	const openingPrompt = "SERF-E2E-OPENING-PROMPT"
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off", PluginDirs: []string{pluginDir}},
	})
	if err != nil {
		t.Fatalf("thread/start against a daemon with startup context: %v", err)
	}
	ref := started.Thread.Serf.Ref
	if ref == "" {
		t.Fatalf("thread/start returned no ref: %#v", started.Thread)
	}
	t.Cleanup(func() {
		_, _ = clientRequest[appwire.EmptyResponse](context.Background(), client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref})
	})
	// The opening turn is the user's, and it is the FIRST turn: a turn started
	// for the daemon's own context would have taken this id.
	if started.Turn.ID == "" {
		t.Fatal("thread/start returned no opening turn; the user's prompt did not get one")
	}

	// The user's prompt reaches the model, and the daemon's startup context
	// rides along with it rather than having had a turn of its own.
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("the opening prompt never reached the model: %v", err)
	}
	body, marshalErr := json.Marshal(call.Body)
	if marshalErr != nil {
		t.Fatalf("marshal the model request: %v", marshalErr)
	}
	request := string(body)
	if !containsAll(request, openingPrompt, contextMarker) {
		t.Fatalf("the first model request is missing the prompt or the startup context: %s", request)
	}
	call.RespondText("acknowledged")
}

// writeSessionStartContextPlugin installs a plugin whose SessionStart hook
// prints marker on stdout. A context event's plain stdout becomes model
// context, which the session queues as daemon-authored steering while it starts.
func writeSessionStartContextPlugin(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("create plugin manifest dir: %v", err)
	}
	manifest := map[string]any{
		"name":        "e2e-startup-context",
		"version":     "0.0.1",
		"description": "emits model context at session start",
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "printf '%s' " + marker,
							"timeout": 10,
						},
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), data, 0o644); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	return dir
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
