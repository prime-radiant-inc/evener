package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// Competing durable receivers are driven only by production admission and the
// completion release hook. No test callback selects or advances a fair turn.
func TestAFailingDelegateDoesNotStarveItsSiblings(t *testing.T) {
	t.Parallel()
	testNotificationCompetition(t, true, 2)
}

// A failed restore retains its work without spending an admitted turn or
// blocking a healthy receiver when the existing executor releases capacity.
func TestAFailedRestoreTakesTheDelegatesTurn(t *testing.T) {
	testNotificationCompetition(t, true, 1)
}

func TestEverySelectionTakesItsTurnSoNoDelegateMonopolizesTheDrive(t *testing.T) {
	t.Parallel()
	testNotificationCompetition(t, false, 2)
}

func testNotificationCompetition(t *testing.T, broken bool, rounds int) {
	t.Helper()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = repeatFinalResponse(2, "warm competing receiver")
	root := restoreSupervisionRoot(t, fixture, agenttest.NewFakeClock())
	first := root.createDelegate(context.Background(), delegateArgs{Task: "first competing receiver"})
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	waitForStableSupervisionRun(t, root, first.ChildSessionID)
	last := root.createDelegate(context.Background(), delegateArgs{Task: "last competing receiver"})
	if last.Err != nil {
		t.Fatal(last.Err)
	}
	waitForStableSupervisionRun(t, root, last.ChildSessionID)
	children := []string{first.ChildSessionID, fixture.childID, last.ChildSessionID}
	markers := []string{"fair-receiver-a-39f1", "fair-receiver-b-29d7", "fair-receiver-c-73b8"}
	if broken {
		if err := os.Remove(filepath.Join(fixture.stateDir, sessionsSubdir, fixture.childID+".meta.json")); err != nil {
			t.Fatal(err)
		}
	}
	c := root.delegateController
	c.mu.Lock()
	c.driveLimit = 1
	generations := make([]uint64, len(children))
	for i, childID := range children {
		generations[i] = c.durable[c.delegateOwnerOfSessionLocked(childID)].Generation
	}
	c.mu.Unlock()
	reached := make(chan int, 3*rounds)
	release := make(chan struct{})
	var runsMu sync.Mutex
	var runs []<-chan struct{}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		// No healthy provider remains held, and the unrestorable receiver has
		// no sender. Join the actual owning tails before root.Close.
		root.sendersWG.Wait()
		runsMu.Lock()
		captured := slices.Clone(runs)
		runsMu.Unlock()
		for _, done := range captured {
			awaitSignal(t, done, "competing receiver generation cleanup")
		}
	})
	counts := make([]int, len(children)) // provider steps are serialized by fakeAdapter
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionStartCommitted = func(sub *subagent) {
			updateSessionTestConfig(sub.sess, func(childCfg *testConfig) {
				childCfg.subagentAfterFinalStatePublish = func(*subagent) {
					for i, childID := range children {
						if childID != sub.sess.ID() || counts[i] >= rounds {
							continue
						}
						if err := c.enqueueNotification(childID, notification{
							ID:      fmt.Sprintf("fair-%d-%d", i, counts[i]+1),
							Message: llm.User(fmt.Sprintf("%s-round-%d", markers[i], counts[i]+1)),
						}); err != nil {
							t.Error(err)
						}
					}
				}
			})
		}
	})
	fixture.adapter.mu.Lock()
	for range 3 * rounds {
		fixture.adapter.steps = append(fixture.adapter.steps, func(req llm.Request) llm.Response {
			actual := -1
			for i, marker := range markers {
				for _, message := range req.Messages {
					if strings.Contains(message.Text(), marker) {
						if actual != -1 && actual != i {
							t.Error("one provider request mixed competing receivers")
						}
						actual = i
					}
				}
			}
			if actual < 0 {
				t.Error("provider received no competing receiver payload")
				reached <- actual
				return finalResponse("unexpected request")
			}
			// Provider entry follows launch's done reset and precedes release.
			sub := root.subagents.get(children[actual])
			sub.mu.Lock()
			done := sub.done
			sub.mu.Unlock()
			runsMu.Lock()
			runs = append(runs, done)
			runsMu.Unlock()
			counts[actual]++
			observationRequest(t, req, []string{fmt.Sprintf("%s-round-%d", markers[actual], counts[actual])}, nil)
			reached <- actual
			if actual == 0 && counts[actual] == 1 {
				<-release // Hold the real leased provider, not a fabricated capacity token.
			}
			return finalResponse("competing receiver handled")
		})
	}
	fixture.adapter.mu.Unlock()
	for i, childID := range children {
		if err := c.enqueueNotification(childID, notification{ID: fmt.Sprintf("fair-%d-1", i), Message: llm.User(markers[i] + "-round-1")}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			select {
			case actual := <-reached:
				if actual != 0 {
					t.Fatalf("first provider receiver = %d, want 0", actual)
				}
			// TRIPWIRE: the scripted provider reports real entry, only a hang reaches this guard.
			case <-time.After(5 * time.Second):
				t.Fatal("first receiver never acquired production capacity")
			}
		}
	}
	c.mu.Lock()
	drives := c.drivesInUse
	for i, childID := range children[1:] {
		generation := c.durable[c.delegateOwnerOfSessionLocked(childID)].Generation
		if generation != generations[i+1] {
			t.Errorf("receiver %d admitted generation %d without capacity, previous=%d", i+1, generation, generations[i+1])
		}
	}
	c.mu.Unlock()
	if drives != 1 {
		t.Fatalf("occupied production drive slots=%d, want one", drives)
	}
	select {
	case actual := <-reached:
		t.Fatalf("receiver %d bypassed occupied production capacity", actual)
	default:
	}
	// Both other receivers now have durable pending work. Release is the only
	// progress stimulus: no manual recheck, notify, timer advance or parent turn.
	close(release)
	var got []int
	want := []int{0}
	for round := range rounds {
		for i := range children {
			if round == 0 && i == 0 || broken && i == 1 {
				continue
			}
			want = append(want, i)
		}
	}
	got = append(got, 0)
	for len(got) < len(want) {
		select {
		case actual := <-reached:
			got = append(got, actual)
		// TRIPWIRE: release drives real scripted entries, only a hang reaches this guard.
		case <-time.After(5 * time.Second):
			t.Fatalf("production release stranded competing receivers: got %v, want %v", got, want)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("actual provider receiver order = %v, want fair admission order %v", got, want)
	}
	runsMu.Lock()
	captured := slices.Clone(runs)
	runsMu.Unlock()
	for _, done := range captured {
		awaitSignal(t, done, "competing receiver generation")
	}
	root.sendersWG.Wait()
	for i, childID := range children {
		if broken && i == 1 {
			continue
		}
		waitForStableSupervisionRun(t, root, childID)
		fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, childID), childID)
		if err != nil {
			t.Fatal(err)
		}
		for round := range rounds {
			id := fmt.Sprintf("fair-%d-%d", i, round+1)
			if _, observed := fold.observed[id]; !observed {
				t.Errorf("healthy receiver did not successfully observe %s", id)
			}
		}
		if pending := fold.pendingIDs(); len(pending) != 0 {
			t.Errorf("healthy receiver retained pending work: %v", pending)
		}
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2+len(want) {
		t.Fatalf("provider requests=%d, want two warm-ups plus %d exact admissions", got, len(want))
	}
	if broken {
		fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, fixture.childID), fixture.childID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(fold.pendingIDs(), []string{"fair-1-1"}) || len(fold.observed) != 0 {
			t.Fatalf("failed restore lost or observed undelivered work: %+v", fold)
		}
		c.mu.Lock()
		state := c.attention[fixture.delegateID]
		failed, turn := state.restoreFailures, state.driveTurn
		c.mu.Unlock()
		if failed == 0 || turn != 0 {
			t.Fatalf("failed receiver evidence: restore failures=%d admitted turn=%d, want failed but unadmitted", failed, turn)
		}
	}
}
