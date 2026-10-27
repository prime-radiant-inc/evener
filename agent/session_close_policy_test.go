package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// releaseObservation is what a caller can see of one release pass: the events
// that reached the stream in order, the settled flags, and the returned error.
type releaseObservation struct {
	kinds             []events.SessionEvent
	state             SessionState
	closing           bool
	sessionEndLatched bool
	err               error
}

func observeRelease(t *testing.T, s *Session, options closeOptions, policy runtimeReleasePolicy) releaseObservation {
	t.Helper()
	collected := make(chan []events.SessionEvent, 1)
	go func() {
		var kinds []events.SessionEvent
		for ev := range s.Events() {
			kinds = append(kinds, ev)
		}
		collected <- kinds
	}()
	err := s.releaseRuntime(context.Background(), options, policy)
	kinds := <-collected // returns only once the stream is closed
	s.mu.Lock()
	defer s.mu.Unlock()
	return releaseObservation{kinds: kinds, state: s.state, closing: s.closing, sessionEndLatched: s.sessionEndEmitted, err: err}
}

// TestReleaseRuntimePolicyOutcomes pins the observable result of the single
// teardown pass under each policy: whether the terminal boundary is announced,
// where it lands in the stream, the final state, and that the stream always
// closes.
func TestReleaseRuntimePolicyOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		policy        runtimeReleasePolicy
		options       closeOptions
		alreadyEnded  bool
		wantEnd       int
		wantClosedSet bool
	}{
		{name: "terminal announces session end", policy: releaseTerminal, options: closeOptions{cleanupEnv: true}, wantEnd: 1, wantClosedSet: true},
		{name: "terminal after an end already emitted stays silent", policy: releaseTerminal, options: closeOptions{cleanupEnv: true}, alreadyEnded: true, wantEnd: 0, wantClosedSet: true},
		{name: "forced terminal re-announces", policy: releaseTerminal, options: closeOptions{cleanupEnv: true, forceTerminal: true}, alreadyEnded: true, wantEnd: 1, wantClosedSet: true},
		{name: "child close without env cleanup", policy: releaseTerminal, options: closeOptions{}, wantEnd: 1, wantClosedSet: true},
		{name: "retirement announces nothing and keeps state", policy: releaseRetirement, options: closeOptions{}, wantEnd: 0, wantClosedSet: false},
		{name: "retirement with env cleanup", policy: releaseRetirement, options: closeOptions{cleanupEnv: true}, wantEnd: 0, wantClosedSet: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newQueuePersistTestSession(t, t.TempDir())
			if tc.alreadyEnded {
				s.mu.Lock()
				s.sessionEndEmitted = true
				s.mu.Unlock()
			}
			before := s.State()
			got := observeRelease(t, s, tc.options, tc.policy)
			if got.err != nil {
				t.Fatalf("release error: %v", got.err)
			}
			if n := countKind(got.kinds, events.EventSessionEnd); n != tc.wantEnd {
				t.Fatalf("session end events = %d, want %d (kinds %v)", n, tc.wantEnd, got.kinds)
			}
			if tc.wantEnd == 1 && got.kinds[len(got.kinds)-1].Kind != events.EventSessionEnd {
				t.Fatalf("session end is not the last event: %v", got.kinds)
			}
			if !got.closing {
				t.Fatal("closing flag not set")
			}
			if !got.sessionEndLatched {
				t.Fatal("session end latch not set")
			}
			if tc.wantClosedSet && got.state != SessionClosed {
				t.Fatalf("state = %s, want closed", got.state)
			}
			if !tc.wantClosedSet && got.state != before {
				t.Fatalf("retirement changed state %s -> %s", before, got.state)
			}
		})
	}
}

// TestRetirementReleaseTranscriptFaultKeepsTranscriptOpen pins the order
// between the transcript close and everything after it: a fault at the
// transcript-close boundary surfaces as the release error, the transcript is
// left open for the caller, and the stream still closes. A terminal pass
// never consults the fault.
func TestRetirementReleaseTranscriptFaultKeepsTranscriptOpen(t *testing.T) {
	injected := errors.New("injected transcript close fault")
	restore := setRetirementReleaseFault(func(point string) error {
		if point == "transcript_close" {
			return injected
		}
		return nil
	})
	t.Cleanup(restore)

	retired := newQueuePersistTestSession(t, t.TempDir())
	got := observeRelease(t, retired, closeOptions{}, releaseRetirement)
	if !errors.Is(got.err, injected) {
		t.Fatalf("release error = %v, want the injected fault", got.err)
	}
	if err := retired.closeAttachedTranscript(); err != nil {
		t.Fatalf("transcript was closed despite the fault: %v", err)
	}

	terminal := newQueuePersistTestSession(t, t.TempDir())
	got = observeRelease(t, terminal, closeOptions{cleanupEnv: true}, releaseTerminal)
	if got.err != nil {
		t.Fatalf("terminal close consulted the retirement fault: %v", got.err)
	}
	if countKind(got.kinds, events.EventSessionEnd) != 1 {
		t.Fatalf("terminal close under fault lost its session end: %v", got.kinds)
	}
}
