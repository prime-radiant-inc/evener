package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestAppThread_CarriesPendingEscalationsSnapshot(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "th_1")
	s.SetPendingEscalationsSnapshotFunc(func() []appwire.SandboxEscalationRequested {
		return []appwire.SandboxEscalationRequested{{EscalationID: "esc_1", Tool: "read_file", DeniedPath: "/x", Mode: "read-only"}}
	})
	thread := s.appThread()
	got := thread.Serf.PendingEscalations
	if len(got) != 1 || got[0].EscalationID != "esc_1" || got[0].DeniedPath != "/x" {
		t.Fatalf("appThread must carry the pending-escalation snapshot on thread/read: %+v", got)
	}
	// Stamped with this thread's identifiers so a client routes it like a live card.
	if got[0].ThreadID != "th_1" || got[0].Ref != "local:th_1" {
		t.Fatalf("snapshot escalation must be stamped with the thread's id/ref, got %+v", got[0])
	}
}

func TestHandleSandboxEscalationResolve(t *testing.T) {
	t.Parallel()

	t.Run("no callback is unavailable", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		if _, err := s.handleAppSandboxEscalationResolve(context.Background(),
			appwire.SandboxEscalationResolveParams{EscalationID: "esc_1", Approve: true}); err == nil {
			t.Fatal("want an error when no session callback is attached")
		}
	})

	t.Run("empty id is invalid", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetSandboxEscalationResolveFunc(func(string, bool) error { return nil })
		if _, err := s.handleAppSandboxEscalationResolve(context.Background(),
			appwire.SandboxEscalationResolveParams{Approve: true}); err == nil {
			t.Fatal("want an invalid-params error for an empty escalationId")
		}
	})

	t.Run("forwards id and decision", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		var gotID string
		var gotApprove bool
		s.SetSandboxEscalationResolveFunc(func(id string, approve bool) error {
			gotID, gotApprove = id, approve
			return nil
		})
		if _, err := s.handleAppSandboxEscalationResolve(context.Background(),
			appwire.SandboxEscalationResolveParams{EscalationID: "esc_1", Approve: true}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if gotID != "esc_1" || !gotApprove {
			t.Fatalf("callback received (%q, %v), want (esc_1, true)", gotID, gotApprove)
		}
	})

	t.Run("unknown id surfaces a clean error", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetSandboxEscalationResolveFunc(func(string, bool) error { return errors.New("not pending") })
		if _, err := s.handleAppSandboxEscalationResolve(context.Background(),
			appwire.SandboxEscalationResolveParams{EscalationID: "gone", Approve: false}); err == nil {
			t.Fatal("want an error surfaced for an unknown/already-resolved id")
		}
	})
}
