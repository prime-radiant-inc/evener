package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	addr := flag.String("addr", "", "connect to existing server (skip embedded startup)")
	provider := flag.String("provider", "", "LLM provider (openai, anthropic, google)")
	model := flag.String("model", "", "LLM model identifier")
	workDir := flag.String("dir", "", "working directory")
	stateDir := flag.String("state-dir", "", "override runtime state directory")
	flag.Parse()

	var serverAddr string

	if *addr != "" {
		// Connect to an existing server.
		serverAddr = *addr
	} else {
		// Start embedded server.
		ctx := context.Background()
		embedded, err := startEmbedded(ctx, embeddedConfig{
			provider: *provider,
			model:    *model,
			workDir:  *workDir,
			stateDir: *stateDir,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
			os.Exit(1)
		}
		defer embedded.Close()
		serverAddr = embedded.addr
	}

	m := newModel(serverAddr)
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Start SSE streaming in background, sending events to Bubble Tea.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		streamSSE(ctx, serverAddr, p.Send)
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		cancel()
		os.Exit(1)
	}
	cancel()
}
