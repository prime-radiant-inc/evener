package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	startupOpts, err := parseTUIStartupOptions(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(2)
	}

	ctx := context.Background()
	runtime, err := startHubClient(ctx, hubStartConfig{
		RawAddr:           startupOpts.HubAddr,
		HubBin:            startupOpts.HubBin,
		StateDir:          startupOpts.StateDir,
		LogFile:           startupOpts.LogFile,
		CurrentExecutable: os.Args[0],
		AutoStart:         startupOpts.AutoStartHub,
		HealthTimeout:     5 * time.Second,
	})
	if err != nil {
		fmt.Fprint(os.Stderr, startupErrorScreen(err))
		os.Exit(1)
	}

	initTheme()
	m := newHubModel(runtime.Client, runtime.Address.BaseURL)
	var programOpts []tea.ProgramOption
	if !startupOpts.Debug {
		programOpts = append(programOpts, tea.WithAltScreen())
	}
	if _, err := tea.NewProgram(m, programOpts...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(1)
	}
}
