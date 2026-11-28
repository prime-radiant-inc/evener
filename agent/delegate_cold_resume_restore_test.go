package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestColdResumeRestoresAReachabilityExcludedToolOnTheChild pins that a cold
// restore judges the child against the ceiling it was BUILT with.
//
// restoreIdle hands the child only the frozen ceiling; the child's NewSession
// widens it back with effectiveChildToolCeiling from its own registrations. A
// restore that then validated those registrations against the bare frozen list
// would discard exactly the names that widening restored, so the construction
// path would re-admit a reachability-excluded name and the cold path would throw
// it away again.
//
// The fixture uses "list_dir": a normal session tool that the frozen ceiling
// excludes for reachability only, so a child honoring the bare frozen list cannot
// hold it.
func TestColdResumeRestoresAReachabilityExcludedToolOnTheChild(t *testing.T) {
	t.Parallel()

	const (
		recovered  = "list_dir"   // reachability-excluded at creation, registrable now
		roleDenied = "job_status" // never in the ceiling and never registered
		ordinary   = "read_file"  // allowed and registered
	)

	restore := func(t *testing.T, reachabilityExcluded []string) (*subagent, error) {
		t.Helper()
		fixture := newColdDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
			descriptor.ToolNameCeiling = []string{"communicate", ordinary}
			descriptor.CeilingReachabilityExcluded = reachabilityExcluded
		})
		root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(
			fixture.client,
			fixture.profile,
			fixture.workspace,
			fixture.meta,
			fixture.stateDir,
			testConfig{},
		)
		if err != nil {
			t.Fatalf("restore root: %v", err)
		}
		t.Cleanup(root.Close)
		reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
		if err != nil {
			t.Fatalf("ReserveStart: %v", err)
		}
		started, err := root.delegateController.CommitStart(reservation)
		if err != nil {
			t.Fatalf("CommitStart: %v", err)
		}
		sub, restored, restoreErr := (delegateRuntime{owner: root}).restoreIdle(started)
		if restored && sub != nil {
			t.Cleanup(func() { sub.sess.discardRestoredCandidate() })
		}
		return sub, restoreErr
	}

	// The defect itself: the frozen ceiling excluded the name only for
	// reachability, and the child registers it now. Without the fix, restoreIdle
	// rejects the restored candidate with "exceeds the committed ceiling".
	t.Run("a reachability-excluded name survives the cold restore", func(t *testing.T) {
		sub, err := restore(t, []string{recovered})
		if err != nil {
			t.Fatalf("cold restore rejected a name the child registers and the ceiling only excluded for reachability: %v", err)
		}
		if sub == nil {
			t.Fatal("cold restore admitted no subagent session")
		}
		// Both surfaces the child is judged on: its own registry, and the
		// model-facing cache the Restrict call rebuilds.
		if sub.sess.reg.Get(recovered) == nil {
			t.Errorf("the child's registry lost %q, which it registers and the ceiling only excluded for reachability", recovered)
		}
		if names := sub.sess.reg.RegisteredNames(); !names[recovered] {
			t.Errorf("the child's registered names omit %q: %v", recovered, names)
		}
		if sub.sess.reg.Get(ordinary) == nil {
			t.Errorf("the cold restore dropped an ordinary tool: %v", sub.sess.reg.RegisteredNames())
		}
	})

	// The other direction, and the reason the fix is not a general loosening.
	//
	// The exclusion set production records is diff(role ceiling, kept), so a name
	// the role never allowed cannot appear in it — that is what makes "the child
	// registers it" a sound test for re-admission. What must still hold is the
	// consequence: a name outside the role ceiling stays out of the restored
	// child even though every session registers it as a core tool.
	t.Run("a role-forbidden name stays out of the restored child", func(t *testing.T) {
		sub, err := restore(t, []string{recovered})
		if err != nil {
			t.Fatalf("cold restore: %v", err)
		}
		if sub == nil {
			t.Fatal("cold restore admitted no subagent session")
		}
		if names := sub.sess.reg.RegisteredNames(); names[roleDenied] {
			t.Errorf("a name outside the role's ceiling survived the restore: %v", names)
		}
	})

	// A descriptor written before the field existed carries nothing, and the
	// frozen ceiling must stay exactly what it was.
	t.Run("an empty exclusion list leaves the frozen ceiling unchanged", func(t *testing.T) {
		sub, err := restore(t, nil)
		if err != nil {
			t.Fatalf("cold restore: %v", err)
		}
		if sub == nil {
			t.Fatal("cold restore admitted no subagent session")
		}
		if sub.sess.reg.Get(recovered) != nil {
			t.Errorf("a descriptor with no reachability exclusions somehow re-admitted %q", recovered)
		}
	})
}
