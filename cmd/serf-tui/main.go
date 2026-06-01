package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/hubstart"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func main() {
	// All shutdown work goes through run() so deferred cleanup (notably
	// tuitheme.ResetTerminalBg, which restores OSC 10/11 colors the user expected
	// before we started) actually fires on every exit path. Calling
	// os.Exit directly from main would skip those defers.
	os.Exit(run())
}

func run() int {
	startupOpts, err := hubstart.ParseTUIStartupOptions(os.Args[1:], os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Usage has already been printed by the flag package via fs.Usage.
			return 0
		}
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		return 2
	}

	authToken := hubstart.ResolveAuthToken(startupOpts.AuthToken, startupOpts.StateDir)

	ctx := context.Background()
	runtime, err := hubstart.StartHubClient(ctx, hubstart.HubStartConfig{
		RawAddr:           startupOpts.HubAddr,
		HubBin:            startupOpts.HubBin,
		StateDir:          startupOpts.StateDir,
		LogFile:           startupOpts.LogFile,
		AuthToken:         authToken,
		CurrentExecutable: currentExecutable(),
		AutoStart:         startupOpts.AutoStartHub,
		HealthTimeout:     5 * time.Second,
	})
	if err != nil {
		fmt.Fprint(os.Stderr, hubstart.StartupErrorScreen(err))
		return 1
	}

	// Probe the terminal's default fg/bg BEFORE any tuitheme.SetTheme call, so
	// (a) "system" theme detection has cached probe data and (b) the
	// deferred restore on exit can return the exact originals.
	tuitheme.ProbeTerminalDefaults()
	tuitheme.InitThemeFromStateDir(startupOpts.StateDir)
	tuitheme.ApplyTerminalBg()
	defer tuitheme.ResetTerminalBg()

	m := newHubModel(runtime.Client, runtime.Address.BaseURL, startupOpts.StateDir)
	var programOpts []tea.ProgramOption
	if !startupOpts.Debug {
		programOpts = append(programOpts, tea.WithAltScreen())
	}
	program := tea.NewProgram(m, programOpts...)
	if m.pending != nil {
		m.pending.SetSend(program.Send)
	}
	finalModel, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		return 1
	}
	if message := postQuitMessageFromModel(finalModel); message != "" {
		fmt.Fprintln(os.Stdout, message)
	}
	return 0
}

func postQuitMessageFromModel(model tea.Model) string {
	m, ok := model.(hubModel)
	if !ok {
		return ""
	}
	return strings.TrimSpace(m.postQuitMessage)
}

// currentExecutable returns the absolute path of the running serf-tui
// binary. It prefers os.Executable() (always absolute on supported
// platforms) and falls back to os.Args[0] when the OS cannot report a
// path. Returning the absolute path lets binresolve.Resolve locate a
// sibling serf-hub even when serf-tui was launched via a relative path
// like "./serf-tui" — which would otherwise be rejected by exec.ErrDot.
func currentExecutable() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	if len(os.Args) > 0 {
		return os.Args[0]
	}
	return ""
}
