package main

import (
	"html/template"
	"sync"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/hubapi"
)

// searchResult is one item in the /api/search response.
type searchResult struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	State   string `json:"state"`
	Age     string `json:"age"`
}

// searchResponse is the JSON envelope returned by /api/search.
type searchResponse struct {
	Live []searchResult `json:"live"`
	Past []searchResult `json:"past"`
}

// agentDisplay is one row in Settings → Agents.
type agentDisplay struct {
	Name     string
	EditPath template.URL
}

// pluginCounts summarises components contributed by a plugin.
type pluginCounts struct {
	Skills int
	Agents int
	Mcps   int
	Hooks  int
}

// pluginDisplay is one row in Settings → Plugins.
type pluginDisplay struct {
	Name     string
	Path     string
	Version  string
	Counts   pluginCounts
	EditPath template.URL
}

// skillDisplay is one row in Settings → Skills.
type skillDisplay struct {
	Name        string
	Plugin      string
	Description string
	EditPath    template.URL
}

// mcpDisplay is one row in Settings → MCP servers.
type mcpDisplay struct {
	Name      string
	Command   string
	Args      []string
	Transport string // "stdio", "sse", or "http" — from mcpprobe.Result.Transport
	Status    string // "available" | "unreachable" | "missing" — from mcpprobe.Result.Status
	Error     string // populated whenever Status isn't "available"; empty otherwise
	Tools     int
	Agents    []string
	EditPath  template.URL
}

// settingsData is the template data passed to all settings section templates.
type settingsData struct {
	Active            string
	HubAddr           string
	RunDir            string
	StateDir          string
	SpawnTimeout      string
	PastPerPage       int
	Providers         []providerDisplay
	ModelDiagnostics  []appwire.ModelListDiagnostic
	Agents            []agentDisplay
	Plugins           []pluginDisplay
	PluginsError      string
	Skills            []skillDisplay
	Mcps              []mcpDisplay
	McpsError         string
	McpConfigPath     string
	PastCount         int
	PastIndexPath     string // path to past index SQLite file (e.g. ~/.serf/index.db)
	PastIndexSize     string // human-readable size of the past index file, empty if unavailable
	BearerTokenAge    string // human-readable age of the auth token file, empty if unavailable
	HubVersion        string // Version constant (e.g. "0.1.0")
	HubCommit         string // git commit hash injected at build time, empty in dev builds
	CodexLaunches     []codexlaunch.CodexLaunchConfig
	ProjectCWD        string            // canonical cwd for the per-project settings page
	AvailableProjects []projectListItem // known projects shown when ProjectCWD is empty
}

// projectListItem is one row in the project picker on the /settings/project page.
type projectListItem struct {
	CWD  string
	Name string
}

// providerDisplay groups model descriptors by provider for the providers page.
type providerDisplay struct {
	Name   string
	Models []string
}

// spawnViewData is the template data for the spawn partial.
type spawnViewData struct {
	DefaultModel           string
	DefaultModelValue      string
	DefaultHarness         string
	DefaultWorkingDir      string
	DefaultWorkingDirValue string
	DefaultBranch          string
	DefaultBranchValue     string
	DefaultAccessMode      string
	DefaultPrompt          string // optional ?prompt= pre-fill
	RecentPrompts          []string
	Harnesses              []launchHarness
	SafeEnv                map[string]string
}

type launchHarness struct {
	ID    string
	Label string
}

// spawnRequest is the JSON body for POST /api/spawn. Items
// carries optional attachments (e.g. image bytes) that the composer wants
// to include with the initial user turn (kata t5j6).
type spawnRequest struct {
	Prompt          string                     `json:"prompt"`
	Harness         string                     `json:"harness"`
	Model           string                     `json:"model"`
	WorkingDir      string                     `json:"working_dir"`
	Branch          string                     `json:"branch"`
	AccessMode      string                     `json:"access_mode"`
	Agent           string                     `json:"agent"`
	ReasoningEffort string                     `json:"reasoning_effort"`
	NonInteractive  *bool                      `json:"non_interactive,omitempty"`
	LaunchOverrides *appwire.LaunchConfigLayer `json:"launch_overrides,omitempty"`
	Items           []appwire.InputItem        `json:"items,omitempty"`
}

// modelsCache is a per-WebServer TTL cache of the RAW live model list (all
// providers' ListModels results, un-overlaid — see overlayLiveEntries).
// Provider /models calls are cheap but not free.
type modelsCache struct {
	mu      sync.Mutex
	expires time.Time
	models  []map[string]any
}

const liveModelsTTL = 5 * time.Minute

// WorkspaceData is the template data for the workspace partial.
type WorkspaceData struct {
	ID                 string
	SourceLabel        string
	Title              string
	Branch             string
	WorkingDir         string
	HomeDir            string
	State              string
	StateLabel         string
	TurnCount          int
	Model              string
	ContextWindow      int
	ContextPercent     int
	ContextNumbers     string
	Cost               string
	ActiveTurnID       string
	RunningFor         string
	ShowSidebarToggle  bool
	ThreadDocumentMode bool
	// GoalStatus/GoalIterations mirror appwire.GoalState for the live goal
	// status pill in the input strip. Empty/zero when no goal is set (e.g. past
	// sessions). There is no iteration cap, so only status and turn count show.
	GoalStatus     string
	GoalIterations int
	Capabilities   hubapi.SessionCapabilities
	// Fork lineage for the preserved-original side of a fork. Non-empty
	// only when this session's meta carries ForkLabel — i.e., it's the
	// dim, snapshotted original. ForkOfTitle is the title of the new
	// branch (the session whose ParentSessionID == this.ID); empty if the
	// new branch is not in the past index.
	ForkLabel      string
	ForkOfTitle    string
	DivergenceTurn int
	// Subagent lineage for the breadcrumb banner (mockup #9). Non-empty only
	// when this session is a subagent with a known parent. ParentRouteID is the
	// /s/<id> route to the parent's workspace; ParentTitle is its display name.
	// The banner gives a subagent a way back to its parent — without it,
	// "view →" was a one-way hard nav with no back-out.
	ParentRouteID string
	ParentTitle   string
	// ObserverRouteIDs are the /s/<id> route ids of this worker's LIVE observer
	// subagents (sessions running a job_watch sidecar on this one). The agent
	// stamps them on the worker's meta at watch-install time (SessionMeta.
	// ObservedBy); workspaceData filters that to the live set. The template
	// renders them as data-observers on #conversation so the renderer can
	// auto-open each observer beside this worker. Local sources only — remote/
	// codex threads have no jobstore and so never carry observers.
	ObserverRouteIDs []string
}

// sendRequest is the JSON body accepted by POST /s/<id>/send. Items carries
// Codex-style input parts; image entries carry their bytes as a base64-encoded
// `data` field that Go's json unmarshals into `[]byte` automatically.
type sendRequest struct {
	Text  string              `json:"text"`
	Items []appwire.InputItem `json:"items,omitempty"`
}

// steerRequest is the JSON body for POST /s/<id>/steer.
type steerRequest struct {
	Text   string `json:"text"`
	TurnID string `json:"turn_id"`
}

type sessionActionRequest struct {
	TurnID string `json:"turn_id"`
}

// queueRequest is the JSON body for POST /s/<id>/queue (kata 111a). Queues a
// user message to be drained as a fresh user turn after the active turn
// completes; the daemon rejects the call when no turn is in flight. Items
// (kata v80q) carry optional image attachments alongside the text — the
// daemon's TurnQueue handler routes them through queueWithImagesFunc so
// the eventual drained user turn preserves the image bytes.
type queueRequest struct {
	Text  string              `json:"text"`
	Items []appwire.InputItem `json:"items,omitempty"`
}

// drainAsSteerRequest is the JSON body accepted by POST /s/<id>/drain-as-steer.
// All fields are optional — a body-less request matches the kata 0bq1
// classic drain shape. Text/Items ride on turn/drainAsSteer so the daemon
// atomically appends the composer payload and drains the queue.
type drainAsSteerRequest struct {
	Text  string              `json:"text,omitempty"`
	Items []appwire.InputItem `json:"items,omitempty"`
}

type forkRequest struct {
	Turn          int    `json:"turn"`
	EditedMessage string `json:"edited_message"`
	Label         string `json:"label"`
}

// daemonStatus is the subset of /status fields the hub cares about.
type daemonStatus struct {
	SessionID        string  `json:"session_id"`
	Model            string  `json:"model"`
	Profile          string  `json:"profile"`
	State            string  `json:"state"`
	Turns            int     `json:"turns"`
	WorkingDir       string  `json:"working_dir,omitempty"`
	ContextPressure  float64 `json:"context_pressure"`
	ContextUsed      int     `json:"context_used,omitempty"`
	ContextWindow    int     `json:"context_window,omitempty"`
	ContextRemaining int     `json:"context_remaining,omitempty"`
}
