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
	"primeradiant.com/evener/cmd/evener-hub/internal/codexlaunch"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/binresolve"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/plugins"
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

type hubShutdowner interface {
	Shutdown(context.Context) error
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
}

type mainDeps struct {
	loadConfig      func(string) (Config, error)
	ensureDirs      func() error
	acquireLock     func(string) (func(), error)
	newToken        func() (string, error)
	loadAuthToken   func(string) (string, error)
	loadCredentials func(string) (*credentials.Store, error)
	loadRegistry    hubcore.RegistryLoader
	notifyContext   func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	listen          func(context.Context, string, string) (net.Listener, error)
	serve           func(context.Context, hubHTTPServer, hubShutdowner) error
	afterWeb        func(*WebServer)
}

func defaultMainDeps() mainDeps {
	return mainDeps{
		loadConfig:      LoadConfig,
		ensureDirs:      cmdutil.EnsureUserConfigDirs,
		acquireLock:     hostlock.AcquireLock,
		newToken:        newHubToken,
		loadAuthToken:   hubedge.LoadOrCreateAuthToken,
		loadCredentials: credentials.LoadStore,
		loadRegistry:    cmdutil.LoadRegistry,
		notifyContext:   signal.NotifyContext,
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: serveHub,
	}
}

func Run(args []string, _ io.Reader, _ io.Writer, stderr io.Writer) int {
	if err := hubRunMain(args, stderr, defaultMainDeps()); err != nil && !errors.Is(err, flag.ErrHelp) {
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

	// Resolve runtime paths.
	runDir := cfg.RunDir
	if runDir == "" {
		runDir = rendezvous.DefaultDir()
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
		StateRoot:           hubStateRoot,
		ProvidersConfigPath: providersConfigPath,
		CredentialsPath:     credentialsPath,
		NoUserLayer:         noUserLayer,
	}
	var codexLauncher *codexlaunch.CodexLauncher
	if len(cfg.CodexLaunches) > 0 {
		codexLauncher = codexlaunch.NewCodexLauncher(cfg.CodexLaunches)
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

	deletionStore, err := hubcore.NewDeletionStore(hubStateRoot)
	if err != nil {
		_ = hubListener.Close()
		return fmt.Errorf("load deletion state: %w", err)
	}
	transcriptDisplayStore, transcriptDisplayStoreErr := hubcore.NewTranscriptDisplayStore(hubStateRoot)
	if transcriptDisplayStoreErr != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] transcript display state: %v\n", transcriptDisplayStoreErr)
	}

	// Resolve the registry root once for this hub process. Launch configuration
	// may override XDG_CONFIG_HOME for a child, so the child must receive this
	// concrete root rather than resolving its own default from that environment.
	pluginRoot := plugins.NewManager("").Root

	// Web
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:                   cfg.Addr,
		AuthToken:                 authToken,
		MobileBaseURL:             cfg.MobileBaseURL,
		HubStateRoot:              cfg.HubStateRoot,
		LaunchConfigRoot:          cmdutil.DefaultConfigRoot(),
		PluginRoot:                pluginRoot,
		TranscriptDisplayStore:    transcriptDisplayStore,
		TranscriptDisplayStoreErr: transcriptDisplayStoreErr,
		RunDir:                    runDir,
		PastIndexPath:             pastIndexDB,
		Roster:                    roster,
		Past:                      past,
		Archive:                   archive,
		Favorite:                  favorite,
		PinSections:               pinSections,
		Spawner:                   spawner,
		DeletionStore:             deletionStore,
		PastPerPage:               cfg.PastResultsPerPage,
		StateDir:                  stateDir,
		CredsStore:                credsStore,
		Registry:                  hubReg,
		ProvidersConfigPath:       providersConfigPath,
		CredentialsPath:           credentialsPath,
		NoUserLayer:               noUserLayer,
		CodexSources:              cfg.CodexSources,
		CodexLaunches:             cfg.CodexLaunches,
		CodexLauncher:             codexLauncher,
		PokeAttention:             pokeAttention,
		Inputs:                    inputs,
		RemoteThreadCache:         remoteCache,
	})

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
	seedHubMarketplaces(ctx)

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
	startBackground(func() { refreshHubRemoteThreads(ctx, remotePoke, remoteCache, web) })

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
	_, _ = fmt.Fprintf(os.Stderr, "[hub] auth URL (visit once per browser): http://%s/auth?token=%s\n", authHost, authToken)
	_, _ = fmt.Fprintf(os.Stderr, "[hub] auth token also at %s (use as Authorization: Bearer ... for scripted clients)\n", filepath.Join(hubStateRoot, hubedge.TokenFileName))
	if err := deps.serve(ctx, srv, codexShutdowner(codexLauncher)); err != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] %v\n", err)
		return err
	}
	return nil
}

func parseHubOptions(args []string, stderr io.Writer) (hubOptions, error) {
	opts := hubOptions{configPath: DefaultConfigPath()}
	fs := flag.NewFlagSet("evener hub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.configPath, "config", opts.configPath, "path to hub.toml")
	fs.StringVar(&opts.addr, "addr", "", "override hub listen address")
	fs.StringVar(&opts.evenerBinary, "evener", "", "path to evener binary (default: 'evener' on PATH)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: evener-hub [flags]\n\nMulti-session web orchestrator for evener serve daemons.\n\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(stderr, "\nEnvironment variables:\n")
		printHubEnvVars(stderr)
	}
	err := fs.Parse(args)
	if err == nil && fs.NArg() != 0 {
		err = fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return opts, err
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

// codexShutdowner converts a possibly-absent launcher into a companion
// serveHub can honestly nil-check. Assigning a nil *CodexLauncher straight to
// the interface yields a value with a type but no data, which is NOT equal to
// nil - so serveHub's `if companion != nil` would pass and Shutdown would take
// l.Mu.Lock() on a nil receiver. A hub with no codex launches configured is
// the default, so that is every graceful shutdown.
func codexShutdowner(l *codexlaunch.CodexLauncher) hubShutdowner {
	if l == nil {
		return nil
	}
	return l
}

func serveHub(ctx context.Context, srv hubHTTPServer, companion hubShutdowner) error {
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		if companion != nil {
			_ = companion.Shutdown(shutdownCtx)
		}
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
	} {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", v.Name, v.Summary)
	}
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
