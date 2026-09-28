package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

func TestHandleDelegateStop(t *testing.T) {
	t.Parallel()

	t.Run("no callback is unavailable", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		if _, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_1"}); err == nil {
			t.Fatal("want an error when no session callback is attached")
		}
	})

	t.Run("an empty delegate id is invalid", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		s.SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error) { return appwire.DelegateStopStopping, nil })
		_, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: "  "})
		if wireErrorInfo(err) != appwire.ErrorInvalidParams {
			t.Fatalf("empty delegateId = %v, want invalid params", err)
		}
	})

	t.Run("only the root that owns the tree is a target", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		s.SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error) {
			t.Fatal("a stop aimed at another thread reached the session")
			return "", nil
		})
		if _, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:child", DelegateID: "dlg_1"}); err == nil {
			t.Fatal("want a stop aimed at a subagent's own thread refused")
		}
	})

	t.Run("forwards the delegate and answers its outcome", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		var got string
		s.SetDelegateStopFunc(func(delegateID string) (appwire.DelegateStopOutcome, error) {
			got = delegateID
			return appwire.DelegateStopNotRunning, nil
		})
		resp, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: " dlg_1 "})
		if err != nil || resp.Outcome != appwire.DelegateStopNotRunning || got != "dlg_1" {
			t.Fatalf("stop = %+v, %v with delegate %q; want notRunning for dlg_1", resp, err, got)
		}
	})

	t.Run("an unknown delegate is not found", func(t *testing.T) {
		s := NewServer(ServerConfig{})
		s.SetAppIdentity("local", "root")
		s.SetDelegateStopFunc(func(delegateID string) (appwire.DelegateStopOutcome, error) {
			return "", fmt.Errorf("%w %q", agent.ErrUnknownDelegate, delegateID)
		})
		_, err := s.handleAppDelegateStop(context.Background(), appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_gone"})
		if wireErrorInfo(err) != appwire.ErrorResourceNotFound {
			t.Fatalf("unknown delegate = %v, want resourceNotFound", err)
		}
	})
}

// The root advertises the stop while its daemon wires it, so a client offers
// "Stop subagent" instead of asking the coordinator (S6).
func TestRootCapabilitiesAdvertiseStopSubagent(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "root")
	if s.appThread().Evener.Capabilities.StopSubagent {
		t.Fatal("a daemon without the stop wired advertises it")
	}
	s.SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error) { return appwire.DelegateStopStopping, nil })
	if !s.appThread().Evener.Capabilities.StopSubagent {
		t.Fatal("a daemon with the stop wired does not advertise it")
	}
}

// wireErrorInfo is the evener error kind a handler's error carries, "" when
// it is not a wire error.
func wireErrorInfo(err error) appwire.ErrorInfo {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return ""
	}
	data, _ := wire.Data.(appwire.ErrorData)
	return data.EvenerErrorInfo
}
