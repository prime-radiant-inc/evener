package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/minimax"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openaicompat"
	_ "primeradiant.com/serf/llm/providers/openrouter"
	_ "primeradiant.com/serf/llm/providers/openrouter_anthropic"
	"primeradiant.com/serf/server"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9131", "listen address")
	model := fs.String("model", "", "LLM model identifier")
	provider := fs.String("provider", "", "LLM provider")
	workDir := fs.String("dir", "", "working directory")
	stateDir := fs.String("state-dir", "", "override runtime state directory")
	resume := fs.String("resume", "", "resume a previous session by ID")
	resumeLast := fs.Bool("resume-last", false, "resume the most recent session")
	systemPrompt := fs.String("system-prompt", "", "path to a custom system prompt file")
	var systemPromptAppend cmdutil.StringSliceFlag
	fs.Var(&systemPromptAppend, "system-prompt-append", "path to append to system prompt (repeatable)")
	systemPromptAsUser := fs.Bool("system-prompt-as-user", false, "deliver system prompt as first user message")
	maxRounds := fs.Int("max-rounds", -1, "max tool rounds per input (-1=default, 0=unlimited)")
	maxSubagentDepth := fs.Int("max-subagent-depth", -1, "max subagent nesting depth")
	shareTaskStore := fs.Bool("share-task-store", false, "share task list between parent and child sessions")
	resultToolName := fs.String("result-tool-name", "", "override the result tool name")
	reasoningEffort := fs.String("reasoning-effort", "", "reasoning effort: low|medium|high|xhigh|none")
	exportATIF := fs.String("export-atif", "", "export ATIF trajectory to this path")
	contextStrategy := fs.String("context-strategy", "", "context management strategy")
	verbose := fs.Bool("verbose", false, "emit NDJSON events to stderr")
	noProjectPrompts := fs.Bool("no-project-prompts", false, "suppress .serf/prompts/ loading")
	agentName := fs.String("agent", "", "agent persona name (default: default)")
	var skillsDirs cmdutil.StringSliceFlag
	fs.Var(&skillsDirs, "skills-dir", "extra skill directory (repeatable)")
	var mcpServers cmdutil.StringSliceFlag
	fs.Var(&mcpServers, "mcp", "MCP server (repeatable)")
	var mcpConfigs cmdutil.StringSliceFlag
	fs.Var(&mcpConfigs, "mcp-config", "path to .mcp.json file (repeatable)")
	var pluginDirs cmdutil.StringSliceFlag
	fs.Var(&pluginDirs, "plugin-dir", "plugin directory (repeatable)")
	cpuProfile := fs.String("cpu-profile", "", "write CPU profile to file")
	traceFile := fs.String("trace", "", "write execution trace to file")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serf serve [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Start serf as an HTTP server with REST+SSE API.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *cpuProfile != "" {
		stop, err := cmdutil.StartCPUProfile(*cpuProfile)
		if err != nil {
			return fmt.Errorf("CPU profile: %w", err)
		}
		defer stop()
	}
	if *traceFile != "" {
		stop, err := cmdutil.StartTrace(*traceFile)
		if err != nil {
			return fmt.Errorf("trace: %w", err)
		}
		defer stop()
	}

	_ = *verbose // TODO: tee events to stderr NDJSON alongside SSE bridge

	// Resolve working directory.
	wd := *workDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}
	}

	// Resolve state directory.
	// Priority: --state-dir flag > SERF_STATE_DIR env > XDG-computed default.
	sd := *stateDir
	if sd == "" {
		sd = os.Getenv("SERF_STATE_DIR")
	}
	if sd == "" {
		originURL := cmdutil.GitOriginURLFromDir(wd)
		sd = agent.RuntimeDir(originURL, wd, "")
	}

	// Resolve provider and model (flag > env var).
	prov, err := cmdutil.ResolveProvider(*provider)
	if err != nil {
		return err
	}
	mod, err := cmdutil.ResolveModel(*model)
	if err != nil {
		return err
	}

	effort, err := cmdutil.ResolveReasoningEffort(*reasoningEffort, os.Getenv("SERF_REASONING_EFFORT"))
	if err != nil {
		return err
	}

	// Create LLM client and session.
	client, err := llm.NewFromEnv()
	if err != nil {
		return fmt.Errorf("LLM client: %w", err)
	}
	profile, err := cmdutil.SelectProfile(prov, mod)
	if err != nil {
		return err
	}
	env := agent.NewLocalExecutionEnvironment(wd)

	var sess *agent.Session
	if *resume != "" || *resumeLast {
		meta, metaErr := cmdutil.ResolveSessionMeta(sd, *resume, *resumeLast)
		if metaErr != nil {
			return metaErr
		}
		sess, err = agent.RestoreSessionFromMeta(client, profile, env, meta, sd)
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
		fmt.Fprintf(os.Stderr, "[serve] resumed session %s (%d turns)\n", meta.ID, meta.TurnCount)
	} else {
		sessionCfg := agent.SessionConfig{
			MaxToolRoundsPerInput:  cmdutil.MaxRoundsToConfig(*maxRounds),
			ShareTasksWithChildren: *shareTaskStore,
			ResultToolName:         *resultToolName,
			StateDir:               sd,
			SystemPromptFile:       *systemPrompt,
			SystemPromptAppend:     []string(systemPromptAppend),
			NoProjectPrompts:       *noProjectPrompts,
			AgentName:              *agentName,
			SkillsDirs:             []string(skillsDirs),
			MCPConfigFiles:         []string(mcpConfigs),
			MCPInline:              []string(mcpServers),
			PluginDirs:             []string(pluginDirs),
			ContextStrategy:        *contextStrategy,
			ExportATIFPath:         *exportATIF,
			NonInteractive:         true,
			SystemPromptAsUser:     *systemPromptAsUser,
		}
		if *maxSubagentDepth >= 0 {
			sessionCfg.MaxSubagentDepth = *maxSubagentDepth
		}
		if effort.Set {
			sessionCfg.ReasoningEffort = effort.Value
		}
		sess, err = agent.NewSession(client, profile, env, sessionCfg)
		if err != nil {
			return fmt.Errorf("session creation: %w", err)
		}
	}

	// Signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	srv := server.NewServer(server.ServerConfig{})
	srv.SetCompactFunc(sess.Compact)
	srv.SetSteerFunc(sess.Steer)
	srv.SetContextPressureFunc(sess.ContextPressure)
	srv.SetModelFunc(sess.SetModel)
	srv.SetListModelsFunc(cmdutil.ListModelsFunc(client, profile.ID()))

	// Bridge session events to SSE broadcaster.
	go server.Bridge(srv, sess.Events())

	// Input processing loop.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case text, ok := <-srv.InputCh():
				if !ok {
					return
				}
				srv.SetProcessing(true)
				srv.SetState("PROCESSING")
				result, processErr := sess.ProcessInput(ctx, text)
				srv.SetProcessing(false)
				srv.SetState(string(sess.State()))
				if processErr != nil {
					fmt.Fprintf(os.Stderr, "[serve] error: %v\n", processErr)
				}
				_ = result
			}
		}
	}()

	// Start HTTP server.
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		sess.Close()
		return fmt.Errorf("listen %s: %w", *addr, err)
	}
	fmt.Fprintf(os.Stderr, "[serve] listening on %s (session %s)\n", listener.Addr(), sess.ID())

	httpSrv := &http.Server{Handler: srv}
	go func() {
		<-ctx.Done()
		httpSrv.Close()
		sess.Close()
	}()

	if err := httpSrv.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}
