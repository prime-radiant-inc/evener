package appwire

import (
	"encoding/json"
)

// CloneThread returns a copy of t in which every known nested mutable field
// (slices, maps, pointers, and json.RawMessage byte slices) is independent of
// the original. CodexErrorInfo is an opaque any, so its JSON-compatible forms
// are cloned while unsupported dynamic values retain their existing identity.
// Value-typed scalar fields are copied by value semantics.
func CloneThread(t Thread) Thread {
	t.Status = cloneThreadStatus(t.Status)
	t.GitInfo = cloneGitInfo(t.GitInfo)
	t.Turns = cloneTurns(t.Turns)
	t.Evener = cloneEvenerThread(t.Evener)
	return t
}

func cloneThreadStatus(s ThreadStatus) ThreadStatus {
	s.ActiveFlags = append([]string(nil), s.ActiveFlags...)
	return s
}

func cloneGitInfo(g *GitInfo) *GitInfo {
	if g == nil {
		return nil
	}
	cp := *g
	return &cp
}

func cloneTurns(turns []Turn) []Turn {
	if turns == nil {
		return nil
	}
	out := make([]Turn, len(turns))
	for i := range turns {
		out[i] = cloneTurn(turns[i])
	}
	return out
}

func cloneTurn(turn Turn) Turn {
	turn.Items = cloneThreadItems(turn.Items)
	turn.Error = cloneTurnError(turn.Error)
	turn.StartedAt = cloneInt64(turn.StartedAt)
	turn.CompletedAt = cloneInt64(turn.CompletedAt)
	turn.DurationMS = cloneInt64(turn.DurationMS)
	turn.Usage = cloneEvenerUsage(turn.Usage)
	return turn
}

func cloneThreadItems(items []ThreadItem) []ThreadItem {
	if items == nil {
		return nil
	}
	out := make([]ThreadItem, len(items))
	for i := range items {
		out[i] = cloneThreadItem(items[i])
	}
	return out
}

func cloneThreadItem(item ThreadItem) ThreadItem {
	item.Images = cloneInputItems(item.Images)
	item.OutputImages = append([]OutputImage(nil), item.OutputImages...)
	if item.Position != nil {
		position := *item.Position
		item.Position = &position
	}
	item.StartedAt = cloneInt64(item.StartedAt)
	item.CompletedAt = cloneInt64(item.CompletedAt)
	item.DurationMS = cloneInt64(item.DurationMS)
	item.ExitCode = cloneInt64(item.ExitCode)
	item.Raw = append(json.RawMessage(nil), item.Raw...)
	return item
}

func cloneInputItems(items []InputItem) []InputItem {
	if items == nil {
		return nil
	}
	out := make([]InputItem, len(items))
	for i := range items {
		out[i] = cloneMutationInputItem(items[i])
	}
	return out
}

func cloneTurnError(e *TurnError) *TurnError {
	if e == nil {
		return nil
	}
	cp := *e
	if cp.Cause != nil {
		cause := *cp.Cause
		cp.Cause = &cause
	}
	cp.CodexErrorInfo = cloneCodexErrorInfo(e.CodexErrorInfo)
	return &cp
}

func cloneEvenerThread(e EvenerThread) EvenerThread {
	e.Diagnostics = cloneEvenerDiagnostics(e.Diagnostics)
	e.Queue = cloneQueueState(e.Queue)
	e.PendingMutations = clonePendingMutations(e.PendingMutations)
	e.PendingEscalations = append([]SandboxEscalationRequested(nil), e.PendingEscalations...)
	e.ReasoningEffortLevels = append([]string(nil), e.ReasoningEffortLevels...)
	e.Tasks = cloneTaskAggregate(e.Tasks)
	e.Goal = cloneGoalState(e.Goal)
	e.Usage = cloneEvenerUsage(e.Usage)
	e.FailedToolCalls = cloneInt(e.FailedToolCalls)
	// Capabilities is all bools (value type) — no copy needed.
	return e
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneTaskAggregate(value *TaskAggregate) *TaskAggregate {
	if value == nil {
		return nil
	}
	clone := *value
	if value.Current != nil {
		current := *value.Current
		clone.Current = &current
	}
	return &clone
}

func cloneGoalState(value *GoalState) *GoalState {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneEvenerUsage(value *EvenerUsage) *EvenerUsage {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneCodexErrorInfo(value any) any {
	switch value := value.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	case []byte:
		return append([]byte(nil), value...)
	case []any:
		clone := make([]any, len(value))
		for i := range value {
			clone[i] = cloneCodexErrorInfo(value[i])
		}
		return clone
	case map[string]any:
		clone := make(map[string]any, len(value))
		for key, item := range value {
			clone[key] = cloneCodexErrorInfo(item)
		}
		return clone
	default:
		return value
	}
}

func cloneEvenerDiagnostics(d *EvenerDiagnostics) *EvenerDiagnostics {
	if d == nil {
		return nil
	}
	cp := *d
	cp.Tools = append([]EvenerToolInfo(nil), d.Tools...)
	cp.MCP = cloneMCPServers(d.MCP)
	cp.Skills = append([]EvenerSkillInfo(nil), d.Skills...)
	cp.Plugins = append([]EvenerPluginInfo(nil), d.Plugins...)
	cp.HookEvents = append([]EvenerHookEventStatus(nil), d.HookEvents...)
	cp.Jobs = CloneEvenerJobs(d.Jobs)
	cp.Delegates = cloneDelegateInfos(d.Delegates)
	if d.TurnSlots != nil {
		ts := *d.TurnSlots
		cp.TurnSlots = &ts
	}
	cp.Agents = append([]string(nil), d.Agents...)
	return &cp
}

// CloneEvenerJobs returns a defensive copy of typed job diagnostics.
func CloneEvenerJobs(jobs []EvenerJobInfo) []EvenerJobInfo {
	if jobs == nil {
		return nil
	}
	out := make([]EvenerJobInfo, len(jobs))
	for i := range jobs {
		out[i] = jobs[i]
		out[i].Resumable = cloneBool(jobs[i].Resumable)
		out[i].ExitCode = cloneInt(jobs[i].ExitCode)
	}
	return out
}

func cloneMCPServers(servers []EvenerMCPServerInfo) []EvenerMCPServerInfo {
	if servers == nil {
		return nil
	}
	out := make([]EvenerMCPServerInfo, len(servers))
	for i := range servers {
		out[i] = servers[i]
		out[i].Tools = append([]string(nil), servers[i].Tools...)
	}
	return out
}

func cloneDelegateInfos(delegates []EvenerDelegateInfo) []EvenerDelegateInfo {
	if delegates == nil {
		return nil
	}
	out := make([]EvenerDelegateInfo, len(delegates))
	for i := range delegates {
		out[i] = cloneDelegateInfo(delegates[i])
	}
	return out
}

func cloneDelegateInfo(d EvenerDelegateInfo) EvenerDelegateInfo {
	d.RunningForMS = cloneInt64(d.RunningForMS)
	d.QuietForMS = cloneInt64(d.QuietForMS)
	d.DurationMS = cloneInt64(d.DurationMS)
	d.StructuredValid = cloneBool(d.StructuredValid)
	d.Usage = cloneEvenerUsage(d.Usage)
	if d.Worktree != nil {
		worktree := *d.Worktree
		d.Worktree = &worktree
	}
	d.Warnings = append([]string(nil), d.Warnings...)
	d.Diagnostics = append([]string(nil), d.Diagnostics...)
	d.Message = append(json.RawMessage(nil), d.Message...)
	d.StructuredResult = append(json.RawMessage(nil), d.StructuredResult...)
	return d
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneQueueState(q QueueState) QueueState {
	q.Preview = append([]string(nil), q.Preview...)
	q.IDs = append([]string(nil), q.IDs...)
	q.ClientMutationIDs = append([]string(nil), q.ClientMutationIDs...)
	q.Texts = append([]string(nil), q.Texts...)
	return q
}

func clonePendingMutations(mutations []PendingMutation) []PendingMutation {
	if mutations == nil {
		return nil
	}
	out := make([]PendingMutation, len(mutations))
	for i := range mutations {
		out[i] = clonePendingMutation(mutations[i])
	}
	return out
}

func clonePendingMutation(m PendingMutation) PendingMutation {
	m.Input = cloneInputItems(m.Input)
	m.QueueEntryIDs = append([]string(nil), m.QueueEntryIDs...)
	return m
}
