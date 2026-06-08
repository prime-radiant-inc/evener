package hooks

import "primeradiant.com/serf/agent/plugin"

// eventExitPolicy is the exit-code contract for one event (07 §Exit-code semantics).
// Phase 1 covers only events serf currently fires; everything else defaults to
// non-blocking (reserved-placeholder) so an unimplemented event never blocks.
// Tier: claude-compatible-subset (table); the full Claude table (07) is the source.
type eventExitPolicy struct {
	BlockOnExit2 bool // exit 2 blocks the action (else stderr shown to user only)
}

// exitBehavior returns the exit-code policy for the given event. The table
// implements the serf-fired subset of the Claude exit-code semantics table
// (docs/subagent-management/07-lifecycle-hooks-claude-compat.md §"Exit-code semantics").
//
// Events where exit 2 blocks the action (BlockOnExit2=true):
//   - PreToolUse: blocks the tool call
//   - Stop: prevents stopping
//   - SubagentStop: prevents subagent stopping
//   - UserPromptSubmit: blocks the prompt
//   - PreCompact: blocks compaction
//
// All other events (PostToolUse, SessionStart, SessionEnd, Notification, and
// all reserved/unimplemented events) default to non-blocking: exit 2 surfaces
// stderr as a user-visible error message but does not block the action.
// Tier: claude-compatible-subset.
func exitBehavior(e plugin.HookEvent) eventExitPolicy {
	switch e {
	case plugin.HookPreToolUse, plugin.HookStop, plugin.HookSubagentStop,
		plugin.HookUserPromptSubmit, plugin.HookPreCompact:
		return eventExitPolicy{BlockOnExit2: true}
	default: // PostToolUse, SessionStart, SessionEnd, Notification, and all reserved events
		return eventExitPolicy{BlockOnExit2: false}
	}
}
