//go:build browserguard

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The only scripted product dependency is the external provider. Drivers press
// the shipped client actions, with faults confined to the WebSocket boundary.
func TestRetryContinuationJourney(t *testing.T) {
	requireFreshDist(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal(err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	for _, surface := range []string{"browser", "native"} {
		for _, mode := range []string{"normal", "lost-reply", "refused"} {
			t.Run(surface+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				original := "RETRY_ORIGINAL_" + surface + "_" + mode
				started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
					Harness: "evener", CWD: stack.workDir, Model: stack.model,
					Input:           []appwire.InputItem{{Type: "text", Text: original}},
					LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
				})
				if err != nil {
					t.Fatal(err)
				}
				ref := started.Thread.Evener.Ref
				t.Cleanup(func() {
					shutdownCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
					defer stop()
					if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
						t.Error(err)
					}
				})
				first, err := provider.Next(ctx.Done())
				if err != nil {
					t.Fatal(err)
				}
				first.RespondHTTPError(http.StatusBadRequest, "retry_fixture_terminal")
				awaitThread(ctx, t, client, ref, "terminal failed turn", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
				failed, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(failed.Thread.Turns) != 2 || failed.Thread.Turns[1].Status != "failed" || failed.Thread.Turns[1].Error == nil {
					t.Fatal("expected the system prelude followed by a terminal provider failure")
				}
				continued := make(chan *fakellm.Call, 1)
				responderDone := make(chan struct{})
				defer func() { cancel(); <-responderDone }()
				go func() {
					defer close(responderDone)
					call, err := provider.Next(ctx.Done())
					if err != nil {
						return
					}
					continued <- call
					call.RespondToolCall("communicate", communicateArgs("RETRY_DONE_"+mode))
				}()
				dir := t.TempDir()
				fixture := map[string]any{
					"url":    hubedge.AuthURLFor("http://"+stack.addr, stack.token),
					"origin": "http://" + stack.addr, "token": stack.token,
					"artifactDir": dir, "milestonePath": filepath.Join(dir, "milestones.jsonl"),
					"scenarios": []map[string]string{{"ref": ref, "mode": mode}},
				}
				data, err := json.Marshal(fixture)
				if err != nil {
					t.Fatal(err)
				}
				driver := retryContinuationDriverCommand(ctx, "frontend/scripts/retryguard/run.mjs")
				if surface == "native" {
					fixturePath := filepath.Join(dir, "connection.json")
					if err := os.WriteFile(fixturePath, data, 0600); err != nil {
						t.Fatal(err)
					}
					driver = retryContinuationDriverCommand(ctx, "node_modules/vitest/vitest.mjs", "run", "--maxWorkers=1", "src/ConversationScreen.retryJourney.test.tsx")
					driver.Dir = "../../mobile-native"
					driver.Env = append(os.Environ(), "EVENER_RETRY_JOURNEY="+fixturePath)
				} else {
					driver.Stdin = bytes.NewReader(data)
				}
				output, err := driver.CombinedOutput()
				if err != nil {
					t.Fatalf("%s driver: %v\n%s", surface, err, output)
				}
				t.Logf("%s driver completed: %s", surface, output)
				select {
				case <-responderDone:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case call := <-continued:
					texts := call.Texts()
					if !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
						t.Fatalf("provider did not retain original input and receive continuation: %v", texts)
					}
					if slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
						t.Fatal("draft reached provider")
					}
				default:
					t.Fatal("no continuation reached provider")
				}
				awaitThread(ctx, t, client, ref, "continued turn completion", func(thread appwire.Thread) bool { return thread.Evener.ActiveTurnID == "" })
				read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
				if err != nil {
					t.Fatal(err)
				}
				counts := map[string]int{}
				for _, turn := range read.Thread.Turns {
					for _, item := range turn.Items {
						if item.Type == "userMessage" {
							counts[item.Text]++
						}
					}
				}
				if counts[original] != 1 || counts["continue"] != 1 || len(counts) != 2 {
					t.Fatalf("authoritative user input counts: %v", counts)
				}
				if len(read.Thread.Turns) != 3 || read.Thread.Turns[1].Status != "failed" || read.Thread.Turns[2].Status != "completed" {
					t.Fatal("expected the system prelude, failed original turn, and completed continuation")
				}
			})
		}
	}
	t.Run("browser/eligibility", func(t *testing.T) {
		runRetryEligibilityJourney(t, ctx, stack, client, provider)
	})
	t.Run("browser/recovery", func(t *testing.T) {
		runRetryRecoveryJourney(t, ctx, stack, client, provider)
	})
	t.Run("browser/history", func(t *testing.T) {
		runRetryHistoryJourney(t, ctx, stack, client, provider)
	})
}

// retryJourneyPluginDir lays out the one catalog entry the browser composer
// needs for a structured selection. The plugin is a launch redirect only: the
// session, catalog, composer and Retry all run in the real hub and production
// bundle.
func retryJourneyPluginDir(t *testing.T) string {
	t.Helper()
	plugin := filepath.Join(t.TempDir(), "retry-plugin")
	for _, dir := range []string{
		filepath.Join(plugin, ".claude-plugin"),
		filepath.Join(plugin, "skills", "probe"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir plugin dir: %v", err)
		}
	}
	manifest := `{"name":"pkg","version":"1.0.0","description":"retry journey fixture plugin"}`
	if err := os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	skill := "---\nname: probe\ndescription: retry journey fixture\n---\nRETRY_JOURNEY_SKILL\n"
	if err := os.WriteFile(filepath.Join(plugin, "skills", "probe", "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return plugin
}

// startRetryJourneyThread starts one thread with the fixture plugin and drives
// its first provider request to a terminal failure. The ThreadStart response's
// own ref, not a second lookup, names the session the browser opens.
func startRetryJourneyThread(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server, original string) (string, string) {
	t.Helper()
	pluginDir := retryJourneyPluginDir(t)
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		Input:           []appwire.InputItem{{Type: "text", Text: original}},
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off", PluginDirs: []string{pluginDir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := started.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Error(err)
		}
	})
	first, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	first.RespondHTTPError(http.StatusBadRequest, "retry_fixture_terminal")
	awaitThread(ctx, t, client, ref, "terminal failed turn", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == ""
	})
	failed, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(failed.Thread.Turns) != 2 || failed.Thread.Turns[1].Status != "failed" || failed.Thread.Turns[1].Error == nil {
		t.Fatal("expected the system prelude followed by a terminal provider failure")
	}
	return ref, filepath.Join(t.TempDir(), "milestones.jsonl")
}

// retryJourneyDriver runs the production browser bundle against the real hub.
// The fixture's only scripted dependency stays the external provider; faults
// are transport holds installed by the driver.
func retryJourneyDriver(ctx context.Context, stack hubStack, artifactDir, milestonePath, ref, original, mode string) error {
	fixture := map[string]any{
		"url":           hubedge.AuthURLFor("http://"+stack.addr, stack.token),
		"origin":        "http://" + stack.addr,
		"token":         stack.token,
		"artifactDir":   artifactDir,
		"milestonePath": milestonePath,
		"scenarios":     []map[string]string{{"ref": ref, "mode": mode, "original": original}},
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		return err
	}
	driver := retryContinuationDriverCommand(ctx, "frontend/scripts/retryguard/run.mjs")
	driver.Stdin = bytes.NewReader(data)
	output, err := driver.CombinedOutput()
	if err != nil {
		return fmt.Errorf("browser driver: %w\n%s", err, output)
	}
	return nil
}

// waitRetryMilestone blocks until the driver has proved the named point in the
// page. It is the only cross-process handshake: the milestone is the driver's
// own assertion, never a substitute for one.
func waitRetryMilestone(ctx context.Context, t *testing.T, path, name string, driverDone <-chan error) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var record map[string]any
				if json.Unmarshal([]byte(line), &record) == nil && record["milestone"] == name {
					return
				}
			}
		}
		select {
		case err := <-driverDone:
			if err == nil {
				t.Fatalf("driver finished without reaching milestone %q", name)
			}
			t.Fatalf("driver failed before milestone %q: %v", name, err)
		case <-ctx.Done():
			t.Fatalf("waiting for driver milestone %q: %v", name, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("driver never reached milestone %q", name)
}

// retryJourneyCalls fans the real provider requests into one channel so each
// scenario can answer exactly the call it means while the driver runs.
func retryJourneyCalls(ctx context.Context, provider *fakellm.Server) <-chan *fakellm.Call {
	calls := make(chan *fakellm.Call, 8)
	go func() {
		defer close(calls)
		for {
			call, err := provider.Next(ctx.Done())
			if err != nil {
				return
			}
			select {
			case calls <- call:
			case <-ctx.Done():
				return
			}
		}
	}()
	return calls
}

func nextRetryCall(ctx context.Context, t *testing.T, calls <-chan *fakellm.Call, what string, driverDone ...<-chan error) *fakellm.Call {
	t.Helper()
	select {
	case err := <-firstDone(driverDone):
		t.Fatalf("driver finished before %s: %v", what, err)
	case call, ok := <-calls:
		if !ok {
			t.Fatalf("provider closed while waiting for %s", what)
		}
		return call
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", what, ctx.Err())
		return nil
	}
	return nil
}

// firstDone turns the optional driver channel into a selectable one. A nil
// channel blocks forever, which is what scenarios without a concurrent driver
// need.
func firstDone(channels []<-chan error) <-chan error {
	if len(channels) == 0 {
		return nil
	}
	return channels[0]
}

func runRetryEligibilityJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_browser_eligibility"
	ref, milestonePath := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	artifactDir := t.TempDir()
	calls := retryJourneyCalls(ctx, provider)
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryJourneyDriver(ctx, stack, artifactDir, milestonePath, ref, original, "eligibility")
	}()

	continuation := nextRetryCall(ctx, t, calls, "the first continuation", driverDone)
	if texts := continuation.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
		t.Fatalf("first continuation lost the original input: %v", texts)
	}
	if texts := continuation.Texts(); slices.Contains(texts, "user: RETRY_DRAFT_ELIGIBILITY") {
		t.Fatal("the staged draft reached the provider")
	}
	continuation.RespondToolCall("communicate", communicateArgs("RETRY_DONE_ELIGIBILITY"))

	waitRetryMilestone(ctx, t, milestonePath, "ready-for-newer-turn", driverDone)
	newer := nextRetryCall(ctx, t, calls, "the newer running turn", driverDone)
	waitRetryMilestone(ctx, t, milestonePath, "empty-running", driverDone)
	newer.RespondToolCall("communicate", communicateArgs("RETRY_NEWER_SUCCESS"))

	newerFailure := nextRetryCall(ctx, t, calls, "the newer failing turn", driverDone)
	newerFailure.RespondHTTPError(http.StatusBadRequest, "retry_fixture_newer")

	newestContinuation := nextRetryCall(ctx, t, calls, "the newest retry", driverDone)
	if texts := newestContinuation.Texts(); !slices.Contains(texts, "user: continue") {
		t.Fatalf("newest retry did not send the continuation: %v", texts)
	}
	if texts := newestContinuation.Texts(); slices.Contains(texts, "user: RETRY_DRAFT_ELIGIBILITY") {
		t.Fatal("the preserved draft reached the provider")
	}
	newestContinuation.RespondToolCall("communicate", communicateArgs("RETRY_NEWEST_DONE"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" {
				counts[item.Text]++
			}
		}
	}
	if counts[original] != 1 || counts["continue"] != 2 || counts["RETRY_NEWER_RUNNING"] != 1 || counts["RETRY_NEWER_FAIL"] != 1 {
		t.Fatalf("authoritative user input counts: %v", counts)
	}
	if counts["RETRY_DRAFT_ELIGIBILITY"] != 0 {
		t.Fatalf("draft became authoritative input: %v", counts)
	}
	if len(read.Thread.Turns) != 6 {
		t.Fatalf("expected prelude, failed original, two continuations and two newer turns, got %d turns", len(read.Thread.Turns))
	}
	if read.Thread.Turns[1].Status != "failed" || read.Thread.Turns[2].Status != "completed" ||
		read.Thread.Turns[4].Status != "failed" || read.Thread.Turns[5].Status != "completed" {
		t.Fatalf("turn outcomes: %v", read.Thread.Turns)
	}
}

func runRetryRecoveryJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_browser_recovery"
	ref, milestonePath := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	artifactDir := t.TempDir()
	calls := retryJourneyCalls(ctx, provider)
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryJourneyDriver(ctx, stack, artifactDir, milestonePath, ref, original, "recovery")
	}()

	continuation := nextRetryCall(ctx, t, calls, "the lost-reply continuation", driverDone)
	if texts := continuation.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
		t.Fatalf("continuation lost the original input: %v", texts)
	}
	// Hold the accepted continuation until the browser has reloaded and proved
	// the unknown-pending window, so recovery is genuinely unresolved then.
	waitRetryMilestone(ctx, t, milestonePath, "recovery-window-asserted", driverDone)
	continuation.RespondToolCall("communicate", communicateArgs("RETRY_RECOVERED"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" {
				counts[item.Text]++
			}
		}
	}
	if counts[original] != 1 || counts["continue"] != 1 {
		t.Fatalf("authoritative user input counts after lost-reply recovery: %v", counts)
	}
	if counts["RETRY_DRAFT_RECOVERY"] != 0 {
		t.Fatalf("the recovered draft became authoritative input: %v", counts)
	}
	if len(read.Thread.Turns) != 3 || read.Thread.Turns[1].Status != "failed" || read.Thread.Turns[2].Status != "completed" {
		t.Fatalf("turn outcomes after recovery: %v", read.Thread.Turns)
	}
}

func runRetryHistoryJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_browser_history"
	ref, milestonePath := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	// More than one 40-item page after the failure: the failure must be reached
	// through the real older-history owner, not a short transcript that fits.
	const newerTurns = 21
	calls := retryJourneyCalls(ctx, provider)
	for index := 0; index < newerTurns; index++ {
		if _, err := clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{
			Ref:                ref,
			ClientMutationID:   newMutationID(t),
			ExpectedInstanceID: localInstanceIDForTestRef(ref),
			Input:              []appwire.InputItem{{Type: "text", Text: fmt.Sprintf("RETRY_HISTORY_%02d", index)}},
		}); err != nil {
			t.Fatalf("history turn %d: %v", index, err)
		}
		answer := nextRetryCall(ctx, t, calls, "a history provider call")
		if index == newerTurns-1 {
			answer.RespondToolCall("communicate", communicateArgs("RETRY_HISTORY_LAST"))
		} else {
			answer.RespondToolCall("communicate", communicateArgs(fmt.Sprintf("RETRY_HISTORY_REPLY_%02d", index)))
		}
		awaitThread(ctx, t, client, ref, "a completed history turn", func(thread appwire.Thread) bool {
			return thread.Evener.ActiveTurnID == ""
		})
	}
	if err := retryJourneyDriver(ctx, stack, t.TempDir(), milestonePath, ref, original, "history"); err != nil {
		t.Fatal(err)
	}
}

func retryContinuationDriverCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "node", args...)
	// Let the driver's lifecycle reap its detached browser and remove its
	// profile before the existing bounded forced-stop policy takes over.
	configureBackgroundDriverCommand(cmd)
	return cmd
}
