package agent

import (
	"sync"

	"primeradiant.com/serf/llm"
)

// EvalMetrics holds metrics collected during an evaluation run.
type EvalMetrics struct {
	Strategy          string   `json:"strategy"`
	Model             string   `json:"model"`
	Task              string   `json:"task"`
	Completed         bool     `json:"completed"`
	TurnCount         int      `json:"turn_count"`
	TotalInputTokens  int      `json:"total_input_tokens"`
	TotalOutputTokens int      `json:"total_output_tokens"`
	TotalTokens       int      `json:"total_tokens"`
	RecallCalls       int      `json:"recall_calls"`
	ForkSummaryCalls  int      `json:"fork_summary_calls"`
	CompactionEvents  int      `json:"compaction_events"`
	CompactionLayers  []string `json:"compaction_layers"`
	RetentionScore    float64  `json:"retention_score"`
	DurationSeconds   float64  `json:"duration_seconds"`
	Result            string   `json:"result"`

	// F2P test evaluation results (when --test-patch is used).
	F2PResults *F2PResults `json:"f2p_results,omitempty"`

	// Per-question retention probe breakdown (when new probe format is used).
	RetentionBreakdown []ProbeResult `json:"retention_breakdown,omitempty"`
}

// F2PResults captures fail-to-pass test evaluation outcomes.
type F2PResults struct {
	Resolved     bool     `json:"resolved"`
	TestsPassed  []string `json:"tests_passed"`
	TestsFailed  []string `json:"tests_failed"`
	TestErrors   string   `json:"test_errors,omitempty"`
	PatchApplied bool     `json:"patch_applied"`
}

// EvalCollector collects metrics from session events during an evaluation run.
type EvalCollector struct {
	metrics EvalMetrics
	mu      sync.Mutex
}

// NewEvalCollector creates an EvalCollector pre-populated with strategy, model,
// and task metadata.
func NewEvalCollector(strategy, model, task string) *EvalCollector {
	return &EvalCollector{
		metrics: EvalMetrics{
			Strategy:         strategy,
			Model:            model,
			Task:             task,
			CompactionLayers: []string{},
		},
	}
}

// ProcessEvent handles a SessionEvent and updates the collected metrics.
func (c *EvalCollector) ProcessEvent(ev SessionEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch ev.Kind {
	case EventAssistantTextEnd:
		c.metrics.TurnCount++
		if d, ok := ev.Data.(AssistantTextEndData); ok {
			c.addUsage(d.Usage)
		}

	case EventContextCompaction:
		c.metrics.CompactionEvents++
		if d, ok := ev.Data.(ContextCompactionData); ok && d.Layer != "" {
			c.metrics.CompactionLayers = append(c.metrics.CompactionLayers, d.Layer)
		}

	case EventToolCallStart:
		if d, ok := ev.Data.(ToolCallStartData); ok && d.ToolName == "recall" {
			c.metrics.RecallCalls++
		}

	case EventForkSummary:
		c.metrics.ForkSummaryCalls++
	}
}

// addUsage extracts token counts from the Usage field, which may be llm.Usage
// or map[string]any (when deserialized from JSON).
func (c *EvalCollector) addUsage(usage any) {
	switch u := usage.(type) {
	case llm.Usage:
		c.metrics.TotalInputTokens += u.InputTokens
		c.metrics.TotalOutputTokens += u.OutputTokens
	case map[string]any:
		if v, ok := u["input_tokens"].(float64); ok {
			c.metrics.TotalInputTokens += int(v)
		}
		if v, ok := u["output_tokens"].(float64); ok {
			c.metrics.TotalOutputTokens += int(v)
		}
	}
}

// Metrics returns a snapshot of the collected metrics.
func (c *EvalCollector) Metrics() EvalMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	m := c.metrics
	// Return a copy of the layers slice to prevent mutation.
	m.CompactionLayers = append([]string{}, c.metrics.CompactionLayers...)
	// Compute TotalTokens for spec compliance.
	m.TotalTokens = m.TotalInputTokens + m.TotalOutputTokens
	return m
}
