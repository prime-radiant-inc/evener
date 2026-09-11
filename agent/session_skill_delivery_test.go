package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestSkillDelivery_CompleteBody drives the tool route against the real
// session and compares the decoded instruction bytes in the second provider
// request to the original long fixture, independent of the rendering logic.
// The fixture exceeds the removed 32,000-character default tail policy.
func TestSkillDelivery_CompleteBody(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a complete delivery line\n", 2000)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	if len(adapter.Requests()) != 2 {
		t.Fatalf("requests=%d", len(adapter.Requests()))
	}
	requireSingleEnvelope(t, adapter.Requests()[1], "opaque", body, source)
}

// TestSkillDelivery_ConfiguredOutputLimit proves an explicit configured
// use_skill output limit produces a complete-delivery failure — an error
// result, a typed output_limit outcome, and no partial-success state — rather
// than truncated instructions.
func TestSkillDelivery_ConfiguredOutputLimit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			ToolOutputLimits: map[string]schema.ToolOutputLimit{
				"use_skill": {MaxChars: 16, Strategy: schema.TruncTail},
			},
		}))
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if len(adapter.Requests()) != 2 {
		t.Fatalf("requests=%d", len(adapter.Requests()))
	}
	// The model received a failure result, not partial instructions.
	errorSeen := false
	for _, msg := range adapter.Requests()[1].Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "skill-1" {
				content, _ := part.ToolResult.Content.(string)
				if part.ToolResult.IsError && !strings.Contains(content, "BODY_7f2a") {
					errorSeen = true
				}
			}
		}
	}
	if !errorSeen {
		t.Fatal("configured limit did not produce a complete-delivery failure result")
	}
	if names := skillActivatedEventNames(*evs); len(names) != 0 {
		t.Fatalf("failed delivery emitted success events: %v", names)
	}
	if got := lifecycleInventory(s); len(got) != 0 {
		t.Fatalf("failed output shaping left an inventory record: %+v", got)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("failed output shaping left a pending obligation: %+v", got)
	}
	failedOutcome := false
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.ToolCallID == "skill-1" && outcome.Status == "failed" && outcome.ErrorCode == "output_limit" {
				failedOutcome = true
			}
		}
	}
	if !failedOutcome {
		t.Fatal("no typed output_limit failure outcome recorded")
	}
}

// TestSkillDelivery_MandatoryBudgetFailure proves a request whose mandatory
// content plus current activation cannot fit the model window fails dispatch
// visibly: no truncated protected body, no success event, no inventory record.
func TestSkillDelivery_MandatoryBudgetFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a complete delivery line\n", 2000)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	// The window admits the first request but not the complete activation.
	profile := provider.WithContextWindow(newAnthropicProfile("claude-test"), 2000)
	s := newSession(t, withAdapter(adapter), withDir(root), withProfile(profile), withoutGitSnapshot())
	evs, stop := captureEvents(s)
	_, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil)
	stop()
	if err == nil && !hasEventKind(*evs, events.EventError) {
		t.Fatal("over-budget dispatch did not fail visibly")
	}
	if names := skillActivatedEventNames(*evs); len(names) != 0 {
		t.Fatalf("budget failure emitted success events: %v", names)
	}
	if got := lifecycleInventory(s); len(got) != 0 {
		t.Fatalf("budget failure recorded inventory: %+v", got)
	}
	// No request ever carried a truncated protected body.
	for _, req := range adapter.Requests() {
		for _, env := range requestSkillEnvelopes(t, req) {
			if env.Doc.Instructions != body {
				t.Fatalf("dispatch carried a truncated body (%d of %d bytes)", len(env.Doc.Instructions), len(body))
			}
		}
	}
}

// TestSkillDelivery_FailedReinvocationPreservesInventory deletes the source
// after a successful activation: the failed reinvocation records a typed
// failure and the earlier successful inventory entry survives unchanged.
func TestSkillDelivery_FailedReinvocationPreservesInventory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls%2 == 1 {
			return toolCallResponse(useSkillCall("skill-"+strconv.Itoa((calls+1)/2), "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	before := lifecycleInventory(s)["opaque"]
	if before.Ordinary == nil {
		t.Fatal("first activation recorded no inventory")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	after := lifecycleInventory(s)["opaque"]
	if after.Ordinary == nil || *after.Ordinary != *before.Ordinary {
		t.Fatalf("failed reinvocation changed the earlier record: before=%+v after=%+v", before.Ordinary, after.Ordinary)
	}
	failedOutcome := false
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.Status == "failed" && outcome.ErrorCode == "source_missing" {
				failedOutcome = true
			}
		}
	}
	if !failedOutcome {
		t.Fatal("failed reinvocation recorded no typed source_missing outcome")
	}
	if names := skillActivatedEventNames(*evs); len(names) != 1 {
		t.Fatalf("activation events = %v, want exactly the first delivery", names)
	}
}

// TestSkillDelivery_PreDispatchCompaction proves a new activation's carrier
// survives a real pre-dispatch compaction that keeps it in the retained tail:
// the dispatch still carries exactly one complete current body.
func TestSkillDelivery_PreDispatchCompaction(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12)
	// Fold after the activation's tool call executed, before the next dispatch.
	var folded atomic.Bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.execToolCheckpoint = func(name string) {
			if name == "after_execute" && !folded.Swap(true) {
				if err := s.Compact(context.Background()); err != nil {
					t.Errorf("pre-dispatch compaction: %v", err)
				}
			}
		}
	})
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if !folded.Load() {
		t.Fatal("the pre-dispatch compaction never ran")
	}
	if len(adapter.Requests()) != 2 {
		t.Fatalf("requests=%d", len(adapter.Requests()))
	}
	requireSingleEnvelope(t, adapter.Requests()[1], "opaque", body, source)
	if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
		t.Fatalf("activation events = %v, want [opaque]", names)
	}
}

// TestSkillDelivery_RevalidateAfterFold is the dedupe-revalidation contract:
// a first complete activation, a second use_skill with a provisional
// already-present result, then a real fold that removes the old carrier
// before the next dispatch. The next real provider request must carry exactly
// one complete current body through a typed causal notification, and the
// provisional outcome is corrected without a second new-body event.
func TestSkillDelivery_RevalidateAfterFold(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		switch {
		case calls%2 == 1:
			return toolCallResponse(useSkillCall("skill-"+strconv.Itoa((calls+1)/2), "opaque"))
		default:
			return toolCallResponse(communicateCall("done-1", "ok"))
		}
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 2
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	if got := lifecycleInventory(s)["opaque"].Ordinary; got == nil {
		t.Fatal("first activation did not commit")
	}
	// The second use_skill hits the provisional already-present path.
	var folded atomic.Bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.execToolCheckpoint = func(name string) {
			if name == "after_execute" && !folded.Swap(true) {
				if err := s.Compact(context.Background()); err != nil {
					t.Errorf("carrier-removing fold: %v", err)
				}
			}
		}
	})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if !folded.Load() {
		t.Fatal("the carrier-removing fold never ran")
	}
	if len(adapter.Requests()) != 4 {
		t.Fatalf("requests=%d", len(adapter.Requests()))
	}
	// The provisional result carried a brief notice, not a second body copy.
	var provisional string
	for _, msg := range adapter.Requests()[3].Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "skill-2" {
				provisional, _ = part.ToolResult.Content.(string)
			}
		}
	}
	if strings.Contains(provisional, "<skill-context>") {
		t.Fatal("provisional already-present result repeated the body")
	}
	// The fold removed the old carrier; the final dispatch re-admitted exactly
	// one complete current body.
	requireSingleEnvelope(t, adapter.Requests()[3], "opaque", body, source)
	// The corrected outcome links the original second invocation.
	var secondInvocation string
	for _, state := range skillTurnStates(s) {
		for _, obligation := range state.Obligations {
			if obligation.ToolCallID == "skill-2" {
				secondInvocation = obligation.InvocationID
			}
		}
	}
	if secondInvocation == "" {
		t.Fatal("provisional activation recorded no typed obligation")
	}
	corrected := false
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.InvocationID == secondInvocation && outcome.Status == "delivered" {
				corrected = true
			}
		}
	}
	if !corrected {
		t.Fatal("no causal corrected outcome for the revalidated delivery")
	}
	if names := skillActivatedEventNames(*evs); len(names) != 1 {
		t.Fatalf("activation events = %v, want exactly one new-body event", names)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
}

// TestSkillDelivery_ChangedSourceBeforeDispatch changes the source between the
// provisional result and the dispatch with the old carrier folded away: the
// revalidation delivers the complete CURRENT content with an explicit change
// notice, never a false already-present delivery of stale bytes.
func TestSkillDelivery_ChangedSourceBeforeDispatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	oldBody := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+oldBody)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	newBody := strings.Repeat("BODY_9c1e changed\n", 64)
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls%2 == 1 {
			return toolCallResponse(useSkillCall("skill-"+strconv.Itoa((calls+1)/2), "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 2
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	// Change the source and remove the old carrier between the provisional
	// result and the next dispatch.
	var swapped atomic.Bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.execToolCheckpoint = func(name string) {
			if name == "after_execute" && !swapped.Swap(true) {
				if err := os.WriteFile(source, []byte("---\nname: opaque\ndescription: fixture\n---\n"+newBody), 0o644); err != nil {
					t.Errorf("rewrite source: %v", err)
				}
				if err := s.Compact(context.Background()); err != nil {
					t.Errorf("carrier-removing fold: %v", err)
				}
			}
		}
	})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if !swapped.Load() {
		t.Fatal("the source change never landed")
	}
	if len(adapter.Requests()) != 4 {
		t.Fatalf("requests=%d", len(adapter.Requests()))
	}
	// The final dispatch carries exactly one complete CURRENT body.
	delivered := requireSingleEnvelope(t, adapter.Requests()[3], "opaque", newBody, source)
	renderedHash := sha256.Sum256([]byte(delivered.Raw))
	// The corrected outcome records the changed identity (and its notice).
	corrected := false
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.Status == "delivered" && outcome.Identity.RenderedDigest == hex.EncodeToString(renderedHash[:]) {
				corrected = true
			}
		}
	}
	if !corrected {
		t.Fatal("no corrected outcome recorded the changed content")
	}
	ordinary := lifecycleInventory(s)["opaque"].Ordinary
	if ordinary == nil || ordinary.Identity.RenderedDigest != hex.EncodeToString(renderedHash[:]) {
		t.Fatalf("inventory did not advance to the changed content: %+v", ordinary)
	}
	if names := skillActivatedEventNames(*evs); len(names) != 2 {
		t.Fatalf("activation events = %v, want one per genuinely new body", names)
	}
}

// TestSkillDelivery_DeletedSourceBeforeDispatch deletes the source between the
// provisional result and the dispatch with the old carrier folded away: the
// revalidation fails explicitly with a typed outcome — never a false delivery.
func TestSkillDelivery_DeletedSourceBeforeDispatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls%2 == 1 {
			return toolCallResponse(useSkillCall("skill-"+strconv.Itoa((calls+1)/2), "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 2
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	before := lifecycleInventory(s)["opaque"]
	var removed atomic.Bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.execToolCheckpoint = func(name string) {
			if name == "after_execute" && !removed.Swap(true) {
				if err := os.Remove(source); err != nil {
					t.Errorf("remove source: %v", err)
				}
				if err := s.Compact(context.Background()); err != nil {
					t.Errorf("carrier-removing fold: %v", err)
				}
			}
		}
	})
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if !removed.Load() {
		t.Fatal("the source removal never landed")
	}
	if len(adapter.Requests()) != 4 {
		t.Fatalf("requests=%d", len(adapter.Requests()))
	}
	// No false delivery: the final request carries no envelope for the deleted
	// source, and the typed failure supersedes the provisional outcome.
	if envs := requestSkillEnvelopes(t, adapter.Requests()[3]); len(envs) != 0 {
		t.Fatalf("deleted source delivered %d envelopes", len(envs))
	}
	failedOutcome := false
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.Status == "failed" && outcome.ErrorCode == "source_missing" {
				failedOutcome = true
			}
		}
	}
	if !failedOutcome {
		t.Fatal("no typed source_missing failure outcome for the deleted source")
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("failed delivery left obligations: %+v", got)
	}
	// The earlier successful record survives; no new success event fired.
	if after := lifecycleInventory(s)["opaque"]; after.Ordinary == nil || *after.Ordinary != *before.Ordinary {
		t.Fatalf("failed reload changed the earlier record: %+v", after.Ordinary)
	}
	if names := skillActivatedEventNames(*evs); len(names) != 1 {
		t.Fatalf("activation events = %v, want exactly the first delivery", names)
	}
}

// TestSkillDelivery_TransportRetry proves a transport retry after the final
// admission does not double-commit: the retried dispatch carries the same
// complete body and no duplicate success event fires.
func TestSkillDelivery_TransportRetry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	var failed atomic.Bool
	dispatches := 0
	adapter := &agenttest.ScriptedAdapter{
		Provider: "anthropic",
		Responder: func(llm.Request) llm.Response {
			calls++
			if calls == 1 {
				return toolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		},
		FaultResponder: func(llm.Request) error {
			// FaultResponder runs before Responder for each dispatch; fail the
			// first dispatch of the second round (the delivery commit's
			// request), once.
			dispatches++
			if dispatches == 2 && !failed.Swap(true) {
				return llm.ErrorFromHTTPStatus("anthropic", 429, "rate limited", nil, nil)
			}
			return nil
		},
	}
	policy := llm.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			LLMRetryPolicy:   &policy,
			LLMSleep:         func(context.Context, time.Duration) error { return nil },
		}))
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if !failed.Load() {
		t.Fatal("the transport failure never fired")
	}
	if len(adapter.Requests()) != 3 {
		t.Fatalf("requests=%d, want 2 rounds plus one retried dispatch", len(adapter.Requests()))
	}
	// Both dispatch attempts of round 2 carried the complete body.
	requireSingleEnvelope(t, adapter.Requests()[1], "opaque", body, source)
	requireSingleEnvelope(t, adapter.Requests()[2], "opaque", body, source)
	if names := skillActivatedEventNames(*evs); len(names) != 1 {
		t.Fatalf("activation events = %v, want exactly one despite the retry", names)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("retry left obligations: %+v", got)
	}
}

// TestSkillDelivery_SaveRestore proves delivery receipts reconcile across a
// restart: the restored session retains the inventory and the complete
// content in its replayed history, and a further dispatch emits no duplicate
// success.
func TestSkillDelivery_SaveRestore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	stateDir := t.TempDir()
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
		withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: stateDir}))
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	saved, err := schema.LoadSessionMeta(stateDir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	restored, err := RestoreSessionFromMeta(s.Client(), s.Profile(), execenv.NewLocalExecutionEnvironment(root), saved, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	entry := lifecycleInventory(restored)["opaque"]
	if entry.Ordinary == nil || entry.Ordinary.Route != "model_tool" || entry.Ordinary.UserAuthorized {
		t.Fatalf("restored inventory = %+v", entry.Ordinary)
	}
	if got := lifecycleObligations(restored); len(got) != 0 {
		t.Fatalf("restored obligations = %+v, want none after the committed delivery", got)
	}
	evs, stop := captureEvents(restored)
	if _, err := restored.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	// The replayed carrier still proves complete retained content on the
	// restored dispatch, and receipt reconciliation emits no duplicate success.
	last := adapter.Requests()[len(adapter.Requests())-1]
	requireSingleEnvelope(t, last, "opaque", body, source)
	if names := skillActivatedEventNames(*evs); len(names) != 0 {
		t.Fatalf("restored dispatch re-emitted success: %v", names)
	}
}

// TestSkillDelivery_FrozenPlusOrdinary proves a name holding both a frozen
// role preload and an ordinary activation keeps both provenances in its
// inventory entry: the preload record is not rewritten by the ordinary
// activation.
func TestSkillDelivery_FrozenPlusOrdinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			return toolCallResponse(useSkillCall("skill-1", "opaque"))
		}
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
	s.pluginAgents["preload-role"] = plugin.Agent{
		Name: "preload-role", PluginName: "test", AllTools: true,
		SystemPrompt: "preload role", Skills: []string{"opaque"},
	}
	result, err := s.spawnAgent(context.Background(), "child task", "", "", 0, "preload-role", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	var spawned struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(result.(string)), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	sub := s.getSub(spawned.AgentID)
	if sub == nil {
		t.Fatalf("subagent %q not tracked", spawned.AgentID)
	}
	select {
	case <-sub.done:
	case <-time.After(30 * time.Second): // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
		t.Fatal("timed out waiting for child")
	}
	child := sub.sess
	entry := lifecycleInventory(child)["opaque"]
	if entry.Preload == nil || entry.Ordinary == nil {
		t.Fatalf("same-name entry must hold both provenances: %+v", entry)
	}
	if entry.Preload.Source != source || entry.Preload.RenderedDigest == "" {
		t.Fatalf("preload provenance = %+v", entry.Preload)
	}
	if entry.Ordinary.Route != "model_tool" || entry.Ordinary.InvocationID == "" {
		t.Fatalf("ordinary provenance = %+v", entry.Ordinary)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	fileHash := sha256.Sum256(data)
	if entry.Preload.FileDigest != hex.EncodeToString(fileHash[:]) || entry.Ordinary.Identity.FileDigest != hex.EncodeToString(fileHash[:]) {
		t.Fatal("provenance digests diverge from the fixture bytes")
	}
}
