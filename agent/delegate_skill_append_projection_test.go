package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
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
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm") },
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestText(req), marker); got != 1 {
				t.Errorf("the request presented the turn that landed between the round and delegate snapshots %d times, want 1 (double projection)", got)
			}
			return agenttest.FinalResponse("merged")
		},
	})
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

// #7205: a parent steer that lands after the leased round completed its
// controller model-request claim is not in the round's bound claim, so
// CompleteModelRequest never filters it. The skill rebuild re-reads the
// resident history and appends the steer to both the rebuilt request and the
// round's fallback source, presenting it in a round bound before it arrived
// and again when the next request binds it. The rebuilt tail must obey the
// same late-steer predicate.
func TestDelegateRebuildWithholdsSteerAppendedAfterSnapshot(t *testing.T) {
	runtime := newSession(t, withStateDir(t.TempDir()))
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	runtime.delegateController = c
	c.mu.Lock()
	c.live[lease.delegateID].runtime = runtime
	c.live[lease.delegateID].binding.runtime = runtime
	c.mu.Unlock()

	// One pending delivery obligation whose skill this session cannot follow
	// appends a typed failure notification and sets appendedNotifications, so
	// the round rebuilds through rebuildForAppendedSkillTurns.
	runtime.mu.Lock()
	runtime.skillLifecycle.Obligations = append(runtime.skillLifecycle.Obligations, schema.SkillDeliveryObligation{
		InvocationID: "inv-7205",
		Identity:     schema.SkillContentIdentity{Name: skill.GardeningMemorySkillName},
		Route:        "model_tool",
	})
	runtime.skillLifecycle.Revision++
	runtime.mu.Unlock()

	const marker = "steer appended after the snapshot (7205)"
	var once sync.Once
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.afterDelegateModelSnapshot = func() {
			once.Do(func() {
				if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), lease.delegateID, marker); err != nil {
					t.Errorf("late steer: %v", err)
				}
			})
		}
	})

	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	var timings events.RoundTimings
	_, _, history, _, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare delegate model request: %v", err)
	}
	if got := countMessageText(history, parentMessageAttribution+marker); got != 0 {
		t.Fatalf("the rebuilt request presented a steer appended after the snapshot %d times, want 0: %#v", got, history)
	}
	source, ok := runtime.takeRoundFallbackSource()
	if !ok {
		t.Fatal("prepareModelRequestWithError recorded no fallback source")
	}
	fallbackHistory := runtime.requestMessages(source.system, expandHistory(source.turns, source.scope))
	if got := countMessageText(fallbackHistory, parentMessageAttribution+marker); got != 0 {
		t.Fatalf("the fallback source presented a steer appended after the snapshot %d times, want 0", got)
	}
	// Positive control by outcome identity, not wording: the rebuild ran and
	// carried the failed skill-delivery outcome it appended.
	carriedOutcome := false
	for _, turn := range source.turns {
		if turn.SkillState == nil {
			continue
		}
		for _, outcome := range turn.SkillState.Outcomes {
			if outcome.InvocationID == "inv-7205" && outcome.Status == "failed" {
				carriedOutcome = true
			}
		}
	}
	if !carriedOutcome {
		t.Fatalf("the rebuilt fallback source carries no failed outcome for inv-7205: the rebuild never ran, so this proof is vacuous")
	}
	// The steer is still owed: the next request binds it and presents it once.
	_, _, next, _, _, _, err := runtime.prepareModelRequestWithError(ctx, 2, &timings)
	if err != nil {
		t.Fatalf("prepare second delegate model request: %v", err)
	}
	if got := countMessageText(next, parentMessageAttribution+marker); got != 1 {
		t.Fatalf("the next request presented the steer %d times, want 1", got)
	}
}

// #7205: the rebuilt tail's planning copy is priced for the full-history
// estimate, so it must obey the same late-steer predicate as the request. A
// withheld steer that survives only into the estimate can still refuse a
// request whose messages fit the window.
func TestDelegateRebuildEstimatesWithoutWithheldSteer(t *testing.T) {
	runtime := newSession(t, withStateDir(t.TempDir()))
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	runtime.delegateController = c
	c.mu.Lock()
	c.live[lease.delegateID].runtime = runtime
	c.live[lease.delegateID].binding.runtime = runtime
	c.mu.Unlock()

	runtime.mu.Lock()
	runtime.skillLifecycle.Obligations = append(runtime.skillLifecycle.Obligations, schema.SkillDeliveryObligation{
		InvocationID: "inv-7205-estimate",
		Identity:     schema.SkillContentIdentity{Name: skill.GardeningMemorySkillName},
		Route:        "model_tool",
	})
	runtime.skillLifecycle.Revision++
	runtime.mu.Unlock()

	// A grounded measured baseline makes the manager's estimate ride the
	// request instead of being skipped as unmeasured.
	runtime.contextMgr.RecordInputTokens(1, 0)

	marker := strings.Repeat("x", 600_000)
	var once sync.Once
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.afterDelegateModelSnapshot = func() {
			once.Do(func() {
				if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), lease.delegateID, marker); err != nil {
					t.Errorf("late steer: %v", err)
				}
			})
		}
	})

	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	var timings events.RoundTimings
	_, _, _, req, _, _, err := runtime.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare delegate model request: %v", err)
	}
	if req.FullHistoryInputTokensEstimate >= len(marker)/4 {
		t.Fatalf("the withheld steer priced the rebuilt request's full-history estimate at %d tokens, want below %d: the estimate priced a turn the request does not present", req.FullHistoryInputTokensEstimate, len(marker)/4)
	}
}
