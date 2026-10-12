//go:build browserguard

package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestDocumentAnnotationBrowserJourney is the full-stack journey for document
// annotations. It drives the real embedded SPA in real Chrome against a real
// evener-hub process and the real daemon it spawns (only the LLM provider is
// scripted), opens the actual document from a real session file card, selects
// the literal FIRST passage with real CDP mouse input, and then exercises the
// full annotation journey: the comment Send, the exact original-session draft,
// the literal saved note reached through a genuine highlight click, the
// independent clipped geometry, the unchanged-source reopen with a fresh real
// IndexedDB read, and the preservation of unrelated drafts with correct
// original-session routing. The driver reports the five required assertion
// names, and this test validates them independently and requires zero
// forbidden mutations and zero console errors.
//
// A four-minute test-body tripwire bounds the whole run. The authenticated URL
// travels on the driver's stdin only; the token never appears in process
// arguments, diagnostics or report output.
const annotationBrowserTripwire = 4 * time.Minute

// annotationSource is the literal fixture document written into the session
// workspace before the scripted read_file. The hub hashes these exact bytes and
// serves the lowercase SHA256 as the /doc/file ETag, which is the revision the
// driver asserts. It is NOT the synthetic repository REFERENCE/APPENDED golden.
const annotationSource = "First occurrence: same\n\nOther context for a second occurrence is nearby: same\n"

// annotationSetupInput is the exact user text the scripted setup turn carries.
const annotationSetupInput = "ANNOTATION_FIXTURE"

// annotationOtherInput is the exact user text the SECOND session's opening turn
// carries. That session exists so the journey can hold a real unrelated-session
// composer draft: the driver opens it, types a draft through the production
// composer, then returns to the annotation session. The provider observer answers
// this turn's one round as setup traffic too.
const annotationOtherInput = "ANNOTATION_OTHER_FIXTURE"

// annotationDriverResult is the inventory the driver writes to result.json. The
// Go test derives the required assertion names below itself; it never trusts
// the driver's own list.
type annotationDriverResult struct {
	Assertions             map[string]bool `json:"assertions"`
	ForbiddenMutationCalls int             `json:"forbiddenMutationCalls"`
	Failure                string          `json:"failure"`
	Blocked                []string        `json:"blocked"`
	ConsoleErrors          int             `json:"consoleErrors"`
}

// annotationServerState is the authoritative per-session state read through the
// real public ThreadRead seam before and after the driver. TurnCount is the
// daemon's completed model-response count; MutationStateAuthoritative identifies
// a daemon snapshot that can settle uncertain sends (a saved transcript alone
// cannot prove a mutation absent).
type annotationServerState struct {
	TurnCount                  int      `json:"turnCount"`
	ActiveTurnID               string   `json:"activeTurnId"`
	QueueDepth                 int      `json:"queueDepth"`
	QueueRevision              uint64   `json:"queueRevision"`
	PendingMutations           int      `json:"pendingMutations"`
	MutationStateAuthoritative bool     `json:"mutationStateAuthoritative"`
	UserInputs                 []string `json:"userInputs"`
}

// annotationJourneyAssertions is derived here, independently of what the driver
// emits. These are the retained short-document journey's own names.
var annotationJourneyAssertions = []string{
	"finished-selection", "exact-draft", "exact-highlight", "literal-note", "unchanged-reopen",
}

// TestDocumentAnnotationBrowserJourney is the one retained full-stack journey.
func TestDocumentAnnotationBrowserJourney(t *testing.T) {
	runAnnotationBrowserGuard(t)
}

func runAnnotationBrowserGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("browser guard requires Chrome and real daemons")
	}
	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Fatalf("the production frontend is required, run `make build-web` first: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), annotationBrowserTripwire)
	defer cancel()

	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)
	client := stack.dialRPC(ctx, t)

	// Replace the owned fixture's bytes with the literal fixture document before
	// the scripted read. This is test workspace state, never a repository file.
	if err := os.WriteFile(stack.readableFile, []byte(annotationSource), 0o600); err != nil {
		t.Fatalf("write the literal fixture document: %v", err)
	}

	// Evidence directory: kept on failure for triage (see the cascade/skill
	// guards' convention), removed only on a clean pass.
	artifacts, err := os.MkdirTemp(sandboxtest.KeptTempDir(), "annotationguard-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() || os.Getenv("ANNOTATIONGUARD_KEEP_ARTIFACTS") != "" {
			t.Logf("annotation browser evidence: %s", artifacts)
			return
		}
		if err := os.RemoveAll(artifacts); err != nil {
			t.Error(err)
		}
	})

	writeAnnotationSourceIdentity(t, artifacts, annotationSource)

	// The scripted provider observer. It answers the annotation session's setup
	// turn and the second session's setup turn, then parks on provider.Next with
	// an explicit cancellation boundary the test joins after its final
	// real-server observation, so an unexpected extra session-loop request cannot
	// slip past an abandoned observer. A cancellation caused by the test's own
	// observerCancel is not a provider event.
	observerCtx, observerCancel := context.WithCancel(ctx)
	defer observerCancel()
	observerDone := make(chan struct{})
	scriptErr := make(chan error, 1)
	go func() {
		defer close(observerDone)
		// The annotation session's rounds are counted positionally: the first is
		// the read_file round, the second ends the turn, and a third is an
		// annotation action (or the draft) reaching the model, which must fail.
		// The second session contributes exactly one round and is routed by its
		// own prompt text.
		annotationRounds := 0
		for {
			call, err := provider.Next(observerCtx.Done())
			if err != nil {
				if observerCtx.Err() == nil {
					scriptErr <- fmt.Errorf("provider observer stopped before the join boundary: %w", err)
				}
				return
			}
			if call.Contains(annotationOtherInput) && !call.Contains(annotationSetupInput) {
				call.RespondToolCall("communicate", communicateArgs("annotation other fixture ready"))
				continue
			}
			if !call.Contains(annotationSetupInput) {
				scriptErr <- fmt.Errorf("unexpected provider input during the annotation window: %s", strings.Join(call.Texts(), "\n"))
				return
			}
			annotationRounds++
			switch annotationRounds {
			case 1:
				call.RespondToolCall("read_file", map[string]any{"file_path": stack.readableFile})
			case 2:
				call.RespondToolCall("communicate", communicateArgs("annotation fixture ready"))
			default:
				scriptErr <- fmt.Errorf("unexpected provider input during the annotation window: %s", strings.Join(call.Texts(), "\n"))
				return
			}
		}
	}()

	// Idempotent join for the provider observer: cancel it, wait for it to
	// return, and report any session-loop provider event it recorded. Registered
	// immediately so a t.Fatal in setup, the driver, or the final read still
	// joins it; also invoked after the normal final authoritative observation.
	var observerJoin sync.Once
	joinObserver := func() {
		observerJoin.Do(func() {
			observerCancel()
			<-observerDone
			select {
			case providerErr := <-scriptErr:
				t.Errorf("provider observer: %v", providerErr)
			default:
			}
		})
	}
	t.Cleanup(joinObserver)

	started, err := client.ThreadStart(ctx, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		Input:           []appwire.InputItem{{Type: "text", Text: annotationSetupInput}},
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootRef := threadRef(started.Thread)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](cleanupCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: rootRef}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})

	// The observed settled boundary: the setup turn has finished, so only the
	// annotation window follows. TurnCount > 0 is required, or the wait returns
	// vacuously before the turn ever starts.
	awaitThread(ctx, t, client, rootRef, "the opening read_file turn to finish", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Evener.TurnCount > 0
	})

	// Authoritative pre-annotation server baseline, after the setup completed.
	baseline := readAnnotationServerState(ctx, t, client, rootRef)
	requireAnnotationServerSettled(t, "server baseline", baseline)
	requireAnnotationSetupInput(t, "server baseline", baseline)
	writeAnnotationServerEvidence(t, artifacts, "server-baseline.json", baseline)

	// A real SECOND session, so the journey can hold a genuine unrelated-session
	// composer draft. It is started and settled before the driver, and its own
	// setup turn is answered by the observer above.
	startedOther, err := client.ThreadStart(ctx, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		Input:           []appwire.InputItem{{Type: "text", Text: annotationOtherInput}},
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherRef := threadRef(startedOther.Thread)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := clientRequest[appwire.EmptyResponse](cleanupCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: otherRef}); err != nil {
			t.Errorf("thread/shutdown left the second daemon running: %v", err)
		}
	})
	awaitThread(ctx, t, client, otherRef, "the second session's opening turn to finish", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Evener.TurnCount > 0
	})

	// Create the driver's owned artifact files BEFORE it starts (its milestone
	// and control appends assume they exist).
	for _, name := range []string{"control.jsonl", "milestones.jsonl"} {
		if err := os.WriteFile(filepath.Join(artifacts, name), nil, 0o600); err != nil {
			t.Fatalf("create driver artifact %s: %v", name, err)
		}
	}

	fixture, err := json.Marshal(map[string]any{
		"url":           hubedge.AuthURLFor("http://"+stack.addr, stack.token),
		"artifactDir":   artifacts,
		"rootRef":       rootRef,
		"otherRef":      otherRef,
		"controlPath":   filepath.Join(artifacts, "control.jsonl"),
		"milestonePath": filepath.Join(artifacts, "milestones.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}

	log, err := os.Create(filepath.Join(artifacts, "driver.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	driver := exec.CommandContext(ctx, "node", "frontend/scripts/documentannotationguard/run.mjs")
	driver.Stdin = bytes.NewReader(fixture)
	driver.Stdout, driver.Stderr = log, log
	driver.WaitDelay = 15 * time.Second
	for _, arg := range driver.Args {
		if strings.Contains(arg, stack.token) {
			t.Fatal("annotation driver exposes the fixture auth token in process arguments")
		}
	}

	runErr := driver.Run()
	if flushErr := log.Sync(); flushErr != nil {
		t.Logf("driver log sync: %v", flushErr)
	}

	// Whatever the driver reported, surface its assertion inventory.
	result, haveResult := readAnnotationDriverResult(t, artifacts)

	// Authoritative post-journey server observation, AFTER the entire driver.
	final := readAnnotationServerState(ctx, t, client, rootRef)
	requireAnnotationServerSettled(t, "server final", final)
	requireAnnotationSetupInput(t, "server final", final)
	if final.TurnCount != baseline.TurnCount {
		t.Errorf("the annotation journey advanced the completed turn count from %d to %d", baseline.TurnCount, final.TurnCount)
	}
	if final.QueueRevision != baseline.QueueRevision {
		t.Errorf("the annotation journey advanced the queue revision from %d to %d", baseline.QueueRevision, final.QueueRevision)
	}
	writeAnnotationServerEvidence(t, artifacts, "server-final.json", final)

	// Join the provider observer after the final server observation, so the
	// cancellation is the test's own boundary and no extra request is missed.
	joinObserver()

	if runErr != nil {
		skillGuardLogDriverTail(t, log.Name())
		if haveResult {
			t.Errorf("annotation browser journey stopped: %v\ncompleted assertions: %v\nblocked assertions: %v\nforbidden mutation calls: %d\nconsole errors: %d\nevidence: %s",
				runErr, annotationReached(result, annotationJourneyAssertions, true), result.Blocked, result.ForbiddenMutationCalls, result.ConsoleErrors, artifacts)
		}
		t.Fatalf("annotation browser journey did not complete: %v (evidence: %s)", runErr, artifacts)
	}

	// Clean pass shape: every required assertion holds and the annotation issued
	// no input mutation.
	if !haveResult {
		t.Fatalf("the driver exited zero without a result.json inventory (evidence: %s)", artifacts)
	}
	for _, name := range annotationJourneyAssertions {
		if !result.Assertions[name] {
			t.Errorf("missing passing assertion %s", name)
		}
	}
	if result.ForbiddenMutationCalls != 0 {
		t.Errorf("annotation issued %d input mutations, want 0", result.ForbiddenMutationCalls)
	}
	if result.ConsoleErrors != 0 {
		t.Errorf("the browser console reported %d warnings/errors during the journey, want 0", result.ConsoleErrors)
	}
}

// readAnnotationServerState reads the authoritative per-session state through
// the real public ThreadRead seam. A read failure is a setup/server-evidence
// failure, never a silent pass.
func readAnnotationServerState(ctx context.Context, t *testing.T, client *appwire.Client, ref string) annotationServerState {
	t.Helper()
	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: 40})
	if err != nil {
		t.Fatalf("read the thread for server-side annotation evidence: %v", err)
	}
	state := annotationServerState{
		TurnCount:                  read.Thread.Evener.TurnCount,
		ActiveTurnID:               read.Thread.Evener.ActiveTurnID,
		QueueDepth:                 read.Thread.Evener.Queue.Depth,
		QueueRevision:              read.Thread.Evener.Queue.Revision,
		PendingMutations:           len(read.Thread.Evener.PendingMutations),
		MutationStateAuthoritative: read.Thread.Evener.MutationStateAuthoritative,
	}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" {
				state.UserInputs = append(state.UserInputs, item.Text)
			}
		}
	}
	return state
}

// requireAnnotationServerSettled requires a live authoritative, idle session
// with no queued or pending work.
func requireAnnotationServerSettled(t *testing.T, what string, state annotationServerState) {
	t.Helper()
	if !state.MutationStateAuthoritative {
		t.Errorf("%s: mutation state is not authoritative, so mutation absence cannot be proven", what)
	}
	if state.ActiveTurnID != "" {
		t.Errorf("%s: active turn %q, want idle", what, state.ActiveTurnID)
	}
	if state.QueueDepth != 0 {
		t.Errorf("%s: queue depth %d, want 0", what, state.QueueDepth)
	}
	if state.PendingMutations != 0 {
		t.Errorf("%s: %d pending mutations, want 0", what, state.PendingMutations)
	}
}

// requireAnnotationSetupInput requires exactly the one setup user input, matched
// by exact text, never a substring.
func requireAnnotationSetupInput(t *testing.T, what string, state annotationServerState) {
	t.Helper()
	if len(state.UserInputs) != 1 || state.UserInputs[0] != annotationSetupInput {
		t.Errorf("%s: user inputs = %q, want exactly [%s]", what, state.UserInputs, annotationSetupInput)
	}
}

func writeAnnotationServerEvidence(t *testing.T, artifacts, name string, state annotationServerState) {
	t.Helper()
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, name), body, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// annotationReached returns the assertion names the driver recorded as reached
// (or, when want is false, as not reached).
func annotationReached(result annotationDriverResult, names []string, want bool) []string {
	var out []string
	for _, name := range names {
		if result.Assertions[name] == want {
			out = append(out, name)
		}
	}
	return out
}

func readAnnotationDriverResult(t *testing.T, artifacts string) (annotationDriverResult, bool) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(artifacts, "result.json"))
	if err != nil {
		t.Logf("no driver result.json (%v); the driver may have failed before writing it", err)
		return annotationDriverResult{}, false
	}
	var result annotationDriverResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Logf("unreadable driver result.json: %v\n%s", err, body)
		return annotationDriverResult{}, false
	}
	return result, true
}

// writeAnnotationSourceIdentity records the exact inputs this run bound to: the
// BASE commit, the worktree dirtiness, the built frontend hash and the literal
// fixture identity. The token is never written.
func writeAnnotationSourceIdentity(t *testing.T, artifacts, source string) {
	t.Helper()
	digest := sha256.Sum256([]byte(source))
	identity := map[string]any{
		"fixtureSha256": fmt.Sprintf("%x", digest),
		"fixtureBytes":  len(source),
	}
	for key, command := range map[string][]string{
		"base":  {"git", "rev-parse", "HEAD"},
		"dirty": {"git", "status", "--porcelain"},
	} {
		out, err := exec.Command(command[0], command[1:]...).Output()
		if err != nil {
			identity[key] = fmt.Sprintf("<error: %v>", err)
			continue
		}
		identity[key] = strings.TrimSpace(string(out))
	}
	if hash, err := frontendDistHash(distFS()); err != nil {
		identity["frontendDistHash"] = fmt.Sprintf("<error: %v>", err)
	} else {
		identity["frontendDistHash"] = hash
	}
	body, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		t.Fatalf("marshal source identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "source-identity.json"), body, 0o600); err != nil {
		t.Fatalf("write source identity: %v", err)
	}
}
