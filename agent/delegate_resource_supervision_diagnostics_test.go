package agent

import (
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// The diagnostic must distinguish a durable pending entry from a finished warm
// run, and must not hang behind the very locks a failed run may hold.
func TestStableSupervisionFailureSnapshotCapturesPendingAttention(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	const attentionID = "attention:diagnostic-pending"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "diagnostic-sentinel"); err != nil || !appended {
		t.Fatalf("append diagnostic attention = %t, %v", appended, err)
	}
	snapshot := stableSupervisionFailureSnapshot(root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["pending_attention_ids"], []string{attentionID}) || snapshot["current_run_open"] != false || snapshot["done_closed"] != true {
		t.Fatalf("snapshot omitted strict pending predicate or finished warm channel: %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["blocking_predicates"], []string{"pending_attention_ids"}) {
		t.Fatalf("snapshot blocking predicates = %#v, want only durable pending attention", snapshot["blocking_predicates"])
	}
	for _, key := range []string{"arm_retry_ids", "attention_reservations", "controller_finalizing", "running", "driving", "subagent_finalizing", "done_present", "attention_resolutions", "attention_resume_generations", "settlement_claims", "pending_notifications", "notify_callback"} {
		if _, ok := snapshot[key]; !ok {
			t.Errorf("snapshot omitted %q", key)
		}
	}
	if snapshot["provider_cursor"] != 1 || snapshot["provider_steps"] != 1 {
		t.Fatalf("snapshot omitted scripted provider cursor: %#v", snapshot)
	}
	requests, ok := snapshot["provider_requests"].([]llm.Request)
	if !ok || len(requests) != 1 || !requestMessagesContainText(requests[0].Messages, "warm retained runtime") {
		t.Fatalf("snapshot omitted actual provider request: %#v", snapshot)
	}
	if stacks, ok := snapshot["goroutine_stacks"].(string); !ok || !strings.Contains(stacks, "TestStableSupervisionFailureSnapshotCapturesPendingAttention") {
		t.Fatalf("snapshot omitted relevant goroutine stacks: %#v", snapshot)
	}

	t.Run("contended locks remain observable without blocking", func(t *testing.T) {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		root.delegateController.mu.Lock()
		defer root.delegateController.mu.Unlock()
		sub.sess.attentionMu.Lock()
		defer sub.sess.attentionMu.Unlock()
		sub.sess.mu.Lock()
		defer sub.sess.mu.Unlock()
		sub.sess.pendingJobNotifsMu.Lock()
		defer sub.sess.pendingJobNotifsMu.Unlock()
		fixture.adapter.mu.Lock()
		defer fixture.adapter.mu.Unlock()
		snapshot := stableSupervisionFailureSnapshot(root, sub, fixture.adapter)
		for _, key := range []string{"subagent_lock_busy", "controller_lock_busy", "attention_lock_busy", "session_lock_busy", "notification_lock_busy", "provider_lock_busy"} {
			if snapshot[key] != true {
				t.Errorf("snapshot[%q] = %#v, want contended lock", key, snapshot[key])
			}
		}
	})
}

func stableSupervisionFailureSnapshot(root *Session, sub *subagent, adapter *fakeAdapter) map[string]any {
	// This is a best-effort sequential snapshot, not an atomic lifecycle view.
	// Never wait for a runtime lock or hold one while reading another source.
	snapshot := map[string]any{"child_id": sub.sess.id, "delegate_id": sub.sess.owningDelegateID}
	if sub.mu.TryLock() {
		snapshot["running"], snapshot["driving"], snapshot["subagent_finalizing"] = sub.running, sub.driving, sub.finalizing
		snapshot["closed"], snapshot["settlement_claimed"] = sub.closed, sub.settlementClaimed
		snapshot["done_present"] = sub.done != nil
		snapshot["done_closed"] = supervisionChannelClosed(sub.done)
		sub.mu.Unlock()
	} else {
		snapshot["subagent_lock_busy"] = true
	}
	sess := sub.sess
	if sess.attentionMu.TryLock() {
		ids := make([]string, 0, len(sess.delegateAttentionArmIDs))
		for id := range sess.delegateAttentionArmIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		snapshot["arm_retry_ids"] = ids
		sess.attentionMu.Unlock()
	} else {
		snapshot["attention_lock_busy"] = true
	}
	if sess.mu.TryLock() {
		snapshot["notify_callback"] = fmt.Sprintf("%p", sess.notifyFunc)
		snapshot["turn_started_at"] = sess.turnStartedAt
		sess.mu.Unlock()
	} else {
		snapshot["session_lock_busy"] = true
	}
	if sess.pendingJobNotifsMu.TryLock() {
		snapshot["pending_notifications"] = append([]jobNotification(nil), sess.pendingJobNotifs...)
		sess.pendingJobNotifsMu.Unlock()
	} else {
		snapshot["notification_lock_busy"] = true
	}
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, sess.id), sess.id)
	if err != nil {
		snapshot["attention_fold_error"] = err.Error()
	} else {
		snapshot["pending_attention_ids"] = fold.pendingIDs()
		snapshot["attention_resolutions"] = fold.resolutions
		snapshot["attention_resume_generations"] = fold.resumeGenerations
	}
	c := root.delegateController
	if c.mu.TryLock() {
		if aggregate := c.durable[sess.owningDelegateID]; aggregate != nil {
			snapshot["generation"], snapshot["phase"] = aggregate.Generation, aggregate.Phase
			snapshot["current_run_open"], snapshot["needs_attention"] = aggregate.CurrentRunOpen, aggregate.NeedsAttention
			snapshot["pending_deliveries"] = len(aggregate.PendingDeliveries)
		}
		if live := c.live[sess.owningDelegateID]; live != nil {
			snapshot["controller_finalizing"] = live.finalizing != nil
			if live.finalizing != nil {
				snapshot["finalizing_generation"] = live.finalizing.generation
				snapshot["finalization_released"] = supervisionChannelClosed(live.finalizing.released)
			}
			if binding := live.binding; binding != nil {
				snapshot["binding_generation"], snapshot["binding_ready"] = binding.lease.generation, binding.ready
				if evidence := binding.evidence; evidence != nil {
					snapshot["completion_requirement"], snapshot["completion_outcome"], snapshot["terminal_seen"] = evidence.requirement, evidence.outcome, evidence.terminalSeen
				}
			}
		}
		reservations := []map[string]any{}
		for _, record := range c.reservations {
			if record.delegateID == sess.owningDelegateID && record.trigger == delegatestore.TriggerAttention && record.runtime == sess && record.attentionID != "" {
				reservations = append(reservations, map[string]any{"generation": record.generation, "attention_id": record.attentionID, "admitted": record.attentionAdmitted, "resolution_ready": record.attentionResolutionReady})
			}
		}
		snapshot["attention_reservations"] = reservations
		claims := []map[string]any{}
		for _, claim := range c.settlementClaims {
			if claim.lease.delegateID == sess.owningDelegateID {
				claims = append(claims, map[string]any{"generation": claim.lease.generation, "ready": supervisionChannelClosed(claim.ready), "mode": claim.mode})
			}
		}
		snapshot["settlement_claims"] = claims
		c.mu.Unlock()
	} else {
		snapshot["controller_lock_busy"] = true
	}
	if adapter != nil {
		// fakeAdapter holds this mutex across a scripted provider step. Blocking
		// Requests() here would hide a provider stuck inside that step.
		if adapter.mu.TryLock() {
			snapshot["provider_cursor"], snapshot["provider_steps"] = adapter.i, len(adapter.steps)
			snapshot["provider_requests"] = append([]llm.Request(nil), adapter.requests...)
			adapter.mu.Unlock()
		} else {
			snapshot["provider_lock_busy"] = true
		}
	}
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	stacks := []string{}
	for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(stack, "primeradiant.com/evener/agent.") {
			stacks = append(stacks, stack)
		}
	}
	snapshot["goroutine_stacks"] = strings.Join(stacks, "\n\n")
	snapshot["goroutine_stacks_truncated"] = n == len(buf)
	blockers := []string{}
	for _, key := range []string{"running", "driving", "subagent_finalizing", "current_run_open", "controller_finalizing"} {
		if snapshot[key] == true {
			blockers = append(blockers, key)
		}
	}
	for _, key := range []string{"arm_retry_ids", "pending_attention_ids"} {
		if ids, ok := snapshot[key].([]string); ok && len(ids) != 0 {
			blockers = append(blockers, key)
		}
	}
	if records, ok := snapshot["attention_reservations"].([]map[string]any); ok && len(records) != 0 {
		blockers = append(blockers, "attention_reservations")
	}
	if snapshot["done_present"] == false || snapshot["done_closed"] == false {
		blockers = append(blockers, "completion_channel")
	}
	if snapshot["attention_fold_error"] != nil {
		blockers = append(blockers, "attention_fold_error")
	}
	snapshot["blocking_predicates"] = blockers
	return snapshot
}

func supervisionChannelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
