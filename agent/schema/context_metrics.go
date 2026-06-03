package schema

// ContextMetrics describes the estimated current context size, all in tokens.
type ContextMetrics struct {
	Used      int // estimated tokens currently consumed by the conversation
	Window    int // the model's total context-window size
	Remaining int // Window minus Used, floored at zero
}
