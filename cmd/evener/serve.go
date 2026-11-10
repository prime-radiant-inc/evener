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
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/commitprovenance"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// shutdownDrainWaitBudget bounds how long a returning serve waits for its
// bridge drains before abandoning them. See the teardown defer in
// runServeWithDeps for what expiry does and, more importantly, what it
// deliberately does not do.
const shutdownDrainWaitBudget = 30 * time.Second

// Daemon HTTP listener deadlines: ReadHeaderTimeout bounds the pre-auth window
// before a peer finishes its request headers (before AuthGuard runs);
// IdleTimeout bounds an idle keep-alive socket. Neither governs a hijacked
// AppWire connection, so long-lived streams are unaffected.
const (
	daemonHTTPReadHeaderTimeout = 10 * time.Second
	daemonHTTPIdleTimeout       = 120 * time.Second
)

// retirementReaderDrainBudget bounds how long a committed retirement waits for
// in-flight readers to leave before giving up on them. A drain that runs out
// skips the release that follows it — release expects a drained readers set —
// and the process exits anyway: once Commit has closed admission for good, a
// teardown failure is logged and left observable in the lifecycle, never a
// reason to stay resident and never a refused retire. Admission never reopens,
// and no kill is forced.
const retirementReaderDrainBudget = 30 * time.Second

// retirementTeardownError marks a retirement teardown failure that happened
// AFTER Commit. Commit is terminal: admission is closed permanently and the
// process exits regardless, so the failure is observability — it is logged and
// left in the lifecycle's Failure field — never a refused retire. requestRetirement
// maps it to the truthful accepted outcome instead of an RPC error.
type retirementTeardownError struct{ err error }

func (e *retirementTeardownError) Error() string { return e.err.Error() }
func (e *retirementTeardownError) Unwrap() error { return e.err }

// rendezvousRemovalAttempts bounds how many times shutdown asks for its
// rendezvous entry to be removed. Registration.Remove keeps a failed removal
// retryable rather than terminal, precisely so a transient filesystem failure
// gets another pass; spending exactly one attempt made that retryability dead
// code. Past this budget the failure is not transient, and an exiting daemon
// that discards it leaves a PID artifact discovery reads as a live daemon at a
// PID the OS is free to reuse.
const rendezvousRemovalAttempts = 3

// rendezvousRemovalRetryPause spaces those attempts so a directory that is
// momentarily busy has time to settle.
const rendezvousRemovalRetryPause = 50 * time.Millisecond

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
	// startup install and thread/clear publish a PreparedAppIdentity, so every
	// fallible step has already happened by the time anything is announced.
	ReplaceAppIdentity(server.PreparedAppIdentity, func())
	SetSandboxEscalationResolveFunc(func(string, bool) error)
	SetDelegateStopFunc(func(string) (appwire.DelegateStopOutcome, error))
	SetCompactFunc(func(context.Context) error)
	SetGoalFunc(func(string) (bool, error))
	SetNotesHumanSetFunc(func(outerID, note string) (appwire.NotesHumanSetResponse, error))
	SetUrlsRemoveFunc(func(outerID, id string) (bool, error))
	// SetThreadEnvelopeSource replaces sixteen read-time session callbacks with
	// one seam the daemon samples at change time. See server/thread_envelope.go.
	SetThreadEnvelopeSource(server.ThreadEnvelopeSource)
	RefreshThreadEnvelope()
	SetModelFunc(func(string) error)
	SetVisionModelFunc(func(string) error)
	UpdateSessionInfo(sessionID, model, profile string)
	UpdateSessionInfoFrom(sessionID string, sample func() (model, profile string))
	SetNameFunc(func(string) error)
	SetReasoningEffortFunc(func(string) error)
	SetListModelsFunc(func(context.Context) ([]appwire.ModelDescriptor, error))
	// SetCostLookupFunc is the one place a dollar figure enters the daemon:
	// the live session's registry resolution of an instance/model reference.
	SetCostLookupFunc(func(string) *registry.Cost)
	SetTasksFunc(func() []appwire.Task)
	SetJobsFunc(func(context.Context, appwire.JobsListParams) (any, error))
	SetThreadActivityReadFunc(func(context.Context, appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error))
	SetThreadDelegatesListFunc(func(context.Context, appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error))
	SetThreadJobsListFunc(func(context.Context, appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error))
	SetThreadWatchesListFunc(func(context.Context, appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error))

	SetJobOutputFunc(func(string, *int64, int64) (appwire.JobOutputPage, bool, error))
	SetJobGetFunc(func(string) (appwire.JobActivityJob, bool, error))
	SetClearFunc(func(context.Context, appwire.ThreadClearParams) error)
	SetWorkingDir(string)
	SetShutdownFunc(func())
	SetDaemonLifecycle(func() appwire.DaemonLifecycle, func(context.Context, appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error))
	// SetDaemonIdleTimeoutSet installs the hook behind
	// evener/daemon/idle-timeout/set, which retargets the automatic
	// idle-retirement deadline.
	SetDaemonIdleTimeoutSet(func(context.Context, appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error))
	// SetRetirementAdmission installs the process-owned admission boundary on
	// the daemon's router. The callback receives the access kind ("read" or
	// "mutation") and returns the lease release the handler runs after it
	// returns.
	SetRetirementAdmission(func(context.Context, string) (func(), error))
	SetProcessing(bool)
	// SetProcessingTurn publishes a running execution's TurnID; serve
	// installs it as each session's execution-started func.
	SetProcessingTurn(string)
	SetState(string)
	SetCancelFunc(context.CancelFunc)
	SetRetrySafeTurnFunctions(server.RetrySafeTurnFunctions)
	RecordDescendantAppEvent(string, events.SessionEvent)
	// WireTranscriptHistory installs a session's recorded-entry hooks into
	// the daemon's thread histories.
	WireTranscriptHistory(*agent.Session)
	// Close stops the daemon's thread histories.
	Close()
	SetDescendantTranscriptPathFunc(func(threadID string) string)
	SetDescendantLiveWatchesFunc(func(threadIDs []string) map[string][]agent.WatchStatusInfo)
	SetDescendantQuotaWaitsFunc(func(threadIDs []string) map[string]*appwire.ThreadFailure)
	SetSubagentTallyFunc(func() (appwire.SubagentTally, bool))
	InputCh() <-chan server.InputMessage
	SubmitContinuation(string, string)
	SubmitNotification()
	SubmitClientMutationStart(string)
	SubmitPendingUserInput(string)
}

type serveDeps struct {
	newFlagSet       func(string, flag.ErrorHandling) *flag.FlagSet
	getwd            func() (string, error)
	ensureConfigDirs func() error
	seedMarketplaces func(context.Context) error
	resolvePlugins   func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error)
	resolveMeta      func(string, string, bool) (schema.SessionMeta, error)
	newClient        func(string, io.Writer) (*llm.Client, func() error, error)
	attachAPILogger  func(*llm.Client, string, io.Writer, bool) (func(string) error, func() error, error)
	buildProfile     func(context.Context, *llm.Client, cmdutil.ModelRef, string) (*provider.Profile, *agent.StartupModelListing, error)
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
	// onDrained runs when that drain has FINISHED. Anything the observer writes
	// to must outlive it: Session.Close() closes the event channel but does not
	// wait for the buffered tail to be delivered, so "the session is closed" is
	// not "the consumer is done". It is a parameter rather than a return so a
	// caller cannot silently skip the wait.
	bridge func(serveServer, *agent.Session, func(events.SessionEvent), func())
	// drainWaitExpiry starts the shutdown budget for waiting on bridge drains
	// and returns the channel that fires when it runs out. Injectable because a
	// test must be able to drive expiry on demand: a test that passes because a
	// real timer happened to fire proves the budget exists, not that it is
	// honoured, and the whole point of the budget is which of the two arms runs.
	drainWaitExpiry func() <-chan time.Time
	// rendezvousRetryPause starts the pause between shutdown's rendezvous
	// removal attempts and returns the channel that fires when it is over.
	// Injectable for the same reason drainWaitExpiry is: a test that clears a
	// blocked removal on a real timer races the retry instead of driving it,
	// and which attempt does the removing is the whole claim.
	rendezvousRetryPause func() <-chan time.Time
	// verboseOut is where --verbose writes its NDJSON. Nil means os.Stderr.
	// Injectable so a test can wedge it: the reason the tee exists is that the
	// real one can be a pipe nobody drains, and that is not reproducible against
	// a real terminal.
	verboseOut io.Writer
	// serveLogOut is where the daemon writes its own diagnostic lines, the
	// [serve] records below and the retirement teardown's. Nil means os.Stderr.
	// Injectable so a failure-injection fixture can own the sink and assert on
	// the diagnostics it deliberately provokes instead of leaking them into test
	// output.
	serveLogOut     io.Writer
	subscriberCount func(serveServer, string) int
	notifyContext   func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	// runContext derives the run's lifetime from the signal context. Its cancel
	// is what thread/shutdown and retirement call to start the exit path, so it
	// is injectable for a test that must gate exactly that call.
	runContext       func(context.Context) (context.Context, context.CancelFunc)
	startCPUProfile  func(string) (func(), error)
	startTrace       func(string) (func(), error)
	startLivePprof   func(logf func(format string, args ...any)) (indexURL string, stop func(), err error)
	register         func(*rvreg.Registration, string, rendezvous.Entry) error
	serveHTTP        func(*http.Server, net.Listener) error
	provisionSandbox func(*execenv.LocalExecutionEnvironment, *agent.SessionConfig, string) error
	newClearSession  func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error)
	// prepareAppIdentity validates a session's transcript into an installable
	// AppWire identity. It is the one fallible step of an identity swap, so it
	// is injectable: a test needs a deterministic preparation failure to prove
	// thread/clear abandons a half-built session instead of publishing it.
	prepareAppIdentity func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error)
	updateIdentity     func(*rvreg.Registration, string, string) error
	observeCallbacks   func(serveCallbackObserver)
	// reclaimScratch removes the session scratch that daemons now gone
	// retained, for a serve working in the directory it is handed — the one
	// resolved from --dir, so the reclaim reads the workspace the daemon
	// allocates against. Only the production entry point sets it, because
	// reclaiming belongs to a real daemon start: tests drive runServe and
	// runServeWithDeps, and the nil they leave here is what keeps a test run
	// off the developer's own scratch bases.
	reclaimScratch func(workingDir string)
	// retirementClock is the retirement controller's sole source of time.
	// Nil means agent.RealRetirementClock(); tests inject a fake to drive the
	// idle deadline deterministically.
	retirementClock agent.RetirementClock
	// retirementObserve reports the retirement pipeline's observable beats —
	// root_published, claim_consumed, prepared, committed, released — with
	// the root session id each beat belongs to. Nil in production; invoked
	// outside every lock. It exists so a test can sequence virtual-time
	// advances against the pipeline and hold a claim open at a gate.
	retirementObserve func(event, rootID string)
	// retirementCommit is the controller's Commit step. Nil in production, where
	// the controller's own Commit runs; a test injects a failure to drive the
	// pre-commit rollback that must abort the claim and reopen the exit.
	retirementCommit func(*agent.RetirementController, *agent.RetirementClaim) error
}

type serveCallbackObserver struct {
	notify             func()
	subscriberCount    func() int
	pendingEscalations func() []appwire.SandboxEscalationRequested
	setSession         func(*agent.Session)
	session            *agent.Session
}

// diagnosticOut is where the daemon writes its own diagnostic lines: the
// test-owned sink when one is installed, otherwise os.Stderr.
func (d serveDeps) diagnosticOut() io.Writer {
	if d.serveLogOut != nil {
		return d.serveLogOut
	}
	return os.Stderr
}

func defaultServeDeps() serveDeps {
	return serveDeps{
		newFlagSet: flag.NewFlagSet,
		getwd:      os.Getwd, ensureConfigDirs: cmdutil.EnsureUserConfigDirs,
		resolveMeta: cmdutil.ResolveSessionMeta, newClient: newUnloggedServeLLMClient,
		attachAPILogger: serveAttachSessionAPILogger,
		buildProfile:    buildInitialProfile, applyCheap: cmdutil.ApplyFastCheapModel,
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
		bridge: func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
			srv := s.(*server.Server)
			sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
				server.BridgeEvent(srv, ev, observer)
			}, onDrained)
		},
		drainWaitExpiry: func() <-chan time.Time { return time.After(shutdownDrainWaitBudget) },
		subscriberCount: func(s serveServer, id string) int { return s.(*server.Server).AppSubscriberCount(id) },
		notifyContext:   signal.NotifyContext, runContext: context.WithCancel,
		startCPUProfile: cmdutil.StartCPUProfile, startTrace: cmdutil.StartTrace,
		startLivePprof:       cmdutil.StartLivePprof,
		register:             func(r *rvreg.Registration, dir string, entry rendezvous.Entry) error { return r.Register(dir, entry) },
		rendezvousRetryPause: func() <-chan time.Time { return time.After(rendezvousRemovalRetryPause) },
		serveHTTP:            func(s *http.Server, l net.Listener) error { return s.Serve(l) },
		provisionSandbox:     provisionSandbox,
		newClearSession:      agent.NewSession,
		prepareAppIdentity:   server.PrepareAppIdentityForRef,
		updateIdentity:       func(r *rvreg.Registration, id, workingDir string) error { return r.UpdateIdentity(id, workingDir) },
	}
}

func newServeLLMClient(stateDir string, warnings io.Writer) (*llm.Client, func() error, error) {
	client, closeClient, err := newUnloggedServeLLMClient(stateDir, warnings)
	if err != nil {
		return nil, nil, err
	}
	closeAPILog, err := serveAttachAPILogger(client, stateDir, warnings)
	if err != nil {
		_ = closeClient()
		return nil, nil, err
	}
	return client, func() error {
		apiLogErr := closeAPILog()
		clientErr := closeClient()
		if apiLogErr != nil {
			return apiLogErr
		}
		return clientErr
	}, nil
}

func newUnloggedServeLLMClient(stateDir string, warnings io.Writer) (*llm.Client, func() error, error) {
	client, err := serveLoadClient(stateDir)
	if err != nil {
		return nil, nil, fmt.Errorf("LLM client: %w", err)
	}
	printRegistryNotices(warnings, client.Registry())
	return client, func() error { return nil }, nil
}

// printRegistryNotices writes the registry's load warnings and its stray
// OAuth records to w at startup (spec §9.5): a misconfigured instance or an
// unreadable auth record is announced once, not swallowed.
func printRegistryNotices(w io.Writer, r *registry.Registry) {
	if w == nil || r == nil {
		return
	}
	for _, notice := range append(r.Warnings(), r.StrayOAuthRecords()...) {
		_, _ = fmt.Fprintln(w, "warning:", notice)
	}
}

func runServe(args []string) error {
	return runServeWithDeps(args, defaultServeDeps())
}

// startupInterrupted reports an interrupt that arrived during a startup step
// which could not read the context itself. Seeding marketplaces waits on the
// plugin store lock, probing the login shell PATH and provisioning the sandbox
// run subprocesses, and net.ListenConfig.Listen on a literal address binds
// happily with a cancelled context — so the interrupt is only noticed where
// the context is read, and a startup that was interrupted has to say so and
// fail rather than bind, shut itself down and exit 0 in silence.
func startupInterrupted(ctx context.Context, step startupStep) error {
	if err := ctx.Err(); err != nil {
		return &startupInterruptedError{step: step, err: err}
	}
	return nil
}

// startupStep names a startup step that cannot read the context itself.
type startupStep string

const (
	stepSeedingMarketplaces   startupStep = "seeding default marketplaces"
	stepProbingLoginShellPath startupStep = "probing the login shell PATH"
	stepProvisioningSandbox   startupStep = "provisioning the sandbox"
	stepCreatingSession       startupStep = "creating the session"
)

// startupInterruptedError reports the startup step an interrupt ended.
type startupInterruptedError struct {
	step startupStep
	err  error
}

func (e *startupInterruptedError) Error() string {
	return fmt.Sprintf("interrupted while %s: %v", e.step, e.err)
}

func (e *startupInterruptedError) Unwrap() error { return e.err }

// armInterruptRunner arms the mutation runner and the server's cancel func in
// the one order an accepted interrupt needs: the runner first.
//
// The interrupt path does not consult the server cancel func; it calls
// serveBridge.cancelAndWaitMutationRunner, which reads the runner fields under
// mutationRunnerMu and then waits for runnerDone before it finalizes the fence.
// Armed cancel-first, a Stop accepted between the two statements finds the runner
// fields still nil, returns without waiting, finalizes the fence and clears it --
// and the claimed turn then runs the very turn the user stopped. One definition,
// so the three arming sites cannot drift into three different orders.
func armInterruptRunner(setRunner, setCancel func()) {
	setRunner()
	setCancel()
}

func runServeWithDeps(args []string, deps serveDeps) error {
	logOut := deps.diagnosticOut()
	fs := deps.newFlagSet("serve", flag.ExitOnError)
	lf := registerLaunchFlags(fs)
	addr := fs.String("addr", "127.0.0.1:9131", "listen address")
	runDirFlag := fs.String("run-dir", "", "override rendezvous run directory")
	appReplaySize := fs.Int("app-replay-size", 0, "AppWire notification replay ring size (default 1000)")
	nonInteractive := fs.Bool("non-interactive", false, "mark this daemon session as headless/non-interactive")
	managerMode := fs.Bool("manager", false, "start this session as a Manager of the sessions its hub can reach (a start-time property; persisted with the session)")
	pluginRoot := fs.String("plugin-root", "", "internal plugin registry root override")
	agentsDoc := fs.String("agents-doc", "", "personal AGENTS.md loaded ahead of project docs (default: <user config root>/AGENTS.md)")
	var modelFallbacks cmdutil.StringSliceFlag
	fs.Var(&modelFallbacks, "model-fallback", "fallback model (provider/model) tried on permanent provider errors (repeatable)")
	daemonIdleTimeout := fs.Duration("daemon-idle-timeout", 0, "retire this daemon after continuous proven inactivity (default: 0 = automatic retirement disabled; the Hub passes its configured value)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(logOut, "Usage: evener serve [flags]\n\n")
		_, _ = fmt.Fprintf(logOut, "Start evener as an app-wire JSON-RPC server.\n\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(logOut, "\nEnvironment variables:\n")
		printServeEnvVars(logOut)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *daemonIdleTimeout < 0 {
		return fmt.Errorf("--daemon-idle-timeout must not be negative (got %v)", *daemonIdleTimeout)
	}
	pluginManager := plugins.NewManager(*pluginRoot)
	if err := rejectPluginSelectionWithResume(lf.enabledPlugins.Value(), *lf.resume, *lf.resumeLast); err != nil {
		return err
	}
	// Signal handling, installed before the first thing that can wait on
	// something: resolving plugins takes the plugin store lock, which an
	// install or an auto-upgrade can be holding across git fetches, and an
	// interrupt has to end that wait rather than being ignored until the
	// daemon reaches its listener.
	//
	// The handler stays registered until serve returns, so the run gets its own
	// context. thread/shutdown and idle retirement end the run by cancelling
	// ctx, and teardown -- where the session closes and
	// kills its background jobs -- comes after that. Stopping NotifyContext
	// there would restore SIGTERM's default disposition, so a supervisor's
	// SIGTERM landing mid-teardown would kill the daemon on the spot and orphan
	// every background job it owns. A repeat SIGTERM during teardown is
	// swallowed on purpose: exiting on it would orphan the jobs the same way,
	// and the drain-wait budget already bounds a wedged teardown.
	signalCtx, stopSignals := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := deps.runContext(signalCtx)
	defer cancel()

	// Before anything that can create the user config root: the legacy-data
	// guard inside EnsureUserConfigDirs reads an existing root as already
	// migrated, and resolving a requested bundled plugin materializes it under
	// exactly that root. Running the guard second would strand a user's legacy
	// configuration and credentials silently.
	if err := deps.ensureConfigDirs(); err != nil {
		return err
	}
	resolvePlugins := deps.resolvePlugins
	if resolvePlugins == nil {
		resolvePlugins = func(ctx context.Context, explicit []string, enabled *[]string) (plugins.LaunchPluginResolution, error) {
			return pluginManager.ResolveForLaunch(ctx, explicit, enabled)
		}
	}
	resolvedPlugins, resolveErr := resolvePlugins(ctx, []string(lf.pluginDirs), lf.enabledPlugins.Value())
	if fatal := fatalLaunchPluginError(resolveErr, lf.enabledPlugins.Value()); fatal != nil {
		return fatal
	}
	if resolveErr != nil {
		_, _ = fmt.Fprintf(logOut, "warning: listing installed plugins: %v\n", resolveErr)
	}
	renderLaunchPluginDiagnostics(logOut, resolvedPlugins.Diagnostics)
	if err := resolvedPlugins.ValidateSelection(); err != nil {
		return err
	}
	settings, err := resolveLaunchSettings(*lf.providerIdleTimeout, *lf.apiLog, *lf.openAIResponsesContinuation)
	if err != nil {
		return err
	}

	if *lf.cpuProfile != "" {
		stop, err := deps.startCPUProfile(*lf.cpuProfile)
		if err != nil {
			return fmt.Errorf("CPU profile: %w", err)
		}
		defer stop()
	}
	if *lf.traceFile != "" {
		stop, err := deps.startTrace(*lf.traceFile)
		if err != nil {
			return fmt.Errorf("trace: %w", err)
		}
		defer stop()
	}
	_, stopPprof, pprofErr := deps.startLivePprof(func(format string, args ...any) {
		_, _ = fmt.Fprintf(logOut, format+"\n", args...)
	})
	if pprofErr != nil {
		return pprofErr
	}
	defer stopPprof()

	// Resolve working directory.
	wd := *lf.workDir
	if wd == "" {
		var err error
		wd, err = deps.getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory: %w", err)
		}
	}
	if deps.reclaimScratch != nil {
		deps.reclaimScratch(wd)
	}
	seedMarketplaces := deps.seedMarketplaces
	if seedMarketplaces == nil {
		seedMarketplaces = func(ctx context.Context) error {
			_, err := pluginManager.SeedDefaultMarketplaces(ctx)
			return err
		}
	}
	if err := seedMarketplaces(ctx); err != nil {
		_, _ = fmt.Fprintf(logOut, "warning: seeding default marketplaces: %v\n", err)
	}
	if err := startupInterrupted(ctx, stepSeedingMarketplaces); err != nil {
		return err
	}

	// Resolve state directory.
	// Priority: --state-dir flag > EVENER_STATE_DIR env > XDG-computed default.
	var project identifier.Project
	sd := *lf.stateDir
	if sd == "" {
		sd = envvars.EVENERStateDir.Getenv()
	}
	if sd == "" {
		var err error
		project, sd, err = cmdutil.DefaultProjectStateDir(wd)
		if err != nil {
			return fmt.Errorf("resolve project state: %w", err)
		}
	}
	// Rendezvous ownership must identify the same state files regardless of the
	// hub's working directory, including when serve accepts a relative path.
	absoluteStateDir, stateDirErr := filepath.Abs(sd)
	if stateDirErr != nil {
		return fmt.Errorf("resolve absolute state directory: %w", stateDirErr)
	}
	sd = absoluteStateDir
	resuming := *lf.resume != "" || *lf.resumeLast
	var resumedMeta schema.SessionMeta
	if resuming {
		var metaErr error
		resumedMeta, metaErr = deps.resolveMeta(sd, *lf.resume, *lf.resumeLast)
		if metaErr != nil {
			return metaErr
		}
		// An implicit --resume-last that lands on a delegate resolves to the
		// newest resumable root, so a recently-touched delegate does not make it
		// useless; an explicit id is still refused below.
		if *lf.resume == "" && *lf.resumeLast && resumedMeta.IsSubagent {
			if root, ok := latestResumableRootMeta(sd); ok {
				resumedMeta = root
			}
		}
		// Refuse a bare delegate resume before any session is reserved (#4442);
		// refuseBareDelegateResume carries the rationale.
		if err := refuseBareDelegateResume(resumedMeta, sd); err != nil {
			return err
		}
	}
	resumeProvider := ""
	resumeModel := ""
	if resuming {
		resumeProvider = resumedMeta.ProfileID
		resumeModel = resumedMeta.Model
	}
	var modelRef cmdutil.ModelRef
	if resuming {
		modelRef, err = cmdutil.ResolveResumeModelRef(*lf.model, envvars.EVENERModel.Getenv(), resumeProvider, resumeModel)
	} else {
		modelRef, err = cmdutil.ResolveModelRef(*lf.model, envvars.EVENERModel.Getenv(), "", "")
	}
	if err != nil {
		return err
	}

	effort, err := cmdutil.ResolveReasoningEffort(*lf.reasoningEffort, envvars.EVENERReasoningEffort.Getenv())
	if err != nil {
		return err
	}

	// Create LLM client and session.
	// Resolved before the session and listener exist, so a missing state home
	// fails the launch with nothing to unwind.
	runDir := *runDirFlag
	if runDir == "" {
		runDir = envvars.EVENERRunDir.Getenv()
	}
	if runDir == "" {
		if runDir, err = rendezvous.DefaultDir(); err != nil {
			return fmt.Errorf("resolve runtime directory: %w", err)
		}
	}
	client, closeClient, err := deps.newClient(sd, logOut)
	if err != nil {
		return err
	}
	defer closeClient() //nolint:errcheck
	reserveSession, closeAPILog, err := deps.attachAPILogger(client, sd, logOut, settings.apiLogEnabled)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck
	if resuming {
		if err := reserveSession(resumedMeta.ID); err != nil {
			return err
		}
	}
	profileCtx := ctx
	if resuming {
		profileCtx = llm.WithAPILogContext(ctx, resumedMeta.ID)
	}
	profile, startupListing, err := deps.buildProfile(profileCtx, client, modelRef, *lf.outputSchema)
	if err != nil {
		return err
	}
	profile, err = deps.applyCheap(profile, *lf.fastCheapModel, client)
	if err != nil {
		return err
	}
	visionModelVal, err := applyVisionModel(profile, *lf.visionModel, client)
	if err != nil {
		return err
	}
	env := execenv.NewLocalExecutionEnvironment(wd)
	// A daemon/session launched outside the developer's shell rc chain (macOS
	// launchd, a GUI app, systemd) inherits a PATH lacking tool directories like
	// /opt/homebrew/bin; the login shell's own PATH wins for spawned commands
	// (kata 31gh). The probe has a short timeout and never blocks launch; a
	// probe failure is reported as the inherited-PATH fallback rather than
	// cached, so the next launch retries it (#4495).
	env.LoginPATH, env.LoginPATHProbeError = execenv.LoginShellPATHProbe()
	if err := startupInterrupted(ctx, stepProbingLoginShellPath); err != nil {
		return err
	}
	memoryProjectID := ""
	if !resuming {
		memoryProjectID = resolveMemoryProjectID(env, logOut)
	}
	memoryStateRoot := memoryStateRootForLaunch(*lf.disableMemory || (resuming && resumedMeta.Config.DisableMemory), logOut)
	commitProvenance, err := commitprovenance.Normalize(*lf.commitProvenance)
	if err != nil {
		return err
	}
	sessionCfg := agent.SessionConfig{
		MemoryStateRoot: memoryStateRoot,
		MemoryProjectID: memoryProjectID,
		DisableMemory:   *lf.disableMemory,
		// The session tree lives exactly as long as this daemon does. Shutdown
		// waits for the input loop before it closes the session, so work that
		// runs synchronously on that loop -- a Notification hook, which runs
		// for its own timeout -- is reached by nothing else: without this the
		// only cancellation it ever sees arrives after the wait it is holding
		// up. A turn is already cancelled directly (turnCtx derives from ctx).
		LifetimeContext:        ctx,
		MaxToolRoundsPerInput:  cmdutil.MaxRoundsToConfig(*lf.maxRounds),
		ShareTasksWithChildren: *lf.shareTaskStore,
		ResultToolName:         *lf.resultToolName,
		StateDir:               sd,
		// runDir is the directory THIS daemon registers its rendezvous entry
		// in, so it is also the one the session's owner-liveness probe reads
		// (issue #5471): a non-default --run-dir must reach the probe or
		// older daemons' verifiable live entries go unseen.
		RendezvousDir:               runDir,
		AcquireSessionOwnership:     reserveSession,
		Project:                     project,
		SystemPromptFile:            *lf.systemPrompt,
		SystemPromptAppend:          []string(lf.systemPromptAppend),
		AgentsDocPath:               *agentsDoc,
		AgentName:                   *lf.agentName,
		SkillsDirs:                  []string(lf.skillsDirs),
		MCPConfigFiles:              []string(lf.mcpConfigs),
		MCPInline:                   []string(lf.mcpServers),
		PluginDirs:                  resolvedPlugins.SelectedDirs,
		ContextStrategy:             *lf.contextStrategy,
		ExportATIFPath:              *lf.exportATIF,
		ExportATIFProviderHandles:   *lf.exportATIFProviderHandles,
		VisionModel:                 visionModelVal,
		NonInteractive:              *nonInteractive,
		Manager:                     *managerMode,
		SystemPromptAsUser:          *lf.systemPromptAsUser,
		ModelFallbacks:              []string(modelFallbacks),
		OpenAIResponsesContinuation: settings.openAIResponsesContinuation,
		ProviderIdleTimeout:         *lf.providerIdleTimeout,
		CommitProvenance:            commitProvenance,
		ResolveProfile:              cmdutil.BuildResolveProfile(client),
		StartupModelListing:         startupListing,
	}
	if *lf.maxSubagentDepth >= 0 {
		sessionCfg.MaxSubagentDepth = *lf.maxSubagentDepth
	}
	if *lf.maxConcurrentDelegates >= 0 {
		sessionCfg.MaxConcurrentDelegateTurns = *lf.maxConcurrentDelegates
	}
	if *lf.maxRetainedTerminal >= 0 {
		sessionCfg.MaxRetainedTerminal = *lf.maxRetainedTerminal
	}
	if effort.Set {
		sessionCfg.ReasoningEffort = effort.Value
	}
	if err := cmdutil.ConfigureSandbox(&sessionCfg, *lf.sandbox, *lf.sandboxNet); err != nil {
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
		// Provisioning allocates the session scratch and the flock lease that
		// keeps the crashed-scratch sweeper off it, and an unsandboxed session
		// mints one of its own on its first command; nothing releases either
		// until a session owns this environment and its Close does. Every way
		// out between here and that hand-off has to dispose of them, or an
		// interrupted startup leaks a directory and a lease per attempt.
		if err := startupInterrupted(ctx, stepProvisioningSandbox); err != nil {
			_ = env.DisposeSessionScratch()
			return err
		}
	}

	var sess *agent.Session
	if resuming {
		sess, err = deps.restoreSession(client, profile, env, resumedMeta, agent.RestoreSessionConfig{
			MemoryStateRoot:             sessionCfg.MemoryStateRoot,
			DisableMemory:               *lf.disableMemory,
			LifetimeContext:             ctx,
			StateDir:                    sd,
			RendezvousDir:               runDir,
			Project:                     project,
			ResolveProfile:              sessionCfg.ResolveProfile,
			StartupModelListing:         startupListing,
			AcquireSessionOwnership:     reserveSession,
			OwnershipAlreadyAcquired:    true,
			ModelFallbacks:              sessionCfg.ModelFallbacks,
			OpenAIResponsesContinuation: settings.openAIResponsesContinuation,
			ProviderIdleTimeout:         *lf.providerIdleTimeout,
			CommitProvenance:            *lf.commitProvenance,
			AgentsDocPath:               *agentsDoc,
			// See RestoreSessionConfig.ExportATIFPath.
			ExportATIFPath:            sessionCfg.ExportATIFPath,
			ExportATIFProviderHandles: sessionCfg.ExportATIFProviderHandles,
		})
		if err != nil {
			// The restore reopens the session's kept scratch and can fail
			// after that with no session built to own it. The scratch still
			// belongs to the session, so only its lease is released.
			_ = env.EndSessionScratch()
			return fmt.Errorf("restore session: %w", err)
		}
		if effort.Set {
			if err := sess.SetReasoningEffort(effort.Value); err != nil {
				sess.Close()
				return fmt.Errorf("set reasoning effort: %w", err)
			}
		}
		reportServeResume(logOut, resumedMeta, modelRef, resumeProvider, resumeModel, strings.TrimSpace(*lf.model) != "")
	} else {
		sess, err = deps.newSession(client, profile, env, sessionCfg)
		if err != nil {
			// The session that would have owned whatever this environment
			// provisioned was never built, so its scratch goes here.
			_ = env.DisposeSessionScratch()
			return fmt.Errorf("session creation: %w", err)
		}
	}

	// One startup line, loudly, states exactly what this host enforces (read from
	// the env's resolved policy so it never overstates). Empty for an unsandboxed
	// session — nothing to announce.
	printServeSandboxLine(logOut, sandboxEnforcementLine(env))

	if err := startupInterrupted(ctx, stepCreatingSession); err != nil {
		sess.Close()
		return err
	}
	// This start's boot generation: the session is owned by now (created
	// under its ownership lock, or reserved before the resume), and the
	// counter is durable before the daemon listens, so no read or
	// notification of this boot can go out under an earlier generation.
	startGeneration, err := schema.NextBootGeneration(sd, sess.ID(), 0)
	if err != nil {
		sess.Close()
		return fmt.Errorf("boot generation: %w", err)
	}
	sess.SetBootGeneration(appwire.BootGeneration(startGeneration))
	// servedBootGeneration is the generation of the served identity; a
	// thread/clear replacement serves above it (see the clear func).
	var servedBootGeneration atomic.Uint64
	servedBootGeneration.Store(startGeneration)
	listener, err := deps.listen(ctx, "tcp", *addr)
	if err != nil {
		sess.Close()
		return fmt.Errorf("listen %s: %w", *addr, err)
	}

	hubToken := envvars.EVENERHubToken.Getenv()
	srv := deps.newServer(server.ServerConfig{
		AppReplaySize: *appReplaySize,
		HubToken:      hubToken,
		AllowedHost:   listener.Addr().String(),
		StateDir:      sd,
	})
	// The daemon's thread histories stop with it, after every deferred
	// teardown below has drained the bridges that feed them.
	defer srv.Close()
	// Install the session's identity and its history BEFORE the event bridge
	// starts, so the first read answers from the transcript every later
	// history/updated extends. Preparation is the only fallible half; a
	// transcript that belongs to another session is one this daemon must not
	// serve.
	workspaceRef := appwire.Ref{SourceID: "local", ThreadID: sess.ID()}.String()
	prepared, err := deps.prepareAppIdentity("local", sess.ID(), workspaceRef, sess.TranscriptPath())
	if err != nil {
		sess.Close()
		listener.Close() //nolint:errcheck // returning the preparation failure; the close error is not actionable
		return fmt.Errorf("prepare app identity: %w", err)
	}
	// The hooks first, then the recorded length: an entry recorded between the
	// two is covered by the length or reaches the history through its hook.
	srv.WireTranscriptHistory(sess)
	srv.ReplaceAppIdentity(prepared.WithRecordedLength(sess.TranscriptRecordedLength()).WithBootGeneration(appwire.BootGeneration(startGeneration)), nil)
	rvRegistration := &rvreg.Registration{}

	live := newLiveSession(sess, env)

	retiring, err := setupRetirement(ctx, deps, live, srv, sess, listener, rvRegistration, *daemonIdleTimeout, cancel)
	if err != nil {
		return err
	}

	// The observer runs ON the bridge goroutine, which is the daemon's
	// authoritative consumer, so it must never block: see verboseEventTee.
	var eventObserver func(events.SessionEvent)
	var closeEventObserver func()
	if *lf.verbose {
		verboseOut := deps.verboseOut
		if verboseOut == nil {
			verboseOut = logOut
		}
		tee := newVerboseEventTee(verboseOut, verboseEventTeeBuffer)
		eventObserver = tee.observe
		closeEventObserver = tee.close
	}

	bridge := newServeBridge(srv, deps, eventObserver, closeEventObserver)
	defer bridge.awaitDrains()

	envelopeSource := bindServerCallbacks(ctx, srv, deps, live, bridge, retiring, rvRegistration, sessionCfg, client, profile, wd, sd, logOut, workspaceRef, &servedBootGeneration, cancel)

	// Seed the envelope from the live session before its bridge starts. Every
	// envelope value changes at once when the session behind them changes, and
	// no single event announces that; this is that moment for the first one.
	srv.RefreshThreadEnvelope()
	// Bridge session events to appwire notifications.
	bridge.bridgeSession(sess)
	// Belt-and-suspenders with the Bridge/projector SessionStart fix (spec
	// §5.4 "two touchpoints"): the session's SessionStart event may already be
	// sitting in its buffered event channel by the time the bridge goroutine
	// above is scheduled to drain it, so thread/read could observe
	// the server's zero-value status -- empty model/profile, default state --
	// until then. That is not a test-only race: it is this daemon's own
	// startup path, and any real client (TUI, hub-spawned session) that dials
	// the rendezvous entry and calls thread/read before the drain completes
	// can observe it (#251).
	//
	// Writing it here, from data the daemon already has locally, closes the
	// window synchronously for every session this daemon starts with --
	// fresh and resumed alike, since both publish through the same buffered
	// EventSessionStart. This mirrors SetModelFunc's closure, which already
	// makes this exact synchronous call after a runtime model switch.
	p := sess.Profile()
	srv.UpdateSessionInfo(sess.ID(), p.Model(), p.ID())
	srv.SetState(sess.WireState())
	if deps.observeCallbacks != nil {
		deps.observeCallbacks(serveCallbackObserver{
			notify:          bridge.notifyCallback,
			subscriberCount: bridge.subscriberCallback, pendingEscalations: envelopeSource.PendingEscalations,
			setSession: func(next *agent.Session) { live.set(next, live.environment()) },
			session:    sess,
		})
	}

	// Input processing loop. Each turn runs under a per-turn cancellable
	// context that is wired into the server's interrupt handler so POST
	// /interrupt actually cancels the in-flight turn. Clearing it after the
	// turn finishes answers "is a cancel armed right now" and nothing more:
	// capabilities.interrupt reports the harness's support (the retry-safe
	// turn/interrupt handler installed at startup, #1375), so a cleared cancel
	// does not withdraw Stop.
	inputLoopDone := make(chan struct{})
	go func() {
		defer close(inputLoopDone)
		processMessage := func(sess *agent.Session, msg server.InputMessage) bool {
			// User, queued, start and continuation messages that name a
			// session belong only to it. A continuation queued while clear
			// builds a replacement must not run its predecessor's objective
			// in the replacement after the gate releases.
			if !inputBelongsToSession(msg, sess.ID()) {
				return false
			}
			runnerDone := make(chan struct{})
			var currentCancel context.CancelFunc
			var nextTurnCtx func(context.Context) (context.Context, context.CancelFunc)
			nextTurnCtx = func(root context.Context) (context.Context, context.CancelFunc) {
				drainCtx, cancelDrain := context.WithCancel(root)
				drainCtx = agent.WithQueuedInputDrainOnInterruptHandler(drainCtx, root, nextTurnCtx)
				currentCancel = cancelDrain
				// Arm the cancel BEFORE publishing processing, matching the two
				// other arming sites below. Published first, this turn is
				// announced as running while nothing can stop it, and a
				// capability set sampled in between says exactly that (5gdv).
				srv.SetCancelFunc(cancelDrain)
				bridge.setMutationRunner(cancelDrain, runnerDone)
				srv.SetProcessing(true)
				return drainCtx, cancelDrain
			}
			turnCtx, cancelTurn := context.WithCancel(ctx)
			currentCancel = cancelTurn
			turnCtx = agent.WithQueuedInputDrainOnInterruptHandler(turnCtx, ctx, nextTurnCtx)
			if !msg.ClientMutationStart && !msg.QueuedInput {
				// The runner first, then the server cancel: the interrupt path
				// does not read srv.SetCancelFunc, it calls
				// cancelAndWaitMutationRunner, which reads these runner fields
				// and waits on runnerDone before finalizing the fence. Armed the
				// other way round, a Stop accepted between the two statements
				// finds the fields still nil, returns without waiting, finalizes
				// the fence and clears it -- and the turn then runs anyway.
				armInterruptRunner(
					func() { bridge.setMutationRunner(cancelTurn, runnerDone) },
					func() { srv.SetCancelFunc(cancelTurn) },
				)
				if !holdServeStateForAwaitingWake(msg.Kind, sess.HasPendingAsk()) {
					srv.SetProcessing(true)
				}
			}
			var result string
			var processErr error
			processed := true
			if msg.ClientMutationStart {
				result, processed, processErr = sess.ProcessClientMutationStart(turnCtx, func(turnID string, phase agent.ClientMutationStartPhase) {
					// Arm on the way in and publish on the way out: a
					// turn/interrupt that finalizes the claimed start between
					// the two must find the runner already armed, while a claim
					// that refuses must never announce a running turn.
					//
					// The runner is armed first for the reason the interrupt path
					// waits on it: cancelAndWaitMutationRunner reads these fields
					// and then waits for runnerDone before finalizing the fence,
					// and it never consults srv.SetCancelFunc.
					armInterruptRunner(
						func() { bridge.setMutationRunner(cancelTurn, runnerDone) },
						func() { srv.SetCancelFunc(cancelTurn) },
					)
					// The execution the claim admits publishes its own TurnID
					// through SetProcessingTurn (WireTranscriptHistory) before
					// its first entry is recorded.
					if phase == agent.ClientMutationStartClaimed {
						srv.SetProcessing(true)
					}
				})
				if !processed {
					// The claim refused: nothing is running, so drop the runner
					// armed for it rather than leave a Stop pointed at a turn
					// that will never start.
					bridge.clearMutationRunner(runnerDone)
					srv.SetCancelFunc(nil)
				}
			} else if msg.QueuedInput {
				// Arm before the claim, the way the start path does: a queued
				// claim sets the durable active turn inside its commit, and a
				// turn/interrupt that lands between that commit and this publish
				// must find a runner to cancel. Armed after the claim, the Stop
				// finalizes the unincorporated claim with nothing to cancel and
				// the input is lost.
				armInterruptRunner(
					func() { bridge.setMutationRunner(cancelTurn, runnerDone) },
					func() { srv.SetCancelFunc(cancelTurn) },
				)
				result, processed, processErr = sess.ProcessPendingUserInput(turnCtx, func(string) {
					srv.SetProcessing(true)
				})
				if !processed {
					// The claim refused: nothing is running, so drop the runner
					// armed for it rather than leave a Stop pointed at a turn
					// that will never start.
					bridge.clearMutationRunner(runnerDone)
					srv.SetCancelFunc(nil)
				}
			} else {
				result, processErr = sess.ProcessInputKind(turnCtx, msg.Text, msg.Images, msg.Kind)
			}
			// The stored state changes only through the session's own events
			// (SESSION_START, SESSION_END, STATUS_SETTLED): ending processing
			// publishes what they stated.
			srv.SetProcessing(false)
			srv.SetCancelFunc(nil)
			currentCancel()
			close(runnerDone)
			bridge.clearMutationRunner(runnerDone)
			if processErr != nil {
				serveLogf(logOut, sess.ID(), "error: %s", turnFailureSummary(processErr))
			}
			_ = result
			return processed
		}
		for {
			sess := live.session()
			if !processNextServeInput(ctx, srv.InputCh(), sess.ID(), func(msg server.InputMessage) bool {
				return processWithMutationRead(srv, func() bool {
					return processMessage(live.session(), msg)
				})
			}) {
				return
			}
		}
	}()

	// Start HTTP server.
	serveLogf(logOut, live.session().ID(), "listening on %s", listener.Addr())

	spawnedBy := "user"
	if envvars.EVENERHubSpawned.Getenv() == "1" {
		spawnedBy = "hub"
	}
	rvEntry := rendezvous.Entry{
		PID:          os.Getpid(),
		Address:      listener.Addr().String(),
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws://" + listener.Addr().String() + "/rpc",
		SourceID:     "local",
		ThreadID:     live.session().ID(),
		SessionID:    live.session().ID(),
		WorkspaceRef: workspaceRef,
		InstanceID:   live.session().ID(),
		WorkingDir:   wd,
		StateDir:     sd,
		Agent:        *lf.agentName,
		Model:        modelRef.Model,
		Provider:     modelRef.Provider,
		HubToken:     hubToken,
		StartedAt:    time.Now().UTC(),
		SpawnedBy:    spawnedBy,
	}
	// retirementLoopDone is closed when the idle-retirement goroutine (started
	// only after a successful rendezvous registration) returns. Nil means no
	// loop was started, so the shutdown path has nothing to join.
	var retirementLoopDone chan struct{}
	if err := deps.register(rvRegistration, runDir, rvEntry); err != nil {
		serveLogf(logOut, live.session().ID(), "rendezvous write failed: %v", err)
	} else {
		defer func() {
			removeRendezvousAtShutdown(rvRegistration.Remove, deps.rendezvousRetryPause, func(err error) {
				serveLogf(logOut, live.session().ID(), "rendezvous removal failed after %d attempts, entry may be stale: %v", rendezvousRemovalAttempts, err)
			})
		}()
		// The idle clock may only start once the daemon has published its
		// initial rendezvous entry. Starting it earlier let a short
		// --daemon-idle-timeout make a fresh daemon eligible, claim and cancel
		// its context before that entry existed, stranding the initial session
		// as stale/unavailable. A registration failure starts no loop, so a
		// daemon that could not publish itself never silently enables automatic
		// retirement. Manual retirement is unaffected: requestRetirement
		// requires the very entry this guards, so it cannot run before it either.
		retirementLoopDone = make(chan struct{})
		go func() {
			defer close(retirementLoopDone)
			_ = retiring.retirement.Run(ctx, retiring.consumeClaim, func(attempt agent.RetirementAttempt) {
				serveLogf(logOut, live.session().ID(), "%s", retirementAttemptSummary(attempt))
			})
		}()
	}

	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: daemonHTTPReadHeaderTimeout,
		IdleTimeout:       daemonHTTPIdleTimeout,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		_ = httpSrv.Close()
		<-inputLoopDone
		// The idle-retirement loop is not part of the HTTP server or the input
		// loop, so nothing above waits for it. Join it here: a serve that
		// returned with Run still live would leave the loop racing whatever the
		// caller does next, such as a test's TempDir removal or leak check.
		if retirementLoopDone != nil {
			<-retirementLoopDone
		}
		live.closeForShutdown()
	}()

	if err := deps.serveHTTP(httpSrv, listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		cancel()
		<-shutdownDone
		return err
	}
	<-shutdownDone
	return nil
}

// removeRendezvousAtShutdown spends rendezvousRemovalAttempts on remove,
// pausing between attempts, and hands a removal that never succeeded to
// report. Registration.Remove stays retryable after a failure by design; this
// is what spends that.
func removeRendezvousAtShutdown(remove func() error, pause func() <-chan time.Time, report func(error)) {
	var err error
	for attempt := range rendezvousRemovalAttempts {
		if err = remove(); err == nil {
			return
		}
		if attempt < rendezvousRemovalAttempts-1 {
			<-pause()
		}
	}
	report(err)
}

// closeSupersededSession closes a session a concurrent clear has replaced, in
// the form its owner is entitled to. shutdownClaimed means shutdown's single
// pass already reached this session -- and already closed it with
// CloseForShutdown, because closeForShutdown holds the live session's transitionMu across
// that close and the claim is read under the same lock -- so the call below is
// a no-op over a terminal boundary shutdown has already published. Without the
// claim this is the session's only close, and the ordinary form is the right
// one: the clear announces the replaced identity with a resync.
//
// Double close buys nothing either way. Both forms run through the session's
// close-once, so the terminal boundary belongs to whichever close runs FIRST,
// and a second call cannot publish one the first declined. The agent package's
// TestSession_DoubleCloseCannotRecoverATerminalBoundary pins that.
func closeSupersededSession(sess *agent.Session, shutdownClaimed bool) {
	if shutdownClaimed {
		sess.CloseForShutdown()
		return
	}
	// This close IS a clear: a concurrent thread/clear replaced this identity,
	// which is the one reason only this path can prove. A shutdown-claimed
	// session was already closed by CloseForShutdown, whose own reason
	// (prompt_input_exit when idle, else other) belongs to that first close.
	sess.CloseWithReason("clear")
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

// processWithMutationRead admits one process-local input turn only while no
// thread/clear owns the mutation gate. The gate is held for clear's whole
// preparation and publication; after it releases, the live session sampled
// inside the callback is the replacement, not the closed predecessor.
func processWithMutationRead(srv serveServer, fn func() bool) bool {
	if gate, ok := srv.(interface{ WithMutationRead(func()) }); ok {
		var processed bool
		gate.WithMutationRead(func() { processed = fn() })
		return processed
	}
	return fn()
}

// inputBelongsToSession reports whether msg may run against sessionID. An
// untagged notification wake belongs to whichever session is live; every
// input that names a session belongs only to that session, so a continuation
// queued by a predecessor cannot execute its objective in the replacement.
func inputBelongsToSession(msg server.InputMessage, sessionID string) bool {
	return msg.SessionID == "" || msg.SessionID == sessionID
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
		serveLogf(w, meta.ID, "resumed with model override %s (was %s/%s)", model.Qualified(), oldProvider, oldModel)
	} else {
		serveLogf(w, meta.ID, "resumed (%d turns)", meta.TurnCount)
	}
}

func printServeSandboxLine(w io.Writer, line string) {
	if line != "" {
		_, _ = fmt.Fprintln(w, line)
	}
}

// holdServeStateForAwaitingWake reports whether the input loop should skip
// marking processing for this message: the session-level entry gate
// (agent/session_lifecycle.go's processInputKindWithProvenance, spec §5.3)
// refuses autonomous wakes while a question is pending, before any state
// transition — so the AppWire status must not flip to active around a wake
// the session will refuse (the flicker's active→awaiting edge would re-fire
// the OS notification, notifications.js Task 11). Mirrors the gate's
// predicate exactly — hasPendingAsk, not raw state (SessionAwaiting alone
// does not imply a pending question: a needs_response rest with no ask
// pending must NOT be held; async wakes may move it on). EntryUserInput is
// always let through since it is how the reply resolves a pending ask (spec
// §5.2).
func holdServeStateForAwaitingWake(kind agent.EntryKind, hasPendingAsk bool) bool {
	return kind != agent.EntryUserInput && hasPendingAsk
}

func printServeEnvVars(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, v := range []envvars.Var{
		envvars.EVENERModel,
		envvars.EVENEROpenAIResponsesContinuation,
		envvars.EVENERReasoningEffort,
		envvars.EVENERStateDir,
		envvars.EVENERRunDir,
		envvars.EVENERHubToken,
		envvars.EVENERHubSpawned,
		envvars.EVENERAllowedDecisions,
		envvars.EVENERProvidersConfig,
		envvars.EVENERPprofAddr,
	} {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", v.Name, v.Summary)
	}
	_ = tw.Flush()
}

// buildInitialProfile constructs the session's initial *Profile by resolving
// the reference on the client's registry, so an instance name defined in
// providers.toml ("work") and a curated id resolve the same way and carry the
// same facts. outputSchemaJSON and EVENER_ALLOWED_DECISIONS are applied in
// this app layer so callers see the same communicate-tool schema regardless of
// model.
func buildInitialProfile(ctx context.Context, client *llm.Client, modelRef cmdutil.ModelRef, outputSchemaJSON string) (*provider.Profile, *agent.StartupModelListing, error) {
	raw, startupListing, err := resolveInitialProfile(ctx, client, modelRef)
	if err != nil {
		return nil, nil, err
	}
	var outputSchema map[string]any
	if trimmed := strings.TrimSpace(outputSchemaJSON); trimmed != "" {
		if jsonErr := json.Unmarshal([]byte(trimmed), &outputSchema); jsonErr != nil {
			return nil, nil, fmt.Errorf("invalid --output-schema: %w", jsonErr)
		}
	}
	p := provider.WithCommunicateOutputSchema(raw, outputSchema)
	allowedDecisions := cmdutil.ParseAllowedDecisions(envvars.EVENERAllowedDecisions.Getenv())
	return provider.WithAllowedDecisions(p, allowedDecisions), startupListing, nil
}

// resolveInitialProfile resolves modelRef on the client's registry. The Codex
// transport rejects an id absent from its roster instead of synthesizing a
// row, and the bundled roster lags the models the backend serves. A session
// restarted after selecting such a model (or a fresh launch with a model the
// picker just listed) resolves before any listing exists in this process, so
// the catalog-only gate would reject a model the provider itself advertises.
// On exactly that rejection, consult the instance's live listing once and
// resolve again; every other failure is returned unchanged.
func resolveInitialProfile(ctx context.Context, client *llm.Client, modelRef cmdutil.ModelRef) (*provider.Profile, *agent.StartupModelListing, error) {
	raw, err := cmdutil.ResolveProfile(client, modelRef.Qualified())
	if err == nil || !errors.Is(err, registry.ErrCodexModelNotListed) {
		return raw, nil, err
	}
	listCtx, cancel := context.WithTimeout(ctx, llm.LiveModelListTimeout)
	defer cancel()
	listing, listErr := client.Models(listCtx, modelRef.Provider)
	if listErr != nil {
		return nil, nil, fmt.Errorf("%w; the provider's live model list could not be read: %w", err, listErr)
	}
	profile, err := cmdutil.ResolveProfile(client, modelRef.Qualified())
	if err != nil {
		return nil, nil, err
	}
	if !listing.Usable {
		return profile, nil, nil
	}
	return profile, &agent.StartupModelListing{Instance: modelRef.Provider, Listing: listing}, nil
}

// applyVisionModel validates the --vision-model ref against the registered
// providers and returns the canonical SessionConfig.VisionModel value. "off"
// (canonicalized to lowercase) disables the side-channel, a bare model keeps
// the active provider, and "provider/model" pins a provider that must be
// registered (configured AND credentialed) — the same rule --fast-cheap-model
// enforces. The active profile is never touched.
func applyVisionModel(profile *provider.Profile, raw string, client *llm.Client) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.EqualFold(raw, "off") {
		return "off", nil
	}
	if prov, model, ok := strings.Cut(raw, "/"); ok {
		if prov == "" || model == "" {
			return "", fmt.Errorf("--vision-model %q is malformed: want \"model\" or \"provider/model\"", raw)
		}
		if prov != profile.ID() && !client.HasProvider(prov) {
			return "", fmt.Errorf("--vision-model provider %q is not configured or has no credential (active provider %q); available providers: %s",
				prov, profile.ID(), strings.Join(client.ProviderNames(), ", "))
		}
	}
	return raw, nil
}
