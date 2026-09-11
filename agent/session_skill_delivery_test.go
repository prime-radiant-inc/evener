package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// --- Dispatch-shape revalidation coverage (fix round 1, review finding I1) ---

// TestSkillDelivery_ProjectionModelSwitch drives the projection shape: a
// pending activation's carrier is projected through a mid-turn cross-provider
// model switch (the N4 replay filter), and the final admission seam revalidates
// the switched request's actual shape before committing.
func TestSkillDelivery_ProjectionModelSwitch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	primary := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		return toolCallResponse(useSkillCall("skill-1", "opaque"))
	}}
	switched := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	// A cross-provider SetModel resolves through the session resolver; without
	// it the switch silently degrades to a same-instance WithModel and the
	// committing dispatch never leaves the primary adapter.
	s := newSession(t, withAdapter(primary), withAdapter(switched), withDir(root),
		withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot(),
		withConfig(SessionConfig{
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if prefix, model, ok := strings.Cut(ref, "/"); ok && prefix == "openai" {
					return NewOpenAIProfile(model), nil
				}
				return nil, nil
			},
		}))
	// Switch the model between the tool result and the committing dispatch, so
	// the request is projected for a different provider/protocol target.
	var switchedMidTurn atomic.Bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.execToolCheckpoint = func(name string) {
			if name == "after_execute" && !switchedMidTurn.Swap(true) {
				if err := s.SetModel("openai/gpt-5.2"); err != nil {
					t.Errorf("SetModel: %v", err)
				}
			}
		}
	})
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	if !switchedMidTurn.Load() {
		t.Fatal("the mid-turn model switch never happened")
	}
	if len(primary.Requests()) != 1 || len(switched.Requests()) != 1 {
		t.Fatalf("requests: primary=%d switched=%d, want 1 each", len(primary.Requests()), len(switched.Requests()))
	}
	// The committing dispatch went to the switched provider and carries exactly
	// one complete body through the projected shape.
	if got := switched.Requests()[0].Provider; got != "openai" {
		t.Fatalf("committing request provider = %q, want the switched provider", got)
	}
	requireSingleEnvelope(t, switched.Requests()[0], "opaque", body, source)
	if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
		t.Fatalf("activation events = %v, want [opaque]", names)
	}
	if got := lifecycleInventory(s)["opaque"].Ordinary; got == nil {
		t.Fatal("projection shape did not commit the delivery")
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
}

// skillContinuationAdapter scripts Responses-protocol continuation answers at
// the in-process provider boundary: each response carries a ResponseID so the
// recorded assistant turn is an anchor candidate for the next request's delta
// planning. The plan reports the phase4d fixture fingerprints.
type skillContinuationAdapter struct {
	mu       sync.Mutex
	requests []llm.Request
	steps    []func(req llm.Request) (llm.Response, error)
	i        int
}

func (a *skillContinuationAdapter) Name() string { return "openai" }

func (a *skillContinuationAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	i := a.i
	a.i++
	a.mu.Unlock()
	if i >= len(a.steps) {
		return agenttest.FinalResponse("done"), nil
	}
	resp, err := a.steps[i](req)
	if err != nil {
		return resp, err
	}
	resp.Provider = "openai"
	if resp.Model == "" {
		resp.Model = req.Model
	}
	resp.ID = fmt.Sprintf("resp_skill_cont_%d", i)
	// Full continuation identity, mirroring the phase-4d fixture: the recorded
	// assistant turn is only an anchor candidate when ResponseID, the id hash,
	// and the endpoint all land on it (the planner anchors to the LAST
	// assistant turn and refuses planning when its metadata is missing).
	resp.Raw = map[string]any{
		"endpoint_url": "https://api.openai.com/v1/responses",
		"id_hash":      fmt.Sprintf("cont-handle-v1:response_id:skill_cont_%d", i),
	}
	return resp, nil
}

func (a *skillContinuationAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *skillContinuationAdapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	return phase4DIContinuationPlan(req), nil
}

func (a *skillContinuationAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

// newContinuationSession builds a real session with Responses continuation
// planning enabled through the phase-4d enabled-support fixture.
func newContinuationSession(t *testing.T, adapter *skillContinuationAdapter, dir string) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    t.TempDir(),
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
			// The auto planner only chooses a Responses delta when it can
			// estimate the token savings; without this hook the estimate is
			// unavailable and every request degrades to full_history.
			responsesContinuationShadowEstimateFunc: func(llm.Request) (int, bool) {
				return 1024, true
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

// TestSkillDelivery_ResponsesContinuationPlanning drives the Responses
// continuation shape: every pre-commit round plans a continuation DELTA, and
// a provisional reinvocation's obligation is revalidated against a delta that
// does not carry the old carrier, so the complete body is re-admitted through
// the typed causal notification and the rebuilt request commits through the
// same seam.
func TestSkillDelivery_ResponsesContinuationPlanning(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	adapter := &skillContinuationAdapter{}
	s := newContinuationSession(t, adapter, root)
	adapter.steps = []func(req llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			return toolCallResponse(useSkillCall("skill-1", "opaque")), nil
		},
		func(req llm.Request) (llm.Response, error) {
			return toolCallResponse(communicateCall("done-1", "ok")), nil
		},
		func(req llm.Request) (llm.Response, error) {
			return toolCallResponse(useSkillCall("skill-2", "opaque")), nil
		},
		func(req llm.Request) (llm.Response, error) {
			return toolCallResponse(communicateCall("done-2", "ok")), nil
		},
	}
	// Plant a prior anchor so every round (including the first) plans a
	// continuation delta instead of opening with full history.
	s.mu.Lock()
	s.history = append(s.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("prior user marker")),
		phase9MatchingAnchor("resp_skill_prior"),
	)
	s.mu.Unlock()
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	if got := lifecycleInventory(s)["opaque"].Ordinary; got == nil {
		t.Fatal("first activation did not commit")
	}
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	requests := adapter.Requests()
	if len(requests) != 4 {
		t.Fatalf("requests=%d", len(requests))
	}
	// Continuation planning is active: the three pre-commit rounds each
	// planned a delta against the previous response.
	for i, req := range requests[:3] {
		if req.HistoryMode != llm.HistoryModeResponsesDelta || req.PreviousResponseID == "" {
			t.Fatalf("request %d is not a continuation delta: mode=%q previous=%q", i, req.HistoryMode, req.PreviousResponseID)
		}
	}
	// The provisional result's committing dispatch: its planned delta cannot
	// carry the old carrier, so the seam reloaded the complete body through a
	// typed causal notification and rebuilt the request. Reload notification
	// turns are delta-ineligible by policy (responsesContinuationDeltaIneligibleReason
	// rejects TurnSystem deltas), so the rebuilt dispatch lands as full
	// history — pinned deliberately; a policy change that admits notification
	// turns into deltas should flip this assertion.
	last := requests[3]
	if last.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("rebuilt committing request mode=%q previous=%q, want full_history after the notification rebuild", last.HistoryMode, last.PreviousResponseID)
	}
	// The full-history rebuild replays the first activation's recorded carrier
	// alongside the reload notification: two envelopes of one identity is the
	// accepted shape here (unlike the single-carrier dispatches). Require at
	// least one, and require every envelope to deliver the complete current
	// body with canonical identity.
	envs := requestSkillEnvelopes(t, last)
	if len(envs) == 0 {
		t.Fatal("rebuilt committing request carries no skill envelope")
	}
	for _, env := range envs {
		if env.Doc.Name != "opaque" || env.Doc.Source != source || env.Doc.BaseDirectory != filepath.Dir(source) {
			t.Fatalf("envelope identity = (%q, %q, %q)", env.Doc.Name, env.Doc.Source, env.Doc.BaseDirectory)
		}
		if env.Doc.Instructions != body {
			t.Fatalf("envelope instructions are not the complete original body (%d bytes, want %d)", len(env.Doc.Instructions), len(body))
		}
	}
	if names := skillActivatedEventNames(*evs); len(names) != 1 {
		t.Fatalf("activation events = %v, want exactly one new-body event", names)
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
}

// TestSkillDelivery_FullHistoryRecovery drives the full-history recovery
// shape: the continuation request's anchor is rejected by the endpoint, so the
// session rebuilds the request from full history; the seam revalidates and
// commits against that rebuilt shape.
func TestSkillDelivery_FullHistoryRecovery(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{
			"code":    "previous_response_not_found",
			"message": "Previous response not found",
			"type":    "invalid_request_error",
		},
	}, nil)
	adapter := &skillContinuationAdapter{}
	adapter.steps = []func(req llm.Request) (llm.Response, error){
		// Round 0: the model calls use_skill.
		func(req llm.Request) (llm.Response, error) {
			return toolCallResponse(useSkillCall("skill-1", "opaque")), nil
		},
		// Round 1: the committing dispatch's anchor is rejected...
		func(req llm.Request) (llm.Response, error) { return llm.Response{}, continuationErr },
		// ...and the full-history retry answers.
		func(req llm.Request) (llm.Response, error) {
			return toolCallResponse(communicateCall("done-1", "ok")), nil
		},
	}
	s := newContinuationSession(t, adapter, root)
	// Plant a prior anchor so round 0/1 plan continuation deltas.
	s.mu.Lock()
	s.history = append(s.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("prior user marker")),
		phase9MatchingAnchor("resp_skill_prior"),
	)
	s.mu.Unlock()
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests=%d, want 3 (delta, rejected delta, full-history retry)", len(requests))
	}
	if requests[1].HistoryMode != llm.HistoryModeResponsesDelta {
		t.Fatalf("request 1 mode = %q, want continuation delta", requests[1].HistoryMode)
	}
	retry := requests[2]
	if retry.HistoryMode != llm.HistoryModeFullHistoryFallback || retry.PreviousResponseID != "" {
		t.Fatalf("recovery request mode=%q previous=%q, want full-history fallback", retry.HistoryMode, retry.PreviousResponseID)
	}
	requireSingleEnvelope(t, retry, "opaque", body, source)
	if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
		t.Fatalf("activation events = %v, want [opaque]", names)
	}
	if got := lifecycleInventory(s)["opaque"].Ordinary; got == nil {
		t.Fatal("full-history recovery did not commit the delivery")
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
}

// TestSkillDelivery_FallbackSmallerWindow drives the model-fallback shape: the
// primary model fails permanently, the chain walks to a fallback profile with
// a strictly smaller context window, and the rebuilt fallback request is
// revalidated and committed through the same seam.
func TestSkillDelivery_FallbackSmallerWindow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	body := strings.Repeat("BODY_7f2a\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	permErr := llm.ErrorFromHTTPStatus("openai", 403, "primary denied", nil, nil)
	// Round 0 must succeed on the primary (the use_skill call); the fault only
	// strikes the committing dispatch, so the chain walks primary→fallback-b
	// exactly once, on the request whose shape this test revalidates.
	var primaryCalls atomic.Int32
	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(req llm.Request) llm.Response {
			if req.Model == "primary" {
				return toolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return toolCallResponse(communicateCall("done-1", "ok"))
		},
		FaultResponder: func(req llm.Request) error {
			if req.Model == "primary" && primaryCalls.Add(1) > 1 {
				return permErr
			}
			return nil
		},
	}
	primaryProfile := WithContextWindow(NewOpenAIProfile("primary"), 200_000)
	// The fallback ref carries a different instance prefix so it resolves
	// through the session resolver (a bare "fallback-b" would WithModel-project
	// onto the primary and inherit its window); the resolver stamps a strictly
	// smaller window on the same openai instance.
	fallbackProfile := WithContextWindow(NewOpenAIProfile("fallback-b"), 64_000)
	if primaryProfile.ContextWindowSize() == 0 || fallbackProfile.ContextWindowSize() == 0 ||
		fallbackProfile.ContextWindowSize() >= primaryProfile.ContextWindowSize() {
		t.Fatalf("window fixture broken: primary=%d fallback=%d", primaryProfile.ContextWindowSize(), fallbackProfile.ContextWindowSize())
	}
	policy := llm.RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond}
	s := newSession(t, withAdapter(adapter), withDir(root),
		withProfile(primaryProfile), withoutGitSnapshot(),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			LLMRetryPolicy:   &policy,
			LLMSleep:         func(context.Context, time.Duration) error { return nil },
			ModelFallbacks:   []string{"small/fallback-b"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if prefix, model, ok := strings.Cut(ref, "/"); ok && prefix == "small" && model == "fallback-b" {
					return fallbackProfile, nil
				}
				return nil, nil
			},
		}))
	evs, stop := captureEvents(s)
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatal(err)
	}
	stop()
	// Shape proof: the use_skill round ran on the primary, the committing
	// dispatch's primary attempt failed permanently, and the fallback model
	// answered it.
	var models []string
	for _, req := range adapter.Requests() {
		models = append(models, req.Model)
	}
	if len(models) != 3 {
		t.Fatalf("requests=%d models=%v", len(models), models)
	}
	for i, want := range []string{"primary", "primary", "fallback-b"} {
		if models[i] != want {
			t.Fatalf("models=%v, want [primary primary fallback-b]", models)
		}
	}
	// The fallback's committing dispatch carried exactly one complete body.
	requireSingleEnvelope(t, adapter.Requests()[2], "opaque", body, source)
	if names := skillActivatedEventNames(*evs); len(names) != 1 || names[0] != "opaque" {
		t.Fatalf("activation events = %v, want [opaque]", names)
	}
	if got := lifecycleInventory(s)["opaque"].Ordinary; got == nil {
		t.Fatal("fallback shape did not commit the delivery")
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations outstanding after admission: %+v", got)
	}
}
