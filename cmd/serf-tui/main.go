package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	authopenai "primeradiant.com/serf/internal/auth/openai"
)

func main() {
	addr := flag.String("addr", "", "connect to existing server (skip embedded startup)")
	provider := flag.String("provider", "", "LLM provider")
	modelFlag := flag.String("model", "", "LLM model identifier")
	workDir := flag.String("dir", "", "working directory")
	stateDir := flag.String("state-dir", "", "override runtime state directory")
	systemPrompt := flag.String("system-prompt", "", "path to a custom system prompt file")
	maxRounds := flag.Int("max-rounds", -1, "max tool rounds per input (0=unlimited, default: 200)")
	reasoningEffort := flag.String("reasoning-effort", "", "reasoning effort: low|medium|high|none")
	resume := flag.String("resume", "", "resume a previous session by ID")
	resumeLast := flag.Bool("resume-last", false, "resume the most recent session")
	listSessions := flag.Bool("list-sessions", false, "pick a session to resume interactively")
	maxSubagentDepth := flag.Int("max-subagent-depth", -1, "max subagent nesting depth")
	shareTaskStore := flag.Bool("share-task-store", false, "share task list between parent and child sessions")
	resultToolName := flag.String("result-tool-name", "", "override the result tool name")
	exportATIF := flag.String("export-atif", "", "export ATIF trajectory to this path")
	contextStrategy := flag.String("context-strategy", "", "context management strategy")
	verbose := flag.Bool("verbose", false, "emit NDJSON events to stderr")
	noProjectPrompts := flag.Bool("no-project-prompts", false, "suppress .serf/prompts/ loading")
	agentName := flag.String("agent", "", "agent persona name (default: default)")
	systemPromptAsUser := flag.Bool("system-prompt-as-user", false, "deliver system prompt as first user message")
	resumeWith := flag.String("resume-with", "", "start a new task using a previous session's context")
	cpuProfile := flag.String("cpu-profile", "", "write CPU profile to file")
	traceFile := flag.String("trace", "", "write execution trace to file")

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

	if *cpuProfile != "" {
		stop, err := cmdutil.StartCPUProfile(*cpuProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
			os.Exit(1)
		}
		defer stop()
	}
	if *traceFile != "" {
		stop, err := cmdutil.StartTrace(*traceFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
			os.Exit(1)
		}
		defer stop()
	}

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
	var initialMessages []chatMessage
	var resolvedStateDir string
	var err error
	asyncCh := make(chan tea.Msg, 64)
	var embedded *embeddedServer

	if *addr != "" {
		// Connect to an existing server.
		serverAddr = *addr
		resolvedStateDir, err = resolveTUIStateDir(*workDir, *stateDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
			os.Exit(1)
		}
	} else {
		resolvedStateDir, err = resolveTUIStateDir(*workDir, *stateDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
			os.Exit(1)
		}
		cfg := embeddedConfig{
			provider:           *provider,
			model:              *modelFlag,
			workDir:            *workDir,
			stateDir:           resolvedStateDir,
			systemPrompt:       *systemPrompt,
			systemPromptAppend: []string(systemPromptAppend),
			maxRounds:          *maxRounds,
			maxSubagentDepth:   *maxSubagentDepth,
			shareTaskStore:     *shareTaskStore,
			resultToolName:     *resultToolName,
			exportATIF:         *exportATIF,
			contextStrategy:    *contextStrategy,
			verbose:            *verbose,
			noProjectPrompts:   *noProjectPrompts,
			agentName:          *agentName,
			systemPromptAsUser: *systemPromptAsUser,
			reasoningEffort:    *reasoningEffort,
			skillsDirs:         []string(skillsDirs),
			mcpServers:         []string(mcpServers),
			mcpConfigs:         []string(mcpConfigs),
			pluginDirs:         []string(pluginDirs),
			resume:             *resume,
			resumeWith:         *resumeWith,
			resumeLast:         *resumeLast,
		}
		authBootstrap := false
		prov, provErr := cmdutil.ResolveProvider(*provider)
		if provErr == nil && prov == "openai" {
			status, statusErr := authopenai.NewService(authopenai.DefaultConfig(), nil).Status(resolvedStateDir)
			if statusErr != nil {
				fmt.Fprintf(os.Stderr, "serf-tui: %v\n", statusErr)
				os.Exit(1)
			}
			authBootstrap = !status.SignedIn || status.Source == authopenai.AuthSourceSignedOut
			if authBootstrap {
				initialMessages = append(initialMessages, chatMessage{
					Kind: msgSystem,
					Text: "OpenAI login required before this session can start. Use /openai to sign in.",
				})
			}
		}

		// Start embedded server.
		if !authBootstrap {
			ctx := context.Background()
			embedded, err = startEmbedded(ctx, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
				os.Exit(1)
			}
			serverAddr = embedded.addr
			resolvedStateDir = embedded.stateDir()
			if len(embedded.history) > 0 {
				initialMessages = historyToMessages(embedded.history)
			}
		}
	}

	initTheme()
	m := newConfiguredModel(serverAddr, resolvedStateDir, initialMessages, embeddedConfig{
		provider:           *provider,
		model:              *modelFlag,
		workDir:            *workDir,
		stateDir:           resolvedStateDir,
		systemPrompt:       *systemPrompt,
		systemPromptAppend: []string(systemPromptAppend),
		maxRounds:          *maxRounds,
		maxSubagentDepth:   *maxSubagentDepth,
		shareTaskStore:     *shareTaskStore,
		resultToolName:     *resultToolName,
		exportATIF:         *exportATIF,
		contextStrategy:    *contextStrategy,
		verbose:            *verbose,
		noProjectPrompts:   *noProjectPrompts,
		agentName:          *agentName,
		systemPromptAsUser: *systemPromptAsUser,
		reasoningEffort:    *reasoningEffort,
		skillsDirs:         []string(skillsDirs),
		mcpServers:         []string(mcpServers),
		mcpConfigs:         []string(mcpConfigs),
		pluginDirs:         []string(pluginDirs),
		resume:             *resume,
		resumeWith:         *resumeWith,
		resumeLast:         *resumeLast,
	}, embedded, asyncCh, embedded == nil && serverAddr == "")
	if serverAddr != "" {
		m.streamID = 1
		go streamSSE(context.Background(), serverAddr, m.streamID, func(msg tea.Msg) {
			asyncCh <- msg
		})
	}
	p := tea.NewProgram(m, tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serf-tui: %v\n", err)
		os.Exit(1)
	}

	if m, ok := finalModel.(model); ok {
		if m.streamCancel != nil {
			m.streamCancel()
		}
		if m.embedded != nil {
			m.embedded.Close()
		}
		printResumeHint(os.Stderr, m.sessionID)
	}
}

// printResumeHint writes session resumption instructions to w.
// It is a no-op when sessionID is empty.
func printResumeHint(w io.Writer, sessionID string) {
	if sessionID == "" {
		return
	}
	fmt.Fprintf(w, "\nSession: %s\n", sessionID)
	fmt.Fprintf(w, "Resume:  serf-tui --resume %s\n", sessionID)
}

// pickSession resolves the state directory and shows an interactive session picker.
func pickSession(workDir, stateDirFlag string) (string, error) {
	sd, err := resolveTUIStateDir(workDir, stateDirFlag)
	if err != nil {
		return "", err
	}
	sessions, err := agent.ListSessions(sd)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}

	return runSessionPicker(sessions)
}

func resolveTUIStateDir(workDir, stateDirFlag string) (string, error) {
	if stateDirFlag != "" {
		return stateDirFlag, nil
	}
	wd := workDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
	}
	originURL := cmdutil.GitOriginURLFromDir(wd)
	return agent.RuntimeDir(originURL, wd, ""), nil
}
