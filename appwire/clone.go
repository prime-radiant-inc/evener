package appwire

import (
	"encoding/json"
	"maps"
)

// CloneThread returns a deep copy of t in which every nested mutable field
// (slices, maps, pointers, and json.RawMessage byte slices) is independent of
// the original. Value-typed scalar fields (strings, ints, bools) are copied by
// value semantics and do not need explicit handling.
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
	// CodexErrorInfo is any — if it is a reference type (slice, map, pointer),
	// the original is shared. We cannot deep-copy an arbitrary `any` without
	// reflection, but CodexErrorInfo comes from a parsed JSON payload that is
	// already an independent value in practice. Leave it as-is to match the
	// existing shallow-copy behavior for this opaque field.
	return &cp
}

func cloneEvenerThread(e EvenerThread) EvenerThread {
	e.Diagnostics = cloneEvenerDiagnostics(e.Diagnostics)
	e.Queue = cloneQueueState(e.Queue)
	e.PendingMutations = clonePendingMutations(e.PendingMutations)
	e.PendingEscalations = append([]SandboxEscalationRequested(nil), e.PendingEscalations...)
	e.ReasoningEffortLevels = append([]string(nil), e.ReasoningEffortLevels...)
	// Capabilities is all bools (value type) — no copy needed.
	// Tasks, Goal, Usage, FailedToolCalls are pointers to value-only structs;
	// the pointer itself is sufficient because their contents are immutable scalars.
	return e
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
	cp.Hooks = cloneStringIntMap(d.Hooks)
	cp.Jobs = append([]EvenerJobInfo(nil), d.Jobs...)
	cp.Delegates = cloneDelegateInfos(d.Delegates)
	if d.TurnSlots != nil {
		ts := *d.TurnSlots
		cp.TurnSlots = &ts
	}
	cp.Agents = append([]string(nil), d.Agents...)
	return &cp
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
	d.Warnings = append([]string(nil), d.Warnings...)
	d.Diagnostics = append([]string(nil), d.Diagnostics...)
	d.Message = append(json.RawMessage(nil), d.Message...)
	d.StructuredResult = append(json.RawMessage(nil), d.StructuredResult...)
	return d
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

func cloneStringIntMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	cp := make(map[string]int, len(m))
	maps.Copy(cp, m)
	return cp
}
