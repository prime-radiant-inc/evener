package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
)

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
