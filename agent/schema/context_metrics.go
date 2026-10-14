package schema

// ContextMetrics describes the estimated current context size, all in tokens.
type ContextMetrics struct {
	Used      int // estimated tokens currently consumed by the conversation
	Window    int // the model's total context-window size
	Remaining int // Window minus Used, floored at zero
	// Assumed reports whether Window is the manager's conservative assumption
	// for an unknown window (contextmgr.AssumedContextWindow) rather than a
	// declared, cataloged, or listed size, so a renderer can present the
	// figure as an estimate instead of fact.
	Assumed bool
}
