package hub

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// costFor is the $/Mtok cost the hub's registry resolves for instance/model
// (spec §7.5) — what every dollar figure a past thread reports is derived
// from. Nil when the hub holds no registry, the reference does not resolve, or
// the row carries no cost: the caller then renders nothing.
func costFor(reg *hubcore.ProviderRegistry, instance, model string) *registry.Cost {
	if reg == nil {
		return nil
	}
	r := reg.Get()
	if r == nil {
		return nil
	}
	res, err := r.Resolve(instance + "/" + model)
	if err != nil {
		return nil
	}
	return res.Caps.Cost
}

// pastEntryCost is costFor over the instance and model a past session
// recorded, the pair every one of its persisted figures is priced at.
func pastEntryCost(cfg hubcore.WebConfig, entry hubcore.PastEntry) *registry.Cost {
	return costFor(cfg.Registry, entry.Meta.ProfileID, entry.Meta.Model)
}

func pastThreadForRead(ctx context.Context, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.Thread, bool, error) {
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		return appwire.Thread{}, false, nil
	}
	thread, err := pastEntryThread(ctx, cfg, entry, params.IncludeTurns)
	if err != nil {
		return thread, true, err
	}
	thread = attachPastThreadSkillCatalog(entry, thread)
	// One thread, one transcript: this path can afford the full-transcript
	// scans the per-entry list sweeps cannot (see stampDerivedTotals).
	// One combined scan answers both figures; two separate ones would read and
	// decode the same immutable bytes twice.
	return stampDerivedTotals(cfg, entry, thread), true, nil
}

func pastThreadReadResponse(ctx context.Context, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, bool, error) {
	return pastThreadItemReadResponse(ctx, cfg, params)
}

func pastThreadTurnsList(ctx context.Context, cfg hubcore.WebConfig, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, bool, error) {
	readParams := appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: true}
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, true, err
	}
	entry, ok := pastEntryForRead(cfg, readParams)
	if !ok {
		return appwire.ThreadTurnsListResponse{}, false, nil
	}
	page, err := pastEntryPageItems(ctx, entry, params.Cursor, itemLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, true, err
	}
	result := page.candidateResult()
	packed, packErr := packThreadTurnsItemCandidates(result, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		thread := appwire.Thread{ID: entry.Meta.ID, SessionID: entry.Meta.ID, CWD: entry.Meta.EnvInfo.WorkingDir, Turns: response.Data}
		thread = stampItemPageTurns(cfg, entry, thread)
		response.Data = thread.Turns
		return response, nil
	}, itemLimit)
	if packErr != nil {
		return appwire.ThreadTurnsListResponse{}, true, packErr
	}
	return packed, true, nil
}

func pastThreadItemReadResponse(ctx context.Context, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, bool, error) {
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		return appwire.ThreadReadResponse{}, false, nil
	}
	thread, err := pastEntryThread(ctx, cfg, entry, false)
	if err != nil {
		return appwire.ThreadReadResponse{}, true, err
	}
	thread = attachPastThreadSkillCatalog(entry, thread)
	if !params.IncludeTurns {
		return appwire.ThreadReadResponse{Thread: stampDerivedTotals(cfg, entry, thread)}, true, nil
	}
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return appwire.ThreadReadResponse{}, true, err
	}
	page, err := pastEntryLatestItems(ctx, entry, itemLimit)
	if err != nil {
		return appwire.ThreadReadResponse{}, true, err
	}
	result := page.candidateResult()
	packed, packErr := packThreadReadItemCandidates(result, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
		thread.Turns = response.Thread.Turns
		thread = stampItemPageTurns(cfg, entry, thread)
		response.Thread = thread
		return response, nil
	}, itemLimit)
	if packErr != nil {
		return appwire.ThreadReadResponse{}, true, packErr
	}
	return packed, true, nil
}

type pastItemPage struct {
	Candidates  []appitempaging.TranscriptItemCandidate
	OlderCursor string
	Identity    appitempaging.CursorIdentity
	Exhausted   bool
}

func (p pastItemPage) candidateResult() transcriptItemCandidateResult {
	return transcriptItemCandidateResult{
		Candidates: appitempaging.TranscriptItemWindow{
			Candidates:  p.Candidates,
			OlderCursor: p.OlderCursor,
		},
		Identity:  p.Identity,
		Exhausted: p.Exhausted,
	}
}

func pastEntryLatestItems(ctx context.Context, entry hubcore.PastEntry, limit int) (pastItemPage, error) {
	path := pastTranscriptPath(entry)
	window, identity, err := pastTranscriptCache.LatestItemWindowFromFileContext(ctx, path, transcriptJSONLMaxLineBytes, apptranscript.ItemWindowOptions{
		ThreadRef: appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String(),
		Limit:     limit,
	}, projectBoundedPastTranscriptTurn)
	if err != nil {
		return pastItemPage{}, err
	}
	return pastItemPage{
		Candidates:  window.Candidates,
		OlderCursor: window.OlderCursor,
		Identity:    identity,
		Exhausted:   window.OlderCursor == "",
	}, nil
}

func pastEntryPageItems(ctx context.Context, entry hubcore.PastEntry, cursor string, limit int) (pastItemPage, error) {
	path := pastTranscriptPath(entry)
	window, identity, err := pastTranscriptCache.PreviousItemWindowFromFileContext(ctx, path, transcriptJSONLMaxLineBytes, apptranscript.ItemWindowOptions{
		ThreadRef: appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String(),
		Cursor:    cursor,
		Limit:     limit,
	}, projectBoundedPastTranscriptTurn)
	if err != nil {
		return pastItemPage{}, err
	}
	return pastItemPage{
		Candidates:  window.Candidates,
		OlderCursor: window.OlderCursor,
		Identity:    identity,
		Exhausted:   window.OlderCursor == "",
	}, nil
}

func stampItemPageTurns(cfg hubcore.WebConfig, entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	stampSessionImageURLs(entry.Meta.ID, thread.Turns)
	stampPastTurnCosts(pastEntryCost(cfg, entry), thread.Turns)
	thread = reconcileAndEnrichPastThread(entry, thread)
	return stampDerivedTotals(cfg, entry, thread)
}

func pastEntryForRead(cfg hubcore.WebConfig, params appwire.ThreadReadParams) (hubcore.PastEntry, bool) {
	if cfg.Past == nil {
		return hubcore.PastEntry{}, false
	}
	threadID, ok := localPastThreadID(params)
	if !ok {
		return hubcore.PastEntry{}, false
	}
	return cfg.Past.Find(threadID)
}

func localPastThreadID(params appwire.ThreadReadParams) (string, bool) {
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return "", false
		}
		if ref.SourceID != "local" {
			return "", false
		}
		return ref.ThreadID, true
	}
	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" {
		return "", false
	}
	return threadID, true
}

func liveThreadCanMergeLocalPast(live appwire.Thread) bool {
	if live.Evener.Ref != "" {
		ref, err := appwire.ParseRef(live.Evener.Ref)
		return err == nil && ref.SourceID == "local"
	}
	if live.Source != "" {
		return live.Source == "local"
	}
	return true
}

func mergePastThreadForRead(ctx context.Context, cfg hubcore.WebConfig, params appwire.ThreadReadParams, live appwire.Thread) (appwire.Thread, error) {
	if !liveThreadCanMergeLocalPast(live) {
		return live, nil
	}
	if params.ThreadID == "" && params.Ref == "" {
		switch {
		case live.Evener.Ref != "":
			params.Ref = live.Evener.Ref
		case live.ID != "":
			params.Ref = appwire.Ref{SourceID: "local", ThreadID: live.ID}.String()
		case live.SessionID != "":
			params.Ref = appwire.Ref{SourceID: "local", ThreadID: live.SessionID}.String()
		}
	}
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		return live, nil
	}
	// A live window is authoritative. Read saved turns only as the compatibility
	// fallback for a live source that returned none; the metadata merged below
	// does not use pastThreadForRead's full-transcript usage or failure scans.
	includePastTurns := params.IncludeTurns && len(live.Turns) == 0
	past, err := pastEntryThread(ctx, cfg, entry, includePastTurns)
	if err != nil {
		return appwire.Thread{}, err
	}
	if live.Evener.Diagnostics == nil {
		past = attachPastThreadSkillCatalog(entry, past)
	}
	if live.ID == "" {
		live.ID = past.ID
	}
	if live.SessionID == "" {
		live.SessionID = past.SessionID
	}
	if live.Preview == "" || live.Preview == live.ID || live.Preview == live.SessionID {
		live.Preview = past.Preview
	}
	if live.Name == "" {
		live.Name = past.Name
	}
	if live.ModelProvider == "" {
		live.ModelProvider = past.ModelProvider
	}
	if live.CreatedAt == 0 {
		live.CreatedAt = past.CreatedAt
	}
	if live.UpdatedAt == 0 {
		live.UpdatedAt = past.UpdatedAt
	}
	if live.Path == "" {
		live.Path = past.Path
	}
	if live.CWD == "" {
		live.CWD = past.CWD
	}
	if live.Source == "" {
		live.Source = past.Source
	}
	if live.Evener.Ref == "" {
		live.Evener.Ref = past.Evener.Ref
	}
	if live.Evener.Profile == "" {
		live.Evener.Profile = past.Evener.Profile
	}
	if live.Evener.Tasks == nil {
		live.Evener.Tasks = past.Evener.Tasks
	}
	if live.Evener.Diagnostics == nil {
		live.Evener.Diagnostics = past.Evener.Diagnostics
	}
	if params.IncludeTurns && len(live.Turns) == 0 {
		live.Turns = past.Turns
	}
	return live, nil
}

// discoverPastThreadSkillCatalog reconstructs the metadata a session had at
// start without loading any skill bodies. The order mirrors session startup:
// embedded skills first, automatic user skills next, project and configured
// extra directories after that, and finally the skills exposed by configured
// plugins. Later layers overwrite an earlier canonical key, just as they do
// during session initialization. Plugin directories use the shared
// first-manifest-wins selection policy; a later duplicate is skipped even if
// the selected plugin fails component loading.
//
// This function is intentionally behind a package variable. Thread-list,
// transcript-list, and turn-page sweeps must remain metadata-only and cheap;
// their tests replace the seam to prove they never invoke cold discovery.
var discoverPastThreadSkillCatalog = discoverPastThreadSkills

func discoverPastThreadSkills(entry hubcore.PastEntry) []appwire.EvenerSkillInfo {
	all := make(map[string]skill.SkillMeta)
	if embedded, err := skill.EmbeddedSkills(); err == nil {
		maps.Copy(all, embedded)
	}
	if userSkillsDir := userdirs.Subdir(userdirs.DefaultConfigRoot(), "skills"); userSkillsDir != "" {
		skill.ScanSkillsDir(userSkillsDir, all)
	}

	workingDir := strings.TrimSpace(entry.Meta.EnvInfo.WorkingDir)
	if workingDir != "" {
		env := execenv.NewLocalExecutionEnvironment(workingDir)
		maps.Copy(all, skill.DiscoverSkills(env, entry.Meta.Config.SkillsDirs...))
	}

	seenPluginNames := make(map[string]struct{}, len(entry.Meta.Config.PluginDirs))
	for _, dir := range entry.Meta.Config.PluginDirs {
		pluginName, ok := pastThreadPluginName(dir)
		if !ok {
			continue
		}
		if _, seen := seenPluginNames[pluginName]; seen {
			continue
		}
		seenPluginNames[pluginName] = struct{}{}
		pluginSkills := make(map[string]skill.SkillMeta)
		skill.ScanSkillsDir(filepath.Join(dir, "skills"), pluginSkills)
		for name, meta := range pluginSkills {
			all[pluginName+":"+name] = meta
		}
	}

	entries := skill.CatalogEntries(all)
	result := make([]appwire.EvenerSkillInfo, 0, len(entries))
	for _, entry := range entries {
		result = append(result, appwire.EvenerSkillInfo{Name: entry.Name, Description: entry.Description})
	}
	return result
}

// pastThreadPluginName reads only the plugin manifest fields needed to locate
// its skill directory. In particular, this does not load agents, commands,
// hooks, or MCP configuration: a malformed unrelated component must not hide
// otherwise valid plugin skills from a cold thread read.
func pastThreadPluginName(dir string) (string, bool) {
	name, err := plugin.ManifestName(dir)
	return name, err == nil
}

func attachPastThreadSkillCatalog(entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	skills := discoverPastThreadSkillCatalog(entry)
	if len(skills) == 0 {
		return thread
	}
	if thread.Evener.Diagnostics == nil {
		thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{}
	}
	thread.Evener.Diagnostics.Skills = skills
	return thread
}

// pastThreadCapabilities is what the hub can carry out for a thread with no
// daemon behind it: the resume-and-retry session mutations (compact, clear,
// change model, shutdown) plus the always-available ones (send, fork, goal,
// rename), all of them landing once qp94's auto-resume runs. Steer, Interrupt
// and Queue stay false — they gate on an active turn a cold thread has none of,
// so the hub deliberately does not resume for them (kata xr4x trues this up to
// qp94's wiring).
//
// It is the hub's answer to "what can still be done with this thread", which is
// why the relay hands the same set to a client at the moment a session closes
// (stampClosedThreadCapabilities) rather than leaving it to read a set the
// departing daemon cut for a turn that is over. One definition, so the pushed
// set and the read that follows it cannot drift.
func pastThreadCapabilities() appwire.ThreadCapabilities {
	caps := appwire.ThreadCapabilities{
		Send:         true,
		ForkFromTurn: true,
		Compact:      true,
		Clear:        true,
		ChangeModel:  true,
		Shutdown:     true,
		Goal:         true,
		Rename:       true,
	}
	caps.ChangeVisionModel = caps.ChangeModel
	return caps
}

func pastEntryThread(ctx context.Context, cfg hubcore.WebConfig, entry hubcore.PastEntry, includeTurns bool) (appwire.Thread, error) {
	title := schema.SessionDisplayName(entry.Meta)
	if title == "" {
		title = entry.Meta.ID
	}
	// name is the wire Name field, which feeds the pane header and browser
	// tab title (kata b309) as well as the TUI's tree title. Unlike Preview
	// (title, above — the full-text fallback), a session with neither a
	// generated name nor a prompt gets the same short form the rail row
	// already renders (kspb's nodeTitle), rather than SessionDisplayName's
	// bare-ID last resort, which is unreadable in a one-line title.
	name := title
	if name == "" || name == strings.TrimSpace(entry.Meta.ID) {
		name = hubcore.ShortID(strings.TrimSpace(entry.Meta.ID))
	}
	cwd := entry.Meta.EnvInfo.WorkingDir
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
	parentRef := ""
	if entry.Meta.ParentSessionID != "" {
		parentRef = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ParentSessionID}.String()
	}
	kind := "session"
	if entry.Meta.IsSubagent {
		kind = "subagent"
	} else if entry.Meta.ParentSessionID != "" {
		kind = "fork"
	}
	createdAt := hubcore.OrderCreatedAt(entry.Meta.CreatedAt, entry.Meta.UpdatedAt)
	updatedAt := hubcore.OrderUpdatedAt(entry.Meta.UpdatedAt, entry.Meta.CreatedAt)
	status := appwire.ThreadStatusNotLoaded
	if cfg.Roster != nil {
		if subState, live := cfg.Roster.SubagentState(entry.Meta.ID); live {
			// subState is already AppWire vocabulary (the daemon's projected
			// thread status); "" means an old daemon carried no per-descendant
			// states, so keep the historical listed-means-working fallback.
			subState = strings.TrimSpace(subState)
			if subState == "" {
				subState = appwire.ThreadStatusActive
			}
			status = subState
		}
	}
	// cumulativeUsage is the persisted full-session token total; the cost stamp
	// derives its "~$X.XX" from it at the session model's price (empty when
	// there is no usage or the model is uncataloged), mirroring the per-turn
	// cost stamp in pastEntryTurns and the live producer in server's appThread.
	//
	// A meta without it leaves both absent HERE, because this function also
	// runs once per entry on the list and transcript-target sweeps. The
	// single-thread read paths recover the figure from the transcript instead —
	// see stampDerivedTotals.
	cumulativeUsage := evenerUsageFromCumulative(entry.Meta.CumulativeUsage)
	thread := appwire.Thread{
		ID:            entry.Meta.ID,
		SessionID:     entry.Meta.ID,
		Preview:       title,
		Name:          name,
		ModelProvider: entry.Meta.Model,
		CreatedAt:     hubcore.UnixSeconds(createdAt),
		UpdatedAt:     hubcore.UnixSeconds(updatedAt),
		Status:        appwire.ThreadStatus{Type: status},
		Path:          filepath.Base(cwd),
		CWD:           cwd,
		Source:        "local",
		Evener: appwire.EvenerThread{
			Ref:          ref,
			ParentRef:    parentRef,
			Kind:         kind,
			Profile:      entry.Meta.ProfileID,
			Tasks:        persistedTaskAggregate(entry.StateDir, entry.Meta.ID),
			Goal:         persistedGoalState(entry.Meta.Goal),
			Capabilities: pastThreadCapabilities(),
			WorkMillis:   entry.Meta.WorkMillis,
			Usage:        cumulativeUsage,
			Cost:         appwire.EstimateCost(pastEntryCost(cfg, entry), cumulativeUsage),
			// ActiveTurnStartedAt stays 0 because the parent status payload does not
			// expose the in-process child's turn start time.
		},
	}
	if _, required, ownershipErr := restartRequiredDaemon(ctx, cfg, ref, entry.Meta.ID); ownershipErr != nil {
		return appwire.Thread{}, ownershipErr
	} else if required {
		thread.Status.Type = appwire.ThreadStatusRestartRequired
		thread.Evener.Capabilities = appwire.ThreadCapabilities{}
	}
	thread.Evener.VisionModel = entry.Meta.VisionModel
	delegates, delegateDiagnostics, err := pastEntryDelegateStatus(ctx, entry)
	if err != nil {
		return appwire.Thread{}, err
	}
	if len(delegates) != 0 || len(delegateDiagnostics) != 0 {
		if thread.Evener.Diagnostics == nil {
			thread.Evener.Diagnostics = &appwire.EvenerDiagnostics{}
		}
		for _, delegate := range delegates {
			projected := appwireDelegateFromAgentStatus(delegate)
			projected.Diagnostics = append(projected.Diagnostics, delegateDiagnostics...)
			thread.Evener.Diagnostics.Delegates = append(thread.Evener.Diagnostics.Delegates, projected)
		}
		// delegateDiagnostics must reach the wire independently of whether
		// any delegate exists to also carry a copy on its own Diagnostics
		// above: a diagnostic about the shared delegates.jsonl itself (e.g.
		// delegatestore.ErrLineTooLong, which degrades to zero delegates)
		// has no delegate to attach to, so
		// appwire.EvenerDiagnostics.DelegateDiagnostics is the only vessel
		// that can still carry it to the wire.
		thread.Evener.Diagnostics.DelegateDiagnostics = append(thread.Evener.Diagnostics.DelegateDiagnostics, delegateDiagnostics...)
	}
	if includeTurns {
		var err error
		thread.Turns, err = pastEntryTurns(cfg, entry)
		if err != nil {
			return appwire.Thread{}, err
		}
		thread = reconcileAndEnrichPastThread(entry, thread)
	}
	annotateThreadProjects([]appwire.Thread{thread})
	return thread, nil
}

func pastEntryDelegateStatus(ctx context.Context, entry hubcore.PastEntry) ([]agent.DelegateStatusInfo, []string, error) {
	sessionID := strings.TrimSpace(entry.Meta.ID)
	if schema.ValidateSessionID(sessionID) != nil {
		return nil, nil, nil //nolint:nilerr // malformed stored metadata has no safe delegate projection; the thread itself remains readable
	}
	rootID := strings.TrimSpace(entry.Meta.JobTreeRootSessionID)
	if rootID == "" {
		rootID = sessionID
	}
	if schema.ValidateSessionID(rootID) != nil {
		return nil, nil, nil //nolint:nilerr // malformed stored metadata has no safe delegate projection; the thread itself remains readable
	}
	path := filepath.Join(entry.StateDir, "sessions", rootID, "delegates.jsonl")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return agent.LoadSessionDelegateStatus(ctx, entry.StateDir, sessionID)
}

func appwireDelegateFromAgentStatus(delegate agent.DelegateStatusInfo) appwire.EvenerDelegateInfo {
	out := appwire.EvenerDelegateInfo{
		DelegateID: delegate.DelegateID, OwnerSessionID: delegate.OwnerSessionID, RootSessionID: delegate.RootSessionID,
		ChildSessionID: delegate.ChildSessionID, TranscriptRef: delegate.TranscriptRef, ParentDelegateID: delegate.ParentDelegateID,
		Type: delegate.Type, Lifecycle: delegate.Lifecycle, Phase: delegate.Phase, Status: delegate.Status,
		Outcome: delegate.Outcome, Reason: delegate.Reason, Terminal: delegate.Terminal, Resumable: delegate.Resumable, NeedsAttention: delegate.NeedsAttention,
		NotResumableReason: delegate.NotResumableReason, ProjectionRevision: delegate.ProjectionRevision,
		Task: delegate.Task, Description: delegate.Description, AgentType: delegate.AgentType, RequestedModel: delegate.RequestedModel,
		ResolvedProfileID: delegate.ResolvedProfileID, ResolvedModel: delegate.ResolvedModel, Model: delegate.Model,
		ReasoningEffort: delegate.ReasoningEffort, OriginTurnID: delegate.OriginTurnID, OriginToolCallID: delegate.OriginToolCallID,
		OriginItemID: delegate.OriginItemID, RunStartedAt: delegate.RunStartedAt, RunEndedAt: delegate.RunEndedAt,
		LatestActivityAt: delegate.LatestActivityAt, RunningForMS: cloneHubInt64(delegate.RunningForMS),
		QuietForMS: cloneHubInt64(delegate.QuietForMS), DurationMS: cloneHubInt64(delegate.DurationMS), PacketKind: delegate.PacketKind,
		Message: append(json.RawMessage(nil), delegate.Message...), StructuredResult: append(json.RawMessage(nil), delegate.StructuredResult...),
		StructuredValid: cloneHubBool(delegate.StructuredValid), StructuredReason: delegate.StructuredReason,
		Warnings: append([]string(nil), delegate.Warnings...), Diagnostics: append([]string(nil), delegate.Diagnostics...),
		ExhaustionBudget: delegate.ExhaustionBudget, ExhaustionLimit: delegate.ExhaustionLimit,
		ExhaustionResumable: cloneHubBool(delegate.ExhaustionResumable), DelegationAllowance: delegate.DelegationAllowance,
		ParentWatchGranted: delegate.ParentWatchGranted,
	}
	if delegate.Usage != nil {
		usage := *delegate.Usage
		out.Usage = &usage
	}
	if delegate.Worktree != nil {
		worktree := *delegate.Worktree
		out.Worktree = &worktree
	}
	return out
}

func cloneHubBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneHubInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// persistedTaskAggregate returns a task count only when the session has a
// persisted task file. TaskStore.Load treats a missing file as an empty store,
// which is correct for evener/tasks/list but would turn an unsupported or old
// session into a false authoritative zero in a thread snapshot. Check presence
// first, then reuse TaskStore.Load and View for the actual data semantics.
func persistedTaskAggregate(stateDir, sessionID string) *appwire.TaskAggregate {
	path := filepath.Join(stateDir, "tasks", sessionID+".json")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	tasks := taskpkg.NewTaskStore(stateDir, sessionID)
	if err := tasks.Load(); err != nil {
		return nil
	}
	summary := taskpkg.Summarize(tasks.View())
	aggregate := &appwire.TaskAggregate{
		Total:     summary.Total,
		Done:      summary.Done,
		Cancelled: summary.Cancelled,
		Remaining: summary.Remaining,
	}
	if summary.Current != nil {
		aggregate.Current = &appwire.TaskSummary{ID: summary.Current.ID, Description: summary.Current.Description}
	}
	return aggregate
}

func persistedGoalState(goal *schema.GoalSnapshot) *appwire.GoalState {
	if goal == nil {
		return nil
	}
	return &appwire.GoalState{Objective: goal.Objective, Status: goal.Status, Iterations: goal.Iterations}
}

// pastTranscriptCache memoizes saved-transcript parsing by file identity. Past
// transcripts are immutable, so lazy paging back through one (a fresh
// thread/turns/list file read per page) reuses one parse instead of re-reading
// the whole transcript each page.
var pastTranscriptCache = apptranscript.NewTurnCache()

// stampDerivedTotals fills in a session token total the meta does not carry,
// and the session's failed-tool-call count, by scanning the session's own span
// of its FULL transcript ONCE.
//
// The usage gap is the common case, not an edge: agent/fork.go's writeForkChild
// builds the child SessionMeta field by field and stamps no CumulativeUsage at
// all, so every fork child arrives with the field unset, and evener.usage and
// evener.cost both empty. The client can then only sum the turns it happens to
// hold, which it must honestly label "tokens (loaded turns)". The transcript
// records per-turn usage regardless, so summing it recovers the full-session
// figure — and because it reads the whole file rather than the loaded window,
// the total does not shrink with thread/read's turnLimit.
//
// The failure count is derived server-side because the client cannot derive it
// honestly. A windowed thread/read hands the client a suffix of the session —
// measured at about 47% of a long real session's document at load (kata hw2n) —
// and a count over that suffix is a partial figure wearing a full-session
// label. For failures that is worse than saying nothing: the harm the count
// exists to fix is a reader concluding a run was clean because they had not yet
// scrolled to the failure, and a "0 failed" computed from the loaded window
// states exactly that conclusion in the session's own chrome.
//
// A fork child's transcript OPENS with a verbatim copy of the parent's prefix,
// whose tokens the PARENT spent and whose failures the PARENT made.
// DivergenceTurn marks where the child's own history begins, and only that span
// counts toward either figure: charging the parent's spend or failures to the
// child would be fabrication.
//
// A present usage total is left alone: it is the daemon's own running count, and
// re-deriving would invite a second, disagreeing figure (the failure count is
// still owed, so that case scans for failures alone).
//
// Applied only on the single-thread read paths. pastEntryThread also runs once
// per entry on the thread-list and transcript-target sweeps, where a scan per
// session would cost a read of every transcript in the state dir.
//
// A read error (a legacy format_version 1 transcript, a missing file) leaves
// both figures absent. "Unknown" is the honest report, the client already
// renders an absent total as nothing rather than "↑0 ↓0" and an absent count as
// nothing rather than "clean", and a missing figure is no reason to fail the
// whole thread projection.
func stampDerivedTotals(cfg hubcore.WebConfig, entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	if thread.Evener.Usage != nil {
		return stampDerivedFailureCount(entry, thread)
	}
	total, failures, err := pastTranscriptCache.DerivedTotalsFromFile(pastTranscriptPath(entry), transcriptJSONLMaxLineBytes, entry.Meta.DivergenceTurn)
	if err != nil {
		return thread
	}
	if total != nil {
		thread.Evener.Usage = total
		thread.Evener.Cost = appwire.EstimateCost(pastEntryCost(cfg, entry), total)
	}
	thread.Evener.FailedToolCalls = &failures
	return thread
}

// derivedWorkspaceUsage is stampDerivedTotals's usage-only view, for the legacy
// web surface that assembles its own WorkspaceData rather than an appwire.Thread
// and owes no failure count. Returns nil for an absent total, on the same terms.
func derivedWorkspaceUsage(entry hubcore.PastEntry) *appwire.EvenerUsage {
	total, _, err := pastTranscriptCache.DerivedTotalsFromFile(pastTranscriptPath(entry), transcriptJSONLMaxLineBytes, entry.Meta.DivergenceTurn)
	if err != nil {
		return nil
	}
	return total
}

// stampDerivedFailureCount is stampDerivedTotals's failure-only half, for a
// thread whose usage total the meta already carried: it scans the session's own
// span for the failure count alone. The full rationale — why the count is
// derived server-side, the divergence cut, error-means-absent — is stamped above
// stampDerivedTotals.
func stampDerivedFailureCount(entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	count, err := pastTranscriptCache.FailedToolCallsFromFile(pastTranscriptPath(entry), transcriptJSONLMaxLineBytes, entry.Meta.DivergenceTurn)
	if err != nil {
		return thread
	}
	thread.Evener.FailedToolCalls = &count
	return thread
}

// pastTranscriptPath is where a saved session's transcript lives.
func pastTranscriptPath(entry hubcore.PastEntry) string {
	return filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
}

func pastEntryTurns(cfg hubcore.WebConfig, entry hubcore.PastEntry) ([]appwire.Turn, error) {
	transcriptPath := pastTranscriptPath(entry)
	toolNames := map[string]string{}
	turns, err := pastTranscriptCache.ItemTurnsFromFile(transcriptPath, transcriptJSONLMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		return appItemsFromReplayTurn(turnID, entryIndex, turn, toolNames)
	})
	if err != nil {
		return nil, err
	}
	stampSessionImageURLs(entry.Meta.ID, turns)
	// ItemTurnsFromFile only has the per-round usage persisted in the transcript;
	// it doesn't know the session's instance and model, so the cost estimate
	// is stamped here as a post-pass against the row those resolve to.
	stampPastTurnCosts(pastEntryCost(cfg, entry), turns)
	return turns, nil
}

// projectBoundedPastTranscriptTurn projects an already-decoded transcript turn
// (decoded once by apptranscript's own reader, not here — kata j13r) into
// AppWire items.
func projectBoundedPastTranscriptTurn(turn schema.Turn, turnID string, entryIndex int, toolNames map[string]string) []appwire.ThreadItem {
	return appItemsFromReplayTurn(turnID, entryIndex, turn, toolNames)
}

// decodeTranscriptTurn reads one saved transcript line into the turn the daemon
// wrote, using the daemon's own type. It is the hub's only entry point from
// transcript bytes to a turn, so every field schema.Turn carries reaches the
// reload path by construction (kata kq8c).
func decodeTranscriptTurn(raw json.RawMessage) (schema.Turn, bool) {
	var entryRec transcript.Entry
	if err := json.Unmarshal(raw, &entryRec); err != nil {
		return schema.Turn{}, false
	}
	return entryRec.Turn, true
}

func stampPastTurnCosts(cost *registry.Cost, turns []appwire.Turn) {
	for i := range turns {
		if turns[i].Usage != nil {
			turns[i].Cost = appwire.EstimateCost(cost, turns[i].Usage)
		}
	}
}

func reconcileAndEnrichPastThread(entry hubcore.PastEntry, thread appwire.Thread) appwire.Thread {
	return enrichThreadFileBackedOutputImages(thread)
}

func appItemsFromReplayTurn(turnID string, turnIndex int, turn schema.Turn, toolNames map[string]string) []appwire.ThreadItem {
	return apptranscript.ProjectTurn(turnID, turnIndex, turn, toolNames, projectReplayInputImage, apptranscript.ToolResultOutputImages)
}

// projectReplayInputImage stamps the sha and size the client needs to fetch an
// inline image back out of the transcript over /s/<session>/images/<sha>.
func projectReplayInputImage(image llm.ImageData) appwire.InputItem {
	item := apptranscript.DefaultImageProjector(image)
	if len(image.Data) == 0 {
		return item
	}
	item.Metadata = map[string]string{"sha": imageSha(image.Data), "size": strconv.Itoa(len(image.Data))}
	return item
}

func enrichThreadFileBackedOutputImages(thread appwire.Thread) appwire.Thread {
	sessionID := strings.TrimSpace(thread.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(thread.ID)
	}
	cwd := strings.TrimSpace(thread.CWD)
	if sessionID == "" || cwd == "" || len(thread.Turns) == 0 {
		return thread
	}
	argsByCallID := map[string]string{}
	for ti := range thread.Turns {
		for ii := range thread.Turns[ti].Items {
			item := thread.Turns[ti].Items[ii]
			if item.Type != "commandExecution" {
				continue
			}
			if item.CallID != "" && item.ArgumentsJSON != "" {
				argsByCallID[item.CallID] = item.ArgumentsJSON
			}
			if item.Status != appwire.TurnStatusCompleted {
				continue
			}
			argsJSON := item.ArgumentsJSON
			if argsJSON == "" && item.CallID != "" {
				argsJSON = argsByCallID[item.CallID]
			}
			fileBacked := outputImagesForToolCall(sessionID, cwd, item.ToolName, argsJSON, item.Output)
			if len(fileBacked) == 0 {
				continue
			}
			item.OutputImages = appendOutputImagesUnique(item.OutputImages, fileBacked)
			thread.Turns[ti].Items[ii] = item
		}
	}
	return thread
}

func appendOutputImagesUnique(existing, extra []appwire.OutputImage) []appwire.OutputImage {
	if len(extra) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(extra))
	for _, img := range existing {
		key := outputImageDescriptorKey(img)
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	out := existing
	for _, img := range extra {
		key := outputImageDescriptorKey(img)
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, img)
	}
	return out
}

// outputImageDescriptorKey prefers SHA over URL: two descriptors for the same
// content can legitimately carry different URLs (a past-thread read's
// sha-addressed /s/.../images/ route vs. the file-backed mechanism's
// /doc/image?... route for the same call, e.g. a read_file image - kata
// 1nr4), and SHA is the real content identity there, not the route that
// happened to serve it. Falls back to URL, then Path, for a descriptor that
// carries no SHA at all.
func outputImageDescriptorKey(img appwire.OutputImage) string {
	if img.SHA != "" {
		return "sha:" + img.SHA
	}
	if img.URL != "" {
		return img.URL
	}
	if img.Path != "" {
		return "path:" + img.Path
	}
	return ""
}

func reconcileDelegateThreadItemForTest(item appwire.ThreadItem, rec agent.HistoricalJobRecord) appwire.ThreadItem {
	return reconcileDelegateThreadItem(item, rec)
}

func reconcileDelegateThreadItems(thread appwire.Thread, jobsByID map[string]agent.HistoricalJobRecord) appwire.Thread {
	if len(jobsByID) == 0 || len(thread.Turns) == 0 {
		return thread
	}
	var turns []appwire.Turn
	clonedItems := map[int]bool{}
	for ti := range thread.Turns {
		for ii := range thread.Turns[ti].Items {
			item := thread.Turns[ti].Items[ii]
			if item.Type != "commandExecution" || item.ToolName != "delegate" {
				continue
			}
			jobID := delegateJobIDFromRaw(item.Raw)
			if jobID == "" {
				continue
			}
			rec, ok := jobsByID[jobID]
			if !ok {
				continue
			}
			reconciled := reconcileDelegateThreadItem(item, rec)
			if turns == nil {
				turns = append([]appwire.Turn(nil), thread.Turns...)
			}
			if !clonedItems[ti] {
				turns[ti].Items = append([]appwire.ThreadItem(nil), thread.Turns[ti].Items...)
				clonedItems[ti] = true
			}
			turns[ti].Items[ii] = reconciled
		}
	}
	if turns != nil {
		thread.Turns = turns
	}
	return thread
}

func reconcileDelegateThreadItem(item appwire.ThreadItem, rec agent.HistoricalJobRecord) appwire.ThreadItem {
	_ = rec
	return item
}

func delegateJobIDFromRaw(raw json.RawMessage) string {
	var payload struct {
		JobID        string `json:"job_id"`
		StartedJobID string `json:"started_job_id"`
		CurrentJobID string `json:"current_job_id"`
		LatestJobID  string `json:"latest_job_id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	for _, value := range []string{payload.JobID, payload.StartedJobID, payload.CurrentJobID, payload.LatestJobID} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isTerminalHistoricalJobStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "stopped", "exhausted":
		return true
	default:
		return false
	}
}
