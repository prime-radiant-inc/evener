package hub

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/hubendpoint"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/rendezvous"
)

// startDiagnostics starts the hub's optional diagnostics: the live pprof
// listener and the raw AppWire frame trace. The returned stop closes the trace
// first and then stops pprof, the reverse of the order they started in. On
// error nothing is left running.
func startDiagnostics(opts hubOptions, stderr io.Writer) (*appserver.WebSocketTrace, func(), error) {
	_, stopPprof, err := cmdutil.StartLivePprof(func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, "[hub] "+format+"\n", args...)
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return nil, nil, err
	}
	if opts.appwireTrace == "" {
		return nil, stopPprof, nil
	}
	tracePath, absErr := filepath.Abs(opts.appwireTrace)
	if absErr != nil {
		stopPprof()
		_, _ = fmt.Fprintf(stderr, "[hub] appwire trace path: %v\n", absErr)
		return nil, nil, fmt.Errorf("resolve appwire trace path: %w", absErr)
	}
	trace, err := appserver.NewWebSocketTrace(tracePath)
	if err != nil {
		stopPprof()
		_, _ = fmt.Fprintf(stderr, "[hub] appwire trace: %v\n", err)
		return nil, nil, fmt.Errorf("create appwire trace: %w", err)
	}
	_, _ = fmt.Fprintf(stderr, "[hub] recording raw browser AppWire frames at %s; this file contains sensitive data\n", tracePath)
	return trace, func() {
		if closeErr := trace.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] close appwire trace: %v\n", closeErr)
		}
		stopPprof()
	}, nil
}

// publishEndpoint publishes the hub's endpoint record. It is called only after
// the actual listener and health handler exist: a record does not mean ready
// until a reader verifies this boot on the live socket. The returned retire
// withdraws the record and must run before the hub lock is released.
func publishEndpoint(web *WebServer, endpoint EndpointConfig, configIdentity, hubStateRoot, boundAddr string) (hubendpoint.Record, func() error, error) {
	record := hubendpoint.Record{
		SchemaVersion:       hubendpoint.SchemaVersion,
		BootID:              hubBootID,
		PID:                 os.Getpid(),
		StartedAt:           web.startedAt,
		ConfigPath:          configIdentity,
		HubStateRoot:        hubStateRoot,
		RequestedListenAddr: endpoint.ListenAddr.String(),
		ListenSource:        endpoint.ListenSource,
		BoundAddr:           boundAddr,
	}
	if endpoint.PublicURL != nil {
		record.PublicURL = endpoint.PublicURL.String()
	}
	if origin, originErr := hubendpoint.LocalOrigin(boundAddr); originErr == nil {
		record.LocalURL = origin.String()
		if ip := net.ParseIP(origin.Hostname()); ip != nil && ip.IsLoopback() {
			record.LoopbackURL = origin.String()
		}
	}
	retire, err := hubendpoint.Publish(hubStateRoot, record)
	if err != nil {
		return hubendpoint.Record{}, nil, fmt.Errorf("publish hub endpoint: %w", err)
	}
	return record, retire, nil
}

// announceHub tells the operator where the hub is listening and how to sign in.
func announceHub(stderr io.Writer, record hubendpoint.Record, runDir, hubStateRoot, authToken string) {
	_, _ = fmt.Fprintf(stderr, "[hub] evener-hub %s listening on %s (run_dir=%s)\n", Version, record.BoundAddr, runDir)
	_, _ = fmt.Fprintf(stderr, "[hub] configured listener: %s (%s)\n", record.RequestedListenAddr, record.ListenSource)
	if origin, originErr := browserLoginOrigin(record, hubHostname); originErr == nil {
		_, _ = fmt.Fprintf(stderr, "[hub] Browser address: %s (local: %s)\n", origin, record.LocalURL)
		if isTerminalWriter(stderr) {
			link := hubedge.AuthURLFor(origin, authToken)
			_, _ = fmt.Fprintf(stderr, "[hub] Private sign-in link (treat as a password): %s\n", link)
		}
	} else {
		_, _ = fmt.Fprintf(stderr, "[hub] cannot advertise browser origin: %v\n", originErr)
	}
	_, _ = fmt.Fprintln(stderr, "[hub] Keep this terminal running while you use Evener in the browser.")
	_, _ = fmt.Fprintln(stderr, "[hub] retrieve a browser login URL deliberately with evener hub login-url [--config PATH]; treat it as a password")
	_, _ = fmt.Fprintf(stderr, "[hub] Auth token file: %s (use as Authorization: Bearer ... for scripted clients)\n", filepath.Join(hubStateRoot, hubedge.TokenFileName))
}

// hubRuntime is the state the startup phases share. Each phase reads what an
// earlier phase filled in and hands back its own cleanup, so runMain keeps the
// defer order literal: the last phase started is the first one stopped.
type hubRuntime struct {
	opts   hubOptions
	deps   mainDeps
	stderr io.Writer

	cfg            Config
	endpoint       EndpointConfig
	configIdentity string

	runDir            string
	stateGlob         string
	pastIndexDB       string
	hubStateRoot      string
	stateDir          string
	authToken         string
	hubToken          string
	sessionToolsHubID string
	boundAddr         string
	pluginRoot        string

	roster           *hubcore.Roster
	past             *hubcore.PastIndex
	archive          *hubcore.ArchiveStore
	favorite         *hubcore.FavoriteStore
	pinSections      *hubcore.PinSectionStore
	sessionSeen      *hubcore.SessionSeenStore
	messageSearch    *hubcore.MessageSearch
	scratchReconcile *scratchReconciler
	credsStore       *credentials.Store
	hubReg           *hubcore.ProviderRegistry
	spawner          *HubSpawner
	listener         net.Listener
	hostEntries      []hostreg.Host
	hostRegistry     *hostreg.Registry
	sshManager       *sshconn.Manager
	opsStore         *hostops.Store
	// opsUnavailable marks a hub whose operation store failed to open: it serves
	// its hosts and refuses host changes until it is started with a store that
	// opens. openOps opens the store (a test replaces it).
	opsUnavailable bool
	// recordsUnreadable marks a start that could not read hub.toml's record
	// sections (see hubcore.WebConfig.RemoteHostRecordsUnreadable).
	recordsUnreadable bool
	openOps           func() (*hostops.Store, error)

	resumeLocks               *hubcore.ResumeLocks
	deletionStore             *hubcore.DeletionStore
	transcriptDisplayStore    *hubcore.TranscriptDisplayStore
	transcriptDisplayStoreErr error
	keybindingsStore          *hubcore.KeybindingsStore
	keybindingsStoreErr       error

	providersConfigPath string
	credentialsPath     string
	noUserLayer         bool

	// inputs is the shared source-revision counter used by NavigationService and
	// the remaining memoized tree projection; bumping it makes the next read
	// observe changed navigation inputs instead of stale state.
	inputs *hubcore.InputsVersion
	// attentionPoke lets a web handler (e.g. an archive decision) nudge the
	// attention watcher to recompute immediately instead of waiting for its next
	// tick. Buffered 1 + non-blocking send: a poke that arrives while one is
	// already pending coalesces into the same recompute. remotePoke is a second,
	// independently-buffered channel fed by the same pokeAttention call so the
	// remote-thread-cache refresher reacts to the same event without stealing
	// pokes from the attention watcher; each drains only its own.
	attentionPoke chan struct{}
	remotePoke    chan struct{}
	// remoteCache holds the last-refreshed remote-source thread list; the
	// refresher goroutine Stores into it on a ~30s ticker + poke, and the tree
	// read path reads it via WebConfig instead of walking the network per request.
	remoteCache *hubcore.RemoteThreadCache

	// Late-bound hooks. The sshconn manager is constructed before the WebServer
	// they point into, and is only ever invoked once the background loops start
	// attaching hosts. A nil slot (no remote hosts) is tolerated by the callers.
	sshStateInvalidatedNavigation func()
	hostAttachedWakeup            func(host string)
	hostManageEvents              func(sshconn.Event)

	sessionCredentials *sessionCredentialWatch
}

func (r *hubRuntime) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.stderr, "[hub] "+format+"\n", args...)
}

func (r *hubRuntime) bump() { r.inputs.Bump() }

func (r *hubRuntime) pokeAttention() {
	r.inputs.Bump()
	select {
	case r.attentionPoke <- struct{}{}:
	default:
	}
	select {
	case r.remotePoke <- struct{}{}:
	default:
	}
}

// loadHubRuntime resolves the configuration and endpoint this hub will run
// with and checks the machine can host it: a state home exists, the state root
// is on a supported filesystem, and the config directories are present.
func loadHubRuntime(opts hubOptions, stderr io.Writer, deps mainDeps) (*hubRuntime, error) {
	r := &hubRuntime{
		opts:          opts,
		deps:          deps,
		stderr:        stderr,
		inputs:        &hubcore.InputsVersion{},
		attentionPoke: make(chan struct{}, 1),
		remotePoke:    make(chan struct{}, 1),
		remoteCache:   &hubcore.RemoteThreadCache{},
	}
	cfg, err := deps.loadConfig(opts.configPath, opts.configExplicit)
	if err != nil {
		r.logf("config: %v", err)
		return nil, err
	}
	cfg, endpoint, err := resolveLoadedEndpointConfig(cfg, opts.configPath, EndpointInput{opts.addr, opts.addrPresent})
	if err != nil {
		r.logf("config: %v", err)
		return nil, err
	}
	configIdentity, err := endpointConfigPath(opts.configPath, endpoint.ConfigExists)
	if err != nil {
		r.logf("config: %v", err)
		return nil, err
	}
	// The registry's catalog cache and every OAuth record live under the
	// default state root whatever hub_state_root says, so a hub with no state
	// home cannot start; say so rather than run with auth that has nowhere to
	// keep records.
	if _, err := DefaultHubStateRoot(); err != nil {
		r.logf("state home: %v", err)
		return nil, err
	}
	if err := hostlock.CheckStorage(cfg.HubStateRoot, deps.inspectFilesystem); err != nil {
		r.logf("%v", err)
		return nil, err
	}
	// Stamp the build User-Agent once: previously every registry-client
	// construction rewrote this process global per request, racing
	// in-flight Codex requests. Fetch paths now bind scoped
	// authenticators per request and never write it.
	tokenauth.ClientVersion = buildinfo.Version()
	if err := deps.ensureDirs(); err != nil {
		r.logf("%v", err)
		return nil, err
	}
	r.cfg = cfg
	r.endpoint = endpoint
	r.configIdentity = configIdentity
	// hubStateRoot holds durable hub credentials, fences and preferences plus
	// coordination artifacts. The past index, daemon and project paths resolve
	// independently. Provider config remains under cmdutil.DefaultConfigRoot.
	r.hubStateRoot = cfg.HubStateRoot
	return r, nil
}

// acquireProcessLock takes the hub lock. Ownership is per selected root:
// hub.lock lives beside auth-token and deletions/ under HubStateRoot, not a raw
// home-dir join. The independently selected past index does not move with a
// hub_state_root override.
func (r *hubRuntime) acquireProcessLock() (func(), error) {
	release, err := r.deps.acquireLock(filepath.Join(r.hubStateRoot, "hub.lock"))
	if err != nil {
		r.logf("%v", err)
		return nil, err
	}
	return release, nil
}

// openStores resolves the runtime paths and opens the roster, the past index
// and the stores that sit beside it, the hub's tokens, and the message search
// index. The returned cleanup closes the search index.
func (r *hubRuntime) openStores() (func(), error) {
	cfg := r.cfg
	r.runDir = cfg.RunDir
	if r.runDir == "" {
		var err error
		if r.runDir, err = rendezvous.DefaultDir(); err != nil {
			return nil, fmt.Errorf("resolve runtime directory: %w", err)
		}
	}
	// Initial ownership discovery must be ready before any request can resume
	// a saved session; the background watcher is not a startup barrier.
	if err := os.MkdirAll(r.runDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare runtime directory: %w", err)
	}
	r.stateGlob = cfg.StateGlob
	if r.stateGlob == "" {
		var err error
		if r.stateGlob, err = DefaultStateGlob(); err != nil {
			return nil, fmt.Errorf("resolve state glob: %w", err)
		}
	}
	r.pastIndexDB = cfg.PastIndexDB
	if r.pastIndexDB == "" {
		var err error
		if r.pastIndexDB, err = DefaultPastIndexDBPath(); err != nil {
			return nil, fmt.Errorf("resolve past-session index: %w", err)
		}
	}

	prober := &hubcore.StatusProber{Timeout: r.deps.rosterProbeTimeout}
	r.roster = hubcore.NewRoster(r.runDir, prober)

	r.past = hubcore.NewPastIndexWithDB(r.stateGlob, r.pastIndexDB)
	if _, err := r.past.Rebuild(); err != nil {
		r.logf("past index rebuild: %v", err)
	}
	r.archive = hubcore.NewArchiveStore(r.pastIndexDB)
	r.favorite = hubcore.NewFavoriteStore(r.pastIndexDB)
	r.pinSections = hubcore.NewPinSectionStore(r.pastIndexDB)
	r.sessionSeen = hubcore.NewSessionSeenStore(r.pastIndexDB)
	// Resolve the durable peer identity before any listener is published.
	var err error
	r.sessionToolsHubID, err = hubcore.NewHubIdentityStore(r.pastIndexDB).HubID()
	if err != nil {
		return nil, fmt.Errorf("session tools hub identity: %w", err)
	}

	hubToken, err := r.deps.newToken()
	if err != nil {
		r.logf("%v", err)
		return nil, err
	}
	r.hubToken = hubToken
	r.authToken, err = r.deps.loadAuthToken(r.hubStateRoot)
	if err != nil {
		r.logf("auth token: %v", err)
		return nil, err
	}
	// stateDir is the parent of the projects/ directory; used for ForkSession
	// as a fallback when a session's project dir can't be found in the past index.
	r.stateDir = filepath.Dir(filepath.Clean(strings.TrimSuffix(r.stateGlob, "*")))

	// A session's Status transitioning (detected per-id by roster.Refresh)
	// means its daemon likely just rewrote its own meta.json out-of-process
	// (agent/session.go's periodic autosave); re-read just that session
	// instead of waiting for the past index's next 60s Rebuild tick, so the
	// sidebar order (which is keyed off UpdatedAt) doesn't lag behind a
	// completed turn.
	r.roster.SetOnStatusChange(refreshPastOnStatus(r.past))

	// The message search index (S14) lives in its own file beside index.db.
	// A hub that cannot open it still serves; search then finds sessions by
	// title and prompt only.
	closeSearch := func() {}
	r.messageSearch, err = hubcore.OpenMessageSearch(filepath.Join(r.hubStateRoot, "search.db"))
	if err != nil {
		r.logf("message search: %v", err)
	} else {
		search := r.messageSearch
		closeSearch = func() { _ = search.Close() }
		// A transcript whose index refused a refresh read asks the repair
		// registry's single lane for the shared repair.
		search.SetRepairAsker(transcriptIndexRepairs.askFromRefresher)
	}
	return closeSearch, nil
}

// loadProviders loads the credential store and the provider registry, and
// builds the spawner that launches sessions with them.
func (r *hubRuntime) loadProviders() error {
	r.providersConfigPath, r.noUserLayer = cmdutil.ProvidersConfigPath()
	r.credentialsPath = cmdutil.CredentialsPath()
	var err error
	r.credsStore, err = r.deps.loadCredentials(r.credentialsPath)
	if err != nil {
		r.logf("credentials store: %v", err)
		return err
	}
	// A providers.toml the registry cannot read is a diagnostic, not a
	// startup failure: the hub keeps an implicit-only registry, every child
	// it spawns resolves the same set, and instance writes stay refused
	// until the user fixes the file by hand (spec §10, §14.1).
	r.hubReg = hubcore.NewProviderRegistry(r.deps.loadRegistry)
	if err := r.hubReg.Reload(); err != nil {
		r.logf("providers config: %v — starting with implicit instances only", err)
	}
	resolvedEvenerBinary := resolveEvenerBinaryPath(r.opts.evenerBinary, currentExecutable(), exec.LookPath)
	if r.opts.evenerBinary == "" && resolvedEvenerBinary != "" && resolvedEvenerBinary != "evener" {
		r.logf("resolved evener at %s", resolvedEvenerBinary)
	}
	r.spawner = &HubSpawner{
		Cfg:                 r.cfg,
		EvenerBinary:        resolvedEvenerBinary,
		RunDir:              r.runDir,
		HubToken:            r.hubToken,
		Registry:            r.hubReg,
		ProvidersConfigPath: r.providersConfigPath,
		CredentialsPath:     r.credentialsPath,
		NoUserLayer:         r.noUserLayer,
	}
	return nil
}

// openListener binds the selected listener before anything downstream needs the
// actual bound address. endpoint retains the configured listener; cfg.Addr
// becomes the bound address for downstream consumers and restart publication.
// This makes "-addr 127.0.0.1:0" a real ephemeral-port request instead of a
// literal ":0" that never resolves to anything callable: the OS hands back a
// free port that cannot collide with another hub, sidestepping the TOCTOU race
// in "probe a free port, then hope nothing else grabs it before we bind" (see
// docs/developing-evener/agentic-testing.md). The returned cleanup closes it.
func (r *hubRuntime) openListener() (func(), error) {
	ln, err := r.deps.listen(context.Background(), "tcp", r.endpoint.ListenAddr.String())
	if err != nil {
		r.logf("listen %s: %v", r.cfg.Addr, err)
		return nil, fmt.Errorf("listen %s: %w", r.cfg.Addr, err)
	}
	hubBindWasEphemeral = isEphemeralAddr(r.cfg.Addr)
	r.cfg.Addr = ln.Addr().String()
	hubBoundAddr = r.cfg.Addr
	r.boundAddr = r.cfg.Addr
	r.listener = ln
	return func() { _ = ln.Close() }, nil
}

// openDurableState opens the hub's recovery, deletion, transcript display and
// keybinding state under the hub state root. The display and keybinding stores
// are optional: a failure is logged and the hub serves without them.
func (r *hubRuntime) openDurableState() error {
	var err error
	r.resumeLocks, err = hubcore.NewPersistentResumeLocks(r.hubStateRoot)
	if err != nil {
		return fmt.Errorf("load recovery state: %w", err)
	}
	r.deletionStore, err = hubcore.NewDeletionStore(r.hubStateRoot)
	if err != nil {
		return fmt.Errorf("load deletion state: %w", err)
	}
	r.transcriptDisplayStore, r.transcriptDisplayStoreErr = hubcore.NewTranscriptDisplayStore(r.hubStateRoot)
	if r.transcriptDisplayStoreErr != nil {
		r.logf("transcript display state: %v", r.transcriptDisplayStoreErr)
	}
	r.keybindingsStore, r.keybindingsStoreErr = hubcore.NewKeybindingsStore(r.hubStateRoot)
	if r.keybindingsStoreErr != nil {
		r.logf("keybindings state: %v", r.keybindingsStoreErr)
	}
	// Resolve the registry root once for this hub process. Launch configuration
	// may override XDG_CONFIG_HOME for a child, so the child must receive this
	// concrete root rather than resolving its own default from that environment.
	r.pluginRoot = plugins.NewManager("").Root
	return nil
}

// connectHosts builds the host registry, the SSH manager that dials through
// it, and the host operation store. The returned cleanup closes the manager,
// which owns every live SSH channel, so they die with the hub.
func (r *hubRuntime) connectHosts() (func(), error) {
	cfg := r.cfg
	// The operation store opens first: it holds the machine records the
	// registry's identities restore from. A store that cannot open does not stop
	// the hub. The hosts in hub.toml keep serving with the identities minted at
	// load, host changes refuse until the store opens, and the open is retried
	// in the background. A corrupt store file takes the custody-first quarantine
	// when its custody can be shown whole; one whose custody cannot is left as it
	// is until it is repaired (openHostOpsStore).
	r.openOpsStore()
	legacy, err := readLegacyRecords(r.opts.configPath)
	if err != nil {
		// The reserved sections an earlier build wrote cannot be read. The hosts
		// come up with identities minted at load, and the manager keeps host
		// changes paused instead of moving records it cannot read.
		r.logf("hub.toml record sections unreadable; hosts keep serving and host changes pause: %v", err)
		r.recordsUnreadable = true
	}
	boot := hostBootDocument(cfg, legacy, r.opsStore)
	r.hostEntries = boot.Hosts
	// Config loading already validated these through hostreg.New; build the
	// real registry used by the SSH manager and handle the (impossible) error
	// like any other startup failure. The retained high-water marks seed the
	// counters first, so a hub.toml host with no persisted record is minted
	// above every mark the records carry (registry spec 08 §1) rather than below
	// a mark another name retained.
	r.hostRegistry, err = hostreg.NewSeeded(r.hostEntries, boot.Records.highWaterMarks())
	if err != nil {
		return nil, fmt.Errorf("validate hosts: %w", err)
	}
	// The deploy wiring is a pure function of the flags: -deploy-binary wins over
	// -build-source, matching the manager's own BuildBinary-first dispatch, and
	// with neither set the default is this hub's own executable — unless
	// -no-deploy disables deploying. Startup says which source is effective, so
	// the default and an opt-out are visible rather than inferred.
	deploy := r.opts.deployWiring()
	if line := r.opts.deployPathLogLine(); line != "" {
		_, _ = fmt.Fprintln(r.stderr, line)
	}
	newSSHManager := r.deps.newSSHManager
	if newSSHManager == nil {
		newSSHManager = sshconn.New
	}
	r.sshManager = newSSHManager(r.hostRegistry, sshconn.Options{
		SessionToolsHubID: r.sessionToolsHubID,
		Logger:            r.logf,
		BuildBinary:       deploy.buildBinary,
		BuildSource:       deploy.buildSource,
		OwnExecutable:     deploy.ownExecutable,
		DeployDisabled:    deploy.disabled,
		DeployHelp:        deploy.help,
		OnEvent:           r.onSSHEvent,
	})
	closeSSH := func() { _ = r.sshManager.Close() }

	// Boot-prune the state-root write probe's crash orphans: evener/host/running
	// writes (and removes) one probe file per probe under every durable state
	// root, so a crash between its rename and its remove leaves a prefix-named
	// stray that only this pass removes.
	for _, root := range runningStateRoots(r.hubStateRoot, r.stateDir) {
		if pruned := pruneHostRunningProbeStrays(root); pruned > 0 {
			r.logf("pruned %d orphaned host-running probe file(s) under %s", pruned, root)
		}
	}
	for _, key := range cfg.removedKnobs {
		r.logf("hub.toml sets %s, which is no longer tunable; the hub ignores it and uses its built-in default", key)
	}
	// The operation store loads before the web server is built; a loaded
	// compensation record whose stash reference is not this hub.toml family's
	// own must never become a read, restore, or remove target, so a foreign
	// path refuses startup (the machine-managed-file posture: a record this
	// build cannot account for is refused loudly, never served).
	if err := validateHostOpsStashReferences(r.opsStore, r.opts.configPath); err != nil {
		closeSSH()
		r.logf("%v", err)
		return nil, err
	}
	return closeSSH, nil
}

// onSSHEvent fans an SSH lifecycle event out to navigation, the remote-thread
// refresher, the host-notification fan-out and the host-management surface.
func (r *hubRuntime) onSSHEvent(ev sshconn.Event) {
	hubSSHStateInvalidation(
		func() {
			if r.sshStateInvalidatedNavigation != nil {
				r.sshStateInvalidatedNavigation()
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
			case r.remotePoke <- struct{}{}:
			default:
			}
			// The same transition wakes the host-notification fan-out: a
			// fan-out sleeping in backoff would otherwise wait up to 30s
			// before subscribing while the new client's notification buffer
			// fills undrained. The wakeup carries no client — the fan-out
			// still resolves the fresh client through ClientIfAttached —
			// and the send below is non-blocking for the same reason the
			// poke above is: the sshconn event loop must never block.
			if r.hostAttachedWakeup != nil {
				r.hostAttachedWakeup(host)
			}
		},
	)(ev)
	// The host-management surface records per-host attach state from
	// the same lifecycle events (midAttach, lastAttachError). The
	// recorder only writes its own map: OnEvent runs with the
	// per-host lock held, so it must never call back into the manager.
	if r.hostManageEvents != nil {
		r.hostManageEvents(ev)
	}
}

// buildWeb constructs the web server over the opened stores and binds the hooks
// that were waiting for it. The returned cleanup drains the AppWire RPC server
// and then shuts the host operations down; it must run before connectHosts's
// cleanup closes the SSH channels.
func (r *hubRuntime) buildWeb(appwireTrace *appserver.WebSocketTrace) (*WebServer, func()) {
	cfg := r.cfg
	sshManager := r.sshManager
	r.scratchReconcile = newScratchReconciler(r.past, r.archive, r.roster)
	web := newWebServer(hubcore.WebConfig{
		HubAddr:                   r.boundAddr,
		AuthToken:                 r.authToken,
		PublicURL:                 cfg.PublicURL,
		HubStateRoot:              cfg.HubStateRoot,
		LaunchConfigRoot:          cmdutil.DefaultConfigRoot(),
		MachineRoots:              cfg.Roots,
		PluginRoot:                r.pluginRoot,
		TranscriptDisplayStore:    r.transcriptDisplayStore,
		TranscriptDisplayStoreErr: r.transcriptDisplayStoreErr,
		KeybindingsStore:          r.keybindingsStore,
		KeybindingsStoreErr:       r.keybindingsStoreErr,
		RunDir:                    r.runDir,
		SpawnTimeout:              cfg.SpawnTimeout,
		DaemonIdleTimeout:         cfg.DaemonIdleTimeout,
		PastIndexPath:             r.pastIndexDB,
		Roster:                    r.roster,
		Past:                      r.past,
		Archive:                   r.archive,
		Favorite:                  r.favorite,
		PinSections:               r.pinSections,
		SessionSeen:               r.sessionSeen,
		HubID:                     r.sessionToolsHubID,
		MessageSearch:             r.messageSearch,
		Spawner:                   r.spawner,
		APILogDefault:             cfg.APILog,
		DeletionStore:             r.deletionStore,
		ResumeLocks:               r.resumeLocks,
		PastPerPage:               cfg.PastResultsPerPage,
		StateDir:                  r.stateDir,
		CredsStore:                r.credsStore,
		Registry:                  r.hubReg,
		ProvidersConfigPath:       r.providersConfigPath,
		CredentialsPath:           r.credentialsPath,
		NoUserLayer:               r.noUserLayer,
		PokeAttention:             r.pokeAttention,
		ScratchReconcile:          r.scratchReconcile.Kick,
		Inputs:                    r.inputs,
		RemoteThreadCache:         r.remoteCache,
		RemoteHosts:               r.hostEntries,
		// The one live registry the SSH manager dials through, shared with the
		// attach handler and the host-management surface, and the selected
		// hub.toml path the host surface rewrites in place (machine-managed).
		RemoteHostRegistry:          r.hostRegistry,
		RemoteHostSSHManager:        sshManager,
		RemoteHostConfigPath:        r.opts.configPath,
		RemoteHostOpsStore:          r.opsStore,
		RemoteHostOpsUnavailable:    r.opsUnavailable,
		RemoteHostRecordsUnreadable: r.recordsUnreadable,
		// This hub's own running identity and the two owner-adjustable
		// deploy-pipeline knobs: the probe deadline the plan's gated probe
		// uses, and the minimum free space evener/host/running's health
		// predicate requires on each durable state root.
		HubBootID:              hubBootID,
		HubProcessStart:        hubProcessStart,
		HostProbeTimeout:       cfg.HostProbeTimeout,
		HostMinFreeSpaceBytes:  cfg.HostMinFreeSpaceBytes,
		HostTombstoneRetention: cfg.HostTombstoneRetention,
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
	// sshconn manager fires EventAttached once the fresh channel is installed,
	// and the controller (built with the WebServer just above) owns the
	// fan-outs. The wakeup rides that same attach event, alongside the
	// navigation invalidation and the remote-thread poke wired into onSSHEvent,
	// rather than inventing a second path. A nil controller (no remote hosts)
	// leaves the slot nil, which the callback already tolerates.
	if web.hostAdmin != nil {
		r.hostAttachedWakeup = web.hostAdmin.hostAttached
	}
	// Bind the host-management event recorder the same way: the sshconn
	// manager fires lifecycle events, and the host rows retain attach state
	// from them.
	if web.hostManage != nil {
		r.hostManageEvents = web.hostManage.observeEvent
	}
	return web, func() {
		// Drain the AppWire RPC server on every exit path, tracing or not: the
		// remote-admin fan-out is bound to appserver.Server.Lifetime(), and
		// Shutdown is what cancels it, so a hub that only stopped its HTTP server
		// would leave one fan-out goroutine per remote host subscribed to the
		// previous server's sources — a leak, and duplicate host notifications
		// once a replacement server subscribed too. It runs before the SSH
		// manager's teardown: the fan-outs stop while the transports they read
		// from are still open, rather than discovering a closed channel and
		// re-dialling on their next retry.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := web.appRPC.Shutdown(shutdownCtx); shutdownErr != nil {
			r.logf("drain AppWire connections: %v", shutdownErr)
		}
		// The deploy pipeline's operation workers run under the controller's
		// lifetime, never an RPC's. On the way out they are cancelled and their
		// records settle to `interrupted` (naming the shutdown) with their hosts'
		// gates released.
		if web.hostManage != nil {
			web.hostManage.ShutdownHostOperations(operationsShutdownTimeout)
		}
	}
}

// wireNavigationInvalidation composes the navigation invalidation into every
// store's change hook. Roster's onChange and PastIndex's onRootChange hooks
// already gate on an actual fingerprint delta (never a no-op probe/rebuild
// cycle), so the sidebar is pushed exactly on a daemon appearing, disappearing
// or changing liveness, or a root session changing or a subagent appearing or
// disappearing in the past index. Archive and favorite decisions live in
// ArchiveStore/FavoriteStore, which never route through PastIndex at all, so
// they invalidate directly.
func (r *hubRuntime) wireNavigationInvalidation(web *WebServer) {
	bump := r.bump
	wirePastNavigation(r.past, bump, web.navigation)
	// A session whose turn the provider refused makes the hub check that
	// instance's credential at once (#3539); the watch starts probing once the
	// background runner exists.
	r.sessionCredentials = &sessionCredentialWatch{auth: web.auth}
	observeSessionCredentials := r.sessionCredentials.observer(r.roster)
	r.roster.SetOnChange(func() {
		bump()
		web.navigation.Invalidate(navigationChangeHint{})
		observeSessionCredentials()
		reconcileSessionTools(web.sources)
	})
	r.archive.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{AllLoadedProjects: true}) })
	r.favorite.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{AllLoadedProjects: true}) })
	r.remoteCache.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{Sources: true}) })
	// A connection-state transition flips sshconn.Manager.Attached, which is the
	// online signal every remote row's liveness and the manifest's
	// sources[].online are derived from. Roster/PastIndex/remoteCache hooks do not
	// observe it on their own: the remote cache only refreshes on its ~30s tick and
	// only invalidates when its contents changed, and a detached host whose last
	// list already failed changes nothing there. Without this the fleet view can
	// keep reporting a host online after it dropped, or keep its rows metas-only
	// after it reconnected.
	r.sshStateInvalidatedNavigation = func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) }
	r.pinSections.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) })
	// A seen mark changes live rows' unseen flag; a mark committed by any
	// writer, the RPC or a session deletion's scrub, invalidates navigation.
	r.sessionSeen.SetOnChange(func() { bump(); web.navigation.Invalidate(navigationChangeHint{}) })
}

// startBackground starts the hub's lifecycle: signal handling and every
// background loop. It returns the context that ends on a signal or shutdown,
// and a cleanup that cancels the loops, waits for them, and then releases the
// signal handler.
func (r *hubRuntime) startBackground(web *WebServer) (context.Context, func()) {
	cfg, roster, past, deps := r.cfg, r.roster, r.past, r.deps
	signalCtx, cancelSignals := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancelBackground := context.WithCancel(signalCtx)
	// The web server's request-triggered background work (the launch-model
	// refresh) hangs off this context, so shutdown cancels an outstanding
	// launch check instead of leaving it to outlive the hub.
	web.lifetime = ctx
	var background sync.WaitGroup
	cleanup := func() {
		cancelBackground()
		background.Wait()
		// The refresh group is not the background group: a request-triggered
		// refresh is started by a handler, not by startBackground, so it is
		// awaited here. cancelBackground has already canceled its context.
		web.waitLaunchRefreshes()
		cancelSignals()
	}
	goBackground := func(fn func()) {
		background.Go(fn)
	}
	// Transcript-index repairs are detached on purpose: a multi-minute rebuild
	// must not hold hub exit, so the repair goroutine deliberately escapes
	// this awaited group and only the context gates starting one.
	transcriptIndexRepairs.wire(ctx)
	r.sessionCredentials.start(ctx, goBackground)
	startSessionTools(ctx, web.sources, goBackground)
	// Populate the roster before serving so the first sidebar request can't hit
	// an empty roster (the "flash of no sessions" right after a restart). Probes
	// run concurrently, so this is bounded by ~one probe timeout regardless of
	// how many daemons are live.
	roster.Refresh()
	// With live daemons known, catch up on archived sessions' scratch: ones that
	// aged into the archive and ones whose daemon exited while the hub was down.
	goBackground(func() { r.scratchReconcile.Run(ctx) })
	r.scratchReconcile.Kick()
	// Start the resettable navigation scheduler only after the initial roster
	// seed, so its first capture cannot publish a transient empty generation.
	goBackground(func() { web.navigation.Start(ctx) })
	// NavigationService is the sole typed-event authority. Drain its FIFO from
	// one lifecycle-owned publisher so readiness coalescing cannot duplicate or
	// reorder invalidations.
	goBackground(func() { runNavigationPublisher(ctx, web.navigation, web.appRPC) })
	goBackground(func() { watchHubRoster(ctx, roster) })

	goBackground(func() {
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
	goBackground(func() { refreshHubMessageSearch(ctx, r.messageSearch, past, cfg.PastIndexRebuild) })

	// Attention watcher: derives each live session's attention level from the
	// same remote-inclusive metas/live the sidebar tree uses (see
	// web.navigationSnapshot), and broadcasts evener/attention/changed when a
	// session's level actually transitions (notifications.js drives the tab
	// title/favicon badge and OS notifications from it). Ticks every 5s and
	// on-demand via attentionPoke.
	goBackground(func() { watchHubAttention(ctx, r.attentionPoke, r.archive, web) })

	// Notices watcher: re-derives the hub's notices every few seconds and
	// broadcasts evener/notices/changed when they change (S11).
	goBackground(func() { watchHubNotices(ctx, web) })

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
	// default — see config.go). Published plugin revisions are retained for
	// existing and resumable sessions. `evener plugin gc`, also run once here,
	// cleans unused marketplace clones without reclaiming published packages.
	startHubPluginMaintenance(ctx, cfg, web, goBackground)
	// Remote-thread cache refresher: refreshRemoteThreadSnapshot (web_api_tree.go)
	// walks every configured remote source's ListThreads, a synchronous
	// network hop that used to run inline on every navigation read. Move it
	// to a ~30s ticker + poke so a tree render never blocks on it; the navigation
	// read path (remoteThreadFetch) reads remoteCache.Snapshot() instead whenever
	// RemoteThreadCache is configured.
	goBackground(func() { refreshHubRemoteThreads(ctx, r.remotePoke, web) })
	// Live-model prefetch: fetch every instance's /models listing into the
	// held registry once at startup, so the Providers sheet reads cached
	// inventory instead of fetching on open. Once only: the hub never polls
	// providers on a timer. Best-effort per instance; a provider that is down
	// keeps its catalog rows until a refresh. A pass that changes what any
	// client shows announces it over the reused instance channel, so every
	// browser refetches its list; a no-op pass stays silent.
	// Through deps so hermetic runMain tests stay offline: the default
	// warms the live cache from real provider endpoints.
	deps.startLivePrefetch(ctx, r.hubReg, web.auth, goBackground, func() {
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
	deps.startLaunchPrefetch(ctx, web, goBackground)
	return ctx, cleanup
}

// hostOpsOpenRetryDelays are the waits between the attempts startup makes to
// open the operation store before it gives up and serves without it.
var hostOpsOpenRetryDelays = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}

// openOpsStore opens the operation store at startup, retrying a failed open a
// few times for the transient failures a start can meet. A store that still
// cannot open does not stop the hub: it serves the hosts in hub.toml and
// refuses host changes until the hub is started with a store that opens.
func (r *hubRuntime) openOpsStore() {
	if r.openOps == nil {
		r.openOps = func() (*hostops.Store, error) {
			return openHostOpsStore(r.hubStateRoot, r.stderr, hostOperationRetention(r.cfg))
		}
	}
	store, err := r.openOps()
	for _, delay := range hostOpsOpenRetryDelays {
		if err == nil {
			break
		}
		r.logf("host operation store not opened yet; retrying: %v", err)
		time.Sleep(delay)
		store, err = r.openOps()
	}
	if err != nil {
		r.opsUnavailable = true
		r.logf("host operation store unavailable; hosts keep serving and host changes stay paused until the hub is started with a store that opens: %v", err)
		return
	}
	r.opsStore = store
}
