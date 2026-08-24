package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestCovPromoteQueuedAsSteerRefNotFound covers the error path where the
// ref doesn't resolve to any thread (local_daemon.go:352-353).
func TestCovPromoteQueuedAsSteerRefNotFound(t *testing.T) {
	s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return nil
	}, nil)
	_, err := s.PromoteQueuedAsSteer(context.Background(), appwire.TurnPromoteQueuedAsSteerParams{
		Ref: "local:nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}
}

// TestCovPromoteQueuedAsSteerBadRef covers the error path where the ref is
// unparseable (local_daemon.go:352, via entryForRefMode:766-767).
func TestCovPromoteQueuedAsSteerBadRef(t *testing.T) {
	s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return nil
	}, nil)
	_, err := s.PromoteQueuedAsSteer(context.Background(), appwire.TurnPromoteQueuedAsSteerParams{
		Ref: "not-a-valid-ref",
	})
	if err == nil {
		t.Fatal("expected error for bad ref")
	}
}

// TestCovPromoteQueuedAsSteerSourceMismatch covers the error path where the
// ref's source doesn't match (local_daemon.go:352, via entryForRefMode:769-770).
func TestCovPromoteQueuedAsSteerSourceMismatch(t *testing.T) {
	s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return nil
	}, nil)
	_, err := s.PromoteQueuedAsSteer(context.Background(), appwire.TurnPromoteQueuedAsSteerParams{
		Ref: "remote:thread1",
	})
	if err == nil {
		t.Fatal("expected error for source mismatch")
	}
}

// TestCovCancelQueuedRefNotFound covers the error path where the ref doesn't
// resolve to any thread (local_daemon.go:364-365).
func TestCovCancelQueuedRefNotFound(t *testing.T) {
	s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return nil
	}, nil)
	_, err := s.CancelQueued(context.Background(), appwire.TurnCancelQueuedParams{
		Ref: "local:nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}
}

// TestCovCancelQueuedBadRef covers the error path where the ref is
// unparseable (local_daemon.go:364, via entryForRefMode:766-767).
func TestCovCancelQueuedBadRef(t *testing.T) {
	s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return nil
	}, nil)
	_, err := s.CancelQueued(context.Background(), appwire.TurnCancelQueuedParams{
		Ref: "not-a-valid-ref",
	})
	if err == nil {
		t.Fatal("expected error for bad ref")
	}
}

// TestCovCancelQueuedSourceMismatch covers the error path where the ref's
// source doesn't match (local_daemon.go:364, via entryForRefMode:769-770).
func TestCovCancelQueuedSourceMismatch(t *testing.T) {
	s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return nil
	}, nil)
	_, err := s.CancelQueued(context.Background(), appwire.TurnCancelQueuedParams{
		Ref: "remote:thread1",
	})
	if err == nil {
		t.Fatal("expected error for source mismatch")
	}
}
