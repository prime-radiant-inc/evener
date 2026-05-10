package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	hubAddr := flag.String("hub-addr", defaultHubAddr, "serf hub address")
	hubBin := flag.String("hub-bin", "", "path to serf-hub binary")
	noAutoStartHub := flag.Bool("no-auto-start-hub", false, "do not start a local hub when unreachable")
	logFile := flag.String("log-file", "", "write auto-started hub logs to this file")
	debug := flag.Bool("debug", false, "disable alternate screen")
	flag.Parse()

	ctx := context.Background()
	runtime, err := startHubClient(ctx, hubStartConfig{
		RawAddr:           *hubAddr,
		HubBin:            *hubBin,
		LogFile:           *logFile,
		CurrentExecutable: os.Args[0],
		AutoStart:         !*noAutoStartHub,
		HealthTimeout:     5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(1)
	}

	initTheme()
	m := newHubModel(runtime.Client, runtime.Address.BaseURL)
	var opts []tea.ProgramOption
	if !*debug {
		opts = append(opts, tea.WithAltScreen())
	}
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(1)
	}
}
