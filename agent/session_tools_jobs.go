package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const (
	maxJobOutputRetentionBytes  = 8 * 1024 * 1024
	defaultJobListLimit         = 50
	maxJobListLimit             = 100
	minJobBlockTimeoutMS        = 1000
	maxJobBlockTimeoutMS        = 60000
	jobToolResultDefaultMaxChar = 20_000
	jobToolResultMinJSONChars   = 800
	jobManagerUnavailableReason = "job manager is not available"
	maxJobGrepMatches           = 100
	maxJobGrepLineBytes         = 4096
	jobKindAgent                = "agent"
	jobKindShell                = "shell"
	jobPhaseStarting            = "starting"
	jobPhaseAwaitingModel       = "awaiting_model"
	jobPhaseModelStreaming      = "model_streaming"
	jobPhaseToolRunning         = "tool_running"
	jobPhaseProcessRunning      = "process_running"
)

// clampJobBlockTimeout clamps a positive max_wait_ms request into the supported
// inline-block window [minJobBlockTimeoutMS, maxJobBlockTimeoutMS] and returns
// it as a wait duration. Callers gate on ms > 0 (and reject negatives) upstream.
func clampJobBlockTimeout(ms int) time.Duration {
	clamped := max(ms, minJobBlockTimeoutMS)
	clamped = min(clamped, maxJobBlockTimeoutMS)
	return time.Duration(clamped) * time.Millisecond
}

var rootOnlyJobControlTools = []string{"delegate", "job_watch"}

func registerJobTools(reg *tool.Registry, s *Session, deps *toolDeps) error {
	if deps != nil && deps.registerTool != nil {
		return registerJobToolsWithRegistrar(reg, jobToolRegisterFunc(func(registered tool.RegisteredTool) error {
			return deps.registerTool(reg, registered)
		}), s, deps)
	}
	return registerJobToolsWithRegistrar(reg, reg, s, deps)
}

type jobToolRegisterFunc func(tool.RegisteredTool) error

func (f jobToolRegisterFunc) Register(registered tool.RegisteredTool) error { return f(registered) }

type jobToolRegistrar interface {
	Register(tool.RegisteredTool) error
}

// registerJobToolsWithRegistrar keeps production registration on Registry while
// allowing deterministic failure injection at each registration boundary.
func registerJobToolsWithRegistrar(reg *tool.Registry, registrar jobToolRegistrar, s *Session, deps *toolDeps) error {
	_ = deps
	if err := registrar.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobStatus(), ReadOnly: true},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobStatusTool(s, args, jobToolResultMaxChars(reg, "job_status"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobList(), ReadOnly: true},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobListTool(s, args, jobToolResultMaxChars(reg, "job_list"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobStop()},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return jobStopTool(ctx, s, args, jobToolResultMaxChars(reg, "job_stop"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefDelegateSend()},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return delegateSendTool(ctx, s, args, jobToolResultMaxChars(reg, "delegate_send"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobWatch(availableEventKindNames())},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobWatchTool(s, args, jobToolResultMaxChars(reg, "job_watch"))
		},
	}); err != nil {
		return err
	}
	return registrar.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefDelegate(s.delegateAgentTypeNames())},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return delegateTool(ctx, s, args, jobToolResultMaxChars(reg, "delegate"))
		},
	})
}

func delegateSendTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	a := sendMessageArgs{
		Target:     stringArg(args, "to"),
		Message:    stringArg(args, "message"),
		Background: true, // default: no wait, return immediately
	}
	if strings.TrimSpace(a.Target) == runtimeMessageAliasCaller {
		return "", errors.New("invalid_request: delegate_send sends to child delegate_id only; observer callbacks use communicate(end_turn=true)")
	}
	// max_wait_ms: 0/absent = no wait; positive = wait inline up to N;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		a.Background = false
		a.BackgroundSet = true
		a.BlockTimeoutMS = int(clampJobBlockTimeout(n) / time.Millisecond)
	}

	res := s.sendDelegateMessage(ctx, a)
	if res.Err != nil {
		return "", res.Err
	}
	res.WaitIgnoredReason = liveSteerWaitIgnoredReason(a.BlockTimeoutMS, res.Status, res.Action)
	return marshalDelegateSendResult(res, maxChars)
}

// liveSteerWaitIgnoredReason returns a note when a caller passed a positive
// max_wait_ms but the send was a live steer of a running delegate, which returns on
// delivery and cannot honor the wait. It returns "" when the wait was honored (a
// resumed job) or not requested, so the field stays omitted in the common case.
func liveSteerWaitIgnoredReason(blockTimeoutMS int, status jobstore.Status, action string) string {
	if blockTimeoutMS > 0 && status == jobstore.StatusRunning && action == "steered" {
		return "live steer returns on delivery; max_wait_ms applies only to started jobs"
	}
	return ""
}

func jobWatchTool(s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	a, err := watchArgsFromToolArgs(args)
	if err != nil {
		return "", err
	}
	var res watchResult
	switch a.Operation {
	case "create":
		var source watchSource
		source, err = normalizeWatchSource(a.Source)
		if err != nil {
			return "", err
		}
		if source.Kind == watchSourceParentSession {
			if !s.cfg.spawn.parentWatchGranted || s.cfg.spawn.parentInstallWatch == nil {
				return "", errors.New("source_not_watchable: source parent requires delegate(watch_parent=true)")
			}
			a.Source = "parent"
			a.Target = runtimeMessageAliasCaller
			res, err = s.cfg.spawn.parentInstallWatch(s.ID(), s.cfg.spawn.parentDelegateID, a)
			break
		}
		a.Source = source.Public
		a.Target = source.Internal
		if source.Kind == watchSourceConcreteJob {
			if ownerRes, forwarded, ownerErr := s.configureDescendantReceiverWatch(a); forwarded || ownerErr != nil {
				res, err = ownerRes, ownerErr
				break
			}
		}
		res, err = jm.configureWatch(a)
	case "clear":
		local, localErr := jm.hasWatchID(a.WatchID)
		if localErr != nil {
			return "", localErr
		}
		if local {
			res, err = jm.clearWatchByID(a.WatchID)
			break
		}
		if ownerRes, found, ownerErr := s.clearDescendantReceiverWatchByID(a.WatchID); found || ownerErr != nil {
			res, err = ownerRes, ownerErr
			break
		}
		if !s.cfg.spawn.parentWatchGranted || s.cfg.spawn.parentClearWatch == nil {
			res, err = jm.clearWatchByID(a.WatchID)
			break
		}
		res, err = s.cfg.spawn.parentClearWatch(s.ID(), s.cfg.spawn.parentDelegateID, a.WatchID)
	case "list":
		return marshalWatchListResult(s.watchListToolResultWithDescendantReceivers(jm.watchListToolResult()), maxChars)
	case "inspect":
		inspect := jm.inspectWatchByID(a.WatchID)
		if watchInspectFound(inspect) {
			return marshalWatchInspectResult(inspect, maxChars)
		}
		if ownerInspect, found := s.inspectDescendantReceiverWatchByID(a.WatchID); found {
			return marshalWatchInspectResult(ownerInspect, maxChars)
		}
		return marshalWatchInspectResult(inspect, maxChars)
	}
	if err != nil {
		return "", err
	}
	return marshalWatchResult(res, maxChars)
}

func (s *Session) configureDescendantReceiverWatch(a watchArgs) (watchResult, bool, error) {
	if s == nil || !strings.HasPrefix(a.Target, "job_") {
		return watchResult{}, false, nil
	}
	ownerJM, ownerSess, _, _, ok := s.resolveDescendantJobOwner(a.Target)
	if !ok || ownerJM == nil || ownerSess == nil {
		return watchResult{}, false, nil
	}
	childArgs := a
	childArgs.Source = a.Source
	childArgs.Target = a.Target
	childArgs.ReceiverSessionID = s.ID()
	childArgs.ReceiverDelegateID = ""
	childArgs.ReceiverNotify = s.enqueueJobNotificationAndNotify
	res, err := ownerJM.configureWatch(childArgs)
	return res, true, err
}

func (s *Session) watchListToolResultWithDescendantReceivers(local jobWatchListToolResult) jobWatchListToolResult {
	for _, child := range s.liveDescendantSessions() {
		if child == nil || child.jobManager == nil {
			continue
		}
		descendant := child.jobManager.watchListToolResultForReceiver(s.ID(), "")
		local.Watches = append(local.Watches, descendant.Watches...)
		local.RecentWatches = append(local.RecentWatches, descendant.RecentWatches...)
	}
	sort.SliceStable(local.Watches, func(i, j int) bool {
		if local.Watches[i].Source != local.Watches[j].Source {
			return local.Watches[i].Source < local.Watches[j].Source
		}
		return local.Watches[i].WatchID < local.Watches[j].WatchID
	})
	local.Count = len(local.Watches)
	return local
}

func (s *Session) inspectDescendantReceiverWatchByID(watchID string) (jobWatchInspectToolResult, bool) {
	for _, child := range s.liveDescendantSessions() {
		if child == nil || child.jobManager == nil {
			continue
		}
		if inspect, ok := child.jobManager.inspectReceiverWatchByID(watchID, s.ID(), ""); ok {
			return inspect, true
		}
	}
	return jobWatchInspectToolResult{}, false
}

func (s *Session) clearDescendantReceiverWatchByID(watchID string) (watchResult, bool, error) {
	for _, child := range s.liveDescendantSessions() {
		if child == nil || child.jobManager == nil {
			continue
		}
		if _, ok := child.jobManager.inspectReceiverWatchByID(watchID, s.ID(), ""); !ok {
			continue
		}
		res, err := child.jobManager.clearReceiverWatchByID(watchID, s.ID(), "")
		return res, true, err
	}
	return watchResult{}, false, nil
}

func (s *Session) liveDescendantSessions() []*Session {
	if s == nil {
		return nil
	}
	var out []*Session
	for _, child := range s.liveSubagentSessions() {
		out = append(out, child)
		out = append(out, child.liveDescendantSessions()...)
	}
	return out
}

func watchInspectFound(inspect jobWatchInspectToolResult) bool {
	return inspect.Watching || inspect.Source != "" || inspect.EndReason != ""
}

// decodeDelegateArgs decodes the delegate tool's raw params into delegateArgs,
// returning an invalid_request error for a malformed wait/allowance value. It is
// pure over the args map (no session state), so the decode — including the
// tri-state sandbox_net (nil = inherit, never a silent false) — is unit-testable
// without minting a delegate.
func decodeDelegateArgs(args map[string]any) (delegateArgs, error) {
	a := delegateArgs{
		Task:            stringArg(args, "task"),
		AgentType:       stringArg(args, "agent_type"),
		Model:           stringArg(args, "model"),
		ReasoningEffort: stringArg(args, "reasoning_effort"),
		WatchParent:     shellBoolArg(args, "watch_parent"),
		Isolation:       stringArg(args, "isolation"),
		Sandbox:         stringArg(args, "sandbox"),
		Background:      true, // default: no wait, return job_id immediately
	}
	// sandbox_net is a tri-state: absent stays nil so the delegate INHERITS the
	// parent's network; present carries the explicit choice. A missing key must not
	// read as false — that would silently force network off. A present-but-non-boolean
	// value (e.g. the string "false" from a non-strict provider) is refused rather
	// than silently decoded as inherit — the same silent no-op this surface refuses
	// elsewhere (net-without-mode, net-with-off).
	switch v := args["sandbox_net"].(type) {
	case nil:
		// omitted → inherit
	case bool:
		a.SandboxNet = &v
	default:
		return delegateArgs{}, errors.New("invalid_request: sandbox_net must be a JSON boolean (true or false, not a quoted string)")
	}
	// max_wait_ms: 0/absent = no wait (background); positive = wait inline up to N;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return delegateArgs{}, errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		clamped := max(n, minJobBlockTimeoutMS)
		clamped = min(clamped, maxJobBlockTimeoutMS)
		a.Background = false
		a.BlockTimeoutMS = clamped
	}
	// delegation_allowance: 0/absent = leaf delegate (cannot delegate); positive
	// = grant; negative = invalid_request. Zero reads as unset (strict-zero rule).
	// createDelegate enforces the grant rule (strictly less than own allowance).
	if n, ok := shellIntArg(args, "delegation_allowance"); ok && n != 0 {
		if n < 0 {
			return delegateArgs{}, errors.New("invalid_request: delegation_allowance must be non-negative")
		}
		a.DelegationAllowance = n
	}
	if resultSchema, ok := args["result_schema"].(map[string]any); ok {
		a.ResultSchema = resultSchema
	}
	return a, nil
}

func delegateTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (string, error) {
	a, err := decodeDelegateArgs(args)
	if err != nil {
		return "", err
	}
	res := s.createDelegate(ctx, a)
	if res.Err != nil {
		return "", res.Err
	}
	return marshalDelegateResult(res, maxChars)
}

func jobStatusTool(s *Session, args map[string]any, maxChars int) (any, error) {
	jobID := strings.TrimSpace(stringArg(args, "job_id"))
	if jobID == "" {
		return "", errors.New("invalid_request: job_id is required")
	}
	if strings.HasPrefix(jobID, "dlg_") {
		return "", errors.New("invalid_request: delegate_id is a conversation handle; inspect a concrete job_id")
	}
	jm, rec, err := s.nestedOrLocalJobManager(jobID)
	if err != nil {
		return "", err
	}
	if live, liveErr := findJobRecord(jm, jobID); liveErr == nil {
		rec = live
	}
	consumeTerminalJobNotification(s, jm, rec)
	out := projectJobStatus(jm.now(), rec)
	rendered, err := marshalBoundedJSON(out, maxChars)
	if err != nil {
		return "", err
	}
	return tool.StateResult{Output: rendered, State: out}, nil
}

// consumeTerminalJobNotification settles the terminal notification of a job
// whose caller just read its terminal status: the caller has learned the job
// ended, so waking it later to say the same thing is an interruption that
// carries no news.
//
// It is recorded durably as its own state (consumed, not delivered) so the
// told-the-caller invariant stays true without claiming a notification turn
// that never happened — serf-doctor can still tell the two apart.
//
// Only the OWNER's own reads consume. A parent's forwarded copy of a
// child-owned pending is a drive signal, not the parent's news to hear:
// settling it there would silence the child's own undelivered notification.
func consumeTerminalJobNotification(s *Session, jm *jobManager, rec *jobstore.JobRecord) {
	if jm == nil || rec == nil || rec.TerminalGen == "" {
		return
	}
	if !rec.Status.IsTerminal() || rec.NotifyState != jobstore.NotifyPending {
		return
	}
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
		return
	}
	consumed := jobstore.Event{
		Kind:        jobstore.EventJobNotificationConsumed,
		TS:          jm.now(),
		JobID:       rec.JobID,
		TerminalGen: rec.TerminalGen,
	}
	if err := jm.appendEvent(consumed); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("job notification consume mark failed: %v", err)})
		return
	}
	rec.NotifyState = jobstore.NotifyConsumed
	// Settle the parent's forwarded COPY too, for the same reason a delivery
	// does: the copy is only a drive signal, and a signal nobody clears
	// re-drives forever.
	if err := jm.forwardSnapshot(consumed); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("job notification consume forward failed: %v", err)})
	}
}

func jobListTool(s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	filter, err := jobListFilterFromArgs(args)
	if err != nil {
		return "", err
	}
	var jobs []jobListEntry
	total := 0
	if filter.IncludeDescendants {
		descJobs, descTotal, listErr := s.walkDescendantJobs(filter)
		if listErr != nil {
			return "", listErr
		}
		jobs = descJobs
		total = descTotal
	} else {
		recs, recTotal, listErr := jm.listWithError(filter)
		if listErr != nil {
			return "", listErr
		}
		total = recTotal
		jobs = make([]jobListEntry, 0, len(recs))
		for _, rec := range recs {
			jobs = append(jobs, projectJobRecord(s, rec))
		}
	}
	delegateRecords, err := loadDelegatesForJobList(jm)
	if err != nil {
		return "", err
	}
	delegates := jobListDelegatesForJobs(s, delegateRecords, jobs)
	s.mu.Lock()
	allowance := s.delegationAllowance
	s.mu.Unlock()
	result := jobListResult{
		Jobs:          jobs,
		Count:         len(jobs),
		Offset:        filter.Offset,
		Total:         total,
		Delegates:     delegates,
		TurnSlots:     turnSlotOccupancyOf(s),
		Watches:       jm.liveWatchSummaries(),
		RecentWatches: jm.recentWatchSummaries(),
	}
	// Allowance ≤ 1 can only grant 0 — a no-op knob; surface it only when it
	// actually enables fan-out (mirrors eliding the delegate schema param).
	if allowance > 1 {
		result.DelegationAllowance = allowance
	}
	_ = maxChars
	return tool.StateResult{Output: formatJobList(result), State: result}, nil
}

// turnSlotOccupancyOf snapshots the session's tree-counter occupancy for the
// job_list footer, or nil when nothing is held (the common idle case — no
// noise). Jobs/InUse/Cap describe the spawn budget; Drives reads the
// separate drive budget (driveCounter) so a drive-saturated tree with zero
// running jobs is visible rather than reporting a dead 0.
func turnSlotOccupancyOf(s *Session) *turnSlotOccupancy {
	if s == nil || s.treeCounter == nil {
		return nil
	}
	total, jobs, _, limit := s.treeCounter.occupancy()
	var drives int64
	if s.driveCounter != nil {
		drives, _, _, _ = s.driveCounter.occupancy()
	}
	if total == 0 && drives == 0 {
		return nil
	}
	return &turnSlotOccupancy{InUse: total, Cap: limit, Jobs: jobs, Drives: drives}
}

var loadDelegatesForJobList = func(jm *jobManager) (map[string]*jobstore.DelegateRecord, error) {
	return jm.store.LoadDelegates()
}

func jobListDelegatesForJobs(s *Session, records map[string]*jobstore.DelegateRecord, jobs []jobListEntry) []delegateListEntry {
	if len(records) == 0 || len(jobs) == 0 {
		return nil
	}
	jobIDs := make(map[string]bool, len(jobs))
	delegateIDs := make(map[string]bool)
	for _, job := range jobs {
		jobIDs[job.JobID] = true
		if job.DelegateID != "" {
			delegateIDs[job.DelegateID] = true
		}
	}
	if len(delegateIDs) == 0 {
		return nil
	}
	orderedIDs := make([]string, 0, len(delegateIDs))
	for delegateID := range delegateIDs {
		orderedIDs = append(orderedIDs, delegateID)
	}
	sort.Strings(orderedIDs)
	delegates := make([]delegateListEntry, 0, len(orderedIDs))
	for _, delegateID := range orderedIDs {
		record := records[delegateID]
		if record == nil || !delegateControlOwnedBySession(record.OwnerSessionID, s.id) {
			continue
		}
		if !jobIDs[record.CurrentJobID] && !jobIDs[record.LatestJobID] {
			continue
		}
		delegate := projectDelegateRecord(record)
		if !jobIDs[delegate.CurrentJobID] {
			delegate.CurrentJobID = ""
		}
		if !jobIDs[delegate.LatestJobID] {
			delegate.LatestJobID = ""
		}
		delegates = append(delegates, delegate)
	}
	return delegates
}

// formatJobList renders job_list as plain text: a schema header, then one job per
// line — job_id, type, status, a label (description or shell command), and a
// bracketed detail tail (started time, reason, exit code, size, resumability) —
// then a count footer with the delegation allowance and any active/recent watches.
func formatJobList(out jobListResult) string {
	var b strings.Builder
	if len(out.Jobs) > 0 {
		b.WriteString("# job_id  type  status  label  [started · reason · exit · bytes]\n")
	}
	for _, j := range out.Jobs {
		fmt.Fprintf(&b, "%s  %s  %s", j.JobID, j.Type, j.Status)
		if j.Depth > 0 {
			fmt.Fprintf(&b, "  depth=%d", j.Depth)
		}
		label := j.Description
		if label == "" && j.Command != nil {
			label = *j.Command
		}
		if label != "" {
			fmt.Fprintf(&b, "  %s", label)
		}
		var detail []string
		if started := shortTimestamp(j.StartedAt); started != "" {
			detail = append(detail, "started "+started)
		}
		if j.Reason != nil && *j.Reason != "" {
			detail = append(detail, *j.Reason)
		}
		if j.ExitCode != nil {
			detail = append(detail, fmt.Sprintf("exit %d", *j.ExitCode))
		}
		if j.DelegateID != "" {
			detail = append(detail, "delegate_id "+j.DelegateID)
		}
		detail = append(detail, fmt.Sprintf("%d bytes", j.TotalBytes))
		// Surface resumability only when a job actually is resumable; resumable=false
		// for an ordinary shell job is noise (keeps rows lean).
		if j.Resumable != nil && *j.Resumable {
			detail = append(detail, "resumable")
		}
		fmt.Fprintf(&b, "  [%s]\n", strings.Join(detail, " · "))
	}
	if len(out.Jobs) == 0 {
		b.WriteString("No jobs.\n")
	}
	if out.Offset > 0 || out.Total > len(out.Jobs) {
		if len(out.Jobs) == 0 {
			// Offset past the end: never print an inverted "showing 51-50".
			fmt.Fprintf(&b, "\nshowing none of %d jobs (offset %d past end).", out.Total, out.Offset)
		} else {
			fmt.Fprintf(&b, "\nshowing %d-%d of %d jobs.", out.Offset+1, out.Offset+len(out.Jobs), out.Total)
		}
	} else {
		fmt.Fprintf(&b, "\n%d job(s).", out.Count)
	}
	if len(out.Delegates) > 0 {
		fmt.Fprintf(&b, " %d delegate(s).", len(out.Delegates))
	}
	if ts := out.TurnSlots; ts != nil {
		fmt.Fprintf(&b, " delegate turn slots: %d/%d in use (%d jobs, %d drive turns).", ts.InUse, ts.Cap, ts.Jobs, ts.Drives)
	}
	if out.DelegationAllowance > 0 {
		fmt.Fprintf(&b, " delegation_allowance: %d.", out.DelegationAllowance)
	}
	for _, d := range out.Delegates {
		var detail []string
		if d.CurrentJobID != "" {
			detail = append(detail, "current_job_id "+d.CurrentJobID)
		}
		if d.LatestJobID != "" && d.LatestJobID != d.CurrentJobID {
			detail = append(detail, "latest_job_id "+d.LatestJobID)
		}
		if d.TranscriptRef != "" {
			detail = append(detail, "transcript_ref "+d.TranscriptRef)
		}
		if d.Resumable {
			detail = append(detail, "resumable")
		} else if d.NotResumableWhy != "" {
			detail = append(detail, d.NotResumableWhy)
		}
		if d.ParentDelegateID != "" {
			detail = append(detail, "parent_delegate_id "+d.ParentDelegateID)
		}
		fmt.Fprintf(&b, "\ndelegate %s  %s", d.DelegateID, d.Status)
		if len(detail) != 0 {
			fmt.Fprintf(&b, "  [%s]", strings.Join(detail, " · "))
		}
	}
	for _, w := range out.Watches {
		fmt.Fprintf(&b, "\nwatch %s → %s (%s)", w.ID, w.Source, w.Condition)
	}
	for _, w := range out.RecentWatches {
		fmt.Fprintf(&b, "\nrecent watch %s → %s (%s, %d delivered)", w.ID, w.Source, w.EndReason, w.Deliveries)
	}
	return b.String()
}

// shortTimestamp renders an RFC3339Nano timestamp as "YYYY-MM-DD HH:MM" for
// compact display, returning "" if it cannot be parsed.
func shortTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func jobStopTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	jobID := strings.TrimSpace(stringArg(args, "job_id"))
	if jobID == "" {
		return "", errors.New("invalid_request: job_id is required")
	}
	if strings.HasPrefix(jobID, "dlg_") {
		return "", errors.New("invalid_request: delegate_id is a conversation handle; stop a concrete job_id")
	}
	// max_wait_ms: 0/absent = request stop and return; positive = wait up to N;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	maxWaitMS := 0
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		maxWaitMS = n
	}

	targetJM := jm
	if routed, _, err := s.nestedOrLocalJobManager(jobID); err == nil {
		targetJM = routed
	}
	var childStopErr error
	if shellBoolArg(args, "include_children") {
		_, childStopErr = s.stopChildren(jobID)
	}
	// Stopping a delegate job cascades into its subtree (spec §2): resolve the
	// delegate's live child session BEFORE the stop signals (and cancels) the
	// coordinator's turn, then stop the coordinator's own running jobs (its
	// workers' delegate + shell jobs) recursively, so they do not survive
	// orphaned.
	cascadeChild := s.delegateChildSessionToCascade(jobID)
	var previousStatus jobstore.Status
	if _, pre, lookupErr := s.nestedOrLocalJobManager(jobID); lookupErr == nil && pre != nil {
		previousStatus = pre.Status
	}
	rec, err := stopNestedOrLocalForJobStop(s, jobID)
	if err != nil {
		return "", errors.Join(childStopErr, err)
	}
	if cascadeChild != nil {
		if _, cascadeErr := stopDelegateSubtreeForJobStop(s, cascadeChild); cascadeErr != nil {
			childStopErr = errors.Join(childStopErr, cascadeErr)
		}
	}
	if maxWaitMS > 0 {
		done := waitForJobDone(ctx, targetJM, jobID, clampJobBlockTimeout(maxWaitMS))
		if _, latest, err := s.nestedOrLocalJobManager(jobID); err == nil {
			rec = latest
		}
		if !done && rec != nil && !rec.Status.IsTerminal() {
			pending := cloneJobRecord(rec)
			pending.Reason = "stop_pending"
			rec = pending
		}
	}
	if childStopErr != nil {
		return "", childStopErr
	}
	_ = maxChars
	stop := jobStopResult{
		JobID:          rec.JobID,
		Status:         string(rec.Status),
		Reason:         stringPtrOrNil(rec.Reason),
		PreviousStatus: string(previousStatus),
		Outcome:        classifyStopOutcome(previousStatus, rec),
	}
	return tool.StateResult{Output: formatJobStop(stop), State: stop}, nil
}

var stopNestedOrLocalForJobStop = func(s *Session, jobID string) (*jobstore.JobRecord, error) {
	return s.stopNestedOrLocal(jobID)
}

var stopDelegateSubtreeForJobStop = func(s *Session, child *Session) ([]*jobstore.JobRecord, error) {
	return s.stopDelegateSubtree(child)
}

type jobStatusResult struct {
	JobID            string  `json:"job_id"`
	Kind             string  `json:"kind"`
	Status           string  `json:"status"`
	Description      string  `json:"description"`
	Phase            string  `json:"phase,omitempty"`
	Reason           *string `json:"reason,omitempty"`
	ExhaustionBudget string  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit  int     `json:"exhaustion_limit,omitempty"`
	Resumable        *bool   `json:"resumable,omitempty"`
	RunningForMS     *int64  `json:"running_for_ms,omitempty"`
	DurationMS       *int64  `json:"duration_ms,omitempty"`
	QuietForMS       *int64  `json:"quiet_for_ms,omitempty"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at,omitempty"`
	LastEventAt      *string `json:"last_event_at,omitempty"`
	TranscriptRef    string  `json:"transcript_ref"`
	ExitCode         *int    `json:"exit_code,omitempty"`
}

// turnSlotOccupancy is the diagnostic tree-counter snapshot surfaced in
// job_list while any delegate-turn slot is held: spawn-budget total in use,
// cap, and jobs, plus drive turns in flight on the separate drive budget.
type turnSlotOccupancy struct {
	InUse int64 `json:"in_use"`
	Cap   int64 `json:"cap"`
	Jobs  int64 `json:"jobs"`
	// Drives is the live occupancy of the separate drive-turn budget
	// (driveCounter); drive turns do not hold spawn-budget slots.
	Drives int64 `json:"drive_turns"`
}

type jobListResult struct {
	Jobs      []jobListEntry      `json:"jobs"`
	Count     int                 `json:"count"`
	Offset    int                 `json:"offset,omitempty"`
	Total     int                 `json:"total"`
	TurnSlots *turnSlotOccupancy  `json:"turn_slots,omitempty"`
	Delegates []delegateListEntry `json:"delegates,omitempty"`
	// Watches/RecentWatches/DelegationAllowance are supervision signal kept only
	// when they carry information: no active watches, no recent watch history, and
	// a no-op delegation allowance (≤ 1, which can only grant 0) are all omitted.
	Watches             []watchListEntry   `json:"watches,omitempty"`
	RecentWatches       []recentWatchEntry `json:"recent_watches,omitempty"`
	DelegationAllowance int                `json:"delegation_allowance,omitempty"`
}

type delegateListEntry struct {
	DelegateID       string `json:"delegate_id"`
	Status           string `json:"status"`
	CurrentJobID     string `json:"current_job_id,omitempty"`
	LatestJobID      string `json:"latest_job_id,omitempty"`
	TranscriptRef    string `json:"transcript_ref,omitempty"`
	Resumable        bool   `json:"resumable"`
	NotResumableWhy  string `json:"not_resumable_reason,omitempty"`
	ParentDelegateID string `json:"parent_delegate_id,omitempty"`
}

// watchListEntry is one active watch in job_list's result, projected from the
// session's live watch configs. condition is a compact one-line summary of the
// watch's trigger; created_at is the watch's install time as an RFC3339Nano
// timestamp.
type watchListEntry struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Condition  string `json:"condition"`
	Deliveries int    `json:"deliveries"`
	CreatedAt  string `json:"created_at"`
}

// recentWatchEntry is one watch that has left the active set, surfaced by job_list's
// bounded recent_watches ring so a fired-then-removed watch stays legible. end_reason
// is one of auto_removed_terminal, cleared, replaced, budget_exhausted.
type recentWatchEntry struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Condition  string `json:"condition"`
	Deliveries int    `json:"deliveries"`
	EndReason  string `json:"end_reason"`
	EndedAt    string `json:"ended_at"`
}

type jobListEntry struct {
	JobID          string  `json:"job_id"`
	DelegateID     string  `json:"delegate_id,omitempty"`
	Kind           string  `json:"kind"`
	Type           string  `json:"type"`
	Status         string  `json:"status"`
	Phase          string  `json:"phase,omitempty"`
	Reason         *string `json:"reason,omitempty"`
	Description    string  `json:"description"`
	ParentJobID    *string `json:"parent_job_id,omitempty"`
	OwnerSessionID string  `json:"owner_session_id"`
	// VisibleToSessionID is internal visibility routing — in a plain list it always
	// equals the owner, so it is kept for tooling but omitted from the model wire.
	VisibleToSessionID string `json:"-"`
	// TranscriptRef/Resumable/NotResumableReason are omitempty: nil for the common
	// running-shell scan (so they vanish), present where they carry signal (a
	// delegate's transcript handle, a runtime-lost delegate's resumability).
	TranscriptRef      *string `json:"transcript_ref,omitempty"`
	ExhaustionBudget   string  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit    int     `json:"exhaustion_limit,omitempty"`
	Resumable          *bool   `json:"resumable,omitempty"`
	NotResumableReason *string `json:"not_resumable_reason,omitempty"`
	StartedAt          string  `json:"started_at"`
	EndedAt            *string `json:"ended_at,omitempty"`
	RunningForMS       *int64  `json:"running_for_ms,omitempty"`
	DurationMS         *int64  `json:"duration_ms,omitempty"`
	QuietForMS         *int64  `json:"quiet_for_ms,omitempty"`
	LastEventAt        *string `json:"last_event_at,omitempty"`
	// LastActivity is the most recent parent-observable activity timestamp
	// (output append or start) for a running job; for a terminal record with no
	// live stamp it falls back to ended_at, then started_at. A quiet running
	// delegate stays at its started_at, which is exactly what the quiet-job
	// watchdog surfaces. current_action is intentionally omitted: a running
	// delegate's mid-run "current action" is not cheaply readable from
	// parent-side state without cross-session probing.
	LastActivity *string `json:"last_activity"`
	ExitCode     *int    `json:"exit_code,omitempty"`
	// TotalBytes is the lifetime output byte count — the same name and concept the
	// shell result and job transcript report, so the field is consistent across
	// every tool the agent reads.
	TotalBytes int64 `json:"total_bytes"`
	// Command is the shell command line for a shell job, omitted for delegates (which
	// have none) so the row stays lean. It lets the agent identify a job without
	// reading the transcript when the description is sparse.
	Command *string `json:"command,omitempty"`
	// Depth is the live-subtree distance from the calling session, populated only
	// by job_list(include_descendants=true): 0 for the caller's own store, 1 for
	// a direct child's store, and so on. It is the depth of the store the row was
	// surfaced from, so a dead descendant's terminal forwarded copy that survives
	// in an ancestor store carries that ancestor's depth. Omitted when 0 for the
	// default and include_nested listings, which do not walk the tree.
	Depth int `json:"depth,omitempty"`
}

type jobStopResult struct {
	JobID          string  `json:"job_id"`
	Status         string  `json:"status"`
	Reason         *string `json:"reason"`
	PreviousStatus string  `json:"previous_status"`
	Outcome        string  `json:"outcome"`
}

// formatJobStop renders a job_stop result as a single plain-text line matching the
// job-family footer style: [job <id> · <status> · <outcome> · <reason>].
func formatJobStop(out jobStopResult) string {
	parts := []string{"job " + out.JobID, out.Status, out.Outcome}
	if out.Reason != nil && *out.Reason != "" {
		parts = append(parts, *out.Reason)
	}
	return "[" + strings.Join(parts, " · ") + "]"
}

// classifyStopOutcome distinguishes a stop that cancelled a live job from one that
// raced with, or arrived after, the job's own completion. previous is the job's
// status read before the stop signal; rec is the record after it.
func classifyStopOutcome(previous jobstore.Status, rec *jobstore.JobRecord) string {
	if previous.IsTerminal() {
		return "already_terminal"
	}
	if rec == nil || !rec.Status.IsTerminal() {
		return "stop_requested" // still finalizing (e.g. reason "stop_pending")
	}
	if rec.Status == jobstore.StatusCancelled {
		return "cancelled_by_request"
	}
	return "completed_during_stop"
}

type delegateSendResult struct {
	DelegateID             string           `json:"delegate_id,omitempty"`
	StartedJobID           string           `json:"started_job_id,omitempty"`
	CurrentJobID           string           `json:"current_job_id,omitempty"`
	LatestJobID            string           `json:"latest_job_id,omitempty"`
	Type                   string           `json:"type,omitempty"`
	Status                 string           `json:"status,omitempty"`
	Reason                 *string          `json:"reason,omitempty"`
	ExhaustionBudget       string           `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit        int              `json:"exhaustion_limit,omitempty"`
	Resumable              *bool            `json:"resumable,omitempty"`
	RunningInBackground    bool             `json:"running_in_background"`
	TimedOut               bool             `json:"timed_out,omitempty"`
	Action                 string           `json:"action"`
	TranscriptRef          string           `json:"transcript_ref,omitempty"`
	Output                 *string          `json:"output,omitempty"`
	Truncated              *bool            `json:"truncated,omitempty"`
	StructuredResult       any              `json:"structured_result,omitempty"`
	StructuredResultValid  *bool            `json:"structured_result_valid,omitempty"`
	StructuredResultReason string           `json:"structured_result_reason,omitempty"`
	Watching               bool             `json:"watching,omitempty"`
	Watches                []watchListEntry `json:"watches,omitempty"`
	// Worktree carries the isolation lane's path/branch/ahead/dirty state for
	// an isolated delegate's terminal job (native worktree tools spec §9
	// lifecycle step 3); nil for a non-isolated delegate.
	Worktree          *delegateWorktreeToolResult `json:"worktree,omitempty"`
	WaitIgnoredReason string                      `json:"wait_ignored_reason,omitempty"`
}

type jobWatchToolResult struct {
	WatchID            string                   `json:"watch_id,omitempty"`
	Source             string                   `json:"source"`
	Watching           bool                     `json:"watching"`
	OutputMatch        string                   `json:"output_match,omitempty"`
	Events             []string                 `json:"events,omitempty"`
	EventFilter        *jobWatchToolEventFilter `json:"event_filter,omitempty"`
	ProgressIntervalMS int                      `json:"progress_interval_ms,omitempty"`
	Send               *jobWatchToolSendArgs    `json:"send,omitempty"`
	// replaced_existing and fired serialize explicitly even when false: the
	// contract's install example shows replaced_existing:false, and §7.1
	// promises "fired=false on none" for terminal catch-up.
	ReplacedExisting bool   `json:"replaced_existing"`
	Fired            bool   `json:"fired"`
	TerminalCatchup  bool   `json:"terminal_catchup,omitempty"`
	Status           string `json:"status,omitempty"`
}

type jobWatchToolEventFilter struct {
	ToolName string `json:"tool_name,omitempty"`
	Status   string `json:"status,omitempty"`
}

type jobWatchListToolResult struct {
	Watches       []jobWatchInspectToolResult `json:"watches"`
	RecentWatches []jobWatchInspectToolResult `json:"recent_watches,omitempty"`
	Count         int                         `json:"count"`
}

type jobWatchInspectToolResult struct {
	WatchID    string `json:"watch_id"`
	Source     string `json:"source,omitempty"`
	Watching   bool   `json:"watching"`
	Condition  string `json:"condition,omitempty"`
	Deliveries int    `json:"deliveries,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	EndReason  string `json:"end_reason,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}

type jobWatchToolSendArgs struct {
	To             string `json:"to"`
	Message        string `json:"message,omitempty"`
	IncludeExcerpt bool   `json:"include_excerpt,omitempty"`
}

type delegateToolResult struct {
	DelegateID             string           `json:"delegate_id,omitempty"`
	StartedJobID           string           `json:"started_job_id,omitempty"`
	JobID                  string           `json:"job_id"`
	LatestJobID            string           `json:"latest_job_id,omitempty"`
	Type                   string           `json:"type"`
	Status                 string           `json:"status"`
	Reason                 *string          `json:"reason,omitempty"`
	ExhaustionBudget       string           `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit        int              `json:"exhaustion_limit,omitempty"`
	Resumable              *bool            `json:"resumable,omitempty"`
	RunningInBackground    bool             `json:"running_in_background"`
	TimedOut               bool             `json:"timed_out"`
	TranscriptRef          string           `json:"transcript_ref"`
	Output                 *string          `json:"output,omitempty"`
	Truncated              *bool            `json:"truncated,omitempty"`
	StructuredResult       any              `json:"structured_result,omitempty"`
	StructuredResultValid  *bool            `json:"structured_result_valid,omitempty"`
	StructuredResultReason string           `json:"structured_result_reason,omitempty"`
	Watching               bool             `json:"watching,omitempty"`
	Watches                []watchListEntry `json:"watches,omitempty"`
	// Worktree carries the isolation lane's path/branch/ahead/dirty state for
	// an isolated delegate's terminal job (native worktree tools spec §9
	// lifecycle step 3); nil for a non-isolated delegate.
	Worktree *delegateWorktreeToolResult `json:"worktree,omitempty"`
	// Sandbox echoes the delegate's enforced box (mode + network) so the parent can
	// verify the child's actual confinement; nil for an unsandboxed (off) delegate.
	Sandbox *delegateSandboxToolResult `json:"sandbox,omitempty"`
	// Model echoes the resolved "provider/model" the delegate actually ran with
	// (captured at spawn, an explicit model arg pin, or the persisted descriptor
	// model on restore); empty when unavailable.
	Model string `json:"model,omitempty"`
}

// delegateWorktreeToolResult is the tool-facing shape of delegateWorktreeReport
// (native worktree tools spec §9 lifecycle step 3).
type delegateWorktreeToolResult struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
	Ahead   int    `json:"ahead_commits"`
	Dirty   bool   `json:"dirty"`
	// DisposalHint carries the spec §P2 completion nudge. It MUST be exported —
	// an unexported field is silently dropped by encoding/json (roborev finding
	// 2718-2). Empty (and omitted) unless the receiving session has the dispose
	// op and owns the delegate.
	DisposalHint string `json:"disposal_hint,omitempty"`
}

func delegateWorktreeToolResultFrom(wt *delegateWorktreeReport) *delegateWorktreeToolResult {
	if wt == nil {
		return nil
	}
	return &delegateWorktreeToolResult{Path: wt.Path, Branch: wt.Branch, HeadSHA: wt.HeadSHA, Ahead: wt.Ahead, Dirty: wt.Dirty, DisposalHint: wt.DisposalHint}
}

type delegateSandboxToolResult struct {
	Mode    string `json:"mode"`
	Network bool   `json:"network"`
}

func delegateSandboxToolResultFrom(sb *delegateSandboxReport) *delegateSandboxToolResult {
	if sb == nil {
		return nil
	}
	return &delegateSandboxToolResult{Mode: sb.Mode, Network: sb.Network}
}

func marshalDelegateSendResult(res sendMessageResult, maxChars int) (any, error) {
	_ = maxChars
	if res.MessageType == "runtime" {
		out := delegateSendResult{
			Type:                res.MessageType,
			Status:              deliveredStatus(res.Delivered),
			RunningInBackground: false,
			Action:              res.Action,
		}
		return tool.StateResult{Output: formatDelegateSend(out), State: out}, nil
	}
	out := delegateSendResult{
		DelegateID:          res.DelegateID,
		StartedJobID:        res.StartedJobID,
		CurrentJobID:        res.JobID,
		LatestJobID:         res.LatestJobID,
		Type:                res.Type,
		Status:              string(res.Status),
		Reason:              stringPtrOrNil(res.Reason),
		ExhaustionBudget:    res.ExhaustionBudget,
		ExhaustionLimit:     res.ExhaustionLimit,
		Resumable:           res.Resumable,
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		Action:              res.Action,
		TranscriptRef:       res.TranscriptRef,
		Watching:            res.Watching,
		Watches:             res.Watches,
		Worktree:            delegateWorktreeToolResultFrom(res.Worktree),
		WaitIgnoredReason:   res.WaitIgnoredReason,
	}
	if !res.RunningInBackground || res.TimedOut {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
	}
	if res.StructuredResult != nil || res.StructuredResultValidSet {
		valid := res.StructuredResultValid
		out.StructuredResult = res.StructuredResult
		out.StructuredResultValid = &valid
		out.StructuredResultReason = res.StructuredResultReason
	}
	return tool.StateResult{Output: formatDelegateSend(out), State: out}, nil
}

func deliveredStatus(delivered bool) string {
	if delivered {
		return "delivered"
	}
	return "not_delivered"
}

// formatDelegateSend renders a delegate send/steer/start result: any reply output,
// a bracketed footer, and the structured_result (JSON, genuinely structured) when
// present.
func formatDelegateSend(out delegateSendResult) string {
	var b strings.Builder
	if out.Output != nil && *out.Output != "" {
		b.WriteString(*out.Output)
		if !strings.HasSuffix(*out.Output, "\n") {
			b.WriteByte('\n')
		}
	}
	foot := []string{out.Action}
	if out.DelegateID != "" {
		foot = append([]string{"delegate_id " + out.DelegateID}, foot...)
	}
	if out.StartedJobID != "" {
		foot = append(foot, "started_job_id "+out.StartedJobID)
	}
	if out.Status != "" {
		foot = append(foot, out.Status)
	}
	if out.RunningInBackground {
		foot = append(foot, "running in background")
	}
	if out.Watching {
		foot = append(foot, "watching")
	}
	if out.WaitIgnoredReason != "" {
		foot = append(foot, "wait ignored: "+out.WaitIgnoredReason)
	}
	b.WriteString("[")
	b.WriteString(strings.Join(foot, " · "))
	b.WriteString("]")
	if len(out.Watches) > 0 {
		b.WriteString("\nwatches:")
		for _, w := range out.Watches {
			fmt.Fprintf(&b, "\n- %s → %s (%s)", w.ID, w.Source, w.Condition)
		}
	}
	if out.Worktree != nil {
		fmt.Fprintf(&b, "\nworktree: path=%s, branch=%s, head=%s, %d commits ahead, dirty=%t",
			out.Worktree.Path, out.Worktree.Branch, out.Worktree.HeadSHA,
			out.Worktree.Ahead, out.Worktree.Dirty)
	}
	if out.StructuredResult != nil {
		if sr, err := json.Marshal(out.StructuredResult); err == nil {
			valid := out.StructuredResultValid != nil && *out.StructuredResultValid
			fmt.Fprintf(&b, "\nstructured_result (valid=%v): %s", valid, sr)
		}
	}
	if out.Worktree != nil && out.Worktree.DisposalHint != "" {
		fmt.Fprintf(&b, "\ndisposal_hint: %s", out.Worktree.DisposalHint)
	}
	return b.String()
}

func marshalWatchResult(res watchResult, maxChars int) (any, error) {
	_ = maxChars
	out := jobWatchToolResult{
		WatchID:            res.WatchID,
		Source:             watchPublicSource(res.Source, res.Target),
		Watching:           res.Watching,
		OutputMatch:        res.OutputMatch,
		Events:             res.Events,
		ProgressIntervalMS: res.ProgressIntervalMS,
		ReplacedExisting:   res.ReplacedExisting,
		Fired:              res.Fired,
		TerminalCatchup:    res.TerminalCatchup,
		Status:             res.Status,
	}
	if res.EventFilter != nil {
		out.EventFilter = &jobWatchToolEventFilter{
			ToolName: res.EventFilter.ToolName,
			Status:   res.EventFilter.Status,
		}
	}
	if res.Send != nil {
		out.Send = &jobWatchToolSendArgs{
			To:             res.Send.To,
			Message:        res.Send.Message,
			IncludeExcerpt: res.Send.IncludeExcerpt,
		}
	}
	return tool.StateResult{Output: formatJobWatch(out), State: out}, nil
}

func marshalWatchListResult(res jobWatchListToolResult, maxChars int) (any, error) {
	_ = maxChars
	return tool.StateResult{Output: formatJobWatchList(res), State: res}, nil
}

func marshalWatchInspectResult(res jobWatchInspectToolResult, maxChars int) (any, error) {
	_ = maxChars
	return tool.StateResult{Output: formatJobWatchInspect(res), State: res}, nil
}

// formatJobWatch renders a job_watch result as a one-line footer summarizing the
// watch's source, trigger condition, and disposition.
func formatJobWatch(out jobWatchToolResult) string {
	if out.TerminalCatchup {
		parts := []string{"watch on " + out.Source, "terminal catch-up"}
		if out.Fired {
			parts = append(parts, "fired")
		} else {
			parts = append(parts, "not fired")
		}
		if out.Status != "" {
			parts = append(parts, out.Status)
		}
		return "[" + strings.Join(parts, " · ") + "]"
	}
	if !out.Watching {
		if out.Source == "" && out.WatchID != "" {
			return fmt.Sprintf("[watch_id %s cleared]", out.WatchID)
		}
		parts := []string{"watch on " + out.Source + " cleared"}
		if out.WatchID != "" {
			parts = append(parts, "watch_id "+out.WatchID)
		}
		return "[" + strings.Join(parts, " · ") + "]"
	}
	parts := []string{"watching " + out.Source}
	if out.WatchID != "" {
		parts = append(parts, "watch_id "+out.WatchID)
	}
	var cond []string
	if out.OutputMatch != "" {
		cond = append(cond, "output_match: "+out.OutputMatch)
	}
	if len(out.Events) > 0 {
		events := "events: " + strings.Join(out.Events, ",")
		if filter := formatJobWatchEventFilter(out.EventFilter); filter != "" {
			events += " " + filter
		}
		cond = append(cond, events)
	}
	if out.ProgressIntervalMS > 0 {
		cond = append(cond, fmt.Sprintf("every %dms", out.ProgressIntervalMS))
	}
	if len(cond) > 0 {
		parts = append(parts, strings.Join(cond, " "))
	}
	if out.Send != nil && out.Send.To != "" {
		parts = append(parts, "→ "+out.Send.To)
	}
	if out.ReplacedExisting {
		parts = append(parts, "replaced existing")
	}
	if out.Fired {
		parts = append(parts, "fired")
	}
	if out.Status != "" {
		parts = append(parts, out.Status)
	}
	return "[" + strings.Join(parts, " · ") + "]"
}

func formatJobWatchEventFilter(filter *jobWatchToolEventFilter) string {
	if filter == nil {
		return ""
	}
	var parts []string
	if filter.ToolName != "" {
		parts = append(parts, "tool_name="+filter.ToolName)
	}
	if filter.Status != "" {
		parts = append(parts, "status="+filter.Status)
	}
	if len(parts) == 0 {
		return ""
	}
	return "where " + strings.Join(parts, ",")
}

func formatJobWatchList(out jobWatchListToolResult) string {
	if len(out.Watches) == 0 && len(out.RecentWatches) == 0 {
		return "no watches"
	}
	var b strings.Builder
	for _, w := range out.Watches {
		status := "watching"
		if !w.Watching {
			status = "pending"
		}
		fmt.Fprintf(&b, "%s  %s  %s", w.WatchID, status, w.Source)
		if w.Condition != "" {
			fmt.Fprintf(&b, "  %s", w.Condition)
		}
		b.WriteByte('\n')
	}
	for _, w := range out.RecentWatches {
		fmt.Fprintf(&b, "%s  %s  %s", w.WatchID, w.EndReason, w.Source)
		if w.Condition != "" {
			fmt.Fprintf(&b, "  %s", w.Condition)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatJobWatchInspect(out jobWatchInspectToolResult) string {
	if out.Watching {
		parts := []string{"watching " + out.Source}
		if out.Condition != "" {
			parts = append(parts, out.Condition)
		}
		return out.WatchID + "  " + strings.Join(parts, "  ")
	}
	if out.EndReason != "" {
		return fmt.Sprintf("%s  %s  %s", out.WatchID, out.EndReason, out.Source)
	}
	if out.Source != "" {
		parts := []string{"pending", out.Source}
		if out.Condition != "" {
			parts = append(parts, out.Condition)
		}
		return out.WatchID + "  " + strings.Join(parts, "  ")
	}
	return out.WatchID + "  not found"
}

func marshalDelegateResult(res delegateResult, maxChars int) (string, error) {
	out := delegateToolResult{
		DelegateID:          res.DelegateID,
		StartedJobID:        res.StartedJobID,
		JobID:               res.JobID,
		LatestJobID:         res.LatestJobID,
		Type:                res.Type,
		Status:              string(res.Status),
		Reason:              stringPtrOrNil(res.Reason),
		ExhaustionBudget:    res.ExhaustionBudget,
		ExhaustionLimit:     res.ExhaustionLimit,
		Resumable:           res.Resumable,
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		TranscriptRef:       res.TranscriptRef,
		Watching:            res.Watching,
		Watches:             res.Watches,
		Worktree:            delegateWorktreeToolResultFrom(res.Worktree),
		Sandbox:             delegateSandboxToolResultFrom(res.Sandbox),
		Model:               res.Model,
	}
	if !res.RunningInBackground || res.TimedOut {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
	}
	if res.StructuredResult != nil || res.StructuredResultValidSet {
		valid := res.StructuredResultValid
		out.StructuredResult = res.StructuredResult
		out.StructuredResultValid = &valid
		out.StructuredResultReason = res.StructuredResultReason
	}
	return marshalBoundedDelegateResult(out, maxChars)
}

func marshalBoundedDelegateResult(out delegateToolResult, maxChars int) (string, error) {
	if fit, ok, err := marshalDelegateResultWithOutputLimit(out, maxChars); err != nil || ok {
		return fit, err
	}
	empty := ""
	out.Output = &empty
	truncated := true
	out.Truncated = &truncated
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	if out.StructuredResult != nil {
		out.StructuredResult = nil
		invalid := false
		out.StructuredResultValid = &invalid
		out.StructuredResultReason = structuredResultReasonProjectionTooLarge
	}
	return marshalBoundedJSON(out, maxChars)
}

func marshalDelegateResultWithOutputLimit(out delegateToolResult, maxChars int) (string, bool, error) {
	if out.Output == nil {
		return marshalBoundedJSONWithFit(out, maxChars)
	}
	original := []rune(*out.Output)
	originalTruncated := out.Truncated != nil && *out.Truncated
	return marshalWithOutputLimit(maxChars, len(original), func(keep int) (string, error) {
		tail := string(original[len(original)-keep:])
		out.Output = &tail
		truncated := originalTruncated || keep < len(original)
		out.Truncated = &truncated
		b, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}

func marshalWithOutputLimit(maxChars, outputRunes int, marshal func(keep int) (string, error)) (string, bool, error) {
	best := ""
	bestOK := false
	lo, hi := 0, outputRunes
	for lo <= hi {
		mid := lo + (hi-lo)/2
		candidate, err := marshal(mid)
		if err != nil {
			return "", false, err
		}
		if maxChars <= 0 || jsonCharLen([]byte(candidate)) <= maxChars {
			best = candidate
			bestOK = true
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, bestOK, nil
}

func sessionJobManager(s *Session) (*jobManager, error) {
	if s == nil || s.jobManager == nil {
		return nil, errors.New(jobManagerUnavailableReason)
	}
	return s.jobManager, nil
}

// sessionRunningJobIDs lists this session's own running (session-launched,
// non-nested) job ids, in job_list's default order. Used by the communicate
// end_turn running-job warning; returns nil when the session has no job
// manager or no running jobs.
func sessionRunningJobIDs(s *Session) []string {
	jm, err := sessionJobManager(s)
	if err != nil {
		return nil
	}
	recs := jm.list(listFilter{Status: jobstore.StatusRunning})
	if len(recs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.JobID)
	}
	return ids
}

func jobListFilterFromArgs(args map[string]any) (listFilter, error) {
	limit := defaultJobListLimit
	if n, ok := shellIntArg(args, "limit"); ok {
		limit = n
	}
	if limit <= 0 {
		return listFilter{}, errors.New("limit must be greater than 0")
	}
	if limit > maxJobListLimit {
		limit = maxJobListLimit
	}
	offset := 0
	if n, ok := shellIntArg(args, "offset"); ok {
		offset = n
	}
	if offset < 0 {
		return listFilter{}, errors.New("offset must be non-negative")
	}

	statuses, err := jobStatusArrayArg(args, "status")
	if err != nil {
		return listFilter{}, err
	}
	types, err := jobTypeArrayArg(args, "type")
	if err != nil {
		return listFilter{}, err
	}
	return listFilter{
		Statuses:           statuses,
		Types:              types,
		Limit:              limit,
		Offset:             offset,
		IncludeNested:      shellBoolArg(args, "include_nested"),
		IncludeDescendants: shellBoolArg(args, "include_descendants"),
	}, nil
}

func jobStatusArrayArg(args map[string]any, key string) ([]jobstore.Status, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	statuses := make([]jobstore.Status, 0, len(values))
	for _, value := range values {
		status := jobstore.Status(fmt.Sprint(value))
		switch status {
		case jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusFailed, jobstore.StatusExhausted, jobstore.StatusCancelled, jobstore.StatusStopped:
			statuses = append(statuses, status)
		default:
			return nil, fmt.Errorf("invalid job status %q", status)
		}
	}
	return statuses, nil
}

func watchArgsFromToolArgs(args map[string]any) (watchArgs, error) {
	operation := strings.TrimSpace(stringArg(args, "operation"))
	if operation == "" {
		return watchArgs{}, errors.New("invalid_request: operation is required")
	}
	if _, ok := args["target"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch uses source, not target")
	}
	if _, ok := args["send"]; ok {
		return watchArgs{}, errors.New("invalid_request: job_watch delivers to the watcher automatically; send is not a public argument")
	}
	a := watchArgs{
		Operation:   operation,
		WatchID:     strings.TrimSpace(stringArg(args, "watch_id")),
		Source:      strings.TrimSpace(stringArg(args, "source")),
		OutputMatch: stringArg(args, "output_match"),
	}
	if n, ok := shellIntArg(args, "progress_interval_ms"); ok {
		a.ProgressIntervalMS = n
	}
	events, err := stringArrayArg(args, "events")
	if err != nil {
		return watchArgs{}, err
	}
	a.Events = events
	if n, ok := shellIntArg(args, "every"); ok {
		a.Every = n
	}
	eventFilter, err := watchEventFilterArg(args)
	if err != nil {
		return watchArgs{}, err
	}
	a.EventFilter = eventFilter
	switch a.Operation {
	case "create":
		if a.Source == "" {
			return watchArgs{}, errors.New("invalid_request: source is required")
		}
		if strings.HasPrefix(a.Source, "dlg_") {
			return watchArgs{}, errors.New("invalid_request: delegate_id is a conversation handle; watch source self, parent, or a concrete job_id")
		}
	case "list":
		if a.Source != "" || a.WatchID != "" {
			return watchArgs{}, errors.New("invalid_request: list requires no source or watch_id")
		}
	case "inspect", "clear":
		if a.WatchID == "" {
			return watchArgs{}, errors.New("invalid_request: watch_id is required")
		}
	default:
		return watchArgs{}, fmt.Errorf("invalid_request: unsupported operation %q", a.Operation)
	}
	if a.Source == "*" {
		return watchArgs{}, errors.New("invalid_request: wildcard watch target is not supported in v1")
	}
	return a, nil
}

func watchEventFilterArg(args map[string]any) (*watchEventFilter, error) {
	raw, ok := args["event_filter"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("event_filter must be an object")
	}
	var filter watchEventFilter
	for key, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid_request: event_filter.%s must be a string", key)
		}
		switch key {
		case "tool_name":
			filter.ToolName = strings.TrimSpace(s)
		case "status":
			filter.Status = strings.TrimSpace(s)
		default:
			return nil, fmt.Errorf("invalid_request: unknown event_filter field %q", key)
		}
	}
	if filter.ToolName == "" && filter.Status == "" {
		return nil, nil
	}
	return &filter, nil
}

func stringArrayArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s values must be strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

//nolint:unused // retained for internal watch-send parsing; public job_watch currently rejects send.
func watchSendArg(args map[string]any) (*watchSendArgs, error) {
	raw, ok := args["send"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("send must be an object")
	}
	if isEmptyWatchSend(values) {
		return nil, nil
	}
	to := strings.TrimSpace(stringArg(values, "to"))
	if to == "" {
		return nil, errors.New("invalid_request: send.to is required")
	}
	return &watchSendArgs{
		To:             to,
		Message:        stringArg(values, "message"),
		IncludeExcerpt: shellBoolArg(values, "include_excerpt"),
	}, nil
}

//nolint:unused // retained with watchSendArg for internal watch-send parsing.
func isEmptyWatchSend(values map[string]any) bool {
	return strings.TrimSpace(stringArg(values, "to")) == "" &&
		stringArg(values, "message") == "" &&
		!shellBoolArg(values, "include_excerpt")
}

func jobTypeArrayArg(args map[string]any, key string) ([]jobstore.JobType, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	types := make([]jobstore.JobType, 0, len(values))
	for _, value := range values {
		jobType := jobstore.JobType(fmt.Sprint(value))
		switch jobType {
		case jobstore.JobShell, jobstore.JobDelegate:
			types = append(types, jobType)
		default:
			return nil, fmt.Errorf("invalid job type %q", jobType)
		}
	}
	return types, nil
}

func findJobRecord(jm *jobManager, jobID string) (*jobstore.JobRecord, error) {
	recs, _, err := jm.listWithError(listFilter{IncludeNested: true})
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		if rec.JobID == jobID {
			return rec, nil
		}
	}
	return nil, errJobNotFound(jobID)
}

func waitForJobDone(ctx context.Context, jm *jobManager, jobID string, timeout time.Duration) bool {
	done, ok := jobDone(jm, jobID)
	if !ok {
		return true
	}
	timer := jm.clock.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C():
		return false
	case <-ctx.Done():
		return false
	}
}

func jobDone(jm *jobManager, jobID string) (<-chan struct{}, bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		return nil, false
	}
	return run.done, true
}

func projectJobRecord(s *Session, rec *jobstore.JobRecord) jobListEntry {
	return projectJobRecordForViewer(s, s, rec)
}

func projectJobRecordForViewer(viewer *Session, assessor *Session, rec *jobstore.JobRecord) jobListEntry {
	resumable := rec.Resumable
	notResumableReason := stringPtrOrNil(rec.NotResumableWhy)
	delegateID := rec.DelegateID
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.id
	}
	effectiveOwnerID := rec.OwnerSessionID
	if effectiveOwnerID == "" && assessor != nil {
		effectiveOwnerID = assessor.id
	}
	if !delegateControlOwnedBySession(effectiveOwnerID, viewerID) {
		delegateID = ""
	}
	if assessor == nil {
		assessor = viewer
	}
	now := time.Now()
	if viewer != nil && viewer.clock != nil {
		now = viewer.clock.Now()
	}
	if assessor != nil && assessor.jobManager != nil {
		now = assessor.jobManager.now()
	}
	statusView := projectJobStatus(now, rec)
	if assessor != nil && isRuntimeLostDelegate(rec) {
		assessment := assessor.assessDelegateResumability(rec, delegateResumabilityProjection)
		resumableValue := assessment.Resumable
		resumable = &resumableValue
		if assessment.Resumable {
			notResumableReason = nil
		} else {
			notResumableReason = stringPtrOrNil(assessment.Reason)
		}
	}
	return jobListEntry{
		JobID:              rec.JobID,
		DelegateID:         delegateID,
		Kind:               statusView.Kind,
		Type:               string(rec.Type),
		Status:             string(rec.Status),
		Phase:              statusView.Phase,
		Reason:             stringPtrOrNil(rec.Reason),
		Description:        jobRecordDisplayLabel(rec),
		ParentJobID:        stringPtrOrNil(rec.ParentJobID),
		OwnerSessionID:     rec.OwnerSessionID,
		VisibleToSessionID: rec.VisibleToSession,
		TranscriptRef:      stringPtrOrNil(statusView.TranscriptRef),
		ExhaustionBudget:   rec.ExhaustionBudget,
		ExhaustionLimit:    rec.ExhaustionLimit,
		Resumable:          resumable,
		NotResumableReason: notResumableReason,
		StartedAt:          rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:            timePtrOrNil(rec.EndedAt),
		RunningForMS:       statusView.RunningForMS,
		DurationMS:         statusView.DurationMS,
		QuietForMS:         statusView.QuietForMS,
		LastEventAt:        statusView.LastEventAt,
		LastActivity:       lastActivityProjection(rec),
		ExitCode:           rec.ExitCode,
		TotalBytes:         rec.OutputBytes,
		Command:            stringPtrOrNil(rec.Command),
	}
}

func projectDelegateRecord(rec *jobstore.DelegateRecord) delegateListEntry {
	if rec == nil {
		return delegateListEntry{}
	}
	// A disposed delegate is never resumable regardless of the durably-recorded
	// Resumable flag (delegate-lane disposal spec §P1): its isolation lane is
	// gone, so present it as not-resumable with the disposal reason.
	resumable := rec.Resumable
	notResumableWhy := rec.NotResumableWhy
	if rec.Disposed {
		resumable = false
		notResumableWhy = notResumableWorktreeDisposed
	}
	return delegateListEntry{
		DelegateID:       rec.DelegateID,
		Status:           string(rec.Status),
		CurrentJobID:     rec.CurrentJobID,
		LatestJobID:      rec.LatestJobID,
		TranscriptRef:    rec.TranscriptRef,
		Resumable:        resumable,
		NotResumableWhy:  notResumableWhy,
		ParentDelegateID: rec.ParentDelegateID,
	}
}

func marshalBoundedJSON(v any, maxChars int) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if maxChars > 0 && jsonCharLen(b) > maxChars {
		return "", fmt.Errorf("job tool JSON output exceeds %d characters after bounding", maxChars)
	}
	return string(b), nil
}

// marshalBoundedJSONWithFit marshals v and returns (json, true, nil) when it fits
// maxChars, or ("", false, nil) when it does not — letting the caller progressively
// drop fields to fit. Used by the delegate result path, which keeps JSON output.
func marshalBoundedJSONWithFit(v any, maxChars int) (string, bool, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", false, err
	}
	if maxChars <= 0 || jsonCharLen(b) <= maxChars {
		return string(b), true, nil
	}
	return "", false, nil
}

func jobToolResultMaxChars(reg *tool.Registry, name string) int {
	if reg == nil {
		return jobToolResultDefaultMaxChar
	}
	registered := reg.Get(name)
	if registered == nil || registered.Limit.MaxChars <= 0 {
		return jobToolResultDefaultMaxChar
	}
	if registered.Limit.MaxChars < jobToolResultMinJSONChars {
		return jobToolResultMinJSONChars
	}
	return registered.Limit.MaxChars
}

func enforceJobToolJSONLimits(reg *tool.Registry) {
	if reg == nil {
		return
	}
	overrides := map[string]schema.ToolOutputLimit{}
	for _, name := range []string{"job_status", "job_list", "job_stop", "delegate", "job_watch", "delegate_send"} {
		registered := reg.Get(name)
		if registered == nil || registered.Limit.MaxChars >= jobToolResultMinJSONChars {
			continue
		}
		overrides[name] = schema.ToolOutputLimit{MaxChars: jobToolResultMinJSONChars, Strategy: registered.Limit.Strategy}
	}
	if len(overrides) > 0 {
		reg.OverrideLimits(overrides)
	}
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func timePtrOrNil(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.Format(time.RFC3339Nano)
	return &formatted
}

func int64Ptr(value int64) *int64 {
	return &value
}

func publicJobKind(t jobstore.JobType) string {
	if t == jobstore.JobDelegate {
		return jobKindAgent
	}
	return jobKindShell
}

func shellTranscriptRef(jobID string) string {
	return "job:" + jobID
}

func defaultJobPhase(rec *jobstore.JobRecord) string {
	if rec == nil || rec.Status.IsTerminal() {
		return ""
	}
	if rec.Phase != "" {
		return rec.Phase
	}
	switch rec.Type {
	case jobstore.JobDelegate:
		return jobPhaseStarting
	case jobstore.JobShell:
		return jobPhaseProcessRunning
	default:
		return ""
	}
}

func jobTranscriptRef(rec *jobstore.JobRecord) string {
	if rec == nil {
		return ""
	}
	if rec.TranscriptRef != "" {
		return rec.TranscriptRef
	}
	if rec.Type == jobstore.JobShell && rec.JobID != "" {
		return shellTranscriptRef(rec.JobID)
	}
	return ""
}

func projectJobStatus(now time.Time, rec *jobstore.JobRecord) jobStatusResult {
	last := rec.StartedAt
	if rec.LastActivity != nil {
		last = *rec.LastActivity
	} else if rec.EndedAt != nil {
		last = *rec.EndedAt
	}

	out := jobStatusResult{
		JobID:            rec.JobID,
		Kind:             publicJobKind(rec.Type),
		Status:           string(rec.Status),
		Description:      jobRecordDisplayLabel(rec),
		Phase:            defaultJobPhase(rec),
		Reason:           stringPtrOrNil(rec.Reason),
		ExhaustionBudget: rec.ExhaustionBudget,
		ExhaustionLimit:  rec.ExhaustionLimit,
		Resumable:        rec.Resumable,
		StartedAt:        rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:          timePtrOrNil(rec.EndedAt),
		LastEventAt:      timePtrOrNil(&last),
		TranscriptRef:    jobTranscriptRef(rec),
		ExitCode:         rec.ExitCode,
	}
	if rec.Status.IsTerminal() {
		end := now
		if rec.EndedAt != nil {
			end = *rec.EndedAt
		}
		out.DurationMS = int64Ptr(end.Sub(rec.StartedAt).Milliseconds())
		out.Phase = ""
	} else {
		out.RunningForMS = int64Ptr(now.Sub(rec.StartedAt).Milliseconds())
		out.QuietForMS = int64Ptr(now.Sub(last).Milliseconds())
	}
	return out
}

// lastActivityProjection renders a record's last_activity timestamp. A running
// job carries a live LastActivity stamp. A terminal record reloaded from the
// store has no stamp (LastActivity is in-memory only, never folded), so it
// falls back to the most recent activity it can attest: EndedAt, then
// StartedAt.
func lastActivityProjection(rec *jobstore.JobRecord) *string {
	if rec.LastActivity != nil {
		return timePtrOrNil(rec.LastActivity)
	}
	if rec.EndedAt != nil {
		return timePtrOrNil(rec.EndedAt)
	}
	started := rec.StartedAt
	return timePtrOrNil(&started)
}
