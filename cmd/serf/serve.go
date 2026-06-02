package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf/internal/rvreg"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
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

// serveLoadClient is the injectable hook for tests. Production code calls
// cmdutil.LoadClient; tests may replace this to inject a stub client.
var serveLoadClient = cmdutil.LoadClient

func newServeLLMClient(stateDir string, warnings io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
	client, cfg, hasConfig, err := serveLoadClient(llm.WithStateDir(stateDir))
	if err != nil {
		return nil, providercfg.Config{}, false, nil, fmt.Errorf("LLM client: %w", err)
	}
	closeAPILog, err := cmdutil.AttachAPILogger(client, stateDir, warnings)
	if err != nil {
		return nil, providercfg.Config{}, false, nil, err
	}
	return client, cfg, hasConfig, closeAPILog, nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9131", "listen address")
	model := fs.String("model", "", "LLM model identifier (provider/model)")
	fastCheapModel := fs.String("fast-cheap-model", "", "fast cheap model identifier for side calls (same provider/model as --model)")
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
	appReplaySize := fs.Int("app-replay-size", 0, "AppWire notification replay ring size (default 1000)")
	noProjectPrompts := fs.Bool("no-project-prompts", false, "suppress .serf/prompts/ loading")
	nonInteractive := fs.Bool("non-interactive", false, "mark this daemon session as headless/non-interactive")
	agentName := fs.String("agent", "", "agent persona name (default: default)")
	var skillsDirs cmdutil.StringSliceFlag
	fs.Var(&skillsDirs, "skills-dir", "extra skill directory (repeatable)")
	var mcpServers cmdutil.StringSliceFlag
	fs.Var(&mcpServers, "mcp", "MCP server (repeatable)")
	var mcpConfigs cmdutil.StringSliceFlag
	fs.Var(&mcpConfigs, "mcp-config", "path to .mcp.json file (repeatable)")
	var pluginDirs cmdutil.StringSliceFlag
	fs.Var(&pluginDirs, "plugin-dir", "plugin directory (repeatable)")
	var modelFallbacks cmdutil.StringSliceFlag
	fs.Var(&modelFallbacks, "model-fallback", "fallback model (provider/model) tried on permanent provider errors (repeatable)")
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
	client, provCfg, hasProvConfig, closeAPILog, err := newServeLLMClient(sd, os.Stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck
	profile, err := buildInitialProfile(provCfg, modelRef, *outputSchema)
	if err != nil {
		return err
	}
	profile, err = applyFastCheapModel(profile, *fastCheapModel)
	if err != nil {
		return err
	}
	env := execenv.NewLocalExecutionEnvironment(wd)
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
		NonInteractive:         *nonInteractive,
		SystemPromptAsUser:     *systemPromptAsUser,
		ModelFallbacks:         []string(modelFallbacks),
		ResolveProfile:         cmdutil.BuildResolveProfile(provCfg, hasProvConfig),
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

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", *addr)
	if err != nil {
		sess.Close()
		return fmt.Errorf("listen %s: %w", *addr, err)
	}

	hubToken := os.Getenv("SERF_HUB_TOKEN")
	srv := server.NewServer(server.ServerConfig{
		AppReplaySize: *appReplaySize,
		HubToken:      hubToken,
		AllowedHost:   listener.Addr().String(),
	})
	srv.SetAppIdentity("local", sess.ID())
	rvRegistration := &rvreg.Registration{}

	var currentMu sync.RWMutex
	currentSess := sess
	getSession := func() *agent.Session {
		currentMu.RLock()
		defer currentMu.RUnlock()
		return currentSess
	}
	srv.SetTranscriptPathFunc(func() string {
		if current := getSession(); current != nil {
			return current.TranscriptPath()
		}
		return ""
	})
	setSession := func(next *agent.Session) {
		currentMu.Lock()
		currentSess = next
		currentMu.Unlock()
	}

	var eventObserver func(events.SessionEvent)
	if *verbose {
		enc := json.NewEncoder(os.Stderr)
		enc.SetEscapeHTML(false)
		var verboseMu sync.Mutex
		eventObserver = func(ev events.SessionEvent) {
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
	srv.SetSteerWithImagesFunc(func(text string, images []server.ImageAttachment) {
		getSession().SteerWithImages(text, images)
	})
	srv.SetQueueFunc(func(text string) error { return getSession().Enqueue(ctx, text) })
	srv.SetQueueWithImagesFunc(func(text string, images []server.ImageAttachment) error {
		return getSession().EnqueueWithImages(ctx, text, images)
	})
	srv.SetDrainAsSteerFunc(func() error { return getSession().DrainAsSteer(ctx) })
	srv.SetDrainAsSteerWithInputFunc(func(text string, images []server.ImageAttachment) error {
		return getSession().DrainAsSteerWithInput(ctx, text, images)
	})
	srv.SetQueueDepthFunc(func() int { return getSession().QueueDepth() })
	srv.SetQueuePreviewFunc(func() []string { return getSession().QueuePreview() })
	srv.SetContextPressureFunc(func() float64 { return getSession().ContextPressure() })
	srv.SetContextMetricsFunc(func() server.ContextMetrics {
		metrics := getSession().ContextMetrics()
		return server.ContextMetrics{Used: metrics.Used, Window: metrics.Window, Remaining: metrics.Remaining}
	})
	srv.SetModelFunc(func(model string) { getSession().SetModel(model) })
	srv.SetListModelsFunc(cmdutil.ListModelsFunc(client, profile.ID()))
	srv.SetDetailedStatusFunc(func() server.DetailedStatus {
		return agentToServerDetailedStatus(getSession().DetailedStatus())
	})
	srv.SetTasksFunc(func() any { return getSession().Tasks() })
	srv.SetClearFunc(func(ctx context.Context) error {
		oldSess := getSession()
		clearCfg := sessionCfg
		clearCfg.SessionStartKind = agent.SessionStartKindClear
		newSess, err := agent.NewSession(client, profile, env, clearCfg)
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

	// Bridge session events to appwire notifications.
	bridgeSession(sess)

	// Input processing loop. Each turn runs under a per-turn cancellable
	// context that is wired into the server's interrupt handler so POST
	// /interrupt actually cancels the in-flight turn. The cancel is
	// cleared after the turn finishes so capabilities.interrupt only
	// reports true while a turn is in flight.
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
				var currentCancel context.CancelFunc
				var nextTurnCtx func(context.Context) (context.Context, context.CancelFunc)
				nextTurnCtx = func(root context.Context) (context.Context, context.CancelFunc) {
					drainCtx, cancelDrain := context.WithCancel(root)
					drainCtx = agent.WithQueuedInputDrainOnInterruptHandler(drainCtx, root, nextTurnCtx)
					currentCancel = cancelDrain
					srv.SetProcessing(true)
					srv.SetState(string(agent.SessionProcessing))
					srv.SetCancelFunc(cancelDrain)
					return drainCtx, cancelDrain
				}
				turnCtx, cancelTurn := context.WithCancel(ctx)
				currentCancel = cancelTurn
				turnCtx = agent.WithQueuedInputDrainOnInterruptHandler(turnCtx, ctx, nextTurnCtx)
				srv.SetCancelFunc(cancelTurn)
				srv.SetProcessing(true)
				srv.SetState(string(agent.SessionProcessing))
				result, processErr := sess.ProcessInput(turnCtx, msg.Text, msg.Images)
				srv.SetProcessing(false)
				srv.SetState(string(sess.State()))
				srv.SetCancelFunc(nil)
				currentCancel()
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
		_ = httpSrv.Close()
		getSession().Close()
	}()

	if err := httpSrv.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// buildInitialProfile constructs the session's initial *Profile from
// the provider config. Instance names (e.g. "work" defined in providers.toml)
// are resolved via cmdutil.ResolveProfileWithLiveWindow, which sources the
// context window from the provider's live /models endpoint for openai-compat
// providers (falling back to the embedded catalog when unavailable).
// outputSchemaJSON and SERF_ALLOWED_DECISIONS are applied in this app layer so
// callers see the same communicate-tool schema regardless of model.
func buildInitialProfile(cfg providercfg.Config, modelRef cmdutil.ModelRef, outputSchemaJSON string) (*provider.Profile, error) {
	raw, err := cmdutil.ResolveProfileWithLiveWindow(cfg, modelRef.Qualified())
	if err != nil {
		return nil, err
	}
	var outputSchema map[string]any
	if trimmed := strings.TrimSpace(outputSchemaJSON); trimmed != "" {
		if jsonErr := json.Unmarshal([]byte(trimmed), &outputSchema); jsonErr != nil {
			return nil, fmt.Errorf("invalid --output-schema: %w", jsonErr)
		}
	}
	p := provider.WithCommunicateOutputSchema(raw, outputSchema)
	allowedDecisions := cmdutil.ParseAllowedDecisions(os.Getenv("SERF_ALLOWED_DECISIONS"))
	return provider.WithAllowedDecisions(p, allowedDecisions), nil
}

func applyFastCheapModel(profile *provider.Profile, raw string) (*provider.Profile, error) {
	if profile == nil || strings.TrimSpace(raw) == "" {
		return profile, nil
	}
	ref, err := cmdutil.ParseModelRef(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid --fast-cheap-model: %w", err)
	}
	if ref.Provider != profile.ID() {
		return nil, fmt.Errorf("--fast-cheap-model provider %q must match active provider %q", ref.Provider, profile.ID())
	}
	return provider.WithCheapModel(profile, ref.Model), nil
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
