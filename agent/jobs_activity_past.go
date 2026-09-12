package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/foldcache"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

// activityUsageCache memoizes per-transcript cumulative usage totals (keyed by
// file identity) so repeat activity-tree fetches don't rescan every retained
// child transcript.
var activityUsageCache = apptranscript.NewTurnCache()

// scanJobJournal and scanDelegateJournal are package vars — like
// historicalJobsStat below — so tests can count or intercept scans (proving
// a journal is read incrementally, or that cancellation stops before a
// later session's file is opened) without instrumenting the filesystem.
// Both wrap the *From variant: every journal is read through
// historicalJobFoldCache/historicalDelegateFoldCache below, which always
// resumes from a byte offset (0 on first touch).
var scanJobJournal = jobstore.ScanEventsFrom
var scanDelegateJournal = delegatestore.ScanEventsFrom

// historicalJobFoldCacheEntries and historicalDelegateFoldCacheEntries bound
// the two fold caches below by NUMBER OF DISTINCT JOURNALS retained, not by
// byte size. This is a real, honest tradeoff, not a "small so it's fine"
// claim: neither jobstore.FoldOrdered's []Event slice nor a
// delegatestore.State's *Aggregate values (which retain the full
// FrozenRolePrompt/FrozenSkillBodies text delegate_created embeds — Apply
// clones the descriptor verbatim, and the delegate-restart path in
// subagents.go needs that full text later, so this cache does not strip it)
// are small in the worst case. What bounds worst-case memory to a KNOWN,
// finite quantity is the entry count: at most N journals resident at once,
// LRU-evicted, regardless of how large any one of them is. Sizing:
//   - Job sessions: the largest observed in production was 6.99 MiB / 562
//     jobs; a "Tuesday" 200k-job session is the stress case this cache
//     must survive, not the common one. 256 distinct sessions comfortably
//     covers a hub actively serving many concurrent job-activity-tree
//     views without needing to reread any of them on the next poll.
//   - Delegate roots: one entry per ROOT (shared across every visited
//     session under it, not per-session), and each can plausibly carry
//     ~10 KB/event of frozen prompt/skill text — a smaller number is the
//     deliberate tradeoff for a per-entry cost that
//     can run larger. 64 distinct roots is still generous for how many
//     independently-rooted trees a hub process realistically has open at
//     once.
//
// TurnCache (internal/apptranscript), the same per-file-identity memoization
// shape for transcripts, defaults to 32 total — these two split that budget
// across TWO caches with different per-entry cost profiles rather than
// reusing one number for both.
const (
	historicalJobFoldCacheEntries      = 256
	historicalDelegateFoldCacheEntries = 64
)

// historicalJobFold is jobstore's fold-cache payload: the raw events read so
// far. FoldOrdered runs fresh over the full accumulated slice on every Get
// rather than folding incrementally — unlike delegatestore's Apply (see
// historicalDelegateFold below), jobstore.Fold/FoldOrdered are already a
// cheap single sorted pass with no per-event state-cloning cost, so an
// incremental merge would only add complexity for no measurable gain.
type historicalJobFold struct {
	events []jobstore.Event
	// records is jobstore.FoldOrdered(events), computed once per Extend
	// call (i.e., once per OBSERVED append), not once per Get call.
	// FoldOrdered is cheap on its own (a single sorted pass — unlike
	// delegatestore's Apply, it does not re-clone and re-diff the whole
	// state per event, see historicalDelegateFold's doc comment) but it is
	// still O(total events), and a hub polling an unchanged file must not
	// pay that cost on every request just because it asked again:
	// foldcache.Cache.Get's own "true hit" path already skips calling
	// Extend at all when nothing changed, so caching the fold alongside
	// the events it came from is what actually lets a hit return in O(1)
	// instead of O(total events) every time.
	records []*jobstore.JobRecord
}

var historicalJobFoldCache = foldcache.New[historicalJobFold](historicalJobFoldCacheEntries)

// extendHistoricalJobFold is historicalJobFoldCache's foldcache.Extend: it
// reads only the events appended since fromOffset, appends them to a FRESH
// copy of prior's (never mutates prior.events in place, since
// foldcache.Cache may still be holding prior as another caller's Result),
// and folds the combined history once.
func extendHistoricalJobFold(ctx context.Context, path string, fromOffset int64, prior historicalJobFold) (historicalJobFold, int64, error) {
	delta, toOffset, err := scanJobJournal(ctx, path, fromOffset, jobstore.ScanLimits{})
	if err != nil {
		return historicalJobFold{}, 0, err
	}
	events := make([]jobstore.Event, 0, len(prior.events)+len(delta))
	events = append(events, prior.events...)
	events = append(events, delta...)
	return historicalJobFold{events: events, records: jobstore.FoldOrdered(events)}, toOffset, nil
}

// loadCachedJobRecords returns jobsPath's complete, folded history via
// historicalJobFoldCache, replaying only what was appended since the last
// call for this path in this process (and re-folding only when something
// was — see historicalJobFold.records). The returned epoch is
// historicalJobFoldCache's generation counter for jobsPath at the moment of
// this read — see foldcache.Result.Epoch and activityContinuation.JobsEpoch.
func loadCachedJobRecords(ctx context.Context, jobsPath string) ([]*jobstore.JobRecord, uint64, error) {
	result, err := historicalJobFoldCache.Get(ctx, jobsPath, extendHistoricalJobFold)
	if err != nil {
		return nil, 0, err
	}
	return result.Value.records, result.Epoch, nil
}

// historicalDelegateFold is delegatestore's fold-cache payload. Unlike
// historicalJobFold, this retains the FOLDED delegatestore.State directly
// (not raw events), extended incrementally via delegatestore.Apply for each
// new event: delegatestore.Apply/Fold internally clone the ENTIRE state and
// re-diff it on every single event applied (agent/internal/delegatestore/
// fold.go), so re-folding a growing root's full event history from scratch
// on every request — the same treatment jobstore gets above — would cost
// O(events x aggregates) per request instead of O(new events x aggregates).
// eventCount tracks how many events have been applied so far, so a delta can
// still be checked for sequence continuity (event.Seq must equal
// eventCount+1 as each is applied) the same way delegatestore.Fold checks a
// from-scratch scan, even though Fold itself cannot be called on a
// non-1-based delta (see delegatestore.ScanEventsFrom's doc comment).
type historicalDelegateFold struct {
	state      delegatestore.State
	eventCount int
	tornTail   bool
}

var historicalDelegateFoldCache = foldcache.New[historicalDelegateFold](historicalDelegateFoldCacheEntries)

// extendHistoricalDelegateFold is historicalDelegateFoldCache's
// foldcache.Extend. maps.Clone(prior.state) before Apply is load-bearing,
// not defensive boilerplate: Apply mutates the map it is given in place
// (agent/internal/delegatestore/fold.go), and foldcache.Cache may still be
// holding prior.state as another caller's already-returned Result.Value —
// mutating it in place would race that caller. Apply itself deep-clones
// each *Aggregate internally before writing back into whatever map it was
// given, so a SHALLOW maps.Clone here (copying the map's pointers, not each
// Aggregate) is sufficient: the clone becomes Apply's write target, the
// original prior.state and its aggregates are never touched.
func extendHistoricalDelegateFold(ctx context.Context, path string, fromOffset int64, prior historicalDelegateFold) (historicalDelegateFold, int64, error) {
	delta, toOffset, readDiagnostics, err := scanDelegateJournal(ctx, path, fromOffset, delegatestore.ScanLimits{})
	if err != nil {
		return historicalDelegateFold{}, 0, err
	}
	state := maps.Clone(prior.state)
	if state == nil {
		state = make(delegatestore.State)
	}
	count := prior.eventCount
	for _, event := range delta {
		count++
		if event.Seq != uint64(count) {
			return historicalDelegateFold{}, 0, fmt.Errorf("delegatestore: delegate event sequence %d, want %d", event.Seq, count)
		}
		if err := delegatestore.Apply(state, event); err != nil {
			return historicalDelegateFold{}, 0, fmt.Errorf("delegate event %d: %w", event.Seq, err)
		}
	}
	return historicalDelegateFold{state: state, eventCount: count, tornTail: readDiagnostics.TornTail}, toOffset, nil
}

// rootDelegateIndex is one root's delegates.jsonl folded via the shared
// cache, then indexed by OwnerSessionID so every visited session in the
// tree looks up its own rows without re-folding the shared journal itself
// (historicalDelegateFoldCache already avoids the underlying re-READ; this
// index avoids repeating the small O(delegates) grouping pass per session
// within one traversal).
type rootDelegateIndex struct {
	byOwner     map[string][]string // ownerSessionID -> sorted delegate IDs
	state       delegatestore.State
	epoch       uint64
	diagnostics []string
}

// historicalActivityCache threads cancellation and shares per-traversal
// state — the root delegate index above — through the recursive historical
// loaders that both the live and persisted activity-tree entry points share.
// It is created fresh for one loadActivitySnapshotForParams call and never
// outlives it, the same traversal-local scope activityBudget already uses in
// jobs_activity.go. This is distinct from (and much shorter-lived than)
// historicalJobFoldCache/historicalDelegateFoldCache above, which persist
// for the whole process so a LATER traversal — a different request, a later
// poll of the same tree — benefits from what an EARLIER one already read.
type historicalActivityCache struct {
	ctx           context.Context
	delegateIndex map[string]*rootDelegateIndex // rootSessionID -> index, lazy
	// budget bounds the LOAD-phase traversal's breadth and depth — how many
	// distinct sessions and delegates get VISITED at all — across this
	// whole cache's lifetime. It reuses activityBudget/
	// activityConsumeWorkUnit, the same primitives
	// projectBoundedActivityTree applies afterward for a DIFFERENT purpose
	// (how much of what WAS visited gets rendered in one page). It does
	// NOT cap how much of any ONE session's own journal gets read: folding
	// a session's complete history is cheap (historicalJobFoldCache
	// above), so there is no reason to cut it short at load time; a wide
	// or deep TREE can still force unbounded traversal work (file opens,
	// cache lookups) independent of how cheap any one file's own read is,
	// which is what this budget exists to bound.
	// rootID/now/revision are irrelevant here (only read by the
	// projection-side continuation/status helpers this budget instance
	// never calls).
	budget *activityBudget
}

// newHistoricalActivityCache builds a fresh, traversal-scoped cache for one
// loadActivitySnapshotForParams call. rootID only flows into
// newBoundedActivityBudget for parity with that constructor's other
// (projection-side) caller — the load-phase budget itself never reads
// activityBudget.rootID, since load-phase truncation mints no continuation:
// all truncation happens at projection, which always mints a real,
// advancing one — see jobs_activity.go's activityContinuation. revision 0
// for the same reason.
func newHistoricalActivityCache(ctx context.Context, rootID string) *historicalActivityCache {
	return &historicalActivityCache{
		ctx:           ctx,
		delegateIndex: map[string]*rootDelegateIndex{},
		budget:        newBoundedActivityBudget(rootID, time.Time{}, 0),
	}
}

// scanRootDelegateState folds rootSessionID's shared delegates.jsonl via
// historicalDelegateFoldCache, returning the folded state, the fold-cache's
// current epoch for this path (see activityContinuation.DelegatesEpoch), and
// the diagnostics the fold observed. Both readers of that journal — the
// memoized traversal index (rootDelegates) and the single-shot status read
// (loadHistoricalStableActivityWithAttention) — go through here so the
// diagnostic wording stays defined once instead of drifting between two
// copies.
func scanRootDelegateState(ctx context.Context, stateDir, rootSessionID string) (delegatestore.State, uint64, []string, error) {
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	result, err := historicalDelegateFoldCache.Get(ctx, path, extendHistoricalDelegateFold)
	if err != nil {
		if errors.Is(err, delegatestore.ErrLineTooLong) {
			// This journal is shared by every session under rootSessionID,
			// and both readers below (the recursive activity-tree walk via
			// rootDelegates, and LoadSessionDelegateStatus's single-shot
			// read via loadHistoricalStableActivityWithAttention, reached
			// from the hub's ThreadRead RPC) depend on it, so propagating
			// this unclassified would hard-fail the WHOLE activity tree,
			// and separately the primary chat/transcript view (live or
			// historical), for every session sharing this root over a
			// single corrupt line. Posture ruling: loud but CONTAINED.
			// Degrade to an
			// empty delegate set (there is no partial result to salvage
			// past a corrupt line ErrLineTooLong itself never returns
			// one for) with a diagnostic that names the file and line
			// (err's own message already carries both), rather than
			// failing the caller outright.
			return delegatestore.State{}, 0, []string{fmt.Sprintf("delegate_journal_line_too_long: %v", err)}, nil
		}
		return nil, 0, nil, err
	}
	var diagnostics []string
	if result.Value.tornTail {
		diagnostics = append(diagnostics, "delegate_journal_torn_tail: ignored unterminated trailing batch")
	}
	return result.Value.state, result.Epoch, diagnostics, nil
}

// rootDelegates returns rootSessionID's delegate index, folding
// delegates.jsonl on the first request for that root THIS TRAVERSAL and
// reusing the result for every later visited session sharing the same
// root, so a traversal visiting many sessions under one root avoids
// repeating the cheap byOwner grouping pass for each of them. The
// underlying read itself is separately deduplicated across traversals by
// historicalDelegateFoldCache.
func (c *historicalActivityCache) rootDelegates(stateDir, rootSessionID string) (*rootDelegateIndex, error) {
	if idx, ok := c.delegateIndex[rootSessionID]; ok {
		return idx, nil
	}
	state, epoch, diagnostics, err := scanRootDelegateState(c.ctx, stateDir, rootSessionID)
	if err != nil {
		return nil, err
	}
	byOwner := make(map[string][]string)
	for id, aggregate := range state {
		if aggregate == nil {
			continue
		}
		owner := aggregate.Descriptor.OwnerSessionID
		byOwner[owner] = append(byOwner[owner], id)
	}
	for owner := range byOwner {
		sort.Strings(byOwner[owner])
	}
	idx := &rootDelegateIndex{byOwner: byOwner, state: state, epoch: epoch, diagnostics: diagnostics}
	c.delegateIndex[rootSessionID] = idx
	return idx, nil
}

// historicalActivityUsage sums a retained session's own token usage from its
// transcript. nil (not zero) when the transcript carries no usage, so the wire
// omits the field and the UI hides the token cluster rather than rendering
// ↑0 ↓0.
func historicalActivityUsage(stateDir, sessionID string, meta schema.SessionMeta) *appwire.EvenerUsage {
	path := filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
	total, err := activityUsageCache.UsageTotalFromFile(path, transcriptJSONLMaxLineBytes, meta.DivergenceTurn)
	if err != nil {
		return nil
	}
	return total
}

// LoadSessionJobActivityTree loads and projects a session's persisted job
// activity tree. ctx is checked between visited sessions (and between
// records within each journal, inside the scanners) so a canceled hub
// request stops opening further files instead of finishing an unbounded
// walk.
func LoadSessionJobActivityTree(ctx context.Context, stateDir, sessionID string, params appwire.JobsListParams) (appwire.JobActivityTree, error) {
	if err := validateActivityRootRef(params.Ref, sessionID); err != nil {
		return appwire.JobActivityTree{}, err
	}
	root := activitySessionLocator{stateDir: stateDir, sessionID: sessionID}
	// loadActivitySnapshotForParamsWithCache, not the cache-discarding
	// loadActivitySnapshotForParams: the revision computation below
	// re-walks the whole tree from the root, and must share this SAME
	// cache (and so the same work-unit budget) rather than starting a
	// second, independently-fresh one — see
	// loadActivitySnapshotForParamsWithCache's doc comment.
	snapshot, startDepth, resumeIndex, cache, err := loadActivitySnapshotForParamsWithCache(ctx, root, params)
	if err != nil {
		return appwire.JobActivityTree{}, err
	}
	rootRevisionID := strings.TrimSpace(snapshot.RootID)
	if rootRevisionID == "" {
		rootRevisionID = sessionID
	}
	revision := activitySnapshotPersistedRevision(snapshot, rootRevisionID)
	if strings.TrimSpace(params.Continuation) != "" {
		full, err := buildActivityFullSnapshot(root, map[string]bool{sessionID: true}, false, cache, 0)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		revision = activitySnapshotPersistedRevision(full, rootRevisionID)
	}
	return projectBoundedActivityTree(*snapshot, sessionID, startDepth, resumeIndex, revision, time.Now().UTC())
}

func loadHistoricalActivityBase(stateDir, sessionID string, required bool, cache *historicalActivityCache) (activityLoadedBase, error) {
	// Checked first, before any file is opened: a request canceled while an
	// earlier session in the same traversal was loading must not go on to
	// open THIS session's jobs.jsonl or the shared delegates.jsonl either.
	if err := cache.ctx.Err(); err != nil {
		return activityLoadedBase{}, err
	}
	// sessionID can arrive here as a descendant ID read out of a persisted job
	// record (buildActivityContinuationAt), not only as the already-indexed
	// root, so it must be checked before any join into a path below — the
	// meta load a couple of lines down validates too, but jobsPath is built
	// unconditionally right after it regardless of metaErr.
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return activityLoadedBase{}, err
	}
	meta, metaErr := schema.LoadSessionMeta(stateDir, sessionID)
	rootID := activityRootIDFromMeta(sessionID, meta)
	jobsPath := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	var jobs []*jobstore.JobRecord
	var jobsEpoch uint64
	if _, err := historicalJobsStat(jobsPath); err != nil {
		if !os.IsNotExist(err) {
			return activityLoadedBase{}, err
		}
		if required {
			return activityLoadedBase{}, fmt.Errorf("child session %q unavailable in state directory", sessionID)
		}
	} else {
		// This session's COMPLETE job history loads unconditionally here —
		// historicalJobFoldCache makes that O(events appended since the
		// last read of this path in this process), not O(file size), so
		// there is no reason to cap it at the traversal's remaining
		// work-unit budget. That budget still bounds how many SESSIONS get
		// visited (buildActivityFullSnapshot's per-child
		// activityConsumeWorkUnit), just not how much of any ONE session's
		// own journal gets read. How much of THIS session's jobs actually
		// get RENDERED is purely projectActivitySessionAt's call, backed
		// by a real, advancing continuation rather than a load-time
		// truncation with nothing to resume into.
		records, epoch, err := loadCachedJobRecords(cache.ctx, jobsPath)
		if err != nil {
			return activityLoadedBase{}, err
		}
		jobs = records
		jobsEpoch = epoch
	}
	stable, delegatesEpoch, diagnostics, err := loadHistoricalStableActivity(cache, stateDir, rootID, sessionID)
	if err != nil {
		return activityLoadedBase{}, err
	}
	return activityLoadedBase{snapshot: activitySessionSnapshot{
		SessionID:       sessionID,
		Ref:             encodeRef("", sessionID),
		Label:           activityLabelFromMeta(sessionID, meta, metaErr),
		RootID:          rootID,
		Revision:        activityRevisionFromMeta(meta),
		Jobs:            jobs,
		LiveJobs:        map[string]*jobstore.JobRecord{},
		StableDelegates: stable,
		Usage:           historicalActivityUsage(stateDir, sessionID, meta),
		Diagnostics:     diagnostics,
		JobsEpoch:       jobsEpoch,
		DelegatesEpoch:  delegatesEpoch,
	}}, nil
}

// loadHistoricalStableActivity returns ownerSessionID's stable delegate
// rows from rootSessionID's shared delegate journal, via cache so the
// journal itself is folded at most once per root across the whole
// traversal (see historicalActivityCache), plus the fold-cache's current
// epoch for that journal (see activityContinuation.DelegatesEpoch).
func loadHistoricalStableActivity(cache *historicalActivityCache, stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, uint64, []string, error) {
	idx, err := cache.rootDelegates(stateDir, rootSessionID)
	if err != nil {
		return nil, 0, nil, err
	}
	ids := idx.byOwner[ownerSessionID]
	rows := make(map[string]delegateSnapshot, len(ids))
	for _, id := range ids {
		rows[id] = captureDelegateSnapshot(idx.state[id])
	}
	return rows, idx.epoch, idx.diagnostics, nil
}

// loadHistoricalStableActivityWithAttention is LoadSessionDelegateStatus'
// loader, reached from the hub's ThreadRead RPC — a separate, single-shot
// read of the same shared delegates.jsonl the recursive job-activity-tree
// walk indexes via historicalActivityCache.rootDelegates, so it shares the
// same fold cache, even though it doesn't share the traversal-local
// rootDelegateIndex (this is one read,
// not a multi-session traversal that benefits from memoizing the byOwner
// grouping too). Its own continuation contract has no epoch to check: the
// hub's ThreadRead RPC this serves has no pagination/continuation concept
// at all, so the epoch scanRootDelegateState returns is simply discarded
// here.
func loadHistoricalStableActivityWithAttention(ctx context.Context, stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, []string, error) {
	state, _, diagnostics, err := scanRootDelegateState(ctx, stateDir, rootSessionID)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(state))
	for id, aggregate := range state {
		if aggregate == nil || aggregate.Descriptor.OwnerSessionID != ownerSessionID {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make(map[string]delegateSnapshot, len(ids))
	for _, id := range ids {
		// The delegate journal scan above is itself ctx-aware, but a root
		// can have many stable delegates, and an eligible one costs a
		// transcript file read (delegateTranscriptPathFromRef +
		// readExistingDelegateAttentionFold) — checking ctx only before the
		// scan started would let this loop keep doing that work well after
		// a caller has given up.
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		aggregate := state[id]
		row := captureDelegateSnapshot(aggregate)
		row.needsAttention = false
		if delegateAttentionProjectionEligible(state, id) {
			transcriptPath, childSessionID, err := delegateTranscriptPathFromRef(stateDir, aggregate.Descriptor.TranscriptRef)
			if err != nil {
				return nil, nil, fmt.Errorf("delegate %s attention transcript: %w", id, err)
			}
			fold, err := readExistingDelegateAttentionFold(transcriptPath, childSessionID)
			if err != nil {
				return nil, nil, fmt.Errorf("delegate %s attention transcript: %w", id, err)
			}
			row.needsAttention = len(fold.pendingIDs()) != 0
		}
		rows[id] = row
	}
	return rows, diagnostics, nil
}

func activityRootIDFromMeta(sessionID string, meta schema.SessionMeta) string {
	if rootID := strings.TrimSpace(meta.JobTreeRootSessionID); rootID != "" {
		return rootID
	}
	return strings.TrimSpace(sessionID)
}

func activityRevisionFromMeta(meta schema.SessionMeta) uint64 {
	return meta.JobTreeRevision
}

func activityLabelFromMeta(sessionID string, meta schema.SessionMeta, metaErr error) string {
	if metaErr == nil {
		return activitySessionLabel(meta)
	}
	return strings.TrimSpace(sessionID)
}

func validateActivityRootRef(ref, expectedSessionID string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	projectID, sessionID, err := decodeRef(ref)
	if err != nil {
		return err
	}
	if projectID != "" {
		return fmt.Errorf("activity root ref %q crosses source boundary", ref)
	}
	if sessionID != expectedSessionID {
		return fmt.Errorf("activity root ref %q does not match session %q", ref, expectedSessionID)
	}
	return nil
}
