package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestASkillReloadKeepsTheReportAReleaseFreed pins that the reload's rebuild is
// taken from one coherent capture rather than a position measured earlier.
//
// The reload rebuilds the request after the projection captured its turns. If a
// stop's hold on a report ends in that window, the release moves the report to
// the history tail; the rebuild then measures "what is new" from a length taken
// before the move. Every index after the report shifted, so the suffix it
// appends is not the set of turns the request lacks — the request carries the
// new notification while omitting the report that same release just made
// presentable, which is the order the release exists to prevent.
//
// The assertion is on the request the rebuild produces: it must carry the
// released report.
func TestASkillReloadKeepsTheReportAReleaseFreed(t *testing.T) {
	const marker = "the report a release freed during the skill reload"
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

	// A report a stop still holds: the projection filters it out of the request.
	const heldID = "delegate:delivery-held-owed-reply"
	if _, err := runtime.appendDelegateNotificationDurably(heldID, marker); err != nil {
		t.Fatalf("append the held report: %v", err)
	}
	runtime.holdDelegateAttentionFromAutomaticDriveAt(heldID, 1)
	// A second row a later stop still holds. The release ends only the row it
	// supersedes, so this one stays out of the projection AND out of the
	// rebuild: it is the row whose presence makes a positional boundary wrong,
	// because the projection's drop shifts every later index while the rebuild
	// measures its suffix on the unfiltered capture.
	const stillHeldID = "delegate:delivery-held-behind-a-newer-stop"
	if _, err := runtime.appendDelegateNotificationDurably(stillHeldID, "a report a later stop still holds"); err != nil {
		t.Fatalf("append the still-held report: %v", err)
	}
	runtime.holdDelegateAttentionFromAutomaticDriveAt(stillHeldID, 2)
	// Later exchanges arrive while the hold stands, exactly as they do in
	// production: the report is now buried in prior history, not at the tail.
	for i := range 3 {
		runtime.mu.Lock()
		runtime.history = append(runtime.history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("exchange %d after the held report", i))))
		runtime.mu.Unlock()
	}

	// The reload rebuilds only when a typed skill turn has been appended. An
	// obligation whose source is gone is denied before the plan can accept it.
	missing := filepath.Join(stateDir, "skills", "opaque", "SKILL.md")
	if err := runtime.persistSkillToolObligations(&schema.SkillTurnState{Obligations: []schema.SkillDeliveryObligation{{
		InvocationID: "inv-reload-release",
		ToolCallID:   "skill-reload-release",
		Identity: schema.SkillContentIdentity{
			Name:         "opaque",
			DeclaredName: "opaque",
			Source:       missing,
			FileDigest:   "reload-release-file",
		},
		Route: "model_tool",
	}}}); err != nil {
		t.Fatalf("record the reload obligation: %v", err)
	}

	// The window: after the projection captured its turns and holds, and before
	// the reload rebuilds. The release ends the hold and moves the report to the
	// tail.
	released := false
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.afterDelegateAttentionProjection = func() {
			if released {
				return
			}
			released = true
			runtime.releaseAutomaticDriveHolds(delegateHoldReleaseDelivery, 1)
		}
	})

	ctx := context.WithValue(context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease), delegateAttentionRunContextKey{}, struct{}{})
	var timings events.RoundTimings
	_, _, history, _, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare the attention-run request: %v", err)
	}
	if !released {
		t.Fatal("premise broken: the release never ran inside the reload window")
	}
	runtime.attentionMu.Lock()
	_, stillHeld := runtime.attentionAutomaticDriveHolds[heldID]
	runtime.attentionMu.Unlock()
	if stillHeld {
		t.Fatalf("premise broken: %q is still held after the release", heldID)
	}
	// Exactly once, not merely present. A rebuild that slices at a length
	// measured before the release re-appends turns the request already
	// projects, because the move shifted every index after the report: the
	// request then carries the report twice, or carries it while dropping the
	// reload's own notification from the position it was counted at.
	if carried := countMessageText(history, marker); carried != 1 {
		t.Fatalf("the rebuilt request carries %q %d times, want exactly 1: the release ended its hold and moved it to the history tail, and the reload must rebuild from one coherent capture rather than from a length measured before that move", heldID, carried)
	}
	seen := make(map[string]int, len(history))
	for _, message := range history {
		if text := message.Text(); text != "" {
			seen[text]++
		}
	}
	for text, count := range seen {
		if count > 1 {
			t.Fatalf("the rebuilt request carries one message %d times: %q — a rebuild that appends from a stale position duplicates the turns the projection already carried", count, text)
		}
	}
}
