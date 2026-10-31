package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestManagerWatchRebindDeliversNewQuestionAfterEarlierOne pins that a watch
// survives a reconnect without swallowing the next question.
//
// Live hub notifications carry no identity of their own, so the hub manufactures
// one per event. A plain per-subscription counter produced "question-1" for the
// first question of every subscription, and the watcher dedups on
// instance/generation/entry/event while keeping that memory across a rebind. So
// the first question after a reconnect was dropped as already seen, and the
// snapshot could not recover a request raised after it: the worker waited with
// nobody able to observe it.
//
// The two halves are asserted together, because either alone is satisfiable
// wrongly: a distinct ID must be delivered, and a repeat of the same ID must
// still be suppressed, or the fix would simply remove dedup.
func TestManagerWatchRebindDeliversNewQuestionAfterEarlierOne(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	jm := root.jobManager
	const ref, instance, generation = "local:worker", "worker-instance", "7@worker"
	watch, err := jm.configureHubWatch(ref, instance, generation, "local:manager", []string{"question"}, false)
	if err != nil {
		t.Fatal(err)
	}
	question := func(entryID string) appwire.SessionWatchUpdate {
		return appwire.SessionWatchUpdate{
			Event: "question",
			Metadata: appwire.SessionObservationMetadata{
				Ref: ref, InstanceID: instance, GenerationID: generation,
				EntryID: entryID, CausalKnown: true,
			},
		}
	}
	deliveries := func(entryID string) int {
		n := 0
		for _, event := range loadJobStoreEvents(t, jm) {
			if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && event.WatchSend.TriggerIdentity == instance+"/"+generation+"/"+entryID+":question" && !event.WatchSend.EndNotice {
				n++
			}
		}
		return n
	}
	totalDeliveries := func() int {
		n := 0
		for _, event := range loadJobStoreEvents(t, jm) {
			if event.WatchSend != nil && event.WatchSend.Key.WatchID == watch.WatchID && !event.WatchSend.EndNotice {
				n++
			}
		}
		return n
	}

	// First subscription: the pre-rebind question arrives.
	if err := jm.onHubObservation(watch.WatchID, question("question-wg_aaa-1")); err != nil {
		t.Fatal(err)
	}
	if got := deliveries("question-wg_aaa-1"); got != 1 {
		t.Fatalf("deliveries after the first question = %d, want 1", got)
	}

	// A repeat of that exact identity is still deduped: the same event replayed
	// over the same subscription must not wake the Manager twice.
	if err := jm.onHubObservation(watch.WatchID, question("question-wg_aaa-1")); err != nil {
		t.Fatal(err)
	}
	if got := deliveries("question-wg_aaa-1"); got != 1 {
		t.Fatalf("deliveries after repeating the same identity = %d, want 1", got)
	}

	// After a rebind the new subscription numbers its own events from one, but
	// its IDs carry a different subscription prefix, so the next question is a
	// new identity and must be delivered.
	if err := jm.onHubObservation(watch.WatchID, question("question-wg_bbb-1")); err != nil {
		t.Fatal(err)
	}
	if got := deliveries("question-wg_bbb-1"); got != 1 {
		t.Fatalf("deliveries after a rebind question = %d, want 1 for its new identity", got)
	}
	if got := deliveries("question-wg_aaa-1"); got != 1 {
		t.Fatalf("deliveries for the earlier identity after rebind = %d, want 1", got)
	}
	if got := deliveries("question-wg_bbb-1"); got != 1 {
		t.Fatalf("deliveries for the rebind identity = %d, want 1", got)
	}
	if got := totalDeliveries(); got != 2 {
		t.Fatalf("total non-end deliveries after rebind = %d, want 2", got)
	}
}
