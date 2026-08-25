package tui

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/cmd/evener-tui/internal/hubstart"
)

// scriptedCovProgram is a minimal tuiProgram for testing run().
type scriptedCovProgram struct {
	model    tea.Model
	err      error
	runCalls *int
}

func (p *scriptedCovProgram) Run() (tea.Model, error) {
	if p.runCalls != nil {
		*p.runCalls++
	}
	return p.model, p.err
}
func (p *scriptedCovProgram) Send(tea.Msg) {}

// ---- Run: flag.ErrHelp returns 0 --------------------------------------------

func TestCovRun_HelpFlagReturns0(t *testing.T) {
	oldParse := parseStartupOptions
	parseStartupOptions = func(args []string, getenv func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{}, flag.ErrHelp
	}
	t.Cleanup(func() { parseStartupOptions = oldParse })
	if code := Run([]string{"-h"}, nil, io.Discard, io.Discard); code != 0 {
		t.Fatalf("Run with -h = %d, want 0", code)
	}
}

// ---- Run: parse error returns 2 --------------------------------------------

func TestCovRun_ParseErrorReturns2(t *testing.T) {
	oldParse := parseStartupOptions
	parseStartupOptions = func(args []string, getenv func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{}, errors.New("bad flag")
	}
	t.Cleanup(func() { parseStartupOptions = oldParse })
	var stderr bytes.Buffer
	if code := Run([]string{"--bad"}, nil, io.Discard, &stderr); code != 2 {
		t.Fatalf("Run with bad flag = %d, want 2", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("bad flag")) {
		t.Fatalf("stderr should contain error: %q", stderr.String())
	}
}

// ---- run(): ensureUserConfigDirs error returns 1 ----------------------------

func TestCovRun_EnsureDirsErrorReturns1(t *testing.T) {
	oldArgs, oldErr, oldParse, oldDirs := processArgs, standardError, parseStartupOptions, ensureUserConfigDirs
	processArgs = func() []string { return []string{"evener-tui"} }
	var stderr bytes.Buffer
	standardError = &stderr
	parseStartupOptions = func([]string, func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{StateDir: "x"}, nil
	}
	ensureUserConfigDirs = func() error { return errors.New("dirs error") }
	t.Cleanup(func() {
		processArgs, standardError = oldArgs, oldErr
		parseStartupOptions, ensureUserConfigDirs = oldParse, oldDirs
	})
	if code := run(); code != 1 {
		t.Fatalf("run with dirs error = %d, want 1", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("dirs error")) {
		t.Fatalf("stderr should contain dirs error: %q", stderr.String())
	}
}

// ---- run(): hub connect error returns 1 -------------------------------------

func TestCovRun_HubErrorReturns1(t *testing.T) {
	oldArgs, oldErr, oldParse, oldDirs, oldWarm, oldStart := processArgs, standardError, parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient
	processArgs = func() []string { return []string{"evener-tui"} }
	var stderr bytes.Buffer
	standardError = &stderr
	parseStartupOptions = func([]string, func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{StateDir: "x"}, nil
	}
	ensureUserConfigDirs = func() error { return nil }
	warmModelCatalog = func() {}
	startHubClient = func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
		return hubstart.HubRuntime{}, errors.New("hub unreachable")
	}
	t.Cleanup(func() {
		processArgs, standardError = oldArgs, oldErr
		parseStartupOptions, ensureUserConfigDirs = oldParse, oldDirs
		warmModelCatalog, startHubClient = oldWarm, oldStart
	})
	if code := run(); code != 1 {
		t.Fatalf("run with hub error = %d, want 1", code)
	}
}

// ---- run(): program.Run error returns 1 -------------------------------------

func TestCovRun_ProgramErrorReturns1(t *testing.T) {
	oldArgs, oldErr, oldParse, oldDirs, oldWarm, oldStart := processArgs, standardError, parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient
	oldProbe, oldInit, oldApply, oldReset, oldProgram := probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram
	processArgs = func() []string { return []string{"evener-tui"} }
	var stderr bytes.Buffer
	standardError = &stderr
	parseStartupOptions = func([]string, func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{StateDir: "x"}, nil
	}
	ensureUserConfigDirs = func() error { return nil }
	warmModelCatalog = func() {}
	startHubClient = func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
		return hubstart.HubRuntime{Address: hubstart.HubAddress{BaseURL: "http://hub"}}, nil
	}
	probeTerminalDefaults = func() bool { return true }
	initThemeFromStateDir = func(string) {}
	applyTerminalBg = func() {}
	resetTerminalBg = func() {}
	newTUIProgram = func(model tea.Model, opts ...tea.ProgramOption) tuiProgram {
		return &scriptedCovProgram{model: model, err: errors.New("program crash")}
	}
	t.Cleanup(func() {
		processArgs, standardError = oldArgs, oldErr
		parseStartupOptions, ensureUserConfigDirs = oldParse, oldDirs
		warmModelCatalog, startHubClient = oldWarm, oldStart
		probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram = oldProbe, oldInit, oldApply, oldReset, oldProgram
	})
	if code := run(); code != 1 {
		t.Fatalf("run with program error = %d, want 1", code)
	}
	if got, want := stderr.String(), "evener-tui: program crash\n"; got != want {
		t.Fatalf("program error stderr = %q, want %q", got, want)
	}
}

// ---- run(): success returns 0 and prints post-quit message -------------------

func TestCovRun_SuccessReturns0(t *testing.T) {
	oldArgs, oldOut, oldErr, oldParse, oldDirs, oldWarm, oldStart := processArgs, standardOutput, standardError, parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient
	oldProbe, oldInit, oldApply, oldReset, oldProgram := probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram
	processArgs = func() []string { return []string{"evener-tui"} }
	var stdout, stderr bytes.Buffer
	standardOutput = &stdout
	standardError = &stderr
	parseStartupOptions = func([]string, func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{StateDir: "x"}, nil
	}
	ensureUserConfigDirs = func() error { return nil }
	warmModelCatalog = func() {}
	startHubClient = func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
		return hubstart.HubRuntime{Address: hubstart.HubAddress{BaseURL: "http://hub"}}, nil
	}
	probeTerminalDefaults = func() bool { return true }
	initThemeFromStateDir = func(string) {}
	applyTerminalBg = func() {}
	resetTerminalBg = func() {}
	newTUIProgram = func(model tea.Model, opts ...tea.ProgramOption) tuiProgram {
		m := model.(hubModel)
		m.postQuitMessage = "  goodbye  "
		return &scriptedCovProgram{model: m, err: nil}
	}
	t.Cleanup(func() {
		processArgs, standardOutput, standardError = oldArgs, oldOut, oldErr
		parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient = oldParse, oldDirs, oldWarm, oldStart
		probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram = oldProbe, oldInit, oldApply, oldReset, oldProgram
	})
	if code := run(); code != 0 {
		t.Fatalf("run success = %d, want 0", code)
	}
	if got, want := stdout.String(), "goodbye\n"; got != want {
		t.Fatalf("success stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("success stderr = %q, want empty", stderr.String())
	}
}

// ---- run(): debug mode skips alt screen -------------------------------------

func TestCovRun_DebugModeNoAltScreen(t *testing.T) {
	oldArgs, oldErr, oldParse, oldDirs, oldWarm, oldStart := processArgs, standardError, parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient
	oldProbe, oldInit, oldApply, oldReset, oldProgram := probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram
	processArgs = func() []string { return []string{"evener-tui"} }
	var stderr bytes.Buffer
	standardError = &stderr
	parseStartupOptions = func([]string, func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{StateDir: "x", Debug: true}, nil
	}
	ensureUserConfigDirs = func() error { return nil }
	warmModelCatalog = func() {}
	startHubClient = func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
		return hubstart.HubRuntime{Address: hubstart.HubAddress{BaseURL: "http://hub"}}, nil
	}
	probeTerminalDefaults = func() bool { return true }
	initThemeFromStateDir = func(string) {}
	applyTerminalBg = func() {}
	resetTerminalBg = func() {}
	var programOptsCount, constructorCalls, programRunCalls int
	newTUIProgram = func(model tea.Model, opts ...tea.ProgramOption) tuiProgram {
		constructorCalls++
		programOptsCount = len(opts)
		return &scriptedCovProgram{model: model, runCalls: &programRunCalls}
	}
	t.Cleanup(func() {
		processArgs, standardError = oldArgs, oldErr
		parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient = oldParse, oldDirs, oldWarm, oldStart
		probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram = oldProbe, oldInit, oldApply, oldReset, oldProgram
	})
	if code := run(); code != 0 {
		t.Fatalf("debug run = %d, want 0; stderr=%q", code, stderr.String())
	}
	if constructorCalls != 1 || programRunCalls != 1 {
		t.Fatalf("constructor calls=%d program Run calls=%d, want 1 each", constructorCalls, programRunCalls)
	}
	if programOptsCount != 0 {
		t.Fatalf("debug mode should pass 0 program opts (no alt screen), got %d", programOptsCount)
	}
}

// ---- currentExecutable -------------------------------------------------------

func TestCovCurrentExecutable_UsesProcessExecutable(t *testing.T) {
	oldExec := processExecutable
	oldArgs := processArgs
	processExecutable = func() (string, error) { return "/usr/local/bin/evener-tui", nil }
	processArgs = func() []string { return []string{"./evener-tui"} }
	t.Cleanup(func() {
		processExecutable = oldExec
		processArgs = oldArgs
	})
	if got := currentExecutable(); got != "/usr/local/bin/evener-tui" {
		t.Fatalf("currentExecutable = %q, want /usr/local/bin/evener-tui", got)
	}
}

func TestCovCurrentExecutable_FallsBackToArgs(t *testing.T) {
	oldExec := processExecutable
	oldArgs := processArgs
	processExecutable = func() (string, error) { return "", io.ErrUnexpectedEOF }
	processArgs = func() []string { return []string{"./evener-tui"} }
	t.Cleanup(func() {
		processExecutable = oldExec
		processArgs = oldArgs
	})
	if got := currentExecutable(); got != "./evener-tui" {
		t.Fatalf("currentExecutable fallback = %q, want ./evener-tui", got)
	}
}

func TestCovCurrentExecutable_EmptyArgs(t *testing.T) {
	oldExec := processExecutable
	oldArgs := processArgs
	processExecutable = func() (string, error) { return "", io.ErrUnexpectedEOF }
	processArgs = func() []string { return nil }
	t.Cleanup(func() {
		processExecutable = oldExec
		processArgs = oldArgs
	})
	if got := currentExecutable(); got != "" {
		t.Fatalf("currentExecutable empty = %q, want empty", got)
	}
}

func TestCovCurrentExecutable_EmptyExeString(t *testing.T) {
	oldExec := processExecutable
	oldArgs := processArgs
	processExecutable = func() (string, error) { return "", nil }
	processArgs = func() []string { return []string{"./fallback"} }
	t.Cleanup(func() {
		processExecutable = oldExec
		processArgs = oldArgs
	})
	if got := currentExecutable(); got != "./fallback" {
		t.Fatalf("currentExecutable empty exe = %q, want ./fallback", got)
	}
}

// ---- postQuitMessageFromModel ------------------------------------------------

func TestCovPostQuitMessageFromModel_NotHubModel(t *testing.T) {
	got := postQuitMessageFromModel(nonHubModel{})
	if got != "" {
		t.Fatalf("non-hubModel = %q, want empty", got)
	}
}

func TestCovPostQuitMessageFromModel_WithMessage(t *testing.T) {
	m := hubModel{postQuitMessage: "session ended"}
	got := postQuitMessageFromModel(m)
	if got != "session ended" {
		t.Fatalf("with message = %q, want 'session ended'", got)
	}
}

func TestCovPostQuitMessageFromModel_WhitespaceTrimmed(t *testing.T) {
	m := hubModel{postQuitMessage: "  trimmed  "}
	got := postQuitMessageFromModel(m)
	if got != "trimmed" {
		t.Fatalf("trimmed = %q, want 'trimmed'", got)
	}
}

func TestCovPostQuitMessageFromModel_Empty(t *testing.T) {
	m := hubModel{}
	got := postQuitMessageFromModel(m)
	if got != "" {
		t.Fatalf("empty = %q, want empty", got)
	}
}

// nonHubModel is a non-hubModel tea.Model for testing postQuitMessageFromModel.
type nonHubModel struct{}

func (nonHubModel) Init() tea.Cmd                         { return nil }
func (m nonHubModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (nonHubModel) View() string                          { return "" }
