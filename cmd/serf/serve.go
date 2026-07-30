package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"text/tabwriter"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf/internal/rvreg"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/internal/plugins"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/kimi_anthropic"
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
var serveAttachAPILogger = func(client *llm.Client, stateDir string, warnings io.Writer) (func() error, error) {
	return cmdutil.AttachAPILogger(client, stateDir, warnings)
}
var serveAttachSessionAPILogger = cmdutil.AttachSessionAPILogger

type serveServer interface {
	http.Handler
	// ReplaceAppIdentity is the only way serve installs an identity: both the
	// startup install and /clear publish a PreparedAppIdentity, so every
	// fallible step has already happened by the time anything is announced.
	ReplaceAppIdentity(server.PreparedAppIdentity, func())
	SetSandboxEscalationResolveFunc(func(string, bool) error)
	SetCompactFunc(func(context.Context) error)
	SetSteerFunc(func(string))
	SetSteerWithImagesFunc(func(string, []server.ImageAttachment))
	SetQueueFunc(func(string) error)
	SetQueueWithImagesFunc(func(string, []server.ImageAttachment) error)
	SetGoalFunc(func(string) (bool, error))
	SetDrainAsSteerFunc(func() error)
	SetDrainAsSteerWithInputFunc(func(string, []server.ImageAttachment) error)
	SetPromoteQueuedAsSteerFunc(func(int, string) error)
	SetCancelQueuedFunc(func(int, string) (string, int, error))
	// SetThreadEnvelopeSource replaces sixteen read-time session callbacks with
	// one seam the daemon samples at change time. See server/thread_envelope.go.
	SetThreadEnvelopeSource(server.ThreadEnvelopeSource)
	RefreshThreadEnvelope()
	SetModelFunc(func(string) error)
	UpdateSessionInfo(sessionID, model, profile string)
	SetNameFunc(func(string))
	SetReasoningEffortFunc(func(string))
	SetListModelsFunc(func(context.Context) ([]server.ModelsResponseItem, error))
	SetTasksFunc(func() any)
	SetClearFunc(func(context.Context) error)
	SetWorkingDir(string)
	SetShutdownFunc(func())
	SetProcessing(bool)
	SetState(string)
	SetCancelFunc(context.CancelFunc)
	SetRetrySafeTurnFunctions(server.RetrySafeTurnFunctions)
	InputCh() <-chan server.InputMessage
	SubmitContinuation(string)
	SubmitNotification()
	SubmitClientMutationStart(string)
}

type serveDeps struct {
	newFlagSet       func(string, flag.ErrorHandling) *flag.FlagSet
	getwd            func() (string, error)
	ensureConfigDirs func() error
	seedMarketplaces func() error
	resolveMeta      func(string, string, bool) (schema.SessionMeta, error)
	newClient        func(string, io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error)
	attachAPILogger  func(*llm.Client, string, io.Writer) (func(string) error, func() error, error)
	buildProfile     func(providercfg.Config, cmdutil.ModelRef, string) (*provider.Profile, error)
	applyCheap       func(*provider.Profile, string, *llm.Client) (*provider.Profile, error)
	newSession       func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error)
	restoreSession   func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error)
	listen           func(context.Context, string, string) (net.Listener, error)
	newServer        func(server.ServerConfig) serveServer
	// bridge attaches the daemon's event consumer to a session. It MUST return
	// once the attachment is in effect, draining on its own goroutine -- the
	// caller does not spawn it. A blocking implementation would leave the
	// session live with a feed that still drops.
	//
	// The returned channel closes when that drain has FINISHED. Anything the
	// observer writes to must outlive it: Session.Close() closes the event
	// channel but does not wait for the buffered tail to be delivered, so
	// "the session is closed" is not "the consumer is done".
	bridge func(serveServer, *agent.Session, func(events.SessionEvent)) <-chan struct{}
	// verboseOut is where --verbose writes its NDJSON. Nil means os.Stderr.
	// Injectable so a test can wedge it: the reason the tee exists is that the
	// real one can be a pipe nobody drains, and that is not reproducible against
	// a real terminal.
	verboseOut       io.Writer
	subscriberCount  func(serveServer, string) int
	notifyContext    func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	startCPUProfile  func(string) (func(), error)
	startTrace       func(string) (func(), error)
	register         func(*rvreg.Registration, string, rendezvous.Entry) error
	serveHTTP        func(*http.Server, net.Listener) error
	provisionSandbox func(*execenv.LocalExecutionEnvironment, *agent.SessionConfig, string) error
	newClearSession  func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error)
	// prepareAppIdentity projects a session's transcript into an installable
	// AppWire identity. It is the one fallible step of an identity swap, so it
	// is injectable: a test needs a deterministic preparation failure to prove
	// /clear abandons a half-built session instead of publishing it.
	prepareAppIdentity func(sourceID, threadID, transcriptPath string) (server.PreparedAppIdentity, error)
	updateSessionID    func(*rvreg.Registration, string) error
	observeCallbacks   func(serveCallbackObserver)
}

type serveCallbackObserver struct {
	notify             func()
	subscriberCount    func() int
	pendingEscalations func() []appwire.SandboxEscalationRequested
	setSession         func(*agent.Session)
	session            *agent.Session
}

func defaultServeDeps() serveDeps {
	return serveDeps{
		newFlagSet: flag.NewFlagSet,
		getwd:      os.Getwd, ensureConfigDirs: cmdutil.EnsureUserConfigDirs,
		seedMarketplaces: func() error { _, err := plugins.NewManager("").SeedDefaultMarketplaces(); return err },
		resolveMeta:      cmdutil.ResolveSessionMeta, newClient: newUnloggedServeLLMClient,
		attachAPILogger: serveAttachSessionAPILogger,
		buildProfile:    buildInitialProfile, applyCheap: applyFastCheapModel,
		newSession: agent.NewSession, restoreSession: agent.RestoreSessionFromMetaWithConfig,
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		newServer: func(cfg server.ServerConfig) serveServer { return server.NewServer(cfg) },
		// The daemon registers as the session's AUTHORITATIVE consumer rather
		// than ranging over Events(): its projection is the sole authority for
		// turn reads, so an event it misses is absent from every thread/read
		// for the life of the identity. Registering is what makes the feed
		// lossless -- there is no separate switch, and there must not be one.
		bridge: func(s serveServer, sess *agent.Session, observer func(events.SessionEvent)) <-chan struct{} {
			srv := s.(*server.Server)
			return sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
				server.BridgeEvent(srv, ev, observer)
			})
		},
		subscriberCount: func(s serveServer, id string) int { return s.(*server.Server).AppServer().SubscriberCount(id) },
		notifyContext:   signal.NotifyContext, startCPUProfile: cmdutil.StartCPUProfile, startTrace: cmdutil.StartTrace,
		register:           func(r *rvreg.Registration, dir string, entry rendezvous.Entry) error { return r.Register(dir, entry) },
		serveHTTP:          func(s *http.Server, l net.Listener) error { return s.Serve(l) },
		provisionSandbox:   provisionSandbox,
		newClearSession:    agent.NewSession,
		prepareAppIdentity: server.PrepareAppIdentity,
		updateSessionID:    func(r *rvreg.Registration, id string) error { return r.UpdateSessionID(id) },
	}
}

func newServeLLMClient(stateDir string, warnings io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
	client, cfg, hasConfig, closeClient, err := newUnloggedServeLLMClient(stateDir, warnings)
	if err != nil {
		return nil, providercfg.Config{}, false, nil, err
	}
	closeAPILog, err := serveAttachAPILogger(client, stateDir, warnings)
	if err != nil {
		_ = closeClient()
		return nil, providercfg.Config{}, false, nil, err
	}
	return client, cfg, hasConfig, func() error {
		apiLogErr := closeAPILog()
		clientErr := closeClient()
		if apiLogErr != nil {
			return apiLogErr
		}
		return clientErr
	}, nil
}

func newUnloggedServeLLMClient(stateDir string, _ io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
	client, cfg, hasConfig, err := serveLoadClient(llm.WithStateDir(stateDir))
	if err != nil {
		return nil, providercfg.Config{}, false, nil, fmt.Errorf("LLM client: %w", err)
	}
	return client, cfg, hasConfig, func() error { return nil }, nil
}

func runServe(args []string) error {
	return runServeWithDeps(args, defaultServeDeps())
}

func runServeWithDeps(args []string, deps serveDeps) error {
	fs := deps.newFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9131", "listen address")
	model := fs.String("model", "", "LLM model identifier (provider/model)")
	fastCheapModel := fs.String("fast-cheap-model", "", "auxiliary model for side calls (naming, summarization, web fetch); 'provider/model' may use a different provider than --model, or a bare 'model' for the active provider")
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
	maxConcurrentDelegates := fs.Int("max-concurrent-delegates", -1, "max concurrently running delegate turns per session tree (default: 50)")
	maxRetainedTerminal := fs.Int("max-retained-terminal", -1, "max retained terminal delegate records per session (default: 2048)")
	shareTaskStore := fs.Bool("share-task-store", false, "share task list between parent and child sessions")
	resultToolName := fs.String("result-tool-name", "", "override the result tool name")
	reasoningEffort := fs.String("reasoning-effort", "", "reasoning effort: minimal|low|medium|high|xhigh|max|none")
	exportATIF := fs.String("export-atif", "", "export ATIF trajectory to this path")
	exportATIFProviderHandles := fs.String("export-atif-provider-handles", "", "ATIF provider handle export mode: redacted|raw-local (default: redacted)")
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
	openAIResponsesContinuation := fs.String("openai-responses-continuation", "", "OpenAI Responses continuation mode: off|auto (default: off)")
	sandboxMode := fs.String("sandbox", "off", "sandbox mode: off (default), read-only, workspace-write, or restricted")
	sandboxNet := fs.String("sandbox-net", "on", "sandbox network egress on|off (default on; only applies with a non-off --sandbox mode)")
	cpuProfile := fs.String("cpu-profile", "", "write CPU profile to file")
	traceFile := fs.String("trace", "", "write execution trace to file")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: serf serve [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Start serf as an app-wire JSON-RPC server.\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nEnvironment variables:\n")
		printServeEnvVars(os.Stderr)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	resolvedOpenAIResponsesContinuation := resolveOpenAIResponsesContinuation(*openAIResponsesContinuation, nil)

	if *cpuProfile != "" {
		stop, err := deps.startCPUProfile(*cpuProfile)
		if err != nil {
			return fmt.Errorf("CPU profile: %w", err)
		}
		defer stop()
	}
	if *traceFile != "" {
		stop, err := deps.startTrace(*traceFile)
		if err != nil {
			return fmt.Errorf("trace: %w", err)
		}
		defer stop()
	}

	// Resolve working directory.
	wd := *workDir
	if wd == "" {
		var err error
		wd, err = deps.getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}
	}
	if err := deps.ensureConfigDirs(); err != nil {
		return err
	}
	if err := deps.seedMarketplaces(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: seeding default marketplaces: %v\n", err)
	}

	// Resolve state directory.
	// Priority: --state-dir flag > SERF_STATE_DIR env > XDG-computed default.
	var project identifier.Project
	sd := *stateDir
	if sd == "" {
		sd = envvars.SERFStateDir.Getenv()
	}
	if sd == "" {
		var err error
		project, sd, err = cmdutil.DefaultProjectStateDir(wd)
		if err != nil {
			return fmt.Errorf("resolve project state: %w", err)
		}
	}

	resuming := *resume != "" || *resumeLast
	var resumedMeta schema.SessionMeta
	if resuming {
		var metaErr error
		resumedMeta, metaErr = deps.resolveMeta(sd, *resume, *resumeLast)
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
	var modelRef cmdutil.ModelRef
	var err error
	if resuming {
		modelRef, err = cmdutil.ResolveResumeModelRef(*model, envvars.SERFModel.Getenv(), resumeProvider, resumeModel)
	} else {
		modelRef, err = cmdutil.ResolveModelRef(*model, envvars.SERFModel.Getenv(), "", "")
	}
	if err != nil {
		return err
	}

	effort, err := cmdutil.ResolveReasoningEffort(*reasoningEffort, envvars.SERFReasoningEffort.Getenv())
	if err != nil {
		return err
	}

	// Create LLM client and session.
	client, provCfg, hasProvConfig, closeClient, err := deps.newClient(sd, os.Stderr)
	if err != nil {
		return err
	}
	defer closeClient() //nolint:errcheck
	reserveSession, closeAPILog, err := deps.attachAPILogger(client, sd, os.Stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck
	if resuming {
		if err := reserveSession(resumedMeta.ID); err != nil {
			return err
		}
	}
	profile, err := deps.buildProfile(provCfg, modelRef, *outputSchema)
	if err != nil {
		return err
	}
	profile, err = deps.applyCheap(profile, *fastCheapModel, client)
	if err != nil {
		return err
	}
	env := execenv.NewLocalExecutionEnvironment(wd)
	sessionCfg := agent.SessionConfig{
		MaxToolRoundsPerInput:       cmdutil.MaxRoundsToConfig(*maxRounds),
		ShareTasksWithChildren:      *shareTaskStore,
		ResultToolName:              *resultToolName,
		StateDir:                    sd,
		AcquireSessionOwnership:     reserveSession,
		Project:                     project,
		SystemPromptFile:            *systemPrompt,
		SystemPromptAppend:          []string(systemPromptAppend),
		NoProjectPrompts:            *noProjectPrompts,
		AgentName:                   *agentName,
		SkillsDirs:                  []string(skillsDirs),
		MCPConfigFiles:              []string(mcpConfigs),
		MCPInline:                   []string(mcpServers),
		PluginDirs:                  plugins.NewManager("").EnabledPluginDirs([]string(pluginDirs)),
		ContextStrategy:             *contextStrategy,
		ExportATIFPath:              *exportATIF,
		ExportATIFProviderHandles:   *exportATIFProviderHandles,
		NonInteractive:              *nonInteractive,
		SystemPromptAsUser:          *systemPromptAsUser,
		ModelFallbacks:              []string(modelFallbacks),
		OpenAIResponsesContinuation: resolvedOpenAIResponsesContinuation,
		ResolveProfile:              cmdutil.BuildResolveProfile(provCfg, hasProvConfig),
	}
	if *maxSubagentDepth >= 0 {
		sessionCfg.MaxSubagentDepth = *maxSubagentDepth
	}
	if *maxConcurrentDelegates >= 0 {
		sessionCfg.MaxConcurrentDelegateTurns = *maxConcurrentDelegates
	}
	if *maxRetainedTerminal >= 0 {
		sessionCfg.MaxRetainedTerminal = *maxRetainedTerminal
	}
	if effort.Set {
		sessionCfg.ReasoningEffort = effort.Value
	}
	if err := configureSandbox(&sessionCfg, *sandboxMode, *sandboxNet); err != nil {
		return err
	}
	// Engage enforcement for a FRESH session from the flag-set mode. A resume
	// re-provisions the env from the PERSISTED mode inside
	// RestoreSessionFromMetaWithConfig (immutable across restart), so the flag
	// governs only new sessions here.
	if !resuming {
		if err := deps.provisionSandbox(env, &sessionCfg, env.WorkingDirectory()); err != nil {
			return err
		}
	}

	var sess *agent.Session
	if resuming {
		sess, err = deps.restoreSession(client, profile, env, resumedMeta, agent.RestoreSessionConfig{
			StateDir:                    sd,
			Project:                     project,
			ResolveProfile:              sessionCfg.ResolveProfile,
			AcquireSessionOwnership:     reserveSession,
			ModelFallbacks:              sessionCfg.ModelFallbacks,
			OpenAIResponsesContinuation: resolvedOpenAIResponsesContinuation,
		})
		if err != nil {
			return fmt.Errorf("restore session: %w", err)
		}
		if effort.Set {
			sess.SetReasoningEffort(effort.Value)
		}
		reportServeResume(os.Stderr, resumedMeta, modelRef, resumeProvider, resumeModel, strings.TrimSpace(*model) != "")
	} else {
		sess, err = deps.newSession(client, profile, env, sessionCfg)
		if err != nil {
			return fmt.Errorf("session creation: %w", err)
		}
	}

	// One startup line, loudly, states exactly what this host enforces (read from
	// the env's resolved policy so it never overstates). Empty for an unsandboxed
	// session — nothing to announce.
	printServeSandboxLine(os.Stderr, sandboxEnforcementLine(env))

	// Signal handling.
	ctx, cancel := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	listener, err := deps.listen(ctx, "tcp", *addr)
	if err != nil {
		sess.Close()
		return fmt.Errorf("listen %s: %w", *addr, err)
	}

	hubToken := envvars.SERFHubToken.Getenv()
	srv := deps.newServer(server.ServerConfig{
		AppReplaySize: *appReplaySize,
		HubToken:      hubToken,
		AllowedHost:   listener.Addr().String(),
	})
	// Seed the daemon's turn snapshot from this session's transcript BEFORE the
	// event bridge starts, so the first read answers from the same memory every
	// later notification advances. Preparation is the only fallible half; a
	// transcript that cannot be projected is a session this daemon must not
	// serve, because every read after it would silently start mid-conversation.
	prepared, err := deps.prepareAppIdentity("local", sess.ID(), sess.TranscriptPath())
	if err != nil {
		sess.Close()
		listener.Close() //nolint:errcheck // returning the preparation failure; the close error is not actionable
		return fmt.Errorf("prepare app identity: %w", err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
	rvRegistration := &rvreg.Registration{}

	var currentMu sync.RWMutex
	currentSess := sess
	// currentEnv tracks the CURRENT session's execution environment (each session
	// owns its own). /clear reads it to inherit the live sandbox and swaps it
	// alongside currentSess, so a cleared session's sandbox reflects what the running
	// session actually enforces (on resume the persisted mode, not the launch flag).
	currentEnv := env
	getSession := func() *agent.Session {
		currentMu.RLock()
		defer currentMu.RUnlock()
		return currentSess
	}
	setSession := func(next *agent.Session, nextEnv *execenv.LocalExecutionEnvironment) {
		currentMu.Lock()
		currentSess = next
		currentEnv = nextEnv
		currentMu.Unlock()
	}

	// The observer runs ON the bridge goroutine, which is the daemon's
	// authoritative consumer, so it must never block: see verboseEventTee.
	var eventObserver func(events.SessionEvent)
	var closeEventObserver func()
	if *verbose {
		verboseOut := deps.verboseOut
		if verboseOut == nil {
			verboseOut = os.Stderr
		}
		tee := newVerboseEventTee(verboseOut, verboseEventTeeBuffer)
		eventObserver = tee.observe
		closeEventObserver = tee.close
	}

	// Every bridge drain this serve starts, so teardown can wait for them.
	// A session gets a new one on /clear, and each ends when its own session's
	// event channel closes.
	var drainsMu sync.Mutex
	var bridgeDrains []<-chan struct{}
	// The tee must OUTLIVE every drain. Session.Close() closes the event channel
	// but does not wait for the buffered tail, so a drain is still calling the
	// observer after the session is closed -- and observe on a closed tee panics
	// on the drain's own goroutine, which kills the process and skips every
	// defer registered above this one. Waiting and closing live in ONE defer so
	// the order cannot be broken by inserting another defer between them.
	defer func() {
		drainsMu.Lock()
		pending := append([]<-chan struct{}(nil), bridgeDrains...)
		drainsMu.Unlock()
		for _, drained := range pending {
			<-drained
		}
		if closeEventObserver != nil {
			closeEventObserver()
		}
	}()

	var notifyCallback func()
	var subscriberCallback func() int
	var mutationRunnerMu sync.Mutex
	var mutationRunnerCancel context.CancelFunc
	var mutationRunnerDone <-chan struct{}
	setMutationRunner := func(cancel context.CancelFunc, done <-chan struct{}) {
		mutationRunnerMu.Lock()
		mutationRunnerCancel = cancel
		mutationRunnerDone = done
		mutationRunnerMu.Unlock()
	}
	clearMutationRunner := func(done <-chan struct{}) {
		mutationRunnerMu.Lock()
		if mutationRunnerDone == done {
			mutationRunnerCancel = nil
			mutationRunnerDone = nil
		}
		mutationRunnerMu.Unlock()
	}
	cancelAndWaitMutationRunner := func() {
		mutationRunnerMu.Lock()
		cancelRunner := mutationRunnerCancel
		runnerDone := mutationRunnerDone
		mutationRunnerMu.Unlock()
		if cancelRunner != nil {
			cancelRunner()
		}
		if runnerDone != nil {
			<-runnerDone
		}
	}
	bridgeSession := func(s *agent.Session) {
		// The idle kick is set on the Session, so it must be re-established
		// whenever the session is replaced (e.g. on /clear). It feeds the
		// first continuation prompt back into the serve loop's input channel
		// as an EntryContinuation-kind message (the agent module must not
		// import server, so this is a callback into the server, spec §C4/§7).
		s.SetKickFunc(func(prompt string) { srv.SubmitContinuation(prompt) })
		// The notify wake is wired alongside the kick: when a child finishes
		// and the parent is idle, the durable notification queue is already
		// populated; this callback feeds a text-less EntryNotification kick
		// into the serve loop so the parent drains it on the next turn.
		notifyCallback = func() { srv.SubmitNotification() }
		s.SetNotifyFunc(notifyCallback)
		s.SetClientMutationStartWakeFunc(func() {
			srv.SubmitClientMutationStart(s.ID())
		})
		// The M7 sandbox-escalation gate blocks a denied tool call only when a human
		// is actually watching this thread; the probe reads the live AppWire
		// subscriber count. Set per-session (like the kick/notify wakes) so it tracks
		// the current session's id across /clear.
		subscriberCallback = func() int { return deps.subscriberCount(srv, s.ID()) }
		s.SetSubscriberCountFunc(subscriberCallback)
		// Called synchronously, NOT with `go`: the bridge registers as the
		// session's authoritative consumer and then drains on its own
		// goroutine, so returning here means the registration has taken effect.
		// Spawning this would reopen the window in which the session is live
		// and its feed is still best-effort.
		drained := deps.bridge(srv, s, eventObserver)
		drainsMu.Lock()
		bridgeDrains = append(bridgeDrains, drained)
		drainsMu.Unlock()
	}

	srv.SetSandboxEscalationResolveFunc(func(id string, approve bool) error {
		return getSession().ResolveSandboxEscalation(id, approve)
	})
	srv.SetCompactFunc(func(ctx context.Context) error { return getSession().Compact(ctx) })
	// The steer RPC carries human-sent steering, so it takes the user-sourced
	// entry points: UIs render it as a user message, not a system steering
	// divider (issue #24).
	srv.SetSteerFunc(func(text string) { getSession().SteerFromUser(text) })
	srv.SetSteerWithImagesFunc(func(text string, images []server.ImageAttachment) {
		getSession().SteerFromUserWithImages(text, images)
	})
	srv.SetQueueFunc(func(text string) error { return getSession().Enqueue(ctx, text) })
	srv.SetQueueWithImagesFunc(func(text string, images []server.ImageAttachment) error {
		return getSession().EnqueueWithImages(ctx, text, images)
	})
	srv.SetGoalFunc(func(objective string) (bool, error) {
		if strings.TrimSpace(objective) == "" {
			getSession().ClearGoal()
			return false, nil
		}
		return getSession().SetGoal(ctx, objective)
	})
	srv.SetDrainAsSteerFunc(func() error { return getSession().DrainAsSteer(ctx) })
	srv.SetDrainAsSteerWithInputFunc(func(text string, images []server.ImageAttachment) error {
		return getSession().DrainAsSteerWithInput(ctx, text, images)
	})
	srv.SetPromoteQueuedAsSteerFunc(func(index int, expectedID string) error {
		return getSession().PromoteQueuedAsSteer(ctx, index, expectedID)
	})
	srv.SetCancelQueuedFunc(func(index int, expectedID string) (string, int, error) {
		return getSession().CancelQueued(ctx, index, expectedID)
	})
	srv.SetRetrySafeTurnFunctions(server.RetrySafeTurnFunctions{
		Start: func(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return getSession().AcceptClientMutationStart(params)
		},
		Steer: func(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			return getSession().AcceptClientMutationSteer(params)
		},
		Queue: func(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return getSession().AcceptClientMutationQueue(params)
		},
		Drain: func(params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			return getSession().AcceptClientMutationDrainAsSteer(params)
		},
		Promote: func(params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return getSession().AcceptClientMutationPromoteQueuedAsSteer(params)
		},
		Cancel: func(params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			return getSession().AcceptClientMutationCancelQueued(params)
		},
		Interrupt: func(waitCtx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			return getSession().InterruptClientMutation(waitCtx, params, func() {
				cancelAndWaitMutationRunner()
			})
		},
	})
	// One seam replaces sixteen read-time callbacks. The daemon samples session
	// state through this at the moments it changes (server/thread_envelope.go's
	// facetsByEvent) and never on a read.
	srv.SetThreadEnvelopeSource(liveThreadEnvelopeSource{session: getSession})
	pendingEscalations := func() []appwire.SandboxEscalationRequested {
		return mapServePendingEscalations(getSession().PendingEscalations())
	}
	srv.SetModelFunc(func(model string) error {
		sess := getSession()
		if err := sess.SetModel(model); err != nil {
			return err
		}
		// Refresh the daemon's cached session info synchronously (G2): status.Model
		// otherwise only updates on EventSessionStart, which never fires again
		// mid-session, so thread/read would report a stale model until the next
		// turn re-derived it.
		p := sess.Profile()
		srv.UpdateSessionInfo(sess.ID(), p.Model(), p.ID())
		return nil
	})
	srv.SetNameFunc(func(name string) { getSession().Rename(name) })
	srv.SetReasoningEffortFunc(func(effort string) { getSession().SetReasoningEffort(effort) })
	srv.SetListModelsFunc(cmdutil.ListModelsFunc(client, profile.ID()))
	srv.SetTasksFunc(func() any { return getSession().Tasks() })
	srv.SetClearFunc(func(ctx context.Context) error {
		oldSess := getSession()
		currentMu.RLock()
		oldEnv := currentEnv
		currentMu.RUnlock()
		clearCfg := sessionCfg
		clearCfg.SessionStartKind = plugin.SessionStartKindClear
		// The cleared session inherits the CURRENT session's ACTUAL sandbox (on resume
		// the persisted mode, not the launch flag), so its persisted config matches what
		// it runs under. Reconcile from the live env before building the new session.
		reconcileClearSandbox(&clearCfg, oldEnv)
		// Build and provision a FRESH env for the cleared session BEFORE any destructive
		// change. If provisioning fails, /clear aborts with oldSess still current rather
		// than leaving a live session running unconfined while persisting a sandbox mode
		// (a fail-open). Each session owns its own env + session tmp, disposed on Close,
		// so oldSess.Close() no longer pulls the tmp out from under the new session.
		clearEnv := execenv.NewLocalExecutionEnvironment(wd)
		if err := deps.provisionSandbox(clearEnv, &clearCfg, wd); err != nil {
			return fmt.Errorf("clear sandbox: %w", err)
		}
		newSess, err := deps.newClearSession(client, profile, clearEnv, clearCfg)
		if err != nil {
			clearEnv.Cleanup()
			return fmt.Errorf("new session: %w", err)
		}
		// Everything that can fail happens before anything shared moves, so the
		// swap itself is infallible and needs no rollback. A rollback is not
		// merely inelegant here: undoing a published identity means emitting a
		// second thread/closed, and every subscriber has already been told the
		// thread it is watching ended.
		prepared, err := deps.prepareAppIdentity("local", newSess.ID(), newSess.TranscriptPath())
		if err != nil {
			newSess.Close() // disposes clearEnv
			return fmt.Errorf("prepare app identity: %w", err)
		}
		// The rendezvous is the last fallible step and the daemon's public
		// address for this thread. Moving it before the replacement means a
		// client that discovers the new session id can always reach a daemon
		// already serving it; a failure here still names the old session, which
		// is still the live one.
		if err := deps.updateSessionID(rvRegistration, newSess.ID()); err != nil {
			newSess.Close() // disposes clearEnv
			return fmt.Errorf("rendezvous update: %w", err)
		}
		// One projection commit swaps the live session, the daemon's identity
		// and the turn snapshot, and closes the old thread's stream. No
		// notification can be projected between the halves, so no client sees
		// the old thread's authority answering for the new thread's id.
		srv.ReplaceAppIdentity(prepared, func() { setSession(newSess, clearEnv) })
		// The commit above zeroed the envelope with the identity it described.
		// Re-seed from the replacement session here rather than inside the
		// commit: sampling every facet reads jobs.jsonl and the task store, and
		// the commit holds the projection gate. Nothing is lost by seeding
		// after it -- the new session's own events are still queued in its
		// channel, and its bridge has not started.
		srv.RefreshThreadEnvelope()
		oldSess.Close() // disposes oldEnv
		bridgeSession(newSess)
		return nil
	})

	srv.SetWorkingDir(wd)
	srv.SetShutdownFunc(func() {
		cancel()
	})

	// Seed the envelope from the live session before its bridge starts. Every
	// envelope value changes at once when the session behind them changes, and
	// no single event announces that; this is that moment for the first one.
	srv.RefreshThreadEnvelope()
	// Bridge session events to appwire notifications.
	bridgeSession(sess)
	if deps.observeCallbacks != nil {
		deps.observeCallbacks(serveCallbackObserver{
			notify:          notifyCallback,
			subscriberCount: subscriberCallback, pendingEscalations: pendingEscalations,
			setSession: func(next *agent.Session) { setSession(next, currentEnv) },
			session:    sess,
		})
	}
	if resuming {
		// Belt-and-suspenders with the Bridge/projector SessionStart fix
		// (spec §5.4 "two touchpoints"): the session's SessionStart event may
		// already be sitting in its buffered event channel by the time the
		// bridge goroutine above is scheduled to drain it, so /status could
		// read the server's default state until then. Writing the restored
		// state here closes that window synchronously.
		srv.SetState(string(sess.State()))
	}

	// Input processing loop. Each turn runs under a per-turn cancellable
	// context that is wired into the server's interrupt handler so POST
	// /interrupt actually cancels the in-flight turn. The cancel is
	// cleared after the turn finishes so capabilities.interrupt only
	// reports true while a turn is in flight.
	inputLoopDone := make(chan struct{})
	go func() {
		defer close(inputLoopDone)
		processMessage := func(sess *agent.Session, msg server.InputMessage) bool {
			if msg.ClientMutationStart && msg.SessionID != sess.ID() {
				return false
			}
			runnerDone := make(chan struct{})
			var currentCancel context.CancelFunc
			var nextTurnCtx func(context.Context) (context.Context, context.CancelFunc)
			nextTurnCtx = func(root context.Context) (context.Context, context.CancelFunc) {
				drainCtx, cancelDrain := context.WithCancel(root)
				drainCtx = agent.WithQueuedInputDrainOnInterruptHandler(drainCtx, root, nextTurnCtx)
				currentCancel = cancelDrain
				srv.SetProcessing(true)
				srv.SetState(string(agent.SessionProcessing))
				srv.SetCancelFunc(cancelDrain)
				setMutationRunner(cancelDrain, runnerDone)
				return drainCtx, cancelDrain
			}
			turnCtx, cancelTurn := context.WithCancel(ctx)
			currentCancel = cancelTurn
			turnCtx = agent.WithQueuedInputDrainOnInterruptHandler(turnCtx, ctx, nextTurnCtx)
			if !msg.ClientMutationStart {
				srv.SetCancelFunc(cancelTurn)
				setMutationRunner(cancelTurn, runnerDone)
				if !holdServeStateForAwaitingWake(msg.Kind, sess.HasPendingAsk()) {
					srv.SetProcessing(true)
					srv.SetState(string(agent.SessionProcessing))
				}
			}
			var result string
			var processErr error
			processed := true
			if msg.ClientMutationStart {
				result, processed, processErr = sess.ProcessClientMutationStart(turnCtx, func() {
					srv.SetCancelFunc(cancelTurn)
					setMutationRunner(cancelTurn, runnerDone)
					srv.SetProcessing(true)
					srv.SetState(string(agent.SessionProcessing))
				})
			} else {
				result, processErr = sess.ProcessInputKind(turnCtx, msg.Text, msg.Images, msg.Kind)
			}
			srv.SetProcessing(false)
			srv.SetCancelFunc(nil)
			srv.SetState(sess.WireState())
			currentCancel()
			close(runnerDone)
			clearMutationRunner(runnerDone)
			if processErr != nil {
				fmt.Fprintf(os.Stderr, "[serve] error: %v\n", processErr)
			}
			_ = result
			return processed
		}
		for {
			sess := getSession()
			if !processNextServeInput(ctx, srv.InputCh(), sess.ID(), func(msg server.InputMessage) bool {
				return processMessage(getSession(), msg)
			}) {
				return
			}
		}
	}()

	// Start HTTP server.
	fmt.Fprintf(os.Stderr, "[serve] listening on %s (session %s)\n", listener.Addr(), getSession().ID())

	spawnedBy := "user"
	if envvars.SERFHubSpawned.Getenv() == "1" {
		spawnedBy = "hub"
	}
	runDir := *runDirFlag
	if runDir == "" {
		runDir = envvars.SERFRunDir.Getenv()
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
	if err := deps.register(rvRegistration, runDir, rvEntry); err != nil {
		fmt.Fprintf(os.Stderr, "[serve] rendezvous write failed: %v\n", err)
	} else {
		defer func() {
			_ = rvRegistration.Remove()
		}()
	}

	httpSrv := &http.Server{Handler: srv}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		_ = httpSrv.Close()
		<-inputLoopDone
		getSession().Close()
	}()

	if err := deps.serveHTTP(httpSrv, listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		cancel()
		<-shutdownDone
		return err
	}
	<-shutdownDone
	return nil
}

// processNextServeInput gives durable turn/start work priority over the
// process-local wake channel. The caller repeats it after every processed
// message, so an accepted start still progresses when its wake was coalesced
// because the channel was full.
func processNextServeInput(
	ctx context.Context,
	input <-chan server.InputMessage,
	sessionID string,
	process func(server.InputMessage) bool,
) bool {
	if process(server.InputMessage{ClientMutationStart: true, SessionID: sessionID}) {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case msg, ok := <-input:
		if !ok {
			return false
		}
		process(msg)
		return true
	}
}

func mapServePendingEscalations(data []events.SandboxEscalationRequestedData) []appwire.SandboxEscalationRequested {
	out := make([]appwire.SandboxEscalationRequested, 0, len(data))
	for _, d := range data {
		out = append(out, appwire.SandboxEscalationRequested{
			EscalationID: d.EscalationID, Mode: d.Mode, Tool: d.Tool, Kind: d.Kind,
			DeniedPath: d.DeniedPath, Command: d.Command, OutputSoFar: d.OutputSoFar, PartiallyRan: d.PartiallyRan,
		})
	}
	return out
}

func reportServeResume(w io.Writer, meta schema.SessionMeta, model cmdutil.ModelRef, oldProvider, oldModel string, overridden bool) {
	if overridden {
		_, _ = fmt.Fprintf(w, "[serve] resumed session %s with model override %s (was %s/%s)\n", meta.ID, model.Qualified(), oldProvider, oldModel)
	} else {
		_, _ = fmt.Fprintf(w, "[serve] resumed session %s (%d turns)\n", meta.ID, meta.TurnCount)
	}
}

func printServeSandboxLine(w io.Writer, line string) {
	if line != "" {
		_, _ = fmt.Fprintln(w, line)
	}
}

// holdServeStateForAwaitingWake reports whether the input loop should skip
// its Processing shadow-write for this message: the session-level entry gate
// (agent/session_lifecycle.go's processInputKindWithProvenance, spec §5.3)
// refuses autonomous wakes while a question is pending, before any state
// transition — so the /status shadow must not flip to active around a wake
// the session will refuse (the flicker's active→awaiting edge would re-fire
// the OS notification, notifications.js Task 11). Mirrors the gate's
// predicate exactly — hasPendingAsk, not raw state (attention-status-model
// v5 reconciliation: SessionAwaiting alone no longer implies a pending
// question, so a general inbox-semantics re-arm with no ask pending must NOT
// be held; async wakes re-arm by design there). EntryUserInput is always let
// through since it is how the reply resolves a pending ask (spec §5.2).
func holdServeStateForAwaitingWake(kind agent.EntryKind, hasPendingAsk bool) bool {
	return kind != agent.EntryUserInput && hasPendingAsk
}

func printServeEnvVars(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, v := range []envvars.Var{
		envvars.SERFModel,
		envvars.SERFOpenAIResponsesContinuation,
		envvars.SERFReasoningEffort,
		envvars.SERFStateDir,
		envvars.SERFRunDir,
		envvars.SERFHubToken,
		envvars.SERFHubSpawned,
		envvars.SERFAllowedDecisions,
		envvars.SERFProvidersConfig,
	} {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", v.Name, v.Summary)
	}
	_ = tw.Flush()
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
	allowedDecisions := cmdutil.ParseAllowedDecisions(envvars.SERFAllowedDecisions.Getenv())
	return provider.WithAllowedDecisions(p, allowedDecisions), nil
}

// applyFastCheapModel sets the auxiliary cheap model for side calls. The ref is
// "provider/model" to route side calls to a different provider instance than the
// active model, or a bare "model" to keep the active provider. A cross-provider
// cheap provider must be registered in the client (i.e. configured AND
// credentialed) — that is what actually lets the side call route, unlike a
// config-only check which is credential-blind.
func applyFastCheapModel(profile *provider.Profile, raw string, client *llm.Client) (*provider.Profile, error) {
	if profile == nil || strings.TrimSpace(raw) == "" {
		return profile, nil
	}
	raw = strings.TrimSpace(raw)
	if cheapProvider, model, ok := strings.Cut(raw, "/"); ok && cheapProvider != "" && model != "" && cheapProvider != profile.ID() {
		if !clientHasProvider(client, cheapProvider) {
			return nil, fmt.Errorf("--fast-cheap-model provider %q is not configured or has no credential (active provider %q); available providers: %s",
				cheapProvider, profile.ID(), strings.Join(client.ProviderNames(), ", "))
		}
	}
	return provider.WithCheapModel(profile, raw), nil
}

func clientHasProvider(client *llm.Client, name string) bool {
	if client == nil {
		return false
	}
	for _, p := range client.ProviderNames() {
		if strings.EqualFold(p, name) {
			return true
		}
	}
	return false
}

// serfUsageFromLLM maps a session's cumulative llm.Usage to the wire
// appwire.SerfUsage shown in /status and thread/read. Returns nil when every
// total (including CacheReadTokens) is zero — a fresh session, an old daemon
// that never seeded usage, or a Codex thread — so the status row hides the
// usage cluster rather than rendering ↑0 ↓0 (WS2 A7).
func serfUsageFromLLM(u llm.Usage) *appwire.SerfUsage {
	return appwire.SerfUsageFromLLM(u)
}

func agentToServerDetailedStatus(ds agent.DetailedStatus) server.DetailedStatus {
	var out server.DetailedStatus

	for _, t := range ds.Tools {
		out.Tools = append(out.Tools, server.ToolInfo{Name: t.Name, Source: t.Source})
	}
	for _, m := range ds.MCP {
		out.MCP = append(out.MCP, server.MCPServerInfo{Name: m.Name, Tools: m.Tools, Status: m.Status, Error: m.Error})
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
	for _, job := range ds.Jobs {
		out.Jobs = append(out.Jobs, server.JobStatusInfo{
			JobID:            job.JobID,
			JobType:          job.JobType,
			Status:           job.Status,
			Reason:           job.Reason,
			ExhaustionBudget: job.ExhaustionBudget,
			ExhaustionLimit:  job.ExhaustionLimit,
			Resumable:        job.Resumable,
			ExitCode:         job.ExitCode,
			OutputBytes:      job.OutputBytes,
			TranscriptRef:    job.TranscriptRef,
		})
	}
	out.Agents = ds.Agents

	return out
}

// liveThreadEnvelopeSource is the daemon's one window onto live session state
// for the materialized thread envelope. The server samples it at the moments
// those values change (server/thread_envelope.go's facetsByEvent), never on a
// read: every method here can take the session's mutex, the task store's, or
// read jobs.jsonl, and a read path that could reach them would hold the
// projection gate across that work.
//
// It resolves the session per call rather than capturing one, so it follows
// /clear onto the replacement session exactly as the sixteen closures it
// replaced did.
type liveThreadEnvelopeSource struct {
	session func() *agent.Session
}

func (l liveThreadEnvelopeSource) ContextPressure() float64 {
	return l.session().ContextPressure()
}

func (l liveThreadEnvelopeSource) ContextMetrics() server.ContextMetrics {
	metrics := l.session().ContextMetrics()
	return server.ContextMetrics{Used: metrics.Used, Window: metrics.Window, Remaining: metrics.Remaining}
}

func (l liveThreadEnvelopeSource) DetailedStatus() server.DetailedStatus {
	return agentToServerDetailedStatus(l.session().DetailedStatus())
}

func (l liveThreadEnvelopeSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	return l.session().ClientMutationProjection()
}

// TaskAggregate reports nil on a persisted-store failure. That is unavailable
// task state, not an authoritative empty list, and the wire distinguishes them.
func (l liveThreadEnvelopeSource) TaskAggregate() *appwire.TaskAggregate {
	tasks, err := l.session().TasksWithError()
	if err != nil {
		return nil
	}
	done := 0
	for _, task := range tasks {
		if task.Status == taskpkg.TaskDone {
			done++
		}
	}
	return &appwire.TaskAggregate{Total: len(tasks), Done: done}
}

func (l liveThreadEnvelopeSource) GoalStatus() (string, int, bool) {
	return l.session().GoalStatus()
}

func (l liveThreadEnvelopeSource) WorkMetrics() (int64, *appwire.SerfUsage, int64) {
	sess := l.session()
	return sess.WorkMillisSnapshot(), serfUsageFromLLM(sess.CumulativeUsageSnapshot()), sess.ActiveTurnStartedAtMillis()
}

func (l liveThreadEnvelopeSource) FailedToolCalls() (int, bool) {
	return l.session().FailedToolCallsSnapshot()
}

func (l liveThreadEnvelopeSource) AskPending() bool {
	return l.session().HasPendingAsk()
}

func (l liveThreadEnvelopeSource) PendingEscalations() []appwire.SandboxEscalationRequested {
	return mapServePendingEscalations(l.session().PendingEscalations())
}

func (l liveThreadEnvelopeSource) ReasoningInfo() (string, []string, bool) {
	sess := l.session()
	p := sess.Profile()
	return sess.ReasoningEffort(), p.ReasoningEffortLevels(), p.SupportsReasoning()
}

func (l liveThreadEnvelopeSource) SessionMeta() schema.SessionMeta {
	return l.session().Meta()
}
