package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

const (
	activityMaxWorkUnits    = 2000
	activityMaxNewDepth     = 32
	activityMaxEncodedBytes = 4 << 20
	activityMaxTokenBytes   = 16 << 10
	activityContinuationV1  = 1
)

type activityContinuation struct {
	Version   int      `json:"v"`
	RootID    string   `json:"root"`
	SessionID string   `json:"session"`
	Path      []string `json:"path"`
}

// activitySessionSnapshot is the lock-free input to the activity projection.
// Traversal owns constructing it; projection only consumes cloned job records.
type activitySessionSnapshot struct {
	SessionID       string
	Ref             string
	Label           string
	RootID          string
	Revision        uint64
	Jobs            []*jobstore.JobRecord
	LiveJobs        map[string]*jobstore.JobRecord
	StableDelegates map[string]delegateSnapshot
	Usage           *appwire.EvenerUsage // cumulative self-only tokens; nil = unknown
	Diagnostics     []string
	Children        map[string]*activitySessionSnapshot // child session ID
	Errors          map[string]error                    // child session ID
}

type activitySessionLocator struct {
	live      *Session
	stateDir  string
	sessionID string
}

type activityLoadedBase struct {
	snapshot       activitySessionSnapshot
	directChildren map[string]*subagent
}

// activityBudget carries projection-local traversal state and optional response
// bounds. A zero-value budget remains the unlimited cycle guard Task 2 used.
type activityBudget struct {
	visiting     map[string]bool
	bounded      bool
	rootID       string
	maxWorkUnits int
	usedWork     int
	maxDepth     int
	now          time.Time
}

func newActivityBudget() *activityBudget {
	return &activityBudget{visiting: make(map[string]bool)}
}

func newBoundedActivityBudget(rootID string, now time.Time) *activityBudget {
	return &activityBudget{
		visiting:     make(map[string]bool),
		bounded:      true,
		rootID:       rootID,
		maxWorkUnits: activityMaxWorkUnits,
		maxDepth:     activityMaxNewDepth,
		now:          now,
	}
}

func encodeActivityContinuation(cont activityContinuation) string {
	payload, err := json.Marshal(cont)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeActivityContinuation(token, expectedRoot string) (activityContinuation, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return activityContinuation{}, errors.New("empty continuation")
	}
	if len(token) > activityMaxTokenBytes {
		return activityContinuation{}, fmt.Errorf("continuation exceeds %d bytes", activityMaxTokenBytes)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return activityContinuation{}, fmt.Errorf("decode continuation: %w", err)
	}
	var cont activityContinuation
	if err := json.Unmarshal(raw, &cont); err != nil {
		return activityContinuation{}, fmt.Errorf("unmarshal continuation: %w", err)
	}
	if cont.Version != activityContinuationV1 {
		return activityContinuation{}, fmt.Errorf("unsupported continuation version %d", cont.Version)
	}
	if cont.RootID == "" || cont.SessionID == "" {
		return activityContinuation{}, errors.New("continuation missing root or session")
	}
	if expectedRoot != "" && cont.RootID != expectedRoot {
		return activityContinuation{}, fmt.Errorf("continuation root %q does not match %q", cont.RootID, expectedRoot)
	}
	if err := validIDToken(cont.RootID); err != nil {
		return activityContinuation{}, fmt.Errorf("invalid continuation root: %w", err)
	}
	if err := validIDToken(cont.SessionID); err != nil {
		return activityContinuation{}, fmt.Errorf("invalid continuation session: %w", err)
	}
	seen := make(map[string]bool, len(cont.Path))
	for _, hop := range cont.Path {
		if err := validIDToken(hop); err != nil {
			return activityContinuation{}, fmt.Errorf("invalid continuation path hop %q: %w", hop, err)
		}
		if seen[hop] {
			return activityContinuation{}, fmt.Errorf("duplicate continuation path hop %q", hop)
		}
		seen[hop] = true
	}
	cont.Path = append([]string(nil), cont.Path...)
	return cont, nil
}

// JobActivityTree builds the session's job-activity tree reported over
// appwire, after validating params' root reference against this session.
func (s *Session) JobActivityTree(params appwire.JobsListParams) (appwire.JobActivityTree, error) {
	if s == nil {
		return appwire.JobActivityTree{}, errors.New("session unavailable")
	}
	if err := validateActivityRootRef(params.Ref, s.ID()); err != nil {
		return appwire.JobActivityTree{}, err
	}
	root := activitySessionLocator{live: s, stateDir: s.stateDir, sessionID: s.ID()}
	now := s.sclock().Now().UTC()
	if s.jobActivityClock == nil {
		snapshot, startDepth, err := loadActivitySnapshotForParams(root, params)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		return projectBoundedActivityTree(*snapshot, root.sessionID, startDepth, 0, now)
	}
	return projectStableLiveActivityTreeAt(s.jobActivityClock, root.sessionID, now, func() (*activitySessionSnapshot, int, error) {
		return loadActivitySnapshotForParams(root, params)
	})
}

func projectStableLiveActivityTree(clock *jobActivityClock, rootID string, load func() (*activitySessionSnapshot, int, error)) (appwire.JobActivityTree, error) {
	return projectStableLiveActivityTreeAt(clock, rootID, time.Now().UTC(), load)
}

func projectStableLiveActivityTreeAt(clock *jobActivityClock, rootID string, now time.Time, load func() (*activitySessionSnapshot, int, error)) (appwire.JobActivityTree, error) {
	for range 8 {
		before := activityCurrentRootRevision(clock)
		snapshot, startDepth, err := load()
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		after := activityCurrentRootRevision(clock)
		if before == after {
			return projectBoundedActivityTree(*snapshot, rootID, startDepth, after, now)
		}
	}
	return appwire.JobActivityTree{}, errors.New("activity tree changed while snapshot was being built; retry")
}

func activityCurrentRootRevision(clock *jobActivityClock) uint64 {
	if clock == nil {
		return 0
	}
	return clock.revision.Load()
}

func activityCurrentRootID(clock *jobActivityClock, fallback string) string {
	if clock != nil && strings.TrimSpace(clock.rootSessionID) != "" {
		return clock.rootSessionID
	}
	return strings.TrimSpace(fallback)
}

func loadActivitySnapshotForParams(root activitySessionLocator, params appwire.JobsListParams) (*activitySessionSnapshot, int, error) {
	if strings.TrimSpace(params.Continuation) == "" {
		visited := map[string]bool{root.sessionID: true}
		snapshot, err := buildActivityFullSnapshot(root, visited, false)
		return snapshot, 0, err
	}
	cont, err := decodeActivityContinuation(params.Continuation, root.sessionID)
	if err != nil {
		return nil, 0, err
	}
	visited := map[string]bool{root.sessionID: true}
	snapshot, err := buildActivityContinuationSnapshot(root, cont, visited, false)
	if err != nil {
		return nil, 0, err
	}
	return snapshot, -len(cont.Path), nil
}

func buildActivityFullSnapshot(loc activitySessionLocator, visited map[string]bool, required bool) (*activitySessionSnapshot, error) {
	loaded, err := loadActivityBase(loc, required)
	if err != nil {
		return nil, err
	}
	snapshot := loaded.snapshot
	snapshot.Children = make(map[string]*activitySessionSnapshot)
	snapshot.Errors = make(map[string]error)
	for _, delegateID := range sortedStableActivityDelegateIDs(snapshot.StableDelegates) {
		row := snapshot.StableDelegates[delegateID]
		childID, err := activityChildSessionForStable(row)
		if err != nil {
			key := delegateID
			if row.descriptor.ChildSessionID != "" {
				key = row.descriptor.ChildSessionID
			}
			snapshot.Errors[key] = err
			continue
		}
		if snapshot.Children[childID] != nil || snapshot.Errors[childID] != nil {
			continue
		}
		if visited[childID] {
			snapshot.Errors[childID] = errors.New("cycle detected")
			continue
		}
		childLoc, err := resolveActivityChildByID(loc, loaded, childID)
		if err != nil {
			snapshot.Errors[childID] = err
			continue
		}
		nextVisited := cloneActivityVisited(visited)
		nextVisited[childID] = true
		child, err := buildActivityFullSnapshot(childLoc, nextVisited, true)
		if err != nil {
			snapshot.Errors[childID] = err
			continue
		}
		snapshot.Children[childID] = child
	}
	return &snapshot, nil
}

func buildActivityContinuationSnapshot(loc activitySessionLocator, cont activityContinuation, visited map[string]bool, required bool) (*activitySessionSnapshot, error) {
	if len(cont.Path) == 0 {
		if loc.sessionID != cont.SessionID {
			return nil, fmt.Errorf("continuation session %q does not match root %q", cont.SessionID, loc.sessionID)
		}
		return buildActivityFullSnapshot(loc, visited, required)
	}
	return buildActivityContinuationAt(loc, cont, 0, visited, required)
}

func buildActivityContinuationAt(loc activitySessionLocator, cont activityContinuation, hop int, visited map[string]bool, required bool) (*activitySessionSnapshot, error) {
	if hop == len(cont.Path) {
		if loc.sessionID != cont.SessionID {
			return nil, fmt.Errorf("continuation session %q does not match resolved path %q", cont.SessionID, loc.sessionID)
		}
		return buildActivityFullSnapshot(loc, visited, required)
	}
	loaded, err := loadActivityBase(loc, required)
	if err != nil {
		return nil, err
	}
	delegateID := cont.Path[hop]
	if row, ok := loaded.snapshot.StableDelegates[delegateID]; ok {
		childID, err := activityChildSessionForStable(row)
		if err != nil {
			return nil, err
		}
		if visited[childID] {
			return nil, errors.New("cycle detected")
		}
		childLoc, err := resolveActivityChildByID(loc, loaded, childID)
		if err != nil {
			return nil, err
		}
		nextVisited := cloneActivityVisited(visited)
		nextVisited[childID] = true
		child, err := buildActivityContinuationAt(childLoc, cont, hop+1, nextVisited, true)
		if err != nil {
			return nil, err
		}
		filtered := activityFilterSnapshotToDelegate(loaded.snapshot, delegateID, child)
		return &filtered, nil
	}
	return nil, fmt.Errorf("continuation path hop %q not found", delegateID)
}

func loadActivityBase(loc activitySessionLocator, required bool) (activityLoadedBase, error) {
	if loc.live != nil {
		return loadLiveActivityBase(loc.live)
	}
	return loadHistoricalActivityBase(loc.stateDir, loc.sessionID, required)
}

func loadLiveActivityBase(s *Session) (activityLoadedBase, error) {
	if s == nil {
		return activityLoadedBase{}, errors.New("session unavailable")
	}
	snapshot := activitySessionSnapshot{
		SessionID:       s.ID(),
		Ref:             encodeRef("", s.ID()),
		Label:           liveActivitySessionLabel(s),
		RootID:          activityCurrentRootID(s.jobActivityClock, s.ID()),
		Revision:        activityCurrentRootRevision(s.jobActivityClock),
		Jobs:            []*jobstore.JobRecord{},
		LiveJobs:        map[string]*jobstore.JobRecord{},
		StableDelegates: map[string]delegateSnapshot{},
	}
	if s.delegateController != nil {
		for _, row := range s.delegateController.Snapshot().rows {
			if row.descriptor.OwnerSessionID == s.ID() {
				snapshot.StableDelegates[row.id] = row
			}
		}
	}
	snapshot.Usage = appwire.EvenerUsageFromLLM(s.CumulativeUsageSnapshot())
	jm, err := sessionJobManager(s)
	if err == nil && jm != nil && jm.store != nil {
		ordered, err := jm.store.LoadOrdered()
		if err != nil {
			return activityLoadedBase{}, err
		}
		snapshot.Jobs = ordered
		snapshot.LiveJobs = jm.liveJobRecords()
	}
	children := make(map[string]*subagent)
	if s.subagents != nil {
		for _, sub := range s.subagents.directSubagents() {
			if sub == nil {
				continue
			}
			children[sub.id] = sub
		}
	}
	return activityLoadedBase{snapshot: snapshot, directChildren: children}, nil
}

func liveActivitySessionLabel(s *Session) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	id := s.id
	name := s.naming.value
	prompt := s.cfg.spawn.subagentTask
	for _, turn := range s.history {
		if turn.Kind == schema.TurnUserInput {
			prompt = turn.Message.Text()
			break
		}
	}
	s.mu.Unlock()
	return activitySessionLabel(schema.SessionMeta{ID: id, Name: name, OriginalPrompt: prompt})
}

func resolveActivityChildByID(parent activitySessionLocator, loaded activityLoadedBase, childID string) (activitySessionLocator, error) {
	if sub := loaded.directChildren[childID]; sub != nil && sub.sess != nil {
		sub.mu.Lock()
		closed := sub.closed
		sub.mu.Unlock()
		if !closed {
			if !sameActivityStateDir(parent.stateDir, sub.sess.StateDir()) {
				return activitySessionLocator{}, fmt.Errorf("child session %q crosses state directory boundary", childID)
			}
			return activitySessionLocator{live: sub.sess, stateDir: parent.stateDir, sessionID: childID}, nil
		}
	}
	if strings.TrimSpace(parent.stateDir) == "" {
		return activitySessionLocator{}, fmt.Errorf("child session %q unavailable: no state directory", childID)
	}
	return activitySessionLocator{stateDir: parent.stateDir, sessionID: childID}, nil
}

func sameActivityStateDir(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return left == right
	}
	return canonicalOrClean(left) == canonicalOrClean(right)
}

func activitySessionLabel(meta schema.SessionMeta) string {
	if label := strings.TrimSpace(schema.SessionDisplayName(meta)); label != "" {
		return label
	}
	return strings.TrimSpace(meta.ID)
}

func activityChildSessionForStable(row delegateSnapshot) (string, error) {
	descriptor := row.descriptor
	if descriptor.ChildSessionID == "" || descriptor.TranscriptRef == "" {
		return "", fmt.Errorf("delegate %q has an incomplete child link", row.id)
	}
	projectID, childID, err := decodeRef(descriptor.TranscriptRef)
	if err != nil {
		return "", err
	}
	if projectID != "" {
		return "", fmt.Errorf("transcript ref %q crosses state directory boundary", descriptor.TranscriptRef)
	}
	if childID != descriptor.ChildSessionID {
		return "", fmt.Errorf("delegate %q child link does not match durable child session", row.id)
	}
	return childID, nil
}

func sortedStableActivityDelegateIDs(records map[string]delegateSnapshot) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func cloneActivityVisited(visited map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(visited))
	maps.Copy(clone, visited)
	return clone
}

func activityFilterSnapshotToDelegate(base activitySessionSnapshot, delegateID string, child *activitySessionSnapshot) activitySessionSnapshot {
	filtered := activitySessionSnapshot{
		SessionID:       base.SessionID,
		Ref:             base.Ref,
		Label:           base.Label,
		RootID:          base.RootID,
		Revision:        base.Revision,
		Jobs:            []*jobstore.JobRecord{},
		LiveJobs:        map[string]*jobstore.JobRecord{},
		StableDelegates: make(map[string]delegateSnapshot, 1),
		Diagnostics:     append([]string(nil), base.Diagnostics...),
		Children:        make(map[string]*activitySessionSnapshot),
		Errors:          make(map[string]error),
	}
	if row, ok := base.StableDelegates[delegateID]; ok {
		filtered.StableDelegates[delegateID] = row
		if child != nil && row.descriptor.ChildSessionID != "" {
			filtered.Children[row.descriptor.ChildSessionID] = child
		}
	}
	return filtered
}

func projectBoundedActivityTree(snapshot activitySessionSnapshot, rootID string, startDepth int, revision uint64, now time.Time) (appwire.JobActivityTree, error) {
	budget := newBoundedActivityBudget(rootID, now)
	root := projectActivitySessionAt(snapshot, budget, startDepth, nil)
	tree := appwire.JobActivityTree{Revision: revision, Root: root}
	return trimActivityTreeToFit(tree, rootID)
}

func activitySnapshotPersistedRevision(snapshot *activitySessionSnapshot, rootID string) uint64 {
	if snapshot == nil {
		return 0
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		rootID = strings.TrimSpace(snapshot.RootID)
		if rootID == "" {
			rootID = strings.TrimSpace(snapshot.SessionID)
		}
	}
	var maxRevision uint64
	var walk func(*activitySessionSnapshot)
	walk = func(node *activitySessionSnapshot) {
		if node == nil {
			return
		}
		nodeRoot := strings.TrimSpace(node.RootID)
		if (nodeRoot == "" || rootID == "" || nodeRoot == rootID) && node.Revision > maxRevision {
			maxRevision = node.Revision
		}
		for _, row := range node.StableDelegates {
			if row.revision > maxRevision {
				maxRevision = row.revision
			}
		}
		for _, childID := range sortedActivityChildIDs(node.Children) {
			walk(node.Children[childID])
		}
	}
	walk(snapshot)
	return maxRevision
}

func sortedActivityChildIDs(children map[string]*activitySessionSnapshot) []string {
	ids := make([]string, 0, len(children))
	for id := range children {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// mergeActivityRecords overlays live records on durable history without
// mutating either input. Durable records retain their append positions. Jobs
// visible only in the live map are inserted by (StartedAt, JobID).
func mergeActivityRecords(durable []*jobstore.JobRecord, live map[string]*jobstore.JobRecord) []*jobstore.JobRecord {
	durableOrder := make([]string, 0, len(durable))
	durableByID := make(map[string]*jobstore.JobRecord, len(durable))
	for _, rec := range durable {
		if rec == nil || rec.JobID == "" {
			continue
		}
		if _, seen := durableByID[rec.JobID]; !seen {
			durableOrder = append(durableOrder, rec.JobID)
		}
		durableByID[rec.JobID] = rec
	}

	liveByID := make(map[string]*jobstore.JobRecord, len(live))
	liveKeys := make([]string, 0, len(live))
	for key := range live {
		liveKeys = append(liveKeys, key)
	}
	sort.Strings(liveKeys)
	for _, key := range liveKeys {
		rec := live[key]
		if rec == nil || rec.JobID == "" {
			continue
		}
		if _, seen := liveByID[rec.JobID]; !seen {
			liveByID[rec.JobID] = rec
		}
	}

	merged := make([]*jobstore.JobRecord, 0, len(durableByID)+len(liveByID))
	seen := make(map[string]bool, len(durableByID)+len(liveByID))
	for _, jobID := range durableOrder {
		rec := durableByID[jobID]
		if liveRec := liveByID[jobID]; liveRec != nil {
			rec = liveRec
		}
		merged = append(merged, cloneActivityRecord(rec))
		seen[jobID] = true
	}

	liveOnly := make([]*jobstore.JobRecord, 0, len(liveByID))
	for jobID, rec := range liveByID {
		if !seen[jobID] {
			liveOnly = append(liveOnly, cloneActivityRecord(rec))
		}
	}
	sort.Slice(liveOnly, func(i, j int) bool {
		return activityRecordBefore(liveOnly[i], liveOnly[j])
	})
	for _, rec := range liveOnly {
		at := len(merged)
		for i, current := range merged {
			if activityRecordBefore(rec, current) {
				at = i
				break
			}
		}
		merged = append(merged, nil)
		copy(merged[at+1:], merged[at:])
		merged[at] = rec
	}
	return merged
}

func cloneActivityRecord(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil {
		return nil
	}
	clone := *rec
	if rec.EndedAt != nil {
		ended := *rec.EndedAt
		clone.EndedAt = &ended
	}
	if rec.ExitCode != nil {
		exit := *rec.ExitCode
		clone.ExitCode = &exit
	}
	if rec.LastActivity != nil {
		last := *rec.LastActivity
		clone.LastActivity = &last
	}
	return &clone
}

func activityRecordBefore(left, right *jobstore.JobRecord) bool {
	if left.StartedAt.Equal(right.StartedAt) {
		return left.JobID < right.JobID
	}
	return left.StartedAt.Before(right.StartedAt)
}

func projectActivitySession(snapshot activitySessionSnapshot, budget *activityBudget) appwire.JobActivitySession {
	return projectActivitySessionAt(snapshot, budget, 0, nil)
}

func projectActivitySessionAt(snapshot activitySessionSnapshot, budget *activityBudget, depth int, path []string) appwire.JobActivitySession {
	if budget == nil {
		budget = newActivityBudget()
	}
	projected := appwire.JobActivitySession{
		SessionID:   snapshot.SessionID,
		Ref:         snapshot.Ref,
		Label:       snapshot.Label,
		Entries:     make([]appwire.JobActivityEntry, 0),
		Diagnostics: append([]string(nil), snapshot.Diagnostics...),
	}

	cycleKey := snapshot.SessionID + "\x00" + snapshot.Ref
	if budget.visiting == nil {
		budget.visiting = make(map[string]bool)
	}
	if budget.visiting[cycleKey] {
		projected.Branch.Error = fmt.Sprintf("activity cycle at session %q", snapshot.SessionID)
		projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
		return projected
	}
	budget.visiting[cycleKey] = true
	defer delete(budget.visiting, cycleKey)

	records := activityOwnedRecords(snapshot.SessionID, mergeActivityRecords(snapshot.Jobs, snapshot.LiveJobs))
	for _, rec := range records {
		if rec == nil {
			continue
		}
		switch rec.Type {
		case jobstore.JobShell:
			if !activityConsumeWorkUnit(budget, 1) {
				markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path)
				projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
				return projected
			}
			job := projectActivityJob(rec, snapshot.Ref)
			projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "shell", Job: &job})
		default:
			appendActivityBranchError(&projected.Branch, fmt.Sprintf("job %q has unsupported type %q", rec.JobID, rec.Type))
		}
	}
	for _, delegateID := range sortedStableActivityDelegateIDs(snapshot.StableDelegates) {
		if !activityConsumeWorkUnit(budget, 1) {
			markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path)
			projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
			return projected
		}
		delegate := projectStableActivityDelegate(snapshot, snapshot.StableDelegates[delegateID], budget, depth, path)
		projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "delegate", Delegate: &delegate})
	}

	projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
	return projected
}

func projectStableActivityDelegate(snapshot activitySessionSnapshot, row delegateSnapshot, budget *activityBudget, depth int, path []string) appwire.JobActivityDelegate {
	descriptor := row.descriptor
	status := projectStableDelegateStatus(budget.now, row)
	delegate := appwire.JobActivityDelegate{
		DelegateID:          row.id,
		OwnerSessionID:      descriptor.OwnerSessionID,
		RootSessionID:       snapshot.RootID,
		ChildSessionID:      descriptor.ChildSessionID,
		ChildRef:            descriptor.TranscriptRef,
		ParentDelegateID:    descriptor.ParentDelegateID,
		Type:                "delegate",
		Lifecycle:           string(row.lifecycle),
		Phase:               string(row.phase),
		Status:              string(row.lifecycle),
		ProjectionRevision:  row.revision,
		Resumable:           row.resumable,
		NotResumableReason:  row.notResumableReason,
		Mandate:             descriptor.Task,
		Task:                descriptor.Task,
		Description:         descriptor.Description,
		AgentType:           descriptor.AgentType,
		RequestedModel:      descriptor.RequestedModel,
		ResolvedProfileID:   descriptor.ResolvedProfileID,
		ResolvedModel:       descriptor.ResolvedModel,
		Model:               descriptor.ResolvedModel,
		ReasoningEffort:     descriptor.Config.ReasoningEffort,
		OriginTurnID:        descriptor.OriginTurnID,
		OriginToolCallID:    descriptor.OriginToolCallID,
		OriginItemID:        descriptor.OriginItemID,
		RunStartedAt:        status.RunStartedAt,
		LatestActivityAt:    status.LatestActivityAt,
		RunningForMS:        cloneInt64(status.RunningForMS),
		QuietForMS:          cloneInt64(status.QuietForMS),
		DurationMS:          cloneInt64(status.DurationMS),
		DelegationAllowance: descriptor.DelegationAllowance,
		ParentWatchGranted:  descriptor.ParentWatchGranted,
		Turns:               []appwire.JobActivityJob{},
	}
	if row.lastOutcome != nil {
		delegate.Outcome = string(row.lastOutcome.Status)
		delegate.Reason = row.lastOutcome.Reason
		delegate.Terminal = !row.currentRunOpen
		if !row.lastOutcome.EndedAt.IsZero() {
			delegate.RunEndedAt = row.lastOutcome.EndedAt.UTC().Format(time.RFC3339Nano)
		}
		delegate.ExhaustionBudget = string(row.lastOutcome.ExhaustionBudget)
		delegate.ExhaustionLimit = row.lastOutcome.ExhaustionLimit
		if row.lastOutcome.Resumable != nil {
			resumable := *row.lastOutcome.Resumable
			delegate.ExhaustionResumable = &resumable
		}
	}
	if packet := row.latestPacket; packet != nil {
		delegate.PacketKind = string(packet.Kind)
		delegate.Message = append(json.RawMessage(nil), packet.Message...)
		delegate.StructuredResult = append(json.RawMessage(nil), packet.StructuredResult...)
		delegate.StructuredReason = packet.StructuredResultReason
		delegate.Warnings = append([]string(nil), packet.Warnings...)
		if packet.StructuredResultValid != nil {
			valid := *packet.StructuredResultValid
			delegate.StructuredValid = &valid
		}
		if len(packet.Metadata) != 0 {
			var metadata delegateTerminalPacketMetadata
			if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
				appendActivityBranchError(&delegate.Branch, "delegate terminal metadata is invalid")
				delegate.Diagnostics = append(delegate.Diagnostics, "delegate terminal metadata is invalid")
			} else {
				delegate.Usage = activityUsageFromCumulative(metadata.CumulativeUsage)
				if metadata.Worktree != nil {
					delegate.Worktree = &appwire.JobActivityWorktree{
						Path: metadata.Worktree.Path, Branch: metadata.Worktree.Branch,
						HeadSHA: metadata.Worktree.HeadSHA, Ahead: metadata.Worktree.Ahead, Dirty: metadata.Worktree.Dirty,
					}
				}
			}
		}
	}
	childID, err := activityChildSessionForStable(row)
	if err != nil {
		appendActivityBranchError(&delegate.Branch, err.Error())
		return delegate
	}
	if err := snapshot.Errors[childID]; err != nil {
		appendActivityBranchError(&delegate.Branch, err.Error())
		return delegate
	}
	child := snapshot.Children[childID]
	if child == nil {
		appendActivityBranchError(&delegate.Branch, fmt.Sprintf("child session %q unavailable", childID))
		return delegate
	}
	if child.SessionID != childID || child.Ref != descriptor.TranscriptRef {
		appendActivityBranchError(&delegate.Branch, fmt.Sprintf("delegate %q child link does not match loaded session", row.id))
		return delegate
	}
	if delegate.Usage == nil && child.Usage != nil {
		usage := *child.Usage
		delegate.Usage = &usage
	}
	childPath := appendActivityPath(path, row.id)
	if budget != nil && budget.bounded && depth >= budget.maxDepth {
		markActivityDelegateTruncated(&delegate, budget, child.SessionID, childPath)
		return delegate
	}
	projectedChild := projectActivitySessionAt(*child, budget, depth+1, childPath)
	delegate.Child = &projectedChild
	return delegate
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func activityUsageFromCumulative(usage *schema.CumulativeUsage) *appwire.EvenerUsage {
	if usage == nil || *usage == (schema.CumulativeUsage{}) {
		return nil
	}
	return &appwire.EvenerUsage{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, TotalTokens: usage.TotalTokens,
	}
}

func activityOwnedRecords(sessionID string, records []*jobstore.JobRecord) []*jobstore.JobRecord {
	if sessionID == "" || len(records) == 0 {
		return records
	}
	owned := make([]*jobstore.JobRecord, 0, len(records))
	for _, rec := range records {
		if rec == nil {
			continue
		}
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != sessionID {
			continue
		}
		owned = append(owned, rec)
	}
	return owned
}

func activityConsumeWorkUnit(budget *activityBudget, units int) bool {
	if budget == nil || !budget.bounded {
		return true
	}
	if units < 0 {
		units = 0
	}
	if budget.usedWork+units > budget.maxWorkUnits {
		return false
	}
	budget.usedWork += units
	return true
}

func markActivitySessionTruncated(session *appwire.JobActivitySession, budget *activityBudget, sessionID string, path []string) {
	if session == nil {
		return
	}
	session.Branch.Truncated = true
	if budget != nil && budget.rootID != "" {
		session.Branch.Continuation = encodeActivityContinuation(activityContinuation{
			Version:   activityContinuationV1,
			RootID:    budget.rootID,
			SessionID: sessionID,
			Path:      append([]string(nil), path...),
		})
	}
}

func markActivityDelegateTruncated(delegate *appwire.JobActivityDelegate, budget *activityBudget, sessionID string, path []string) {
	if delegate == nil {
		return
	}
	delegate.Branch.Truncated = true
	if budget != nil && budget.rootID != "" {
		delegate.Branch.Continuation = encodeActivityContinuation(activityContinuation{
			Version:   activityContinuationV1,
			RootID:    budget.rootID,
			SessionID: sessionID,
			Path:      append([]string(nil), path...),
		})
	}
}

func appendActivityPath(path []string, delegateID string) []string {
	next := append([]string(nil), path...)
	if delegateID != "" {
		next = append(next, delegateID)
	}
	return next
}

func appendActivityBranchError(branch *appwire.JobActivityBranchState, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if branch.Error == "" {
		branch.Error = message
		return
	}
	branch.Error += "; " + message
}

func activityOutcome(status jobstore.Status) (bool, string) {
	switch status {
	case jobstore.StatusRunning:
		return false, ""
	case jobstore.StatusFailed, jobstore.StatusExhausted:
		return true, "failure"
	case jobstore.StatusCompleted:
		return true, "success"
	case jobstore.StatusCancelled, jobstore.StatusStopped:
		return true, "neutral"
	default:
		return status.IsTerminal(), ""
	}
}

func aggregateActivity(entries []appwire.JobActivityEntry, branch appwire.JobActivityBranchState) (appwire.JobActivityCounts, string) {
	counts := appwire.JobActivityCounts{Complete: activityBranchComplete(branch)}
	addJob := func(job appwire.JobActivityJob) {
		if !job.Terminal {
			counts.Active++
			return
		}
		if job.Outcome == "failure" {
			counts.Failed++
			return
		}
		counts.Completed++
	}
	for _, entry := range entries {
		if entry.Job != nil {
			addJob(*entry.Job)
		}
		if entry.Delegate == nil {
			continue
		}
		if entry.Delegate.Type == "delegate" {
			addJob(appwire.JobActivityJob{Terminal: entry.Delegate.Terminal, Outcome: activityDelegateOutcome(entry.Delegate.Outcome)})
		} else {
			for _, turn := range entry.Delegate.Turns {
				addJob(turn)
			}
		}
		if !activityBranchComplete(entry.Delegate.Branch) {
			counts.Complete = false
		}
		if entry.Delegate.Child != nil {
			counts.Active += entry.Delegate.Child.Counts.Active
			counts.Failed += entry.Delegate.Child.Counts.Failed
			counts.Completed += entry.Delegate.Child.Counts.Completed
			if !entry.Delegate.Child.Counts.Complete {
				counts.Complete = false
			}
		}
	}

	switch {
	case counts.Active > 0:
		return counts, "working"
	case counts.Failed > 0:
		return counts, "failed"
	case !counts.Complete:
		return counts, "unavailable"
	case counts.Completed > 0:
		return counts, "ended"
	default:
		return counts, "idle"
	}
}

func activityDelegateOutcome(outcome string) string {
	switch delegatestore.OutcomeStatus(outcome) {
	case delegatestore.OutcomeFailed, delegatestore.OutcomeExhausted:
		return "failure"
	case delegatestore.OutcomeCompleted:
		return "success"
	case delegatestore.OutcomeCancelled, delegatestore.OutcomeStopped:
		return "neutral"
	default:
		return ""
	}
}

func activityBranchComplete(branch appwire.JobActivityBranchState) bool {
	return branch.Error == "" && !branch.Truncated && branch.Continuation == ""
}

func trimActivityTreeToFit(tree appwire.JobActivityTree, rootID string) (appwire.JobActivityTree, error) {
	for {
		recomputeActivitySession(&tree.Root)
		raw, err := json.Marshal(tree)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		if len(raw) <= activityMaxEncodedBytes {
			return tree, nil
		}
		if !trimActivityTrailingEntry(&tree.Root, rootID, nil) {
			return tree, nil
		}
	}
}

func trimActivityTrailingEntry(session *appwire.JobActivitySession, rootID string, path []string) bool {
	if session == nil || len(session.Entries) == 0 {
		return false
	}
	i := len(session.Entries) - 1
	entry := &session.Entries[i]
	if entry.Delegate != nil && entry.Delegate.Child != nil {
		if trimActivityTrailingEntry(entry.Delegate.Child, rootID, appendActivityPath(path, entry.Delegate.DelegateID)) {
			return true
		}
	}
	session.Entries = session.Entries[:i]
	session.Branch.Truncated = true
	session.Branch.Continuation = encodeActivityContinuation(activityContinuation{
		Version:   activityContinuationV1,
		RootID:    rootID,
		SessionID: session.SessionID,
		Path:      append([]string(nil), path...),
	})
	return true
}

func recomputeActivitySession(session *appwire.JobActivitySession) {
	if session == nil {
		return
	}
	for i := range session.Entries {
		delegate := session.Entries[i].Delegate
		if delegate != nil && delegate.Child != nil {
			recomputeActivitySession(delegate.Child)
		}
	}
	session.Counts, session.Aggregate = aggregateActivity(session.Entries, session.Branch)
}

// projectActivityJob projects one job record into the activity tree's job
// shape. (The flat jobs-list compatibility path this once also served was
// retired in 4993cdd53; the tree is its only consumer now.)
func projectActivityJob(rec *jobstore.JobRecord, ownerRef string) appwire.JobActivityJob {
	if rec == nil {
		return appwire.JobActivityJob{}
	}
	description := rec.Description
	if description == "" {
		description = rec.Command
	}
	if description == "" {
		description = rec.Task
	}
	terminal, outcome := activityOutcome(rec.Status)
	job := appwire.JobActivityJob{
		JobID:          rec.JobID,
		OwnerSessionID: rec.OwnerSessionID,
		OwnerRef:       ownerRef,
		// jobTranscriptRef derives the canonical "job:<id>" ref for shell jobs
		// (their records never store one) and passes through a stored ref
		// (delegate turns point at the child session) unchanged.
		TranscriptRef: jobTranscriptRef(rec),
		Type:          string(rec.Type),
		Status:        string(rec.Status),
		Outcome:       outcome,
		Terminal:      terminal,
		Background:    rec.Background,
		HasOutput:     rec.OutputPath != "" || rec.OutputBytes > 0,
		Description:   description,
		Command:       rec.Command,
		Task:          rec.Task,
		Reason:        rec.Reason,
		StartedAt:     rec.StartedAt.UTC().Format(time.RFC3339),
		OutputBytes:   rec.OutputBytes,
	}
	if rec.EndedAt != nil {
		job.EndedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	if rec.LastActivity != nil {
		job.LastOutputAt = rec.LastActivity.UTC().Format(time.RFC3339)
	}
	if rec.ExitCode != nil {
		exit := *rec.ExitCode
		job.ExitCode = &exit
	}
	return job
}
