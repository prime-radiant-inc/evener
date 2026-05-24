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
)

func main() {
	startupOpts, err := parseTUIStartupOptions(os.Args[1:], os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Usage has already been printed by the flag package via fs.Usage.
			return
		}
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(2)
	}

	authToken := resolveAuthToken(startupOpts.AuthToken, startupOpts.StateDir)

	ctx := context.Background()
	runtime, err := startHubClient(ctx, hubStartConfig{
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
		fmt.Fprint(os.Stderr, startupErrorScreen(err))
		os.Exit(1)
	}

	initThemeFromStateDir(startupOpts.StateDir)
	applyTerminalBg()
	defer resetTerminalBg()

	m := newHubModel(runtime.Client, runtime.Address.BaseURL, startupOpts.StateDir)
	var programOpts []tea.ProgramOption
	if !startupOpts.Debug {
		programOpts = append(programOpts, tea.WithAltScreen())
	}
	program := tea.NewProgram(m, programOpts...)
	if m.pending != nil {
		m.pending.setSend(program.Send)
	}
	finalModel, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(1)
	}
	if message := postQuitMessageFromModel(finalModel); message != "" {
		fmt.Fprintln(os.Stdout, message)
	}
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
