package agent

import (
	"sync"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm"
)

// evalMetrics holds metrics collected during an evaluation run.
type evalMetrics struct {
	Strategy string `json:"strategy"`
	Model    string `json:"model"`
	Task     string `json:"task"`

	// Completed reports whether the latest session-input boundary the collector
	// observed was successful: an EventSessionEnd whose SessionEndData.Reason is
	// "input_complete". "turn_failed" (unless the session is closing, which
	// emits no input boundary) and "interrupted" clear it, so the latest input
	// wins; a failed skill route can record a turn failure and still reach
	// "input_complete". The teardown-only "session_closed" leaves it unchanged,
	// so false means no successful boundary was observed.
	//
	// It records session-input completion, not task success: a
	// communicate(end_turn=true) call only ends the turn, and the eval's
	// executable task checks and retained tool chronology remain the task
	// evidence.
	Completed         bool     `json:"completed"`
	TurnCount         int      `json:"turn_count"`
	TotalInputTokens  int      `json:"total_input_tokens"`
	TotalOutputTokens int      `json:"total_output_tokens"`
	TotalTokens       int      `json:"total_tokens"`
	CacheReadTokens   int      `json:"cache_read_tokens"`
	CacheWriteTokens  int      `json:"cache_write_tokens"`
	ForkSummaryCalls  int      `json:"fork_summary_calls"`
	CompactionEvents  int      `json:"compaction_events"`
	CompactionLayers  []string `json:"compaction_layers"`
	RetentionScore    float64  `json:"retention_score"`
	DurationSeconds   float64  `json:"duration_seconds"`
	Result            string   `json:"result"`

	// F2P test evaluation results (when --test-patch is used).
	F2PResults *f2pResults `json:"f2p_results,omitempty"`

	// Per-question retention probe breakdown (when new probe format is used).
	RetentionBreakdown []probeResult `json:"retention_breakdown,omitempty"`

	// Diff captures the agent's code changes (git diff) after the run.
	Diff string `json:"diff,omitempty"`
}

// Session-end reasons that report an input's terminal boundary. session_closed
// reports session teardown instead and must not change Completed. A newly added
// input-terminal reason must be added to the switch in ProcessEvent; an
// unlisted reason leaves the last observed boundary unchanged.
const (
	evalSessionEndInputComplete = "input_complete"
	evalSessionEndTurnFailed    = "turn_failed"
	evalSessionEndInterrupted   = "interrupted"
)

// f2pResults captures fail-to-pass test evaluation outcomes.
type f2pResults struct {
	Resolved     bool     `json:"resolved"`
	TestsPassed  []string `json:"tests_passed"`
	TestsFailed  []string `json:"tests_failed"`
	TestErrors   string   `json:"test_errors,omitempty"`
	PatchApplied bool     `json:"patch_applied"`
}

// evalCollector collects metrics from session events during an evaluation run.
type evalCollector struct {
	metrics evalMetrics
	mu      sync.Mutex
}

// newEvalCollector creates an EvalCollector pre-populated with strategy, model,
// and task metadata.
func newEvalCollector(strategy, model, task string) *evalCollector {
	return &evalCollector{
		metrics: evalMetrics{
			Strategy:         strategy,
			Model:            model,
			Task:             task,
			CompactionLayers: []string{},
		},
	}
}

// ProcessEvent handles a SessionEvent and updates the collected metrics.
func (c *evalCollector) ProcessEvent(ev events.SessionEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch ev.Kind {
	case events.EventAssistantTextEnd:
		c.metrics.TurnCount++
		if d, ok := ev.Data.(events.AssistantTextEndData); ok {
			c.addUsage(d.Usage)
		}

	case events.EventContextCompaction:
		c.metrics.CompactionEvents++
		if d, ok := ev.Data.(events.ContextCompactionData); ok && d.Layer != "" {
			c.metrics.CompactionLayers = append(c.metrics.CompactionLayers, d.Layer)
		}

	case events.EventForkSummary:
		c.metrics.ForkSummaryCalls++

	case events.EventSessionEnd:
		if d, ok := ev.Data.(events.SessionEndData); ok {
			switch d.Reason {
			case evalSessionEndInputComplete:
				c.metrics.Completed = true
			case evalSessionEndTurnFailed, evalSessionEndInterrupted:
				c.metrics.Completed = false
			}
		}
	}
}

// addUsage accumulates token counts from an AssistantTextEndData usage record.
func (c *evalCollector) addUsage(u llm.Usage) {
	c.metrics.TotalInputTokens += u.InputTokens
	c.metrics.TotalOutputTokens += u.OutputTokens
	if u.CacheReadTokens != nil {
		c.metrics.CacheReadTokens += *u.CacheReadTokens
	}
	if u.CacheWriteTokens != nil {
		c.metrics.CacheWriteTokens += *u.CacheWriteTokens
	}
}

// Metrics returns a snapshot of the collected metrics.
func (c *evalCollector) Metrics() evalMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	m := c.metrics
	// Return a copy of the layers slice to prevent mutation.
	m.CompactionLayers = append([]string{}, c.metrics.CompactionLayers...)
	// Compute TotalTokens for spec compliance.
	m.TotalTokens = m.TotalInputTokens + m.TotalOutputTokens
	return m
}
