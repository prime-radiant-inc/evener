package agent

import (
	"context"
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

// activityContinuation is a real, checked cursor position (#448's
// incremental-fold round, closing #812 on this surface): resuming from one
// re-enters the exact session a prior page's mid-list cutoff stopped at
// (RootID/SessionID/Path, the pre-existing navigational shape) AND skips
// exactly the entries that session already rendered (ResumeIndex), instead
// of re-rendering that session's list from its own top with a fresh budget
// the way the pre-#448 mid-list continuation always has. JobsEpoch/
// DelegatesEpoch are the fold-cache generations (foldcache.Result.Epoch —
// see historicalJobFoldCache/historicalDelegateFoldCache in
// jobs_activity_past.go) SessionID's own jobs.jsonl and RootID's shared
// delegates.jsonl were at when this token was minted; loadActivitySnapshotForParams
// rejects a resume whose epochs no longer match what the fold caches
// currently report for those same paths, rather than silently applying
// ResumeIndex to a journal that was rewritten or shrunk out from under it.
// An ordinary append never bumps epoch (see foldcache.Result.Epoch's own
// doc comment) — append-only growth is exactly the case a resume must
// tolerate, not treat as staleness.
type activityContinuation struct {
	Version        int      `json:"v"`
	RootID         string   `json:"root"`
	SessionID      string   `json:"session"`
	Path           []string `json:"path"`
	ResumeIndex    int      `json:"idx,omitempty"`
	JobsEpoch      uint64   `json:"jobs_epoch,omitempty"`
	DelegatesEpoch uint64   `json:"dlg_epoch,omitempty"`
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
	// JobsEpoch and DelegatesEpoch are historicalJobFoldCache's and
	// historicalDelegateFoldCache's current generation counters for this
	// session's own jobs.jsonl and its root's shared delegates.jsonl,
	// respectively (0 for a LIVE session's own snapshot — loadLiveActivityBase
	// reads neither cache, and a live root's own data is always current, never
	// stale in the sense these caches guard against). Carried into any
	// continuation a mid-list cutoff on THIS session mints — see
	// activityContinuation and markActivitySessionTruncated.
	JobsEpoch      uint64
	DelegatesEpoch uint64
	Children       map[string]*activitySessionSnapshot // child session ID
	Errors         map[string]error                    // child session ID
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
	// Continuation paths are client-controlled (roborev finding on #807):
	// without this, a long valid path could force buildActivityContinuationAt
	// to open many historical sessions' files with no bound at all, since
	// ordinary (non-continuation) traversal's own depth limit
	// (activityMaxNewDepth) is enforced by buildActivityFullSnapshot's
	// recursion, which this path-following code doesn't go through.
	if len(cont.Path) > activityMaxNewDepth {
		return activityContinuation{}, fmt.Errorf("continuation path length %d exceeds %d", len(cont.Path), activityMaxNewDepth)
	}
	if cont.ResumeIndex < 0 {
		return activityContinuation{}, fmt.Errorf("continuation resume index %d is negative", cont.ResumeIndex)
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
		// context.Background() here is a real, known gap, not a soft one:
		// this root's OWN job list loads via loadLiveActivityBase ->
		// jm.store.LoadOrdered(), a completely separate, unbounded,
		// non-cancelable read on jobstore.Store's stateful, cursor-caching,
		// mutex-protected internal path (readAllLocked) — NOT the bounded
		// jobstore.ScanEvents this ctx would otherwise reach. Store.LoadOrdered
		// has one caller; retrofitting it with the same ScanLimits+ctx
		// ScanEvents uses would mean redesigning how its cursor-trust
		// invariants interact with a resumable, ceiling-respecting read,
		// inside code shared with the live append path — real, but out of
		// scope for this pass (#448 roborev finding 3; flagged for a
		// follow-up). What context.Background() DOES still reach: any
		// already-exited descendant of this live root, loaded through
		// loadHistoricalActivityBase's bounded scanners exactly like the
		// hub's persisted fallback. Separately, this path's own caller
		// (cmd/evener/serve.go's SetJobsFunc hook) takes no context to
		// thread through in the first place, so even a bounded LoadOrdered
		// would only gain the byte/event ceilings here, not cancellation.
		snapshot, startDepth, resumeIndex, err := loadActivitySnapshotForParams(context.Background(), root, params)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		return projectBoundedActivityTree(*snapshot, root.sessionID, startDepth, resumeIndex, 0, now)
	}
	return projectStableLiveActivityTreeAt(s.jobActivityClock, root.sessionID, now, func() (*activitySessionSnapshot, int, int, error) {
		return loadActivitySnapshotForParams(context.Background(), root, params)
	})
}

func projectStableLiveActivityTree(clock *jobActivityClock, rootID string, load func() (*activitySessionSnapshot, int, int, error)) (appwire.JobActivityTree, error) {
	return projectStableLiveActivityTreeAt(clock, rootID, time.Now().UTC(), load)
}

func projectStableLiveActivityTreeAt(clock *jobActivityClock, rootID string, now time.Time, load func() (*activitySessionSnapshot, int, int, error)) (appwire.JobActivityTree, error) {
	for range 8 {
		before := activityCurrentRootRevision(clock)
		snapshot, startDepth, resumeIndex, err := load()
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		after := activityCurrentRootRevision(clock)
		if before == after {
			return projectBoundedActivityTree(*snapshot, rootID, startDepth, resumeIndex, after, now)
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

// loadActivitySnapshotForParams builds a fresh historicalActivityCache for
// ctx and threads it through the whole recursive load, so the shared root
// delegate journal is scanned once no matter how many sessions this one
// build visits, and a canceled ctx stops before opening a later session's
// files (checked in loadHistoricalActivityBase). It returns the resumeIndex
// a continuation's mid-list cutoff carried (0 for a fresh, non-continuation
// load), for projectBoundedActivityTree to apply against the target
// session's own entries.
func loadActivitySnapshotForParams(ctx context.Context, root activitySessionLocator, params appwire.JobsListParams) (*activitySessionSnapshot, int, int, error) {
	cache := newHistoricalActivityCache(ctx, root.sessionID)
	if strings.TrimSpace(params.Continuation) == "" {
		visited := map[string]bool{root.sessionID: true}
		snapshot, err := buildActivityFullSnapshot(root, visited, false, cache, 0)
		return snapshot, 0, 0, err
	}
	cont, err := decodeActivityContinuation(params.Continuation, root.sessionID)
	if err != nil {
		return nil, 0, 0, err
	}
	visited := map[string]bool{root.sessionID: true}
	snapshot, jobsEpoch, delegatesEpoch, err := buildActivityContinuationSnapshot(root, cont, visited, false, cache)
	if err != nil {
		return nil, 0, 0, err
	}
	// The target session's fold-cache generations must still match what the
	// continuation was minted against: an ordinary append never moves
	// either epoch (see activityContinuation's doc comment), so this only
	// ever rejects a resume whose underlying journal was rewritten or
	// shrunk since — exactly the case ResumeIndex is unsafe to apply to.
	if cont.JobsEpoch != jobsEpoch || cont.DelegatesEpoch != delegatesEpoch {
		return nil, 0, 0, errors.New("activity continuation is stale: the underlying journal changed; restart pagination without a continuation")
	}
	return snapshot, -len(cont.Path), cont.ResumeIndex, nil
}

// buildActivityFullSnapshot loads loc's full subtree.
//
// depth is loc's own depth, explicit rather than derived from
// len(visited)-1 (roborev finding on #807's saturation commit): visited
// keeps growing across a continuation's whole hop chain for cycle
// detection, but projection resets ITS depth to 0 at the continuation
// target (loadActivitySnapshotForParams's startDepth = -len(cont.Path),
// reaching 0 exactly at the target — see decodeActivityContinuation).
// Deriving depth from len(visited)-1 here counted the ancestor chain
// leading UP TO a continuation target as part of the target's own depth,
// so a max-length continuation could make the target's own children look
// already past activityMaxNewDepth and load them as empty placeholders
// instead of the next page, even though projection treats the target as a
// fresh depth-0 root. Every caller passes 0 for a session that is
// projection's own depth-0 node (an ordinary tree root, OR a continuation
// target reached via any number of hops); the recursive call below passes
// depth+1 for an actual child.
func buildActivityFullSnapshot(loc activitySessionLocator, visited map[string]bool, required bool, cache *historicalActivityCache, depth int) (*activitySessionSnapshot, error) {
	loaded, err := loadActivityBase(loc, required, cache)
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
		// #448 finding 1 (load-time traversal bound): mirror
		// activityMaxNewDepth/activityMaxWorkUnits here so a wide or deep
		// tree can't force unbounded loading (file opens, recursion,
		// decoding) before projection's own budget ever gets a chance to
		// apply. The two limits need different treatment on the wire,
		// though (roborev finding: depth was silently dropping the child
		// with no marker at all):
		//
		//   - Depth: projectStableActivityDelegate dereferences
		//     snapshot.Children[childID] BEFORE its own depth check runs, so
		//     leaving the entry unset here surfaces as a generic "child
		//     session unavailable" branch error instead of the honest,
		//     continuation-bearing Truncated projection's depth-truncation
		//     already knows how to produce (markActivityDelegateTruncated).
		//     A placeholder child — present, but with nothing loaded under
		//     it — lets projection's own check run and do that correctly.
		//   - Work-unit exhaustion: projectActivitySessionAt's delegate loop
		//     consumes a unit and checks it BEFORE ever dereferencing
		//     snapshot.Children, so leaving the entry unset there already
		//     reaches projection's identical exhaustion point on this same,
		//     now-smaller tree — no placeholder needed.
		if depth >= cache.budget.maxDepth {
			// Ref must equal descriptor.TranscriptRef, not a hard-coded
			// local ref (roborev finding on #807): projectStableActivityDelegate
			// validates child.Ref != descriptor.TranscriptRef before its own
			// depth check runs, so a placeholder built from a different ref
			// shape than the descriptor's own would mismatch and fall into
			// "child link does not match loaded session" instead of the
			// honest depth-truncation branch. activityChildSessionForStable
			// above already rejects a non-local TranscriptRef outright, so
			// today row.descriptor.TranscriptRef is always
			// encodeRef("", childID) too — this keeps the placeholder
			// correct by construction rather than by that coincidence.
			snapshot.Children[childID] = &activitySessionSnapshot{
				SessionID:       childID,
				Ref:             row.descriptor.TranscriptRef,
				LiveJobs:        map[string]*jobstore.JobRecord{},
				StableDelegates: map[string]delegateSnapshot{},
				Children:        map[string]*activitySessionSnapshot{},
				Errors:          map[string]error{},
			}
			continue
		}
		if !activityConsumeWorkUnit(cache.budget, 1) {
			continue
		}
		childLoc, err := resolveActivityChildByID(loc, loaded, childID)
		if err != nil {
			snapshot.Errors[childID] = err
			continue
		}
		nextVisited := cloneActivityVisited(visited)
		nextVisited[childID] = true
		child, err := buildActivityFullSnapshot(childLoc, nextVisited, true, cache, depth+1)
		if err != nil {
			// A canceled request must surface as a real error all the way to
			// the caller, not be laundered into a per-child branch error
			// that leaves the parent — and ultimately
			// LoadSessionJobActivityTree — reporting success with a
			// silently missing subtree (#448 finding 2 / regression review
			// finding 1). Any OTHER per-child error (corruption, a missing
			// state dir, …) keeps the existing behavior: record it on this
			// one branch and keep visiting siblings.
			if cache.ctx.Err() != nil {
				return nil, cache.ctx.Err()
			}
			snapshot.Errors[childID] = err
			continue
		}
		snapshot.Children[childID] = child
	}
	return &snapshot, nil
}

// buildActivityContinuationSnapshot re-enters the session cont's Path chain
// points at and returns its ROOT-shaped, single-child-filtered snapshot for
// the wire — the pre-existing navigational shape — ALONGSIDE the TARGET
// session's own JobsEpoch/DelegatesEpoch (not the outer root's; the target
// is what ResumeIndex was minted against and what it must be checked
// against on resume — see loadActivitySnapshotForParams).
func buildActivityContinuationSnapshot(loc activitySessionLocator, cont activityContinuation, visited map[string]bool, required bool, cache *historicalActivityCache) (*activitySessionSnapshot, uint64, uint64, error) {
	if len(cont.Path) == 0 {
		if loc.sessionID != cont.SessionID {
			return nil, 0, 0, fmt.Errorf("continuation session %q does not match root %q", cont.SessionID, loc.sessionID)
		}
		// No hops: loc IS the continuation target, projection's own
		// depth-0 node (see buildActivityFullSnapshot's doc comment).
		snapshot, err := buildActivityFullSnapshot(loc, visited, required, cache, 0)
		if err != nil {
			return nil, 0, 0, err
		}
		return snapshot, snapshot.JobsEpoch, snapshot.DelegatesEpoch, nil
	}
	return buildActivityContinuationAt(loc, cont, 0, visited, required, cache)
}

func buildActivityContinuationAt(loc activitySessionLocator, cont activityContinuation, hop int, visited map[string]bool, required bool, cache *historicalActivityCache) (*activitySessionSnapshot, uint64, uint64, error) {
	if hop == len(cont.Path) {
		if loc.sessionID != cont.SessionID {
			return nil, 0, 0, fmt.Errorf("continuation session %q does not match resolved path %q", cont.SessionID, loc.sessionID)
		}
		// loc is the continuation target: projection's own depth-0 node
		// (see buildActivityFullSnapshot's doc comment) regardless of how
		// many hops led here — NOT len(visited)-1, which would count the
		// whole ancestor chain as depth already consumed.
		snapshot, err := buildActivityFullSnapshot(loc, visited, required, cache, 0)
		if err != nil {
			return nil, 0, 0, err
		}
		return snapshot, snapshot.JobsEpoch, snapshot.DelegatesEpoch, nil
	}
	// Charge this hop against the shared load budget the same way
	// buildActivityFullSnapshot charges each child it visits (roborev
	// finding on #807): decodeActivityContinuation's path-length cap bounds
	// how many hops a SINGLE continuation can name, but says nothing about
	// how much of the tree-wide load budget resolving them consumes --
	// loadActivityBase below opens files, and without this, that happened
	// unconditionally, once per hop, with no bound of its own.
	if !activityConsumeWorkUnit(cache.budget, 1) {
		return nil, 0, 0, errors.New("continuation path exhausted the load budget")
	}
	loaded, err := loadActivityBase(loc, required, cache)
	if err != nil {
		return nil, 0, 0, err
	}
	delegateID := cont.Path[hop]
	if row, ok := loaded.snapshot.StableDelegates[delegateID]; ok {
		childID, err := activityChildSessionForStable(row)
		if err != nil {
			return nil, 0, 0, err
		}
		if visited[childID] {
			return nil, 0, 0, errors.New("cycle detected")
		}
		childLoc, err := resolveActivityChildByID(loc, loaded, childID)
		if err != nil {
			return nil, 0, 0, err
		}
		nextVisited := cloneActivityVisited(visited)
		nextVisited[childID] = true
		child, jobsEpoch, delegatesEpoch, err := buildActivityContinuationAt(childLoc, cont, hop+1, nextVisited, true, cache)
		if err != nil {
			return nil, 0, 0, err
		}
		filtered := activityFilterSnapshotToDelegate(loaded.snapshot, delegateID, child)
		return &filtered, jobsEpoch, delegatesEpoch, nil
	}
	return nil, 0, 0, fmt.Errorf("continuation path hop %q not found", delegateID)
}

func loadActivityBase(loc activitySessionLocator, required bool, cache *historicalActivityCache) (activityLoadedBase, error) {
	if loc.live != nil {
		return loadLiveActivityBase(loc.live)
	}
	return loadHistoricalActivityBase(loc.stateDir, loc.sessionID, required, cache)
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

func projectBoundedActivityTree(snapshot activitySessionSnapshot, rootID string, startDepth, resumeIndex int, revision uint64, now time.Time) (appwire.JobActivityTree, error) {
	budget := newBoundedActivityBudget(rootID, now)
	root := projectActivitySessionAt(snapshot, budget, startDepth, nil, resumeIndex)
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
	return projectActivitySessionAt(snapshot, budget, 0, nil, 0)
}

// projectActivitySessionAt renders snapshot's own entries (its owned shell
// jobs, then its stable delegates — the fixed order every entryIndex below
// counts against) into one appwire.JobActivitySession, recursing into each
// delegate's child subtree. resumeIndex skips that many leading entries
// without consuming budget or rendering them — the #448/#812 fix: a
// continuation minted by markActivitySessionTruncated below carries exactly
// this position, so a resumed page picks up where the truncated one left
// off instead of re-rendering snapshot's list from the top with a fresh
// budget.
//
// resumeIndex is meaningful only for the ONE session a continuation names —
// but for a continuation whose Path has hops (the truncated session was a
// nested delegate, not the tree's own root), that session is reached only
// after buildActivityContinuationAt's filtered ancestor chain is
// re-descended through len(Path) recursive projectStableActivityDelegate
// calls, not at THIS function's own top-level call. depth carries exactly
// that position already (loadActivitySnapshotForParams sets the top-level
// call's startDepth to -len(cont.Path), so depth reaches 0 exactly once,
// at the same node a plain, non-continuation load would call depth 0 — its
// own root, or here, the continuation's target session) — so resumeIndex
// is threaded UNCHANGED through every recursive call below and applied only
// where depth == 0, rather than being reset to 0 for children: a
// zero-hop continuation (target IS the root) needs it applied at THIS
// call's depth 0 too, which passing 0 to every recursive call would miss
// whenever depth's OWN starting point was already negative.
func projectActivitySessionAt(snapshot activitySessionSnapshot, budget *activityBudget, depth int, path []string, resumeIndex int) appwire.JobActivitySession {
	if budget == nil {
		budget = newActivityBudget()
	}
	effectiveResumeIndex := 0
	if depth == 0 {
		effectiveResumeIndex = resumeIndex
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

	entryIndex := 0
	records := activityOwnedRecords(snapshot.SessionID, mergeActivityRecords(snapshot.Jobs, snapshot.LiveJobs))
	for _, rec := range records {
		if rec == nil {
			continue
		}
		switch rec.Type {
		case jobstore.JobShell:
			if entryIndex < effectiveResumeIndex {
				entryIndex++
				continue
			}
			if !activityConsumeWorkUnit(budget, 1) {
				markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path, entryIndex, snapshot.JobsEpoch, snapshot.DelegatesEpoch)
				projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
				return projected
			}
			entryIndex++
			job := projectActivityJob(rec, snapshot.Ref)
			projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "shell", Job: &job})
		default:
			appendActivityBranchError(&projected.Branch, fmt.Sprintf("job %q has unsupported type %q", rec.JobID, rec.Type))
		}
	}
	for _, delegateID := range sortedStableActivityDelegateIDs(snapshot.StableDelegates) {
		if entryIndex < effectiveResumeIndex {
			entryIndex++
			continue
		}
		if !activityConsumeWorkUnit(budget, 1) {
			markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path, entryIndex, snapshot.JobsEpoch, snapshot.DelegatesEpoch)
			projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
			return projected
		}
		entryIndex++
		delegate := projectStableActivityDelegate(snapshot, snapshot.StableDelegates[delegateID], budget, depth, path, resumeIndex)
		projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "delegate", Delegate: &delegate})
	}

	projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
	return projected
}

func projectStableActivityDelegate(snapshot activitySessionSnapshot, row delegateSnapshot, budget *activityBudget, depth int, path []string, resumeIndex int) appwire.JobActivityDelegate {
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
	projectedChild := projectActivitySessionAt(*child, budget, depth+1, childPath, resumeIndex)
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

// activityBudgetRemaining is how many more work units budget has left before
// activityConsumeWorkUnit starts refusing. Used by the LOAD phase (#448
// finding 1) as a pure skip/no-skip gate on a session's own journal scan
// (roborev finding on #807's saturation commit: this once fed a raw
// MaxEvents ceiling, "sizing" the scan — it no longer does; the scan itself
// always uses the fixed historicalJobScanLimits, and this only decides
// whether to run it at all once the tree-wide budget is already exhausted)
// — the same budget projection already enforces, just consulted one phase
// earlier. An unbounded budget (nil, or bounded==false) has no ceiling to
// report; callers treat that as "don't skip this scan," so this only needs
// to return a sentinel they won't mistake for zero remaining.
func activityBudgetRemaining(budget *activityBudget) int {
	if budget == nil || !budget.bounded {
		return -1
	}
	remaining := budget.maxWorkUnits - budget.usedWork
	if remaining < 0 {
		return 0
	}
	return remaining
}

// activityOwnedShellCount is the number of work units loading sessionID's
// jobs would consume in projection: one per owned, shell-typed record —
// exactly what projectActivitySessionAt's own loop counts
// (activityOwnedRecords + the JobShell case). The load phase (#448 finding 1)
// consumes this same count from the shared traversal budget so aggregate
// work across an entire tree is bounded during loading, not only afterward
// during projection.
func activityOwnedShellCount(sessionID string, jobs []*jobstore.JobRecord) int {
	count := 0
	for _, rec := range activityOwnedRecords(sessionID, jobs) {
		if rec != nil && rec.Type == jobstore.JobShell {
			count++
		}
	}
	return count
}

// markActivitySessionTruncated marks a mid-list cutoff within sessionID's
// own entries (jobs then delegates, projectActivitySessionAt's iteration
// order) and mints a REAL, advancing continuation: resumeIndex is how many
// of sessionID's entries have now been accounted for across every page up
// to and including this one (projectActivitySessionAt's entryIndex at the
// moment the budget ran out), so a resume picks up exactly where this page
// stopped instead of re-rendering from the top (#812's fix on this
// surface — the round-3 "Truncated with no continuation" state this
// replaced existed only because, before #448's incremental-fold round, a
// resumed load re-scanned this same session's own journal from byte zero
// with a fresh budget and could never actually advance; folding through
// historicalJobFoldCache/historicalDelegateFoldCache instead of rescanning
// removes that reason). jobsEpoch/delegatesEpoch are the fold-cache
// generations (see activityContinuation) sessionID's own jobs.jsonl and the
// root's shared delegates.jsonl were at when this snapshot was built —
// carried in the continuation so a later resume can detect a rewrite that
// invalidates resumeIndex's meaning rather than silently applying it to
// different data (checked in loadActivitySnapshotForParams).
//
// This also closes roborev's separate #807 finding that a LOAD-phase
// truncation (snapshot.LoadTruncated, in the pre-incremental-fold design)
// could coincide with a projection-phase budget trip on the same session
// and mint a non-advancing continuation: that scenario cannot arise here,
// because there is no more silent load-phase truncation to coincide with —
// loadHistoricalActivityBase now either loads a session's full history
// through the fold caches or fails loudly (ErrLineTooLong), never silently
// degrades to a partial LoadTruncated snapshot.
func markActivitySessionTruncated(session *appwire.JobActivitySession, budget *activityBudget, sessionID string, path []string, resumeIndex int, jobsEpoch, delegatesEpoch uint64) {
	if session == nil {
		return
	}
	session.Branch.Truncated = true
	if budget != nil && budget.rootID != "" {
		session.Branch.Continuation = encodeActivityContinuation(activityContinuation{
			Version:        activityContinuationV1,
			RootID:         budget.rootID,
			SessionID:      sessionID,
			Path:           append([]string(nil), path...),
			ResumeIndex:    resumeIndex,
			JobsEpoch:      jobsEpoch,
			DelegatesEpoch: delegatesEpoch,
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
