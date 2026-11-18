package agent

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// TestSkillReloadDoesNotPresentAnUnadmittedWatchFrame pins that the skill
// reload's rebuild is not a second, ungated entry into an automatic request.
//
// The projection filters an automatic attention run's turns by a stop's hold and
// by whether an original watch frame's wake has been admitted, and it records
// the unfiltered length it projected as the boundary the reload measures from.
// A stable watch frame that lands after that capture is durable in resident
// history before any arm of its own runs, so the reload appends it from the raw
// slice. Without the same gate there, the request carries a row nothing
// admitted: the run resolves it, and the arm that would have released an
// earlier stopped report finds the row gone and skips the release. The request
// is the evidence, not the marker.
func TestSkillReloadDoesNotPresentAnUnadmittedWatchFrame(t *testing.T) {
	const marker = "unadmitted reload frame the gate must withhold"
	profile := namedInstanceProfile("ant", "anthropic", "claude-sonnet-4-5")
	stateDir := realTempDirForTest(t)
	runtime := newSession(t, withAdapter(&fakeAdapter{name: "ant"}), withProfile(profile), withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	runtime.delegateController = c
	runtime.owningDelegateID = lease.delegateID
	c.mu.Lock()
	c.live[lease.delegateID].runtime = runtime
	c.live[lease.delegateID].binding.runtime = runtime
	c.mu.Unlock()

	// The reload rebuilds only when a typed skill turn has been appended to the
	// resident history the request projected. An obligation whose source is gone
	// is denied before the plan can accept it, which records its typed
	// explanation into this dispatch and rebuilds.
	missing := filepath.Join(stateDir, "skills", "opaque", "SKILL.md")
	if err := runtime.persistSkillToolObligations(&schema.SkillTurnState{Obligations: []schema.SkillDeliveryObligation{{
		InvocationID: "inv-reload-gate",
		ToolCallID:   "skill-reload-gate",
		Identity: schema.SkillContentIdentity{
			Name:         "opaque",
			DeclaredName: "opaque",
			Source:       missing,
			FileDigest:   "reload-gate-file",
		},
		Route: "model_tool",
	}}}); err != nil {
		t.Fatalf("record the reload obligation: %v", err)
	}

	// The frame lands after the projection captured the turns and the held IDs
	// that filter them, and before the reload re-snapshots resident history.
	// Nothing admits its wake.
	const frameID = "watch:wd_reload:1"
	landed := false
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.afterDelegateAttentionProjection = func() {
			if landed {
				return
			}
			landed = true
			if _, err := runtime.appendDelegateNotificationDurably(frameID, marker); err != nil {
				t.Errorf("append the unadmitted watch frame: %v", err)
				return
			}
		}
	})

	ctx := context.WithValue(context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease), delegateAttentionRunContextKey{}, struct{}{})
	var timings events.RoundTimings
	_, _, history, _, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare the attention-run request: %v", err)
	}
	if !landed {
		t.Fatal("premise broken: the frame never landed after the snapshot, so the reload had nothing to withhold")
	}
	// The frame is pending and withheld by the gate after the round, which is
	// the state the request must not have contradicted. A round that presented
	// it resolves the row, so this also reports whether the bypass fired.
	runtime.attentionMu.Lock()
	pending := delegateAttentionPendingSet(runtime.delegateAttentionPendingIDsLocked())
	_, stillPending := pending[frameID]
	runtime.attentionMu.Unlock()
	if presented := countMessageText(history, marker) != 0; presented {
		t.Fatalf("the request presented %q: its wake never passed the gate, so the run resolves a row nothing admitted and the release it owed an earlier stop is skipped", frameID)
	}
	if !stillPending {
		t.Fatalf("premise broken: %q is no longer pending after the round, so this test cannot tell withholding from resolution", frameID)
	}
}
