package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
)

func jobStatusTool(s *Session, args map[string]any, maxChars int) (any, error) {
	target := strings.TrimSpace(stringArg(args, "target"))
	if target == "" {
		return "", errors.New("invalid_request: target is required")
	}
	if strings.HasPrefix(target, "dlg_") {
		return stableDelegateStatusTool(s, target, maxChars)
	}
	jm, rec, err := s.nestedOrLocalJobManager(target)
	if err != nil {
		return "", err
	}
	if string(rec.Type) == delegateResourceType {
		return "", fmt.Errorf("legacy_delegate_activation: %s is a retired delegate activation alias; use its stable delegate_id", target)
	}
	if live, liveErr := findJobRecord(jm, target); liveErr == nil {
		rec = live
	}
	out := projectJobStatus(jm.now(), rec)
	rendered, err := marshalBoundedJSON(out, maxChars)
	if err != nil {
		return "", err
	}
	return tool.StateResult{Output: rendered, State: out}, nil
}

func stableDelegateStatusTool(s *Session, delegateID string, maxChars int) (any, error) {
	if s == nil || s.delegateController == nil {
		return "", errors.New("delegate controller is unavailable")
	}
	rows := stableDelegateRowsForSession(s, true)
	var row *stableDelegateVisibleRow
	for i := range rows {
		if rows[i].snapshot.id == delegateID {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return "", fmt.Errorf("not_controllable: stable delegate %s is not visible to this session", delegateID)
	}
	now := s.sclock().Now()
	out := projectStableDelegateStatus(now, row.snapshot)
	rendered, err := marshalBoundedJSON(out, maxChars)
	if err != nil {
		return "", err
	}
	return tool.StateResult{Output: rendered, State: out}, nil
}

// consumeTerminalJobNotification settles the terminal notification after its
// job_status result is durable in the session transcript. At that point the
// caller has learned the job ended, so waking it later to say the same thing is
// an interruption that carries no news.
//
// It is recorded durably as its own state (consumed, not delivered) so the
// told-the-caller invariant stays true without claiming a notification turn
// that never happened — evener-doctor can still tell the two apart.
//
// Only the OWNER's own reads consume. A parent's forwarded copy of a
// child-owned pending is a drive signal, not the parent's news to hear:
// settling it there would silence the child's own undelivered notification.
func consumeTerminalJobNotification(s *Session, jm *jobManager, rec *jobstore.JobRecord) {
	if jm == nil || rec == nil || rec.TerminalGen == "" {
		return
	}
	// not_armed is a terminal job whose notification was never armed: a kept
	// foreground shell, or one a crash stopped before arming. The caller has
	// learned it ended either way, so it settles just as a pending one does.
	if !rec.Status.IsTerminal() || (rec.NotifyState != jobstore.NotifyPending && rec.NotifyState != jobstore.NotifyNotArmed) {
		return
	}
	ownerSessionID := rec.OwnerSessionID
	if ownerSessionID == "" {
		ownerSessionID = jm.sessionID
	}
	if ownerSessionID != s.id {
		return
	}
	consumed := jobstore.Event{
		Kind:        jobstore.EventJobNotificationConsumed,
		TS:          jm.now(),
		JobID:       rec.JobID,
		TerminalGen: rec.TerminalGen,
	}
	if err := jm.appendEvent(consumed); err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("job notification consume mark failed: %v", err)})
		return
	}
	rec.NotifyState = jobstore.NotifyConsumed
	if jm.consume != nil {
		jm.consume(rec.JobID)
	}
	// Settle the parent's forwarded COPY too, for the same reason a delivery
	// does: the copy is only a drive signal, and a signal nobody clears
	// re-drives forever.
	if err := jm.forwardSnapshot(consumed); err != nil {
		s.emit(events.WarningData{Message: fmt.Sprintf("job notification consume forward failed: %v", err)})
	}
}

func jobStopTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	target := strings.TrimSpace(stringArg(args, "target"))
	if target == "" {
		return "", errors.New("invalid_request: target is required")
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
	if strings.HasPrefix(target, "dlg_") {
		return stopStableDelegate(ctx, s, target, maxWaitMS)
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}

	targetJM := jm
	if routed, rec, err := s.nestedOrLocalJobManager(target); err == nil {
		if string(rec.Type) == delegateResourceType {
			return "", fmt.Errorf("legacy_delegate_activation: %s is a retired delegate activation alias; use its stable delegate_id", target)
		}
		targetJM = routed
	}
	var childStopErr error
	if shellBoolArg(args, "include_children") {
		_, childStopErr = s.stopChildren(target)
	}
	var previousStatus jobstore.Status
	if _, pre, lookupErr := s.nestedOrLocalJobManager(target); lookupErr == nil && pre != nil {
		previousStatus = pre.Status
	}
	rec, err := stopNestedOrLocalForJobStop(s, target)
	if err != nil {
		return "", errors.Join(childStopErr, err)
	}
	if maxWaitMS > 0 {
		done := waitForJobDone(ctx, targetJM, target, clampJobBlockTimeout(maxWaitMS))
		if _, latest, err := s.nestedOrLocalJobManager(target); err == nil {
			rec = latest
		}
		if !done && rec != nil && !rec.Status.IsTerminal() {
			pending := rec.Clone()
			pending.Reason = "stop_pending"
			rec = pending
		}
	}
	if childStopErr != nil {
		return "", childStopErr
	}
	_ = maxChars
	stop := jobStopResult{
		ID:             rec.JobID,
		JobID:          rec.JobID,
		Type:           string(rec.Type),
		Status:         string(rec.Status),
		Reason:         stringPtrOrNil(rec.Reason),
		PreviousStatus: string(previousStatus),
		Outcome:        classifyStopOutcome(previousStatus, rec),
	}
	return tool.StateResult{Output: formatJobStop(stop), State: stop}, nil
}

func stopStableDelegate(ctx context.Context, s *Session, delegateID string, maxWaitMS int) (any, error) {
	if s == nil || s.delegateController == nil {
		return "", errors.New("delegate controller is unavailable")
	}
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return "", err
	}
	result, cancelPlan, plans, err := s.delegateController.StopSubtreeAndDrive(ctx, actor, delegateID)
	if err != nil {
		return "", err
	}
	// #655: report the watches that survive this stop and keep delivering to
	// the stopped delegate subtree. One read, taken at result time — after the
	// wait/timeout decision — so the reported set is the current one on every
	// path (settled, timed out, or request-and-return). The inventory covers
	// the subtree, not just the target, because a subtree stop leaves
	// descendant watches live too.
	executeDelegateCancelPlan(cancelPlan)
	if err := s.executeDelegateMutationPlans(plans); err != nil {
		return "", err
	}
	completed := false
	if maxWaitMS > 0 {
		completed = waitForDelegateStopDone(ctx, s, result.done, clampJobBlockTimeout(maxWaitMS))
	}
	live := s.liveWatchesDeliveringToDelegate(delegateID)
	stop := stableDelegateStopResult(result, completed, actor.describe())
	stop.LiveWatches = live
	if completed {
		populateDelegateStopEvidence(&stop, s, delegateID)
	}
	return tool.StateResult{Output: formatJobStop(stop), State: stop}, nil
}

// populateDelegateStopEvidence fills in the cancellation provenance kata tpb0
// asks for once a stop has actually completed: whether this exact delegate
// resource can still be resumed, and whatever partial scratch/worktree
// evidence its run loop had already gathered (preserved through FinishGeneration's
// PhaseStopping branch, see delegate_tree_finish.go). Reuses the same snapshot
// row job_status and job_list already project — no new read path.
func populateDelegateStopEvidence(stop *jobStopResult, s *Session, delegateID string) {
	for _, row := range stableDelegateRowsForSession(s, true) {
		if row.snapshot.id != delegateID {
			continue
		}
		resumable := row.snapshot.resumable
		stop.Resumable = &resumable
		stop.NotResumableReason = row.snapshot.notResumableReason
		if packet := row.snapshot.latestPacket; packet != nil && len(packet.Metadata) != 0 {
			var metadata delegateTerminalPacketMetadata
			if err := json.Unmarshal(packet.Metadata, &metadata); err == nil {
				stop.ScratchPath = metadata.ScratchPath
				if wt := metadata.Worktree; wt != nil {
					stop.Worktree = &delegateWorktreeToolResult{
						Path: wt.Path, Branch: wt.Branch, HeadSHA: wt.HeadSHA, Ahead: wt.Ahead, Dirty: wt.Dirty,
					}
				}
			}
		}
		return
	}
}

func waitForDelegateStopDone(ctx context.Context, s *Session, done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	default:
	}
	timer := s.sclock().NewTimer(timeout)
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

func stableDelegateStopResult(result delegateStopResult, completed bool, requestedBy string) jobStopResult {
	reason := "stop_pending"
	status := string(jobstore.StatusRunning)
	outcome := "stop_requested"
	if completed {
		reason = "stopped_by_parent"
		status = string(result.lifecycle)
		outcome = result.outcome
	}
	return jobStopResult{
		ID:             result.id,
		JobID:          result.id,
		Type:           "delegate",
		Status:         status,
		Reason:         &reason,
		PreviousStatus: string(result.previousLifecycle),
		Outcome:        outcome,
		RequestedBy:    requestedBy,
	}
}

var stopNestedOrLocalForJobStop = func(s *Session, jobID string) (*jobstore.JobRecord, error) {
	return s.stopNestedOrLocal(jobID)
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

type stableDelegateStatusResult struct {
	ID                 string                 `json:"id"`
	Type               string                 `json:"type"`
	Status             string                 `json:"status"`
	Name               string                 `json:"name,omitempty"`
	Task               string                 `json:"task"`
	Description        string                 `json:"description,omitempty"`
	AgentType          string                 `json:"agent_type"`
	Tools              []string               `json:"tools,omitempty"`
	Model              string                 `json:"model,omitempty"`
	ReasoningEffort    string                 `json:"reasoning_effort,omitempty"`
	Resumable          bool                   `json:"resumable"`
	NeedsAttention     bool                   `json:"needs_attention"`
	NotResumableReason string                 `json:"not_resumable_reason,omitempty"`
	TranscriptRef      string                 `json:"transcript_ref"`
	Cwd                string                 `json:"cwd,omitempty"`
	Isolation          string                 `json:"isolation,omitempty"`
	SandboxMode        string                 `json:"sandbox_mode,omitempty"`
	SandboxNetwork     *bool                  `json:"sandbox_network,omitempty"`
	RunStartedAt       string                 `json:"run_started_at,omitempty"`
	LatestActivityAt   string                 `json:"latest_activity_at,omitempty"`
	RunningForMS       *int64                 `json:"running_for_ms,omitempty"`
	QuietForMS         *int64                 `json:"quiet_for_ms,omitempty"`
	DurationMS         *int64                 `json:"duration_ms,omitempty"`
	LastOutcome        *delegatestore.Outcome `json:"last_outcome,omitempty"`
}

func projectStableDelegateStatus(now time.Time, snapshot delegateSnapshot) stableDelegateStatusResult {
	descriptor := snapshot.descriptor
	out := stableDelegateStatusResult{
		ID:                 snapshot.id,
		Type:               "delegate",
		Status:             string(snapshot.lifecycle),
		Name:               descriptor.Name,
		Task:               descriptor.Task,
		Description:        descriptor.Description,
		AgentType:          descriptor.AgentType,
		Tools:              append([]string(nil), descriptor.ToolNameCeiling...),
		Model:              descriptor.ResolvedModel,
		ReasoningEffort:    descriptor.Config.ReasoningEffort,
		Resumable:          snapshot.resumable,
		NeedsAttention:     snapshot.needsAttention,
		NotResumableReason: snapshot.notResumableReason,
		TranscriptRef:      snapshot.transcriptRef,
		LastOutcome:        snapshot.lastOutcome,
		Cwd:                descriptor.WorkingDir,
		Isolation:          descriptor.Isolation,
	}
	if descriptor.Sandbox != nil {
		out.SandboxMode = descriptor.Sandbox.Mode
		// A nil Network pointer means the sandbox did not explicitly disable
		// networking, so the effective default is enabled. Normalize to true
		// so the UI shows the effective setting rather than omitting it.
		net := true
		if descriptor.Sandbox.Network != nil {
			net = *descriptor.Sandbox.Network
		}
		out.SandboxNetwork = &net
	}
	if !snapshot.runStartedAt.IsZero() {
		out.RunStartedAt = snapshot.runStartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !snapshot.latestActivityAt.IsZero() {
		out.LatestActivityAt = snapshot.latestActivityAt.UTC().Format(time.RFC3339Nano)
	}
	if snapshot.currentRunOpen && !snapshot.runStartedAt.IsZero() {
		running := max(now.Sub(snapshot.runStartedAt).Milliseconds(), int64(0))
		out.RunningForMS = &running
		quietSince := snapshot.latestActivityAt
		if quietSince.IsZero() {
			quietSince = snapshot.runStartedAt
		}
		quiet := max(now.Sub(quietSince).Milliseconds(), int64(0))
		out.QuietForMS = &quiet
	} else if snapshot.lastOutcome != nil && !snapshot.runStartedAt.IsZero() && !snapshot.lastOutcome.EndedAt.IsZero() {
		duration := max(snapshot.lastOutcome.EndedAt.Sub(snapshot.runStartedAt).Milliseconds(), int64(0))
		out.DurationMS = &duration
	}
	return out
}

type jobStopResult struct {
	ID             string  `json:"id"`
	JobID          string  `json:"-"`
	Type           string  `json:"type"`
	Status         string  `json:"status"`
	Reason         *string `json:"reason"`
	PreviousStatus string  `json:"previous_status"`
	Outcome        string  `json:"outcome"`
	// RequestedBy, Resumable, NotResumableReason, ScratchPath, and Worktree are
	// delegate-only cancellation provenance (kata tpb0): who requested the
	// stop, whether the same delegate resource can still be resumed, and
	// whatever partial evidence its run loop had already gathered. All are
	// empty/nil for a shell job_stop. RequestedBy is known at admission and so
	// is reported on every delegate stop; the rest are read from the settled
	// delegate row and stay empty/nil until the stop completes (status/outcome
	// still "running"/"stop_requested").
	RequestedBy        string                      `json:"requested_by,omitempty"`
	Resumable          *bool                       `json:"resumable,omitempty"`
	NotResumableReason string                      `json:"not_resumable_reason,omitempty"`
	ScratchPath        string                      `json:"scratch_path,omitempty"`
	Worktree           *delegateWorktreeToolResult `json:"worktree,omitempty"`
	// LiveWatches is the #655 provenance: watches that survive this stop and
	// keep delivering to the stopped delegate (receiver-keyed). Reported on
	// every delegate stop — admission-time as well as settle-time — because
	// the parent otherwise has no way to learn they exist. Empty for a shell
	// job_stop or a delegate with no surviving watches.
	LiveWatches []watchListEntry `json:"live_watches,omitempty"`
}

// formatJobStop renders a job_stop result as a single plain-text line matching the
// job-family footer style: [job <id> · <status> · <outcome> · <reason>]. For a
// completed delegate stop it appends the cancellation provenance kata tpb0
// asks for: the requesting actor, the delegate's resumable classification,
// and any partial scratch/worktree evidence its run loop had already
// gathered before being cancelled.
func formatJobStop(out jobStopResult) string {
	id := out.ID
	if id == "" {
		id = out.JobID
	}
	parts := []string{out.Type + " " + id, out.Status, out.Outcome}
	if out.Reason != nil && *out.Reason != "" {
		parts = append(parts, *out.Reason)
	}
	if out.Type == "delegate" && out.PreviousStatus != "" {
		parts = append(parts, "was "+out.PreviousStatus)
	}
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(strings.Join(parts, " · "))
	b.WriteString("]")
	if out.Type != "delegate" {
		return b.String()
	}
	if out.RequestedBy != "" {
		fmt.Fprintf(&b, "\nrequested by: %s", out.RequestedBy)
	}
	if out.Resumable != nil {
		if *out.Resumable {
			b.WriteString("\nresumable: yes")
		} else {
			reason := out.NotResumableReason
			if reason == "" {
				reason = "not resumable"
			}
			fmt.Fprintf(&b, "\nresumable: no (%s)", reason)
		}
	}
	if out.ScratchPath != "" {
		fmt.Fprintf(&b, "\nscratch: %s", out.ScratchPath)
	}
	if out.Worktree != nil {
		fmt.Fprintf(&b, "\nworktree: path=%s, branch=%s, head=%s, %d commits ahead, dirty=%t",
			out.Worktree.Path, out.Worktree.Branch, out.Worktree.HeadSHA, out.Worktree.Ahead, out.Worktree.Dirty)
	}
	if n := len(out.LiveWatches); n > 0 {
		fmt.Fprintf(&b, "\nlive watches: %d still armed and delivering to this delegate", n)
		for i, row := range out.LiveWatches {
			if i >= 5 {
				fmt.Fprintf(&b, "\n  +%d more (see live_watches in state)", n-i)
				break
			}
			detail := row.Condition
			if detail == "" {
				detail = "no condition"
			}
			fmt.Fprintf(&b, "\n  %s · source=%s · %s · clear it (job_watch operation=\"clear\" watch_id=%s)", row.ID, row.Source, detail, row.ID)
		}
	}
	return b.String()
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

//go:fix inline
func publicJobKind(t jobstore.JobType) string {
	_ = t
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
		out.DurationMS = new(end.Sub(rec.StartedAt).Milliseconds())
		out.Phase = ""
	} else {
		out.RunningForMS = new(now.Sub(rec.StartedAt).Milliseconds())
		out.QuietForMS = new(now.Sub(last).Milliseconds())
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
