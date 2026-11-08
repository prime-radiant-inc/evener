package sshconn

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// The tests here drive the post-publish and post-announce validation windows of
// Ensure and AttachUnderGate through the afterPublish and OnEvent seams: each
// window is a close race in production, and the seams let a test land the
// teardown exactly inside it instead of hoping a sleep biases the race.

type ensureEvidenceEvents struct {
	mu     sync.Mutex
	events []Event
}

func (e *ensureEvidenceEvents) record(ev Event) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
}

func (e *ensureEvidenceEvents) has(kind EventKind, state State) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Kind == kind && ev.State == state {
			return true
		}
	}
	return false
}

func ensureEvidenceHost() hostreg.Host {
	return hostreg.Host{Name: "alpha", SSH: "alpha.example"}
}

// newEnsureEvidenceManager builds the standard one-host manager over the fake
// runner, with the caller's seams installed.
func newEnsureEvidenceManager(t *testing.T, opts Options) (*Manager, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	return newTestManager(t, testRegistry(t, ensureEvidenceHost()), fr, opts), fr
}

func TestEnsureWithoutRegistryReportsHostNotFound(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, nil, fr, Options{})
	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("Ensure without a registry = %v, want ErrHostNotFound", err)
	}
}

func TestEnsureReapsReplacementClosedBetweenPublishAndValidation(t *testing.T) {
	t.Parallel()

	t.Run("fresh host", func(t *testing.T) {
		t.Parallel()
		m, fr := newEnsureEvidenceManager(t, Options{
			afterPublish: func(_ string, ch *Channel) { ch.Close() },
		})
		_, err := m.Ensure(context.Background(), "alpha")
		if !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("Ensure = %v, want ErrManagerClosed for a replacement closed after publish", err)
		}
		if ch := m.currentChannel("alpha"); ch != nil {
			t.Fatal("closed replacement stayed mapped")
		}
		if len(fr.recordedStarts()) == 0 {
			t.Fatal("the attach never ran its start")
		}
	})

	t.Run("dropped predecessor pairs its detach", func(t *testing.T) {
		t.Parallel()
		var armed atomic.Bool
		events := &ensureEvidenceEvents{}
		// The dropped predecessor's supervisor wakes on the markLost below and,
		// once the host gate frees, clears the mapping itself — a legal
		// interleaving in which the second Ensure never retires the predecessor
		// and the pairing this test pins never happens. Park the supervisor in
		// beforeSuperviseGate until the assertions are done; the deferred open
		// releases it ahead of the manager cleanup's Close.
		supGate := newGateHook()
		supGate.arm()
		defer supGate.open()
		m, _ := newEnsureEvidenceManager(t, Options{
			afterPublish: func(_ string, ch *Channel) {
				if armed.Load() {
					ch.Close()
				}
			},
			OnEvent:             events.record,
			beforeSuperviseGate: func(string, *Channel) { supGate.hook() },
		})
		first, err := m.Ensure(context.Background(), "alpha")
		if err != nil {
			t.Fatalf("first Ensure: %v", err)
		}
		// Drop the predecessor's link: it stays mapped (its supervisor owns the
		// reconnect), so the next Ensure attaches a replacement while the
		// dropped channel is the stale predecessor.
		first.markLost()
		supGate.wait(t, "the dropped predecessor's supervisor")
		armed.Store(true)

		_, err = m.Ensure(context.Background(), "alpha")
		if !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("replacement Ensure = %v, want ErrManagerClosed for a replacement closed after publish", err)
		}
		if !events.has(EventDetached, StateDisconnected) {
			t.Fatal("the announced predecessor's detach was never paired")
		}
		if ch := m.currentChannel("alpha"); ch != nil {
			t.Fatal("closed replacement stayed mapped")
		}
	})
}

func TestEnsureReportsReplacementLostBetweenPublishAndValidation(t *testing.T) {
	t.Parallel()
	var published *Channel
	m, _ := newEnsureEvidenceManager(t, Options{
		afterPublish: func(_ string, ch *Channel) {
			published = ch
			ch.markLost()
		},
	})
	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil || !errors.Is(err, ErrSSHStart) {
		t.Fatalf("Ensure = %v, want the dropped-link refusal for a replacement lost after publish", err)
	}
	if published == nil || !published.isClosed() {
		t.Fatal("replacement lost after publish was not reaped")
	}
}

func TestEnsurePairsDetachedWhenReplacementDiesAfterAnnounce(t *testing.T) {
	t.Parallel()
	var published *Channel
	var once sync.Once
	events := &ensureEvidenceEvents{}
	// The supervisor started at the announce wakes on the close and recovers
	// the host: once the host gate frees, its reconnect publishes a healthy
	// replacement, and afterPublish would overwrite published before the final
	// assertion reads it — the same race class the adjacent tests park. Park
	// the supervisor in beforeSuperviseGate until the assertions are done; the
	// deferred open releases it ahead of the manager cleanup's Close.
	supGate := newGateHook()
	supGate.arm()
	defer supGate.open()
	m, _ := newEnsureEvidenceManager(t, Options{
		afterPublish:        func(_ string, ch *Channel) { published = ch },
		beforeSuperviseGate: func(string, *Channel) { supGate.hook() },
		OnEvent: func(ev Event) {
			events.record(ev)
			if ev.Kind == EventAttached {
				once.Do(func() { published.Close() })
			}
		},
	})
	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Ensure = %v, want ErrManagerClosed for a replacement closed after its announce", err)
	}
	supGate.wait(t, "the announced replacement's supervisor")
	if !events.has(EventDetached, StateDisconnected) {
		t.Fatal("the announced replacement's detach was never paired")
	}
	if published == nil || !published.isClosed() {
		t.Fatal("replacement closed after its announce was not reaped")
	}
}

func TestEnsureRefusesReplacementLostAfterAnnounce(t *testing.T) {
	t.Parallel()
	var published *Channel
	var once sync.Once
	// The supervisor this refusal leaves in charge wakes on the announce-time
	// markLost and, once the host gate frees, closes the lost channel — a legal
	// interleaving in which published.isClosed() is true during correct
	// operation and the assertion below fails spuriously. Park the supervisor
	// in beforeSuperviseGate until the assertion is done; the deferred open
	// releases it ahead of the manager cleanup's Close.
	supGate := newGateHook()
	supGate.arm()
	defer supGate.open()
	m, _ := newEnsureEvidenceManager(t, Options{
		afterPublish:        func(_ string, ch *Channel) { published = ch },
		beforeSuperviseGate: func(string, *Channel) { supGate.hook() },
		OnEvent: func(ev Event) {
			if ev.Kind == EventAttached {
				once.Do(func() { published.markLost() })
			}
		},
	})
	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil || !errors.Is(err, ErrSSHStart) {
		t.Fatalf("Ensure = %v, want the dropped-link refusal for a replacement lost after its announce", err)
	}
	supGate.wait(t, "the refused replacement's supervisor")
	if published == nil || !published.isLost() || published.isClosed() {
		t.Fatal("replacement lost after its announce must stay with its supervisor, closed but not reaped here")
	}
}

func TestAttachUnderGateRequiresHostName(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, ensureEvidenceHost()), fr, Options{})
	_, _, err := m.AttachUnderGate(context.Background(), hostreg.Host{Name: "   "}, hostops.Holder{Kind: hostops.HolderManager, Activity: "test"}, true)
	if err == nil {
		t.Fatal("AttachUnderGate accepted a blank host name")
	}
}

func TestAttachUnderGateReapsDroppedReplacementAfterPublish(t *testing.T) {
	t.Parallel()
	var published *Channel
	m, _, host := attachUnderGateManager(t, Options{
		afterPublish: func(_ string, ch *Channel) {
			published = ch
			ch.Close()
		},
	})
	release, err := m.TryAcquire(host.Name, attachUnderGateCaller())
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer release()

	_, _, err = m.AttachUnderGate(context.Background(), host, attachUnderGateCaller(), true)
	if err == nil || !errors.Is(err, ErrSSHStart) {
		t.Fatalf("AttachUnderGate = %v, want the dropped-link refusal for a replacement closed after publish", err)
	}
	if published == nil || !published.isClosed() {
		t.Fatal("replacement closed after publish was not reaped")
	}
	if ch := m.currentChannel(host.Name); ch != nil {
		t.Fatal("closed replacement stayed mapped")
	}
}
