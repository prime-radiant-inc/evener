// Tests that a user skill selection whose admission was lost to a metadata
// save failure (or a crash in the same window) is reconciled from the durable
// typed input record at restore, never silently dropped.
package agent

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestSkillActivation_SteeringSelectionReconciledAfterAdmissionSaveFailure
// drives the exact production steering path (consumeSteeringMessage) with a
// skill selection while the metadata path is broken: the steering turn —
// carrying the typed input record with the prepared invocations — is durable,
// but admitSkillActivationBatch's save fails and rolls the obligation back.
// After the repair and a restart, restore must reconstruct the pending
// selection from the durable input record: exactly one obligation for the
// steering-route invocation, its carrier, and no duplication on a second
// restore.
func TestSkillActivation_SteeringSelectionReconciledAfterAdmissionSaveFailure(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_steering_reconcile")
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

	repair := breakSessionMetaPath(t, s)
	if !s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) {
		t.Fatal("steering message was not durably consumed")
	}
	// The admission's save failed: nothing half-admits into the live session.
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("failed admission left live obligations: %+v", got)
	}
	repair()
	// Persist the clean (obligation-free) snapshot a crash would have left.
	if err := s.saveMeta(); err != nil {
		t.Fatalf("save after repair: %v", err)
	}
	s.Close()

	restore := func() *Session {
		t.Helper()
		meta, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
		if err != nil {
			t.Fatal(err)
		}
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), meta, stateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { restored.Close() })
		return restored
	}

	restored := restore()
	obligations := lifecycleObligations(restored)
	if len(obligations) != 1 {
		t.Fatalf("restored obligations = %+v, want exactly one reconciled from the durable steering input record", obligations)
	}
	obligation := obligations[0]
	if obligation.Identity.Name != "opaque" {
		t.Fatalf("reconciled obligation skill = %q, want opaque", obligation.Identity.Name)
	}
	if obligation.Route != "user_selection" {
		t.Fatalf("reconciled obligation route = %q, want user_selection", obligation.Route)
	}

	// The steering path is pinned explicitly: the restored history keeps the
	// steering turn whose typed input record carried the prepared invocation
	// the reconcile re-drove, under the same invocation identity.
	var steeringInput *schema.SkillInputRecord
	for _, state := range skillTurnStates(restored) {
		if state.Input != nil && len(state.Input.Names) == 1 && state.Input.Names[0] == "opaque" {
			steeringInput = state.Input
		}
	}
	if steeringInput == nil {
		t.Fatal("restored history lost the steering turn's typed skill input record")
	}
	if len(steeringInput.Prepared) != 1 || steeringInput.Prepared[0].InvocationID != obligation.InvocationID {
		t.Fatalf("prepared invocations = %+v, want the reconciled obligation %q", steeringInput.Prepared, obligation.InvocationID)
	}
	steeringTurnSeen := false
	for _, turn := range restored.history {
		if turn.Kind == schema.TurnSteering && turn.SkillState != nil && turn.SkillState.Input != nil &&
			len(turn.SkillState.Input.Prepared) == 1 && turn.SkillState.Input.Prepared[0].InvocationID == obligation.InvocationID {
			steeringTurnSeen = true
		}
	}
	if !steeringTurnSeen {
		t.Fatal("the prepared input record did not arrive on a steering turn")
	}

	// The reconciled admission published its carrier: the obligation is
	// deliverable at the next dispatch seam, not a phantom.
	carrierSeen := false
	for _, state := range skillTurnStates(restored) {
		for _, carried := range state.Obligations {
			if carried.InvocationID == obligation.InvocationID {
				carrierSeen = true
			}
		}
	}
	if !carrierSeen {
		t.Fatal("reconciled admission did not publish its carrier turn")
	}

	// Idempotency: a second restore sees the obligation and carrier already
	// durable and must not re-drive the admission.
	again := restore()
	if got := lifecycleObligations(again); len(got) != 1 {
		t.Fatalf("second restore obligations = %+v, want exactly one (no duplicate reconciliation)", got)
	}
}

// TestSkillActivation_AdmittedSelectionIsNotReReconciled pins the coverage
// predicate: a selection whose admission succeeded leaves durable evidence
// (obligation, carrier), and a restore must not drive a second admission for
// the same invocation.
func TestSkillActivation_AdmittedSelectionIsNotReReconciled(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_no_re_reconcile")
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

	if !s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) {
		t.Fatal("steering message was not durably consumed")
	}
	if got := lifecycleObligations(s); len(got) != 1 {
		t.Fatalf("admission obligations = %+v, want exactly one", got)
	}
	s.Close()

	meta, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), meta, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := lifecycleObligations(restored); len(got) != 1 {
		t.Fatalf("restored obligations = %+v, want exactly one (the admission must not be re-driven)", got)
	}
	carriers := 0
	for _, state := range skillTurnStates(restored) {
		carriers += len(state.Obligations)
	}
	if carriers != 1 {
		t.Fatalf("restored carrier obligations = %d, want exactly one", carriers)
	}
}
