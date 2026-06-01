package hubcore

import (
	"context"

	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/launchconfig"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"
)

// RelayLifecycleHooks are optional test seams the relay idle-retirement loop
// invokes to observe teardown. They are nil in production and set once via
// WebConfig at construction (never mutated after a relay goroutine starts), so
// each hub server instance carries its own — no shared global, no data race.
type RelayLifecycleHooks struct {
	IdleExit        func(threadID string)
	AfterIdleDelete func(threadID string)
}

// WebConfig is everything the web server needs.
type WebConfig struct {
	HubAddr             string
	AuthToken           string // capability token gating every non-exempt route
	HubStateRoot        string // root of hub-level state; defaults to $HOME/.serf
	RunDir              string // run directory where rendezvous files live
	PastIndexPath       string // path to the SQLite past-index DB, for display in settings
	Roster              *Roster
	Past                *PastIndex
	Spawner             Spawner             // optional; nil disables spawn
	Models              []ModelDescriptor   // available models for the spawn chip
	PastPerPage         int                 // results per page for /past; defaults to 50 when zero
	StateDir            string              // root of the projects/<sha> state directory; needed for ForkSession
	CredsStore          *credentials.Store  // credentials store; passed to auth controller
	PluginDirs          []string            // explicit plugin dirs; when empty, default to ~/.config/serf/plugins/*
	MCPConfigPath       string              // MCP config file path; when empty, default to ~/.config/serf/mcp.json
	ProviderConfig      *providercfg.Config // instance-to-tag mapping; nil when providers.toml absent (env path)
	ProvidersConfigPath string              // path to providers.toml; forwarded to the auth controller
	CodexSources        []appsource.CodexSourceConfig
	CodexLaunches       []codexlaunch.CodexLaunchConfig
	CodexLauncher       *codexlaunch.CodexLauncher

	RelayHooks RelayLifecycleHooks // test-only relay lifecycle seams; nil in production
}

// Spawner forks a serf serve subprocess and waits for its rendezvous file to appear.
// Returns the discovered Entry on success.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) (rendezvous.Entry, error)
	Resume(ctx context.Context, req ResumeRequest) (rendezvous.Entry, error)
}

// SpawnRequest carries the per-spawn knobs passed directly from the caller.
type SpawnRequest struct {
	Resolved      launchconfig.Resolved
	WorkingDir    string
	StateDir      string
	RunDir        string
	AppReplaySize int
	Env           []string // populated by ToEnv during Spawn
	Provider      string   // for credential injection
}

// ResumeRequest carries the resolved state needed to resume a saved session.
type ResumeRequest struct {
	SessionID     string
	WorkingDir    string
	StateDir      string
	Resolved      launchconfig.Resolved
	RunDir        string
	AppReplaySize int
	Env           []string // populated by ToEnv during Resume
	Provider      string   // for credential injection
}
