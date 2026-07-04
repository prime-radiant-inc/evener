package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
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
