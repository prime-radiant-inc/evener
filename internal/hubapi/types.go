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
	Ref              string              `json:"ref"`
	HostID           string              `json:"host_id"`
	SessionID        string              `json:"session_id"`
	Title            string              `json:"title"`
	State            string              `json:"state"`
	Live             bool                `json:"live"`
	Project          string              `json:"project"`
	WorkingDir       string              `json:"working_dir,omitempty"`
	Branch           string              `json:"branch,omitempty"`
	Model            string              `json:"model,omitempty"`
	Profile          string              `json:"profile,omitempty"`
	TurnCount        int                 `json:"turn_count"`
	ContextPressure  float64             `json:"context_pressure"`
	ParentSessionID  string              `json:"parent_session_id,omitempty"`
	DivergenceTurn   int                 `json:"divergence_turn,omitempty"`
	ForkLabel        string              `json:"fork_label,omitempty"`
	IsSubagent       bool                `json:"is_subagent"`
	Capabilities     SessionCapabilities `json:"capabilities"`
	Streams          SessionStreams      `json:"streams"`
}

type SessionCapabilities struct {
	Send           bool   `json:"send"`
	Steer          bool   `json:"steer"`
	Interrupt      bool   `json:"interrupt"`
	Compact        bool   `json:"compact"`
	Clear          bool   `json:"clear"`
	Fork           bool   `json:"fork"`
	Resume         bool   `json:"resume"`
	Shutdown       bool   `json:"shutdown"`
	ChangeModel    bool   `json:"change_model"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
}

type SessionStreams struct {
	TranscriptFollow string `json:"transcript_follow,omitempty"`
	Live             string `json:"live,omitempty"`
	Replay           string `json:"replay,omitempty"`
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

type SpawnRequest struct {
	Task            string `json:"task,omitempty"`
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

type ErrorResponse struct {
	Error string `json:"error"`
}
