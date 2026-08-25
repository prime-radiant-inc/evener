package appsource

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func covMutationSource() *LocalDaemonSource {
	return NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://local-present",
			SourceID:  "local",
			ThreadID:  "present",
			SessionID: "present",
		}}}
	}, nil)
}

func requireCovMutationError(t *testing.T, err error, want string, unavailable bool) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	var wire appwire.WireError
	if got := errors.As(err, &wire); got != unavailable {
		t.Fatalf("errors.As(WireError) = %v, want %v (error %T)", got, unavailable, err)
	}
	if unavailable {
		wantWire := appwire.SessionUnavailable(want)
		if wire.Code != wantWire.Code || wire.Message != wantWire.Message || wire.Data != wantWire.Data {
			t.Fatalf("wire error = %#v, want %#v", wire, wantWire)
		}
	}
}

// TestCovPromoteQueuedAsSteerRefNotFound covers the error path where the
// ref doesn't resolve to any thread (local_daemon.go:352-353).
func TestCovPromoteQueuedAsSteerRefNotFound(t *testing.T) {
	s := covMutationSource()
	_, err := s.PromoteQueuedAsSteer(t.Context(), appwire.TurnPromoteQueuedAsSteerParams{
		Ref: "local:missing",
	})
	requireCovMutationError(t, err, "thread not found: missing", true)
}

// TestCovPromoteQueuedAsSteerBadRef covers the error path where the ref is
// unparseable (local_daemon.go:352, via entryForRefMode:766-767).
func TestCovPromoteQueuedAsSteerBadRef(t *testing.T) {
	s := covMutationSource()
	_, err := s.PromoteQueuedAsSteer(t.Context(), appwire.TurnPromoteQueuedAsSteerParams{
		Ref: "not-a-valid-ref",
	})
	requireCovMutationError(t, err, `invalid ref "not-a-valid-ref"`, false)
}

// TestCovPromoteQueuedAsSteerSourceMismatch covers the error path where the
// ref's source doesn't match (local_daemon.go:352, via entryForRefMode:769-770).
func TestCovPromoteQueuedAsSteerSourceMismatch(t *testing.T) {
	s := covMutationSource()
	_, err := s.PromoteQueuedAsSteer(t.Context(), appwire.TurnPromoteQueuedAsSteerParams{
		Ref: "remote:present",
	})
	requireCovMutationError(t, err, "source not found: remote", false)
}

// TestCovCancelQueuedRefNotFound covers the error path where the ref doesn't
// resolve to any thread (local_daemon.go:364-365).
func TestCovCancelQueuedRefNotFound(t *testing.T) {
	s := covMutationSource()
	_, err := s.CancelQueued(t.Context(), appwire.TurnCancelQueuedParams{
		Ref: "local:missing",
	})
	requireCovMutationError(t, err, "thread not found: missing", true)
}

// TestCovCancelQueuedBadRef covers the error path where the ref is
// unparseable (local_daemon.go:364, via entryForRefMode:766-767).
func TestCovCancelQueuedBadRef(t *testing.T) {
	s := covMutationSource()
	_, err := s.CancelQueued(t.Context(), appwire.TurnCancelQueuedParams{
		Ref: "not-a-valid-ref",
	})
	requireCovMutationError(t, err, `invalid ref "not-a-valid-ref"`, false)
}

// TestCovCancelQueuedSourceMismatch covers the error path where the ref's
// source doesn't match (local_daemon.go:364, via entryForRefMode:769-770).
func TestCovCancelQueuedSourceMismatch(t *testing.T) {
	s := covMutationSource()
	_, err := s.CancelQueued(t.Context(), appwire.TurnCancelQueuedParams{
		Ref: "remote:present",
	})
	requireCovMutationError(t, err, "source not found: remote", false)
}
