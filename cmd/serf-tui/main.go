// cmd/serf-tui/main.go
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9131", "server address")
	flag.Parse()

	p := tea.NewProgram(newModel(*addr), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(1)
	}
}
