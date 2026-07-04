package hubapi

import "time"

// HealthResponse is returned by GET /api/health.
type HealthResponse struct {
	Version      string             `json:"version"`
	StartedAt    time.Time          `json:"started_at"`
	HubAddr      string             `json:"hub_addr"`
	RunDir       string             `json:"run_dir,omitempty"`
	StateGlob    string             `json:"state_glob,omitempty"`
	Capabilities HealthCapabilities `json:"capabilities"`
}

type HealthCapabilities struct {
	Tree             bool `json:"tree"`
	TranscriptFollow bool `json:"transcript_follow"`
	SpawnSchema      bool `json:"spawn_schema"`
	Spawn            bool `json:"spawn"`
	Fork             bool `json:"fork"`
	RemoteSources    bool `json:"remote_sources"`
}

// TreeResponse is returned by GET /api/tree.
type TreeResponse struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Sources     []Source      `json:"sources"`
	Live        []TreeNode    `json:"live"`
	Projects    []TreeProject `json:"projects"`
	// serf:naming-ignore
	AttentionSummary AttentionSummary `json:"attentionSummary"` // camelCase: see AttentionSummary's doc
}

// AttentionSummary is the authoritative badge count set: how many live,
// top-level, not-manually-archived sessions need attention, are erroring, or
// are working — the NeedsYou tier's eligibility set (only an explicit archive
// decision suppresses; age never decays attention). Notification clients
// (notifications.js) drive the tab title and favicon badge from this on
// baseline load. It mirrors
// hubcore.AttentionSummary's shape as a parallel wire type — hubapi cannot
// import the hub's internal package — including its camelCase tags: this is
// the same "summary" object serf/attention/changed pushes incrementally
// (hubcore.AttentionChangedPayload.Summary), so the JS layer applies one
// field-access path to either the REST baseline or the live notification.
type AttentionSummary struct {
	// serf:naming-ignore
	NeedsYou int `json:"needsYou"`
	Error    int `json:"error"`
	Working  int `json:"working"`
}

type Source struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Online bool   `json:"online"`
}

type TreeProject struct {
	Key         string     `json:"key"`
	Name        string     `json:"name"`
	WorkingDir  string     `json:"working_dir,omitempty"`
	RollupState string     `json:"rollup_state,omitempty"`
	Sessions    []TreeNode `json:"sessions"`
}

type TreeNode struct {
	RowID     string     `json:"row_id"`
	Ref       string     `json:"ref"`
	HostID    string     `json:"host_id"`
	SessionID string     `json:"session_id"`
	Title     string     `json:"title"`
	Project   string     `json:"project"`
	State     string     `json:"state"`
	Kind      string     `json:"kind"`
	Live      bool       `json:"live"`
	UpdatedAt time.Time  `json:"updated_at,omitempty"`
	Age       string     `json:"age,omitempty"`
	Model     string     `json:"model,omitempty"`
	Children  []TreeNode `json:"children,omitempty"`
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
	ParentSessionID  string  `json:"parent_session_id,omitempty"`
	DivergenceTurn   int     `json:"divergence_turn,omitempty"`
	ForkLabel        string  `json:"fork_label,omitempty"`
	IsSubagent       bool    `json:"is_subagent"`
	// GoalStatus/GoalIterations mirror appwire.GoalState (status + continuation
	// turn count) when a /goal is set on a live session, else empty/zero. Kept
	// flattened so hubapi need not depend on appwire. There is no iteration cap,
	// so only the status and turn count are surfaced.
	GoalStatus     string              `json:"goal_status,omitempty"`
	GoalIterations int                 `json:"goal_iterations,omitempty"`
	Capabilities   SessionCapabilities `json:"capabilities"`
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

type SpawnSchema struct {
	Fields []SpawnField `json:"fields"`
}

type SpawnField struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Values   []string `json:"values,omitempty"`
	Required bool     `json:"required"`
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
}

type ErrorResponse struct {
	Error         string `json:"error"`
	Code          int    `json:"code,omitempty"`
	SerfErrorInfo string `json:"serf_error_info,omitempty"`
}
