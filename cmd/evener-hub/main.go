// Command evener-hub is the web orchestrator for evener serve daemons.
package hub

import (
	"context"
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

var (
	hubExecutable  = os.Executable
	hubProcessArgs = func() []string { return os.Args }
	hubHostname    = os.Hostname
	hubRunMain     = runMain
)

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
	configPath   string
	addr         string
	evenerBinary string
	appwireTrace string
	// deployBinary and buildSource describe how a missed host gets the
	// controller's build pushed to it. They are empty for a local-only
	// controller, which needs no deploy path at all.
	deployBinary string
	buildSource  string
}

type mainDeps struct {
	loadConfig      func(string) (Config, error)
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
	startLivePrefetch func(context.Context, *hubcore.ProviderRegistry, time.Duration, func(func()), func())
	notifyContext     func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	listen            func(context.Context, string, string) (net.Listener, error)
	serve             func(context.Context, hubHTTPServer) error
	afterWeb          func(*WebServer)
	// stdin/stdout carry the process streams the `attach` subcommand bridges to
	// the hub's loopback AppWire edge. The normal hub command ignores them.
	stdin  io.Reader
	stdout io.Writer
}

func defaultMainDeps() mainDeps {
	return mainDeps{
		loadConfig:        LoadConfig,
		ensureDirs:        cmdutil.EnsureUserConfigDirs,
		acquireLock:       hostlock.AcquireLock,
		newToken:          newHubToken,
		loadAuthToken:     hubedge.LoadOrCreateAuthToken,
		loadCredentials:   credentials.LoadStore,
		loadRegistry:      cmdutil.LoadRegistry,
		startLivePrefetch: startLiveModelsPrefetch,
		notifyContext:     signal.NotifyContext,
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

	cfg, err := deps.loadConfig(opts.configPath)
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

	pprofURL, stopPprof, err := cmdutil.StartLivePprof()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	defer stopPprof()
	if pprofURL != "" {
		_, _ = fmt.Fprintf(stderr, "[hub] pprof listening on %s\n", pprofURL)
	}

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
	prober := &hubcore.StatusProber{Timeout: 500 * time.Millisecond}
	roster := hubcore.NewRoster(runDir, prober)

	past := hubcore.NewPastIndexWithDB(stateGlob, pastIndexDB)
	if _, err := past.Rebuild(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "[hub] past index rebuild: %v\n", err)
	}
	archive := hubcore.NewArchiveStore(pastIndexDB)
	favorite := hubcore.NewFavoriteStore(pastIndexDB)
	pinSections := hubcore.NewPinSectionStore(pastIndexDB)

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
		_, _ = fmt.Fprintf(os.Stderr, "[hub] resolved evener at %s\n", resolvedEvenerBinary)
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
	// like any other startup failure.
	hostRegistry, err := hostreg.New(hostEntries)
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
	// -build-source, matching the manager's own BuildBinary-first dispatch. When
	// both are set, say which one is used rather than silently ignoring the other.
	deploy := opts.deployWiring()
	switch {
	case opts.deployBinary != "" && opts.buildSource != "":
		_, _ = fmt.Fprintf(stderr, "[hub] deploy path: -deploy-binary %s takes precedence over -build-source %s\n", opts.deployBinary, opts.buildSource)
	case opts.deployBinary != "":
		_, _ = fmt.Fprintf(stderr, "[hub] deploy path: -deploy-binary %s\n", opts.deployBinary)
	case opts.buildSource != "":
		_, _ = fmt.Fprintf(stderr, "[hub] deploy path: -build-source %s\n", opts.buildSource)
	}
	newSSHManager := deps.newSSHManager
	if newSSHManager == nil {
		newSSHManager = sshconn.New
	}
	sshManager := newSSHManager(hostRegistry, sshconn.Options{
		Logger:      func(format string, args ...any) { _, _ = fmt.Fprintf(stderr, "[hub] "+format+"\n", args...) },
		BuildBinary: deploy.buildBinary,
		BuildSource: deploy.buildSource,
		DeployHelp:  deploy.help,
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

	web := newWebServer(hubcore.WebConfig{
		HubAddr:                   cfg.Addr,
		AuthToken:                 authToken,
		MobileBaseURL:             cfg.MobileBaseURL,
		HubStateRoot:              cfg.HubStateRoot,
		LaunchConfigRoot:          cmdutil.DefaultConfigRoot(),
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
		// hub.toml path the UI's host sidecar persists beside.
		RemoteHostRegistry:   hostRegistry,
		RemoteHostSSHManager: sshManager,
		RemoteHostConfigPath: opts.configPath,
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

	// Navigation invalidation hooks: Roster/PastIndex's onChange hook already
	// gates on an actual content-fingerprint delta (never a no-op probe/rebuild
	// cycle — see bump above), so composing the navigation invalidation into the
	// same hook pushes the sidebar exactly on a daemon appearing/disappearing/
	// changing liveness, or a session appearing/ending/changing in the past
	// index. Archive and favorite decisions live in ArchiveStore/FavoriteStore,
	// which never route through PastIndex at all, so they invalidate directly.
	past.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) })
	roster.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) })
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

	if deps.afterWeb != nil {
		deps.afterWeb(web)
	}

	// Lifecycle
	signalCtx, cancelSignals := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelSignals()
	ctx, cancelBackground := context.WithCancel(signalCtx)
	var background sync.WaitGroup
	defer func() {
		cancelBackground()
		background.Wait()
	}()
	startBackground := func(fn func()) {
		background.Go(fn)
	}
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

	// Attention watcher: derives each live session's attention level from the
	// same roster/past-index/archive inputs the sidebar tree uses, and
	// broadcasts evener/attention/changed whenever a session's level actually
	// transitions (notifications.js drives the tab title/favicon badge and OS
	// notifications from it). Ticks every 5s and on-demand via attentionPoke.
	startBackground(func() { watchHubAttention(ctx, attentionPoke, archive, past, roster, web) })

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
	// held registry once at startup and every livePrefetchInterval after,
	// so the Providers sheet reads cached inventory instead of fetching on
	// open. Best-effort per instance; a provider that is down keeps its
	// catalog rows until the next tick. A pass that changes what any
	// client shows announces it over the reused instance channel, so every
	// browser refetches its list; a no-op pass stays silent.
	// Through deps so hermetic runMain tests stay offline: the default
	// warms the live cache from real provider endpoints.
	deps.startLivePrefetch(ctx, hubReg, livePrefetchInterval, startBackground, func() {
		// A server-initiated pass has no originating client, so the broadcast
		// names none: every client, including the one that may have just asked
		// for the prefetch, reads it as an unowned list change and refetches.
		notifyInstanceUpdated(web.appRPC, "")
	})

	srv := &listenerHTTPServer{
		Server: &http.Server{
			Addr:    cfg.Addr,
			Handler: web.Handler(),
		},
		ln: hubListener,
	}

	_, _ = fmt.Fprintf(os.Stderr, "[hub] evener-hub %s listening on %s (run_dir=%s)\n", Version, cfg.Addr, runDir)
	// Build a usable auth URL. If the bind addr is 0.0.0.0 or ::, replace
	// it with a hostname the operator can reach the hub at.
	authHost := advertisedHubHost(cfg.Addr, hubHostname)
	_, _ = fmt.Fprintf(os.Stderr, "[hub] auth URL (visit once per browser): %s\n", hubedge.AuthURLFor("http://"+authHost, authToken))
	_, _ = fmt.Fprintf(os.Stderr, "[hub] auth token also at %s (use as Authorization: Bearer ... for scripted clients)\n", filepath.Join(hubStateRoot, hubedge.TokenFileName))
	if err := deps.serve(ctx, srv); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	return nil
}

// hostRegistryEntries maps the validated [[hosts]] entries onto the host
// registry's values. runMain hands the result to hostreg.New (the registry
// sshconn consumes) and to the web config's RemoteHosts (one source per host),
// so this mapping is the last place a configured field can be lost before
// either consumer sees it: every field belongs here, including the host's
// non-default locations (EvenerPath, ConfigPath, Addr) that keep the SSH
// manager attaching with the host's own hub.toml and probing its own listener.
func hostRegistryEntries(cfg Config) []hostreg.Host {
	entries := make([]hostreg.Host, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		entries = append(entries, hostreg.Host{
			Name:       h.Name,
			SSH:        h.SSH,
			User:       h.User,
			EvenerPath: h.EvenerPath,
			ConfigPath: h.ConfigPath,
			Addr:       h.Addr,
			Roots:      h.Roots,
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
	// Validate the deploy flags where they are read: a bad path fails startup
	// naming the flag rather than surfacing at the first attach as a deploy
	// failure. A flag left unset needs no validation, so a local-only controller
	// still starts.
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
