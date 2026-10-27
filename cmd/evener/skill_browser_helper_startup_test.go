//go:build browserguard

package main

import (
	"errors"
	"testing"
)

func TestSkillBrowserRendezvousReportsServeFailure(t *testing.T) {
	startupErr := errors.New("sandbox backend unavailable")
	serveDone := make(chan error, 1)
	serveDone <- startupErr

	entry, err := waitForSkillBrowserRendezvous(t.TempDir(), serveDone)
	if !errors.Is(err, startupErr) {
		t.Fatalf("readiness lost the Serve error: got %v, want %v", err, startupErr)
	}
	if entry.SessionID != "" {
		t.Fatalf("failed startup returned a session: %+v", entry)
	}
}

func TestSkillBrowserRendezvousRejectsCleanEarlyExit(t *testing.T) {
	serveDone := make(chan error, 1)
	serveDone <- nil

	entry, err := waitForSkillBrowserRendezvous(t.TempDir(), serveDone)
	if err == nil || entry.SessionID != "" {
		t.Fatalf("early exit must fail readiness: entry=%+v err=%v", entry, err)
	}
	if len(serveDone) != 0 {
		t.Fatal("readiness did not observe the daemon's exit")
	}
}
