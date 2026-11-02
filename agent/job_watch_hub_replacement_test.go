package agent

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestHubWatchConfigurationReplacement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		before, after []string
		completion    bool
		replaced      bool
	}{
		{"identical", []string{"question"}, []string{"question"}, false, false},
		{"selectors", []string{"question"}, []string{"communicate"}, false, true},
		{"completion", []string{"question"}, []string{"question"}, true, true},
		{"remove_selector", []string{"question", "status"}, []string{"question"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
			jm := root.jobManager
			first, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", tc.before, false)
			if err != nil {
				t.Fatal(err)
			}
			second, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", tc.after, tc.completion)
			if err != nil {
				t.Fatal(err)
			}
			if second.ReplacedExisting != tc.replaced || (second.WatchID != first.WatchID) != tc.replaced {
				t.Fatalf("replacement=%+v first=%+v", second, first)
			}
			jm.mu.Lock()
			_, cfg, ok := jm.watchConfigByIDLocked(second.WatchID)
			correct := ok && reflect.DeepEqual(cfg.events, tc.after) && cfg.hubCompletion == tc.completion && len(jm.watches) == 1
			jm.mu.Unlock()
			if !correct {
				t.Fatal("requested configuration not installed exactly once")
			}
			registered, cleared := 0, 0
			for _, e := range loadJobStoreEvents(t, jm) {
				if e.Kind == jobstore.EventWatchRegistered {
					registered++
				}
				if e.Kind == jobstore.EventWatchCleared {
					cleared++
				}
			}
			want := 1
			if tc.replaced {
				want = 2
			}
			if registered != want || cleared != want-1 {
				t.Fatalf("journal registered=%d cleared=%d", registered, cleared)
			}
		})
	}
}

func TestHubWatchReplacementAtCapacity(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
	jm := root.jobManager
	for i := range maxLiveWatches {
		if _, err := jm.configureHubWatch(fmt.Sprintf("local:worker-%d", i), "instance", "boot", "local:manager", []string{"question"}, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, completion := range []bool{false, true} {
		if _, err := jm.configureHubWatch("local:worker-0", "instance", "boot", "local:manager", []string{"question"}, completion); err != nil {
			t.Fatalf("same-key admission at capacity: %v", err)
		}
	}
	if _, err := jm.configureHubWatch("local:overflow", "instance", "boot", "local:manager", nil, true); err == nil {
		t.Fatal("fresh watch exceeded unchanged bound")
	}
}

// Drive the runtime's real subscription lifetime owner with cancellable contexts.
// No fake hub/client: the E2E covers wire setup, this covers delayed installation
// and cleanup interleavings without relying on goroutine scheduling.
func TestHubWatchReplacementSubscriptionLifetime(t *testing.T) {
	t.Parallel()
	for _, lateOldInstall := range []bool{false, true} {
		t.Run(strconv.FormatBool(lateOldInstall), func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(c *SessionConfig) { c.Manager = true })
			jm := root.jobManager
			old, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", []string{"question"}, false)
			if err != nil {
				t.Fatal(err)
			}
			oldCtx, oldCancel := context.WithCancel(t.Context())
			defer oldCancel()
			if !lateOldInstall && !root.installHubWatchSubscription(jm, old.WatchID, oldCancel) {
				t.Fatal("initial subscription rejected")
			}
			replacement, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", nil, true)
			if err != nil {
				t.Fatal(err)
			}
			nextCtx, nextCancel := context.WithCancel(t.Context())
			defer nextCancel()
			if !root.installHubWatchSubscription(jm, replacement.WatchID, nextCancel) {
				t.Fatal("replacement subscription rejected")
			}
			if lateOldInstall && root.installHubWatchSubscription(jm, old.WatchID, oldCancel) {
				t.Fatal("retired registration resurrected")
			}
			if oldCtx.Err() == nil || nextCtx.Err() != nil {
				t.Fatalf("old=%v replacement=%v", oldCtx.Err(), nextCtx.Err())
			}
			duplicate, stopDuplicate := context.WithCancel(t.Context())
			defer stopDuplicate()
			if root.installHubWatchSubscription(jm, replacement.WatchID, stopDuplicate) || duplicate.Err() == nil {
				t.Fatal("duplicate subscription not cancelled")
			}
			root.cancelHubWatch(old.WatchID) // delayed old consumer cleanup
			root.mu.Lock()
			one := len(root.hubWatchCancels) == 1 && root.hubWatchCancels[replacement.WatchID] != nil
			root.mu.Unlock()
			if !one || nextCtx.Err() != nil {
				t.Fatal("old cleanup removed replacement")
			}
			root.cancelHubWatch(replacement.WatchID)
			if nextCtx.Err() == nil {
				t.Fatal("replacement cancellation did not release subscription")
			}
		})
	}
}

func TestHubWatchReplacementDropsPendingBeforeRegistration(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	old, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", []string{"question"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.onHubObservation(old.WatchID, appwire.SessionWatchUpdate{Event: "question", Metadata: appwire.SessionObservationMetadata{Ref: "local:worker", InstanceID: "instance", GenerationID: "boot", EntryID: "question", CausalKnown: true}}); err != nil {
		t.Fatal(err)
	}
	next, err := jm.configureHubWatch("local:worker", "instance", "boot", "local:manager", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var pending, dropped, cleared, registered int64
	for _, e := range loadJobStoreEvents(t, jm) {
		switch e.Kind {
		case jobstore.EventWatchSendPending:
			pending = e.Seq
		case jobstore.EventWatchSendDropped:
			dropped = e.Seq
		case jobstore.EventWatchCleared:
			cleared = e.Seq
		case jobstore.EventWatchRegistered:
			if e.WatchID == next.WatchID {
				registered = e.Seq
			}
		}
	}
	if pending == 0 || dropped <= pending || registered <= dropped || cleared <= registered {
		t.Fatalf("replacement journal order pending=%d dropped=%d cleared=%d registered=%d", pending, dropped, cleared, registered)
	}
}
