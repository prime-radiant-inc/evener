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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
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
					"storageDir":  dir,
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
					driver.Env = append(os.Environ(), "EVENER_RETRY_JOURNEY="+fixturePath, "EVENER_RETRY_SQLITE_DIR="+dir)
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
	// One-owner Retry scenarios: the browser journey owns them, and each one
	// scripts only the external provider while faults stay at the WebSocket
	// boundary.
	t.Run("browser/repeated-press", func(t *testing.T) {
		runRetryRepeatedPressJourney(t, ctx, stack, client, provider)
	})
	t.Run("browser/latest-only", func(t *testing.T) {
		runRetryLatestOnlyJourney(t, ctx, stack, client, provider)
	})
	t.Run("browser/reload-unresolved", func(t *testing.T) {
		runRetryReloadUnresolvedJourney(t, ctx, stack, client, provider)
	})
	t.Run("browser/delayed-pending", func(t *testing.T) {
		runRetryDelayedPendingJourney(t, ctx, stack, client, provider)
	})
	// The native one-owner journeys: two mounted views of one binding, a newer
	// running/successful/failed turn, and a remount while a durable row is
	// unresolved. Each scripts only the external provider; faults stay at the
	// WebSocket boundary and the milestones are the native driver's own
	// assertions.
	t.Run("native/two-views", func(t *testing.T) {
		runRetryNativeTwoViewsJourney(t, ctx, stack, client, provider)
	})
	t.Run("native/latest-only", func(t *testing.T) {
		runRetryNativeLatestOnlyJourney(t, ctx, stack, client, provider)
	})
	t.Run("native/remount-unresolved", func(t *testing.T) {
		runRetryNativeRemountJourney(t, ctx, stack, client, provider)
	})
}

func retryContinuationDriverCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "node", args...)
	// Let the driver's lifecycle reap its detached browser and remove its
	// profile before the existing bounded forced-stop policy takes over.
	configureBackgroundDriverCommand(cmd)
	return cmd
}

// retryNativeJourneyDriver runs the actual native screen test against the real
// hub. The fixture only scripts the external provider; the WebSocket fault is
// the driver's own transport hold.
func retryNativeJourneyDriver(ctx context.Context, stack hubStack, dir, milestonePath, ref, mode string) error {
	fixture := map[string]any{
		"url":           hubedge.AuthURLFor("http://"+stack.addr, stack.token),
		"origin":        "http://" + stack.addr,
		"token":         stack.token,
		"storageDir":    dir,
		"artifactDir":   dir,
		"milestonePath": milestonePath,
		"scenarios":     []map[string]string{{"ref": ref, "mode": mode}},
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		return err
	}
	fixturePath := filepath.Join(dir, "connection.json")
	if err := os.WriteFile(fixturePath, data, 0600); err != nil {
		return err
	}
	driver := retryContinuationDriverCommand(ctx, "node_modules/vitest/vitest.mjs", "run", "--maxWorkers=1", "src/ConversationScreen.retryJourney.test.tsx")
	driver.Dir = "../../mobile-native"
	driver.Env = append(os.Environ(), "EVENER_RETRY_JOURNEY="+fixturePath, "EVENER_RETRY_SQLITE_DIR="+dir)
	output, err := driver.CombinedOutput()
	if err != nil {
		return fmt.Errorf("native driver: %w\n%s", err, output)
	}
	return nil
}

// startRetryJourneyThread starts one thread whose first provider request is a
// terminal failure, and returns the ref the browser opens. The call is the
// fixture's scripted provider boundary; the shipped clients and hub are real.
func startRetryJourneyThread(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server, original string) string {
	t.Helper()
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
	return ref
}

// retryJourneyDriver runs the production browser bundle against the real hub
// with one scenario. Faults are the driver's transport holds; the fixture only
// scripts the external provider. The milestone path is the driver's own
// assertion channel back to this process.
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
		if milestones, err := backgroundReadMilestones(path); err == nil && backgroundMilestonePresent(milestones, name) {
			return
		}
		select {
		case err := <-driverDone:
			if err == nil {
				t.Fatalf("driver finished without reaching milestone %q", name)
			}
			t.Fatalf("driver failed before milestone %q: %v", name, err)
		case <-ctx.Done():
			t.Fatalf("waiting for driver milestone %q: %v", name, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	t.Fatalf("driver never reached milestone %q", name)
}

// retryJourneyCalls fans the real provider requests into one channel so each
// scenario answers exactly the call it means while the driver runs. The
// returned stop cancels this consumer and waits for it to leave provider.Next:
// a consumer still parked there when the next scenario starts would consume
// that scenario's first request, exactly the legacy responder teardown avoids.
func retryJourneyCalls(ctx context.Context, provider *fakellm.Server) (<-chan *fakellm.Call, func()) {
	consumerCtx, cancel := context.WithCancel(ctx)
	calls := make(chan *fakellm.Call, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(calls)
		for {
			call, err := provider.Next(consumerCtx.Done())
			if err != nil {
				return
			}
			select {
			case calls <- call:
			case <-consumerCtx.Done():
				return
			}
		}
	}()
	return calls, func() {
		cancel()
		<-done
	}
}

func nextRetryCall(ctx context.Context, t *testing.T, calls <-chan *fakellm.Call, what string, driverDone <-chan error) *fakellm.Call {
	t.Helper()
	select {
	case err := <-driverDone:
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

// retryUserInputCounts is the authoritative transcript count every scenario
// shares: one map over the thread's own recorded user input.
func retryUserInputCounts(thread appwire.Thread) map[string]int {
	counts := map[string]int{}
	for _, turn := range thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" {
				counts[item.Text]++
			}
		}
	}
	return counts
}

func readRetryJourneyThread(ctx context.Context, t *testing.T, client *appwire.Client, ref string) appwire.Thread {
	t.Helper()
	read, err := clientRequest[appwire.ThreadReadResponse](ctx, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	return read.Thread
}

func runRetryRepeatedPressJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_browser_repeated_press"
	ref := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	calls, stopCalls := retryJourneyCalls(ctx, provider)
	defer stopCalls()
	dir := t.TempDir()
	milestonePath := filepath.Join(dir, "milestones.jsonl")
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryJourneyDriver(ctx, stack, dir, milestonePath, ref, original, "repeated-press")
	}()

	continuation := nextRetryCall(ctx, t, calls, "the first continuation", driverDone)
	texts := continuation.Texts()
	if !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
		t.Fatalf("continuation lost the original input: %v", texts)
	}
	if slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
		t.Fatal("the staged draft reached the provider")
	}
	continuation.RespondToolCall("communicate", communicateArgs("RETRY_DONE_REPEATED_PRESS"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	thread := readRetryJourneyThread(ctx, t, client, ref)
	counts := retryUserInputCounts(thread)
	if counts[original] != 1 || counts["continue"] != 1 || len(counts) != 2 {
		t.Fatalf("authoritative user input counts: %v", counts)
	}
	if len(thread.Turns) != 3 || thread.Turns[1].Status != "failed" || thread.Turns[2].Status != "completed" {
		t.Fatal("expected the prelude, the failed original turn, and one completed continuation")
	}
}

func runRetryLatestOnlyJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_browser_latest_only"
	ref := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	calls, stopCalls := retryJourneyCalls(ctx, provider)
	defer stopCalls()
	dir := t.TempDir()
	milestonePath := filepath.Join(dir, "milestones.jsonl")
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryJourneyDriver(ctx, stack, dir, milestonePath, ref, original, "latest-only")
	}()

	running := nextRetryCall(ctx, t, calls, "the newer running turn", driverDone)
	if texts := running.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: RETRY_RUNNING_NEW") {
		t.Fatalf("newer running turn lost its inputs: %v", texts)
	}
	// The provider holds the running turn until the page has proved the newer
	// running identity exists with zero display items and no older action.
	waitRetryMilestone(ctx, t, milestonePath, "running-empty", driverDone)
	running.RespondToolCall("communicate", communicateArgs("RETRY_RUNNING_DONE"))

	failing := nextRetryCall(ctx, t, calls, "the newer failing turn", driverDone)
	if texts := failing.Texts(); !slices.Contains(texts, "user: RETRY_NEWER_FAIL") {
		t.Fatalf("newer failing turn lost its input: %v", texts)
	}
	failing.RespondHTTPError(http.StatusBadRequest, "retry_fixture_newer")

	newest := nextRetryCall(ctx, t, calls, "the newest retry", driverDone)
	if texts := newest.Texts(); !slices.Contains(texts, "user: continue") {
		t.Fatalf("the newer failure's Retry did not send the continuation: %v", texts)
	}
	newest.RespondToolCall("communicate", communicateArgs("RETRY_NEWEST_DONE"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	thread := readRetryJourneyThread(ctx, t, client, ref)
	counts := retryUserInputCounts(thread)
	if counts[original] != 1 || counts["RETRY_RUNNING_NEW"] != 1 || counts["RETRY_NEWER_FAIL"] != 1 || counts["continue"] != 1 {
		t.Fatalf("authoritative user input counts: %v", counts)
	}
	if len(thread.Turns) != 5 ||
		thread.Turns[1].Status != "failed" || thread.Turns[2].Status != "completed" ||
		thread.Turns[3].Status != "failed" || thread.Turns[4].Status != "completed" {
		t.Fatalf("turn outcomes: %+v", thread.Turns)
	}
}

func runRetryReloadUnresolvedJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_browser_reload_unresolved"
	ref := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	calls, stopCalls := retryJourneyCalls(ctx, provider)
	defer stopCalls()
	dir := t.TempDir()
	milestonePath := filepath.Join(dir, "milestones.jsonl")
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryJourneyDriver(ctx, stack, dir, milestonePath, ref, original, "reload-unresolved")
	}()

	continuation := nextRetryCall(ctx, t, calls, "the lost-reply continuation", driverDone)
	if texts := continuation.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
		t.Fatalf("continuation lost the original input: %v", texts)
	}
	if texts := continuation.Texts(); slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
		t.Fatal("the staged draft reached the provider")
	}
	// Hold the accepted continuation until the browser has reloaded and proved
	// the unresolved durable row keeps Retry inactive, so recovery is genuinely
	// unresolved across that window.
	waitRetryMilestone(ctx, t, milestonePath, "reload-unresolved", driverDone)
	continuation.RespondToolCall("communicate", communicateArgs("RETRY_RECOVERED"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	thread := readRetryJourneyThread(ctx, t, client, ref)
	counts := retryUserInputCounts(thread)
	if counts[original] != 1 || counts["continue"] != 1 || len(counts) != 2 {
		t.Fatalf("authoritative user input counts after recovery: %v", counts)
	}
	if len(thread.Turns) != 3 || thread.Turns[1].Status != "failed" || thread.Turns[2].Status != "completed" {
		t.Fatal("expected the prelude, the failed original turn, and one completed continuation")
	}
}

func runRetryDelayedPendingJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_browser_delayed_pending"
	ref := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	calls, stopCalls := retryJourneyCalls(ctx, provider)
	defer stopCalls()
	dir := t.TempDir()
	milestonePath := filepath.Join(dir, "milestones.jsonl")
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryJourneyDriver(ctx, stack, dir, milestonePath, ref, original, "delayed-pending")
	}()

	continuation := nextRetryCall(ctx, t, calls, "the continuation after the delayed pending read", driverDone)
	if texts := continuation.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
		t.Fatalf("continuation lost the original input: %v", texts)
	}
	if texts := continuation.Texts(); slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
		t.Fatal("the staged draft reached the provider")
	}
	continuation.RespondToolCall("communicate", communicateArgs("RETRY_DONE_DELAYED_PENDING"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	thread := readRetryJourneyThread(ctx, t, client, ref)
	counts := retryUserInputCounts(thread)
	if counts[original] != 1 || counts["continue"] != 1 || len(counts) != 2 {
		t.Fatalf("authoritative user input counts: %v", counts)
	}
	if len(thread.Turns) != 3 || thread.Turns[1].Status != "failed" || thread.Turns[2].Status != "completed" {
		t.Fatal("expected the prelude, the failed original turn, and one completed continuation")
	}
}

// runRetryNativeTwoViewsJourney mounts two views of one session, presses the
// shared Retry once, and holds the provider response while the native driver
// independently holds acceptance at the WebSocket boundary and proves the
// durable row's identity matches the wire, with no second identity on repeat.
func runRetryNativeTwoViewsJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_native_two_views"
	ref := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	calls, stopCalls := retryJourneyCalls(ctx, provider)
	defer stopCalls()
	dir := t.TempDir()
	milestonePath := filepath.Join(dir, "milestones.jsonl")
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryNativeJourneyDriver(ctx, stack, dir, milestonePath, ref, "two-views")
	}()

	continuation := nextRetryCall(ctx, t, calls, "the one continuation", driverDone)
	if texts := continuation.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
		t.Fatalf("continuation lost the original input: %v", texts)
	}
	if texts := continuation.Texts(); slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
		t.Fatal("the staged draft reached the provider")
	}
	// Hold acceptance until the driver has observed the unresolved durable row
	// in both mounted views.
	waitRetryMilestone(ctx, t, milestonePath, "retry-held", driverDone)
	continuation.RespondToolCall("communicate", communicateArgs("RETRY_DONE_two-views"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	thread := readRetryJourneyThread(ctx, t, client, ref)
	counts := retryUserInputCounts(thread)
	if counts[original] != 1 || counts["continue"] != 1 || len(counts) != 2 {
		t.Fatalf("authoritative user input counts: %v", counts)
	}
	if len(thread.Turns) != 3 || thread.Turns[1].Status != "failed" || thread.Turns[2].Status != "completed" {
		t.Fatal("expected the prelude, the failed original turn, and one completed continuation")
	}
	assertRetryNativeCanonicalIDs(t, milestonePath, thread)
}

// runRetryNativeLatestOnlyJourney advances a real session through a newer
// running turn with no display items, a successful turn, and a newer failed
// turn. The older action must be gone at every step and a stale callback must
// submit nothing; only the newer failure's Retry sends the one continuation.
func runRetryNativeLatestOnlyJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_native_latest_only"
	ref := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	calls, stopCalls := retryJourneyCalls(ctx, provider)
	defer stopCalls()
	dir := t.TempDir()
	milestonePath := filepath.Join(dir, "milestones.jsonl")
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryNativeJourneyDriver(ctx, stack, dir, milestonePath, ref, "latest-only")
	}()

	running := nextRetryCall(ctx, t, calls, "the newer running turn", driverDone)
	if texts := running.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: RETRY_RUNNING_NEW") {
		t.Fatalf("newer running turn lost its inputs: %v", texts)
	}
	// Hold the running turn until the native driver has observed the newer
	// running identity with zero items and no older action.
	waitRetryMilestone(ctx, t, milestonePath, "running-empty", driverDone)
	running.RespondToolCall("communicate", communicateArgs("RETRY_RUNNING_DONE"))

	failing := nextRetryCall(ctx, t, calls, "the newer failing turn", driverDone)
	if texts := failing.Texts(); !slices.Contains(texts, "user: RETRY_NEWER_FAIL") {
		t.Fatalf("newer failing turn lost its input: %v", texts)
	}
	failing.RespondHTTPError(http.StatusBadRequest, "retry_fixture_native_newer")

	newest := nextRetryCall(ctx, t, calls, "the newest retry", driverDone)
	if texts := newest.Texts(); !slices.Contains(texts, "user: continue") {
		t.Fatalf("the newer failure's Retry did not send the continuation: %v", texts)
	}
	if texts := newest.Texts(); slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
		t.Fatal("the staged draft reached the provider")
	}
	waitRetryMilestone(ctx, t, milestonePath, "newest-retry", driverDone)
	newest.RespondToolCall("communicate", communicateArgs("RETRY_NEWEST_DONE"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	thread := readRetryJourneyThread(ctx, t, client, ref)
	counts := retryUserInputCounts(thread)
	if counts[original] != 1 || counts["RETRY_RUNNING_NEW"] != 1 || counts["RETRY_NEWER_FAIL"] != 1 || counts["continue"] != 1 {
		t.Fatalf("authoritative user input counts: %v", counts)
	}
	if len(thread.Turns) != 5 ||
		thread.Turns[1].Status != "failed" || thread.Turns[2].Status != "completed" ||
		thread.Turns[3].Status != "failed" || thread.Turns[4].Status != "completed" {
		t.Fatalf("turn outcomes: %+v", thread.Turns)
	}
	assertRetryNativeCanonicalIDs(t, milestonePath, thread)
}

// runRetryNativeRemountJourney replaces the view tree while the acceptance
// reply is outstanding. The new attachments must read the same unresolved
// durable row, keep Retry inactive, and let the outbox recover exactly one
// continuation after the lost reply is replayed on reconnect.
func runRetryNativeRemountJourney(t *testing.T, ctx context.Context, stack hubStack, client *appwire.Client, provider *fakellm.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	original := "RETRY_ORIGINAL_native_remount"
	ref := startRetryJourneyThread(t, ctx, stack, client, provider, original)
	calls, stopCalls := retryJourneyCalls(ctx, provider)
	defer stopCalls()
	dir := t.TempDir()
	milestonePath := filepath.Join(dir, "milestones.jsonl")
	driverDone := make(chan error, 1)
	go func() {
		driverDone <- retryNativeJourneyDriver(ctx, stack, dir, milestonePath, ref, "remount-unresolved")
	}()

	continuation := nextRetryCall(ctx, t, calls, "the lost-reply continuation", driverDone)
	if texts := continuation.Texts(); !slices.Contains(texts, "user: "+original) || !slices.Contains(texts, "user: continue") {
		t.Fatalf("continuation lost the original input: %v", texts)
	}
	if texts := continuation.Texts(); slices.Contains(texts, "user: RETRY_UNSENT_DRAFT") {
		t.Fatal("the staged draft reached the provider")
	}
	// Hold the acceptance until the driver has remounted and proved the new
	// view keeps Retry inactive against the same durable row.
	waitRetryMilestone(ctx, t, milestonePath, "remounted-unresolved", driverDone)
	continuation.RespondToolCall("communicate", communicateArgs("RETRY_RECOVERED_REMOUNT"))

	if err := <-driverDone; err != nil {
		t.Fatal(err)
	}
	thread := readRetryJourneyThread(ctx, t, client, ref)
	counts := retryUserInputCounts(thread)
	if counts[original] != 1 || counts["continue"] != 1 || len(counts) != 2 {
		t.Fatalf("authoritative user input counts after recovery: %v", counts)
	}
	if len(thread.Turns) != 3 || thread.Turns[1].Status != "failed" || thread.Turns[2].Status != "completed" {
		t.Fatal("expected the prelude, the failed original turn, and one completed continuation")
	}
	assertRetryNativeCanonicalIDs(t, milestonePath, thread)
}

// Compare the native canonical model's actual IDs, including its successor,
// with an independent hub read, not provider output copy or an invented ID.
func assertRetryNativeCanonicalIDs(t *testing.T, path string, thread appwire.Thread) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			Milestone string `json:"milestone"`
			Detail    struct {
				TurnIDs []string `json:"turnIds"`
			} `json:"detail"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Milestone == "canonical-settled" {
			seen = event.Detail.TurnIDs
		}
	}
	want := make([]string, len(thread.Turns))
	for i, turn := range thread.Turns {
		want[i] = turn.ID
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("native canonical IDs %v differ from hub IDs %v", seen, want)
	}
}
