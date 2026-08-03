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

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
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
	SessionID string
	Ref       string
	Label     string
	RootID    string
	Revision  uint64
	Jobs      []*jobstore.JobRecord
	LiveJobs  map[string]*jobstore.JobRecord
	Delegates map[string]*jobstore.DelegateRecord
	Children  map[string]*activitySessionSnapshot // child session ID
	Errors    map[string]error                    // child session ID
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
}

func newActivityBudget() *activityBudget {
	return &activityBudget{visiting: make(map[string]bool)}
}

func newBoundedActivityBudget(rootID string) *activityBudget {
	return &activityBudget{
		visiting:     make(map[string]bool),
		bounded:      true,
		rootID:       rootID,
		maxWorkUnits: activityMaxWorkUnits,
		maxDepth:     activityMaxNewDepth,
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

func (s *Session) JobActivityTree(params appwire.JobsListParams) (appwire.JobActivityTree, error) {
	if s == nil {
		return appwire.JobActivityTree{}, errors.New("session unavailable")
	}
	if err := validateActivityRootRef(params.Ref, s.ID()); err != nil {
		return appwire.JobActivityTree{}, err
	}
	root := activitySessionLocator{live: s, stateDir: s.stateDir, sessionID: s.ID()}
	if s.jobTreeClock == nil {
		snapshot, startDepth, err := loadActivitySnapshotForParams(root, params)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		return projectBoundedActivityTree(*snapshot, root.sessionID, startDepth, 0)
	}
	return projectStableLiveActivityTree(s.jobTreeClock, root.sessionID, func() (*activitySessionSnapshot, int, error) {
		return loadActivitySnapshotForParams(root, params)
	})
}

func projectStableLiveActivityTree(clock *jobTreeClock, rootID string, load func() (*activitySessionSnapshot, int, error)) (appwire.JobActivityTree, error) {
	for range 8 {
		before := activityCurrentRootRevision(clock)
		snapshot, startDepth, err := load()
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		after := activityCurrentRootRevision(clock)
		if before == after {
			return projectBoundedActivityTree(*snapshot, rootID, startDepth, after)
		}
	}
	return appwire.JobActivityTree{}, errors.New("activity tree changed while snapshot was being built; retry")
}

func activityCurrentRootRevision(clock *jobTreeClock) uint64 {
	if clock == nil {
		return 0
	}
	return clock.revision.Load()
}

func activityCurrentRootID(clock *jobTreeClock, fallback string) string {
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
	for _, delegateID := range sortedActivityDelegateIDs(snapshot.Delegates) {
		record := snapshot.Delegates[delegateID]
		childID, err := activityChildSessionForRecord(record)
		if err != nil {
			key := delegateID
			if record != nil && record.ChildSessionID != "" {
				key = record.ChildSessionID
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
		childLoc, err := resolveActivityChildLocator(loc, loaded, record)
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
	record := loaded.snapshot.Delegates[delegateID]
	if record == nil {
		return nil, fmt.Errorf("continuation path hop %q not found", delegateID)
	}
	childID, err := activityChildSessionForRecord(record)
	if err != nil {
		return nil, err
	}
	if visited[childID] {
		return nil, errors.New("cycle detected")
	}
	childLoc, err := resolveActivityChildLocator(loc, loaded, record)
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
		SessionID: s.ID(),
		Ref:       encodeRef("", s.ID()),
		Label:     activitySessionLabel(s.Meta()),
		RootID:    activityCurrentRootID(s.jobTreeClock, s.ID()),
		Revision:  activityCurrentRootRevision(s.jobTreeClock),
		Jobs:      []*jobstore.JobRecord{},
		LiveJobs:  map[string]*jobstore.JobRecord{},
		Delegates: map[string]*jobstore.DelegateRecord{},
	}
	jm, err := sessionJobManager(s)
	if err == nil && jm != nil && jm.store != nil {
		ordered, err := jm.store.LoadOrdered()
		if err != nil {
			return activityLoadedBase{}, err
		}
		delegates, err := jm.store.LoadDelegates()
		if err != nil {
			return activityLoadedBase{}, err
		}
		snapshot.Jobs = ordered
		snapshot.LiveJobs = jm.liveJobRecords()
		snapshot.Delegates = delegates
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

func resolveActivityChildLocator(parent activitySessionLocator, loaded activityLoadedBase, record *jobstore.DelegateRecord) (activitySessionLocator, error) {
	childID, err := activityChildSessionForRecord(record)
	if err != nil {
		return activitySessionLocator{}, err
	}
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

func activityChildSessionForRecord(record *jobstore.DelegateRecord) (string, error) {
	if record == nil {
		return "", errors.New("delegate record unavailable")
	}
	if record.ChildSessionID == "" || record.TranscriptRef == "" {
		return "", fmt.Errorf("delegate %q has an incomplete child link", record.DelegateID)
	}
	projectID, childID, err := decodeRef(record.TranscriptRef)
	if err != nil {
		return "", err
	}
	if projectID != "" {
		return "", fmt.Errorf("transcript ref %q crosses state directory boundary", record.TranscriptRef)
	}
	if childID != record.ChildSessionID {
		return "", fmt.Errorf("delegate %q child link does not match durable child session", record.DelegateID)
	}
	return childID, nil
}

func sortedActivityDelegateIDs(records map[string]*jobstore.DelegateRecord) []string {
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
		SessionID: base.SessionID,
		Ref:       base.Ref,
		Label:     base.Label,
		RootID:    base.RootID,
		Revision:  base.Revision,
		Jobs:      filterActivityRecordsByDelegate(base.Jobs, delegateID),
		LiveJobs:  filterActivityLiveRecordsByDelegate(base.LiveJobs, delegateID),
		Delegates: make(map[string]*jobstore.DelegateRecord, 1),
		Children:  make(map[string]*activitySessionSnapshot),
		Errors:    make(map[string]error),
	}
	if record := base.Delegates[delegateID]; record != nil {
		filtered.Delegates[delegateID] = record
		if child != nil && record.ChildSessionID != "" {
			filtered.Children[record.ChildSessionID] = child
		}
	}
	return filtered
}

func filterActivityRecordsByDelegate(records []*jobstore.JobRecord, delegateID string) []*jobstore.JobRecord {
	filtered := make([]*jobstore.JobRecord, 0, len(records))
	for _, rec := range records {
		if rec != nil && rec.Type == jobstore.JobDelegate && rec.DelegateID == delegateID {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

func filterActivityLiveRecordsByDelegate(records map[string]*jobstore.JobRecord, delegateID string) map[string]*jobstore.JobRecord {
	filtered := make(map[string]*jobstore.JobRecord)
	for jobID, rec := range records {
		if rec != nil && rec.Type == jobstore.JobDelegate && rec.DelegateID == delegateID {
			filtered[jobID] = rec
		}
	}
	return filtered
}

func projectBoundedActivityTree(snapshot activitySessionSnapshot, rootID string, startDepth int, revision uint64) (appwire.JobActivityTree, error) {
	budget := newBoundedActivityBudget(rootID)
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
	if rec.Resumable != nil {
		resumable := *rec.Resumable
		clone.Resumable = &resumable
	}
	if rec.StructuredResultValid != nil {
		valid := *rec.StructuredResultValid
		clone.StructuredResultValid = &valid
	}
	if rec.DelegateRestore != nil {
		restore := *rec.DelegateRestore
		restore.FrozenToolNames = append([]string(nil), rec.DelegateRestore.FrozenToolNames...)
		restore.FrozenSkillNames = append([]string(nil), rec.DelegateRestore.FrozenSkillNames...)
		restore.FrozenSkillBodies = append([]string(nil), rec.DelegateRestore.FrozenSkillBodies...)
		restore.ExplicitToolGrants = append([]string(nil), rec.DelegateRestore.ExplicitToolGrants...)
		clone.DelegateRestore = &restore
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
		SessionID: snapshot.SessionID,
		Ref:       snapshot.Ref,
		Label:     snapshot.Label,
		Entries:   make([]appwire.JobActivityEntry, 0),
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
	delegateGroups := groupActivityDelegates(records)
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
		case jobstore.JobDelegate:
			if rec.DelegateID == "" {
				if !activityConsumeWorkUnit(budget, 1) {
					markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path)
					projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
					return projected
				}
				appendActivityBranchError(&projected.Branch, fmt.Sprintf("delegate job %q has no delegate id", rec.JobID))
				continue
			}
			group := delegateGroups[rec.DelegateID]
			if group == nil || group.anchor.JobID != rec.JobID {
				continue
			}
			if !activityConsumeWorkUnit(budget, len(group.turns)) {
				markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path)
				projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
				return projected
			}
			delegate := projectActivityDelegate(snapshot, group, budget, depth, path)
			projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "delegate", Delegate: &delegate})
		default:
			appendActivityBranchError(&projected.Branch, fmt.Sprintf("job %q has unsupported type %q", rec.JobID, rec.Type))
		}
	}

	projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
	return projected
}

type activityDelegateGroup struct {
	anchor *jobstore.JobRecord
	turns  []*jobstore.JobRecord
}

func groupActivityDelegates(records []*jobstore.JobRecord) map[string]*activityDelegateGroup {
	groups := make(map[string]*activityDelegateGroup)
	for _, rec := range records {
		if rec == nil || rec.Type != jobstore.JobDelegate || rec.DelegateID == "" {
			continue
		}
		group := groups[rec.DelegateID]
		if group == nil {
			group = &activityDelegateGroup{}
			groups[rec.DelegateID] = group
		}
		group.turns = append(group.turns, rec)
	}
	for _, group := range groups {
		sort.SliceStable(group.turns, func(i, j int) bool {
			return activityRecordBefore(group.turns[i], group.turns[j])
		})
		group.anchor = group.turns[0]
	}
	return groups
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

func projectActivityDelegate(snapshot activitySessionSnapshot, group *activityDelegateGroup, budget *activityBudget, depth int, path []string) appwire.JobActivityDelegate {
	anchor := group.anchor
	delegate := appwire.JobActivityDelegate{
		DelegateID: anchor.DelegateID,
		Mandate:    activityMandate(anchor),
		Turns:      make([]appwire.JobActivityJob, 0, len(group.turns)),
	}
	for _, turn := range group.turns {
		delegate.Turns = append(delegate.Turns, projectActivityJob(turn, snapshot.Ref))
		if delegate.Mandate == "" {
			delegate.Mandate = activityMandate(turn)
		}
	}
	record := snapshot.Delegates[anchor.DelegateID]
	if record == nil {
		delegate.Branch.Error = fmt.Sprintf("delegate %q record unavailable", anchor.DelegateID)
		return delegate
	}
	if record.DelegateID != anchor.DelegateID {
		delegate.Branch.Error = fmt.Sprintf("delegate %q record identifies %q", anchor.DelegateID, record.DelegateID)
		return delegate
	}
	if record.ChildSessionID == "" || record.TranscriptRef == "" {
		delegate.Branch.Error = fmt.Sprintf("delegate %q has an incomplete child link", anchor.DelegateID)
		return delegate
	}

	delegate.ChildSessionID = record.ChildSessionID
	delegate.ChildRef = record.TranscriptRef
	if err := snapshot.Errors[record.ChildSessionID]; err != nil {
		delegate.Branch.Error = err.Error()
		return delegate
	}
	child := snapshot.Children[record.ChildSessionID]
	if child == nil {
		delegate.Branch.Error = fmt.Sprintf("child session %q unavailable", record.ChildSessionID)
		return delegate
	}
	if child.SessionID != record.ChildSessionID || child.Ref != record.TranscriptRef {
		delegate.Branch.Error = fmt.Sprintf("delegate %q child link does not match loaded session", anchor.DelegateID)
		return delegate
	}
	childPath := appendActivityPath(path, anchor.DelegateID)
	if budget != nil && budget.bounded && depth >= budget.maxDepth {
		markActivityDelegateTruncated(&delegate, budget, child.SessionID, childPath)
		return delegate
	}
	projectedChild := projectActivitySessionAt(*child, budget, depth+1, childPath)
	delegate.Child = &projectedChild
	return delegate
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

func activityMandate(rec *jobstore.JobRecord) string {
	if rec == nil {
		return ""
	}
	if rec.Task != "" {
		return rec.Task
	}
	return rec.Description
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
		for _, turn := range entry.Delegate.Turns {
			addJob(turn)
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

// projectActivityJob projects the shared job fields used by both the activity
// tree and the temporary flat jobs-list compatibility path.
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
		TranscriptRef:  rec.TranscriptRef,
		Type:           string(rec.Type),
		Status:         string(rec.Status),
		Outcome:        outcome,
		Terminal:       terminal,
		Background:     rec.Background,
		HasOutput:      rec.OutputPath != "" || rec.OutputBytes > 0,
		Description:    description,
		Command:        rec.Command,
		Task:           rec.Task,
		Reason:         rec.Reason,
		StartedAt:      rec.StartedAt.UTC().Format(time.RFC3339),
		OutputBytes:    rec.OutputBytes,
	}
	if rec.EndedAt != nil {
		job.EndedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	if rec.ExitCode != nil {
		exit := *rec.ExitCode
		job.ExitCode = &exit
	}
	return job
}
