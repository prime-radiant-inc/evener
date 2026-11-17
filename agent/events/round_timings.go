package events

import "time"

// RoundTimings captures per-phase wall-clock durations for a single round
// of the agentic loop in processOneInput. Emitted via EventRoundTimings
// at the end of each round for observability and profiling.
type RoundTimings struct {
	Round         int           `json:"round"`
	SystemPrompt  time.Duration `json:"system_prompt_ns"`  // LoadProjectDocs + BuildSystemPrompt
	ContextMgmt   time.Duration `json:"context_mgmt_ns"`   // ManageContext (includes compaction)
	HistoryExpand time.Duration `json:"history_expand_ns"` // History copy + message expansion
	// ToolDefs is the round's tool-definition hand-off: taking the cached
	// definitions the request advertises. The definitions themselves are built
	// once at session init (rebuildToolDefsCache) and again only when the tool
	// set changes (a model switch, a registered tool, an MCP recovery), never
	// by a round, so a rebuilding round is not something a round reports.
	ToolDefs time.Duration `json:"tool_defs_ns"`
	// RequestBuild is everything the round does between the expanded history
	// and the request it dispatches: projecting that history onto the target's
	// wire, laying out messages, pricing tools, token budgeting, continuation
	// planning and skill-delivery admission.
	RequestBuild time.Duration `json:"request_build_ns"`
	LLMCall      time.Duration `json:"llm_call_ns"`      // The actual LLM Complete call
	ToolExec     time.Duration `json:"tool_exec_ns"`     // Tool execution
	Persistence  time.Duration `json:"persistence_ns"`   // appendTurn + maybeAutoSave
	AfterAction  time.Duration `json:"after_action_ns"`  // Strategy AfterAction
	LoopOverhead time.Duration `json:"loop_overhead_ns"` // Everything else (loop detection, steering, etc.)
	TotalRound   time.Duration `json:"total_round_ns"`   // Total round time
}
