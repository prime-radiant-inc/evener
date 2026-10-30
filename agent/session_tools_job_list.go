package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/activitybound"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
)

func jobListTool(s *Session, args map[string]any, maxChars int) (any, error) {
	if _, hub := args["hub"]; hub {
		return nil, errors.New("hub discovery requires authenticated dispatch")
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	filter, err := jobListFilterFromArgs(args)
	if err != nil {
		return "", err
	}
	now := s.sclock().Now()
	items, err := stableJobListCandidates(s, jm, filter, now)
	if err != nil {
		return "", err
	}
	sort.Slice(items, func(i, j int) bool {
		left := jobListItemActivity(items[i])
		right := jobListItemActivity(items[j])
		if left.Equal(right) {
			return items[i].ID < items[j].ID
		}
		return left.After(right)
	})
	total := len(items)
	if filter.Offset > 0 {
		if filter.Offset >= len(items) {
			items = nil
		} else {
			items = items[filter.Offset:]
		}
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	s.mu.Lock()
	allowance := s.delegationAllowance
	s.mu.Unlock()
	result := jobListResult{
		Items:         items,
		Count:         len(items),
		Offset:        filter.Offset,
		Total:         total,
		TurnSlots:     turnSlotOccupancyOf(s),
		Watches:       jm.liveWatchSummaries(),
		RecentWatches: jm.recentWatchSummaries(),
	}
	// Allowance ≤ 1 can only grant 0 — a no-op knob; surface it only when it
	// actually enables fan-out (mirrors eliding the delegate schema param).
	if allowance > 1 {
		result.DelegationAllowance = allowance
	}
	return tool.StateResult{Output: formatJobList(result, maxChars), State: result}, nil
}

func stableJobListCandidates(s *Session, jm *jobManager, filter listFilter, now time.Time) ([]jobListEntry, error) {
	shellFilter := filter
	shellFilter.Status = ""
	shellFilter.Statuses = nil
	shellFilter.Type = ""
	shellFilter.Types = nil
	shellFilter.Offset = 0
	shellFilter.Limit = 0

	var shellItems []jobListEntry
	if filter.IncludeDescendants {
		rows, _, err := s.walkDescendantJobs(shellFilter)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Type != string(jobstore.JobShell) {
				continue
			}
			row.ID = row.JobID
			shellItems = append(shellItems, row)
		}
	} else {
		records, _, err := jm.listWithError(shellFilter)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if record.Type != jobstore.JobShell {
				continue
			}
			row := projectJobRecordAt(s, s, record, now)
			row.ID = row.JobID
			shellItems = append(shellItems, row)
		}
	}

	items := make([]jobListEntry, 0, len(shellItems))
	for _, item := range shellItems {
		if stableJobListItemMatches(item, filter) {
			items = append(items, item)
		}
	}
	includeStableDescendants := filter.IncludeNested || filter.IncludeDescendants
	for _, visible := range delegateRowsForSession(s, includeStableDescendants) {
		item := projectDelegateListItem(now, visible)
		if stableJobListItemMatches(item, filter) {
			items = append(items, item)
		}
	}
	return items, nil
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

type delegateVisibleRow struct {
	snapshot delegateSnapshot
	depth    int
}

func delegateRowsForSession(s *Session, includeDescendants bool) []delegateVisibleRow {
	if s == nil || s.delegateController == nil {
		return nil
	}
	snapshots := s.delegateController.Snapshot().rows
	byID := make(map[string]delegateSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byID[snapshot.id] = snapshot
	}

	depths := make(map[string]int)
	for _, snapshot := range snapshots {
		if !delegateRowVisibleTo(s, snapshot.parentID, snapshot.descriptor.OwnerSessionID, snapshot.descriptor.VisibleSessionID) {
			continue
		}
		depths[snapshot.id] = 0
	}
	if includeDescendants {
		// Every row reachable from a visible base row over ParentDelegateID is a
		// descendant of this session, whoever ran each create. Filtering these by
		// VisibleSessionID would hide a child delegate's own delegates from its
		// ancestors (issue #4904).
		for changed := true; changed; {
			changed = false
			for _, snapshot := range snapshots {
				if _, known := depths[snapshot.id]; known {
					continue
				}
				parentDepth, parentVisible := depths[snapshot.parentID]
				if !parentVisible {
					continue
				}
				depths[snapshot.id] = parentDepth + 1
				changed = true
			}
		}
	}

	rows := make([]delegateVisibleRow, 0, len(depths))
	for id, depth := range depths {
		rows = append(rows, delegateVisibleRow{snapshot: byID[id], depth: depth})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].snapshot.id < rows[j].snapshot.id })
	return rows
}

func projectDelegateListItem(now time.Time, visible delegateVisibleRow) jobListEntry {
	snapshot := visible.snapshot
	status := projectDelegateStatus(now, snapshot)
	resumable := snapshot.resumable
	item := jobListEntry{
		ID:                   snapshot.id,
		Kind:                 "delegate",
		Type:                 "delegate",
		Status:               status.Status,
		Name:                 snapshot.descriptor.Name,
		Description:          snapshot.descriptor.Description,
		OwnerSessionID:       snapshot.descriptor.OwnerSessionID,
		VisibleToSessionID:   snapshot.descriptor.VisibleSessionID,
		TranscriptRef:        stringPtrOrNil(snapshot.transcriptRef),
		Resumable:            &resumable,
		NotResumableReason:   stringPtrOrNil(snapshot.notResumableReason),
		StartedAt:            status.RunStartedAt,
		RunningForMS:         status.RunningForMS,
		DurationMS:           status.DurationMS,
		QuietForMS:           status.QuietForMS,
		LastActivity:         stringPtrOrNil(status.LatestActivityAt),
		Depth:                visible.depth,
		Task:                 snapshot.descriptor.Task,
		AgentType:            snapshot.descriptor.AgentType,
		Tools:                append([]string(nil), snapshot.descriptor.ToolNameCeiling...),
		Model:                snapshot.descriptor.ResolvedModel,
		ReasoningEffort:      snapshot.descriptor.Config.ReasoningEffort,
		ParentDelegateID:     stringPtrOrNil(snapshot.parentID),
		LatestActivitySortAt: snapshot.latestActivityAt,
	}
	if snapshot.lastOutcome != nil {
		item.Reason = stringPtrOrNil(snapshot.lastOutcome.Reason)
		item.ExhaustionBudget = string(snapshot.lastOutcome.ExhaustionBudget)
		item.ExhaustionLimit = snapshot.lastOutcome.ExhaustionLimit
		endedAt := snapshot.lastOutcome.EndedAt
		item.EndedAt = timePtrOrNil(&endedAt)
	}
	return item
}

func stableJobListItemMatches(item jobListEntry, filter listFilter) bool {
	status := jobstore.Status(item.Status)
	jobType := jobstore.JobType(item.Type)
	if filter.Status != "" && status != filter.Status {
		return false
	}
	if len(filter.Statuses) > 0 && !statusAllowed(status, filter.Statuses) {
		return false
	}
	if filter.Type != "" && jobType != filter.Type {
		return false
	}
	if len(filter.Types) > 0 && !typeAllowed(jobType, filter.Types) {
		return false
	}
	return true
}

func jobListItemActivity(item jobListEntry) time.Time {
	if !item.LatestActivitySortAt.IsZero() {
		return item.LatestActivitySortAt
	}
	for _, raw := range []*string{item.LastActivity, item.EndedAt} {
		if raw == nil || *raw == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, *raw); err == nil {
			return parsed
		}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, item.StartedAt); err == nil {
		return parsed
	}
	return time.Time{}
}

type jobListLabel struct {
	name, description string
}

// formatJobList budgets previews without changing the selected page or its state.
// Only bounded prefixes are normalized, so formatting never scans a full brief.
func formatJobList(out jobListResult, maxChars int) string {
	labels := make([]jobListLabel, len(out.Items))
	for i, j := range out.Items {
		description := j.Description
		if description == "" && j.Command != nil {
			description = *j.Command
		}
		labels[i] = jobListLabel{
			name:        collapseWhitespace(activitybound.Truncate(j.Name, activitybound.MaxLabelRunes)),
			description: collapseWhitespace(activitybound.Truncate(description, activitybound.MaxLabelRunes)),
		}
	}
	full := renderJobList(out, labels, activitybound.MaxLabelRunes)
	if maxChars <= 0 || utf8.RuneCountInString(full) <= maxChars {
		return full
	}
	// Fixed fields, including watches and footer, take precedence over previews.
	// If they alone exceed the limit, leave truncation/recovery to the registry.
	best := renderJobList(out, labels, 0)
	if utf8.RuneCountInString(best) >= maxChars {
		return best
	}
	low, high := 1, activitybound.MaxLabelRunes-1
	for low <= high {
		limit := low + (high-low)/2
		candidate := renderJobList(out, labels, limit)
		if utf8.RuneCountInString(candidate) <= maxChars {
			best = candidate
			low = limit + 1
		} else {
			high = limit - 1
		}
	}
	return best
}

// renderJobList renders job_list as plain text: a schema header, then one job per
// line — job_id, type, status, a label (description or shell command), and a
// bracketed detail tail (started time, reason, exit code, size, resumability) —
// then a count footer with the delegation allowance and any active/recent watches.
func renderJobList(out jobListResult, labels []jobListLabel, labelLimit int) string {
	var b strings.Builder
	if len(out.Items) > 0 {
		b.WriteString("# id  type  status  label  [started · reason · exit · bytes]\n")
	}
	for i, j := range out.Items {
		fmt.Fprintf(&b, "%s  %s  %s", j.ID, j.Type, j.Status)
		if j.Depth > 0 {
			fmt.Fprintf(&b, "  depth=%d", j.Depth)
		}
		parts := make([]string, 0, 2)
		if name := activitybound.Truncate(labels[i].name, labelLimit); name != "" {
			parts = append(parts, name)
		}
		if label := activitybound.Truncate(labels[i].description, labelLimit); label != "" {
			parts = append(parts, label)
		}
		if len(parts) != 0 {
			fmt.Fprintf(&b, "  %s", strings.Join(parts, " — "))
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
		detail = append(detail, fmt.Sprintf("%d bytes", j.TotalBytes))
		// Surface resumability only when a job actually is resumable; resumable=false
		// for an ordinary shell job is noise (keeps rows lean).
		if j.Resumable != nil && *j.Resumable {
			detail = append(detail, "resumable")
		}
		fmt.Fprintf(&b, "  [%s]\n", strings.Join(detail, " · "))
	}
	if len(out.Items) == 0 {
		b.WriteString("No jobs.\n")
	}
	if out.Offset > 0 || out.Total > len(out.Items) {
		if len(out.Items) == 0 {
			// Offset past the end: never print an inverted "showing 51-50".
			fmt.Fprintf(&b, "\nshowing none of %d jobs (offset %d past end).", out.Total, out.Offset)
		} else {
			fmt.Fprintf(&b, "\nshowing %d-%d of %d jobs.", out.Offset+1, out.Offset+len(out.Items), out.Total)
		}
	} else {
		fmt.Fprintf(&b, "\n%d job(s).", out.Count)
	}
	if ts := out.TurnSlots; ts != nil {
		fmt.Fprintf(&b, " delegate turn slots: %d/%d in use (%d jobs, %d drive turns).", ts.InUse, ts.Cap, ts.Jobs, ts.Drives)
	}
	if out.DelegationAllowance > 0 {
		fmt.Fprintf(&b, " delegation_allowance: %d.", out.DelegationAllowance)
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

// TurnSlotOccupancy is the diagnostic tree-counter snapshot surfaced in
// job_list while any delegate-turn slot is held: spawn-budget total in use,
// cap, and jobs, plus drive turns in flight on the separate drive budget.
type TurnSlotOccupancy struct {
	InUse int64 `json:"in_use"`
	Cap   int64 `json:"cap"`
	Jobs  int64 `json:"jobs"`
	// Drives is the live occupancy of the separate drive-turn budget
	// (driveCounter); drive turns do not hold spawn-budget slots.
	Drives int64 `json:"drive_turns"`
}

type turnSlotOccupancy = TurnSlotOccupancy

type jobListResult struct {
	Items     []jobListEntry     `json:"items"`
	Jobs      []jobListEntry     `json:"-"`
	Count     int                `json:"count"`
	Offset    int                `json:"offset,omitempty"`
	Total     int                `json:"total"`
	TurnSlots *turnSlotOccupancy `json:"turn_slots,omitempty"`
	// Watches/RecentWatches/DelegationAllowance are supervision signal kept only
	// when they carry information: no active watches, no recent watch history, and
	// a no-op delegation allowance (≤ 1, which can only grant 0) are all omitted.
	Watches             []watchListEntry   `json:"watches,omitempty"`
	RecentWatches       []recentWatchEntry `json:"recent_watches,omitempty"`
	DelegationAllowance int                `json:"delegation_allowance,omitempty"`
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
// is one of auto_removed_terminal, cleared, replaced, budget_exhausted, fired (a
// one-shot timer retired by its only fire), job_manager_closed — the whole set
// recordWatchEndedLocked writes. runtime_lost, the reason a restart stamps on the
// durable watch_cleared events of the watches it could not restore, never appears
// here: those watches end in the store before this in-memory ring exists.
type recentWatchEntry struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Condition  string `json:"condition"`
	Deliveries int    `json:"deliveries"`
	EndReason  string `json:"end_reason"`
	EndedAt    string `json:"ended_at"`
}

type jobListEntry struct {
	ID     string  `json:"id"`
	JobID  string  `json:"job_id,omitempty"`
	Kind   string  `json:"kind"`
	Type   string  `json:"type"`
	Status string  `json:"status"`
	Phase  string  `json:"phase,omitempty"`
	Reason *string `json:"reason,omitempty"`
	// Name is the delegate's display label (the `name` create argument);
	// empty for shells and unnamed delegates, which render as absent.
	Name             string   `json:"name,omitempty"`
	Description      string   `json:"description"`
	Task             string   `json:"task,omitempty"`
	AgentType        string   `json:"agent_type,omitempty"`
	Tools            []string `json:"tools,omitempty"`
	Model            string   `json:"model,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	ParentJobID      *string  `json:"parent_job_id,omitempty"`
	ParentDelegateID *string  `json:"parent_delegate_id,omitempty"`
	OwnerSessionID   string   `json:"owner_session_id"`
	// VisibleToSessionID is internal visibility routing: the session that ran the
	// create, which differs from OwnerSessionID for a nested delegate (the owner
	// names the tree root that admitted the runtime). Kept for tooling but omitted
	// from the model wire.
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

	LatestActivitySortAt time.Time `json:"-"`
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
	return stringListArg(args, key, func(s string) (jobstore.Status, error) {
		status := jobstore.Status(s)
		switch status {
		case jobstore.StatusRunning,
			jobstore.Status("idle"), jobstore.Status("settling"), jobstore.Status("stopping"), jobstore.Status("closed"),
			jobstore.StatusCompleted, jobstore.StatusCommandExitedNonzero, jobstore.StatusCommandKilled,
			jobstore.StatusFailed, jobstore.StatusExhausted, jobstore.StatusCancelled, jobstore.StatusStopped:
			return status, nil
		}
		return "", fmt.Errorf("invalid job status %q", status)
	})
}

// collapseWhitespace trims text and turns each inner run of whitespace into
// one space.
func collapseWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
