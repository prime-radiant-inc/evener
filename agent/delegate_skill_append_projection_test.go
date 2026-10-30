package agent

import (
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// #4261: a leased delegate round projects its request from the delegate
// controller's own history snapshot, which is taken AFTER the round's
// historyTurns snapshot and can already hold turns appended in between.
// rebuildForAppendedSkillTurns measures the turns to append against the round
// snapshot, so when a skill delivery turn is appended in that same round, a
// turn that landed between the two snapshots is already in the projected
// history and is projected a second time. The request must carry it once.
func TestDelegateSkillAppendProjectsBetweenSnapshotsTurnOnce(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	const marker = "between-snapshots marker for issue 4261"
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm") },
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestText(req), marker); got != 1 {
				t.Errorf("the request presented the turn that landed between the round and delegate snapshots %d times, want 1 (double projection)", got)
			}
			return agenttest.FinalResponse("merged")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// One pending skill delivery obligation whose recorded identity is
	// incomplete. prepareSkillDelivery's failure path appends a typed
	// notification turn for it in the next round and sets appendedNotifications,
	// so the request rebuilds through rebuildForAppendedSkillTurns.
	sub.sess.mu.Lock()
	sub.sess.skillLifecycle.Obligations = append(sub.sess.skillLifecycle.Obligations, schema.SkillDeliveryObligation{
		InvocationID: "inv-4261",
		Route:        "model_tool",
	})
	sub.sess.skillLifecycle.Revision++
	sub.sess.mu.Unlock()

	// A turn lands after the round's history snapshot and before the delegate
	// controller's own snapshot, so the delegate snapshot is strictly longer
	// than the round snapshot and carries a turn the round snapshot does not.
	var landOnce sync.Once
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.afterModelHistorySnapshot = func() {
			landOnce.Do(func() {
				if _, err := sub.sess.appendDelegateNotificationDurably("attention:4261", marker); err != nil {
					t.Errorf("append the between-snapshots turn: %v", err)
				}
			})
		}
	})

	sendAndAcknowledge(t, root, fixture.delegateID, "FOLLOW-UP: merge it")
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Positive control: the send round must have built exactly one request, so
	// the projection assertion above actually ran. Without it the test would
	// pass vacuously if the round never reached the adapter step.
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (the send round's asserting request never ran)", got)
	}
}
