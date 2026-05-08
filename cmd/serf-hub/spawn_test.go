package main

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/rendezvous"
)

func TestFindTemplate(t *testing.T) {
	cfg := Config{
		SpawnTemplates: []SpawnTemplate{
			{Name: "code, gpt", Provider: "openai", Model: "gpt-5.2", Agent: "default"},
			{Name: "review, claude", Provider: "anthropic", Model: "claude-opus-4-7"},
		},
	}
	got, ok := findTemplate(cfg, "code, gpt")
	if !ok {
		t.Fatal("expected to find template")
	}
	if got.Model != "gpt-5.2" {
		t.Errorf("Model: %q", got.Model)
	}
}

func TestFindTemplate_Missing(t *testing.T) {
	cfg := Config{}
	if _, ok := findTemplate(cfg, "nope"); ok {
		t.Fatal("expected not-found")
	}
}

func TestBuildSpawnArgs(t *testing.T) {
	tmpl := SpawnTemplate{
		Provider:        "openai",
		Model:           "gpt-5.2",
		Agent:           "default",
		ReasoningEffort: "medium",
	}
	args := buildSpawnArgs(tmpl, "/Users/jesse/git/foo")
	want := map[string]string{
		"--provider":         "openai",
		"--model":            "gpt-5.2",
		"--agent":            "default",
		"--reasoning-effort": "medium",
		"--dir":              "/Users/jesse/git/foo",
		"--addr":             "127.0.0.1:0",
	}
	got := pairsToMap(args)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("arg %s: got %q, want %q", k, got[k], v)
		}
	}
}

// pairsToMap collapses ["--k", "v", ...] to {"--k": "v"} for assertions.
func pairsToMap(args []string) map[string]string {
	out := make(map[string]string)
	for i := 0; i+1 < len(args); i += 2 {
		out[args[i]] = args[i+1]
	}
	return out
}

func TestWaitForRendezvous_AppearsInTime(t *testing.T) {
	dir := t.TempDir()

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = rendezvous.Write(dir, rendezvous.Entry{
			PID:     12345,
			Address: "127.0.0.1:50000",
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := WaitForRendezvous(ctx, dir, 12345)
	if err != nil {
		t.Fatalf("WaitForRendezvous: %v", err)
	}
	if got.Address != "127.0.0.1:50000" {
		t.Errorf("Address: %q", got.Address)
	}
}

func TestWaitForRendezvous_TimesOut(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := WaitForRendezvous(ctx, dir, 99999)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForRendezvous_WrongPID(t *testing.T) {
	dir := t.TempDir()
	_, _ = rendezvous.Write(dir, rendezvous.Entry{PID: 11111, Address: "x"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := WaitForRendezvous(ctx, dir, 22222); err == nil {
		t.Fatal("expected timeout for wrong PID")
	}

}

// A stale rendezvous file from a dead process whose PID was reused must not
// match our newly-spawned daemon. WaitForRendezvous filters by startedAfter.
func TestWaitForRendezvous_IgnoresStaleEntryFromBeforeStart(t *testing.T) {
	dir := t.TempDir()

	// Stale entry: same PID, but written before our spawn time.
	_, _ = rendezvous.Write(dir, rendezvous.Entry{
		PID:       55555,
		Address:   "127.0.0.1:11111",
		StartedAt: time.Now().Add(-1 * time.Hour),
	})

	startedAfter := time.Now()

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = rendezvous.Write(dir, rendezvous.Entry{
			PID:       55555,
			Address:   "127.0.0.1:22222",
			StartedAt: time.Now(),
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := WaitForRendezvous(ctx, dir, 55555, WithStartedAfter(startedAfter))
	if err != nil {
		t.Fatalf("WaitForRendezvous: %v", err)
	}
	if got.Address != "127.0.0.1:22222" {
		t.Errorf("matched stale entry: address=%q", got.Address)
	}
}
