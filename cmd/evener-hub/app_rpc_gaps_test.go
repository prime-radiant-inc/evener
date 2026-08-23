package main

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
)

// TestBlockedUnknownMutationError covers the error construction function.
func TestBlockedUnknownMutationError(t *testing.T) {
	inner := errors.New("persistence unavailable")
	err := blockedUnknownMutationError("mutation-1", inner)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("expected WireError, got %T: %v", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("expected CodeInternalError, got %q", wire.Code)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatal("expected ErrorData")
	}
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("expected ErrorMutationOutcomeUnknown, got %q", data.EvenerErrorInfo)
	}
	if data.ClientMutationID != "mutation-1" {
		t.Fatalf("expected ClientMutationID 'mutation-1', got %q", data.ClientMutationID)
	}
	if data.MutationOutcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("expected MutationOutcomeUnknown, got %v", data.MutationOutcome)
	}
	if data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("expected RetryDispositionBlocked, got %v", data.RetryDisposition)
	}
	if data.Cause != "persistenceUnavailable" {
		t.Fatalf("expected Cause 'persistenceUnavailable', got %q", data.Cause)
	}
}

// TestHubLaunchConfigRootSet covers the path where cfg.LaunchConfigRoot is set.
func TestHubLaunchConfigRootSet(t *testing.T) {
	cfg := hubcore.WebConfig{LaunchConfigRoot: "/custom/config"}
	got := hubLaunchConfigRoot(cfg)
	if got != "/custom/config" {
		t.Fatalf("expected '/custom/config', got %q", got)
	}
}

// TestHubLaunchConfigRootUnset covers the fallback path.
func TestHubLaunchConfigRootUnset(t *testing.T) {
	cfg := hubcore.WebConfig{}
	got := hubLaunchConfigRoot(cfg)
	if got != cmdutil.DefaultConfigRoot() {
		t.Fatalf("expected default config root %q, got %q", cmdutil.DefaultConfigRoot(), got)
	}
}

// TestAllowsPastFallbackAfterLiveReadFailureNonSubscribe covers the
// non-subscribe path (returns true).
func TestAllowsPastFallbackAfterLiveReadFailureNonSubscribe(t *testing.T) {
	// This tests the function logic; we can't create a real Source in the
	// main package test, but we can test the logic with nil source.
	// For non-subscribe, it returns true regardless of source.
	got := allowsPastFallbackAfterLiveReadFailure(nil, appwire.ThreadReadParams{Subscribe: false}, nil)
	if !got {
		t.Fatal("non-subscribe should allow past fallback")
	}
}

// TestAllowsPastFallbackAfterLiveReadFailureSubscribeNilSource covers the
// subscribe path with nil source (not a RelaySessionSource, so returns true).
func TestAllowsPastFallbackAfterLiveReadFailureSubscribeNilSource(t *testing.T) {
	got := allowsPastFallbackAfterLiveReadFailure(nil, appwire.ThreadReadParams{Subscribe: true}, nil)
	// nil source doesn't implement RelaySessionSource, so requiresLiveHandoff=false
	// → returns true
	if !got {
		t.Fatal("subscribe with non-relay source should allow past fallback")
	}
}
