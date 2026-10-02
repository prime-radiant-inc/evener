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
	t.GitInfo = clonePointer(t.GitInfo)
	t.Turns = cloneTurns(t.Turns)
	t.Evener = cloneEvenerThread(t.Evener)
	return t
}

func cloneThreadStatus(s ThreadStatus) ThreadStatus {
	s.ActiveFlags = append([]string(nil), s.ActiveFlags...)
	return s
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
	turn.StartedAt = clonePointer(turn.StartedAt)
	turn.CompletedAt = clonePointer(turn.CompletedAt)
	turn.DurationMS = clonePointer(turn.DurationMS)
	turn.Usage = clonePointer(turn.Usage)
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
	item.OutputImages = CloneOutputImages(item.OutputImages)
	item.Position = clonePointer(item.Position)
	item.StartedAt = clonePointer(item.StartedAt)
	item.CompletedAt = clonePointer(item.CompletedAt)
	item.DurationMS = clonePointer(item.DurationMS)
	item.ExitCode = clonePointer(item.ExitCode)
	item.Raw = append(json.RawMessage(nil), item.Raw...)
	return item
}

// CloneOverlayItem returns a copy of item that shares no mutable state with
// it.
func CloneOverlayItem(item OverlayItem) OverlayItem {
	if item.Anchor != nil {
		anchor := *item.Anchor
		item.Anchor = &anchor
	}
	item.Item = cloneThreadItem(item.Item)
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
	cp.Cause = clonePointer(cp.Cause)
	cp.CodexErrorInfo = cloneCodexErrorInfo(e.CodexErrorInfo)
	return &cp
}

func cloneEvenerThread(e EvenerThread) EvenerThread {
	e.Diagnostics = CloneEvenerDiagnostics(e.Diagnostics)
	e.Queue = cloneQueueState(e.Queue)
	e.PendingMutations = clonePendingMutations(e.PendingMutations)
	e.PendingEscalations = append([]SandboxEscalationRequested(nil), e.PendingEscalations...)
	e.ReasoningEffortLevels = append([]string(nil), e.ReasoningEffortLevels...)
	e.Tasks = CloneTaskAggregate(e.Tasks)
	e.Goal = clonePointer(e.Goal)
	e.SessionURLs = append([]SessionURL(nil), e.SessionURLs...)
	e.Usage = clonePointer(e.Usage)
	e.FailedToolCalls = clonePointer(e.FailedToolCalls)
	e.Activity = CloneThreadActivity(e.Activity)
	e.Subagents = clonePointer(e.Subagents)
	e.PendingQuestion = ClonePendingQuestion(e.PendingQuestion)
	e.Failure = CloneThreadFailure(e.Failure)
	e.Access = clonePointer(e.Access)
	// Capabilities is all bools (value type) — no copy needed.
	return e
}

// clonePointer returns a copy of *value that shares nothing with it; nil stays
// nil. The copy is one level deep, so it is only for types whose fields are
// all values.
func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// CloneThreadActivity deep-copies a pulse meter sample; nil stays nil.
func CloneThreadActivity(value *ThreadActivity) *ThreadActivity {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Minutes = append([]int(nil), value.Minutes...)
	return &clone
}

// ClonePendingQuestion returns a copy of value whose option labels are its
// own; nil stays nil.
func ClonePendingQuestion(value *PendingQuestion) *PendingQuestion {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Options = append([]string(nil), value.Options...)
	return &clone
}

// CloneThreadFailure returns a copy of value whose cause is its own; nil stays
// nil.
func CloneThreadFailure(value *ThreadFailure) *ThreadFailure {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Cause = clonePointer(value.Cause)
	return &clone
}

// CloneTaskAggregate returns a copy of value whose Current task is its own.
// nil stays nil, so "the source cannot know the task state" survives the copy.
func CloneTaskAggregate(value *TaskAggregate) *TaskAggregate {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Current = clonePointer(value.Current)
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

// CloneEvenerDiagnostics returns a defensive copy of d in which every nested
// mutable slice and pointer is independent of the original. It is the
// diagnostics-level counterpart of CloneThread and lets a caller that needs to
// attach one extra slice (e.g. sampled watches) do so without aliasing any other
// slice of the source block.
func CloneEvenerDiagnostics(d *EvenerDiagnostics) *EvenerDiagnostics {
	if d == nil {
		return nil
	}
	cp := *d
	cp.Tools = append([]EvenerToolInfo(nil), d.Tools...)
	cp.MCP = cloneMCPServers(d.MCP)
	cp.Skills = append([]EvenerSkillInfo(nil), d.Skills...)
	for i := range cp.Skills {
		cp.Skills[i].AllowedTools = append([]string(nil), d.Skills[i].AllowedTools...)
	}
	cp.Plugins = append([]EvenerPluginInfo(nil), d.Plugins...)
	cp.HookEvents = append([]EvenerHookEventStatus(nil), d.HookEvents...)
	cp.Jobs = CloneEvenerJobs(d.Jobs)
	cp.Delegates = cloneDelegateInfos(d.Delegates)
	cp.Watches = CloneEvenerWatches(d.Watches)
	cp.TurnSlots = clonePointer(d.TurnSlots)
	cp.Agents = append([]string(nil), d.Agents...)
	cp.DelegateDiagnostics = append([]string(nil), d.DelegateDiagnostics...)
	cp.SkillDiagnostics = append([]EvenerSkillDiagnostic(nil), d.SkillDiagnostics...)
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
		out[i].Resumable = clonePointer(jobs[i].Resumable)
		out[i].ExitCode = clonePointer(jobs[i].ExitCode)
	}
	return out
}

// CloneEvenerWatches returns a defensive copy of typed watch diagnostics.
// Cadence, event, and delivery-time slices are copied so a consumer mutating
// its copy cannot reach the shared wire value.
func CloneEvenerWatches(watches []EvenerWatchInfo) []EvenerWatchInfo {
	if watches == nil {
		return nil
	}
	out := make([]EvenerWatchInfo, len(watches))
	for i := range watches {
		out[i] = watches[i]
		out[i].Cadence = append([]EvenerWatchCadence(nil), watches[i].Cadence...)
		out[i].Events = append([]string(nil), watches[i].Events...)
		out[i].DeliveryTimes = append([]string(nil), watches[i].DeliveryTimes...)
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
	d.RunningForMS = clonePointer(d.RunningForMS)
	d.QuietForMS = clonePointer(d.QuietForMS)
	d.DurationMS = clonePointer(d.DurationMS)
	d.StructuredValid = clonePointer(d.StructuredValid)
	d.Usage = clonePointer(d.Usage)
	d.Worktree = clonePointer(d.Worktree)
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
	// SkillNames is per-entry, so both the outer slice and each entry's names
	// must be independent: CloneThread feeds cached thread state, and a caller
	// editing one entry's selections must never reach the original.
	if q.SkillNames != nil {
		names := make([][]string, len(q.SkillNames))
		for i := range q.SkillNames {
			names[i] = append([]string(nil), q.SkillNames[i]...)
		}
		q.SkillNames = names
	}
	if q.CommandNames != nil {
		names := make([][]string, len(q.CommandNames))
		for i := range q.CommandNames {
			names[i] = append([]string(nil), q.CommandNames[i]...)
		}
		q.CommandNames = names
	}
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
