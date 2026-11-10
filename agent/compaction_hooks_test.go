package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// compactionHookSession is a session whose plugin declares a PreCompact and a
// PostCompact command hook. Each hook appends its stdin (the hook input JSON) to
// its capture file, so a test can decode exactly what each event delivered and
// count how often it fired.
type compactionHookSession struct {
	sess *Session
	pre  string
	post string
}

// doneResponder is the one text reply this fixture's scripted model gives every
// round, named once so the fixture's repeated steps read as deliberate.
func doneResponder(llm.Request) llm.Response { return finalResponse("done") }

// newCompactionHookSession builds the fixture around strategy, which decides
// whether a request-time fold reports a real compaction. A capture file that the
// hook never ran leaves no file, which readCompactionHookInputs reports as no
// runs.
func newCompactionHookSession(t *testing.T, strategy contextmgr.Strategy) compactionHookSession {
	t.Helper()
	control := t.TempDir()
	prePath := filepath.Join(control, "pre-compact-input.jsonl")
	postPath := filepath.Join(control, "post-compact-input.jsonl")
	appendStdin := func(path string) string {
		return "cat >> " + shellquote.Literal(path) + "; echo >> " + shellquote.Literal(path)
	}
	hooksJSON, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PreCompact": []any{
				map[string]any{"matcher": "*", "hooks": []any{
					map[string]any{"type": "command", "timeout": 10, "command": appendStdin(prePath)},
				}},
			},
			"PostCompact": []any{
				map[string]any{"matcher": "*", "hooks": []any{
					map[string]any{"type": "command", "timeout": 10, "command": appendStdin(postPath)},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal hooks.json: %v", err)
	}
	pluginDir := writePluginHooks(t, "compaction-hook-plugin", string(hooksJSON))

	// Three identical rounds: the fold's request, then the summary request, then
	// whichever the strategy lets through. Same reply every time, so the steps
	// are built rather than written out.
	steps := make([]func(llm.Request) llm.Response, 3)
	for i := range steps {
		steps[i] = doneResponder
	}
	sess := newSession(t,
		withDir(t.TempDir()),
		withSteps(steps...),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			PluginDirs:       []string{pluginDir},
			AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			testOnly: testConfig{
				skipGitSnapshot:         true,
				contextStrategyOverride: strategy,
			},
		}),
	)
	return compactionHookSession{sess: sess, pre: prePath, post: postPath}
}

// readCompactionHookInputs decodes every hook input a capture file holds, in
// run order. A missing file means the hook never ran.
func readCompactionHookInputs(t *testing.T, path string) []hooks.Input {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read hook capture %s: %v", path, err)
	}
	var inputs []hooks.Input
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var input hooks.Input
		if err := json.Unmarshal([]byte(line), &input); err != nil {
			t.Fatalf("decode hook input %q: %v", line, err)
		}
		inputs = append(inputs, input)
	}
	return inputs
}

// TestPostCompact_FiresOncePerRealCompaction proves a request-time fold that
// compacted nothing fires no PostCompact, while /compact fires manual and a
// pressure fold fires auto.
func TestPostCompact_FiresOncePerRealCompaction(t *testing.T) {
	noop := newCompactionHookSession(t, compactionEventStrategy{emitCompaction: false})
	if _, err := noop.sess.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := readCompactionHookInputs(t, noop.pre); len(got) != 0 {
		t.Fatalf("a fold that emitted no compaction ran PreCompact %d time(s): %+v", len(got), got)
	}
	if got := readCompactionHookInputs(t, noop.post); len(got) != 0 {
		t.Fatalf("a fold that compacted nothing fired PostCompact %d time(s): %+v", len(got), got)
	}

	pressure := newCompactionHookSession(t, compactionEventStrategy{emitCompaction: true})
	if _, err := pressure.sess.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	post := readCompactionHookInputs(t, pressure.post)
	if len(post) != 1 {
		t.Fatalf("a pressure fold fired PostCompact %d time(s), want 1: %+v", len(post), post)
	}
	if post[0].Trigger != "auto" {
		t.Fatalf("a pressure fold's PostCompact trigger = %q, want auto", post[0].Trigger)
	}

	manual := newCompactionHookSession(t, compactionEventStrategy{emitCompaction: true})
	if err := manual.sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	post = readCompactionHookInputs(t, manual.post)
	if len(post) != 1 {
		t.Fatalf("/compact fired PostCompact %d time(s), want 1: %+v", len(post), post)
	}
	if post[0].Trigger != "manual" {
		t.Fatalf("/compact's PostCompact trigger = %q, want manual", post[0].Trigger)
	}
}

// TestPreCompact_CarriesTriggerAndInstructions proves the input's trigger and
// custom_instructions match the fold: manual and absent custom_instructions for
// /compact (evener's Compact carries none), and auto with the fold's
// instructions for a fold given them (compact_context), auto and absent for a
// pressure fold.
func TestPreCompact_CarriesTriggerAndInstructions(t *testing.T) {
	pressure := newCompactionHookSession(t, compactionEventStrategy{emitCompaction: true})
	if _, err := pressure.sess.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	pre := readCompactionHookInputs(t, pressure.pre)
	if len(pre) != 1 {
		t.Fatalf("a pressure fold ran PreCompact %d time(s), want 1: %+v", len(pre), pre)
	}
	if pre[0].Trigger != "auto" {
		t.Fatalf("a pressure fold's PreCompact trigger = %q, want auto", pre[0].Trigger)
	}
	if pre[0].CustomInstructions != nil {
		t.Fatalf("a pressure fold's custom_instructions = %q, want absent", *pre[0].CustomInstructions)
	}

	const instructions = "condense the API notes"
	steered := newCompactionHookSession(t, compactionEventStrategy{emitCompaction: true})
	if ok, refusal := steered.sess.foldWithForceCompact(context.Background(), instructions, nil, triggerAuto); !ok {
		t.Fatalf("fold with instructions lost publication: %v", refusal)
	}
	pre = readCompactionHookInputs(t, steered.pre)
	if len(pre) != 1 {
		t.Fatalf("a fold with instructions ran PreCompact %d time(s), want 1: %+v", len(pre), pre)
	}
	if pre[0].Trigger != "auto" {
		t.Fatalf("compact_context's PreCompact trigger = %q, want auto", pre[0].Trigger)
	}
	if pre[0].CustomInstructions == nil || *pre[0].CustomInstructions != instructions {
		t.Fatalf("custom_instructions = %v, want %q", pre[0].CustomInstructions, instructions)
	}

	// Whitespace alone is absence, not an instruction: Claude's null means the
	// user passed nothing.
	blank := newCompactionHookSession(t, compactionEventStrategy{emitCompaction: true})
	if ok, refusal := blank.sess.foldWithForceCompact(context.Background(), "   ", nil, triggerAuto); !ok {
		t.Fatalf("fold with whitespace instructions lost publication: %v", refusal)
	}
	pre = readCompactionHookInputs(t, blank.pre)
	if len(pre) != 1 {
		t.Fatalf("a fold with whitespace instructions ran PreCompact %d time(s), want 1: %+v", len(pre), pre)
	}
	if pre[0].CustomInstructions != nil {
		t.Fatalf("whitespace-only custom_instructions = %q, want absent", *pre[0].CustomInstructions)
	}

	manual := newCompactionHookSession(t, compactionEventStrategy{emitCompaction: true})
	if err := manual.sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	pre = readCompactionHookInputs(t, manual.pre)
	if len(pre) != 1 {
		t.Fatalf("/compact ran PreCompact %d time(s), want 1: %+v", len(pre), pre)
	}
	if pre[0].Trigger != "manual" {
		t.Fatalf("/compact's PreCompact trigger = %q, want manual", pre[0].Trigger)
	}
	if pre[0].CustomInstructions != nil {
		t.Fatalf("/compact's custom_instructions = %q, want absent", *pre[0].CustomInstructions)
	}
}

// TestPostCompact_CarriesCompactSummary proves compact_summary is the fold's
// summary text: a real /compact over the production context manager writes a
// checkpoint and then a summary, and the summary — not the checkpoint — is what
// PostCompact reports.
func TestPostCompact_CarriesCompactSummary(t *testing.T) {
	const summaryMarker = "SUMMARY-SENTINEL"
	const summary = "[CONTEXT SUMMARY]\n## Progress\n" + summaryMarker + "\n[END SUMMARY]"
	s, postPath := newSummaryCompactionHookSession(t, "compact-summary-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(summary)}
	})
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): a real fold

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	post := readCompactionHookInputs(t, postPath)
	if len(post) != 1 {
		t.Fatalf("/compact fired PostCompact %d time(s), want 1: %+v", len(post), post)
	}
	if !strings.Contains(post[0].CompactSummary, summaryMarker) {
		t.Fatalf("compact_summary = %q, want the fold's summary text", post[0].CompactSummary)
	}
	if strings.Contains(post[0].CompactSummary, "[CONTEXT CHECKPOINT]") {
		t.Fatalf("compact_summary = %q, want the summary, not the checkpoint", post[0].CompactSummary)
	}
}

// newSummaryCompactionHookSession builds a session whose folds run the real
// context manager (the cheap-model route answers the summarizer through
// responder) and whose plugin captures every PostCompact hook input.
func newSummaryCompactionHookSession(t *testing.T, provider string, responder func(llm.Request) llm.Response) (*Session, string) {
	t.Helper()
	postPath := filepath.Join(t.TempDir(), "post-compact-input.jsonl")
	hooksJSON, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PostCompact": []any{
				map[string]any{"matcher": "*", "hooks": []any{
					map[string]any{
						"type":    "command",
						"timeout": 10,
						"command": "cat >> " + shellquote.Literal(postPath) + "; echo >> " + shellquote.Literal(postPath),
					},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal hooks.json: %v", err)
	}
	pluginDir := writePluginHooks(t, "compact-summary-hook-plugin", string(hooksJSON))

	s := newScriptedSummaryCompactSession(t, provider, responder,
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			PluginDirs:       []string{pluginDir},
			AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			testOnly:         testConfig{skipGitSnapshot: true},
		}))
	return s, postPath
}

// TestPostCompact_SupersededFoldStaysSilent proves the second half of the gate:
// an older fold whose flush runs after a newer fold published fires no
// PostCompact, like every other last-write-wins effect. Only the newest
// publication's PostCompact fires, and it carries the newest summary.
func TestPostCompact_SupersededFoldStaysSilent(t *testing.T) {
	var summaries atomic.Int32
	s, postPath := newSummaryCompactionHookSession(t, "post-compact-order-cheap", func(llm.Request) llm.Response {
		n := summaries.Add(1)
		text := "[CONTEXT SUMMARY]\n## Progress\nfold-" + strconv.Itoa(int(n)) + "\n[END SUMMARY]"
		return llm.Response{Message: llm.Assistant(text)}
	})
	seedNumberedSessionHistory(t, s, 12)

	parked := make(chan struct{})
	release := make(chan struct{})
	var flushes atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		// Fold A parks BEFORE its flush, so its own supersession check runs
		// after fold B has published — the older-flush-runs-late ordering the
		// last-write-wins gate exists for.
		cfg.beforeFoldSideEffectsFlush = func() {
			if flushes.Add(1) == 1 {
				close(parked)
				<-release
			}
		}
	})

	errA := make(chan error, 1)
	go func() {
		errA <- s.Compact(context.Background()) // fold A: publishes, then parks before its flush
	}()
	<-parked

	if err := s.Compact(context.Background()); err != nil { // fold B: full fold in that window
		t.Fatalf("Compact (B): %v", err)
	}
	close(release)
	if err := <-errA; err != nil {
		t.Fatalf("Compact (A): %v", err)
	}

	post := readCompactionHookInputs(t, postPath)
	if len(post) != 1 {
		t.Fatalf("PostCompact fired %d time(s), want 1: a superseded fold must stay silent: %+v", len(post), post)
	}
	if !strings.Contains(post[0].CompactSummary, "fold-2") {
		t.Fatalf("compact_summary = %q, want the newest fold's summary", post[0].CompactSummary)
	}
}

// TestPostCompact_HonorsTheFoldsCancellation proves PostCompact runs on the
// fold's context, like its PreCompact sibling: a request interrupted while the
// hook runs must not wait the hook's full timeout. The hook sleeps far longer
// than the test's bound; only a cancellation from the fold's ctx can end it in
// time. Falling back to the session lifetime fails this test after 10s (the
// hook's timeout).
func TestPostCompact_HonorsTheFoldsCancellation(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "post-compact-started")
	hooksJSON, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"PostCompact": []any{
				map[string]any{"matcher": "*", "hooks": []any{
					map[string]any{
						"type":    "command",
						"timeout": 10,
						"command": "touch " + shellquote.Literal(started) + "; sleep 30",
					},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal hooks.json: %v", err)
	}
	pluginDir := writePluginHooks(t, "post-compact-cancel-plugin", string(hooksJSON))
	// Three identical scripted rounds: the fold's request, the summary request,
	// then whichever the strategy lets through. Built rather than written out, as
	// the sibling fixture does, so the repetition is deliberate.
	steps := make([]func(llm.Request) llm.Response, 3)
	for i := range steps {
		steps[i] = doneResponder
	}
	sess := newSession(t,
		withDir(t.TempDir()),
		withSteps(steps...),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			PluginDirs:       []string{pluginDir},
			AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
			testOnly: testConfig{
				skipGitSnapshot:         true,
				contextStrategyOverride: compactionEventStrategy{emitCompaction: true},
			},
		}),
	)
	drainSessionEvents(sess)
	seedNumberedSessionHistory(t, sess, 12)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel once the hook is actually running, which is the interruption the
	// test is about: the fold's own work is done and only the hook is left.
	go func() {
		// TRIPWIRE: a local shell hook and a scripted model, so only a genuine
		// hang reaches 30s.
		waitForCondition(t, 30*time.Second, "the PostCompact hook to start", func() bool {
			_, statErr := os.Stat(started)
			return statErr == nil
		})
		cancel()
	}()

	begin := time.Now()
	_ = sess.Compact(ctx)
	elapsed := time.Since(begin)
	sess.Close()

	// The hook must have run: a fold that never fired PostCompact would make the
	// timing below meaningless.
	if _, statErr := os.Stat(started); statErr != nil {
		t.Fatalf("PostCompact never started: %v", statErr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Compact took %s with a cancelled ctx, want well under the hook's 10s timeout: "+
			"PostCompact did not see the fold's cancellation", elapsed)
	}
}

// TestPostCompact_SupersessionReadAtTheHookNotFlushStart is the stale-flag
// case. Fold A's flush samples "am I newest?" at its start, then runs every
// deferred effect; a newer fold B publishing DURING that stretch means A's
// answer is stale by the time the hook would fire, and A would announce a
// summary that is no longer the newest compaction.
//
// The seam used here is afterFoldSideEffectsFlush, which sits at the end of
// fold A's flush, so fold B publishes after A's flush-start check passed. Only
// fold B's PostCompact may run.
func TestPostCompact_SupersessionReadAtTheHookNotFlushStart(t *testing.T) {
	var summaries atomic.Int32
	var bPublished atomic.Bool
	s, postPath := newSummaryCompactionHookSession(t, "post-compact-stale-flag-cheap", func(llm.Request) llm.Response {
		n := summaries.Add(1)
		text := "[CONTEXT SUMMARY]\n## Progress\nfold-" + strconv.Itoa(int(n)) + "\n[END SUMMARY]"
		return llm.Response{Message: llm.Assistant(text)}
	})
	seedNumberedSessionHistory(t, s, 12)

	var seams atomic.Int32
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterFoldSideEffectsFlush = func() {
			if seams.Add(1) != 1 {
				return // fold B's own flush: nothing to schedule
			}
			// Fold A's flush-start supersession check has already run and
			// judged A current. Publish B now, in the window before A's hook.
			if err := s.Compact(context.Background()); err != nil {
				t.Errorf("Compact (B): %v", err)
				return
			}
			bPublished.Store(true)
		}
	})

	// Fold A is the outermost Compact; its seam publishes B synchronously.
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact (A): %v", err)
	}
	if !bPublished.Load() {
		t.Fatal("fold B never published inside fold A's flush window")
	}

	post := readCompactionHookInputs(t, postPath)
	if len(post) != 1 {
		t.Fatalf("PostCompact fired %d time(s), want 1 (fold B's only): fold A's flush-start supersession answer was stale by the time its hook ran: %+v",
			len(post), post)
	}
	if !strings.Contains(post[0].CompactSummary, "fold-2") {
		t.Fatalf("compact_summary = %q, want the newest fold's summary", post[0].CompactSummary)
	}
}
