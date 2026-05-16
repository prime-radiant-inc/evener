package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	startupOpts, err := parseTUIStartupOptions(os.Args[1:], os.Getenv)
	if err != nil {
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
		CurrentExecutable: os.Args[0],
		AutoStart:         startupOpts.AutoStartHub,
		HealthTimeout:     5 * time.Second,
	})
	if err != nil {
		fmt.Fprint(os.Stderr, startupErrorScreen(err))
		os.Exit(1)
	}

	initThemeFromStateDir(startupOpts.StateDir)
	m := newHubModel(runtime.Client, runtime.Address.BaseURL, startupOpts.StateDir)
	var programOpts []tea.ProgramOption
	if !startupOpts.Debug {
		programOpts = append(programOpts, tea.WithAltScreen())
	}
	finalModel, err := tea.NewProgram(m, programOpts...).Run()
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
