package agent

import "time"

// RoundTimings captures per-phase wall-clock durations for a single round
// of the agentic loop in processOneInput. Emitted via EventRoundTimings
// at the end of each round for observability and profiling.
type RoundTimings struct {
	Round         int           `json:"round"`
	SystemPrompt  time.Duration `json:"system_prompt_ns"`  // LoadProjectDocs + BuildSystemPrompt
	ContextMgmt   time.Duration `json:"context_mgmt_ns"`   // ManageContext (includes compaction)
	HistoryExpand time.Duration `json:"history_expand_ns"` // History copy + message expansion
	ToolDefs      time.Duration `json:"tool_defs_ns"`      // allToolDefinitions
	LLMCall       time.Duration `json:"llm_call_ns"`       // The actual LLM Complete call
	ToolExec      time.Duration `json:"tool_exec_ns"`      // Tool execution
	Persistence   time.Duration `json:"persistence_ns"`    // appendTurn + maybeAutoSave
	AfterAction   time.Duration `json:"after_action_ns"`   // Strategy AfterAction
	LoopOverhead  time.Duration `json:"loop_overhead_ns"`  // Everything else (loop detection, steering, etc.)
	TotalRound    time.Duration `json:"total_round_ns"`    // Total round time
}
