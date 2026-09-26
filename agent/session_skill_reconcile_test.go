// Tests that a user skill selection whose admission was lost to a metadata
// save failure (or a crash in the same window) is reconciled from the durable
// typed input record at restore, never silently dropped.
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
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
	if s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) == steeringAppendFailed {
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
	t.Parallel()
	root := t.TempDir()
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_no_re_reconcile")
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

	if s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) == steeringAppendFailed {
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

// TestSkillActivation_FailedPreparationIsNotReDeliveredAtRestore pins the
// negative side of the reconcile contract: an input record whose preparation
// FAILED carries no Prepared invocations, which means "kept for correction,
// never re-delivered". Recovery must therefore re-drive nothing for it — a
// restore that reconciled it would deliver a selection the operator was told
// had failed and may have already corrected.
func TestSkillActivation_FailedPreparationIsNotReDeliveredAtRestore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

	// No skill.md is written for this name, so preparation cannot resolve it.
	if s.consumeSteeringMessage(steeringMessage{Text: "steer with an unresolvable skill", SkillNames: []string{"no-such-skill"}}) == steeringAppendFailed {
		t.Fatal("steering message was not durably consumed")
	}
	states := skillTurnStates(s)
	if len(states) != 1 || states[0].Input == nil {
		t.Fatalf("steering turn skill states = %+v, want exactly one typed input record", states)
	}
	if len(states[0].Input.Prepared) != 0 {
		t.Fatalf("a failed preparation recorded %d prepared invocation(s), want none", len(states[0].Input.Prepared))
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("obligations after a failed preparation = %+v, want none", got)
	}
	s.Close()

	// The source becomes available before the restart. A reconcile that
	// re-drove the failed preparation would therefore SUCCEED and deliver the
	// skill -- so this is what makes the assertion below discriminating rather
	// than a restatement of "the name is still missing".
	writeSkillMD(t, root, "no-such-skill", "---\nname: no-such-skill\ndescription: fixture\n---\nBODY_failed_preparation")

	meta, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Skills == nil {
		t.Fatal("session metadata has no skill lifecycle snapshot")
	}
	if len(meta.Skills.Obligations) != 0 {
		t.Fatalf("persisted obligations %+v for a failed preparation, want none", meta.Skills.Obligations)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), meta, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()

	if got := lifecycleObligations(restored); len(got) != 0 {
		t.Fatalf("restored obligations = %+v, want none (a failed preparation must never be re-delivered)", got)
	}
	carriers := 0
	for _, state := range skillTurnStates(restored) {
		carriers += len(state.Obligations)
	}
	if carriers != 0 {
		t.Fatalf("restored carrier obligations = %d, want none", carriers)
	}
}

// TestSkillActivation_PreparedSelectionPinsRecordedSource pins the
// no-retargeting contract on the reconcile path: a durable prepared selection
// records the exact source its preparation resolved, and the restore-time
// re-drive adopts THAT source. When it is gone and a same-name replacement
// exists elsewhere, the re-drive fails visibly and the replacement is never
// activated.
func TestSkillActivation_PreparedSelectionPinsRecordedSource(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_pinned_source")
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	stateDir := t.TempDir()
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

	repair := breakSessionMetaPath(t, s)
	if s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) == steeringAppendFailed {
		t.Fatal("steering message was not durably consumed")
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("failed admission left live obligations: %+v", got)
	}
	repair()
	// The recorded source is gone while a same-name replacement appears at a
	// different location: reconciliation must fail on the recorded source and
	// never adopt the replacement.
	writeSkillMDRel(t, root, filepath.Join(".agents", "skills"), "opaque",
		"---\nname: opaque\ndescription: replacement\n---\nBODY_replacement_retarget")
	if err := os.Remove(source); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	if err := s.saveMeta(); err != nil {
		t.Fatalf("save after repair: %v", err)
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

	if got := lifecycleObligations(restored); len(got) != 0 {
		t.Fatalf("reconciled obligations = %+v, want none: the recorded source is gone and a same-name replacement must never be adopted", got)
	}
	for _, state := range skillTurnStates(restored) {
		for _, carried := range state.Obligations {
			t.Fatalf("reconciled carrier obligation %+v, want none", carried)
		}
	}
	for _, state := range skillTurnStates(restored) {
		for _, outcome := range state.Outcomes {
			if outcome.Identity.Name == "opaque" && outcome.Status != "failed" {
				t.Fatalf("reconciled outcome = %+v, want no successful activation of the replacement", outcome)
			}
		}
	}
	if entry := lifecycleInventory(restored)["opaque"]; entry.Ordinary != nil {
		t.Fatalf("inventory recorded a retargeted activation: %+v", entry.Ordinary)
	}
}

// TestSkillActivation_LostSteeringAdmissionGatesNextDispatch pins the
// steering-admission gate: when a skill-bearing steering message's admission
// never reaches durable obligations, the selection is retained and the NEXT
// model request either carries its complete instructions or fails visibly. It
// is never dispatched without them.
func TestSkillActivation_LostSteeringAdmissionGatesNextDispatch(t *testing.T) {
	t.Run("a repaired metadata path admits the selection before the next request", func(t *testing.T) {
		root := t.TempDir()
		stateDir := t.TempDir()
		body := "BODY_staged_gate"
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
		s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

		repair := breakSessionMetaPath(t, s)
		if s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) == steeringAppendFailed {
			t.Fatal("steering message was not durably consumed")
		}
		if got := lifecycleObligations(s); len(got) != 0 {
			t.Fatalf("failed admission left live obligations: %+v", got)
		}
		repair()

		var rt events.RoundTimings
		_, _, _, req, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt)
		if err != nil {
			t.Fatalf("prepareModelRequestWithError after repair: %v", err)
		}
		if got := lifecycleObligations(s); len(got) != 1 {
			t.Fatalf("obligations after the next request prepared = %+v, want the retained steering selection admitted", got)
		}
		found := false
		for _, env := range requestSkillEnvelopes(t, req) {
			if env.Doc.Name == "opaque" && strings.Contains(env.Doc.Instructions, body) {
				found = true
			}
		}
		if !found {
			t.Fatal("the dispatching request does not carry the steering selection's complete instructions")
		}
	})

	t.Run("a still-broken metadata path fails the request instead of omitting the selection", func(t *testing.T) {
		root := t.TempDir()
		stateDir := t.TempDir()
		writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_retryable_gate")
		s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

		repair := breakSessionMetaPath(t, s)
		if s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) == steeringAppendFailed {
			t.Fatal("steering message was not durably consumed")
		}
		// The selection is still unadmitted. Preparing the next request must
		// either fail (the gate) or carry the instructions — dispatching
		// without them is exactly the silent omission this pins out.
		var rt events.RoundTimings
		_, _, _, req, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt)
		if err == nil && len(requestSkillEnvelopes(t, req)) == 0 {
			t.Fatal("the request was prepared without the steering selection's instructions and without an error")
		}
		// The selection stays retryable: with the metadata path repaired, the
		// next request admits it and carries the complete instructions.
		repair()
		var rt2 events.RoundTimings
		_, _, _, req2, _, _, err2 := s.prepareModelRequestWithError(context.Background(), 0, &rt2)
		if err2 != nil {
			t.Fatalf("prepareModelRequestWithError after repair: %v", err2)
		}
		found := false
		for _, env := range requestSkillEnvelopes(t, req2) {
			if env.Doc.Name == "opaque" && strings.Contains(env.Doc.Instructions, "BODY_retryable_gate") {
				found = true
			}
		}
		if !found {
			t.Fatal("the retried selection was not admitted into the next request")
		}
		if got := lifecycleObligations(s); len(got) != 1 {
			t.Fatalf("obligations after the retried admission = %+v, want exactly one (no duplicate)", got)
		}
	})
}

// TestSkillActivation_RestoredSelectionAdmissionGatesNextDispatch pins the same
// gate on the restore path: when the restore-time reconciliation of a durable
// prepared selection cannot write its obligations, the restored session must
// retain the selection and gate the next request on it, exactly like a live
// steering admission. Discarding that error would leave the restored session
// building requests with the steering prose in history, no skill instructions
// and no error — the silent omission this contract exists to prevent.
func TestSkillActivation_RestoredSelectionAdmissionGatesNextDispatch(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	body := "BODY_restored_gate"
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())

	repair := breakSessionMetaPath(t, s)
	if s.consumeSteeringMessage(steeringMessage{Text: "steer with a skill", SkillNames: []string{"opaque"}}) == steeringAppendFailed {
		t.Fatal("steering message was not durably consumed")
	}
	if got := lifecycleObligations(s); len(got) != 0 {
		t.Fatalf("failed admission left live obligations: %+v", got)
	}
	repair()
	if err := s.saveMeta(); err != nil {
		t.Fatalf("save after repair: %v", err)
	}
	s.Close()

	meta, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	// The metadata store is read-only for the restore: the reconciliation can
	// read the durable prepared record but cannot persist an admission.
	metaPath := filepath.Join(stateDir, sessionsSubdir, s.Meta().ID+".meta.json")
	if err := os.Remove(metaPath); err != nil {
		t.Fatalf("remove meta file: %v", err)
	}
	if err := os.Mkdir(metaPath, 0o755); err != nil {
		t.Fatalf("break meta path: %v", err)
	}
	restoreMeta := func() {
		_ = os.Remove(metaPath)
	}
	defer restoreMeta()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	// Preparing a request while the store is still unwritable must not dispatch
	// the steering without its instructions: the retained selection makes it
	// fail visibly instead.
	var rt events.RoundTimings
	_, _, _, req, _, _, err := restored.prepareModelRequestWithError(context.Background(), 0, &rt)
	if err == nil && len(requestSkillEnvelopes(t, req)) == 0 {
		t.Fatal("the restored session prepared a request without the selection's instructions and without an error")
	}
	if len(restored.pendingSkillAdmissions) != 1 {
		t.Fatalf("pending admissions after the failed retry = %d, want the unadmitted restored selection retained for retry", len(restored.pendingSkillAdmissions))
	}
	// With the store writable again the retry admits the selection and the next
	// request carries the complete instructions.
	restoreMeta()
	var rt2 events.RoundTimings
	_, _, _, req2, _, _, err2 := restored.prepareModelRequestWithError(context.Background(), 0, &rt2)
	if err2 != nil {
		t.Fatalf("prepareModelRequestWithError after repair: %v", err2)
	}
	found := false
	for _, env := range requestSkillEnvelopes(t, req2) {
		if env.Doc.Name == "opaque" && strings.Contains(env.Doc.Instructions, body) {
			found = true
		}
	}
	if !found {
		t.Fatal("the retried restored selection was not admitted into the next request")
	}
	if got := lifecycleObligations(restored); len(got) != 1 {
		t.Fatalf("obligations after the retried admission = %+v, want exactly one", got)
	}
}
