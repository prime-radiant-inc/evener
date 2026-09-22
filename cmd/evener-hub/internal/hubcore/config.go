package hubcore

import (
	"context"
	"os"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/rendezvous"
)

// RelayLifecycleHooks are optional test seams the relay idle-retirement loop
// invokes to observe teardown. They are nil in production and set once via
// WebConfig at construction (never mutated after a relay goroutine starts), so
// each hub server instance carries its own — no shared global, no data race.
type RelayLifecycleHooks struct {
	IdleExit                   func(threadID string)
	AfterIdleDelete            func(threadID string)
	RetryWait                  func(context.Context, time.Duration) error
	AfterPlaceholder           func(threadID string)
	AfterReady                 func(threadID string)
	BeforeExistingRegistration func(threadID string)
	RegisterSubscription       func(context.Context, string, bool) bool
	BeforeSupervisor           func(threadID string)
	BeforeLaunchCommit         func(threadID string)
	BeforeCanonicalPublish     func(relayKey string, notification appwire.Notification)
	AfterCanonicalPublishEntry func(relayKey string, notification appwire.Notification)
}

// WebConfig is everything the web server needs.
type WebConfig struct {
	HubAddr                   string
	AuthToken                 string                   // capability token gating every non-exempt route
	MobileBaseURL             string                   // optional external origin used for mobile pairing QR codes
	HubStateRoot              string                   // root of hub-level machine state (auth-token, index.db, deletions/); defaults to cmdutil.DefaultStateRoot()
	LaunchConfigRoot          string                   // root of the layered launch config (launch.toml, projects/<id>/{launch.toml,meta.toml}); user-editable, so distinct from HubStateRoot — defaults to cmdutil.DefaultConfigRoot() when empty
	TranscriptDisplayStore    *TranscriptDisplayStore  // hub-authoritative Desktop/Mobile transcript-display defaults; nil → load from HubStateRoot
	TranscriptDisplayStoreErr error                    // diagnostic returned while loading the injected store; retained for startup diagnostics
	KeybindingsStore          *KeybindingsStore        // hub-authoritative user keybinding overrides; nil → load from HubStateRoot
	KeybindingsStoreErr       error                    // diagnostic returned while loading the injected store; retained for startup diagnostics
	DaemonProcesses           daemonprocess.Controller // nil selects verified native process operations
	RunDir                    string                   // run directory where rendezvous files live
	DaemonIdleTimeout         time.Duration            // configured idle-retirement deadline the Hub passes to spawned daemons; surfaced in settings
	PastIndexPath             string                   // path to the SQLite past-index DB, for display in settings
	Roster                    *Roster
	Past                      *PastIndex
	Spawner                   Spawner            // optional; nil disables spawn
	ResumeLocks               *ResumeLocks       // shared session ownership and recovery authority; web construction loads from HubStateRoot when nil
	DeletionStore             *DeletionStore     // host-authoritative deletion fences; production persists this under HubStateRoot
	PastPerPage               int                // results per page for /past; defaults to 50 when zero
	StateDir                  string             // root of the projects/<sha> state directory; needed for ForkSession
	CredsStore                *credentials.Store // credentials store; passed to auth controller
	PluginDirs                []string           // explicit plugin dirs; when empty, default to ~/.config/evener/plugins/*
	PluginRoot                string             // internal/plugins.Manager store root; "" → plugins.DefaultRoot() (~/.config/evener/plugins). Distinct from PluginDirs above: this is the marketplace/install registry root, not the explicit --plugin-dir scan list. Tests/sandboxes point this inside their own temp root so plugin/marketplace mutations never touch the real store.
	// PluginManager, when set, is the hub's own already-wired *plugins.Manager
	// for PluginRoot — constructed once per server by newWebServer, so the
	// appRPC server and every consumer reached through it (the plugin CRUD
	// handlers, a launch's plugin-inventory resolution via hubResolvePlugins,
	// and the three background maintenance paths in main_background.go:
	// hubStartUpgrade, seedHubMarketplaces, startHubPluginMaintenance's GC)
	// reaches the same Manager instead of a second, unwired one over a
	// possibly different root. Each WebConfig value carries its own — never a
	// package global — so two servers in one process never answer for each
	// other. nil falls back to a fresh plugins.NewManager(PluginRoot); every
	// test that never builds a server leaves this nil and gets that fallback.
	PluginManager       *plugins.Manager
	MCPConfigPath       string            // MCP config file path; when empty, default to ~/.config/evener/mcp.json
	Registry            *ProviderRegistry // live provider registry; the instance, auth, credential-test and model surfaces all read it
	ProvidersConfigPath string            // path to providers.toml; the instances pane is its only writer
	CredentialsPath     string            // path to credentials.toml; handed to every spawned child as EVENER_CREDENTIALS_CONFIG
	NoUserLayer         bool              // EVENER_PROVIDERS_CONFIG is present and empty: no user layer at all (spec §10). A file that fails to load adds to this per call; it is not folded in here.
	APILogDefault       bool              // hub.toml api_log floor for hub-spawned daemons; applied when no launch layer sets api_log

	Archive     *ArchiveStore    // archive decision store; nil when not configured (tree uses empty decisions)
	Favorite    *FavoriteStore   // favorite decision store; nil when not configured
	PinSections *PinSectionStore // named pin-section store; nil when not configured

	Inputs *InputsVersion // shared inputs-version counter; nil in tests (memo treats as version 0)

	// RemoteThreadCache holds the last-refreshed remote-source thread list so
	// the tree read path never blocks on a network hop. Nil in tests, which
	// fall back to the old synchronous walk (see remoteTreeThreads).
	RemoteThreadCache *RemoteThreadCache

	// RemoteHosts are the validated [[hosts]] entries (component 03) in
	// config order. newHubSourceRegistry registers one
	// appsource.RemoteHubSource per entry.
	RemoteHosts []hostreg.Host
	// RemoteHostRegistry is the controller's live host registry: the one
	// *hostreg.Registry the SSH manager dials through (sshconn.New), the
	// attach handler validates against, and the host-management surface
	// (evener/host/add|list|status|remove|update) mutates — one shared instance, so
	// a host added at runtime is attachable without a restart. nil (tests,
	// embedders) makes the constructors' fallback build one registry and
	// share it across every surface: the SSH manager's own registry when a
	// manager is threaded (the instance its dial paths and AddHost/RemoveHost
	// mutate), else a fresh one built from RemoteHosts.
	RemoteHostRegistry *hostreg.Registry
	// RemoteHostSSHManager owns the live SSH channels for the configured
	// hosts. The host-management surface wires it in so Remove tears the
	// removed host's channel down through the manager's atomic RemoveHost
	// rather than leaving a supervisor or channel behind. nil leaves
	// host-management removal without channel teardown (tests).
	RemoteHostSSHManager *sshconn.Manager
	// RemoteHostConfigPath is the selected hub.toml path. The host-management
	// surface persists its UI-added hosts in a sidecar beside this file; empty
	// disables sidecar persistence (the surface stays memory-only).
	RemoteHostConfigPath string
	// RemoteHostClient returns an attached, initialized AppWire client for a
	// remote host, attaching over SSH on first use (component 04). nil
	// disables remote hosts (tests).
	RemoteHostClient func(ctx context.Context, host string) (*appwire.Client, error)
	// RemoteHostClientIfAttached returns the current attached AppWire client for
	// a remote host ONLY while a live channel is installed, without dialing
	// (component 04's Manager.ClientIfAttached). The non-explicit read paths —
	// every RemoteHubSource call, the empty-filter thread/list fan-out, and the
	// background snapshot — resolve through this seam rather than
	// RemoteHostClient, so none can eagerly attach a dormant host or re-dial one
	// that dropped. nil leaves those paths on the dialing RemoteHostClient
	// (tests).
	RemoteHostClientIfAttached func(host string) (*appwire.Client, bool)
	// RemoteHostFacts returns the component-04 preflight facts (protocol
	// version, hub version, OS/arch, advertised features) for the AppWire
	// connection behind client, the exact generation the probe resolved for the
	// capability probe. An implementation must answer from that same generation
	// rather than racing a reconnect: it reads one
	// sshconn.Manager.ChannelIfAttached value and refuses (a typed
	// SessionUnavailable) when that channel's client is not client, so a probe
	// can never cache a snapshot assembled from two connections. The capability
	// probe combines the facts with its AppWire reads. nil leaves the
	// preflight-owned fields zero-valued (tests).
	RemoteHostFacts func(ctx context.Context, host string, client *appwire.Client) (appsource.HostFacts, error)
	// RemoteHostHandshake returns the attach handshake facts (ProtocolVersion,
	// ServerInfo, SourceID, Features) captured when a remote host's channel
	// attached, ONLY while a live channel is installed, without dialing. In
	// production it is a closure over component 04's Manager.ChannelIfAttached
	// with the client-identity guard at the call site, not
	// Manager.HandshakeIfAttached (which has no production caller). It takes
	// client — the exact generation the probe resolved — and reports false when
	// the installed channel is a different one, so the probe cannot pair one
	// connection's wire reads with another's handshake. nil leaves those fields
	// zero-valued (tests).
	RemoteHostHandshake func(host string, client *appwire.Client) (appwire.InitializeResponse, bool)
	// RemoteHostOnline reports whether the controller's channel to host is
	// currently attached (component 06). Nil leaves every remote host online
	// (tests).
	RemoteHostOnline func(host string) bool

	// PokeAttention nudges the hub's attention watcher to recompute
	// immediately (e.g. after an archive decision changes tier eligibility)
	// instead of waiting for its next tick. Nil when the watcher isn't wired
	// (e.g. in tests that construct a WebServer directly).
	PokeAttention func()

	RelayHooks RelayLifecycleHooks // test-only relay lifecycle seams; nil in production

	// Sandbox seams. Each is nil in production (the real implementation runs);
	// a fuzz/test sandbox sets them so the matching handler runs without
	// shelling out, hitting the network, or mutating the real filesystem. These
	// are the escapes a read-only harness cannot drive: the git-head AppWire
	// method, the live-provider model query, and the directory creator. See
	// cmd/evener-hub's sandbox_test.go.
	ResolveGitHead   func(ctx context.Context, dir string) (string, error) // nil → real `git`
	ResolveGitOrigin func(ctx context.Context, dir string) (string, error) // nil → real `git`
	LiveModels       func(ctx context.Context) []appwire.ModelDescriptor   // nil → real provider query
	MkdirAll         func(path string, perm os.FileMode) error             // nil → os.MkdirAll
}

// Spawner forks a evener serve subprocess and waits for its rendezvous file to appear.
// Returns the discovered Entry on success.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error)
	Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error)
}

// SpawnRequest carries the per-spawn knobs passed directly from the caller.
type SpawnRequest struct {
	Project       identifier.Project
	Resolved      launchconfig.Resolved
	WorkingDir    string
	StateDir      string
	RunDir        string
	PluginRoot    string // internal/plugins.Manager root handed to the child serve process; "" keeps the child's default root resolution
	AgentsDocPath string // personal AGENTS.md handed to the child serve process; "" lets the child resolve it from its own environment
	AppReplaySize int
	Env           []string // populated by ToEnv during Spawn
	Provider      string   // instance the launch selected; gated against the registry before spawning
}

// ResumeRequest carries the resolved state needed to resume a saved session.
type ResumeRequest struct {
	Project       identifier.Project
	SessionID     string
	WorkingDir    string
	StateDir      string
	Resolved      launchconfig.Resolved
	RunDir        string
	AgentsDocPath string // personal AGENTS.md handed to the child serve process; "" lets the child resolve it from its own environment
	AppReplaySize int
	Env           []string // populated by ToEnv during Resume
	Provider      string   // instance the launch selected; gated against the registry before spawning

	// CompletionOwned is set only by explicit thread/resume. Automatic resume
	// retains the configured startup budget; explicit restore awaits readiness,
	// child exit, or caller/Stop cancellation instead of guessing its duration.
	CompletionOwned bool
	ActiveResume    *ActiveResume // hub-owned launch lifetime; never serialized on AppWire
}

// DaemonTarget is the daemon a rendezvous entry names, as the process verifier
// wants it: the session id (the thread id for an entry that carries none),
// the state directory whose API log the daemon holds, and the start the
// process must not postdate.
func DaemonTarget(entry rendezvous.Entry) daemonprocess.Target {
	return daemonprocess.Target{
		PID:       entry.PID,
		SessionID: envvars.FirstNonEmpty(entry.SessionID, entry.ThreadID),
		StateDir:  entry.StateDir,
		StartedAt: entry.StartedAt,
	}
}
