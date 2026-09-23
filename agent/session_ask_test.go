package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// askUserArgsValid builds one valid ask_user question (spec §4.2's own example).
func askUserArgsValid() map[string]any {
	return map[string]any{
		"questions": []any{
			map[string]any{
				"header":   "DB choice",
				"question": "Which datastore for the ingest path?",
				"options": []any{
					map[string]any{"label": "Postgres", "detail": "matches prod; heavier local setup", "recommended": true},
					map[string]any{"label": "SQLite", "detail": "zero setup; diverges from prod"},
				},
			},
		},
	}
}

// askUserArgsTwoQuestions builds a single ask_user call carrying two distinct
// valid questions, so tests can assert the pending set accumulates per
// question, not per call.
func askUserArgsTwoQuestions() map[string]any {
	return map[string]any{
		"questions": []any{
			map[string]any{
				"header":   "DB choice",
				"question": "Which datastore for the ingest path?",
				"options": []any{
					map[string]any{"label": "Postgres", "detail": "matches prod"},
					map[string]any{"label": "SQLite", "detail": "zero setup"},
				},
			},
			map[string]any{
				"header":   "Naming",
				"question": "What should we call the new package?",
				"options": []any{
					map[string]any{"label": "short names", "detail": "terse"},
					map[string]any{"label": "descriptive names", "detail": "verbose"},
				},
			},
		},
	}
}

// askUserArgsDuplicateLabels builds one question whose two options share a
// label — a semantic violation the JSON schema cannot express (spec §4.2).
func askUserArgsDuplicateLabels() map[string]any {
	return map[string]any{
		"questions": []any{
			map[string]any{
				"header":   "DB choice",
				"question": "Which datastore for the ingest path?",
				"options": []any{
					map[string]any{"label": "Postgres", "detail": "matches prod"},
					map[string]any{"label": "Postgres", "detail": "a different detail"},
				},
			},
		},
	}
}

// askUserArgsTwoRecommended builds one question with two options both marked
// recommended — the other semantic violation schema validation cannot catch.
func askUserArgsTwoRecommended() map[string]any {
	return map[string]any{
		"questions": []any{
			map[string]any{
				"header":   "DB choice",
				"question": "Which datastore for the ingest path?",
				"options": []any{
					map[string]any{"label": "Postgres", "detail": "matches prod", "recommended": true},
					map[string]any{"label": "SQLite", "detail": "zero setup", "recommended": true},
				},
			},
		},
	}
}

// askUserCall builds an ask_user tool call from an args map.
func askUserCall(id string, args map[string]any) llm.ToolCallData {
	raw, _ := json.Marshal(args)
	return llm.ToolCallData{ID: id, Name: "ask_user", Arguments: raw, Type: "function"}
}

// newAskTestSession builds a plain interactive root session for ask_user
// handler tests (no scripted LLM calls needed — these tests drive the
// registry directly).
func newAskTestSession(t *testing.T, cfg SessionConfig) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// hasToolDef reports whether name is present among defs.
func hasToolDef(defs []llm.ToolDefinition, name string) bool {
	for _, td := range defs {
		if td.Name == name {
			return true
		}
	}
	return false
}

// TestAskUser_VisibleInteractiveRoot is the control case: a default
// interactive root session advertises and registers ask_user (spec §7 opens
// with "available in every interactive root session").
func TestAskUser_VisibleInteractiveRoot(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	if !hasToolDef(sess.ToolDefinitions(), "ask_user") {
		t.Fatal("ask_user missing from an interactive root session's advertised tools")
	}
	if sess.reg.Get("ask_user") == nil {
		t.Fatal("ask_user not registered in an interactive root session")
	}
}

// TestAskUser_InvisibleNonInteractive covers spec §7 point 1 (registration
// gate) and point 3 (unregistered == unexecutable): a NonInteractive session
// never advertises ask_user, and a forced registry call hits the generic
// unknown-tool path (there is no handler to reach).
func TestAskUser_InvisibleNonInteractive(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{NonInteractive: true})

	if hasToolDef(sess.ToolDefinitions(), "ask_user") {
		t.Fatal("ask_user advertised in a NonInteractive session")
	}
	if sess.reg.Get("ask_user") != nil {
		t.Fatal("ask_user registered in a NonInteractive session")
	}

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsValid()))
	if !res.IsError || !strings.Contains(res.Output, "unknown tool: ask_user") {
		t.Fatalf("ExecuteCall on unregistered ask_user = %+v, want an unknown-tool error", res)
	}
}

// TestAskUser_InvisibleForSubagent covers spec §7 point 1's spawn-carrier
// branch: a live spawn (cfg.spawn.parentSessionID set — the shape
// TestChildRegistryKeepsDelegateWithAllowance uses) never sees ask_user.
func TestAskUser_InvisibleForSubagent(t *testing.T) {
	t.Parallel()
	cfg := SessionConfig{NoProjectPrompts: true}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = 1
	sess := newAskTestSession(t, cfg)

	if hasToolDef(sess.ToolDefinitions(), "ask_user") {
		t.Fatal("ask_user advertised in a subagent session")
	}
	if sess.reg.Get("ask_user") != nil {
		t.Fatal("ask_user registered in a subagent session")
	}
}

// TestAskUser_RestoredSubagentStaysInvisible covers spec §7 point 1's harder
// case: a bare `serve --resume <delegate-id>` restores with an EMPTY spawn
// carrier (spawn is json:"-", never persisted), so the gate must fall back to
// the persisted meta.IsSubagent flag or a resumed delegate would regain
// ask_user.
func TestAskUser_RestoredSubagentStaysInvisible(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	meta := schema.SessionMeta{
		ID:         "restored-subagent",
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		IsSubagent: true,
		Config:     (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
	}
	// restoreCfg.spawn intentionally left zero: the empty-carrier case.
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if restored.cfg.spawn.parentSessionID != "" {
		t.Fatalf("test setup: spawn carrier not empty (%q) — the case under test requires it empty", restored.cfg.spawn.parentSessionID)
	}
	if hasToolDef(restored.ToolDefinitions(), "ask_user") {
		t.Fatal("ask_user advertised in a restored bare-resume subagent session")
	}
	if restored.reg.Get("ask_user") != nil {
		t.Fatal("ask_user registered in a restored bare-resume subagent session")
	}
}

// TestAskUser_ExecGuardUnderConfigDrift exercises the exec-time guard itself
// (spec §7 point 4, defense in depth): register ask_user on an interactive
// root session, then deliberately drift its config to non-interactive AFTER
// registration (config is otherwise immutable post-init — this mutation
// exists only to simulate the drift the guard defends against) and confirm
// Exec still refuses rather than trusting the registration-time gate alone.
func TestAskUser_ExecGuardUnderConfigDrift(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	if sess.reg.Get("ask_user") == nil {
		t.Fatal("ask_user not registered on an interactive root session")
	}

	sess.cfg.NonInteractive = true // deliberate drift injection, see comment above

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsValid()))
	if !res.IsError || res.Output != askUserUnavailableErr {
		t.Fatalf("Exec under config drift = %+v, want IsError=true Output=%q", res, askUserUnavailableErr)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (guarded call must post nothing)", got)
	}
}

// TestAskUser_ValidationDuplicateLabels covers spec §4.2's label-uniqueness
// rule, which the JSON schema cannot express: a violation returns the
// instructive error and posts nothing.
func TestAskUser_ValidationDuplicateLabels(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsDuplicateLabels()))
	if !res.IsError || !strings.Contains(res.Output, "ask_user: option labels must be unique within a question") {
		t.Fatalf("duplicate-label call = %+v, want the instructive uniqueness error", res)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (rejected call must post nothing)", got)
	}
}

// TestAskUser_ValidationTwoRecommended covers spec §4.2's at-most-one-
// recommended rule.
func TestAskUser_ValidationTwoRecommended(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsTwoRecommended()))
	if !res.IsError || !strings.Contains(res.Output, "ask_user: at most one option may be recommended") {
		t.Fatalf("two-recommended call = %+v, want the instructive recommended error", res)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (rejected call must post nothing)", got)
	}
}

// TestAskUser_ValidCallPostsAckAndPending covers the success path (spec
// §5.1): a valid call returns the ack verbatim and grows the pending set by
// one entry per question (not per call) — multiple questions in one call
// each count individually, matching §4.3's global cross-call numbering.
func TestAskUser_ValidCallPostsAckAndPending(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount before any call = %d, want 0", got)
	}

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsValid()))
	if res.IsError {
		t.Fatalf("valid ask_user call errored: %s", res.Output)
	}
	if res.Output != askUserAckText {
		t.Fatalf("ack = %q, want %q", res.Output, askUserAckText)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("askPendingCount after 1 question = %d, want 1", got)
	}

	res2 := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c2", askUserArgsTwoQuestions()))
	if res2.IsError {
		t.Fatalf("second valid ask_user call errored: %s", res2.Output)
	}
	if got := sess.askPendingCount(); got != 3 {
		t.Fatalf("askPendingCount after a 2-question call = %d, want 3 (1 + 2)", got)
	}
}

func TestAskUser_LongHeaderIsAcceptedAndPreserved(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	args := askUserArgsValid()
	const wantHeader = "Progress flavor"
	args["questions"].([]any)[0].(map[string]any)["header"] = wantHeader

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", args))
	if res.IsError {
		t.Fatalf("ask_user with long header errored: %s", res.Output)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if got := sess.askPending[0].Header; got != wantHeader {
		t.Fatalf("pending header = %q, want %q", got, wantHeader)
	}
}

func TestAskUser_OmittedHeaderUsesEmptyInternalHeader(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	args := askUserArgsValid()
	delete(args["questions"].([]any)[0].(map[string]any), "header")

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", args))
	if res.IsError {
		t.Fatalf("ask_user without header errored: %s", res.Output)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if got := sess.askPending[0].Header; got != "" {
		t.Fatalf("pending header = %q, want empty internal header", got)
	}
}

// TestAskUser_ClearAskPendingResetsCount exercises the second unexported
// helper the brief names: clearAskPending empties the pending set built up
// by prior calls.
func TestAskUser_ClearAskPendingResetsCount(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsValid()))
	if res.IsError {
		t.Fatalf("valid ask_user call errored: %s", res.Output)
	}
	if got := sess.askPendingCount(); got == 0 {
		t.Fatal("askPendingCount = 0 after a valid call, want > 0 (test setup broken)")
	}

	sess.clearAskPending()
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount after clearAskPending = %d, want 0", got)
	}
}

// --- Task 4: the boundary rule (spec §5.1) ---
//
// These tests drive full ProcessInput round trips through a scripted adapter,
// modeled on TestCommunicate_ResultExitsLoop (session_communicate_test.go): a
// round that posts questions ends the turn in SessionAwaiting, communicate
// composes with an ask rather than colliding, and Stop hooks are bypassed at
// an ask-ending boundary.

// TestAskUser_BoundaryEndsTurnAwaiting covers the core boundary rule: a round
// whose only action is ask_user ends the turn — no further model round — with
// the session resting in SessionAwaiting, and the ack recorded in history.
func TestAskUser_BoundaryEndsTurnAwaiting(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(ask)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call after an ask-ending boundary")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := sess.ProcessInput(ctx, "which db should we use?", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1 (turn must end at the ask boundary)", got)
	}
	res, ok := findToolResultInHistory(sess.history, "ask1")
	if !ok {
		t.Fatal("ack for the ask_user call not found in history")
	}
	if res.Content != askUserAckText {
		t.Fatalf("ack content = %q, want %q", res.Content, askUserAckText)
	}
}

// TestAskUser_EarlyAskStillEndsTurn scripts a second round's worth of work
// (a read_file call) that the model would run next if the loop continued,
// proving the boundary ends the turn even when there is clearly more scripted
// work queued up — not merely that the script ran out. The trap names what it
// would have called rather than actually returning that response: doing so
// would (if the boundary fix were absent) cascade into an unrelated
// bare-text-retry exhaustion once round 3 finds no script, obscuring the
// real failure this test targets.
func TestAskUser_EarlyAskStillEndsTurn(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(ask)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call: the model had a read_file call queued for round 2, but an early ask must still end the turn at its own round's boundary")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := sess.ProcessInput(ctx, "which db should we use?", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1 (an early ask still ends the turn at its round's boundary)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_MultipleAsksOneRound covers two separate ask_user calls sharing
// one round: one boundary, both acks recorded, pending grown by both.
func TestAskUser_MultipleAsksOneRound(t *testing.T) {
	t.Parallel()
	ask1 := askUserCall("ask1", askUserArgsValid())
	ask2 := askUserCall("ask2", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(ask1, ask2)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call after an ask-ending boundary")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := sess.ProcessInput(ctx, "pick a db and a name", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1 (one boundary for the whole round)", got)
	}
	if got := sess.askPendingCount(); got != 2 {
		t.Fatalf("askPendingCount = %d, want 2 (one question from each call)", got)
	}
	if _, ok := findToolResultInHistory(sess.history, "ask1"); !ok {
		t.Fatal("ack for ask1 not found in history")
	}
	if _, ok := findToolResultInHistory(sess.history, "ask2"); !ok {
		t.Fatal("ack for ask2 not found in history")
	}
}

// TestAskUser_ComposesWithCommunicate covers spec §5.1's "composes, never
// collides": a round pairing ask_user with a terminal communicate delivers
// the communicate message (the same delivery assertion the communicate tests
// use) but rests awaiting, not idle — the ask overrides the boundary state.
func TestAskUser_ComposesWithCommunicate(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	comm := communicateCall("c1", "Posting a question before I continue.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(ask, comm)
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if strings.TrimSpace(out) != "Posting a question before I continue." {
		t.Fatalf("ProcessInput returned %q, want the communicate message delivered", out)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q (the ask overrides communicate's idle boundary)", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// TestAskUser_StopHookCannotBlockAskBoundary covers spec §5.1/§5.5: a
// Blocked-returning Stop hook must not force another round at an ask
// boundary. The hook also touches a marker file so the test can confirm the
// hook was never even consulted (spec: "not consulted"), not merely ignored.
func TestAskUser_StopHookCannotBlockAskBoundary(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "stop-hook-ran")
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(ask)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call: a Blocked Stop hook must not force another round at an ask boundary")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookStop, plugin.RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: "touch " + marker + "; printf '%s' '{\"decision\":\"block\",\"reason\":\"answer your own question first\"}'",
		Timeout: 5,
	})
	sess.hookRunner = runner

	// TRIPWIRE: the adapter is scripted in-process, but the Stop hook is a
	// real `touch`/`printf` subprocess run through exec.CommandContext that
	// writes a real file the test os.Stat's below; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := sess.ProcessInput(ctx, "which db should we use?", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q — a Blocked Stop hook must not prevent the ask boundary", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("Stop hook ran at an ask-ending boundary; spec §5.1 says it must not be consulted")
	}
}

// TestAskUser_InterruptedTurnEndsIdle covers spec §5.1's interrupt carve-out:
// canceling the ProcessInput context mid-round, after ask_user has already
// posted, ends the turn idle (existing interrupt semantics; the user is
// demonstrably present) and clears the pending set. A second tool call
// ("trigger_cancel"), scheduled right after ask_user in the same round,
// cancels the context from inside the round loop's own goroutine so the
// cancellation is deterministic rather than racing a background timer.
func TestAskUser_InterruptedTurnEndsIdle(t *testing.T) {
	t.Parallel()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	ask := askUserCall("ask1", askUserArgsValid())
	triggerCancel := llm.ToolCallData{ID: "cancel1", Name: "trigger_cancel", Arguments: json.RawMessage(`{}`), Type: "function"}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(ask, triggerCancel)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call: an interrupted turn must not continue")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))
	sess.RegisterTool("trigger_cancel", "cancels the test's ProcessInput context mid-round, after ask_user has already posted",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			cancel()
			return "canceling", nil
		})

	_, err := sess.ProcessInput(ctx, "which db should we use?", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessInput err = %v, want context.Canceled (interrupt semantics)", err)
	}

	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after interrupt = %q, want %q", got, SessionIdle)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount after interrupt = %d, want 0 (cleared on interrupt)", got)
	}
}

// TestAskUser_DeniedOrInvalidOnlyAskDoesNotEndTurn covers the invalid-input
// case: a round whose only ask_user call is rejected (duplicate labels) posts
// nothing, so it must not end the turn — the loop continues to round 2.
func TestAskUser_DeniedOrInvalidOnlyAskDoesNotEndTurn(t *testing.T) {
	t.Parallel()
	badAsk := askUserCall("ask1", askUserArgsDuplicateLabels())
	comm := communicateCall("c1", "Proceeding without the clarifying question.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(badAsk)
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2 (an invalid ask must not end the turn)", got)
	}
	if strings.TrimSpace(out) != "Proceeding without the clarifying question." {
		t.Fatalf("ProcessInput returned %q, want the round-2 communicate message", out)
	}
	// Inbox semantics (attention-status-model v5): round 2's communicate is a
	// clean, output-producing completion with nothing else in flight, so it
	// re-arms awaiting on its own — unrelated to the invalid ask, which posted
	// nothing (askPendingCount below is the discriminator that proves that).
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (the invalid call posts nothing)", got)
	}
}

// TestAskUser_PreToolUseDenyPostsNothing covers spec §5.5: a PreToolUse hook
// that denies ask_user records the deny result but posts no question, so the
// round does not end awaiting — the loop continues to round 2 as normal.
func TestAskUser_PreToolUseDenyPostsNothing(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	comm := communicateCall("c1", "Proceeding without the question.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(ask)
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
		},
	}
	sess := newSession(t, withAdapter(f))
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		// Matcher scoped to ask_user's Claude-visible name (toolname.EvenerToClaude)
		// so the deny does not also swallow round 2's communicate call.
		Matcher: "AskUserQuestion",
		Type:    "command",
		Command: `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no interactive user right now"}}'`,
		Timeout: 5,
	})
	sess.hookRunner = runner

	// TRIPWIRE: the adapter is scripted in-process, but the PreToolUse hook
	// is a real `printf` subprocess run through exec.CommandContext; only
	// fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "which db should we use?", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	res, ok := findToolResultInHistory(sess.history, "ask1")
	if !ok {
		t.Fatal("denied ask_user call result not found in history")
	}
	if !res.IsError || res.Content != "no interactive user right now" {
		t.Fatalf("denied result = %+v, want IsError=true Content=%q", res, "no interactive user right now")
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (denied call must post nothing)", got)
	}
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2 (a denied ask must not end the turn early)", got)
	}
	if strings.TrimSpace(out) != "Proceeding without the question." {
		t.Fatalf("ProcessInput returned %q, want the round-2 communicate message", out)
	}
	// Inbox semantics (attention-status-model v5): round 2's communicate is a
	// clean, output-producing completion with nothing else in flight, so it
	// re-arms awaiting on its own. What this test actually guards — that a
	// deny alone does not end the turn early, at the ask boundary — is proven
	// above by askPendingCount==0 and requests==2 (round 2 ran at all).
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q (a deny alone must not end the turn AT THE ASK BOUNDARY; round 2's own communicate legitimately re-arms awaiting)", got, SessionAwaiting)
	}
}

// --- Task 5: awaiting holds — the entry gate + the drain-ladder gate (spec §5.3) ---
//
// These tests drive full ProcessInput/ProcessInputKind round trips through a
// scripted adapter, modeled on TestCommunicate_ResultExitsLoop and the Task 4
// boundary tests above. Round 4's bug (per the brief) was processOneInput
// flipping to SessionProcessing unconditionally before dispatch: a delegate or
// notification finishing while the user reads the question would silently turn
// the needs-you signal off. The entry gate and drain-ladder gate below sit
// strictly before that transition.

// TestAskUser_EntryGateRefusesNotificationWake covers spec §5.3's entry gate: an
// autonomous EntryNotification wake, while the session sits in SessionAwaiting,
// is refused at the ProcessInputKind boundary before any state transition —
// state is asserted SessionAwaiting both before and after (round 4's blocker
// was exactly this state getting clobbered), no model request is made, and the
// notification stays durably queued (not drained) until a real user reply
// resolves the ask, at which point it drains as its own follow-on turn.
func TestAskUser_EntryGateRefusesNotificationWake(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return finalResponse("thanks, going with Postgres") },
			func(req llm.Request) llm.Response { return finalResponse("notification ack") },
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state before entry-gate probe = %q, want %q", got, SessionAwaiting)
	}

	sess.enqueueJobNotification(watchNotification("job_wake", "output_match: done"))

	out, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) while awaiting returned an error: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("ProcessInputKind(EntryNotification) while awaiting returned %q, want empty (no-op)", out)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after refused notification wake = %q, want %q (must not flip)", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests after refused notification wake = %d, want 1 (no model call)", got)
	}
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications after refused wake = %d, want 1 (notification stays queued, not drained)", got)
	}

	if _, err := sess.ProcessInput(ctx, "let's go with Postgres", nil); err != nil {
		t.Fatalf("reply ProcessInput: %v", err)
	}
	// Inbox semantics (attention-status-model v5): the drained notification
	// turn runs last within this ProcessInput call and is itself a clean,
	// output-producing completion ("notification ack") with nothing else in
	// flight, so it re-arms awaiting on its own. What this test guards — the
	// notification actually drained instead of staying stuck — is proven by
	// requests==3 and peekNotifications==0 below, not by the raw rest state.
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after reply = %q, want %q (the drained notification turn's own clean completion re-arms awaiting)", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 3 {
		t.Fatalf("requests after reply = %d, want 3 (reply turn + the drained notification turn)", got)
	}
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("peekNotifications after drain = %d, want 0", got)
	}
}

func TestAskUser_ReplyDrainsRootDelegateAttentionRefusedAtEntryGate(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return finalResponse("thanks, going with Postgres") },
			func(req llm.Request) llm.Response { return finalResponse("delegate completion ack") },
		},
	}
	sess := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true}),
		withAdapter(f),
	)
	wakes := make(chan struct{}, 2)
	sess.SetNotifyFunc(func() { wakes <- struct{}{} })

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	const (
		firstAttentionID = "delegate:dlg_during_ask/delivery/1"
		firstContent     = `<delegate-notification delegate_id="dlg_during_ask">review complete</delegate-notification>`
	)
	if appended, err := sess.appendDelegateNotificationDurably(firstAttentionID, firstContent); err != nil || !appended {
		t.Fatalf("append root attention = appended:%t err:%v", appended, err)
	}
	if err := sess.armDelegateAttention(firstAttentionID); err != nil {
		t.Fatalf("arm root attention: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("root attention emitted no initial wake")
	}

	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) while awaiting: %v", err)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests after refused wake = %d, want 1", got)
	}
	if !sess.hasPendingRootDelegateAttention() {
		t.Fatal("refused wake discarded pending root delegate attention")
	}

	if _, err := sess.ProcessInput(ctx, "let's go with Postgres", nil); err != nil {
		t.Fatalf("reply ProcessInput: %v", err)
	}
	requests := f.Requests()
	if got := len(requests); got != 3 {
		t.Fatalf("requests after reply = %d, want 3 (reply turn + root attention turn)", got)
	}
	if !requestContainsText(requests[2], firstContent) {
		t.Error("root attention turn did not deliver the delegate notification to the model")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, sess.ID()), sess.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[firstAttentionID]; got != delegateAttentionConsumed {
		t.Errorf("root attention disposition = %q, want %q", got, delegateAttentionConsumed)
	}
	if sess.hasPendingRootDelegateAttention() {
		t.Error("root delegate attention remains pending after the reply drain")
	}

	const (
		secondAttentionID = "delegate:dlg_after_reply/delivery/1"
		secondContent     = `<delegate-notification delegate_id="dlg_after_reply">second review complete</delegate-notification>`
	)
	if appended, err := sess.appendDelegateNotificationDurably(secondAttentionID, secondContent); err != nil || !appended {
		t.Fatalf("append later root attention = appended:%t err:%v", appended, err)
	}
	if err := sess.armDelegateAttention(secondAttentionID); err != nil {
		t.Fatalf("arm later root attention: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Error("later root attention emitted no fresh wake")
	}
}

// TestAskUser_EntryGateRefusesContinuationWake covers spec §5.3's entry gate for
// EntryContinuation: refused before any state transition, state unchanged
// throughout, no model request made. (The goal engine's own arm-vs-kick
// machinery — whether a continuation is even offered while awaiting — is
// Task 6; this test only proves the generic entry gate refuses the entry kind
// regardless of what would have produced it.)
func TestAskUser_EntryGateRefusesContinuationWake(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a model call: the entry gate must refuse EntryContinuation while awaiting")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state before entry-gate probe = %q, want %q", got, SessionAwaiting)
	}

	out, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation)
	if err != nil {
		t.Fatalf("ProcessInputKind(EntryContinuation) while awaiting returned an error: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("ProcessInputKind(EntryContinuation) while awaiting returned %q, want empty (no-op)", out)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after refused continuation wake = %q, want %q (must not flip)", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests after refused continuation wake = %d, want 1 (no model call)", got)
	}
}

// TestAskUser_BoundaryDrainHoldsNotifications covers spec §5.3's drain-ladder
// gate: a job notification arriving DURING the asking turn (not via a separate
// entry-gate probe, but genuinely enqueued mid-round by a tool call sharing the
// round with ask_user, deterministically — the same idiom
// TestAskUser_InterruptedTurnEndsIdle uses for its trigger_cancel tool) must not
// drive a notification turn at the ask boundary. It drains only after a real
// reply, at that reply turn's own drain tail.
func TestAskUser_BoundaryDrainHoldsNotifications(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	enqueueNotif := llm.ToolCallData{ID: "notif1", Name: "enqueue_test_notification", Arguments: json.RawMessage(`{}`), Type: "function"}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, enqueueNotif) },
			func(req llm.Request) llm.Response { return finalResponse("thanks, going with Postgres") },
			func(req llm.Request) llm.Response { return finalResponse("notification ack") },
		},
	}
	sess := newSession(t, withAdapter(f))
	sess.RegisterTool("enqueue_test_notification",
		"test-only: enqueues a job notification mid-round, simulating a job finishing while the model is asking a question",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			sess.enqueueJobNotification(watchNotification("job_during_ask", "output_match: done"))
			return "queued", nil
		})

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1 (a notification arriving mid-ask must not drive a turn at the boundary)", got)
	}
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications = %d, want 1 (held, not dropped)", got)
	}

	if _, err := sess.ProcessInput(ctx, "let's go with Postgres", nil); err != nil {
		t.Fatalf("reply ProcessInput: %v", err)
	}
	// Inbox semantics (attention-status-model v5): the drained notification
	// turn runs last within this ProcessInput call and is itself a clean,
	// output-producing completion ("notification ack") with nothing else in
	// flight, so it re-arms awaiting on its own. What this test guards — the
	// notification held during the ask actually drained afterward instead of
	// being dropped — is proven by requests==3 and peekNotifications==0
	// below, not by the raw rest state.
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after reply = %q, want %q (the drained notification turn's own clean completion re-arms awaiting)", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 3 {
		t.Fatalf("requests after reply = %d, want 3 (reply turn + the drained notification turn)", got)
	}
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("peekNotifications after drain = %d, want 0", got)
	}
}

// TestAskUser_QueuedInputDrainsAsReply covers spec §5.3's queued-input rung,
// which stays live by design: a message the user queues mid-round (via the
// same deterministic same-round-tool idiom as the notification test above)
// drains as the very next turn once the ask ends the round — it IS the reply,
// so the pending set clears (askPendingCount below). It does NOT prove the
// session stays idle: under attention-status-model v5's inbox semantics, the
// drained reply's own turn is itself a clean, output-producing completion
// with nothing else in flight, so it independently re-arms awaiting — the
// discriminator that the ASK resolved (rather than the reply never landing)
// is askPendingCount==0 and the ack present in history, not the raw state.
func TestAskUser_QueuedInputDrainsAsReply(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	enqueueFollowUpText := llm.ToolCallData{ID: "enqueue1", Name: "enqueue_test_queued_input", Arguments: json.RawMessage(`{}`), Type: "function"}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, enqueueFollowUpText) },
			func(req llm.Request) llm.Response { return finalResponse("sure, running the linter too") },
		},
	}
	sess := newSession(t, withAdapter(f))
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess.RegisterTool("enqueue_test_queued_input",
		"test-only: enqueues a user message mid-round, simulating the user typing ahead while the model is asking a question",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			if err := sess.Enqueue(ctx, "also run the linter"); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			return "queued", nil
		})

	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q (the drained queued text's own clean completion re-arms awaiting; see askPendingCount below for the ask-resolved discriminator)", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2 (the asking round + the queued text drained as the reply)", got)
	}
	if !requestsContain(f.Requests(), "also run the linter") {
		t.Fatal("the queued text never reached the model as the next user turn")
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (the queued reply resolves the pending set)", got)
	}
	if res, ok := findToolResultInHistory(sess.history, "ask1"); !ok || res.Content != askUserAckText {
		t.Fatalf("ack for ask1 missing or wrong: %+v ok=%v", res, ok)
	}
}

// TestAskUser_BoundaryDrainPreservesFollowUp exercises the follow-up
// preservation concern directly: Session.FollowUp is exported, thread-safe
// public API with zero production callers today (grep across every module
// finds only two direct test callers) — so nothing at the type level prevents
// some future caller from queueing one while an asking round is in flight. A
// same-round test tool simulates that race. The drain-ladder gate must HOLD the
// follow-up rung while awaiting WITHOUT popping it and discarding the result:
// the message must survive intact in the follow-up queue, not be lost, so it
// still runs once the session leaves awaiting.
func TestAskUser_BoundaryDrainPreservesFollowUp(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	queueFollowUp := llm.ToolCallData{ID: "fu1", Name: "enqueue_test_followup_direct", Arguments: json.RawMessage(`{}`), Type: "function"}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, queueFollowUp) },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call: an asking boundary must hold the follow-up rung")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))
	sess.RegisterTool("enqueue_test_followup_direct",
		"test-only: calls Session.FollowUp mid-round, simulating a concurrent caller racing the asking round",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			if err := sess.FollowUp("investigate the flaky test next"); err != nil {
				t.Fatal(err)
			}
			return "queued", nil
		})

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1 (a follow-up queued mid-ask must not run before the reply)", got)
	}
	sess.mu.Lock()
	pending := len(sess.followups)
	sess.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending follow-ups after the awaiting rest = %d, want 1 (held, not dropped)", pending)
	}
}

// --- Task 7: Compact refused / Clear dismisses, while awaiting (spec §5.3) ---
//
// "Compact and Clear are live at rest and touch the transcript the pending
// question lives in: v1 refuses Compact while awaiting with an instructive
// error ... and allows Clear, which dismisses the question along with the
// history and rests idle." Compact's guard lives in the rest-time entry,
// agent/session_compaction.go's Compact — the one cmd/evener/serve.go's
// SetCompactFunc calls. Clear has no in-place Session method at all: /clear
// (serve.go SetClearFunc) constructs a brand-new agent.NewSession with
// SessionStartKind=Clear and swaps it in for the live session, Closing the
// old one afterward — so "Clear while awaiting" is a fresh, always-idle,
// always-empty-pending session by construction, not a state transition on the
// awaiting session itself. The tests below cover Compact's new guard directly
// and reproduce Clear's actual replace-not-reset mechanism at the session
// level; real end-to-end proof that the daemon's thread/clear surface reaches this
// path belongs to a later, serve-level task.

// compactErrPending is the exact instructive error text spec §5.3 requires
// ("a question is pending; reply or clear first"), named once so the
// production string and the test assertion can't silently drift apart.
const compactErrPending = "a question is pending; reply or clear first"

// TestAskUser_CompactRefusedWhileAwaiting covers the guard itself: Compact on
// an awaiting session returns the instructive error, before touching history —
// asserted by comparing a full pre-call snapshot against the post-call
// history, not just a length check — and the session stays SessionAwaiting.
func TestAskUser_CompactRefusedWhileAwaiting(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state before Compact = %q, want %q (test setup broken)", got, SessionAwaiting)
	}

	before := currentHistory(t, sess)
	err := sess.Compact(context.Background())
	if err == nil || !strings.Contains(err.Error(), compactErrPending) {
		t.Fatalf("Compact while awaiting err = %v, want an error containing %q", err, compactErrPending)
	}
	after := currentHistory(t, sess)

	if len(after) != len(before) {
		t.Fatalf("history length after refused Compact = %d, want %d (unchanged)", len(after), len(before))
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("history mutated by a refused Compact:\nbefore=%+v\nafter=%+v", before, after)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after refused Compact = %q, want %q (must not flip)", got, SessionAwaiting)
	}
}

// TestAskUser_CompactSucceedsWhenIdle is the positive control the brief asks
// for: a session that never asked anything compacts exactly as before — the
// new awaiting guard must not fire, or otherwise interfere, on a session
// that is not awaiting.
func TestAskUser_CompactSucceedsWhenIdle(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want %q (test setup broken)", got, SessionIdle)
	}
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact on an idle session: %v, want nil (awaiting guard must not fire)", err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after Compact = %q, want %q", got, SessionIdle)
	}
}

// --- Post-merge fixup: Compact must not over-hold on a plain awaiting rest ---
//
// attention-status-model v5's general inbox semantics (merged post-write-up)
// changed what SessionAwaiting means: it is no longer produced only by a
// pending ask_user question. A plain, output-producing turn with nothing else
// in flight now also rests SessionAwaiting, with an EMPTY pending-ask set.
// Compact's guard above still read raw state (s.State() == SessionAwaiting)
// rather than askPendingCount(), so it refused Compact on ANY rested session
// even when nothing is pending — a regression this test pins.

// TestAskUser_CompactProceedsOnPlainAwaitingRestNoPendingAsk covers the
// corrected guard: a session resting SessionAwaiting purely from a clean
// completion (no question posted, askPendingCount()==0) must not trip the
// pending-ask error — Compact may still fail or succeed for other reasons,
// but not this one.
func TestAskUser_CompactProceedsOnPlainAwaitingRestNoPendingAsk(t *testing.T) {
	t.Parallel()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("here is my answer") },
		},
	}
	sess := newSession(t, withAdapter(f))

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after a plain completion = %q, want %q (test setup broken)", got, SessionAwaiting)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount after a plain completion = %d, want 0 (test setup broken)", got)
	}

	if err := sess.Compact(context.Background()); err != nil && strings.Contains(err.Error(), compactErrPending) {
		t.Fatalf("Compact on a plain awaiting rest with nothing pending returned the pending-ask error: %v (the hold is for a genuine pending question, not this general rest)", err)
	}
}

// TestAskUser_ClearReplacesAwaitingSessionWithFreshIdleOne reproduces spec
// §5.3's Clear requirement at the session level, matching the ACTUAL
// production mechanism (cmd/evener/serve.go SetClearFunc) rather than inventing
// an in-place reset that does not exist: an old session is driven into
// SessionAwaiting with a real pending question, then a replacement is built
// exactly as SetClearFunc builds one — same client/profile/env, cfg with only
// SessionStartKind flipped to Clear, constructed WHILE the old session is
// still open (serve.go closes the old session only after the swap, so this
// test preserves that ordering). The replacement is what the user talks to
// after thread/clear; it is idle with an empty pending set regardless of what the
// old session was doing, and the old session itself is untouched (a swap, not
// a mutation) until its own Close.
func TestAskUser_ClearReplacesAwaitingSessionWithFreshIdleOne(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	}
	client := llm.NewClient()
	client.Register(f)
	profile := NewOpenAIProfile("gpt-5.2")
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	cfg := SessionConfig{}

	oldSess, err := NewSession(client, profile, env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { oldSess.Close() })

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := oldSess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := oldSess.State(); got != SessionAwaiting {
		t.Fatalf("old session state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}
	if got := oldSess.askPendingCount(); got == 0 {
		t.Fatal("old session askPendingCount = 0, want > 0 (test setup broken)")
	}

	// Mirrors serve.go's SetClearFunc: clearCfg := sessionCfg; clearCfg.SessionStartKind = plugin.SessionStartKindClear.
	clearCfg := cfg
	clearCfg.SessionStartKind = plugin.SessionStartKindClear
	newSess, err := NewSession(client, profile, env, clearCfg)
	if err != nil {
		t.Fatalf("NewSession (clear replacement): %v", err)
	}
	t.Cleanup(func() { newSess.Close() })

	if got := newSess.State(); got != SessionIdle {
		t.Fatalf("cleared-replacement state = %q, want %q", got, SessionIdle)
	}
	if got := newSess.askPendingCount(); got != 0 {
		t.Fatalf("cleared-replacement askPendingCount = %d, want 0", got)
	}

	// The old session is a swap victim, not a mutation target: it still
	// carries its awaiting state and pending question until serve.go's
	// subsequent oldSess.Close() (t.Cleanup here) — clear does not reach in
	// and reset it.
	if got := oldSess.State(); got != SessionAwaiting {
		t.Fatalf("old session state after replacement built = %q, want %q (clear must not mutate the old session)", got, SessionAwaiting)
	}
	if got := oldSess.askPendingCount(); got == 0 {
		t.Fatal("old session askPendingCount after replacement built = 0, want > 0 (clear must not mutate the old session)")
	}
}

// --- Task 8: restore re-derivation (spec §5.4, §6) ---
//
// A restarted daemon has no live Session to ask; RestoreSessionFromMetaWithConfig
// must re-derive SessionAwaiting from the restored transcript's tail instead
// of the constructor's blanket idle default. These tests drive a REAL ask
// through the same scripted-adapter harness as the boundary tests above,
// close the session (flushing the transcript to disk), and restore from the
// persisted meta + transcript file — the actual production path — except for
// the interrupted-ask variant, which by definition has no live round to drive
// (the crash happens before the ack is ever recorded) and so writes its
// transcript tail directly.

// newAskRestoreClient builds a fresh client for a restore call, deliberately
// decoupled from the original session's scripted adapter. Restoring issues no
// model call on its own (construction alone never calls Complete); a separate
// client just keeps that assumption from becoming load-bearing by accident.
func newAskRestoreClient() *llm.Client {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	return c
}

// TestAskUser_RestoreRederivesAwaiting covers spec §5.4/§6's core restore
// contract: a session driven to SessionAwaiting by a real, completed ask_user
// call, closed and restored from its own persisted transcript, comes back
// awaiting rather than RestoreSessionFromMetaWithConfig's old hardcoded idle.
func TestAskUser_RestoreRederivesAwaiting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("pre-restore state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_RestoreRederivesAwaitingAcrossTrailingSteering covers spec §6's
// carve-out: a steering turn trailing the ask's ack (e.g. a task-nudge
// reminder injected before the round-boundary check runs — injectPostToolSteering
// running before deliverIfCommunicated) does not resolve the question; the
// tail scan must see past it to the ack underneath. Built directly via
// appendTurn rather than through the live drained-steer/hook path: the scan
// only inspects transcript shape, so a hand-appended TurnSteering turn
// exercises exactly the shape the live machinery would have produced, without
// coupling this test to which mechanism produces it.
func TestAskUser_RestoreRederivesAwaitingAcrossTrailingSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("pre-restore state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}
	sess.appendTurn(schema.TurnSteering, llm.User("reminder: the user has not answered yet"))

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (a trailing steering turn must not resolve the pending ask)", got, SessionAwaiting)
	}
}

// TestAskUser_SteeringCarrierClaimAnswersAskFailsClosedForUnknownProvenance
// covers RoboRev #1905 round 1's Low (session_tools_ask.go:117):
// steeringCarrierClaimAnswersAsk read a MISSING client-mutation journal
// record the same as a present-but-kindless one and answered true either
// way, so a carrier claim whose journal entry was lost (or not yet visible)
// would fail open and wrongly clear a still-unanswered ask. A carrier
// identity's journal record must exist by construction (SetHumanNote/
// AcceptClientMutationSteer write it before the entry is ever queued), so an
// unknown id here is exactly the case with no evidence the steer answers
// anything -- it must fail closed.
func TestAskUser_SteeringCarrierClaimAnswersAskFailsClosedForUnknownProvenance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	identity := queuedClientMutationIdentity{ClientMutationID: "never-written", SteeringCarrier: true}
	if got := sess.steeringCarrierClaimAnswersAsk(identity); got {
		t.Fatal("steeringCarrierClaimAnswersAsk = true for a carrier identity with no journal record, want false (fail closed)")
	}
}

// TestAskUser_AcceptSteeringCarrierInputTagsSteeringCarrierOnAppendFailureForAnAnsweringSteer
// covers RoboRev #1905 round 1's Medium (session_ask_test.go:1464 in the
// panel's numbering): neither TurnFailure tagging shape had a behavioral
// test exercising the real drain path. This one drives an ANSWERING steer's
// carrier through the real ProcessPendingUserInput/processOneInput entry
// point (so the entry clear itself runs, exactly as
// TestAskUser_HumanNoteDuringPendingAskDoesNotResolveIt drives the human-note
// sibling) with its transcript append forced to fail
// (acceptSteeringCarrierInput's carrierSteerUndelivered case), and asserts
// the persisted TurnFailure carries SteeringCarrier=true alongside the live
// askPendingCount the entry clear already resolved.
func TestAskUser_AcceptSteeringCarrierInputTagsSteeringCarrierOnAppendFailureForAnAnsweringSteer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "carrier-tag-1",
		Input:            clientMutationInput("please hold", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	refusal := refuseSteerAppends(sess, "carrier-tag-1")
	refusal.refuse.Store(true)
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err == nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want the injected append failure", ran, err)
	}
	if got := refusal.refusals.Load(); got != 1 {
		t.Fatalf("steer append attempts = %d, want 1", got)
	}

	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after an answering carrier's failed append = %d, want 0 (the entry clear already resolved it)", got)
	}
	last := sess.history[len(sess.history)-1]
	if last.Kind != schema.TurnFailure || last.Error == nil || !last.Error.SteeringCarrier {
		t.Fatalf("last turn = %+v, want a TurnFailure tagged SteeringCarrier", last)
	}
}

// TestAskUser_RecordFailedSteeringSelectionTagsSteeringCarrierForAnAnsweringSteer
// is the append-failure test's selection-failure sibling: an ANSWERING
// steer's carrier claim whose own skill selection fails to prepare
// (recordFailedSteeringSelection) must also persist SteeringCarrier=true.
// Driven through the real ProcessPendingUserInput entry point, like the
// append-failure test above, so the entry clear itself runs.
func TestAskUser_RecordFailedSteeringSelectionTagsSteeringCarrierForAnAnsweringSteer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "carrier-skill-tag-1",
		Input:            clientMutationInput("please hold", nil, []string{"no-such-skill"}),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	// errSteeringCarrierStoodDown is swallowed by processOneInput's own
	// dispatch (session_lifecycle.go:1944) -- a graceful stand-down, not a
	// visible failure -- so only ran is asserted here.
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); !ran || err != nil {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want ran=true err=nil", ran, err)
	}

	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after an answering carrier's failed skill selection = %d, want 0 (the entry clear already resolved it)", got)
	}
	last := sess.history[len(sess.history)-1]
	if last.Kind != schema.TurnFailure || last.Error == nil || !last.Error.SteeringCarrier {
		t.Fatalf("last turn = %+v, want a TurnFailure tagged SteeringCarrier", last)
	}
}

// TestAskUser_AcceptSteeringCarrierInputDoesNotTagSteeringCarrierOnAppendFailureForAHumanNote
// is the append-failure test's human-note sibling: a human-note carrier's
// entry clear is skipped (steeringCarrierClaimAnswersAsk), so its own
// append failure must persist SteeringCarrier=false and leave the live
// pending ask untouched.
func TestAskUser_AcceptSteeringCarrierInputDoesNotTagSteeringCarrierOnAppendFailureForAHumanNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-tag-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued note")
	}
	refusal := refuseSteerAppends(sess, "note-tag-1")
	refusal.refuse.Store(true)
	if err := sess.acceptSteeringCarrierInput(ctx, queuedClientMutationIdentity{ClientMutationID: "note-tag-1", StableTurnID: turnID, SteeringCarrier: true}); err == nil {
		t.Fatal("acceptSteeringCarrierInput succeeded, want the injected append failure")
	}

	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after a human-note carrier's failed append = %d, want 1 (a note update never answers the ask)", got)
	}
	last := sess.history[len(sess.history)-1]
	if last.Kind != schema.TurnFailure || last.Error == nil || last.Error.SteeringCarrier {
		t.Fatalf("last turn = %+v, want a TurnFailure NOT tagged SteeringCarrier", last)
	}
}

// TestAskUser_RecordFailedSteeringSelectionDoesNotTagSteeringCarrierForAHumanNoteClaim
// is the selection-failure test's human-note sibling: SetHumanNote's own RPC
// never attaches a skill selection (addPendingSteering posts only a text
// InputItem), so this drives recordFailedSteeringSelection directly with a
// synthetic human-note-kinded message over a REAL journal record (SetHumanNote
// is still the one path that stamps SteeringKindHumanNote) to exercise the
// shared predicate itself, matching TestAskUser_RecordFailedSteeringSelectionDoesNotTagAHumanNoteClaim's
// setup but asserting the persisted tag and live count directly instead of
// through a restore.
func TestAskUser_RecordFailedSteeringSelectionDoesNotTagSteeringCarrierForAHumanNoteClaim(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-skill-tag-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	sess.setSteeringCarrierClaimDrain("note-skill-tag-1")
	msg := steeringMessage{
		ClientMutationID: "note-skill-tag-1",
		Kind:             events.SteeringKindHumanNote,
		Text:             "human updated their whiteboard: watch the ingest path",
	}
	if !sess.recordFailedSteeringSelection(msg, errors.New(`skill "no-such-skill" not found`)) {
		t.Fatal("recordFailedSteeringSelection reported the record itself failed to append")
	}
	sess.setSteeringCarrierClaimDrain("")

	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after the tagged failure = %d, want 1 (unaffected either way; the tag governs restore)", got)
	}
	last := sess.history[len(sess.history)-1]
	if last.Kind != schema.TurnFailure || last.Error == nil || last.Error.SteeringCarrier {
		t.Fatalf("last turn = %+v, want a TurnFailure NOT tagged SteeringCarrier", last)
	}
}

// streamAskUserThenFinish scripts a round that posts one ask_user call and
// ends its turn on it — the streaming-adapter shape of toolCallResponse(ask),
// for a scriptedStreamAdapter script map.
func streamAskUserThenFinish(ask llm.ToolCallData) func(*llm.ChanStream) {
	return func(st *llm.ChanStream) {
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: ask.ID, Name: ask.Name, Type: "function"}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: ask.ID, Arguments: ask.Arguments}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &ask})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}
}

// TestAskUser_RestoreResolvesAcrossInterrupt covers the interrupt half of the
// boundary turnResolvesAskBoundary enforces on the server
// (agent/session_lifecycle.go: the interrupt path calls clearAskPending
// directly and appends a steering turn carrying SteeringKindInterrupted). The
// restore-side derivation must reach the identical answer the live path
// already produced, or a restored session reports a stale askPending on the
// wire that no client-side logic corrects — the client trusts the wire's
// flag rather than re-deriving this boundary itself. Driven through two real
// ProcessInput rounds on a scripted STREAMING adapter (a plain fakeAdapter.Complete never
// returns an error, so it cannot produce a genuine cancellation): round one
// posts and acks ask_user; round two's own model call is cancelled
// mid-stream, the same way TestSettlement_InterruptWithNoSalvagePersistsNothing
// drives a real interrupted round.
func TestAskUser_RestoreResolvesAcrossInterrupt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	round := 0
	c := llm.NewClient()
	c.Register(&scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"gpt-5.2": func(st *llm.ChanStream) {
				round++
				if round == 1 {
					streamAskUserThenFinish(ask)(st)
					return
				}
				// Round two: the user's follow-up round is interrupted before
				// the model produces anything — the same shape
				// TestSettlement_InterruptWithNoSalvagePersistsNothing scripts.
				st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "weighing options"})
				cancel()
				st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: context.Canceled})
			},
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	if _, err := sess.ProcessInput(context.Background(), "which db should we use?", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-interrupt pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.ProcessInput(ctx, "actually, hold on", nil); err == nil {
		t.Fatal("second ProcessInput returned nil, want the mid-stream cancellation")
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after the interrupt = %d, want 0", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 0 {
		t.Fatalf("restored askPending = %+v, want empty (an interrupt resolves the pending ask)", restored.askPending)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q (deriveRestoredState and deriveRestoredAskPending must agree on this boundary)", got, SessionIdle)
	}
}

// TestAskUser_RestoreResolvesAcrossInterruptSameRound covers the interrupt
// clause's other half: an interrupt whose transcript tail has no TurnUserInput
// anywhere near it, because the cancellation lands in the SAME round that
// posted and acked ask_user (the trigger_cancel idiom
// TestAskUser_InterruptedTurnEndsIdle uses), not a later round entered by a
// fresh ProcessInput. Without turnResolvesAskBoundary's SteeringKindInterrupted
// clause, the backward scan would skip the interrupt marker as non-decisive
// bookkeeping and land on the turn immediately before it — the TurnToolResults
// carrying ask1's own completed, non-error ack — and re-derive the session as
// awaiting with ask1 still pending: the "interrupted ack-less ask" spec §6
// forbids.
func TestAskUser_RestoreResolvesAcrossInterruptSameRound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	ask := askUserCall("ask1", askUserArgsValid())
	triggerCancel := llm.ToolCallData{ID: "cancel1", Name: "trigger_cancel", Arguments: json.RawMessage(`{}`), Type: "function"}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, triggerCancel) },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call: an interrupted turn must not continue")
				return llm.Response{}
			},
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.RegisterTool("trigger_cancel", "cancels the test's ProcessInput context mid-round, after ask_user has already posted",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			cancel()
			return "canceling", nil
		})

	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessInput err = %v, want context.Canceled (interrupt semantics)", err)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after the interrupt = %d, want 0", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 0 {
		t.Fatalf("restored askPending = %+v, want empty (an interrupt resolves the pending ask even mid-round)", restored.askPending)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q (the interrupt marker must be decisive before the scan ever reaches ask1's own ack)", got, SessionIdle)
	}
}

// TestAskUser_InterruptMarkerWriteFailurePreservesBoundary keeps an existing
// ask_user question pending when an already-canceled input reaches the
// interrupt marker but the marker cannot be recorded. The queued input proves
// the failed marker does not open the autonomous drain, while restore proves
// the live boundary and the transcript-derived boundary agree.
func TestAskUser_InterruptMarkerWriteFailurePreservesBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted provider and local temporary transcript files use real
	// I/O; 30s is a generous hang guard, and this test makes no network requests.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()

	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	modelCalls := 0
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				modelCalls++
				return toolCallResponse(ask)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call after an unrecorded interrupt marker")
				return llm.Response{}
			},
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(parentCtx, "which db should we use?", nil); err != nil {
		t.Fatalf("initial ask ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pending count before already-canceled input = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state before already-canceled input = %q, want %q", got, SessionAwaiting)
	}
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "queued-behind-unrecorded-interrupt",
		Input:            []appwire.InputItem{{Type: "text", Text: "wait for the pending answer"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth before already-canceled input = %d, want 1", got)
	}
	drainPendingEvents(sess)

	// Use the real transcript file and arm the fault only after ask_user's
	// successful tool-results turn has been persisted. A cleanly rolled-back
	// marker write is not a retained record, so the marker must not be announced
	// or adopted.
	fs := attachEnvironmentFailureFS(t, sess)
	markerFaultHit := false
	markerFailure := errors.New("injected interrupt marker write failure")
	fs.mu.Lock()
	fs.failure = markerFailure
	fs.onFailure = func() { markerFaultHit = true }
	fs.mu.Unlock()
	turnCtx, cancel := context.WithCancel(parentCtx)
	cancel()
	processCtx := WithQueuedInputDrainOnInterrupt(turnCtx, parentCtx)

	_, err = sess.ProcessInput(processCtx, "which db should we use?", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessInput err = %v, want context.Canceled", err)
	}
	if !markerFaultHit {
		t.Fatal("the injected filesystem failure did not reach the interrupt marker write")
	}
	if !errors.Is(err, markerFailure) {
		t.Fatalf("ProcessInput err = %v, want the interrupt marker write failure", err)
	}
	if modelCalls != 1 {
		t.Fatalf("model calls = %d, want 1 (failed marker must not drain queued input)", modelCalls)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after failed interrupt marker = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after failed interrupt marker = %q, want %q", got, SessionAwaiting)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("live wire state after failed interrupt marker = %q, want %q", got, SessionAwaiting)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after failed interrupt marker = %d, want 1", got)
	}
	for _, turn := range sessionHistory(sess) {
		if turn.SteeringKind == events.SteeringKindInterrupted {
			t.Fatal("unrecorded interrupt marker appeared in live history")
		}
	}
	var terminalEvents int
	for _, ev := range drainPendingEvents(sess) {
		if ev.Kind == events.EventSessionEnd {
			data, ok := ev.Data.(events.SessionEndData)
			if !ok {
				t.Fatalf("session-end event data = %#v, want SessionEndData", ev.Data)
			}
			terminalEvents++
			if data.Reason == "interrupted" || data.Interrupted {
				t.Fatalf("failed interrupt marker emitted interrupted session end: %+v", data)
			}
			if data.Reason != "turn_failed" || data.State != string(SessionAwaiting) {
				t.Fatalf("failed interrupt marker session end = %+v, want turn_failed/Awaiting", data)
			}
		}
		if ev.Kind != events.EventSteeringInjected {
			continue
		}
		data, ok := ev.Data.(events.SteeringInjectedData)
		if ok && data.Kind == events.SteeringKindInterrupted {
			t.Fatal("unrecorded interrupt marker was announced to live subscribers")
		}
	}
	if terminalEvents != 1 {
		t.Fatalf("failed interrupt marker emitted %d session-end events, want exactly one turn_failed/Awaiting", terminalEvents)
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored pending count = %d, want 1", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q", got, SessionAwaiting)
	}
	if got := restored.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("restored wire state = %q, want %q", got, SessionAwaiting)
	}
	if got := restored.QueueDepth(); got != 1 {
		t.Fatalf("restored queue depth = %d, want 1", got)
	}
	for _, turn := range sessionHistory(restored) {
		if turn.SteeringKind == events.SteeringKindInterrupted {
			t.Fatal("unrecorded interrupt marker appeared after restore")
		}
	}
}

// TestAskUser_FailedInterruptMarkerAfterAnsweredToolRoundMatchesRestore
// covers the marker rejection after a user reply has already cleared the
// in-memory ask set. The admitted reply runs a completed tool-results round,
// then cancellation reaches the marker with a clean rollback failure. Live
// settlement must derive the same awaiting boundary restore reads from that
// durable tool completion, while retaining the resolved ask and rejecting the
// marker.
func TestAskUser_FailedInterruptMarkerAfterAnsweredToolRoundMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	loop := llm.ToolCallData{ID: "loop1", Name: "loop_tool", Arguments: json.RawMessage(`{}`), Type: "function"}
	trigger := llm.ToolCallData{ID: "cancel1", Name: "trigger_cancel", Arguments: json.RawMessage(`{}`), Type: "function"}
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return toolCallResponse(loop, trigger) },
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.RegisterTool("loop_tool", "returns a completed result", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	sess.RegisterTool("trigger_cancel", "lets the post-tool hook cancel", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		return "triggered", nil
	})

	// TRIPWIRE: scripted provider and local temporary transcript files use real
	// I/O; 30s is a generous hang guard, and this test makes no network requests.
	initialCtx, initialCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer initialCancel()
	if _, err := sess.ProcessInput(initialCtx, "which db should we use?", nil); err != nil {
		t.Fatalf("initial ask ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("initial pending count = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("initial state = %q, want %q", got, SessionAwaiting)
	}
	drainPendingEvents(sess)

	markerFailure := errors.New("answered-round interrupt marker sync failure")
	markerFaultHit := false
	// TRIPWIRE: the local scripted reply cancels at the post-tool hook; 30s only guards a hang.
	replyCtx, cancelReply := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelReply()
	replyCtx = context.WithValue(replyCtx, sessionToolRoundHooksKey{}, sessionToolRoundHooks{
		beforeSteering: func() {
			// The successful reply's tool-results turn is durable before this
			// hook arms the fault for the next append and cancels the round.
			fs := attachEnvironmentFailureFS(t, sess)
			fs.mu.Lock()
			fs.failure = markerFailure
			fs.onFailure = func() { markerFaultHit = true }
			fs.mu.Unlock()
			cancelReply()
		},
	})

	_, processErr := sess.ProcessInput(replyCtx, "Postgres, thanks", nil)
	if !errors.Is(processErr, context.Canceled) {
		t.Fatalf("reply ProcessInput error = %v, want context.Canceled", processErr)
	}
	if !markerFaultHit {
		t.Fatal("injected filesystem failure did not reach the interrupt marker")
	}
	if !errors.Is(processErr, markerFailure) {
		t.Fatalf("reply ProcessInput error = %v, want the marker sync failure", processErr)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2 (ask plus admitted reply tool round)", got)
	}
	if result, ok := findToolResultInHistory(sess.history, "loop1"); !ok || result.IsError {
		t.Fatalf("completed reply tool result = %+v, found=%v, want one non-error result", result, ok)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count = %d, want 0 (the admitted reply resolved ask1)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after failed marker = %q, want %q (match the durable completed tool round)", got, SessionAwaiting)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("live wire state after failed marker = %q, want %q", got, SessionAwaiting)
	}
	terminalEvents := 0
	for _, ev := range drainPendingEvents(sess) {
		if ev.Kind != events.EventSessionEnd {
			continue
		}
		data, ok := ev.Data.(events.SessionEndData)
		if !ok {
			t.Fatalf("session-end event data = %#v, want SessionEndData", ev.Data)
		}
		terminalEvents++
		if data.Reason != "turn_failed" || data.State != string(SessionAwaiting) || data.Interrupted {
			t.Fatalf("failed marker session-end = %+v, want turn_failed/Awaiting without interruption", data)
		}
	}
	if terminalEvents != 1 {
		t.Fatalf("failed marker emitted %d session-end events, want exactly one", terminalEvents)
	}
	for _, turn := range sessionHistory(sess) {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindInterrupted {
			t.Fatal("failed interrupt marker appeared in live history")
		}
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored pending count = %d, want 0", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (durable completed tool round)", got, SessionAwaiting)
	}
	if got := restored.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("restored wire state = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_StreamedSalvageBeforeFailedInterruptMarkerPreservesBoundary
// covers a cancellation that has already streamed a partial response. The
// salvage explanation is recorded before the round loop's durable interrupt
// marker; when that marker is rejected, restore must treat the explanation as
// non-resolving and retain the pending ask exactly as the live session does.
func TestAskUser_StreamedSalvageBeforeFailedInterruptMarkerPreservesBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted provider and local temporary transcript files use real
	// I/O; 30s is a generous hang guard, and this test makes no network requests.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()
	turnCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	ask := askUserCall("ask1", askUserArgsValid())
	var fs *environmentSyncFailureFS
	markerFailure := errors.New("injected streamed interrupt marker write failure")
	markerFaultHit := false
	var rounds int
	a := &scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"gpt-5.2": func(st *llm.ChanStream) {
				rounds++
				if rounds == 1 {
					streamAskUserThenFinish(ask)(st)
					return
				}
				fs.mu.Lock()
				// The carrier's durable steer has landed before this model
				// request. Let the cancellation salvage persistence settle,
				// then reject only the round loop's main interrupt marker.
				fs.syncsBeforeFailure = 2
				fs.failure = markerFailure
				fs.onFailure = func() { markerFaultHit = true }
				fs.mu.Unlock()
				st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "partial"})
				st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "partial", Delta: "draft before cancellation"})
				cancel()
				st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: context.Canceled})
			},
		},
	}
	c := llm.NewClient()
	c.Register(a)
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(parentCtx, "which db should we use?", nil); err != nil {
		t.Fatalf("initial ask ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pending count before streamed cancellation = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state before streamed cancellation = %q, want %q", got, SessionAwaiting)
	}
	if _, err := sess.SetHumanNote("note-streamed-cancel", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}

	fs = attachEnvironmentFailureFS(t, sess)

	_, ran, processErr := sess.ProcessPendingUserInput(turnCtx, nil)
	if !ran {
		t.Fatalf("ProcessPendingUserInput ran=%v, want the human-note carrier to run", ran)
	}
	if !errors.Is(processErr, context.Canceled) {
		t.Fatalf("streamed cancellation err = %v, want context.Canceled", processErr)
	}
	if !markerFaultHit {
		t.Fatal("the injected filesystem failure did not reach the main interrupt marker")
	}
	if !errors.Is(processErr, markerFailure) {
		t.Fatalf("streamed cancellation err = %v, want the interrupt marker write failure", processErr)
	}
	if got := len(a.Requests()); got != 2 {
		t.Fatalf("streamed model requests = %d, want exactly 2", got)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after failed main marker = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after failed main marker = %q, want %q", got, SessionAwaiting)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("live wire state after failed main marker = %q, want %q", got, SessionAwaiting)
	}
	hist := sessionHistory(sess)
	salvageFound := false
	markerFound := false
	for _, turn := range hist {
		if turn.Kind != schema.TurnSteering {
			continue
		}
		if turn.SteeringKind == events.SteeringKindInterruptedSalvage && turn.Message.Text() == interruptSalvageSteering {
			salvageFound = true
		} else if turn.SteeringKind == events.SteeringKindInterrupted {
			markerFound = true
		}
	}
	if !salvageFound {
		t.Fatal("streamed cancellation did not persist its salvage explanation")
	}
	if markerFound {
		t.Fatal("failed main interrupt marker appeared in live history")
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored pending count after failed main marker = %d, want 1", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state after failed main marker = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_InterruptMarkerRetainedWriteIsAdopted covers the other durable
// pair outcome: the whole marker line remains after its first sync and rollback
// both fail, and the recovery barrier is also unavailable, so the owner adopts
// the ErrRetainedUnsynced record once.
func TestAskUser_InterruptMarkerRetainedWriteIsAdopted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted provider and local temporary transcript files use real
	// I/O; 30s is a generous hang guard, and this test makes no network requests.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	ask := askUserCall("ask1", askUserArgsValid())
	triggerCancel := llm.ToolCallData{ID: "cancel1", Name: "trigger_cancel", Arguments: json.RawMessage(`{}`), Type: "function"}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, triggerCancel) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.RegisterTool("trigger_cancel", "cancels after ask_user posts",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) { return "cancel after the tool results are durable", nil })
	syncFailure := errors.New("retained interrupt marker sync failure")
	rollbackFailure := errors.New("retained interrupt marker rollback failure")
	durabilityFailure := errors.New("retained interrupt marker barrier failure")
	processCtx := context.WithValue(ctx, sessionToolRoundHooksKey{}, sessionToolRoundHooks{
		beforeSteering: func() {
			// Attach and arm only after ask_user's successful tool-results turn
			// has been persisted, so the injected outcome belongs to the marker.
			attachEnvironmentUnverifiableWrite(t, sess, syncFailure, rollbackFailure, durabilityFailure)
			cancel()
		},
	})

	_, processErr := sess.ProcessInput(processCtx, "which db should we use?", nil)
	if !errors.Is(processErr, context.Canceled) {
		t.Fatalf("ProcessInput err = %v, want context.Canceled", processErr)
	}
	for _, injected := range []error{syncFailure, rollbackFailure, durabilityFailure} {
		if errors.Is(processErr, injected) {
			t.Fatalf("retained marker was rejected instead of adopted: %v", processErr)
		}
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after retained interrupt marker = %d, want 0", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("live state after retained interrupt marker = %q, want %q", got, SessionIdle)
	}
	markerCount := 0
	for _, turn := range sessionHistory(sess) {
		if turn.SteeringKind == events.SteeringKindInterrupted {
			markerCount++
		}
	}
	if markerCount != 1 {
		t.Fatalf("live retained interrupt markers = %d, want exactly one", markerCount)
	}
	markerEventCount := 0
	durabilityWarning := false
	for _, ev := range drainPendingEvents(sess) {
		if data, ok := ev.Data.(events.SteeringInjectedData); ev.Kind == events.EventSteeringInjected && ok && data.Kind == events.SteeringKindInterrupted {
			markerEventCount++
		}
		if warning, ok := ev.Data.(events.WarningData); ev.Kind == events.EventWarning && ok && strings.Contains(warning.Message, durabilityFailure.Error()) {
			durabilityWarning = true
		}
	}
	if markerEventCount != 1 {
		t.Fatalf("live retained interrupted steering events = %d, want exactly one", markerEventCount)
	}
	if !durabilityWarning {
		t.Fatal("retained interrupt marker barrier failure was not surfaced as a warning")
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state after retained interrupt marker = %q, want %q", got, SessionIdle)
	}
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored pending count after retained interrupt marker = %d, want 0", got)
	}
	restoredMarkerCount := 0
	for _, turn := range sessionHistory(restored) {
		if turn.SteeringKind == events.SteeringKindInterrupted {
			restoredMarkerCount++
		}
	}
	if restoredMarkerCount != 1 {
		t.Fatalf("restored retained interrupt markers = %d, want exactly one", restoredMarkerCount)
	}
}

// TestAskUser_RestoreResolvesAcrossUserSteer covers the accepted-user-steer
// half of the same boundary: a steer enters processOneInput as EntryUserInput,
// which clears s.askPending unconditionally on entry. Driven through the real
// steering-carrier entry point (AcceptClientMutationSteer, claimSteeringCarrierTurn,
// acceptSteeringCarrierInput — the same production path
// session_notes_refine_test.go's TestNotificationWakeFirstRequestReflectsURLRemoval
// drives directly, and the one consumeSteeringMessage/appendSteeringTurn
// actually build the persisted TurnSteering(source=user) turn through), not a
// hand-built turn.
func TestAskUser_RestoreResolvesAcrossUserSteer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-steer pending count = %d, want 1 (test setup broken)", got)
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-1",
		Input:            clientMutationInput("focus on the tests", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	if err := sess.acceptSteeringCarrierInput(ctx, queuedClientMutationIdentity{ClientMutationID: "steer-1", StableTurnID: turnID, SteeringCarrier: true}); err != nil {
		t.Fatalf("acceptSteeringCarrierInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after the accepted steer = %d, want 0", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 0 {
		t.Fatalf("restored askPending = %+v, want empty (an accepted user steer resolves the pending ask)", restored.askPending)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q (deriveRestoredState and deriveRestoredAskPending must agree on this boundary)", got, SessionIdle)
	}
}

// TestAskUser_RestoreResolvesAcrossUserSteerFollowedBySameRoundDaemonReminder
// is the combined fixture #1946 asks for alongside the individual oracles:
// TestAskUser_RestoreResolvesAcrossUserSteer pins a resolving user steer
// alone, TestAskUser_RestoreRederivesAwaitingAcrossTrailingSteering pins a
// trailing reminder alone — this pins the two together, in one round. The
// steer is accepted through the real queue/claim/carrier path (so its turn
// and journal record are exactly what production writes), and the round it
// opens then carries a daemon task reminder (the same shape
// injectPostToolSteering appends via appendSteeringTurn: kind task-nudge, no
// user source) before a plain final response. On restore, the final
// response is a generic completion whose round-entry check
// (roundEntryResolvesAskBoundary) walks back and FIRST meets the reminder —
// non-resolving — so the completion cannot settle the boundary there; the
// scan must continue past the reminder and reach the user steer entry,
// which resolves it and keeps the older ask cleared. No production defect
// was found in this interaction; the fixture exists to pin it.
func TestAskUser_RestoreResolvesAcrossUserSteerFollowedBySameRoundDaemonReminder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-steer pending count = %d, want 1 (test setup broken)", got)
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-1",
		Input:            clientMutationInput("focus on the tests", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	if err := sess.acceptSteeringCarrierInput(ctx, queuedClientMutationIdentity{ClientMutationID: "steer-1", StableTurnID: turnID, SteeringCarrier: true}); err != nil {
		t.Fatalf("acceptSteeringCarrierInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after the accepted steer = %d, want 0", got)
	}

	// The same-round daemon task reminder, appended exactly as production
	// appends daemon nudges (appendSteeringTurn: task-nudge kind, no user
	// source), and the round's plain final response after it. Both are
	// transcript-shape-only appends, like the trailing-steering oracle's:
	// the restore scan inspects shape, not which mechanism produced it.
	sess.appendSteeringTurn(taskReminderNudge(), events.SteeringKindTaskNudge)
	sess.appendTurn(schema.TurnAssistant, llm.Assistant("noted"))
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live pending count after the reminder and completion = %d, want 0 (neither may resurrect the cleared ask)", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 0 {
		t.Fatalf("restored askPending = %+v, want empty (the outer scan must pass the reminder and resolve at the user steer entry)", restored.askPending)
	}
	// The round ends in a plain final response with nothing after it, so the
	// restored at-rest state is the generic "agent moved last" awaiting of
	// deriveRestoredState's TurnAssistant branch — with no ask pending. That
	// Awaiting-with-empty-pending pair is the shape
	// TestAskUser_RestoreGenericAwaitingKeepsPendingEmptyAndGoalKicks pins;
	// what this fixture adds is that the reminder never lets the scan
	// re-derive the older ask underneath it.
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (generic awaiting: agent moved last, no ask pending)", got, SessionAwaiting)
	}
}

// TestAskUser_HumanNoteDuringPendingAskDoesNotResolveIt covers RoboRev
// #1806 round 4's High/Medium: a human-note update is user-sourced
// (events.SteeringSourceUser) exactly like an answering steer, but it does
// not address the pending question, so it must not clear askPending. This
// drives the REAL production wake path SetHumanNote uses --
// ProcessPendingUserInput, which claims the note as a steering carrier and
// enters processOneInput as EntryUserInput -- rather than calling
// acceptSteeringCarrierInput directly: the earlier version of this test did
// that, which skips processOneInput's entry-clear entirely and could not
// catch the entry clear firing unconditionally before
// clearAskPendingForResolvingSteer ever ran.
func TestAskUser_HumanNoteDuringPendingAskDoesNotResolveIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return finalResponse("noted") },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-note pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	// The carrier turn still runs a model round to read the drained note (the
	// same "carries no content of its own but still requests a completion"
	// shape TestAskUser_InjectPostToolSteeringClearsPendingAskForAnAcceptedUserSteer
	// exercises for an ordinary steer); only the live pending count is
	// asserted here.
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after a human-note steer drained through the real ProcessPendingUserInput/processOneInput path = %d, want 1 (a note update does not answer the pending ask)", got)
	}
}

// TestAskUser_RestoreUsesJournalProvenanceForAKindlessLegacyHumanNoteTurn
// covers RoboRev #1806 round 4's Medium (session_tools_ask.go:323): a
// steering turn persisted before SteeringKind was stamped carries no kind of
// its own, so a legacy human-note turn looks like an ordinary (kindless)
// answering steer and wrongly resolves a pending ask on restore. The
// client-mutation journal still knows the record was notes/human/set even
// when the turn's own SteeringKind field does not, and turnResolvesAskBoundary
// (via steeringOriginForTurn) must read that provenance for a kindless turn
// instead of defaulting to "answers". Built directly against
// deriveRestoredAskPending/deriveRestoredState (as the offline restore-
// contract tests in session_tools_misc_contract_fuzz_test.go do) rather than
// through a live session, since a fresh session always stamps kinds and so
// cannot reproduce the legacy (pre-stamping) shape.
// The fixture text deliberately does NOT carry the write-path human-note
// prefix (humanNoteSteerPrefix): isHumanNoteSteer's last-resort text-shape
// fallback would classify the turn as a note even with the journal ignored,
// leaving the journal record with nothing left to prove. With the prefix
// absent, only the origins lookup can keep the ask pending, and the
// nil-origins control below asserts the discriminating pair directly: the
// same kindless turn resolves as an ordinary answering steer exactly when
// no journal record backs it (the text-shape fallback itself is the
// sibling test TestAskUser_RestoreClassifiesAKindlessProvenancelessHumanNoteByItsTextShape's
// own subject, and keeps its prefixed fixture for exactly that reason).
func TestAskUser_RestoreUsesJournalProvenanceForAKindlessLegacyHumanNoteTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// A legacy human-note steering turn: user-sourced, kindless (as it would
	// be if written before SteeringKind existed), its ClientMutationID the
	// only link back to the journal record that still says what wrote it.
	// The text carries no human-note prefix, so the journal record is the
	// only note evidence this turn has.
	legacyNote := schema.NewTurn(schema.TurnSteering, llm.User("watch the ingest path"))
	legacyNote.SteeringSource = events.SteeringSourceUser
	legacyNote.ClientMutationID = "note-legacy"
	history := append(append([]schema.Turn{}, sess.history...), legacyNote)
	origins := map[string]steeringOrigin{"note-legacy": {method: clientMutationMethodNotesHumanSet}}

	pending, isAskRound := deriveRestoredAskPending(history, 0, origins)
	if !isAskRound || len(pending) != 1 {
		t.Fatalf("deriveRestoredAskPending with journal provenance = pending=%#v isAskRound=%v, want ask1 still pending", pending, isAskRound)
	}
	if state := deriveRestoredState(history, 0, origins); state != SessionAwaiting {
		t.Fatalf("deriveRestoredState with journal provenance = %q, want %q", state, SessionAwaiting)
	}

	// The discriminating nil-origins control: the SAME kindless, prefix-free
	// turn with no journal record to consult reads as an ordinary answering
	// steer and resolves the ask. Only the origins-versus-nil difference
	// separates the two runs, so the journal's notes/human/set evidence is
	// load-bearing: a restore path that ignored it would fail the first
	// half, and one that hallucinated note classification without it would
	// fail this one.
	pending, isAskRound = deriveRestoredAskPending(history, 0, nil)
	if isAskRound || len(pending) != 0 {
		t.Fatalf("deriveRestoredAskPending without journal provenance = pending=%#v isAskRound=%v, want the kindless turn to resolve the ask as an ordinary answering steer", pending, isAskRound)
	}
	if state := deriveRestoredState(history, 0, nil); state != SessionIdle {
		t.Fatalf("deriveRestoredState without journal provenance = %q, want %q", state, SessionIdle)
	}
}

// TestAskUser_RestoreClassifiesAKindlessProvenancelessHumanNoteByItsTextShape
// covers RoboRev #1907 round 2's low/medium (session_tools_ask.go:117-122,
// 374-376 vs session_notes_rpc.go:931-936): origin.steeringKind() only
// recovers note origin from a recorded kind or a notes/human/set method,
// unlike isHumanNoteSteer, which ALSO falls back to the write-path text
// shape (humanNoteSteerPrefix) when neither is available. turnResolvesAskBoundary
// used steeringKind() directly, so a kindless steering turn with NO reachable
// provenance -- the sibling case TestAskUser_RestoreUsesJournalProvenanceForAKindlessLegacyHumanNoteTurn
// covers has a journal record, this one does not -- read as an ordinary
// (kindless) answering steer and wrongly resolved a pending ask. This is
// exactly what happens to an inherited fork prefix: steeringOriginBoundary
// nils the origins lookup for every turn before divergenceTurn regardless of
// what the map carries, so a legacy pre-kind-stamping note in that prefix has
// no provenance left except its own text. Built directly against
// deriveRestoredAskPending/deriveRestoredState, like the sibling test, with
// divergenceTurn set so the note turn is entirely inherited.
func TestAskUser_RestoreClassifiesAKindlessProvenancelessHumanNoteByItsTextShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// A legacy human-note steering turn with NO provenance reachable at all:
	// kindless, and belonging to an inherited fork prefix -- divergenceTurn
	// below scopes it out of the origins map entirely, unlike the journal-
	// backed sibling test. Only the write-path text shape can still mark it a
	// note.
	legacyNote := schema.NewTurn(schema.TurnSteering, llm.User("human updated their whiteboard: watch the ingest path"))
	legacyNote.SteeringSource = events.SteeringSourceUser
	legacyNote.ClientMutationID = "note-legacy-inherited"
	history := append(append([]schema.Turn{}, sess.history...), legacyNote)
	divergenceTurn := len(history) + 1 // entirely inherited: steeringOriginBoundary nils origins for it
	origins := map[string]steeringOrigin{"note-legacy-inherited": {method: clientMutationMethodNotesHumanSet}}

	pending, isAskRound := deriveRestoredAskPending(history, divergenceTurn, origins)
	if !isAskRound || len(pending) != 1 {
		t.Fatalf("deriveRestoredAskPending for an inherited kindless note = pending=%#v isAskRound=%v, want ask1 still pending", pending, isAskRound)
	}
	if state := deriveRestoredState(history, divergenceTurn, origins); state != SessionAwaiting {
		t.Fatalf("deriveRestoredState for an inherited kindless note = %q, want %q", state, SessionAwaiting)
	}
}

// TestAcceptSteeringCarrierInput_PanicMidDrainStillClearsTheClaim covers
// RoboRev #1806 round 4's Low (session_lifecycle.go:2733-2735):
// setSteeringCarrierClaimDrain(id) / injectDrainedSteering() /
// setSteeringCarrierClaimDrain("") cleared without defer, so a panic between
// the two calls leaked the claim id — a later unrelated
// recordFailedSteeringSelection could then mistag its own TurnFailure
// SteeringCarrier and wrongly resolve an ask on restore
// (steeringSelectionFailureIsCarrierClaim keys purely on the leaked id).
// Panics from inside steerAppendRefusal.onRefuse
// (session_drain_as_steer_turn_boundary_test.go), the existing seam for "the
// steer is popped and its append is about to fail": it fires from inside
// s.clientMutationTranscriptAppend, i.e. inside consumeSteeringMessage inside
// injectDrainedSteering, so this drives a REAL mid-drain panic rather than
// one injected before the drain is ever entered. Measured: nothing between
// acceptSteeringCarrierInput and the append hook recovers (no recover() in
// session_queue.go/session_client_mutation.go/session.go other than
// processOneInput's, which this direct call never reaches), and
// appendTurnAfterTranscriptWriteLocked calls write() before taking s.mu, so
// the panic unwinds through attentionMu's defer without ever holding s.mu.
func TestAcceptSteeringCarrierInput_PanicMidDrainStillClearsTheClaim(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	defer sess.Close()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-1",
		Input:            clientMutationInput("hello", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	identity := queuedClientMutationIdentity{ClientMutationID: "steer-1", StableTurnID: turnID, SteeringCarrier: true}
	refusal := refuseSteerAppends(sess, "steer-1")
	refusal.onRefuse = func() { panic("injected steering carrier drain panic") }
	refusal.refuse.Store(true)
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("injected panic did not propagate")
			}
		}()
		_ = sess.acceptSteeringCarrierInput(context.Background(), identity)
	}()
	if sess.steeringSelectionFailureIsCarrierClaim("steer-1") {
		t.Fatal("the claim id leaked across the panic: a later unrelated recordFailedSteeringSelection would mistag its own TurnFailure SteeringCarrier")
	}
}

// TestAskUser_RestoreResolvesAcrossFailedSteeringCarrier covers the case a
// user steer's OWN turn fails outright before posting anything: processOneInput
// clears s.askPending unconditionally on entry (session_lifecycle.go's
// "Pending asks resolve with this accepted turn" comment), before the turn's
// model call ever runs, so a steering carrier that fails immediately ("it
// carries no content of its own", session_lifecycle.go) has already resolved
// the ask server-side even though it leaves no steering/user turn behind —
// only a TurnFailure marker tagged SteeringCarrier (schema.TurnFailureInfo's
// own doc comment). Driven through the real acceptSteeringCarrierInput ->
// carrierSteerUndelivered path (session_notes_refine_test.go's harness), with
// the steer's own transcript append forced to fail — not a hand-built
// TurnFailure turn.
func TestAskUser_RestoreResolvesAcrossFailedSteeringCarrier(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "carrier-1",
		Input:            clientMutationInput("please hold", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	refusal := refuseSteerAppends(sess, "carrier-1")
	refusal.refuse.Store(true)
	if err := sess.acceptSteeringCarrierInput(ctx, queuedClientMutationIdentity{ClientMutationID: "carrier-1", StableTurnID: turnID, SteeringCarrier: true}); err == nil {
		t.Fatal("acceptSteeringCarrierInput succeeded, want the injected append failure")
	}
	if got := refusal.refusals.Load(); got != 1 {
		t.Fatalf("steer append attempts = %d, want 1", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 0 {
		t.Fatalf("restored askPending = %+v, want empty (a failed steering carrier resolves the pending ask)", restored.askPending)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q (deriveRestoredState and deriveRestoredAskPending must agree on this boundary)", got, SessionIdle)
	}
}

// TestAskUser_RestoreResolvesAcrossFailedSteeringSelectionCarrier covers the
// case a claimed carrier's OWN steer is retired for a different reason: its
// skill selection could not be prepared (recordFailedSteeringSelection),
// rather than its transcript append failing (the case above). The carrier
// turn's mere acceptance still cleared askPending before the drain ever ran,
// so the untagged TurnFailure the selection failure would otherwise leave
// behind must be tagged SteeringCarrier too, or restore rescans past it to
// the earlier ask_user call and re-derives a pending ask the live session
// had already closed. Driven through the real claimSteeringCarrierTurn ->
// acceptSteeringCarrierInput path with an unresolvable skill name, not a
// hand-built TurnFailure.
func TestAskUser_RestoreResolvesAcrossFailedSteeringSelectionCarrier(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "carrier-skill-1",
		Input:            clientMutationInput("please hold", nil, []string{"no-such-skill"}),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	identity := queuedClientMutationIdentity{ClientMutationID: "carrier-skill-1", StableTurnID: turnID, SteeringCarrier: true}
	if err := sess.acceptSteeringCarrierInput(ctx, identity); !errors.Is(err, errSteeringCarrierStoodDown) {
		t.Fatalf("acceptSteeringCarrierInput error = %v, want errSteeringCarrierStoodDown", err)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 0 {
		t.Fatalf("restored askPending = %+v, want empty (a failed steering-selection carrier resolves the pending ask)", restored.askPending)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q (deriveRestoredState and deriveRestoredAskPending must agree on this boundary)", got, SessionIdle)
	}
}

// TestAskUser_RestoreDoesNotResolveAcrossAFailedHumanNoteCarrierAppend covers
// RoboRev #1806 round 5's Medium (members 0, 1, 2): the mirror of
// TestAskUser_RestoreResolvesAcrossFailedSteeringCarrier above for a
// human-note carrier. steeringCarrierClaimAnswersAsk already skips the entry
// clear for a human note, so askPending stays live-pending; if the note's
// own steer then fails to append, acceptSteeringCarrierInput must not tag
// the resulting TurnFailure SteeringCarrier either — doing so would let
// restore's backward scan stop at this failure and derive the ask as
// resolved (SessionIdle, empty pending) while the live session never
// resolved it. Driven through the real acceptSteeringCarrierInput ->
// carrierSteerUndelivered path with the note's own transcript append forced
// to fail, exactly like the answering-steer sibling above.
func TestAskUser_RestoreDoesNotResolveAcrossAFailedHumanNoteCarrierAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued note")
	}
	refusal := refuseSteerAppends(sess, "note-1")
	refusal.refuse.Store(true)
	if err := sess.acceptSteeringCarrierInput(ctx, queuedClientMutationIdentity{ClientMutationID: "note-1", StableTurnID: turnID, SteeringCarrier: true}); err == nil {
		t.Fatal("acceptSteeringCarrierInput succeeded, want the injected append failure")
	}
	if got := refusal.refusals.Load(); got != 1 {
		t.Fatalf("note append attempts = %d, want 1", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 1 {
		t.Fatalf("restored askPending = %+v, want 1 question (a failed human-note carrier append must not resolve the pending ask)", restored.askPending)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (a human-note carrier's own append failure must not resolve the ask either)", got, SessionAwaiting)
	}
}

// TestAskUser_RecordFailedSteeringSelectionDoesNotTagAHumanNoteClaim covers
// the skill-prepare-failure half of the same class: recordFailedSteeringSelection
// used to tag SteeringCarrier on any claim-ID match, regardless of whether
// the claimed steer itself answers the ask. SetHumanNote cannot attach a
// skill selection today (its addPendingSteering call only ever posts a text
// InputItem), so a human note carrying SkillNames cannot arise through the
// live RPC surface; this drives recordFailedSteeringSelection directly with
// a synthetic human-note-kinded message to exercise the shared predicate
// (steeringCarrierClaimAnswersAsk) itself, over a REAL journal record
// (SetHumanNote is still the one path that stamps SteeringKindHumanNote).
func TestAskUser_RecordFailedSteeringSelectionDoesNotTagAHumanNoteClaim(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-skill-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	sess.setSteeringCarrierClaimDrain("note-skill-1")
	msg := steeringMessage{
		ClientMutationID: "note-skill-1",
		Kind:             events.SteeringKindHumanNote,
		Text:             "human updated their whiteboard: watch the ingest path",
	}
	if !sess.recordFailedSteeringSelection(msg, errors.New(`skill "no-such-skill" not found`)) {
		t.Fatal("recordFailedSteeringSelection reported the record itself failed to append")
	}
	sess.setSteeringCarrierClaimDrain("")
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after the tagged failure = %d, want 1 (unaffected either way; the tag governs restore)", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 1 {
		t.Fatalf("restored askPending = %+v, want 1 question (a human-note claim's failed skill selection must not resolve the pending ask)", restored.askPending)
	}
}

// TestAskUser_RestoreDoesNotResolveAcrossASuccessfulHumanNoteCarrierCompletion
// covers the open Medium from RoboRev #1806's round-6 panel
// (session_tools_ask.go:539-542): a successful human-note carrier does not
// clear askPending live (steeringCarrierClaimAnswersAsk skips the entry
// clear, and the note itself is not an answering steer per
// steeringAnswersAsk), but its round still runs a real model
// completion to acknowledge the note. deriveRestoredAskPending's
// TurnToolResults branch treated ANY non-ask_user completion (e.g. a
// communicate ack) as decisive, on the unstated assumption that every
// round's entry already cleared askPending live -- true for a genuine user
// reply, false for a non-resolving carrier. The backward scan stopped at
// this completion and rebuilt an empty pending set, losing the still-live
// ask1. Drives the real SetHumanNote -> ProcessPendingUserInput ->
// processOneInput path (like TestAskUser_HumanNoteDuringPendingAskDoesNotResolveIt),
// with the note's round completing via a real "communicate" tool call
// (communicateCall) rather than a bare final response, so the completion
// lands as TurnToolResults, not TurnAssistant.
func TestAskUser_RestoreDoesNotResolveAcrossASuccessfulHumanNoteCarrierCompletion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	comm := communicateCall("c1", "noted")
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-note pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after the note's round completed = %d, want 1 (test setup broken)", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 1 {
		t.Fatalf("restored askPending = %+v, want 1 question (a successful human-note carrier's own completion must not resolve the pending ask)", restored.askPending)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_RestoreAccumulatesPendingQuestionsAcrossNonResolvingRounds
// covers RoboRev #1906/#1907's round-2 Medium (session_tools_ask.go:583-589
// in the panel's numbering): a human-note carrier preserves ask1 (its entry
// clear is skipped), but its OWN round can post a second, unrelated
// ask_user question (ask2) instead of just acknowledging the note --
// ask_user's live Exec APPENDS to askPending (registerAskTool,
// "s.askPending = append(s.askPending, parsed...)"), never replaces it, so
// live askPending ends up [ask1, ask2]. deriveRestoredAskPending's backward
// scan used to stop at the FIRST ask_user round it found (the newest,
// ask2's) and return only its questions, silently dropping ask1. The scan
// now accumulates every non-resolving round's ask_user questions back to
// the last round whose OWN entry actually resolved the boundary
// (roundEntryResolvesAskBoundary), matching live's append semantics.
func TestAskUser_RestoreAccumulatesPendingQuestionsAcrossNonResolvingRounds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask1 := askUserCall("ask1", askUserArgsValid())
	ask2Args := map[string]any{
		"questions": []any{
			map[string]any{
				"header":   "Naming",
				"question": "What should we call the new package?",
				"options": []any{
					map[string]any{"label": "short names", "detail": "terse"},
					map[string]any{"label": "descriptive names", "detail": "verbose"},
				},
			},
		},
	}
	ask2 := askUserCall("ask2", ask2Args)
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask1) },
			func(req llm.Request) llm.Response { return toolCallResponse(ask2) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-note pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}
	if got := sess.askPendingCount(); got != 2 {
		t.Fatalf("live pending count after the note's own round posted a second question = %d, want 2 (ask1 and ask2 both pending; the note never answered ask1)", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 2 {
		t.Fatalf("restored askPending = %+v, want 2 questions (ask1 from before the note, ask2 from the note's own round)", restored.askPending)
	}
	headers := map[string]bool{}
	for _, q := range restored.askPending {
		headers[q.Header] = true
	}
	if !headers["DB choice"] || !headers["Naming"] {
		t.Fatalf("restored askPending headers = %+v, want both %q and %q", restored.askPending, "DB choice", "Naming")
	}
}

// buildAnsweredAskHistoryForFork drives a real (throwaway) session through an
// ask_user round resolved by a real answering steer under clientMutationID,
// and returns its final history: [TurnUserInput, TurnAssistant(ask1 call),
// TurnToolResults(ask1 ack), TurnSteering(clientMutationID, kindless,
// source=user)]. Used as a fork's inherited prefix below — a real production
// shape, not a hand-built turn.
func buildAnsweredAskHistoryForFork(t *testing.T, clientMutationID string) []schema.Turn {
	t.Helper()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: clientMutationID,
		Input:            clientMutationInput("go ahead and use Postgres", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	if err := sess.acceptSteeringCarrierInput(ctx, queuedClientMutationIdentity{ClientMutationID: clientMutationID, StableTurnID: turnID, SteeringCarrier: true}); err != nil {
		t.Fatalf("acceptSteeringCarrierInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("pending count after the answering steer = %d, want 0 (fixture setup broken)", got)
	}
	return append([]schema.Turn{}, sess.history...)
}

// TestAskUser_ForkedChildDoesNotApplyItsOwnJournalToAnInheritedAnsweringSteer
// covers RoboRev #1806 round 5's Medium (member 1, and member 0/2's second
// finding): deriveRestoredState/deriveRestoredAskPending applied the child
// session's WHOLE steering journal (s.clientMutations.steeringOrigins()) to
// its entire history, including the inherited prefix a fork copied from its
// parent. A reused ClientMutationID -- the inherited turn is an ordinary
// (kindless) answering steer the PARENT recorded, but the CHILD's own,
// unrelated journal happens to record a notes/human/set record under the
// same id -- let the child's record reclassify the parent's turn as a
// non-resolving note, so restore rederives the already-answered ask1 as
// still pending. Fixed by scoping the provenance lookup exactly like
// escapeHistoryWithSessionProvenance already does (steeringOriginBoundary):
// nil origins for turns before DivergenceTurn, the child's own journal only
// for turns at or after it. inheritedHistory here is entirely inherited
// (DivergenceTurn = len+1), mirroring
// TestRestoredForkEscapesItsInheritedPrefixWithoutTheChildJournal's own
// fork-collision harness for the notes-escaping class of this same bug.
func TestAskUser_ForkedChildDoesNotApplyItsOwnJournalToAnInheritedAnsweringSteer(t *testing.T) {
	t.Parallel()
	const collidingID = "cm-collides"
	inheritedHistory := buildAnsweredAskHistoryForFork(t, collidingID)

	const sessionID = "01KASKFORKPROVENANCEBOUND0"
	stateDir := t.TempDir()
	store, err := newClientMutationStore(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(func(snapshot *clientMutationSnapshot) error {
		req := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, collidingID, struct{ Note string }{Note: "the child's own, unrelated note"})
		snapshot.Journal[collidingID] = clientMutationRecord{
			ClientMutationID:  req.ClientMutationID,
			Method:            req.Method,
			Payload:           req.Payload,
			PayloadHash:       req.PayloadHash,
			OperationState:    clientMutationOperationTerminal,
			ExecutionState:    "incorporated",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		// Every turn in inheritedHistory came from the parent; the child has
		// not added any of its own yet.
		ParentSessionID: "01KPARENT0000000000000000",
		DivergenceTurn:  len(inheritedHistory) + 1,
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{StateDir: stateDir, resumeHistory: inheritedHistory},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if len(restored.askPending) != 0 {
		t.Fatalf("restored askPending = %+v, want empty (the parent's real answering steer already resolved this; the child's own colliding journal record must not reclassify it as a note)", restored.askPending)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q", got, SessionIdle)
	}
}

// TestAskUser_RestoreDoesNotResolveAcrossAnUnrelatedTurnFailure is the
// narrowing this round adds: only a TurnFailure tagged SteeringCarrier
// (session_lifecycle.go's acceptSteeringCarrierInput, carrierSteerUndelivered)
// is a resolution boundary. An ordinary TurnFailure — here,
// recordFailedSteeringSelection's record of a drained steer whose skill
// selection could not be prepared (the "mid-turn steering-selection failure"
// this round's fix explicitly preserves pending asks across) — must not
// resolve anything: the ask_user call's own TurnToolResults, still ahead of
// it in the backward scan, is what restore reads. Driven through the real
// consumeSteeringMessage/recordFailedSteeringSelection path
// TestSkillActivation_SteeringSelectionReconciledAfterAdmissionSaveFailure
// also drives directly, appended AFTER a real ask_user round — not a
// hand-built TurnFailure.
func TestAskUser_RestoreDoesNotResolveAcrossAnUnrelatedTurnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-failure pending count = %d, want 1 (test setup broken)", got)
	}

	if sess.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"no-such-skill"}}) == steeringAppendFailed {
		t.Fatal("the failure record itself did not durably land (test setup broken)")
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after the unrelated failure = %d, want 1 (the failure never touched this round's own ask)", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := len(restored.askPending); got != 1 {
		t.Fatalf("restored askPending = %+v (len %d), want the one ask1 question still pending", restored.askPending, got)
	}
	if got := restored.askPending[0].Header; got != "DB choice" {
		t.Fatalf("restored askPending[0].Header = %q, want %q", got, "DB choice")
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (the unrelated failure must not have resolved this ask)", got, SessionAwaiting)
	}
}

// TestAskUser_InjectPostToolSteeringClearsPendingAskForAnAcceptedUserSteer
// covers the live half of the steer-resolution boundary: a user steer queued
// while the ask_user round is still running is drained by
// injectPostToolSteering, mid-round, well before any accepted-turn ENTRY
// would otherwise clear askPending. Queuing the steer inside the adapter's
// first-step closure (which runs and returns before the harness executes
// ask_user's own tool call, in turn before injectPostToolSteering runs) puts
// it in the queue in time for that same round's drain. Clearing askPending
// mid-round is not enough on its own: the drained steer must also reach the
// model on a second request rather than stranding the round at the ask
// boundary it just resolved — the second fakeAdapter step only fires for a
// genuine new round, and its request is asserted to carry the steer's text.
func TestAskUser_InjectPostToolSteeringClearsPendingAskForAnAcceptedUserSteer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	var sess *Session
	queueSteer := func() {
		if err := sess.ensureClientMutationStore(); err != nil {
			t.Fatalf("ensureClientMutationStore: %v", err)
		}
		if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: "mid-flight-steer",
			Input:            clientMutationInput("go ahead and pick Postgres", nil, nil),
		}); err != nil {
			t.Fatalf("AcceptClientMutationSteer: %v", err)
		}
	}
	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				queueSteer()
				return toolCallResponse(ask)
			},
			func(req llm.Request) llm.Response {
				return finalResponse("using Postgres, thanks")
			},
		},
	}
	c := llm.NewClient()
	c.Register(fa)
	var err error
	sess, err = NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount after the mid-flight steer drained = %d, want 0 (the drained steer resolves the ask it landed after)", got)
	}
	var sawUserSteer bool
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && turn.SteeringSource == events.SteeringSourceUser {
			sawUserSteer = true
		}
	}
	if !sawUserSteer {
		t.Fatal("no TurnSteering(source=user) landed in history; the drain never ran (test setup broken)")
	}
	requests := fa.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2 (a second request must read the drained steer, not strand it at the ask boundary)", len(requests))
	}
	if !requestContainsText(requests[1], "go ahead and pick Postgres") {
		t.Fatalf("second model request did not carry the drained steer: %+v", requests[1])
	}
}

// TestAskUser_SteeringInjectedNeverObservesAskPendingStillTrue covers
// RoboRev #1806's member-3 Medium: a delivered user steer must clear
// askPending BEFORE EventSteeringInjected publishes, because the server
// refreshes its ask facet on that event (server/thread_envelope.go); emitting
// first lets that refresh cache a stale askPending=true until the next ask
// change. The production fix threads a testOnly hook
// (cfg.testOnly.beforeSteeringInjectedPublish) that fires synchronously,
// on the SAME goroutine as consumeSteeringMessage, immediately before the
// event publishes — so this samples askPendingCount() at exactly that
// instant with no cross-goroutine race to reason about.
func TestAskUser_SteeringInjectedNeverObservesAskPendingStillTrue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	var sess *Session
	queueSteer := func() {
		if err := sess.ensureClientMutationStore(); err != nil {
			t.Fatalf("ensureClientMutationStore: %v", err)
		}
		if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: "mid-flight-steer",
			Input:            clientMutationInput("go ahead and pick Postgres", nil, nil),
		}); err != nil {
			t.Fatalf("AcceptClientMutationSteer: %v", err)
		}
	}
	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				queueSteer()
				return toolCallResponse(ask)
			},
			func(req llm.Request) llm.Response {
				return finalResponse("using Postgres, thanks")
			},
		},
	}
	c := llm.NewClient()
	c.Register(fa)
	var err error
	sess, err = NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var samples []int
	updateSessionTestConfig(sess, func(cfg *testConfig) {
		cfg.beforeSteeringInjectedPublish = func() {
			samples = append(samples, sess.askPendingCount())
		}
	})

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if len(samples) == 0 {
		t.Fatal("beforeSteeringInjectedPublish never fired; the drain never ran (test setup broken)")
	}
	for i, got := range samples {
		if got != 0 {
			t.Fatalf("askPendingCount immediately before EventSteeringInjected publish #%d = %d, want 0 (the ask facet must never observe the injected steer before the clear)", i, got)
		}
	}
}

// TestAskUser_ConsumeSteeringMessageEmitsInjectedBeforeAdmissionWarning covers
// RoboRev #1905 round 1's Medium (session_queue.go:1257): round 3's original
// intent was only to move clearAskPendingForResolvingSteer ahead of
// EventSteeringInjected, but the change also moved admitPreparedSkillSelection
// and unparkSteering ahead of the emit, flipping the observable event order
// from Injected-then-Warn to Warn-then-Injected whenever a skill-bearing
// steer's own append lands but its admission fails, and exposing parked state
// before the event that announces the steer landed. This drives the exact
// production path TestSkillActivation_SteeringSelectionReconciledAfterAdmissionSaveFailure
// already exercises for admission-save-failure reconciliation (consumeSteeringMessage
// with breakSessionMetaPath active) and pins that EventSteeringInjected still
// publishes strictly before the admission failure's EventWarning.
func TestAskUser_ConsumeSteeringMessageEmitsInjectedBeforeAdmissionWarning(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_order_pin")
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	eventsDone := captureSessionEvents(s)

	repair := breakSessionMetaPath(t, s)
	if s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) == steeringAppendFailed {
		t.Fatal("steering message was not durably consumed (test setup broken)")
	}
	repair()
	s.Close()

	captured := <-eventsDone
	injectedIdx, warnIdx := -1, -1
	for i, ev := range captured {
		switch ev.Kind {
		case events.EventSteeringInjected:
			if injectedIdx == -1 {
				injectedIdx = i
			}
		case events.EventWarning:
			if warnIdx == -1 {
				warnIdx = i
			}
		}
	}
	if injectedIdx == -1 || warnIdx == -1 {
		t.Fatalf("did not observe both events: EventSteeringInjected at %d, EventWarning at %d, captured=%+v", injectedIdx, warnIdx, captured)
	}
	if injectedIdx >= warnIdx {
		t.Fatalf("EventSteeringInjected at %d, EventWarning at %d; want Injected strictly before Warning (admit/unpark must stay after the emit)", injectedIdx, warnIdx)
	}
}

// TestAskUser_FollowUpNotDrainedWhilePendingAskSurvivesAHumanNoteCarrierRound
// covers RoboRev #1907 round 2's Medium (session_lifecycle.go's drain-ladder
// gate, ~line 1399): the gate's comment used to prove `s.State() ==
// SessionAwaiting` there is equivalent to `askPendingCount() > 0`, on the
// premise that processOneInput's entry always cleared askPending
// unconditionally -- true before this PR, false now for a human-note
// carrier (steeringCarrierClaimAnswersAsk skips the clear). A note carrier's
// own round, if it merely acknowledges the note (askedThisRound stays false
// since nothing NEW was posted), settles deliverIfCommunicated's boundary
// SessionIdle at this exact capture point -- armAwaitingAtSettle's general
// upgrade runs later, at the outer loop's own terminal settle -- so reading
// state alone reported "not awaiting" with ask1 still genuinely unanswered,
// and the ladder popped and ran a queued FollowUp it should have left
// intact. Drives the real ProcessPendingUserInput path for the note
// carrier's round (a real "communicate" tool call, matching
// TestAskUser_RestoreDoesNotResolveAcrossASuccessfulHumanNoteCarrierCompletion's
// shape) with a real FollowUp queued beforehand, and asserts the follow-up's
// own model request never ran within that same call.
func TestAskUser_FollowUpNotDrainedWhilePendingAskSurvivesAHumanNoteCarrierRound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	comm := communicateCall("c1", "noted")
	var followUpRan bool
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
			func(req llm.Request) llm.Response {
				followUpRan = true
				return finalResponse("ran the followup")
			},
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-note pending count = %d, want 1 (test setup broken)", got)
	}
	if err := sess.FollowUp("run the tests"); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}

	if _, err := sess.SetHumanNote("note-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}

	if followUpRan {
		t.Fatal("the queued follow-up ran while ask1 was still pending; the drain ladder must not pop it")
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after the note's round = %d, want 1 (still unanswered)", got)
	}
}

// TestAskUser_RestoreRederivesIdleAfterAnsweredAsk covers spec §6's other
// half: a user turn following the ack (the reply) resolves the question. It
// no longer asserts idle after the reply: under attention-status-model v5's
// inbox semantics (merged post-write-up), the reply's OWN turn is itself a
// clean completion with user-visible output and nothing else in flight, so
// it legitimately re-arms awaiting — the identical wire value an unresolved
// ask would show. What must hold, and what this test asserts instead, is
// that the ASK genuinely resolved: askPendingCount drops to 0 the moment the
// reply is accepted (spec §5.2, checked live, where the pending set is a
// real in-memory signal — it is not persisted, so checking it again after
// restore would be vacuous), and the reply demonstrably reached the model as
// a new turn (the second fakeAdapter step only fires for a genuine new
// round). Restored state is awaiting for the same inbox reason live state
// was, not because ask1 resurrected as pending: the transcript-shape pending
// definition (§6) that distinguishes "resolved" from "still pending" keys on
// ask1's ack having a LATER TurnUserInput, which it does here regardless of
// what the tail's own state settles to.
func TestAskUser_RestoreRederivesIdleAfterAnsweredAsk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return finalResponse("thanks, using Postgres") },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("pre-reply state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-reply pending count = %d, want 1 (test setup broken)", got)
	}
	out, err := sess.ProcessInput(ctx, "Postgres, thanks", nil)
	if err != nil {
		t.Fatalf("reply ProcessInput: %v", err)
	}
	if out != "thanks, using Postgres" {
		t.Fatalf("reply output = %q, want the model's second-step response (the reply must reach the model as a new turn)", out)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("post-reply pending count = %d, want 0 (the reply must resolve the ask)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("post-reply state = %q, want %q (inbox semantics: the reply's own clean, output-producing turn re-arms awaiting)", got, SessionAwaiting)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	// The restored state is awaiting for the same general reason (agent
	// moved last with the reply's own plain-text turn) — not because ask1
	// re-derived as pending. deriveRestoredAskPending (spec §2) reads this
	// tail as a GENERIC awaiting rest too — the decisive turn is the reply's
	// own plain final response, with no tool calls at all — so checking
	// askPendingCount here would still read 0: not because restore skips the
	// set (post-§2 it doesn't), but because nothing here is genuinely
	// pending. The meaningful check already ran live above.
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (agent moved last: the reply's own response)", got, SessionAwaiting)
	}
}

// TestAskUser_RestoreRederivesIdleAfterInterruptedAsk covers spec §6's
// never-pending carve-out: a crash between the model's ask_user tool call and
// the tool actually executing leaves an orphaned call with no ack anywhere in
// the transcript. ResumeHistory's own orphan repair (history_repair.go) turns
// that into a synthetic TOOL_RESULTS turn named "ask_user" so a provider
// never sees a dangling call — but that synthetic result is marked IsError,
// so it must NOT be mistaken for the ack (spec: "an interrupted ack-less ask
// is never pending"). Built by writing the transcript tail directly (a real
// crash can't be scripted): a USER_INPUT turn followed by an ASSISTANT turn
// whose tool call is never answered, with nothing else appended — the
// "call_lost" shape TestResumeHistoryRepairsOrphanedAssistantToolCallsBeforeLaterUserInput
// exercises one file over, but driven through the actual restore entry point
// so the repair really runs before the state derivation sees it.
func TestAskUser_RestoreRederivesIdleAfterInterruptedAsk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := "01ASKRESTOREINTERRUPTED"

	askArgs, err := json.Marshal(askUserArgsValid())
	if err != nil {
		t.Fatalf("marshal ask args: %v", err)
	}

	tpath := filepath.Join(dir, sessionsSubdir, id+".transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID: id,
		ProfileID: "openai",
		Model:     "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnUserInput, llm.User("which db should we use?"))); err != nil {
		t.Fatalf("append user turn: %v", err)
	}
	if err := tw.Append(schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "ask1", Name: "ask_user", Arguments: askArgs, Type: "function"}},
		},
	})); err != nil {
		t.Fatalf("append assistant turn: %v", err)
	}
	// Deliberately nothing else: the process died before ask_user's tool
	// result was ever recorded.
	if err := tw.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        id,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	// Test-setup sanity: confirm the orphan-repair path actually ran, so a
	// failure below proves the state derivation, not a broken fixture.
	repaired, ok := findToolResultInHistory(restored.history, "ask1")
	if !ok {
		t.Fatal("test setup: orphan repair did not synthesize a result for the interrupted ask1 call")
	}
	if !repaired.IsError {
		t.Fatal("test setup: synthetic orphan-repair result is not marked IsError — the case this test targets did not occur")
	}

	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q (an interrupted ack-less ask must never be pending)", got, SessionIdle)
	}
}

// --- Post-merge fixup: restore must rebuild the pending-ask SET, not just the state ---
// (ask-attention-tiering design spec, 2026-07-04, §2)
//
// The restore tests above prove deriveRestoredState re-derives SessionAwaiting
// after a restart with an unanswered ask_user question. That alone is not
// enough: every hold keyed on the pending SET rather than raw state — the
// entry gate (session_lifecycle.go:453), the goal engine's arm-don't-kick
// paths (session_goal.go:61,:246), and Compact's guard
// (session_compaction.go:30) — reads len(s.askPending), which stayed empty
// across every restore before this fix (askPending was written only by the
// live ask_user Exec path and cleared at turn entry, session_lifecycle.go:783).
// A restored session with a genuinely pending question therefore had every
// one of those holds silently inert. The tests below drive the real restore
// entry point (RestoreSessionFromMeta) and observe each hold at its own
// production seam, mirroring the live TestAskUser_EntryGateRefusesNotificationWake
// / TestGoalHoldsAwaiting_SetGoalArmsWithoutKick / TestAskUser_CompactRefusedWhileAwaiting
// tests above but through a restored session instead of a live one.

// TestAskUser_RestoreRebuildsPendingHoldsEntryGate covers the entry gate
// (spec §5.3): a restored session with an unanswered ask_user question must
// refuse an autonomous EntryNotification wake exactly as the live entry gate
// does (TestAskUser_EntryGateRefusesNotificationWake) — proving askPending
// itself, not just the derived SessionAwaiting state, survives restore. It
// wires its own fakeAdapter for the restore (rather than newAskRestoreClient's
// throwaway one) so it can assert on the request count: before the fix, the
// entry gate's len(s.askPending)>0 condition reads false after restore, so
// the wake is wrongly accepted and reaches Complete.
func TestAskUser_RestoreRebuildsPendingHoldsEntryGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	meta := sess.Meta()
	sess.Close()

	restoreAdapter := &fakeAdapter{name: "openai"}
	restoreClient := llm.NewClient()
	restoreClient.Register(restoreAdapter)
	restored, err := RestoreSessionFromMeta(restoreClient, withTestSessionNamer(restoreClient, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}

	restored.enqueueJobNotification(watchNotification("job_wake", "output_match: done"))

	out, err := restored.ProcessInputKind(ctx, "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) on a restored pending ask returned an error: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("ProcessInputKind(EntryNotification) on a restored pending ask returned %q, want empty (the wake must be held)", out)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("state after the wake = %q, want %q (must not flip)", got, SessionAwaiting)
	}
	if got := len(restoreAdapter.Requests()); got != 0 {
		t.Fatalf("model requests after the notification wake = %d, want 0 (the entry gate must hold it before any model call)", got)
	}
	if got := restored.peekNotifications(); got != 1 {
		t.Fatalf("peekNotifications after the held wake = %d, want 1 (queued, not drained)", got)
	}
}

// TestAskUser_RestoreRebuildsPendingArmsSetGoalWithoutKick covers the goal
// engine's SetGoal-idle-kick hold (spec §5.3, mirroring the live
// TestGoalHoldsAwaiting_SetGoalArmsWithoutKick): /goal issued against a
// restored session with an unanswered ask_user question must arm (store the
// goal, active) rather than kick. Before the fix, SetGoal's
// len(s.askPending)>0 read is always false post-restore, so it would kick
// immediately instead of arming.
func TestAskUser_RestoreRebuildsPendingArmsSetGoalWithoutKick(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}

	var kicked []string
	restored.SetKickFunc(func(prompt string) { kicked = append(kicked, prompt) })

	started, err := restored.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if started {
		t.Fatal("SetGoal against a restored pending ask reported started=true, want false (arm, don't kick)")
	}
	if len(kicked) != 0 {
		t.Fatalf("SetGoal against a restored pending ask must not kick, got %d kicks", len(kicked))
	}
	snap, ok := restored.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive || snap.Objective != "ship the feature" {
		t.Fatalf("SetGoal must still store the goal while a restored question is pending, got %+v ok=%v", snap, ok)
	}
}

// TestAskUser_RestoreRebuildsPendingRefusesCompact covers Compact's guard
// (spec §5.3, mirroring the live TestAskUser_CompactRefusedWhileAwaiting): a
// restored session with an unanswered ask_user question must refuse Compact
// with the same instructive error the live guard returns. Before the fix,
// Compact's askPendingCount()>0 check always reads 0 post-restore, so
// compaction would proceed and could summarize away the pending question's
// own transcript tail.
func TestAskUser_RestoreRebuildsPendingRefusesCompact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}

	if err := restored.Compact(context.Background()); err == nil || !strings.Contains(err.Error(), compactErrPending) {
		t.Fatalf("Compact against a restored pending ask err = %v, want an error containing %q", err, compactErrPending)
	}
}

// TestAskUser_RestoreRebuildsPendingCountAndOrder pins the SET's contents,
// not just its non-emptiness: two separate ask_user calls sharing one round
// (mirroring the live TestAskUser_MultipleAsksOneRound) must rebuild as a
// 2-entry pending set in call order (spec §2's "multiple ask_user calls in
// the round: union, in call order" edge case) — exercising
// questionsFromAskCalls across two tool calls in the round's assistant turn,
// not just one call's multi-question array.
func TestAskUser_RestoreRebuildsPendingCountAndOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask1 := askUserCall("ask1", askUserArgsValid()) // header "DB choice"
	ask2Args := map[string]any{
		"questions": []any{
			map[string]any{
				"header":   "Naming",
				"question": "What should we call the new package?",
				"options": []any{
					map[string]any{"label": "short names", "detail": "terse"},
					map[string]any{"label": "descriptive names", "detail": "verbose"},
				},
			},
		},
	}
	ask2 := askUserCall("ask2", ask2Args)
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask1, ask2) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "pick a db and a name", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 2 {
		t.Fatalf("pre-restore askPendingCount = %d, want 2 (test setup broken)", got)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if !restored.HasPendingAsk() {
		t.Fatal("HasPendingAsk() = false after restoring an unanswered 2-call ask round, want true")
	}
	if got := restored.askPendingCount(); got != 2 {
		t.Fatalf("restored askPendingCount = %d, want 2 (one question from each ask_user call in the round)", got)
	}
	restored.mu.Lock()
	pending := append([]askQuestion{}, restored.askPending...)
	restored.mu.Unlock()
	if len(pending) != 2 || pending[0].Header != "DB choice" || pending[1].Header != "Naming" {
		t.Fatalf("restored askPending = %+v, want [{Header:DB choice ...} {Header:Naming ...}] in call order", pending)
	}
}

// TestAskUser_RestoreGenericAwaitingKeepsPendingEmptyAndGoalKicks is the
// negative control that must stay green both before and after the fix: a
// restart after a GENERIC awaiting rest (a plain completion, no ask_user
// involved — attention-status-model v5's general inbox semantics) must never
// rebuild a pending set, and an idle /goal must still kick immediately, not
// arm — mirroring the live TestSetGoal_KicksOnPlainAwaitingRestNoPendingAsk.
// This is spec §2's "generic awaiting: no rebuild" edge case, observed
// through restore.
func TestAskUser_RestoreGenericAwaitingKeepsPendingEmptyAndGoalKicks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("here is my answer") },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("pre-restore state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q (test setup broken)", got, SessionAwaiting)
	}
	if restored.HasPendingAsk() {
		t.Fatal("HasPendingAsk() = true after restoring a GENERIC awaiting rest with no ask, want false")
	}
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored askPendingCount = %d, want 0 (a generic awaiting rest must never rebuild a pending set)", got)
	}

	var kicked []string
	restored.SetKickFunc(func(prompt string) { kicked = append(kicked, prompt) })
	started, err := restored.SetGoal(context.Background(), "ship it")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if !started {
		t.Fatal("SetGoal against a restored GENERIC awaiting rest must report started=true and kick immediately (nothing is genuinely pending)")
	}
	if len(kicked) != 1 {
		t.Fatalf("kick count = %d, want exactly 1", len(kicked))
	}
}

// TestAskUser_RestoreRebuildsPendingThenReplyClears covers the turn-entry
// clear (session_lifecycle.go:783): once a restored session with a rebuilt
// pending set receives the user's reply, the pending set must clear exactly
// as it does live (spec §5.2) — proving the rebuilt set is a real, live
// askPending and not some restore-only shadow the ordinary clear path
// forgets about.
func TestAskUser_RestoreRebuildsPendingThenReplyClears(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	meta := sess.Meta()
	sess.Close()

	restoreClient := llm.NewClient()
	restoreClient.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("thanks, using Postgres") },
		},
	})
	restored, err := RestoreSessionFromMeta(restoreClient, withTestSessionNamer(restoreClient, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored askPendingCount = %d, want 1 (test setup broken)", got)
	}

	if _, err := restored.ProcessInput(ctx, "Postgres, thanks", nil); err != nil {
		t.Fatalf("reply ProcessInput: %v", err)
	}
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount after the reply = %d, want 0 (the turn-entry clear, session_lifecycle.go:783, must still fire on a restored session)", got)
	}
}

// --- Task 10: the ask-user prompt-section gate (spec §4.5, §7) ---
//
// These tests render the system prompt directly (session_surface_behavior_test.go's
// sess.renderSystemPrompt(sess.env) pattern) rather than driving a full
// ProcessInput round trip: the gate under test is template composition, not
// turn machinery. The three cases mirror the invisibility semantics already
// proven for the tool's own registration above (TestAskUser_VisibleInteractiveRoot
// / _InvisibleNonInteractive / _InvisibleForSubagent): the guidance section
// shows exactly when ask_user is registered.

// TestAskUser_PromptSectionVisibleForInteractiveRoot covers spec §4.5: an
// interactive root session's rendered system prompt carries the ask-user
// guidance section, opening with its verbatim "Asking the user." lead-in.
func TestAskUser_PromptSectionVisibleForInteractiveRoot(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{NoProjectPrompts: true}))

	prompt, _ := sess.renderSystemPrompt(sess.env)
	if !strings.Contains(prompt, "Asking the user.") {
		t.Fatal("system prompt missing ask-user guidance for an interactive root session")
	}
}

// TestAskUser_PromptSectionHiddenWhenNonInteractive covers spec §4.5/§7: a
// NonInteractive session never registers ask_user (TestAskUser_InvisibleNonInteractive
// above), so its rendered prompt must not carry the guidance section either —
// invisible, not merely unused.
func TestAskUser_PromptSectionHiddenWhenNonInteractive(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{NoProjectPrompts: true, NonInteractive: true}))

	prompt, _ := sess.renderSystemPrompt(sess.env)
	if strings.Contains(prompt, "Asking the user.") {
		t.Fatal("system prompt contains ask-user guidance in a NonInteractive session")
	}
}

// TestAskUser_PromptSectionHiddenForSubagent covers spec §4.5/§7: a subagent
// session (live spawn carrier set, the same config shape
// TestAskUser_InvisibleForSubagent above uses) never registers ask_user, so
// its rendered prompt must not carry the guidance section.
func TestAskUser_PromptSectionHiddenForSubagent(t *testing.T) {
	t.Parallel()
	cfg := SessionConfig{NoProjectPrompts: true}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = 1
	sess := newSession(t, withConfig(cfg))

	prompt, _ := sess.renderSystemPrompt(sess.env)
	if strings.Contains(prompt, "Asking the user.") {
		t.Fatal("system prompt contains ask-user guidance in a subagent session")
	}
}

// --- Shorthand form (question + options) tests ---

// askUserArgsShorthand builds a single ask_user call using the shorthand form
// (question + options directly, not in questions array).
func askUserArgsShorthand() map[string]any {
	return map[string]any{
		"question": "Which datastore for the ingest path?",
		"options": []any{
			map[string]any{"label": "Postgres", "detail": "matches prod; heavier local setup", "recommended": true},
			map[string]any{"label": "SQLite", "detail": "zero setup; diverges from prod"},
		},
	}
}

// askUserArgsShorthandWithOptionals builds a shorthand form with all optional fields.
func askUserArgsShorthandWithOptionals() map[string]any {
	return map[string]any{
		"header":   "DB choice",
		"question": "Which datastore for the ingest path?",
		"options": []any{
			map[string]any{"label": "Postgres", "detail": "matches prod"},
			map[string]any{"label": "SQLite", "detail": "zero setup"},
		},
		"why":           "Determines the ingest pipeline dependencies.",
		"if_unanswered": "Use Postgres to match production.",
		"multi_select":  false,
	}
}

// TestAskUser_ShorthandDecodesToBatchEquivalent verifies that shorthand and
// batch forms, given identical inputs, normalize to the same internal
// representation.
func TestAskUser_ShorthandDecodesToBatchEquivalent(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	// Execute the shorthand form
	res1 := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsShorthand()))
	if res1.IsError {
		t.Fatalf("shorthand ask_user call errored: %s", res1.Output)
	}
	if res1.Output != askUserAckText {
		t.Fatalf("shorthand ack = %q, want %q", res1.Output, askUserAckText)
	}
	shorthandCount := sess.askPendingCount()

	sess.clearAskPending()

	// Execute the equivalent batch form
	batchArgs := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which datastore for the ingest path?",
				"options": []any{
					map[string]any{"label": "Postgres", "detail": "matches prod; heavier local setup", "recommended": true},
					map[string]any{"label": "SQLite", "detail": "zero setup; diverges from prod"},
				},
			},
		},
	}
	res2 := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c2", batchArgs))
	if res2.IsError {
		t.Fatalf("batch ask_user call errored: %s", res2.Output)
	}
	if res2.Output != askUserAckText {
		t.Fatalf("batch ack = %q, want %q", res2.Output, askUserAckText)
	}
	batchCount := sess.askPendingCount()

	if shorthandCount != batchCount {
		t.Fatalf("pending counts differ: shorthand=%d, batch=%d", shorthandCount, batchCount)
	}

	// Verify the pending questions are equivalent
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.askPending) < 1 {
		t.Fatal("no pending questions after batch call")
	}
	lastPending := sess.askPending[len(sess.askPending)-1]
	if lastPending.Question != "Which datastore for the ingest path?" {
		t.Fatalf("pending question = %q", lastPending.Question)
	}
}

func TestAskUser_ShorthandSurvivesSessionPrevalidation(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	res := sess.execTool(context.Background(), askUserCall("c1", askUserArgsShorthand()), "")
	if res.IsError {
		t.Fatalf("session ask_user shorthand errored: %s", res.FullOutput)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("askPendingCount = %d, want 1", got)
	}
}

// TestAskUser_ShorthandWithAllOptionals verifies shorthand form with all
// optional fields (header, why, if_unanswered, multi_select) normalizes correctly.
func TestAskUser_ShorthandWithAllOptionals(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsShorthandWithOptionals()))
	if res.IsError {
		t.Fatalf("shorthand with optionals errored: %s", res.Output)
	}
	if res.Output != askUserAckText {
		t.Fatalf("ack = %q, want %q", res.Output, askUserAckText)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.askPending) < 1 {
		t.Fatal("no pending questions after shorthand call")
	}
	if sess.askPending[0].Header != "DB choice" {
		t.Fatalf("pending header = %q, want 'DB choice'", sess.askPending[0].Header)
	}
}

// TestAskUser_ShorthandMissingOptionsErrors verifies that question without
// options produces a specific error naming the missing field.
func TestAskUser_ShorthandMissingOptionsErrors(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	// Shorthand without options
	args := map[string]any{
		"question": "Which one?",
	}

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", args))
	if !res.IsError {
		t.Fatalf("expected error for missing options, got ack: %s", res.Output)
	}

	if !strings.Contains(res.Output, "'options' is required when using the 'question' shorthand") {
		t.Fatalf("error message = %q, want to contain field-specific text about 'options' being required", res.Output)
	}
	if !strings.Contains(res.Output, "Minimal example") {
		t.Fatalf("error message = %q, want to contain 'Minimal example'", res.Output)
	}

	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (rejected call must post nothing)", got)
	}
}

// TestAskUser_BothQuestionAndQuestionsErrors verifies that supplying both
// questions and question/options produces a specific error naming the conflict.
func TestAskUser_BothQuestionAndQuestionsErrors(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "First question?",
				"options": []any{
					map[string]any{"label": "A", "detail": "Option A"},
					map[string]any{"label": "B", "detail": "Option B"},
				},
			},
		},
		"question": "Second question?",
		"options": []any{
			map[string]any{"label": "X", "detail": "Option X"},
			map[string]any{"label": "Y", "detail": "Option Y"},
		},
	}

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", args))
	if !res.IsError {
		t.Fatalf("expected error for both forms present, got ack: %s", res.Output)
	}

	if !strings.Contains(res.Output, "both 'questions' and 'question'/'options' given") {
		t.Fatalf("error message = %q, want to contain field-specific text about both forms being given", res.Output)
	}
	if !strings.Contains(res.Output, "Minimal example") {
		t.Fatalf("error message = %q, want to contain 'Minimal example'", res.Output)
	}

	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (rejected call must post nothing)", got)
	}
}

// TestAskUser_DuplicateLabelsInShorthand verifies that duplicate labels in
// shorthand form are caught with the minimal example in the error.
func TestAskUser_DuplicateLabelsInShorthand(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	args := map[string]any{
		"question": "Which one?",
		"options": []any{
			map[string]any{"label": "Same", "detail": "First"},
			map[string]any{"label": "Same", "detail": "Second"},
		},
	}

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", args))
	if !res.IsError {
		t.Fatalf("expected error for duplicate labels, got ack: %s", res.Output)
	}

	if !strings.Contains(res.Output, "option labels must be unique") {
		t.Fatalf("error message = %q, want to contain 'option labels must be unique'", res.Output)
	}
	if !strings.Contains(res.Output, "Minimal example") {
		t.Fatalf("error message = %q, want to contain 'Minimal example'", res.Output)
	}

	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (rejected call must post nothing)", got)
	}
}

// TestAskUser_NeitherFormPresentErrors verifies that omitting both questions
// and question/options produces a specific error naming the required field.
func TestAskUser_NeitherFormPresentErrors(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})

	// Empty args or args with only unrelated fields
	args := map[string]any{}

	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", args))
	if !res.IsError {
		t.Fatalf("expected error for neither form present, got ack: %s", res.Output)
	}

	if !strings.Contains(res.Output, "'questions' is required") {
		t.Fatalf("error message = %q, want to contain field-specific text about 'questions' being required", res.Output)
	}
	if !strings.Contains(res.Output, "Minimal example") {
		t.Fatalf("error message = %q, want to contain 'Minimal example'", res.Output)
	}

	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount = %d, want 0 (rejected call must post nothing)", got)
	}
}

// TestAskUser_MidRoundResolvingSteerClearingAFreshAskDoesNotForceAwaitingEarly
// covers a RoboRev #1906 round-2 Medium that measurement refuted rather than
// confirmed: the claim was that askedThisRound's per-round delta
// (askPendingCount() > askBefore) can wrongly read false when a resolving
// steer drains mid-round (clearAskPendingForResolvingSteer) after this
// round's own ask_user call appended a question, because the count "returns
// to its starting value" -- so a generation counter set at the ask_user
// append site (untouched by the clear) was proposed instead. Measured
// directly: clearAskPendingForResolvingSteer's clear is unconditional
// (s.askPending = nil, never selective) and always runs AFTER this round's
// own ToolExec (which is where ask_user appends), so whenever a mid-round
// clear fires the round's ending count is exactly 0 -- never able to land
// back on a positive askBefore -- and askedThisRound=false at that point is
// correct: nothing is left pending, so the round should continue (spec
// §5.1's "no ask, no communicate -- next round"), not stop early into a
// SessionAwaiting with an empty pending set. A generation counter divorced
// from the clear gets this exact case wrong: it flags askedThisRound=true
// off the ask_user append alone, stops the turn at this round, and forces
// SessionAwaiting though askPendingCount() is 0 -- bypassing Stop hooks
// (deliverIfCommunicated's own doc: askedThisRound "bypasses Stop hooks
// entirely") for a boundary with nothing to await. This drives a note-carrier
// round whose own model call posts a brand-new ask_user question, with a
// resolving steer queued as a side effect of that same model response so
// injectPostToolSteering drains it later in the SAME round (after the fresh
// question already appended), and asserts the turn runs a THIRD model
// request (i.e. round 1 did not stop early) with a clean, empty pending set.
func TestAskUser_MidRoundResolvingSteerClearingAFreshAskDoesNotForceAwaitingEarly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask1 := askUserCall("ask1", askUserArgsValid())
	ask2 := askUserCall("ask2", askUserArgsValid())
	var sess *Session
	queueSteer := func() {
		if err := sess.ensureClientMutationStore(); err != nil {
			t.Fatalf("ensureClientMutationStore: %v", err)
		}
		if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: "mid-flight-answer",
			Input:            clientMutationInput("use Postgres", nil, nil),
		}); err != nil {
			t.Fatalf("AcceptClientMutationSteer: %v", err)
		}
	}
	c := llm.NewClient()
	fa := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask1) },
			func(req llm.Request) llm.Response {
				queueSteer()
				return toolCallResponse(ask2)
			},
			func(req llm.Request) llm.Response { return finalResponse("using Postgres, thanks") },
		},
	}
	c.Register(fa)
	var err error
	sess, err = NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-note pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("askPendingCount after the mid-round resolving steer = %d, want 0 (the steer clears both the old ask and the one this round just posted)", got)
	}
	if got := len(fa.Requests()); got != 3 {
		t.Fatalf("model requests = %d, want 3 (the note round must not stop early; the third request reads the drained steer)", got)
	}
}

// TestAskUser_LiveStateAfterFailedHumanNoteCarrierAppendMatchesRestore covers
// a RoboRev #1907 round-2 Medium, measured real: a failed human-note
// carrier's own transcript append leaves askPending set (its entry clear
// never ran, steeringCarrierClaimAnswersAsk), but the generic non-provider
// failure tail in processOneInput's caller
// (session_lifecycle.go's "handleModelError owns terminal provider
// recovery... Keep this generic tail" branch) settled the live session
// SessionIdle unconditionally, with no regard for askPendingCount(). Restore
// of the identical transcript (TestAskUser_RestoreDoesNotResolveAcrossAFailed
// HumanNoteCarrierAppend) correctly derives SessionAwaiting. WireState (the
// externally-reported status) reads State() directly, so a live client would
// have read this session as idle with a genuinely unanswered question still
// live -- exactly the deadlock WireState's own doc warns against ("masking
// the question as working would deadlock"). Drives the real
// ProcessPendingUserInput path (not a direct acceptSteeringCarrierInput
// call, which never reaches this tail) with the note's own transcript append
// forced to fail, and asserts the LIVE session's state, not just a restored
// one.
func TestAskUser_LiveStateAfterFailedHumanNoteCarrierAppendMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}

	if _, err := sess.SetHumanNote("note-1", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	refusal := refuseSteerAppends(sess, "note-1")
	refusal.refuse.Store(true)

	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err == nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want ran=true and the injected append failure", ran, err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live askPendingCount after the failed carrier append = %d, want 1 (still unanswered)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after the failed carrier append = %q, want %q (matching restore; a pending ask must not settle idle)", got, SessionAwaiting)
	}
}

// TestAskUser_LiveStateAfterPoisonedTranscriptRefusalMatchesRestore covers a
// pending human-note carrier whose non-terminal tool result poisons the
// transcript. The next round is refused by the poisoned-transcript guard after
// a turn has already entered Processing; live state must use the same awaiting
// boundary that restore derives from the durable ask and note records.
func TestAskUser_LiveStateAfterPoisonedTranscriptRefusalMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	loop := llm.ToolCallData{ID: "loop1", Name: "loop_tool", Arguments: json.RawMessage(`{}`), Type: "function"}
	var fs *environmentSyncFailureFS
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response {
				armEnvironmentPartialWriteAfter(fs, 1)
				return toolCallResponse(loop)
			},
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.RegisterTool("loop_tool", "runs one non-terminal tool round", map[string]any{"type": "object"}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	// TRIPWIRE: scripted model and deterministic local transcript harness; this only trips on a genuine lifecycle deadlock.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-poisoned-transcript", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	fs = attachEnvironmentFailureFS(t, sess)
	_, ran, err := sess.ProcessPendingUserInput(ctx, nil)
	if !ran || !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want a poisoned-transcript refusal after the carrier round", ran, err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after poisoned-transcript refusal = %q, want %q", got, SessionAwaiting)
	}
	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state after poisoned-transcript refusal = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_LiveStateAfterFailedHumanNoteCarrierProviderErrorMatchesRestore
// covers the provider-owned terminal path: a human-note carrier preserves the
// pending ask, and handleModelError must leave the live session awaiting just
// as restore does for the same transcript shape.
func TestAskUser_LiveStateAfterFailedHumanNoteCarrierProviderErrorMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) { return toolCallResponse(ask), nil },
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "carrier provider failure", nil, nil)
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-provider-failure", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err == nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want a provider failure after the carrier ran", ran, err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after provider failure = %d, want 1 (the note does not answer ask1)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after provider failure = %q, want %q (matching restore)", got, SessionAwaiting)
	}
}

// TestAskUser_LiveStateAfterExhaustedNoToolCarrierMatchesRestore covers the
// no-tool retry terminal path: a human-note carrier preserves the pending ask,
// and exhausting bare-text retries must leave the live session awaiting.
func TestAskUser_LiveStateAfterExhaustedNoToolCarrierMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	steps := []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return toolCallResponse(ask) },
	}
	for range maxBareTextRetries + 1 {
		steps = append(steps, func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare text")} })
	}
	c := llm.NewClient()
	adapter := &fakeAdapter{name: "openai", steps: steps}
	c.Register(adapter)
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-no-tool-exhaustion", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err == nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want no-tool retry exhaustion after the carrier ran", ran, err)
	}
	if got := len(adapter.Requests()); got != maxBareTextRetries+2 {
		t.Fatalf("provider requests = %d, want %d (initial ask plus carrier and its exhausted retries)", got, maxBareTextRetries+2)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after no-tool exhaustion = %d, want 1 (the note does not answer ask1)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after no-tool exhaustion = %q, want %q (matching restore)", got, SessionAwaiting)
	}
}

// TestAskUser_LiveStateAfterToolRoundBudgetCarrierMatchesRestore covers the
// explicit MaxToolRoundsPerInput terminal boundary: a human-note carrier
// preserves the pending ask, but the tool-round exhaustion branch must leave
// the live session awaiting just as restore does for the same transcript.
func TestAskUser_LiveStateAfterToolRoundBudgetCarrierMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	loop := llm.ToolCallData{ID: "loop1", Name: "loop_tool", Arguments: json.RawMessage(`{}`), Type: "function"}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return toolCallResponse(loop) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:              dir,
		MaxToolRoundsPerInput: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.RegisterTool("loop_tool", "runs one non-terminal tool round", map[string]any{"type": "object"}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-tool-round-budget", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	_, ran, err := sess.ProcessPendingUserInput(ctx, nil)
	if !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want tool-round budget exhaustion after the carrier ran", ran, err)
	}
	requireBudgetExhaustion(t, err, exhaustedBudgetToolRounds, 1, true)
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live pending count after tool-round exhaustion = %d, want 1 (the note does not answer ask1)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after tool-round exhaustion = %q, want %q (matching restore)", got, SessionAwaiting)
	}
	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state after tool-round exhaustion = %q, want %q (live and restore must agree)", got, SessionAwaiting)
	}
}

// TestAskUser_LiveStateAfterNotificationYieldMatchesRestore covers the
// notification yield boundary: a human-note carrier leaves ask1 pending while
// a real queued job notification asks the turn loop to yield before another
// model round. The live boundary must retain the unanswered ask; restore of
// the same transcript must derive the same awaiting state.
func TestAskUser_LiveStateAfterNotificationYieldMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	taskList := llm.ToolCallData{ID: "task-list", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return toolCallResponse(taskList) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted adapter and queued notification are deterministic; this only trips on a genuine lifecycle deadlock.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-notification-yield", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	sess.enqueueJobNotification(jobNotification{JobID: "pending-notification", JobType: "shell", Status: "completed"})
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want the carrier round to yield for notification", ran, err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live askPendingCount after notification yield = %d, want 1", got)
	}
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("live pending notifications after yield = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after notification yield = %q, want %q", got, SessionAwaiting)
	}
	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored askPendingCount = %d, want 1", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state after notification yield = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_LiveStateAfterCommunicateWithPendingNotificationMatchesRestore
// covers the clean communication boundary after a human-note carrier. The
// carrier does not answer ask1, and the notification arrives while its model
// turn is completing. deliverIfCommunicated returns before the later
// notification boundary, so the final clean settle must keep the live state
// aligned with restore instead of letting autonomy mask the pending ask.
func TestAskUser_LiveStateAfterCommunicateWithPendingNotificationMatchesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	var sess *Session
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response {
				sess.enqueueJobNotification(jobNotification{JobID: "pending-notification", JobType: "shell", Status: "completed"})
				return communicateResponse(true, "note recorded")
			},
		},
	}
	c.Register(adapter)
	var err error
	sess, err = NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// TRIPWIRE: scripted provider and in-memory notification are deterministic;
	// this only fires on a genuine lifecycle deadlock.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-communicate-notification", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want the carrier communication turn", ran, err)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (the pending notification must not start an autonomous turn)", got)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live askPendingCount = %d, want 1 (the human note does not answer ask1)", got)
	}
	if got := sess.peekNotifications(); got != 1 {
		t.Fatalf("live pending notifications = %d, want 1 (the notification must remain queued)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after carrier communication = %q, want %q", got, SessionAwaiting)
	}
	if got := sess.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("live wire state after carrier communication = %q, want %q", got, SessionAwaiting)
	}

	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored askPendingCount = %d, want 1", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state = %q, want %q", got, SessionAwaiting)
	}
	if got := restored.WireState(); got != string(SessionAwaiting) {
		t.Fatalf("restored wire state = %q, want %q", got, SessionAwaiting)
	}
}

// TestAskUser_LiveStateAfterObserverYieldMatchesRestore drives a stable watch
// through the real job-manager event rail. The observer handoff arrives after
// a non-terminal tool round while ask1 remains pending; the live boundary must
// preserve the same awaiting state that restore derives from the transcript.
func TestAskUser_LiveStateAfterObserverYieldMatchesRestore(t *testing.T) {
	t.Parallel()
	fixture := newStableWatchRuntimeBase(t, nil)
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	loop := llm.ToolCallData{ID: "loop1", Name: "loop_tool", Arguments: json.RawMessage(`{}`), Type: "function"}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return toolCallResponse(loop) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Stand sess in as dlg_source's runtime the way a real delegate child is:
	// it uses the root's controller but does not own it. Releasing the
	// controller NewSession built for it, and clearing ownership, is what keeps
	// sess.Close from shutting the root's delegate tree down around itself and
	// waiting out the close budget for its own stop.
	if err := sess.closeOwnedDelegateStore(); err != nil {
		t.Fatalf("close the session's own delegate store: %v", err)
	}
	sess.ownsDelegateController = false
	sess.delegateController = fixture.controller
	sess.delegateRootSessionID = fixture.root.ID()
	sess.owningDelegateID = "dlg_source"
	sess.jobManager.delegateController = fixture.controller
	sess.jobManager.retirementOwner = sess
	fixture.controller.mu.Lock()
	fixture.controller.live["dlg_source"].runtime = sess
	fixture.controller.live["dlg_source"].binding.runtime = sess
	fixture.controller.mu.Unlock()
	if _, err := jobWatchToolWithContext(context.Background(), fixture.root, map[string]any{
		"operation": "create",
		"source":    "dlg_source",
		"events":    []any{"assistant.tool"},
	}, 4096); err != nil {
		t.Fatalf("create stable observer watch: %v", err)
	}
	sess.RegisterTool("loop_tool", "runs one non-terminal tool round", map[string]any{"type": "object"}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})

	// TRIPWIRE: scripted model and deterministic local observer harness; this only trips on a genuine lifecycle deadlock.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pre-carrier pending count = %d, want 1 (test setup broken)", got)
	}
	if _, err := sess.SetHumanNote("note-observer-yield", "watch the ingest path"); err != nil {
		t.Fatalf("SetHumanNote: %v", err)
	}
	if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput: ran=%v err=%v, want the carrier round to yield for observer delivery", ran, err)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("live askPendingCount after observer yield = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("live state after observer yield = %q, want %q", got, SessionAwaiting)
	}
	if pending := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(pending) != 0 {
		t.Fatalf("observer watch remained pending after lifecycle yield: %#v", pending)
	}
	attentionIDs, err := fixture.root.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read observer attention: %v", err)
	}
	if len(attentionIDs) == 0 {
		t.Fatal("observer attention count = 0, want at least 1")
	}
	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 1 {
		t.Fatalf("restored askPendingCount = %d, want 1", got)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored state after observer yield = %q, want %q", got, SessionAwaiting)
	}
}

// TestRoundEntryResolvesAskBoundary_SkipsBookkeepingTurnsToFindTheRealEntry
// covers a RoboRev #1907 round-2 Medium: roundEntryResolvesAskBoundary's
// backward walk only skips TurnAssistant/TurnToolResults on its way to the
// round's entry turn, so a non-decisive bookkeeping turn interleaved between
// them (TurnEnvironment, TurnNotesContext, TurnCheckpoint, TurnSummary,
// TurnModelSwitch, TurnHookCompleted, TurnSystem, or an unrelated non-carrier
// TurnFailure) is misread as the entry itself: turnResolvesAskBoundary's
// default case reports false for every one of these kinds, so the walk stops
// there and reports "did not resolve" even when the REAL entry turn, one
// step further back, is a genuine resolving TurnUserInput. The outer scan
// (deriveRestoredAskPending/deriveRestoredState) already treats these same
// kinds as transparent pass-through (turnResolvesAskBoundary's own doc: "does
// not resolve... the round it happened to may have posted real content...
// still ahead in the scan"); the entry walk must match it. Builds a minimal
// history with a TurnEnvironment turn sitting between a resolving
// TurnUserInput entry and the round's own TurnAssistant/TurnToolResults pair.
func TestRoundEntryResolvesAskBoundary_SkipsBookkeepingTurnsToFindTheRealEntry(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("which db should we use?")),
		schema.NewTurn(schema.TurnEnvironment, llm.User("cwd: /repo")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("checking the schema")),
		schema.NewTurn(schema.TurnToolResults, llm.User("tool result")),
	}
	if !roundEntryResolvesAskBoundary(history, 3, 0, nil) {
		t.Fatal("roundEntryResolvesAskBoundary = false, want true (the real entry is a resolving TurnUserInput one step past the TurnEnvironment bookkeeping turn)")
	}
}

func TestRoundEntryResolvesAskBoundary_SkipsNonCarrierFailureToFindTheRealEntry(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("which db should we use?")),
		{Kind: schema.TurnFailure, Message: llm.System("provider failed"), Error: &schema.TurnFailureInfo{Message: "provider failed"}},
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("checking the schema")),
		schema.NewTurn(schema.TurnToolResults, llm.User("tool result")),
	}
	if !roundEntryResolvesAskBoundary(history, 3, 0, nil) {
		t.Fatal("roundEntryResolvesAskBoundary = false, want true (a non-carrier TurnFailure is transparent bookkeeping before the resolving TurnUserInput entry)")
	}
}
