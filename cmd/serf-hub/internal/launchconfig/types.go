// Package launchconfig owns the layered configuration that hub-serf
// passes when launching a serf serve subprocess. Layers (global, in-repo,
// local per-project, per-launch) are merged into a single Resolved
// value which is then turned into argv + env via ToArgs/ToEnv.
package launchconfig

import "time"

// Layer is one writable or in-memory layer of launch configuration. All
// scalar value fields are pointer-typed so the merge logic can
// distinguish "not set at this layer" from "explicitly zero."
type Layer struct {
	Schema                 int               `toml:"schema,omitempty"`
	Model                  string            `toml:"model,omitempty"`
	FastCheapModel         string            `toml:"fast_cheap_model,omitempty"`
	Agent                  string            `toml:"agent,omitempty"`
	ReasoningEffort        string            `toml:"reasoning_effort,omitempty"`
	ContextStrategy        string            `toml:"context_strategy,omitempty"`
	MaxRounds              *int              `toml:"max_rounds,omitempty"`
	MaxSubagentDepth       *int              `toml:"max_subagent_depth,omitempty"`
	NoProjectPrompts       *bool             `toml:"no_project_prompts,omitempty"`
	NonInteractive         *bool             `toml:"non_interactive,omitempty"`
	AppReplaySize          *int              `toml:"app_replay_size,omitempty"`
	SkillsDirs             []string          `toml:"skills_dirs,omitempty"`
	PluginDirs             []string          `toml:"plugin_dirs,omitempty"`
	MCPConfigs             []string          `toml:"mcp_configs,omitempty"`
	SystemPromptMode       string            `toml:"system_prompt_mode,omitempty"`
	SystemPromptFile       string            `toml:"system_prompt_file,omitempty"`
	SystemPromptText       string            `toml:"system_prompt_text,omitempty"`
	SystemPromptAppendMode string            `toml:"system_prompt_append_mode,omitempty"`
	SystemPromptAppendFile string            `toml:"system_prompt_append_file,omitempty"`
	SystemPromptAppendText string            `toml:"system_prompt_append_text,omitempty"`
	SystemPromptAppend     []string          `toml:"system_prompt_append,omitempty"`
	ModelFallbacks         []string          `toml:"model_fallbacks,omitempty"`
	ModelFallbacksSet      bool              `toml:"-" json:"-"`
	MCPs                   []MCPServerSpec   `toml:"mcps,omitempty"`
	Env                    map[string]string `toml:"env,omitempty"`
	Verbose                *bool             `toml:"verbose,omitempty"`
	TraceFile              string            `toml:"trace_file,omitempty"`
	CPUProfile             string            `toml:"cpu_profile,omitempty"`
	ExportATIFPath         string            `toml:"export_atif_path,omitempty"`
}

// MCPServerSpec describes one MCP server entry. Matches the shape passed
// to `serf serve --mcp name:command args...`.
type MCPServerSpec struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args,omitempty"`
}

// LayerName identifies which layer a value came from.
type LayerName string

const (
	LayerGlobal  LayerName = "global"
	LayerRepo    LayerName = "repo"
	LayerProject LayerName = "project"
	LayerLaunch  LayerName = "launch"
)

// Resolved is the output of merging every layer. Provenance maps each
// effective field name to the topmost contributing LayerName.
type Resolved struct {
	Effective   Layer
	Layers      map[LayerName]Layer
	Provenance  map[string]LayerName
	Repo        *RepoStatus
	Diagnostics []Diagnostic
}

// TrustState describes the in-repo .serf/launch.toml trust outcome.
type TrustState string

const (
	TrustAbsent    TrustState = "absent"
	TrustUntrusted TrustState = "untrusted"
	TrustTrusted   TrustState = "trusted"
	TrustChanged   TrustState = "changed"
	TrustRejected  TrustState = "rejected"
)

// RepoStatus describes the in-repo launch.toml that resolver found, if any.
type RepoStatus struct {
	Path    string
	Hash    string
	Trust   TrustState
	Preview string
}

// Diagnostic is a non-fatal note from the resolver. Surfaced on the wire.
type Diagnostic struct {
	Layer   LayerName
	Field   string
	Message string
}

// Meta is the contents of ~/.serf/projects/<id>/meta.toml.
type Meta struct {
	Schema    int       `toml:"schema"`
	CWD       string    `toml:"cwd"`
	CreatedAt time.Time `toml:"created_at"`
	Trust     MetaTrust `toml:"trust,omitempty"`
}

// MetaTrust records the TOFU decision for the in-repo file.
type MetaTrust struct {
	// Hashes is the set of content hashes that have been explicitly trusted or
	// rejected. New trust decisions append to this set so that branch-switching
	// with different .serf/launch.toml content does not require re-prompting.
	Hashes []string `toml:"hashes,omitempty"`
	// Hash is the singular trusted hash from the original TOFU implementation.
	//
	// Deprecated: new code reads Hashes; old single-hash entries are migrated
	// to Hashes on first write.
	Hash      string    `toml:"hash,omitempty"`
	Decision  string    `toml:"decision,omitempty"` // "trusted" | "rejected"
	DecidedAt time.Time `toml:"decided_at,omitempty"`
}
