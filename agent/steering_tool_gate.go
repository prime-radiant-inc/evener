package agent

// canInstructTool reports whether canned instruction text this session injects
// — steering, reminders, job notifications, compaction checkpoints — may tell
// the agent to call the named tool. The answer is no unless this session's own
// registry serves it: an instruction naming a tool the session cannot call is
// one the model can only fail, and it has no way to tell that case apart from a
// tool it merely has not noticed yet (ruled 2026-08-06). Every builder of
// canned instruction text asks this first and words a tool-free fallback — or
// stays silent, when the text is nothing but a call recipe — for a false.
func (s *Session) canInstructTool(name string) bool {
	if s == nil || s.reg == nil {
		return false
	}
	return s.reg.Get(name) != nil
}
