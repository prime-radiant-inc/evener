package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/rendezvous"
)

func writeRendezvous(t *testing.T, dir string, e rendezvous.Entry) {
	t.Helper()
	if _, err := rendezvous.Write(dir, e); err != nil {
		t.Fatalf("write rendezvous: %v", err)
	}
}

func TestRoster_LoadFromDir(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:        1001,
		Address:    "127.0.0.1:50001",
		WorkingDir: "/tmp/a",
		Model:      "gpt-5.2",
		Provider:   "openai",
		StartedAt:  time.Now().UTC(),
		SpawnedBy:  "user",
	})
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:        1002,
		Address:    "127.0.0.1:50002",
		WorkingDir: "/tmp/b",
		Model:      "claude-opus-4-7",
		Provider:   "anthropic",
		StartedAt:  time.Now().UTC(),
		SpawnedBy:  "hub",
	})

	r := NewRoster(dir, nil) // nil prober skips liveness for this test
	r.Refresh()
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
}

func TestRoster_FindBySessionID(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})
	r := NewRoster(dir, fakeProber{
		sessionID: "01SESS001",
	})
	r.Refresh()
	got, ok := r.Find("01SESS001")
	if !ok {
		t.Fatal("expected to find session")
	}
	if got.Address != "127.0.0.1:50001" {
		t.Errorf("Address: got %q", got.Address)
	}
}

func TestRoster_PrunesUnreachable(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})
	r := NewRoster(dir, fakeProber{
		shouldFail: true,
	})
	// First failure keeps the entry (no previous entry, so list is empty but not pruned yet).
	r.Refresh()
	// Second consecutive failure prunes the entry.
	r.Refresh()
	if got := r.List(); len(got) != 0 {
		t.Fatalf("expected unreachable entries to be pruned after two failures, got %d", len(got))
	}
}

func TestRoster_FindMissing(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	if _, ok := r.Find("missing"); ok {
		t.Fatal("expected missing to return false")
	}
}

func TestRoster_DefaultRunDir(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	want := filepath.Join("/tmp/fakehome", ".serf", "run")
	if got := rendezvous.DefaultDir(); got != want {
		t.Fatalf("DefaultDir: got %q want %q", got, want)
	}
}

func TestRoster_Watch_PicksUpNewFile(t *testing.T) {
	dir := t.TempDir()
	r := NewRoster(dir, fakeProber{sessionID: "01SESS001"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go r.Watch(ctx)

	// Give the watcher a moment to start.
	time.Sleep(100 * time.Millisecond)

	writeRendezvous(t, dir, rendezvous.Entry{
		PID:     1001,
		Address: "127.0.0.1:50001",
	})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.Find("01SESS001"); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("roster did not pick up the new rendezvous file")
}

// fakeProber implements liveness check for tests without real network calls.
type fakeProber struct {
	sessionID  string
	status     string
	shouldFail bool
}

func (p fakeProber) Probe(addr string) (sessionID, status string, ok bool) {
	if p.shouldFail {
		return "", "", false
	}
	return p.sessionID, p.status, true
}
