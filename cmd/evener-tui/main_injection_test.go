package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/cmd/evener-tui/internal/hubstart"
)

// TestRunHelpWritesUsageToInjectedStderr pins the injected-stream contract:
// flag help/usage must reach the caller-supplied stderr, not os.Stderr.
func TestRunHelpWritesUsageToInjectedStderr(t *testing.T) {
	oldGetenv := processGetenv
	processGetenv = func(string) string { return "" }
	t.Cleanup(func() { processGetenv = oldGetenv })

	var stderr bytes.Buffer
	if code := Run([]string{"-h"}, nil, io.Discard, &stderr); code != 0 {
		t.Fatalf("Run(-h) = %d, want 0", code)
	}
	if got := stderr.String(); !strings.Contains(got, "Usage: evener tui") {
		t.Fatalf("help usage did not reach injected stderr: %q", got)
	}
}

// TestRunParseDiagnosticsStayInInjectedStderr pins that the flag package's own
// diagnostic (which it writes to the FlagSet output) lands in the injected
// buffer alongside Run's own error line.
func TestRunParseDiagnosticsStayInInjectedStderr(t *testing.T) {
	oldGetenv := processGetenv
	processGetenv = func(string) string { return "" }
	t.Cleanup(func() { processGetenv = oldGetenv })

	var stderr bytes.Buffer
	if code := Run([]string{"--no-such-flag"}, nil, io.Discard, &stderr); code != 2 {
		t.Fatalf("Run(--no-such-flag) = %d, want 2", code)
	}
	got := stderr.String()
	if !strings.HasPrefix(got, "flag provided but not defined") {
		t.Fatalf("flag package diagnostic did not start injected stderr: %q", got)
	}
	if !strings.Contains(got, "Usage: evener tui") {
		t.Fatalf("flag error did not print usage to injected stderr: %q", got)
	}
	if !strings.Contains(got, "evener-tui: flag provided but not defined") {
		t.Fatalf("run error line missing from injected stderr: %q", got)
	}
}

// TestRunDoesNotSwapPackageIOSeams is the deterministic (race-free) proof that
// Run no longer mutates the package-level stdio seams. A dependency hook
// observes standardError while Run is in flight; it must still be the original.
func TestRunDoesNotSwapPackageIOSeams(t *testing.T) {
	oldErr, oldDirs, oldGetenv := standardError, ensureUserConfigDirs, processGetenv
	var observed io.Writer
	ensureUserConfigDirs = func() error {
		observed = standardError
		return io.ErrClosedPipe
	}
	processGetenv = func(string) string { return "" }
	t.Cleanup(func() {
		standardError, ensureUserConfigDirs, processGetenv = oldErr, oldDirs, oldGetenv
	})

	injected := &bytes.Buffer{}
	if code := Run([]string{"--state-dir=x"}, nil, io.Discard, injected); code != 1 {
		t.Fatalf("Run with dirs error = %d, want 1", code)
	}
	if observed != oldErr {
		t.Fatalf("Run swapped standardError: observed %p, want original %p", observed, oldErr)
	}
}

// TestRunPassesInjectedReaderToProgram pins tea.WithInput for a supplied
// reader: the custom input option appears exactly when stdin is non-nil.
func TestRunPassesInjectedReaderToProgram(t *testing.T) {
	oldGetenv, oldDirs, oldStart := processGetenv, ensureUserConfigDirs, startHubClient
	oldProbe, oldInit, oldApply, oldReset, oldProgram := probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram
	processGetenv = func(string) string { return "" }
	ensureUserConfigDirs = func() error { return nil }
	startHubClient = func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
		return hubstart.HubRuntime{Address: hubstart.HubAddress{BaseURL: "http://hub"}}, nil
	}
	probeTerminalDefaults = func() bool { return true }
	initThemeFromStateDir = func(string) {}
	applyTerminalBg = func() {}
	resetTerminalBg = func() {}
	var optsLen int
	newTUIProgram = func(model tea.Model, opts ...tea.ProgramOption) tuiProgram {
		optsLen = len(opts)
		return &scriptedCovProgram{model: model}
	}
	t.Cleanup(func() {
		processGetenv, ensureUserConfigDirs, startHubClient = oldGetenv, oldDirs, oldStart
		probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram = oldProbe, oldInit, oldApply, oldReset, oldProgram
	})

	if code := Run([]string{"--state-dir=x", "--debug"}, strings.NewReader("q"), io.Discard, io.Discard); code != 0 {
		t.Fatalf("Run with injected reader = %d, want 0", code)
	}
	if optsLen != 1 {
		t.Fatalf("program opts with injected reader = %d, want 1 (WithInput)", optsLen)
	}

	if code := Run([]string{"--state-dir=x", "--debug"}, nil, io.Discard, io.Discard); code != 0 {
		t.Fatalf("Run with nil reader = %d, want 0", code)
	}
	if optsLen != 0 {
		t.Fatalf("program opts with nil reader = %d, want 0 (no WithInput)", optsLen)
	}
}

// TestRunConcurrentCallsDoNotRace runs two independent Run calls at once; the
// race detector must find no shared-state write between them.
func TestRunConcurrentCallsDoNotRace(t *testing.T) {
	oldGetenv := processGetenv
	processGetenv = func(string) string { return "" }
	t.Cleanup(func() { processGetenv = oldGetenv })

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var stderr bytes.Buffer
			if code := Run([]string{"-h"}, nil, io.Discard, &stderr); code != 0 {
				t.Errorf("concurrent Run(-h) = %d, want 0", code)
			}
		}()
	}
	wg.Wait()
}
