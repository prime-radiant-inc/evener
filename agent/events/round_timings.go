package events

import (
	"time"

	"primeradiant.com/evener/agent/schema"
)

// RoundTimings captures per-phase wall-clock durations for a single round
// of the agentic loop in processOneInput. Emitted via EventRoundTimings
// at the end of each round for observability and profiling.
type RoundTimings struct {
	// OwningTurnID names the logical turn the round belongs to — the same
	// owner the persisted ROUND_TIMINGS entry carries, stamped from one read
	// of the active turn so the live and cold projections cannot disagree.
	// Excluded from JSON: the item's structured detail is the measurements,
	// and the cold projection builds that detail from schema.RoundTimings,
	// which has no owner field of its own.
	OwningTurnID  string        `json:"-"`
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

// Timings returns the measurements independently of the live owner, the way
// ContextCompactionData.Compaction does for its own payload: the persisted
// record carries the owner on the turn, not inside the measurements.
func (r RoundTimings) Timings() schema.RoundTimings {
	return schema.RoundTimings{
		Round: r.Round, SystemPrompt: r.SystemPrompt, ContextMgmt: r.ContextMgmt,
		HistoryExpand: r.HistoryExpand, ToolDefs: r.ToolDefs, LLMCall: r.LLMCall,
		ToolExec: r.ToolExec, Persistence: r.Persistence, AfterAction: r.AfterAction,
		LoopOverhead: r.LoopOverhead, TotalRound: r.TotalRound,
	}
}
