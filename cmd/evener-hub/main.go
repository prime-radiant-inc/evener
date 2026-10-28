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
	"strings"
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
	"primeradiant.com/evener/internal/binresolve"
	"primeradiant.com/evener/internal/credentials"

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
	addrPresent    bool
	evenerBinary   string
	appwireTrace   string
	// deployBinary and buildSource describe how a host needing installation gets the
	// controller's build pushed to it. With neither set, deployBinary defaults to
	// this hub's own executable (deployDefault records that), so a host that
	// needs installation is provisioned by default; -no-deploy disables the
	// default and both flags while preserving compatible remote connections.
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
	// inspectFilesystem is the external fstatfs boundary, nil uses the OS.
	inspectFilesystem hostlock.FilesystemInspector
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
	if len(args) > 0 && args[0] == "endpoint" {
		return runEndpoint(args[1:], deps.stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "login-url" {
		return runLoginURL(args[1:], deps.stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "restart" {
		return runHubRestart(args[1:], stderr, deps)
	}

	// Record the launch entrypoint once, before any request can resolve it
	// against a changed PATH or working directory. A failure disables restart
	// without preventing the hub from starting.
	hubLaunchEntrypoint, errHubLaunchEntrypoint = resolveLaunchEntrypoint(hubProcessArgs(), hubExecutable)
	if errHubLaunchEntrypoint != nil {
		_, _ = fmt.Fprintf(stderr, "[hub] launch path: %v\n", errHubLaunchEntrypoint)
	} else {
		_, _, hubLaunchShareBinDir = hubInstallDirs()
		if digest, err := launchBinaryDigest(hubLaunchEntrypoint); err == nil {
			hubLaunchBinarySHA256 = digest
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

	r, err := loadHubRuntime(opts, stderr, deps)
	if err != nil {
		return err
	}
	release, err := r.acquireProcessLock()
	if err != nil {
		return err
	}
	defer release()

	appwireTrace, stopDiagnostics, err := startDiagnostics(opts, stderr)
	if err != nil {
		return err
	}
	defer stopDiagnostics()

	closeStores, err := r.openStores()
	if err != nil {
		return err
	}
	defer closeStores()
	if err := r.loadProviders(); err != nil {
		return err
	}
	closeListener, err := r.openListener()
	if err != nil {
		return err
	}
	defer closeListener()
	if err := r.openDurableState(); err != nil {
		return err
	}
	closeHosts, err := r.connectHosts()
	if err != nil {
		return err
	}
	defer closeHosts()
	web, closeWeb := r.buildWeb(appwireTrace)
	defer closeWeb()
	r.wireNavigationInvalidation(web)

	if deps.afterWeb != nil {
		deps.afterWeb(web)
	}

	ctx, stopBackground := r.startBackground(web)
	defer stopBackground()

	srv := &listenerHTTPServer{
		Server: &http.Server{
			Addr:              r.boundAddr,
			Handler:           web.Handler(),
			ReadHeaderTimeout: hubHTTPReadHeaderTimeout,
			IdleTimeout:       hubHTTPIdleTimeout,
		},
		ln: r.listener,
	}
	record, retireEndpoint, err := publishEndpoint(web, r.endpoint, r.configIdentity, r.hubStateRoot, r.boundAddr)
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := retireEndpoint(); removeErr != nil {
			_, _ = fmt.Fprintf(stderr, "[hub] retire endpoint: %v\n", removeErr)
		}
	}()
	announceHub(stderr, record, r.runDir, r.hubStateRoot, r.authToken)
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

// hostOperationRetention maps the hub's tombstone retention onto the operation
// store's removed-host horizon (registry spec §15); every other bound takes the
// store's shipped default.
func hostOperationRetention(cfg Config) hostops.RetentionPolicy {
	return hostops.RetentionPolicy{RemovedHostHorizon: cfg.HostTombstoneRetention}
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
		_, _ = fmt.Fprintf(stderr, "  endpoint --json [--config PATH]\tinspect the selected hub without credentials\n")
		_, _ = fmt.Fprintf(stderr, "  login-url [--config PATH]\tprint a password-like browser login link for the running hub\n")
		_, _ = fmt.Fprintf(stderr, "\nEnvironment variables:\n")
		printHubEnvVars(stderr)
	}
	err := fs.Parse(args)
	if err == nil && fs.NArg() != 0 {
		err = fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err == nil {
		// Only a path the operator named is a promise to load a file; the
		// default path is allowed to be absent.
		set := setFlagNames(fs)
		opts.configExplicit = set["config"]
		opts.addrPresent = set["addr"]
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

// remoteHostFacts combines disk system facts with the serving process's
// captured handshake. Missing live build metadata stays unknown.
func remoteHostFacts(pf sshconn.Preflight, handshake appwire.InitializeResponse, features appwire.FeatureSet) appsource.HostFacts {
	return appsource.HostFacts{
		ProtocolVersion: handshake.ProtocolVersion,
		HubVersion:      handshake.ServerInfo.BuildVersion,
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

// remoteHostFactsForChannel returns the host's preflight facts for client,
// refusing when ch is not the channel client belongs to.
//
// Attach resolves client through the attached-only lookup, then asks for the
// facts. A supervisor reconnect between those two steps would leave ch
// describing a newer connection than client; answering from it would report
// facts assembled from two generations — the hazard sshconn's channel identity
// check exists to prevent. Refuse with the typed unavailable error the
// auto-resume gate already understands instead.
func remoteHostFactsForChannel(ch attachedChannelView, host string, client *appwire.Client) (appsource.HostFacts, error) {
	if ch == nil || ch.Client() != client {
		return appsource.HostFacts{}, appwire.SessionUnavailable("remote hub unavailable: " + host)
	}
	return remoteHostFacts(ch.Preflight(), ch.Handshake(), client.Features()), nil
}

// remoteHostFactsIfAttached is the RemoteHostFacts seam: it looks host up
// through lookup (a non-dialing attached-only channel lookup) and answers the
// facts for client's own generation.
//
// A caller cancellation or deadline is the caller's own context ending, not
// host unavailability, so it is returned raw before the lookup runs — exactly
// as resolveClient/call leave it raw. Reporting it as the typed SessionUnavailable would fire the
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
// keeps attach from pairing one connection's dial with another's handshake;
// the preflight facts (remoteHostFactsForChannel) are pinned to the same client.
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
