package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// installSessionEndMarkerHook registers a matching SessionEnd hook that touches
// marker, and returns marker. A residency-only release must leave it absent.
func installSessionEndMarkerHook(t *testing.T, s *Session) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "session-end-fired")
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookSessionEnd, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 5,
		Command: "touch " + shellquote.Literal(marker),
	})
	s.hookRunner = runner
	return marker
}

// TestCapacityReclaim_IsResidencyOnly pins the unified capacity policy: a
// max_retained_terminal admission reclaims capacity by unloading the resident
// runtime, not by terminating the delegate. The runtime is unhooked and its
// exact live pointer cleared, the durable aggregate stays idle and resumable,
// no SessionEnd hook runs, no session_closed boundary is emitted, and the
// session is left in its non-closed state.
func TestCapacityReclaim_IsResidencyOnly(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	c.maxRetainedTerminal = 1

	runtime := newQueuePersistTestSession(t, t.TempDir())
	seedDelegateReclaimRuntimeSession(t, c, "dlg_old", "", time.Unix(5, 0).UTC(), false, false, runtime)

	root := newQueuePersistTestSession(t, t.TempDir())
	root.delegateController = c
	c.rootRuntime = root

	marker := installSessionEndMarkerHook(t, runtime)

	collected := make(chan []events.SessionEvent, 1)
	go func() {
		var kinds []events.SessionEvent
		for ev := range runtime.Events() {
			kinds = append(kinds, ev)
		}
		collected <- kinds
	}()

	if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
		t.Fatalf("reclaimDelegateRuntimeCapacity: %v", err)
	}

	// The resident runtime is unloaded from memory: the exact live pointer is
	// cleared and the claim is settled.
	c.mu.Lock()
	live := c.live["dlg_old"]
	fenced := c.reclaiming["dlg_old"] != 0
	aggregate := c.durable["dlg_old"]
	c.mu.Unlock()
	if live != nil && live.runtime != nil {
		t.Fatalf("capacity reclamation left the resident runtime published: %p", live.runtime)
	}
	if fenced {
		t.Fatal("capacity reclamation left its claim fence installed")
	}

	// Durable delegate state survives as idle and resumable.
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable {
		t.Fatalf("reclaimed delegate durable state = %+v, want idle and resumable", aggregate)
	}

	// No terminal effects: no session_closed boundary, no SessionEnd hook, and
	// the session is not closed.
	kinds := <-collected
	if n := countKind(kinds, events.EventSessionEnd); n != 0 {
		t.Fatalf("residency-only reclamation emitted %d session end event(s): %v", n, kinds)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("residency-only reclamation ran the SessionEnd hook")
	}
	if got := runtime.State(); got == SessionClosed {
		t.Fatal("residency-only reclamation closed the session")
	}
	// The teardown pass is spent, proving the release really ran rather than
	// being skipped.
	if err := runtime.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); !errors.Is(err, errRetirementTeardownSpent) {
		t.Fatalf("runtime teardown after reclamation = %v, want errRetirementTeardownSpent", err)
	}
}

// TestCapacityReclaim_KeepsDelegateResumable proves the released delegate is
// still drivable: a delegate_send after capacity reclamation cold-restores a
// fresh runtime and completes the follow-up turn.
func TestCapacityReclaim_KeepsDelegateResumable(t *testing.T) {
	t.Parallel()
	root, tree, _ := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	original := tree.residentDelegateRuntime(d.DelegateID)
	if original == nil {
		t.Fatal("fixture: delegate has no resident runtime")
	}

	tree.maxRetainedTerminal = 1
	if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
		t.Fatalf("reclaimDelegateRuntimeCapacity: %v", err)
	}
	if got := tree.residentDelegateRuntime(d.DelegateID); got != nil {
		t.Fatalf("capacity reclamation kept runtime %p resident", got)
	}
	if got := root.subagents.get(d.ChildSessionID); got != nil {
		t.Fatal("capacity reclamation left the released runtime in the root manager")
	}

	root.client.Register(newFakeAdapter("openai", func(llm.Request) llm.Response {
		return agenttest.FinalResponse("resumed-after-reclaim")
	}))
	outcome := (delegateRuntime{owner: root}).send(context.Background(), d.DelegateID, "followup-after-reclaim", 0)
	if outcome.result.Err != nil {
		t.Fatalf("delegate_send after capacity reclamation: %v", outcome.result.Err)
	}
	restored := tree.residentDelegateRuntime(d.DelegateID)
	if restored == nil || restored == original {
		t.Fatalf("delegate_send runtime = %p, want a fresh cold-restored runtime distinct from %p", restored, original)
	}
}

// TestCapacityReclaim_RefusesSharedTaskStoreOwner: residency-only release keeps
// every member resumable, so a subtree owning a shared task store must be
// refused exactly as the idle release refuses it. The owner stays resident and
// admission fails loudly instead of stranding the resolver's next restore.
func TestCapacityReclaim_RefusesSharedTaskStoreOwner(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.maxRetainedTerminal = 1
	seedDelegateReclaimRuntime(t, c, "dlg_owner", "", time.Unix(10, 0).UTC(), false, false)
	seedDelegateReclaimRuntime(t, c, "dlg_child", "dlg_owner", time.Unix(5, 0).UTC(), false, false)
	c.mu.Lock()
	c.durable["dlg_child"].Descriptor.SharedTaskStoreOwnerSessionID = c.durable["dlg_owner"].Descriptor.ChildSessionID
	c.mu.Unlock()

	if _, err := c.ClaimRuntimeReclamation(1); err == nil {
		t.Fatal("capacity reclamation claimed a subtree owning a shared task store")
	}
	c.mu.Lock()
	ownerRuntime := c.live["dlg_owner"].runtime
	c.mu.Unlock()
	if ownerRuntime == nil {
		t.Fatal("refused claim released the shared task store owner's runtime")
	}
}
