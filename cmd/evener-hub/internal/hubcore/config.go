package hubcore

import (
	"context"
	"os"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/credentials"
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
	AuthToken                 string                  // capability token gating every non-exempt route
	MobileBaseURL             string                  // optional external origin used for mobile pairing QR codes
	HubStateRoot              string                  // root of hub-level machine state (auth-token, index.db, deletions/); defaults to cmdutil.DefaultStateRoot()
	LaunchConfigRoot          string                  // root of the layered launch config (launch.toml, projects/<id>/{launch.toml,meta.toml}); user-editable, so distinct from HubStateRoot — defaults to cmdutil.DefaultConfigRoot() when empty
	TranscriptDisplayStore    *TranscriptDisplayStore // hub-authoritative Desktop/Mobile transcript-display defaults; nil → load from HubStateRoot
	TranscriptDisplayStoreErr error                   // diagnostic returned while loading the injected store; retained for startup diagnostics
	KeybindingsStore          *KeybindingsStore       // hub-authoritative user keybinding overrides; nil → load from HubStateRoot
	KeybindingsStoreErr       error                   // diagnostic returned while loading the injected store; retained for startup diagnostics
	RunDir                    string                  // run directory where rendezvous files live
	PastIndexPath             string                  // path to the SQLite past-index DB, for display in settings
	Roster                    *Roster
	Past                      *PastIndex
	Spawner                   Spawner            // optional; nil disables spawn
	ResumeLocks               *ResumeLocks       // per-session resume serialization shared by the REST and RPC paths; nil → each path falls back to its own lock
	DeletionStore             *DeletionStore     // host-authoritative deletion fences; production persists this under HubStateRoot
	PastPerPage               int                // results per page for /past; defaults to 50 when zero
	StateDir                  string             // root of the projects/<sha> state directory; needed for ForkSession
	CredsStore                *credentials.Store // credentials store; passed to auth controller
	PluginDirs                []string           // explicit plugin dirs; when empty, default to ~/.config/evener/plugins/*
	PluginRoot                string             // internal/plugins.Manager store root; "" → plugins.DefaultRoot() (~/.config/evener/plugins). Distinct from PluginDirs above: this is the marketplace/install registry root, not the explicit --plugin-dir scan list. Tests/sandboxes point this inside their own temp root so plugin/marketplace mutations never touch the real store.
	MCPConfigPath             string             // MCP config file path; when empty, default to ~/.config/evener/mcp.json
	Registry                  *ProviderRegistry  // live provider registry; the instance, auth, credential-test and model surfaces all read it
	ProvidersConfigPath       string             // path to providers.toml; the instances pane is its only writer
	CredentialsPath           string             // path to credentials.toml; handed to every spawned child as EVENER_CREDENTIALS_CONFIG
	NoUserLayer               bool               // EVENER_PROVIDERS_CONFIG is present and empty: no user layer at all (spec §10). A file that fails to load adds to this per call; it is not folded in here.

	Archive     *ArchiveStore    // archive decision store; nil when not configured (tree uses empty decisions)
	Favorite    *FavoriteStore   // favorite decision store; nil when not configured
	PinSections *PinSectionStore // named pin-section store; nil when not configured

	Inputs *InputsVersion // shared inputs-version counter; nil in tests (memo treats as version 0)

	// RemoteThreadCache holds the last-refreshed remote-source thread list so
	// the tree read path never blocks on a network hop. Nil in tests, which
	// fall back to the old synchronous walk (see remoteTreeThreads).
	RemoteThreadCache *RemoteThreadCache

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
	ResolveGitHead func(ctx context.Context, dir string) (string, error) // nil → real `git`
	LiveModels     func(ctx context.Context) []appwire.ModelDescriptor   // nil → real provider query
	MkdirAll       func(path string, perm os.FileMode) error             // nil → os.MkdirAll
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
	AppReplaySize int
	Env           []string // populated by ToEnv during Resume
	Provider      string   // instance the launch selected; gated against the registry before spawning
}
