package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/minimax"
	_ "primeradiant.com/serf/llm/providers/ollama"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openaicompat"
	_ "primeradiant.com/serf/llm/providers/openrouter"
	_ "primeradiant.com/serf/llm/providers/openrouter_anthropic"
	"primeradiant.com/serf/rendezvous"
	"primeradiant.com/serf/server"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9131", "listen address")
	model := fs.String("model", "", "LLM model identifier (provider/model)")
	workDir := fs.String("dir", "", "working directory")
	stateDir := fs.String("state-dir", "", "override runtime state directory")
	runDirFlag := fs.String("run-dir", "", "override rendezvous run directory")
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
	outputSchema := fs.String("output-schema", "", "inline JSON Schema applied to the communicate tool's output field")
	verbose := fs.Bool("verbose", false, "emit NDJSON events to stderr")
	sseRingSize := fs.Int("sse-ring-size", 0, "SSE/AppWire replay ring size (default 1000)")
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
		fmt.Fprintf(os.Stderr, "Start serf as an app-wire JSON-RPC server.\n\n")
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

	resuming := *resume != "" || *resumeLast
	var resumedMeta agent.SessionMeta
	if resuming {
		var metaErr error
		resumedMeta, metaErr = cmdutil.ResolveSessionMeta(sd, *resume, *resumeLast)
		if metaErr != nil {
			return metaErr
		}
	}
	resumeProvider := ""
	resumeModel := ""
	if resuming {
		resumeProvider = resumedMeta.ProfileID
		resumeModel = resumedMeta.Model
	}
	modelRef, err := cmdutil.ResolveModelRef(*model, os.Getenv("SERF_MODEL"), resumeProvider, resumeModel)
	if err != nil {
		return err
	}

	effort, err := cmdutil.ResolveReasoningEffort(*reasoningEffort, os.Getenv("SERF_REASONING_EFFORT"))
	if err != nil {
		return err
	}

	// Create LLM client and session.
	client, err := llm.NewFromEnv(llm.WithStateDir(sd))
	if err != nil {
		return fmt.Errorf("LLM client: %w", err)
	}
	profile, err := cmdutil.SelectProfile(modelRef.Provider, modelRef.Model, *outputSchema)
	if err != nil {
		return err
	}
	env := agent.NewLocalExecutionEnvironment(wd)
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

	var sess *agent.Session
	if resuming {
		sess, err = agent.RestoreSessionFromMeta(client, profile, env, resumedMeta, sd)
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
		fmt.Fprintf(os.Stderr, "[serve] resumed session %s (%d turns)\n", resumedMeta.ID, resumedMeta.TurnCount)
	} else {
		sess, err = agent.NewSession(client, profile, env, sessionCfg)
		if err != nil {
			return fmt.Errorf("session creation: %w", err)
		}
	}

	// Signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		sess.Close()
		return fmt.Errorf("listen %s: %w", *addr, err)
	}

	hubToken := os.Getenv("SERF_HUB_TOKEN")
	srv := server.NewServer(server.ServerConfig{
		RingBufferSize: *sseRingSize,
		HubToken:       hubToken,
		AllowedHost:    listener.Addr().String(),
	})
	srv.SetAppIdentity("local", sess.ID())
	rvRegistration := &serveRendezvousRegistration{}

	var currentMu sync.RWMutex
	currentSess := sess
	getSession := func() *agent.Session {
		currentMu.RLock()
		defer currentMu.RUnlock()
		return currentSess
	}
	setSession := func(next *agent.Session) {
		currentMu.Lock()
		currentSess = next
		currentMu.Unlock()
	}

	var eventObserver func(agent.SessionEvent)
	if *verbose {
		enc := json.NewEncoder(os.Stderr)
		enc.SetEscapeHTML(false)
		var verboseMu sync.Mutex
		eventObserver = func(ev agent.SessionEvent) {
			verboseMu.Lock()
			defer verboseMu.Unlock()
			_ = enc.Encode(ev)
		}
	}

	bridgeSession := func(s *agent.Session) {
		go server.BridgeWithObserver(srv, s.Events(), eventObserver)
	}

	srv.SetCompactFunc(func(ctx context.Context) error { return getSession().Compact(ctx) })
	srv.SetSteerFunc(func(text string) { getSession().Steer(text) })
	srv.SetContextPressureFunc(func() float64 { return getSession().ContextPressure() })
	srv.SetModelFunc(func(model string) { getSession().SetModel(model) })
	srv.SetListModelsFunc(cmdutil.ListModelsFunc(client, profile.ID()))
	srv.SetDetailedStatusFunc(func() server.DetailedStatus {
		return agentToServerDetailedStatus(getSession().DetailedStatus())
	})
	srv.SetTasksFunc(func() any { return getSession().Tasks() })
	srv.SetClearFunc(func(ctx context.Context) error {
		oldSess := getSession()
		newSess, err := agent.NewSession(client, profile, env, sessionCfg)
		if err != nil {
			return fmt.Errorf("new session: %w", err)
		}
		setSession(newSess)
		srv.SetAppIdentity("local", newSess.ID())
		if err := rvRegistration.UpdateSessionID(newSess.ID()); err != nil {
			setSession(oldSess)
			srv.SetAppIdentity("local", oldSess.ID())
			newSess.Close()
			return fmt.Errorf("rendezvous update: %w", err)
		}
		oldSess.Close()
		bridgeSession(newSess)
		return nil
	})

	srv.SetWorkingDir(wd)
	srv.SetShutdownFunc(func() {
		cancel()
	})

	// Bridge session events to SSE broadcaster.
	bridgeSession(sess)

	// Input processing loop.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-srv.InputCh():
				if !ok {
					return
				}
				sess := getSession()
				srv.SetProcessing(true)
				srv.SetState("PROCESSING")
				result, processErr := sess.ProcessInput(ctx, msg.Text, msg.Images)
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
	fmt.Fprintf(os.Stderr, "[serve] listening on %s (session %s)\n", listener.Addr(), getSession().ID())

	spawnedBy := "user"
	if os.Getenv("SERF_HUB_SPAWNED") == "1" {
		spawnedBy = "hub"
	}
	runDir := *runDirFlag
	if runDir == "" {
		runDir = os.Getenv("SERF_RUN_DIR")
	}
	if runDir == "" {
		runDir = rendezvous.DefaultDir()
	}
	rvEntry := rendezvous.Entry{
		PID:        os.Getpid(),
		Address:    listener.Addr().String(),
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws://" + listener.Addr().String() + "/rpc",
		SourceID:   "local",
		ThreadID:   getSession().ID(),
		SessionID:  getSession().ID(),
		WorkingDir: wd,
		StateDir:   sd,
		Agent:      *agentName,
		Model:      modelRef.Model,
		Provider:   modelRef.Provider,
		HubToken:   hubToken,
		StartedAt:  time.Now().UTC(),
		SpawnedBy:  spawnedBy,
	}
	if err := rvRegistration.Register(runDir, rvEntry); err != nil {
		fmt.Fprintf(os.Stderr, "[serve] rendezvous write failed: %v\n", err)
	} else {
		defer func() {
			_ = rvRegistration.Remove()
		}()
	}

	httpSrv := &http.Server{Handler: srv}
	go func() {
		<-ctx.Done()
		httpSrv.Close()
		getSession().Close()
	}()

	if err := httpSrv.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func agentToServerDetailedStatus(ds agent.DetailedStatus) server.DetailedStatus {
	var out server.DetailedStatus

	for _, t := range ds.Tools {
		out.Tools = append(out.Tools, server.ToolInfo{Name: t.Name, Source: t.Source})
	}
	for _, m := range ds.MCP {
		out.MCP = append(out.MCP, server.MCPServerInfo{Name: m.Name, Tools: m.Tools})
	}
	for _, s := range ds.Skills {
		out.Skills = append(out.Skills, server.SkillInfo{Name: s.Name, Description: s.Description})
	}
	for _, p := range ds.Plugins {
		out.Plugins = append(out.Plugins, server.PluginStatusInfo{
			Name:       p.Name,
			Version:    p.Version,
			SkillCount: p.SkillCount,
			AgentCount: p.AgentCount,
			HookCount:  p.HookCount,
			MCPCount:   p.MCPCount,
		})
	}
	if len(ds.Hooks) > 0 {
		out.Hooks = make(map[string]int, len(ds.Hooks))
		for event, count := range ds.Hooks {
			out.Hooks[string(event)] = count
		}
	}
	for _, s := range ds.Subagents {
		out.Subagents = append(out.Subagents, server.SubagentStatusInfo{
			ID:        s.ID,
			Status:    string(s.Status),
			TurnsUsed: s.TurnsUsed,
		})
	}
	out.Agents = ds.Agents

	return out
}
