// Command evener-hub is the web orchestrator for evener serve daemons.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/binresolve"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/rendezvous"

	// Side-effect imports register provider adapters. These are the same
	// adapters `evener serve` uses, so the hub's model/list reflects what
	// spawning will succeed at — only providers configured in the hub's
	// environment surface in the picker.
	_ "primeradiant.com/evener/llm/providers/all"
)

const Version = "0.1.0"

// Hub HTTP listener deadlines: ReadHeaderTimeout bounds the pre-auth window
// before a peer finishes its request headers (before AuthGuard middleware
// runs); IdleTimeout bounds an idle keep-alive socket. Neither governs a
// hijacked AppWire connection, so long-lived streams are unaffected.
const (
	hubHTTPReadHeaderTimeout = 10 * time.Second
	hubHTTPIdleTimeout       = 120 * time.Second
)

var (
	hubExecutable  = os.Executable
	hubProcessArgs = func() []string { return os.Args }
	hubHostname    = os.Hostname
	hubRunMain     = runMain
	// hubBuildDirty reports whether this hub was built from a dirty tree, by the
	// same rule sshconn refuses deploy sources under (a "<sha>-dirty" controller
	// version, isDirtyVersion). It is a seam so a test can put the hub in either
	// state without stamping the binary it runs in; deploy_flags.go reads it to
	// pick the remedy that matches what sshconn will actually do with the source.
	hubBuildDirty = func() bool { return strings.HasSuffix(strings.TrimSpace(buildinfo.Version()), "-dirty") }
	// hubProcessStart is this hub process's own start instant, captured when
	// the package initializes. It is what evener/host/running reports as its
	// processStartTime (deploy pipeline 08b §10: "present exactly when the
	// serving hub knows its own process start time"), and a restart's new
	// instant is what makes the post-restart probe distinguish the replacement
	// process.
	hubProcessStart = time.Now()
	// hubBootID identifies this controller process incarnation for the durable
	// probe epochs evener/host/plan persists (deploy pipeline 08b §6 step 2: "a
	// worker's durable (controller boot id, per-host monotonic op sequence)"). It is drawn once per process, so a restart
	// always presents a fresh boot id and never continues an old boot's
	// sequence.
	hubBootID = newHubBootID()
)

// newHubBootID draws this process's boot id: random hex, with a pid+nanotime
// fallback for the (practically unreachable) entropy failure, so a boot id
// always exists.
func newHubBootID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

type hubHTTPServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

// listenerHTTPServer adapts an *http.Server plus an already-bound
// net.Listener to the hubHTTPServer interface. serveHub (and its tests) only
// know about ListenAndServe/Shutdown; this keeps that surface unchanged while
// letting runMain claim the listener up front (see the "-addr 127.0.0.1:0"
// comment in runMain) instead of handing http.Server a bare address string
// and letting it bind lazily inside ListenAndServe, by which point the real
// port can no longer be reported anywhere upstream.
type listenerHTTPServer struct {
	*http.Server
	ln net.Listener
}

func (s *listenerHTTPServer) ListenAndServe() error {
	return s.Serve(s.ln)
}

type navigationPublisher interface {
	BroadcastAll(string, any)
}

func runNavigationPublisher(ctx context.Context, navigation *NavigationService, publisher navigationPublisher) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-navigation.PublicationReady():
			for {
				payloads := navigation.DrainPublications()
				if len(payloads) == 0 {
					break
				}
				for _, payload := range payloads {
					publisher.BroadcastAll(appwire.NotifyEvenerNavigationInvalidated, payload)
				}
			}
		}
	}
}

type hubOptions struct {
	configPath string
	// configExplicit records whether the operator named the path with --config.
	// A named path must load; the implicit default path (DefaultConfigPath) may
	// be absent and yield DefaultConfig().
	configExplicit bool
	addr           string
	evenerBinary   string
	appwireTrace   string
	// deployBinary and buildSource describe how a missed host gets the
	// controller's build pushed to it. With neither set, deployBinary defaults to
	// this hub's own executable (deployDefault records that), so a host that
	// needs the controller's build is provisioned by default; -no-deploy disables
	// the default and both flags, leaving a local-only controller with no deploy
	// path at all.
	deployBinary string
	buildSource  string
	// noDeploy is the explicit opt-out: the hub wires no deploy source (not even
	// the own-executable default) and a host that needs one is refused with the
	// remedy named. deployWiring applies it before the flags, so -no-deploy wins
	// over both of them.
	noDeploy bool
	// deployDefault records that deployBinary was not named by the operator: it
	// is this hub's own executable, adopted by validateDeployFlags. Only the
	// startup log and the deploy wiring read it: the log says the source was
	// defaulted rather than given, and the wiring marks the source as the own
	// executable (the one source a dirty controller may install).
	deployDefault bool
	// defaultFailure records why the own-executable default was not adopted, so
	// the unwired state's refusal names the actual cause: an executable that read
	// fine but is not evener is a different problem from one that could not be
	// located or read at all. See deploy_flags.go's deployDefaultFailure.
	defaultFailure deployDefaultFailure
}

type mainDeps struct {
	// loadConfig loads the path the flag layer resolved; the bool is true when
	// the operator named it with --config, which makes a missing file a startup
	// refusal instead of a silent DefaultConfig() fallback.
	loadConfig      func(string, bool) (Config, error)
	ensureDirs      func() error
	acquireLock     func(string) (func(), error)
	newToken        func() (string, error)
	loadAuthToken   func(string) (string, error)
	loadCredentials func(string) (*credentials.Store, error)
	loadRegistry    hubcore.RegistryLoader
	// newSSHManager builds the SSH connection manager. It is a seam so a test can
	// capture the sshconn.Options the hub hands it — in particular DeployHelp,
	// which is what makes the terminal version refusal name the hub's flags
	// instead of sshconn's internal field name. nil uses sshconn.New.
	newSSHManager func(*hostreg.Registry, sshconn.Options) *sshconn.Manager
	// startLivePrefetch warms the holder's live model cache: main wires it to
	// the background runner and the broadcast, tests to a synchronous seam.
	startLivePrefetch func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, func(func()), func())
	// startLaunchPrefetch warms the picker's cached launch model list so the
	// first open after startup is instant: main wires it to the background
	// runner, tests to a synchronous seam.
	startLaunchPrefetch func(context.Context, *WebServer, func(func()))
	notifyContext       func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	listen              func(context.Context, string, string) (net.Listener, error)
	serve               func(context.Context, hubHTTPServer) error
	afterWeb            func(*WebServer)
	// rosterProbeTimeout bounds the roster's status probe of each live
	// daemon; zero keeps hubcore.StatusProber's 500ms default. A test that
	// reads a real daemon through the roster sets a generous one, so a loaded
	// runner cannot time the probe out.
	rosterProbeTimeout time.Duration
	// stdin/stdout carry the process streams the `attach` subcommand bridges to
	// the hub's loopback AppWire edge. The normal hub command ignores them.
	stdin  io.Reader
	stdout io.Writer
}

func defaultMainDeps() mainDeps {
	return mainDeps{
		loadConfig:          loadConfigForCommandLine,
		ensureDirs:          cmdutil.EnsureUserConfigDirs,
		acquireLock:         hostlock.AcquireLock,
		newToken:            newHubToken,
		loadAuthToken:       hubedge.LoadOrCreateAuthToken,
		loadCredentials:     credentials.LoadStore,
		loadRegistry:        cmdutil.LoadRegistry,
		startLivePrefetch:   startLiveModelsPrefetch,
		startLaunchPrefetch: startLaunchModelsPrefetch,
		notifyContext:       signal.NotifyContext,
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve:  serveHub,
		stdin:  os.Stdin,
		stdout: os.Stdout,
	}
}

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	deps := defaultMainDeps()
	if stdin != nil {
		deps.stdin = stdin
	}
	if stdout != nil {
		deps.stdout = stdout
	}
	if err := hubRunMain(args, stderr, deps); err != nil && !errors.Is(err, flag.ErrHelp) {
		return 1
	}
	return 0
}

func runMain(args []string, stderr io.Writer, deps mainDeps) error {
	// Handle --version flag before full parsing
	for _, arg := range args {
		if arg == "--version" || arg == "-version" {
			return printVersionInfo(stderr)
		}
	}

	// `attach` is a client-mode subcommand; it shares the deps seam but none of
	// the hub startup path (no config dirs, no hostlock, no listener), so it is
	// dispatched before the normal hub flag parsing.
	if len(args) > 0 && args[0] == "attach" {
		return runAttach(args[1:], stderr, deps)
	}

	opts, err := parseHubOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}

	cfg, err := deps.loadConfig(opts.configPath, opts.configExplicit)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] config: %v\n", err)
		return err
	}
	// Stamp the build User-Agent once: previously every registry-client
	// construction rewrote this process global per request, racing
	// in-flight Codex requests. Fetch paths now bind scoped
	// authenticators per request and never write it.
	tokenauth.ClientVersion = buildinfo.Version()
	if opts.addr != "" {
		cfg.Addr = opts.addr
	}
	if err := deps.ensureDirs(); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}

	// flock to ensure single hub per host. hub.lock lives beside the other
	// hub-level state (auth-token, index.db, deletions/) under HubStateRoot,
	// not a raw home-dir join, so a configured hub_state_root override moves
	// the lock too.
	lockPath := filepath.Join(cfg.HubStateRoot, "hub.lock")
	release, err := deps.acquireLock(lockPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	defer release()

	_, stopPprof, err := cmdutil.StartLivePprof(func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "[hub] "+format+"\n", args...)
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	defer stopPprof()

	var appwireTrace *appserver.WebSocketTrace
	if opts.appwireTrace != "" {
		tracePath, absErr := filepath.Abs(opts.appwireTrace)
		if absErr != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] appwire trace path: %v\n", absErr)
			return fmt.Errorf("resolve appwire trace path: %w", absErr)
		}
		appwireTrace, err = appserver.NewWebSocketTrace(tracePath)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] appwire trace: %v\n", err)
			return fmt.Errorf("create appwire trace: %w", err)
		}
		defer func() {
			if closeErr := appwireTrace.Close(); closeErr != nil {
				_, _ = fmt.Fprintf(stderr, "[hub] close appwire trace: %v\n", closeErr)
			}
		}()
		_, _ = fmt.Fprintf(stderr, "[hub] recording raw browser AppWire frames at %s; this file contains sensitive data\n", tracePath)
	}

	// Resolve runtime paths.
	runDir := cfg.RunDir
	if runDir == "" {
		runDir = rendezvous.DefaultDir()
	}
	// Initial ownership discovery must be ready before any request can resume
	// a saved session; the background watcher is not a startup barrier.
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("prepare runtime directory: %w", err)
	}
	stateGlob := cfg.StateGlob
	if stateGlob == "" {
		stateGlob = DefaultStateGlob()
	}
	pastIndexDB := cfg.PastIndexDB
	if pastIndexDB == "" {
		pastIndexDB = DefaultPastIndexDBPath()
	}

	// Roster + past index
	prober := &hubcore.StatusProber{Timeout: deps.rosterProbeTimeout}
	roster := hubcore.NewRoster(runDir, prober)

	past := hubcore.NewPastIndexWithDB(stateGlob, pastIndexDB)
	if _, err := past.Rebuild(); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] past index rebuild: %v\n", err)
	}
	archive := hubcore.NewArchiveStore(pastIndexDB)
	favorite := hubcore.NewFavoriteStore(pastIndexDB)
	pinSections := hubcore.NewPinSectionStore(pastIndexDB)
	sessionSeen := hubcore.NewSessionSeenStore(pastIndexDB)

	// Spawner
	hubToken, err := deps.newToken()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	// hubStateRoot holds hub-level machine state: the auth token, the
	// deletion-fence store, hub.lock (above), and index.db (below). Provider
	// config (providers.toml/credentials.toml) is user-editable and lives
	// under the config root instead — see cmdutil.DefaultConfigRoot.
	hubStateRoot := cfg.HubStateRoot
	authToken, err := deps.loadAuthToken(hubStateRoot)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] auth token: %v\n", err)
		return err
	}
	// The message search index (S14) lives in its own file beside index.db.
	// A hub that cannot open it still serves; search then finds sessions by
	// title and prompt only.
	messageSearch, err := hubcore.OpenMessageSearch(filepath.Join(hubStateRoot, "search.db"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] message search: %v\n", err)
	} else {
		defer func() { _ = messageSearch.Close() }()
	}
	providersConfigPath, noUserLayer := cmdutil.ProvidersConfigPath()
	credentialsPath := cmdutil.CredentialsPath()
	credsStore, err := deps.loadCredentials(credentialsPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] credentials store: %v\n", err)
		return err
	}
	// A providers.toml the registry cannot read is a diagnostic, not a
	// startup failure: the hub keeps an implicit-only registry, every child
	// it spawns resolves the same set, and instance writes stay refused
	// until the user fixes the file by hand (spec §10, §14.1).
	hubReg := hubcore.NewProviderRegistry(deps.loadRegistry)
	if err := hubReg.Reload(); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] providers config: %v — starting with implicit instances only\n", err)
	}
	resolvedEvenerBinary := resolveEvenerBinaryPath(opts.evenerBinary, currentExecutable(), exec.LookPath)
	if opts.evenerBinary == "" && resolvedEvenerBinary != "" && resolvedEvenerBinary != "evener" {
		_, _ = fmt.Fprintf(stderr, "[hub] resolved evener at %s\n", resolvedEvenerBinary)
	}
	spawner := &HubSpawner{
		Cfg:                 cfg,
		EvenerBinary:        resolvedEvenerBinary,
		RunDir:              runDir,
		HubToken:            hubToken,
		Registry:            hubReg,
		ProvidersConfigPath: providersConfigPath,
		CredentialsPath:     credentialsPath,
		NoUserLayer:         noUserLayer,
	}
	// stateDir is the parent of the projects/ directory; used for ForkSession
	// as a fallback when a session's project dir can't be found in the past index.
	stateDir := filepath.Dir(filepath.Clean(strings.TrimSuffix(stateGlob, "*")))

	// inputs is the shared source-revision counter used by NavigationService and
	// the remaining memoized tree projection; bumping it makes the next read
	// observe changed navigation inputs instead of stale state.
	inputs := &hubcore.InputsVersion{}

	// Wire archive/favorite's content-delta-gated onChange hook (Task 10) to
	// the shared inputs-version counter, so a decision busts the tree memo.
	// Past/roster get the same bump below, composed with the
	// evener/tree/changed broadcast once web (and its appRPC) exists.
	bump := inputs.Bump
	archive.SetOnChange(bump)
	favorite.SetOnChange(bump)

	// A session's Status transitioning (detected per-id by roster.Refresh)
	// means its daemon likely just rewrote its own meta.json out-of-process
	// (agent/session.go's periodic autosave); re-read just that session
	// instead of waiting for the past index's next 60s Rebuild tick, so the
	// sidebar order (which is keyed off UpdatedAt) doesn't lag behind a
	// completed turn.
	roster.SetOnStatusChange(refreshPastOnStatus(past))

	// attentionPoke lets a web handler (e.g. an archive decision) nudge the
	// attention watcher below to recompute immediately instead of waiting for
	// its next tick. Buffered 1 + non-blocking send: a poke that arrives while
	// one is already pending coalesces into the same recompute. remotePoke is
	// a second, independently-buffered channel fed by the same pokeAttention
	// call so the remote-thread-cache refresher (below) reacts to the same
	// event without stealing pokes from the attention watcher — each has its
	// own channel and drains only its own.
	attentionPoke := make(chan struct{}, 1)
	remotePoke := make(chan struct{}, 1)
	pokeAttention := func() {
		inputs.Bump()
		select {
		case attentionPoke <- struct{}{}:
		default:
		}
		select {
		case remotePoke <- struct{}{}:
		default:
		}
	}

	// remoteCache holds the last-refreshed remote-source thread list; the
	// refresher goroutine below Stores into it on a ~30s ticker + poke, and
	// the tree read path (remoteTreeThreads) reads it via WebConfig instead of
	// performing a synchronous network walk per request.
	remoteCache := &hubcore.RemoteThreadCache{}

	// Bind the listener before anything downstream (WebConfig.HubAddr, the
	// startup log line, the advertised auth URL) reads cfg.Addr, and
	// overwrite cfg.Addr with what actually got bound. This is what makes
	// "-addr 127.0.0.1:0" a real ephemeral-port request instead of a literal
	// ":0" that never resolves to anything callable: the OS hands back a
	// free port that cannot collide with another hub, sidestepping the
	// TOCTOU race in "probe a free port, then hope nothing else grabs it
	// before we bind" (see docs/developing-evener/agentic-testing.md).
	hubListener, err := deps.listen(context.Background(), "tcp", cfg.Addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] listen %s: %v\n", cfg.Addr, err)
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}
	cfg.Addr = hubListener.Addr().String()

	resumeLocks, err := hubcore.NewPersistentResumeLocks(hubStateRoot)
	if err != nil {
		_ = hubListener.Close()
		return fmt.Errorf("load recovery state: %w", err)
	}
	deletionStore, err := hubcore.NewDeletionStore(hubStateRoot)
	if err != nil {
		_ = hubListener.Close()
		return fmt.Errorf("load deletion state: %w", err)
	}
	transcriptDisplayStore, transcriptDisplayStoreErr := hubcore.NewTranscriptDisplayStore(hubStateRoot)
	if transcriptDisplayStoreErr != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] transcript display state: %v\n", transcriptDisplayStoreErr)
	}
	keybindingsStore, keybindingsStoreErr := hubcore.NewKeybindingsStore(hubStateRoot)
	if keybindingsStoreErr != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] keybindings state: %v\n", keybindingsStoreErr)
	}

	// Resolve the registry root once for this hub process. Launch configuration
	// may override XDG_CONFIG_HOME for a child, so the child must receive this
	// concrete root rather than resolving its own default from that environment.
	pluginRoot := plugins.NewManager("").Root

	// Web
	hostEntries := hostRegistryEntries(cfg)
	// Config loading already validated these through hostreg.New; build the
	// real registry used by the SSH manager and handle the (impossible) error
	// like any other startup failure. The file's retained high-water marks seed
	// the counters first, so a hub.toml host with no persisted record is minted
	// above every mark the file carries (registry spec 08 §1) rather than below
	// a mark another name retained.
	hostRegistry, err := hostreg.NewSeeded(hostEntries, hostHighWaterMarks(cfg))
	if err != nil {
		_ = hubListener.Close()
		return fmt.Errorf("validate hosts: %w", err)
	}
	// sshStateInvalidatedNavigation is late-bound: the sshconn manager is
	// constructed before the WebServer it must invalidate, and it is only ever
	// invoked once the background loops start attaching hosts.
	var sshStateInvalidatedNavigation func()
	// hostAttachedWakeup is late-bound like the navigation hook above: the
	// host-admin controller (and its per-host fan-outs) is constructed with
	// the WebServer below, after the SSH manager, and it is only ever invoked
	// once the background loops start attaching hosts.
	var hostAttachedWakeup func(host string)
	// hostManageEvents is late-bound the same way: the host-management
	// controller is constructed with the WebServer below, after the SSH
	// manager, and its event recorder is only ever invoked once the
	// background loops start attaching hosts.
	var hostManageEvents func(sshconn.Event)
	// The deploy wiring is a pure function of the flags: -deploy-binary wins over
	// -build-source, matching the manager's own BuildBinary-first dispatch, and
	// with neither set the default is this hub's own executable — unless
	// -no-deploy disables deploying. Startup says which source is effective, so
	// the default and an opt-out are visible rather than inferred.
	deploy := opts.deployWiring()
	if line := opts.deployPathLogLine(); line != "" {
		_, _ = fmt.Fprintln(stderr, line)
	}
	newSSHManager := deps.newSSHManager
	if newSSHManager == nil {
		newSSHManager = sshconn.New
	}
	sshManager := newSSHManager(hostRegistry, sshconn.Options{
		Logger:         func(format string, args ...any) { _, _ = fmt.Fprintf(stderr, "[hub] "+format+"\n", args...) },
		BuildBinary:    deploy.buildBinary,
		BuildSource:    deploy.buildSource,
		OwnExecutable:  deploy.ownExecutable,
		DeployDisabled: deploy.disabled,
		DeployHelp:     deploy.help,
		OnEvent: func(ev sshconn.Event) {
			hubSSHStateInvalidation(
				func() {
					if sshStateInvalidatedNavigation != nil {
						sshStateInvalidatedNavigation()
					}
				},
				// An attach is not only a liveness change: a host that was dormant
				// contributes no fresh rows to the snapshot walk (it is skipped
				// attached-only), so its threads stay absent from the navigation tree
				// until the next ~30s tick. Poke the remote-thread refresher on the
				// same transition so an explicit evener/host/attach populates the tree
				// immediately. remotePoke is buffered 1 and this send is non-blocking,
				// so the sshconn event loop never blocks on a refresh already pending.
				func(host string) {
					select {
					case remotePoke <- struct{}{}:
					default:
					}
					// The same transition wakes the host-notification fan-out: a
					// fan-out sleeping in backoff would otherwise wait up to 30s
					// before subscribing while the new client's notification buffer
					// fills undrained. The wakeup carries no client — the fan-out
					// still resolves the fresh client through ClientIfAttached —
					// and the send below is non-blocking for the same reason the
					// poke above is: the sshconn event loop must never block.
					if hostAttachedWakeup != nil {
						hostAttachedWakeup(host)
					}
				},
			)(ev)
			// The host-management surface records per-host attach state from
			// the same lifecycle events (midAttach, lastAttachError). The
			// recorder only writes its own map: OnEvent runs with the
			// per-host lock held, so it must never call back into the manager.
			if hostManageEvents != nil {
				hostManageEvents(ev)
			}
		},
	})
	// The manager owns every live SSH channel; tie their lifetime to this
	// process so they die with the hub.
	defer func() { _ = sshManager.Close() }()

	// Boot-prune the state-root write probe's crash orphans: evener/host/running
	// writes (and removes) one probe file per probe under every durable state
	// root, so a crash between its rename and its remove leaves a prefix-named
	// stray that only this pass removes.
	for _, root := range runningStateRoots(hubStateRoot, stateDir) {
		if pruned := pruneHostRunningProbeStrays(root); pruned > 0 {
			_, _ = fmt.Fprintf(stderr, "[hub] pruned %d orphaned host-running probe file(s) under %s\n", pruned, root)
		}
	}
	// The operation store opens before the hub serves anything. A corrupt store
	// file takes §4's custody-first quarantine; a corrupt file whose custody
	// snapshot is incomplete refuses the boot outright — the hub must never serve
	// hosts past a fence the quarantine cannot prove.
	opsStore, err := openHostOpsStore(hubStateRoot, stderr, hostOperationRetention(cfg))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	// The operation store loads before the web server is built; a loaded
	// compensation record whose stash reference is not this hub.toml family's
	// own must never become a read, restore, or remove target, so a foreign
	// path refuses startup (the machine-managed-file posture: a record this
	// build cannot account for is refused loudly, never served).
	if err := validateHostOpsStashReferences(opsStore, opts.configPath); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	web := newWebServer(hubcore.WebConfig{
		HubAddr:                   cfg.Addr,
		AuthToken:                 authToken,
		MobileBaseURL:             cfg.MobileBaseURL,
		HubStateRoot:              cfg.HubStateRoot,
		LaunchConfigRoot:          cmdutil.DefaultConfigRoot(),
		MachineRoots:              cfg.Roots,
		PluginRoot:                pluginRoot,
		TranscriptDisplayStore:    transcriptDisplayStore,
		TranscriptDisplayStoreErr: transcriptDisplayStoreErr,
		KeybindingsStore:          keybindingsStore,
		KeybindingsStoreErr:       keybindingsStoreErr,
		RunDir:                    runDir,
		DaemonIdleTimeout:         cfg.DaemonIdleTimeout,
		PastIndexPath:             pastIndexDB,
		Roster:                    roster,
		Past:                      past,
		Archive:                   archive,
		Favorite:                  favorite,
		PinSections:               pinSections,
		SessionSeen:               sessionSeen,
		MessageSearch:             messageSearch,
		Spawner:                   spawner,
		APILogDefault:             cfg.APILog,
		DeletionStore:             deletionStore,
		ResumeLocks:               resumeLocks,
		PastPerPage:               cfg.PastResultsPerPage,
		StateDir:                  stateDir,
		CredsStore:                credsStore,
		Registry:                  hubReg,
		ProvidersConfigPath:       providersConfigPath,
		CredentialsPath:           credentialsPath,
		NoUserLayer:               noUserLayer,
		PokeAttention:             pokeAttention,
		Inputs:                    inputs,
		RemoteThreadCache:         remoteCache,
		RemoteHosts:               hostEntries,
		// The one live registry the SSH manager dials through, shared with the
		// attach handler and the host-management surface, and the selected
		// hub.toml path the host surface rewrites in place (machine-managed).
		RemoteHostRegistry:   hostRegistry,
		RemoteHostSSHManager: sshManager,
		RemoteHostConfigPath: opts.configPath,
		RemoteHostOpsStore:   opsStore,
		// This hub's own running identity and the two owner-adjustable
		// deploy-pipeline knobs: the probe deadline the plan's gated probe
		// uses, and the minimum free space evener/host/running's health
		// predicate requires on each durable state root.
		HubBootID:             hubBootID,
		HubProcessStart:       hubProcessStart,
		HostProbeTimeout:      cfg.HostProbeTimeout,
		HostMinFreeSpaceBytes: cfg.HostMinFreeSpaceBytes,
		// The host-record retention knobs (registry spec 08 §6/§11/§15): the
		// tombstone retention and bounds and the receipt/marker/audit bounds the
		// host-management surface prunes and evicts by.
		HostTombstoneRetention:        cfg.HostTombstoneRetention,
		HostTombstoneMaxRows:          cfg.HostTombstoneMaxRows,
		HostTombstoneMaxRowBytes:      cfg.HostTombstoneMaxRowBytes,
		HostTombstoneMaxCount:         cfg.HostTombstoneMaxCount,
		HostTombstoneMaxBytes:         cfg.HostTombstoneMaxBytes,
		HostSupersededReceiptMaxCount: cfg.HostSupersededReceiptMaxCount,
		HostSupersededReceiptTTL:      cfg.HostSupersededReceiptTTL,
		HostPrunedReceiptMaxCount:     cfg.HostPrunedReceiptMaxCount,
		HostPrunedReceiptTTL:          cfg.HostPrunedReceiptTTL,
		HostKeylessAuditMaxCount:      cfg.HostKeylessAuditMaxCount,
		HostKeylessAuditTTL:           cfg.HostKeylessAuditTTL,
		RemoteHostClient: func(ctx context.Context, host string) (*appwire.Client, error) {
			ch, err := sshManager.Ensure(ctx, host)
			if err != nil {
				return nil, err
			}
			return ch.Client(), nil
		},
		// The attached-only lookup every non-explicit read path resolves
		// through, so none can implicitly attach a dormant host.
		RemoteHostClientIfAttached: sshManager.ClientIfAttached,
		RemoteHostFacts: func(ctx context.Context, host string, client *appwire.Client) (appsource.HostFacts, error) {
			return remoteHostFactsIfAttached(ctx, host, client, func(host string) (attachedChannelView, bool) {
				// Non-dialing: read one installed channel and refuse unless it is
				// the very channel client belongs to, so the facts can never
				// describe a different generation than the probe's wire reads.
				ch, ok := sshManager.ChannelIfAttached(host)
				if !ok {
					return nil, false
				}
				return ch, true
			})
		},
		RemoteHostHandshake: func(host string, client *appwire.Client) (appwire.InitializeResponse, bool) {
			ch, ok := sshManager.ChannelIfAttached(host)
			if !ok {
				return appwire.InitializeResponse{}, false
			}
			return remoteHostHandshakeForChannel(ch, client)
		},
		RemoteHostOnline: sshManager.Attached,
	}, appwireTrace)
	// Bind the host-notification wakeup now that the controller exists: the
	// sshconn manager (built above) fires EventAttached once the fresh channel
	// is installed, and the controller (built with the WebServer just above)
	// owns the fan-outs. The wakeup rides that same attach event — alongside
	// the navigation invalidation and the remote-thread poke wired into OnEvent
	// above — rather than inventing a second path. A nil controller (no remote
	// hosts) leaves the slot nil, which the callback already tolerates.
	if web.hostAdmin != nil {
		hostAttachedWakeup = web.hostAdmin.hostAttached
	}
	// Bind the host-management event recorder the same way: the sshconn
	// manager fires lifecycle events, and the host rows retain attach state
	// from them.
	if web.hostManage != nil {
		hostManageEvents = web.hostManage.observeEvent
	}
	// The deploy pipeline's operation workers run under the controller's
	// lifetime, never an RPC's. On the way out they are cancelled and their
	// records settle to `interrupted` (naming the shutdown) with their hosts'
	// gates released, before the SSH manager below tears the channels down.
	if web.hostManage != nil {
		defer func() { web.hostManage.ShutdownHostOperations(operationsShutdownTimeout) }()
	}
	// Drain the AppWire RPC server on every exit path, tracing or not: the
	// remote-admin fan-out is bound to appserver.Server.Lifetime(), and
	// Shutdown is what cancels it, so a hub that only stopped its HTTP server
	// would leave one fan-out goroutine per remote host subscribed to the
	// previous server's sources — a leak, and duplicate host notifications
	// once a replacement server subscribed too.
	//
	// It is registered after the SSH manager's teardown, so it runs first: the
	// fan-outs stop while the transports they read from are still open, rather
	// than discovering a closed channel and re-dialling on their next retry.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := web.appRPC.Shutdown(shutdownCtx); shutdownErr != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] drain AppWire connections: %v\n", shutdownErr)
		}
	}()

	// Navigation invalidation hooks: Roster's onChange and PastIndex's onRootChange
	// hooks already gate on an actual fingerprint delta (never a no-op
	// probe/rebuild cycle — see bump above), so composing the navigation
	// invalidation into the same hook pushes the sidebar exactly on a daemon
	// appearing/disappearing/changing liveness, or a root session changing or a
	// subagent appearing/disappearing in the past index. Archive and favorite decisions live in ArchiveStore/FavoriteStore,
	// which never route through PastIndex at all, so they invalidate directly.
	wirePastNavigation(past, bump, web.navigation)
	// A session whose turn the provider refused makes the hub check that
	// instance's credential at once (#3539); the watch starts probing once the
	// background runner exists, below.
	sessionCredentials := &sessionCredentialWatch{auth: web.auth}
	observeSessionCredentials := sessionCredentials.observer(roster)
	roster.SetOnChange(func() {
		bump()
		web.navigation.Invalidate(navigationChangeHint{})
		observeSessionCredentials()
	})
	archive.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{AllLoadedProjects: true}) })
	favorite.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{AllLoadedProjects: true}) })
	remoteCache.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{Sources: true}) })
	// A connection-state transition flips sshconn.Manager.Attached, which is the
	// online signal every remote row's liveness and the manifest's
	// sources[].online are derived from. Roster/PastIndex/remoteCache hooks do not
	// observe it on their own: the remote cache only refreshes on its ~30s tick and
	// only invalidates when its contents changed, and a detached host whose last
	// list already failed changes nothing there. Without this the fleet view can
	// keep reporting a host online after it dropped, or keep its rows metas-only
	// after it reconnected.
	sshStateInvalidatedNavigation = func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) }
	if pinSections != nil {
		pinSections.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) })
	}
	// A seen mark changes live rows' unseen flag; a mark committed by any
	// writer, the RPC or a session deletion's scrub, invalidates navigation.
	sessionSeen.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) })

	if deps.afterWeb != nil {
		deps.afterWeb(web)
	}

	// Lifecycle
	signalCtx, cancelSignals := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelSignals()
	ctx, cancelBackground := context.WithCancel(signalCtx)
	// The web server's request-triggered background work (the launch-model
	// refresh) hangs off this context, so shutdown cancels an outstanding
	// launch check instead of leaving it to outlive the hub.
	web.lifetime = ctx
	var background sync.WaitGroup
	defer func() {
		cancelBackground()
		background.Wait()
		// The refresh group is not the background group: a request-triggered
		// refresh is started by a handler, not by startBackground, so it is
		// awaited here. cancelBackground has already canceled its context.
		web.waitLaunchRefreshes()
	}()
	startBackground := func(fn func()) {
		background.Go(fn)
	}
	sessionCredentials.start(ctx, startBackground)
	// Populate the roster before serving so the first sidebar request can't hit
	// an empty roster (the "flash of no sessions" right after a restart). Probes
	// run concurrently, so this is bounded by ~one probe timeout regardless of
	// how many daemons are live.
	roster.Refresh()
	// Start the resettable navigation scheduler only after the initial roster
	// seed, so its first capture cannot publish a transient empty generation.
	startBackground(func() { web.navigation.Start(ctx) })
	// NavigationService is the sole typed-event authority. Drain its FIFO from
	// one lifecycle-owned publisher so readiness coalescing cannot duplicate or
	// reorder invalidations.
	startBackground(func() { runNavigationPublisher(ctx, web.navigation, web.appRPC) })
	startBackground(func() { watchHubRoster(ctx, roster) })

	startBackground(func() {
		ticker := time.NewTicker(cfg.PastIndexRebuild)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = past.Rebuild()
			}
		}
	})

	// Message search refresher: re-reads the transcripts that changed on the
	// past index's rebuild interval (S14).
	startBackground(func() { refreshHubMessageSearch(ctx, messageSearch, past, cfg.PastIndexRebuild) })

	// Attention watcher: derives each live session's attention level from the
	// same remote-inclusive metas/live the sidebar tree uses (see
	// web.navigationSnapshot), and broadcasts evener/attention/changed when a
	// session's level actually transitions (notifications.js drives the tab
	// title/favicon badge and OS notifications from it). Ticks every 5s and
	// on-demand via attentionPoke.
	startBackground(func() { watchHubAttention(ctx, attentionPoke, archive, web) })

	// Notices watcher: re-derives the hub's notices every few seconds and
	// broadcasts evener/notices/changed when they change (S11).
	startBackground(func() { watchHubNotices(ctx, web) })

	// Seed the bundled default marketplaces (best-effort, first-run-gated —
	// see SeedDefaultMarketplaces). Every evener CLI path does this already
	// (cmd/evener/run.go, serve.go, plugincmd.go); the hub was the one surface
	// that never did, so a fresh install whose first interaction is the web
	// UI (Settings → Marketplaces & Plugins) saw zero marketplaces until a
	// session happened to spawn and seed them first.
	seedHubMarketplaces(ctx, web)

	// Plugin auto-upgrade daemon (design doc §9.1): refreshes every known
	// marketplace, then upgrades every installed, git-backed plugin with
	// autoUpgrade enabled. Runs once immediately and then on
	// cfg.PluginAutoUpgradeInterval; gated by cfg.PluginAutoUpgrade (on by
	// default — see config.go). Never deletes; superseded dirs are reclaimed
	// separately by `evener plugin gc` (also run once here, before any session
	// exists, per §12).
	startHubPluginMaintenance(ctx, cfg, web, startBackground)
	// Remote-thread cache refresher: refreshRemoteThreads (web_api_tree.go)
	// walks every configured remote source's ListThreads, a synchronous
	// network hop that used to run inline on every navigation read. Move it
	// to a ~30s ticker + poke so a tree render never blocks on it; the navigation
	// read path (remoteTreeThreads) reads remoteCache.Get() instead whenever
	// RemoteThreadCache is configured.
	startBackground(func() { refreshHubRemoteThreads(ctx, remotePoke, web) })
	// Live-model prefetch: fetch every instance's /models listing into the
	// held registry once at startup, so the Providers sheet reads cached
	// inventory instead of fetching on open. Once only: the hub never polls
	// providers on a timer. Best-effort per instance; a provider that is down
	// keeps its catalog rows until a refresh. A pass that changes what any
	// client shows announces it over the reused instance channel, so every
	// browser refetches its list; a no-op pass stays silent.
	// Through deps so hermetic runMain tests stay offline: the default
	// warms the live cache from real provider endpoints.
	deps.startLivePrefetch(ctx, hubReg, web.auth, startBackground, func() {
		// A server-initiated pass has no originating client, so the broadcast
		// names none: every client, including the one that may have just asked
		// for the prefetch, reads it as an unowned list change and refetches.
		notifyInstanceUpdated(web.appRPC, "")
	})
	// Launch-model warm: run the picker's `evener launch-check --models` once
	// at startup, so the first model picker open is served from cache instead
	// of waiting on the live listing. After that the picker refreshes the list
	// when it is opened.
	// Through deps so hermetic runMain tests stay offline.
	deps.startLaunchPrefetch(ctx, web, startBackground)

	srv := &listenerHTTPServer{
		Server: &http.Server{
			Addr:              cfg.Addr,
			Handler:           web.Handler(),
			ReadHeaderTimeout: hubHTTPReadHeaderTimeout,
			IdleTimeout:       hubHTTPIdleTimeout,
		},
		ln: hubListener,
	}

	_, _ = fmt.Fprintf(stderr, "[hub] evener-hub %s listening on %s (run_dir=%s)\n", Version, cfg.Addr, runDir)
	// Build a usable auth URL. If the bind addr is 0.0.0.0 or ::, replace
	// it with a hostname the operator can reach the hub at.
	authHost := advertisedHubHost(cfg.Addr, hubHostname)
	_, _ = fmt.Fprintf(stderr, "[hub] auth URL (visit once per browser): %s\n", hubedge.AuthURLFor("http://"+authHost, authToken))
	_, _ = fmt.Fprintf(stderr, "[hub] auth token also at %s (use as Authorization: Bearer ... for scripted clients)\n", filepath.Join(hubStateRoot, hubedge.TokenFileName))
	if err := deps.serve(ctx, srv); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	return nil
}

// openHostOpsStore opens the hub's operation store — the file the
// host-management surface mirrors per-host boundary records into — beside the
// hub's other durable stores. A corrupt store file takes §4's custody-first
// quarantine: the corrupt file is renamed aside with its custody file, the
// replacement store serves empty, and a crash between the custody write and the
// rename, or between the rename and the replacement open, is completed on the
// next boot. The quarantine's operator-visible health signal names the
// quarantined file and its custody, and stays visible for as long as the
// custody file keeps the names it closed closed.
//
// A corrupt file whose custody snapshot is incomplete — anything that would
// leave a fence unprovable — fails startup here, never serves: the error aborts
// the boot rather than leaving host operations unwired. Every other open failure
// keeps the pre-quarantine disposition: the failure is logged and the hub
// serves with host operations unwired, because a transient permission or I/O
// problem is not a fence the hub cannot prove.
//
// The opened store also runs §3's boot reap: expired confirmation tokens are
// dropped at startup, so a restart never leaves an unexpired-looking row behind
// for a later pass to trust. A reap that cannot write is logged for the same
// reason the mirror's failures are — the hub serves, and the next validate or
// consume pass for a name reaps lazily anyway.
//
// The same boot pass deletes every probe-epoch row silently (§7: "an epoch-only
// probe record ... boot deletes it silently, never transitions it to
// interrupted, and never revives a token or a worker") and prunes the
// state-root write probe's crash orphans: an epoch with no mint behind it is
// inert, and a crashed probe's temp/target file is a stray nothing else will
// remove.
func openHostOpsStore(stateRoot string, stderr io.Writer, retention hostops.RetentionPolicy) (*hostops.Store, error) {
	store, err := hostops.OpenWithRetention(hostops.StorePath(stateRoot), retention)
	if err != nil {
		if errors.Is(err, hostops.ErrStoreCorrupt) {
			return nil, fmt.Errorf("host operation store: %w", err)
		}
		_, _ = fmt.Fprintf(stderr, "[hub] host operation store not opened, host boundary records will not be mirrored: %v\n", err)
		return nil, nil
	}
	if signal := store.Quarantine(); signal != nil {
		_, _ = fmt.Fprintf(stderr,
			"[hub] host operation store quarantined %s (custody %s, quarantine epoch %d); the replacement store serves the custody's orphan-unverified imports as historical rows (no name is held closed)\n",
			signal.QuarantinedFile, signal.CustodyFile, signal.QuarantineEpoch)
	}
	if reaped, err := store.ReapExpiredTokens(); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] host operation store opened, but its expired confirmation tokens were not reaped: %v\n", err)
	} else if reaped > 0 {
		_, _ = fmt.Fprintf(stderr, "[hub] host operation store reaped %d expired confirmation token(s)\n", reaped)
	}
	if reapedEpochs, err := store.ReapProbeEpochs(); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] host operation store opened, but its probe epochs were not reaped: %v\n", err)
	} else if reapedEpochs > 0 {
		_, _ = fmt.Fprintf(stderr, "[hub] host operation store reaped %d probe epoch(s)\n", reapedEpochs)
	}
	// §7's interrupted transition, before the store serves any request: a
	// record a crash left pending/running is a terminal unknown outcome. A
	// retry with the same operation ID gets the interrupted record back.
	if interrupted, err := store.RecoverInterrupted(); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] host operation store opened, but its in-flight operations were not moved to interrupted: %v\n", err)
	} else if interrupted > 0 {
		_, _ = fmt.Fprintf(stderr, "[hub] host operation store moved %d in-flight operation(s) to interrupted\n", interrupted)
	}
	return store, nil
}

// hostOperationRetention maps the hub's owner knobs onto the operation store's
// §4 retention policy: the five terminal/tombstone/store-byte/age bounds, with
// the removed-host horizon being the tombstoneRetention knob §4 cites
// (registry spec §15). Every zero value floors to the store's shipped default.
func hostOperationRetention(cfg Config) hostops.RetentionPolicy {
	return hostops.RetentionPolicy{
		TerminalPerHost:    cfg.HostOperationTerminalPerHost,
		TerminalStoreWide:  cfg.HostOperationTerminalStoreWide,
		StoreMaxBytes:      cfg.HostOperationStoreMaxBytes,
		TerminalMaxAge:     cfg.HostOperationTerminalMaxAge,
		TombstonesPerHost:  cfg.HostOperationTombstonesPerHost,
		RemovedHostHorizon: cfg.HostTombstoneRetention,
	}
}

// hostRegistryEntries maps the validated [[hosts]] entries onto the host
// registry's values. runMain hands the result to hostreg.New (the registry
// sshconn consumes) and to the web config's RemoteHosts (one source per host),
// so this mapping is the last place a configured field can be lost before
// either consumer sees it: every field belongs here, including the host's
// non-default locations (EvenerPath, ConfigPath, Addr) that keep the SSH
// manager attaching with the host's own hub.toml and probing its own listener.
//
// It is also where the file's machine records join the entry: the persisted
// (generation, incarnation id, presence epoch) triple is restored onto the
// entry, so the boot load keeps the identity the file recorded instead of
// minting a new one (registry spec 08 §15: "The boot load restores persisted
// generations before the store serves any request"). A file that carries no
// record leaves the zeros hostreg.New fills at load.
func hostRegistryEntries(cfg Config) []hostreg.Host {
	entries := make([]hostreg.Host, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		generation, incarnation, epoch := resolveHostIdentity(h.Name, cfg.HostRecords, cfg.Generations)
		entries = append(entries, hostreg.Host{
			Name:       h.Name,
			SSH:        h.SSH,
			User:       h.User,
			EvenerPath: h.EvenerPath,
			ConfigPath: h.ConfigPath,
			Addr:       h.Addr,
			Roots:      h.Roots,
			KeyPath:    h.KeyPath,
			// Generation, IncarnationID and PresenceEpoch: the persisted
			// identity, when the file carries one.
			Generation:    generation,
			IncarnationID: incarnation,
			PresenceEpoch: epoch,
		})
	}
	return entries
}

// hubSSHStateInvalidation adapts an sshconn lifecycle hook to navigation
// invalidation, calling invalidate only on the transitions that change
// Manager.Attached: an attach, a detach, or a terminal attach failure.
// Intermediate EventState transitions (preflighting, attaching, reconnecting)
// never change the attached answer, so they do not force a rebuild.
//
// onAttach, when non-nil, runs only on EventAttached, alongside invalidate. It
// exists because an attach is more than a liveness change: the snapshot walk is
// attached-only, so a newly attached host's threads are absent from the
// navigation tree until the refresher's next tick unless that transition pokes
// it. Detach and terminal failure do not need the extra callback — the
// last-known-good carry-forward already keeps a dropped host's rows.
func hubSSHStateInvalidation(invalidate func(), onAttach func(host string)) func(sshconn.Event) {
	return func(ev sshconn.Event) {
		switch ev.Kind {
		case sshconn.EventAttached:
			if invalidate != nil {
				invalidate()
			}
			if onAttach != nil {
				onAttach(ev.Host)
			}
		case sshconn.EventDetached, sshconn.EventFailed:
			if invalidate != nil {
				invalidate()
			}
		}
	}
}

func parseHubOptions(args []string, stderr io.Writer) (hubOptions, error) {
	opts := hubOptions{configPath: DefaultConfigPath()}
	fs := flag.NewFlagSet("evener hub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.configPath, "config", opts.configPath, "path to hub.toml")
	fs.StringVar(&opts.addr, "addr", "", "override hub listen address")
	fs.StringVar(&opts.evenerBinary, "evener", "", "path to evener binary (default: 'evener' on PATH)")
	fs.StringVar(&opts.appwireTrace, "appwire-trace", "", "write raw per-connection browser AppWire frames to a new JSONL file")
	fs.StringVar(&opts.deployBinary, "deploy-binary", "", "path to a pre-built evener for the host's target, pushed as-is (no build source or Go toolchain needed)")
	fs.StringVar(&opts.buildSource, "build-source", "", "path to an evener checkout's module root to cross-compile the host's target from")
	fs.BoolVar(&opts.noDeploy, "no-deploy", false, "never deploy to hosts: disable the own-executable default and ignore -deploy-binary/-build-source")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: evener-hub [flags]\n\nMulti-session web orchestrator for evener serve daemons.\n\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(stderr, "\nSubcommands:\n")
		_, _ = fmt.Fprintf(stderr, "  attach --stdio\tproxy AppWire between the hub's loopback /rpc and stdin/stdout\n")
		_, _ = fmt.Fprintf(stderr, "\nEnvironment variables:\n")
		printHubEnvVars(stderr)
	}
	err := fs.Parse(args)
	if err == nil && fs.NArg() != 0 {
		err = fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err == nil {
		// Only a path the operator named is a promise to load a file; the
		// default path is allowed to be absent. fs.Visit reports the flags that
		// were actually set, so the two are distinguishable after parsing.
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "config" {
				opts.configExplicit = true
			}
		})
	}
	// Validate the deploy flags where they are read: a bad path fails startup
	// naming the flag rather than surfacing at the first attach as a deploy
	// failure. A flag left unset needs no validation — and with both unset the
	// deploy default (this hub's own executable) is adopted only if it passes the
	// same checks, so an embedder or test binary still starts with no deploy path.
	if err == nil {
		err = opts.validateDeployFlags()
	}
	return opts, err
}

// remoteHostFacts assembles the preflight half of a remote host's capability
// snapshot from the channel's captured facts and the freshly initialized
// client's advertised features.
//
// The channel's facts are captured before attach: when the host's build differs
// from the controller's, sshconn's ensureOnce redeploys the controller's build
// and restarts the host hub before attaching, but the channel keeps those
// pre-deploy facts. pf.Version is therefore stale exactly in that case, while
// Features come from the newly initialized client. A successful Ensure
// guarantees the attached hub runs the controller's build — the only build the
// manager deploys — so report that version rather than the pre-deploy string.
func remoteHostFacts(pf sshconn.Preflight, features appwire.FeatureSet) appsource.HostFacts {
	return appsource.HostFacts{
		ProtocolVersion: pf.Protocol,
		HubVersion:      buildinfo.Version(),
		OS:              pf.OS,
		Arch:            pf.Arch,
		Features:        features,
	}
}

// attachedChannelView is the slice of a live sshconn.Channel the remote-host
// facts seams read. One sshconn.Manager.ChannelIfAttached lookup yields one
// generation, so reading the client, preflight, and handshake from the same
// value cannot splice a reconnect's facts onto the previous client's reads.
// *sshconn.Channel satisfies it.
type attachedChannelView interface {
	Client() *appwire.Client
	Preflight() sshconn.Preflight
	Handshake() appwire.InitializeResponse
}

// remoteHostFactsForChannel returns the preflight half of the host's capability
// snapshot for client, refusing when ch is not the channel client belongs to.
//
// The capability probe resolves client through the attached-only lookup and
// runs every wire read on it, then asks for the facts. A supervisor reconnect
// between those two steps would leave ch describing a newer connection than
// client; answering from it would let the probe cache a snapshot assembled from
// two generations — the hazard sshconn's channel identity check exists to
// prevent. Refuse with the typed unavailable error the auto-resume gate already
// understands instead; the probe is not cached on failure, so it re-probes the
// new generation cleanly.
func remoteHostFactsForChannel(ch attachedChannelView, host string, client *appwire.Client) (appsource.HostFacts, error) {
	if ch == nil || ch.Client() != client {
		return appsource.HostFacts{}, appwire.SessionUnavailable("remote hub unavailable: " + host)
	}
	return remoteHostFacts(ch.Preflight(), client.Features()), nil
}

// remoteHostFactsIfAttached is the RemoteHostFacts seam: it looks host up
// through lookup (a non-dialing attached-only channel lookup) and answers the
// facts for client's own generation.
//
// A caller cancellation or deadline is the caller's own context ending, not
// host unavailability, so it is returned raw before the lookup runs — exactly
// as the capability probe leaves a canceled context raw and resolveClient/call
// leave it raw. Reporting it as the typed SessionUnavailable would fire the
// auto-resume/refusal gates for a request the caller abandoned. A host whose
// channel is gone or is a different generation is the typed SessionUnavailable
// those gates act on.
func remoteHostFactsIfAttached(ctx context.Context, host string, client *appwire.Client, lookup func(string) (attachedChannelView, bool)) (appsource.HostFacts, error) {
	if err := ctx.Err(); err != nil {
		return appsource.HostFacts{}, err
	}
	ch, ok := lookup(host)
	if !ok {
		return appsource.HostFacts{}, appwire.SessionUnavailable("remote hub unavailable: " + host)
	}
	return remoteHostFactsForChannel(ch, host, client)
}

// remoteHostHandshakeForChannel returns the attach handshake ch captured, but
// only when ch is the channel client belongs to. Reporting false otherwise
// keeps the probe from pairing one connection's wire reads with another's
// handshake; the preflight facts it already accepted (remoteHostFactsForChannel)
// are then the only source, and those are pinned to the same client.
func remoteHostHandshakeForChannel(ch attachedChannelView, client *appwire.Client) (appwire.InitializeResponse, bool) {
	if ch == nil || ch.Client() != client {
		return appwire.InitializeResponse{}, false
	}
	return ch.Handshake(), true
}

// printVersionInfo prints version information including backend git SHA and frontend hash.
func printVersionInfo(w io.Writer) error {
	fHash, _ := frontendDistHash(distFS())
	if _, err := fmt.Fprintf(w, "evener-hub version: %s\n", buildinfo.VersionLong()); err != nil {
		return err
	}
	if buildinfo.GitSHA != "" {
		if _, err := fmt.Fprintf(w, "backend git SHA: %s\n", buildinfo.GitSHA); err != nil {
			return err
		}
	}
	if fHash != "" {
		if _, err := fmt.Fprintf(w, "frontend hash: %s\n", fHash); err != nil {
			return err
		}
	}
	return nil
}

func advertisedHubHost(addr string, hostname func() (string, error)) string {
	if !strings.HasPrefix(addr, "0.0.0.0:") && !strings.HasPrefix(addr, "[::]:") {
		return addr
	}
	port := addr[strings.LastIndex(addr, ":"):]
	host, _ := hostname()
	if host == "" {
		host = "localhost"
	}
	return host + port
}

func serveHub(ctx context.Context, srv hubHTTPServer) error {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.ListenAndServe()
	if ctx.Err() != nil {
		<-shutdownDone
	}
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func printHubEnvVars(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, v := range []envvars.Var{
		envvars.EVENERProvidersConfig,
		envvars.EVENERStateDir,
		envvars.OpenAIAPIKey,
		envvars.AnthropicAPIKey,
		envvars.GeminiAPIKey,
		envvars.GoogleAPIKey,
		envvars.OpenRouterAPIKey,
		envvars.EVENERPprofAddr,
	} {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", v.Name, v.Summary)
	}
	// Every implicit provider the registry knows reads its own key and base
	// URL; naming them all here would be a second, drifting roster.
	_, _ = fmt.Fprintf(tw, "  %s\t%s\n", "<ID>_API_KEY / <ID>_BASE_URL", "any implicit provider's key or base URL (evener providers list)")
	_ = tw.Flush()
}

// currentExecutable returns the path of the running evener-hub binary,
// preferring os.Executable() (always absolute on supported platforms)
// and falling back to os.Args[0]. The absolute path is what
// binresolve.Resolve needs to find a sibling "evener" binary even when
// evener-hub was launched via a relative path like "./evener-hub".
func currentExecutable() string {
	if exe, err := hubExecutable(); err == nil && exe != "" {
		return exe
	}
	args := hubProcessArgs()
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// resolveEvenerBinaryPath determines which "evener" binary the hub should
// invoke for launch-check + spawning. Resolution order is:
//  1. explicit (--evener flag): always wins.
//  2. sibling next to the running evener binary (the hub runs as `evener hub`).
//  3. lookup of "evener" on $PATH.
//
// When none of those succeed, "" is returned so HubSpawner falls back
// to its built-in default of running "evener" — which lets exec.Command
// do its own runtime PATH search (matching pre-kata behaviour).
func resolveEvenerBinaryPath(explicit, currentExecutable string, lookPath func(string) (string, error)) string {
	if explicit != "" {
		return explicit
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := binresolve.Resolve("evener", "", currentExecutable, lookPath)
	if err != nil {
		// Neither a sibling nor a PATH lookup succeeded. Fall back to
		// the empty default; HubSpawner will invoke "evener" and let
		// exec.Command surface a friendly error if it is unavailable.
		return ""
	}
	return path
}

// wirePastNavigation connects the past index to navigation invalidation and the
// shared inputs counter. Navigation lists roots only, so the root signal is
// the one that bumps and invalidates: a running subagent's autosave or title
// change moves neither, while a root's shown fields changing, or a subagent
// being added or removed, moves both.
func wirePastNavigation(past *hubcore.PastIndex, bump func(), navigation *NavigationService) {
	past.SetOnRootChange(func() { bump(); navigation.Invalidate(navigationChangeHint{}) })
}
