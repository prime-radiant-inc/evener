package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
)

func main() {
	addr := flag.String("addr", "", "connect to existing server (skip embedded startup)")
	provider := flag.String("provider", "", "LLM provider (openai, anthropic, google)")
	model := flag.String("model", "", "LLM model identifier")
	workDir := flag.String("dir", "", "working directory")
	stateDir := flag.String("state-dir", "", "override runtime state directory")
	systemPrompt := flag.String("system-prompt", "", "path to a custom system prompt file")
	maxRounds := flag.Int("max-rounds", -1, "max tool rounds per input (0=unlimited, default: 200)")
	reasoningEffort := flag.String("reasoning-effort", "", "reasoning effort: low|medium|high|none")
	resume := flag.String("resume", "", "resume a previous session by ID")
	resumeLast := flag.Bool("resume-last", false, "resume the most recent session")
	listSessions := flag.Bool("list-sessions", false, "pick a session to resume interactively")

	var systemPromptAppend cmdutil.StringSliceFlag
	flag.Var(&systemPromptAppend, "system-prompt-append", "path to append to system prompt (repeatable)")
	var skillsDirs cmdutil.StringSliceFlag
	flag.Var(&skillsDirs, "skills-dir", "extra skill directory (repeatable)")
	var mcpServers cmdutil.StringSliceFlag
	flag.Var(&mcpServers, "mcp", "MCP server (repeatable, format: name:command args...)")
	var mcpConfigs cmdutil.StringSliceFlag
	flag.Var(&mcpConfigs, "mcp-config", "path to .mcp.json file (repeatable)")
	var pluginDirs cmdutil.StringSliceFlag
	flag.Var(&pluginDirs, "plugin-dir", "plugin directory (repeatable)")

	flag.Parse()

	// If --list-sessions, show interactive picker before starting anything else.
	if *listSessions {
		sessionID, err := pickSession(*workDir, *stateDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
			os.Exit(1)
		}
		if sessionID == "" {
			// User cancelled.
			return
		}
		*resume = sessionID
	}

	var serverAddr string

	if *addr != "" {
		// Connect to an existing server.
		serverAddr = *addr
	} else {
		// Start embedded server.
		ctx := context.Background()
		embedded, err := startEmbedded(ctx, embeddedConfig{
			provider:           *provider,
			model:              *model,
			workDir:            *workDir,
			stateDir:           *stateDir,
			systemPrompt:       *systemPrompt,
			systemPromptAppend: []string(systemPromptAppend),
			maxRounds:          *maxRounds,
			reasoningEffort:    *reasoningEffort,
			skillsDirs:         []string(skillsDirs),
			mcpServers:         []string(mcpServers),
			mcpConfigs:         []string(mcpConfigs),
			pluginDirs:         []string(pluginDirs),
			resume:             *resume,
			resumeLast:         *resumeLast,
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

// pickSession resolves the state directory and shows an interactive session picker.
func pickSession(workDir, stateDirFlag string) (string, error) {
	wd := workDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
	}

	sd := stateDirFlag
	if sd == "" {
		originURL := cmdutil.GitOriginURLFromDir(wd)
		sd = agent.RuntimeDir(originURL, wd, "")
	}

	sessions, err := agent.ListSessions(sd)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}

	return runSessionPicker(sessions)
}
