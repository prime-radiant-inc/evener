package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"primeradiant.com/serf/rendezvous"
)

// HubSpawner fulfills the Spawner interface using SpawnDaemon.
type HubSpawner struct {
	Cfg        Config
	SerfBinary string // path to the serf binary; "" → "serf" on PATH
	RunDir     string
}

func (h *HubSpawner) Templates() []SpawnTemplate {
	return h.Cfg.SpawnTemplates
}

func (h *HubSpawner) Spawn(ctx context.Context, templateName, workingDir string) (rendezvous.Entry, error) {
	t, ok := findTemplate(h.Cfg, templateName)
	if !ok {
		return rendezvous.Entry{}, fmt.Errorf("template %q not found", templateName)
	}
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return SpawnDaemon(ctx, h.SerfBinary, h.RunDir, t, workingDir, timeout)
}

func (h *HubSpawner) Resume(ctx context.Context, sessionID string) (rendezvous.Entry, error) {
	timeout := h.Cfg.SpawnTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return ResumeDaemon(ctx, h.SerfBinary, h.RunDir, sessionID, timeout)
}

// findTemplate returns the named SpawnTemplate from the config.
func findTemplate(cfg Config, name string) (SpawnTemplate, bool) {
	for _, t := range cfg.SpawnTemplates {
		if t.Name == name {
			return t, true
		}
	}
	return SpawnTemplate{}, false
}

// buildSpawnArgs assembles the arg slice for `serf serve` from a template
// and a working directory.
//
// Always passes --addr 127.0.0.1:0 so the daemon binds an ephemeral port,
// which it reports via its rendezvous file. Empty fields in the template
// are omitted so `serf serve` can fall back to its environment.
func buildSpawnArgs(t SpawnTemplate, workingDir string) []string {
	args := []string{"--addr", "127.0.0.1:0"}
	if workingDir != "" {
		args = append(args, "--dir", workingDir)
	}
	if t.Provider != "" {
		args = append(args, "--provider", t.Provider)
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Agent != "" {
		args = append(args, "--agent", t.Agent)
	}
	if t.ReasoningEffort != "" {
		args = append(args, "--reasoning-effort", t.ReasoningEffort)
	}
	return args
}

// SpawnDaemon launches a `serf serve` subprocess from the given template,
// then waits up to timeout for its rendezvous file to appear.
//
// Returns the rendezvous Entry on success, or error on timeout / spawn failure.
// Caller does NOT manage the subprocess lifecycle — the spawned daemon
// runs independently and lives until killed or sent /shutdown.
func SpawnDaemon(ctx context.Context, serfBinary string, runDir string, t SpawnTemplate, workingDir string, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := append([]string{"serve"}, buildSpawnArgs(t, workingDir)...)

	cmd := exec.Command(serfBinary, args...)
	cmd.Env = append(os.Environ(), "SERF_HUB_SPAWNED=1")
	cmd.Stdout = os.Stderr // forward to hub stderr for now
	cmd.Stderr = os.Stderr

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, err := WaitForRendezvous(waitCtx, runDir, cmd.Process.Pid, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		return rendezvous.Entry{}, fmt.Errorf("daemon spawn timed out: %w", err)
	}
	return entry, nil
}

// WaitOption configures WaitForRendezvous.
type WaitOption func(*waitConfig)

type waitConfig struct {
	startedAfter time.Time
}

// WithStartedAfter rejects rendezvous entries whose StartedAt is on or
// before t. Use this to defend against a recycled PID matching a stale
// entry from a previously-crashed daemon.
func WithStartedAfter(t time.Time) WaitOption {
	return func(c *waitConfig) { c.startedAfter = t }
}

// WaitForRendezvous polls runDir for a rendezvous Entry whose PID matches.
// Returns when found, or when ctx is canceled.
func WaitForRendezvous(ctx context.Context, runDir string, pid int, opts ...WaitOption) (rendezvous.Entry, error) {
	cfg := waitConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.PID != pid {
				continue
			}
			if !cfg.startedAfter.IsZero() && !e.StartedAt.After(cfg.startedAfter) {
				continue
			}
			return e, nil
		}
		select {
		case <-ctx.Done():
			return rendezvous.Entry{}, errors.New("timeout waiting for rendezvous")
		case <-ticker.C:
		}
	}
}

// ResumeDaemon launches `serf serve --resume <sessionID>` and waits for
// rendezvous. Returns the new daemon's rendezvous Entry.
//
// Note: resume always creates a NEW session_id (the daemon mints a fresh
// one). Caller resolves it via roster lookup after rendezvous appears.
func ResumeDaemon(ctx context.Context, serfBinary, runDir, sessionID string, timeout time.Duration) (rendezvous.Entry, error) {
	if serfBinary == "" {
		serfBinary = "serf"
	}
	args := []string{"serve", "--addr", "127.0.0.1:0", "--resume", sessionID}
	cmd := exec.Command(serfBinary, args...)
	cmd.Env = append(os.Environ(), "SERF_HUB_SPAWNED=1")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return rendezvous.Entry{}, fmt.Errorf("start daemon: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entry, err := WaitForRendezvous(waitCtx, runDir, cmd.Process.Pid, WithStartedAfter(startedAt))
	if err != nil {
		_ = cmd.Process.Kill()
		return rendezvous.Entry{}, fmt.Errorf("resume timed out: %w", err)
	}
	return entry, nil
}
