package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
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
	if !res.IsError || res.Output != "ask_user: option labels must be unique within a question" {
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
	if !res.IsError || res.Output != "ask_user: at most one option may be recommended" {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want %q", got, SessionIdle)
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
		// Matcher scoped to ask_user's Claude-visible name (toolname.SerfToClaude)
		// so the deny does not also swallow round 2's communicate call.
		Matcher: "AskUserQuestion",
		Type:    "command",
		Command: `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no interactive user right now"}}'`,
		Timeout: 5,
	})
	sess.hookRunner = runner

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want %q (a deny alone must not end the turn awaiting)", got, SessionIdle)
	}
}
