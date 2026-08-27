package hubapi

import "time"

// MobileAPIVersion is the REST contract version implemented by the dedicated
// mobile client. Pairing requires an exact match with HealthResponse.
const MobileAPIVersion = 1

// HealthResponse is returned by GET /api/health.
type HealthResponse struct {
	Version          string             `json:"version"`
	MobileAPIVersion int                `json:"mobile_api_version"`
	StartedAt        time.Time          `json:"started_at"`
	HubAddr          string             `json:"hub_addr"`
	RunDir           string             `json:"run_dir,omitempty"`
	StateGlob        string             `json:"state_glob,omitempty"`
	BackendGitSha    string             `json:"backend_git_sha,omitempty"`
	FrontendHash     string             `json:"frontend_hash,omitempty"`
	Capabilities     HealthCapabilities `json:"capabilities"`
}

type HealthCapabilities struct {
	TranscriptFollow bool `json:"transcript_follow"`
	Spawn            bool `json:"spawn"`
	Fork             bool `json:"fork"`
	RemoteSources    bool `json:"remote_sources"`
}

// AttentionSummary is the authoritative badge count set: how many live,
// top-level, not-manually-archived sessions need attention, are erroring, or
// are working — the NeedsYou tier's eligibility set (only an explicit archive
// decision suppresses; age never decays attention). Notification clients
// (notifications.js) drive the tab title and favicon badge from this on
// baseline load. It mirrors
// appwire.AttentionSummary's shape as a parallel wire type — hubapi is the
// REST client surface and deliberately does not import appwire — including
// its camelCase tags: this is the same "summary" object evener/attention/changed
// pushes incrementally (appwire.AttentionChangedPayload.Summary), so the JS
// layer applies one field-access path to either the REST baseline or the live
// notification.
type AttentionSummary struct {
	NeedsYou int `json:"needsYou"` //nolint:tagliatelle // camelCase: see AttentionSummary's doc
	Error    int `json:"error"`
	Working  int `json:"working"`
}

type Source struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Online bool   `json:"online"`
}

// PinSection is one named group returned by GET /api/pin-sections.
type PinSection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"member_count"`
}

type SessionPinAssignment struct {
	SessionRef string     `json:"session_ref"`
	Section    PinSection `json:"section"`
}

type SessionPinMutationResponse struct {
	OK         bool                 `json:"ok"`
	Changed    bool                 `json:"changed"`
	Assignment SessionPinAssignment `json:"assignment"`
}

// SessionDetail is returned by GET /api/sessions/{ref}.
type SessionDetail struct {
	Ref              string  `json:"ref"`
	HostID           string  `json:"host_id"`
	SessionID        string  `json:"session_id"`
	Title            string  `json:"title"`
	State            string  `json:"state"`
	Live             bool    `json:"live"`
	Project          string  `json:"project"`
	WorkingDir       string  `json:"working_dir,omitempty"`
	Branch           string  `json:"branch,omitempty"`
	Model            string  `json:"model,omitempty"`
	Profile          string  `json:"profile,omitempty"`
	TurnCount        int     `json:"turn_count"`
	ActiveTurnID     string  `json:"active_turn_id,omitempty"`
	ContextPressure  float64 `json:"context_pressure"`
	ContextUsed      int     `json:"context_used,omitempty"`
	ContextWindow    int     `json:"context_window,omitempty"`
	ContextRemaining int     `json:"context_remaining,omitempty"`
	// WorkMillis is the session's accumulated wall-clock work time in
	// milliseconds; ActiveTurnStartedAt is the Unix epoch-milliseconds timestamp
	// the current in-flight turn began, 0 when idle/ended (WS2).
	WorkMillis          int64 `json:"work_millis,omitempty"`
	ActiveTurnStartedAt int64 `json:"active_turn_started_at,omitempty"`
	// FailedToolCalls mirrors appwire.EvenerThread.FailedToolCalls: how many of
	// this session's tool calls failed, or absent when nothing counted them
	// (an unreadable transcript, or a source that never derives the figure).
	// Kept as a pointer, not a plain int, so a measured zero and "nobody
	// counted" stay distinguishable on the wire — collapsing them would let a
	// producer that forgets to populate the field silently read as a clean
	// session instead of an uncounted one.
	FailedToolCalls *int   `json:"failed_tool_calls,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	DivergenceTurn  int    `json:"divergence_turn,omitempty"`
	ForkLabel       string `json:"fork_label,omitempty"`
	IsSubagent      bool   `json:"is_subagent"`
	// GoalStatus/GoalIterations mirror appwire.GoalState (status + continuation
	// turn count) when a /goal is set on a live session, else empty/zero. Kept
	// flattened so hubapi need not depend on appwire. There is no iteration cap,
	// so only the status and turn count are surfaced.
	GoalStatus     string `json:"goal_status,omitempty"`
	GoalIterations int    `json:"goal_iterations,omitempty"`
	// Usage mirrors appwire.EvenerUsage's cumulative self-only token totals
	// (WS2), flattened into hubapi's own Usage type for the same reason as
	// GoalStatus above — hubapi cannot depend on appwire. Nil when no token
	// data is available.
	Usage        *Usage              `json:"usage,omitempty"`
	Capabilities SessionCapabilities `json:"capabilities"`
}

// Usage is hubapi's flattened mirror of appwire.EvenerUsage — a session's
// cumulative self-only token totals. See SessionDetail.Usage.
type Usage struct {
	InputTokens     int64 `json:"input_tokens,omitempty"`
	OutputTokens    int64 `json:"output_tokens,omitempty"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
	TotalTokens     int64 `json:"total_tokens,omitempty"`
}

type SessionCapabilities struct {
	Send        bool `json:"send"`
	Steer       bool `json:"steer"`
	Interrupt   bool `json:"interrupt"`
	Compact     bool `json:"compact"`
	Clear       bool `json:"clear"`
	Fork        bool `json:"fork"`
	Resume      bool `json:"resume"`
	Shutdown    bool `json:"shutdown"`
	ChangeModel bool `json:"change_model"`
	// Queue mirrors appwire.ThreadCapabilities.Queue (kata 111a). True when
	// the daemon will accept turn/queue while a turn is in flight; gates the
	// composer's queue affordance on the web UI.
	Queue          bool   `json:"queue"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
}

type ModelOption struct {
	Provider             string  `json:"provider"`
	Model                string  `json:"model"`
	DisplayName          string  `json:"display_name,omitempty"`
	ContextWindow        int     `json:"context_window,omitempty"`
	SupportsTools        bool    `json:"supports_tools,omitempty"`
	SupportsReasoning    bool    `json:"supports_reasoning,omitempty"`
	InputCostPerMillion  float64 `json:"input_cost_per_million,omitempty"`
	OutputCostPerMillion float64 `json:"output_cost_per_million,omitempty"`
}

// SpawnRequest is the JSON body for POST /api/spawn.
// Prompt is the current field; Task is accepted for legacy callers only.
type SpawnRequest struct {
	Prompt          string `json:"prompt,omitempty"`
	Task            string `json:"task,omitempty"` // Deprecated: use Prompt.
	Harness         string `json:"harness,omitempty"`
	Model           string `json:"model,omitempty"`
	WorkingDir      string `json:"working_dir,omitempty"`
	Agent           string `json:"agent,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type SpawnResponse struct {
	Ref       string `json:"ref"`
	HostID    string `json:"host_id"`
	SessionID string `json:"session_id"`
}

type RefResponse = SpawnResponse

type ForkRequest struct {
	Turn          int    `json:"turn"`
	EditedMessage string `json:"edited_message"`
	Label         string `json:"label"`
	// DeferInput forks at the turn WITHOUT appending a replacement message:
	// the child holds only the entries before the turn and the turn's
	// original text comes back in ForkResponse.OriginalInput so the caller
	// can stage it for editing and explicit submission (issue #42).
	// Mutually exclusive with EditedMessage.
	DeferInput bool `json:"defer_input,omitempty"`
}

// ForkResponse is the JSON response for POST /api/sessions/<id>/fork. It
// carries the child ref plus, for deferred-input forks, the source turn's
// original user text.
type ForkResponse struct {
	Ref           string `json:"ref"`
	HostID        string `json:"host_id"`
	SessionID     string `json:"session_id"`
	OriginalInput string `json:"original_input,omitempty"`
}

type ErrorResponse struct {
	Error           string `json:"error"`
	Code            int    `json:"code,omitempty"`
	EvenerErrorInfo string `json:"evener_error_info,omitempty"`
}
