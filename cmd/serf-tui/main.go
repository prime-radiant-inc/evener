package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9131", "server address")
	flag.Parse()

	m := newModel(*addr)
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Start SSE streaming in background, sending events to Bubble Tea
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		streamSSE(ctx, *addr, p.Send)
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		cancel()
		os.Exit(1)
	}
	cancel()
}
