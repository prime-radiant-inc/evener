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

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// TestAskUser_EntryGateRefusesWatchDeliveryWake covers spec §5.3's entry gate for
// EntryWatchDelivery: refused before any state transition, state unchanged
// throughout, no model request made.
func TestAskUser_EntryGateRefusesWatchDeliveryWake(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a model call: the entry gate must refuse EntryWatchDelivery while awaiting")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state before entry-gate probe = %q, want %q", got, SessionAwaiting)
	}

	out, err := sess.ProcessInputKind(ctx, "a delegate delivered a watch frame", nil, EntryWatchDelivery)
	if err != nil {
		t.Fatalf("ProcessInputKind(EntryWatchDelivery) while awaiting returned an error: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("ProcessInputKind(EntryWatchDelivery) while awaiting returned %q, want empty (no-op)", out)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after refused watch-delivery wake = %q, want %q (must not flip)", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests after refused watch-delivery wake = %d, want 1 (no model call)", got)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			sess.FollowUp("investigate the flaky test next")
			return "queued", nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
// agent/session_compaction.go's Compact — the one cmd/serf/serve.go's
// SetCompactFunc calls. Clear has no in-place Session method at all: /clear
// (serve.go SetClearFunc) constructs a brand-new agent.NewSession with
// SessionStartKind=Clear and swaps it in for the live session, Closing the
// old one afterward — so "Clear while awaiting" is a fresh, always-idle,
// always-empty-pending session by construction, not a state transition on the
// awaiting session itself. The tests below cover Compact's new guard directly
// and reproduce Clear's actual replace-not-reset mechanism at the session
// level; real end-to-end proof that the daemon's /clear surface reaches this
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
// production mechanism (cmd/serf/serve.go SetClearFunc) rather than inventing
// an in-place reset that does not exist: an old session is driven into
// SessionAwaiting with a real pending question, then a replacement is built
// exactly as SetClearFunc builds one — same client/profile/env, cfg with only
// SessionStartKind flipped to Clear, constructed WHILE the old session is
// still open (serve.go closes the old session only after the swap, so this
// test preserves that ordering). The replacement is what the user talks to
// after /clear; it is idle with an empty pending set regardless of what the
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	// re-derived as pending. askPending is never restored (it is not part of
	// persisted meta), so checking it here would trivially read 0 regardless
	// of transcript shape; the meaningful check already ran live above.
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

// --- Task 10: the ask-user prompt-section gate (spec §4.5, §7) ---
//
// These tests render the system prompt directly (session_behavior_tag_test.go's
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

	prompt := sess.renderSystemPrompt(sess.env)
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

	prompt := sess.renderSystemPrompt(sess.env)
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

	prompt := sess.renderSystemPrompt(sess.env)
	if strings.Contains(prompt, "Asking the user.") {
		t.Fatal("system prompt contains ask-user guidance in a subagent session")
	}
}
