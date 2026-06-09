package hooks

import "primeradiant.com/serf/agent/plugin"

// eventExitPolicy is the exit-code contract for one event (07 §Exit-code semantics).
// Phase 1 covers only events serf currently fires; everything else defaults to
// non-blocking (reserved-placeholder) so an unimplemented event never blocks.
// Tier: claude-compatible-subset (table); the full Claude table (07) is the source.
type eventExitPolicy struct {
	BlockOnExit2 bool // exit 2 blocks the action (else stderr shown to user only)
}

// exitBehavior returns the exit-code policy for the given event. BlockOnExit2 is
// set ONLY for events whose runner actually enforces the block on exit 2; the
// table never claims a block that no runner consumes.
//
// Enforced (BlockOnExit2=true): the runner denies/blocks when a matched hook
// exits 2:
//   - PreToolUse: RunPreToolUse denies the tool call
//   - Stop: runStopEvent prevents stopping
//   - SubagentStop: runStopEvent prevents the subagent stopping
//
// In the Claude contract, exit 2 also blocks UserPromptSubmit (erase the prompt)
// and PreCompact (block compaction); see the full Claude table in 07
// §"Exit-code semantics". Serf does NOT yet enforce those blocks: RunUserPromptSubmit
// and RunPreCompact only aggregate output and have no block path at their dispatch
// sites (07 marks UserPromptSubmit erase-prompt and PreCompact blocking as deferred
// parity items). They are therefore false here rather than carrying a dead "block"
// entry — when the dispatch sites grow a block path, flip them back and add a runner
// that consumes the flag (TestExitBehavior_BlockEntriesAreEnforced guards this).
//
// All other events (PostToolUse, SessionStart, SessionEnd, Notification, and all
// reserved/unimplemented events) are non-blocking: exit 2 does not block the action.
// Where the exit-2 stderr goes is event-specific and decided by the runners /
// routeOutput, not here — to the model for PostToolUse, user-visible for most
// others, and discarded for SessionEnd (whose runner ignores all output).
// Tier: claude-compatible-subset.
func exitBehavior(e plugin.HookEvent) eventExitPolicy {
	switch e {
	case plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop:
		return eventExitPolicy{BlockOnExit2: true}
	default: // PostToolUse, SessionStart, SessionEnd, Notification, UserPromptSubmit, PreCompact, and all reserved events
		return eventExitPolicy{BlockOnExit2: false}
	}
}
