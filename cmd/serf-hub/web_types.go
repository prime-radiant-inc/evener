package main

import (
	"encoding/json"
	"html/template"
	"sync"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/hubapi"
)

// modelDescriptor is a provider/model pair used by the spawn chip and /api/models.
type modelDescriptor struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

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
	Name     string
	Command  string
	Args     []string
	Status   string // "running" | "stopped" | "error" | "unknown"
	Tools    int
	Agents   []string
	EditPath template.URL
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

type replayHeader struct {
	SessionID string `json:"session_id"`
	ProfileID string `json:"profile_id"`
	Model     string `json:"model"`
}

type replayEntry struct {
	Turn replayTurn `json:"turn"`
}

type replayTurn struct {
	Kind    string        `json:"kind"`
	Message replayMessage `json:"message"`
}

type replayMessage struct {
	Role    string       `json:"role"`
	Content []replayPart `json:"content"`
}

type replayPart struct {
	Kind       string            `json:"kind"`
	Text       string            `json:"text,omitempty"`
	Image      *replayImage      `json:"image,omitempty"`
	ToolCall   *replayToolCall   `json:"tool_call,omitempty"`
	ToolResult *replayToolResult `json:"tool_result,omitempty"`
}

type replayImage struct {
	Data      []byte `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Name      string `json:"name,omitempty"`
}

type replayToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type replayToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    any    `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
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

// modelsCache holds a per-process cache of live ListModels results keyed by
// provider name, with a TTL. Provider /models calls are cheap but not free.
type modelsCache struct {
	mu      sync.Mutex
	expires time.Time
	models  []map[string]any
}

var liveModelsCache modelsCache

const liveModelsTTL = 5 * time.Minute

// WorkspaceData is the template data for the workspace partial.
type WorkspaceData struct {
	ID             string
	SourceLabel    string
	Title          string
	Branch         string
	WorkingDir     string
	HomeDir        string
	State          string
	StateLabel     string
	TurnCount      int
	Model          string
	ContextWindow  int
	ContextPercent int
	ContextNumbers string
	Cost           string
	ActiveTurnID   string
	RunningFor     string
	Capabilities   hubapi.SessionCapabilities
	// Fork lineage for the preserved-original side of a fork. Non-empty
	// only when this session's meta carries ForkLabel — i.e., it's the
	// dim, snapshotted original. ForkOfTitle is the title of the new
	// branch (the session whose ParentSessionID == this.ID); empty if the
	// new branch is not in the past index.
	ForkLabel      string
	ForkOfTitle    string
	DivergenceTurn int
}

// sendRequest is the JSON body accepted by POST /s/<id>/send. Items carries
// Codex-style input parts; image entries carry their bytes as a base64-encoded
// `data` field that Go's json unmarshals into `[]byte` automatically.
type sendRequest struct {
	Text  string              `json:"text"`
	Items []appwire.InputItem `json:"items,omitempty"`
}

// Per-request limits for image attachments. Match the browser-side cap so
// REST and AppWire accept the same image payload surface.
const (
	sendMaxImageItems   = 8
	sendMaxImageBytes   = 8 * 1024 * 1024  // per-image
	sendMaxRequestBytes = 96 * 1024 * 1024 // total request body
)

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
