package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/foldcache"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
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
// claim: neither a session's folded job list and its job-ID index nor a
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

// historicalJobFold is jobstore's fold-cache payload: the journal folded
// into the ordered list a page projects. Each appended delta extends it in
// O(delta) instead of refolding the whole history, and a page reads the list
// without copying it, so serving one costs the page, not the history behind
// it.
type historicalJobFold struct {
	jobs *activityJobList
	// positions indexes jobs by job ID for the next extension. Readers never
	// see it; see activityJobPositions for why an extension can trust it.
	positions *activityJobPositions
	// lastSeq is the highest seq folded. jobstore.FoldOrdered applies events
	// in seq order, so a delta extends the fold only when none of its events
	// is older than lastSeq.
	lastSeq int64
}

// activityJobPositions maps each job ID in list to its index. Extending a
// fold updates the map in place for the extended list, so it describes only
// the list it names: extending a fold whose list is not that one rebuilds
// the map from the fold's own list first.
type activityJobPositions struct {
	of   map[string]int
	list *activityJobList
}

var historicalJobFoldCache = foldcache.New[historicalJobFold](historicalJobFoldCacheEntries)

// extendHistoricalJobFold is historicalJobFoldCache's foldcache.Extend: it
// reads only the events appended since fromOffset and folds them onto prior
// without changing prior, which foldcache.Cache may still be handing to
// other callers. A delta holding an event older than one already folded
// belongs earlier in a full fold, so that delta refolds the journal from
// byte zero instead.
func extendHistoricalJobFold(ctx context.Context, path string, fromOffset int64, prior historicalJobFold) (historicalJobFold, int64, error) {
	delta, toOffset, err := scanJobJournal(ctx, path, fromOffset, jobstore.ScanLimits{})
	if err != nil {
		return historicalJobFold{}, 0, err
	}
	sortJobEventsBySeq(delta)
	if len(delta) > 0 && delta[0].Seq < prior.lastSeq {
		all, toOffset, err := scanJobJournal(ctx, path, 0, jobstore.ScanLimits{})
		if err != nil {
			return historicalJobFold{}, 0, err
		}
		sortJobEventsBySeq(all)
		return historicalJobFold{}.extend(all), toOffset, nil
	}
	return prior.extend(delta), toOffset, nil
}

func sortJobEventsBySeq(events []jobstore.Event) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
}

// extend folds events, in seq order and none older than f.lastSeq, into a
// new fold that shares every chunk of f.jobs the events leave alone. A
// record already in f.jobs is cloned before its first change, since f may
// still be serving it. The list matches jobstore.FoldOrdered over the whole
// history, less records without a job ID, which no page shows.
func (f historicalJobFold) extend(events []jobstore.Event) historicalJobFold {
	positions := f.positions
	if positions == nil || positions.list != f.jobs {
		positions = &activityJobPositions{of: make(map[string]int, f.jobs.len())}
		for rec := range f.jobs.all() {
			positions.of[rec.JobID] = len(positions.of)
		}
	}
	b := newActivityJobListBuilder(f.jobs, positions.of)
	created := make(map[string]bool)
	lastSeq := f.lastSeq
	for _, event := range events {
		lastSeq = max(lastSeq, event.Seq)
		if event.JobID == "" || !jobstore.IsJobRecordEvent(event.Kind) {
			continue
		}
		at, known := positions.of[event.JobID]
		if !known {
			b.push(jobstore.ApplyJob(nil, event))
			created[event.JobID] = true
			continue
		}
		b.replace(at, func(rec *jobstore.JobRecord) *jobstore.JobRecord {
			if !created[event.JobID] {
				rec = rec.Clone()
				created[event.JobID] = true
			}
			return jobstore.ApplyJob(rec, event)
		})
	}
	positions.list = b.list()
	return historicalJobFold{jobs: positions.list, positions: positions, lastSeq: lastSeq}
}

// loadCachedJobRecords returns jobsPath's complete, folded history via
// historicalJobFoldCache, replaying only what was appended since the last
// call for this path in this process. The returned epoch is
// historicalJobFoldCache's generation counter for jobsPath at the moment of
// this read — see foldcache.Result.Epoch and activityContinuation.JobsEpoch.
func loadCachedJobRecords(ctx context.Context, jobsPath string) (*activityJobList, uint64, bool, error) {
	result, err := historicalJobFoldCache.Get(ctx, jobsPath, extendHistoricalJobFold)
	if err != nil {
		return nil, 0, false, err
	}
	return result.Value.jobs, result.Epoch, result.Absent, nil
}

// historicalDelegateFold is delegatestore's fold-cache payload. Unlike
// historicalJobFold, this retains the FOLDED delegatestore.State directly
// (not raw events), extended incrementally via delegatestore.Apply for each
// new event, so a request folds only the events appended since the cached
// state instead of re-folding a growing root's full event history from
// scratch — the same treatment jobstore gets above.
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
// mutating it in place would race that caller. Apply clones each *Aggregate
// an event touches before writing to it and leaves the rest shared, so a
// SHALLOW maps.Clone here (copying the map's pointers, not each Aggregate)
// is sufficient: the clone becomes Apply's write target, the original
// prior.state and its aggregates are never touched.
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
// cache, then indexed by creator session so every visited session in the
// tree looks up its own rows without re-folding the shared journal itself
// (historicalDelegateFoldCache already avoids the underlying re-READ; this
// index avoids repeating the small O(delegates) grouping pass per session
// within one traversal).
type rootDelegateIndex struct {
	byCreator   map[string][]string // creator session ID -> sorted delegate IDs
	state       delegatestore.State
	journal     activityJournalState
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
	// rootID is the delegate-journal root this traversal learned from the
	// first session whose meta loaded, so a required child whose own meta is
	// missing can still find the rows the shared journal attributes to it.
	// Without it the meta fallback below would name the child's own session
	// ID and scan a per-child journal that does not exist.
	rootID string
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
// activityJournalState is what a session knows about one journal besides its
// contents: the generation it folded at, and the two conditions a generation
// cannot express — the journal was not there, or it was there and could not
// be read. Both report generation 0, which is also what a journal folded once
// and never rewritten reports, so a continuation carries these alongside the
// generation rather than trusting the number alone.
type activityJournalState struct {
	epoch      uint64
	absent     bool
	unreadable bool
}

func scanRootDelegateState(ctx context.Context, stateDir, rootSessionID string) (delegatestore.State, activityJournalState, []string, error) {
	path := schema.SessionDelegateJournalPath(stateDir, rootSessionID)
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
			// The generation reported here is not one this read earned:
			// nothing was folded. Recording the condition instead lets a
			// resume tell it apart from a journal folded once and never
			// rewritten, which reports the same 0.
			return delegatestore.State{}, activityJournalState{unreadable: true}, []string{fmt.Sprintf("delegate_journal_line_too_long: %v", err)}, nil
		}
		return nil, activityJournalState{}, nil, err
	}
	var diagnostics []string
	if result.Value.tornTail {
		diagnostics = append(diagnostics, "delegate_journal_torn_tail: ignored unterminated trailing batch")
	}
	return result.Value.state, activityJournalState{epoch: result.Epoch, absent: result.Absent}, diagnostics, nil
}

// rootDelegates returns rootSessionID's delegate index, folding
// delegates.jsonl on the first request for that root THIS TRAVERSAL and
// reusing the result for every later visited session sharing the same
// root, so a traversal visiting many sessions under one root avoids
// repeating the cheap byCreator grouping pass for each of them. The
// underlying read itself is separately deduplicated across traversals by
// historicalDelegateFoldCache.
func (c *historicalActivityCache) rootDelegates(stateDir, rootSessionID string) (*rootDelegateIndex, error) {
	if idx, ok := c.delegateIndex[rootSessionID]; ok {
		return idx, nil
	}
	state, journal, diagnostics, err := scanRootDelegateState(c.ctx, stateDir, rootSessionID)
	if err != nil {
		return nil, err
	}
	byCreator := make(map[string][]string)
	for id, aggregate := range state {
		if aggregate == nil {
			continue
		}
		creator := delegateCreatorSessionID(aggregate.Descriptor.VisibleSessionID, aggregate.Descriptor.OwnerSessionID)
		byCreator[creator] = append(byCreator[creator], id)
	}
	for creator := range byCreator {
		sort.Strings(byCreator[creator])
	}
	idx := &rootDelegateIndex{byCreator: byCreator, state: state, journal: journal, diagnostics: diagnostics}
	c.delegateIndex[rootSessionID] = idx
	return idx, nil
}

// historicalActivityUsage sums a retained session's own token usage from its
// transcript. nil (not zero) when the transcript carries no usage, so the wire
// omits the field and the UI hides the token cluster rather than rendering
// ↑0 ↓0.
func historicalActivityUsage(stateDir, sessionID string, meta schema.SessionMeta) *appwire.EvenerUsage {
	path := schema.SessionTranscriptPath(stateDir, sessionID)
	total, err := activityUsageCache.UsageTotalFromFile(path, transcript.DefaultMaxLineBytes, meta.DivergenceTurn)
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
	snapshot, startDepth, resumeIndex, err := loadActivitySnapshotForParams(ctx, root, params)
	if err != nil {
		return appwire.JobActivityTree{}, err
	}
	rootRevisionID := strings.TrimSpace(snapshot.RootID)
	if rootRevisionID == "" {
		rootRevisionID = sessionID
	}
	// A continuation page reports the revision its token was minted against,
	// NOT one recomputed here. A historical revision is a max over a bounded,
	// work-budget-shaped snapshot (activitySnapshotPersistedRevision), so
	// recomputing it on resume can yield a different number for the same, valid
	// continuation: resolving the token's path hops spends budget before the
	// walk, so it can visit a smaller set of sessions than the root request
	// did, and a descendant that appended between the two requests moves the
	// max the other way. A consumer that fences a page by revision (the
	// Activity Panel) would then discard a valid page and refetch the root
	// forever, so pagination would never advance on exactly the large-history
	// trees this bound exists for. Echoing the token's revision keeps the
	// number stable across the whole walk; the page's entries are still fenced
	// by the per-journal epochs checked on resume, which is what actually says
	// whether a position is safe to resume from.
	revision := activitySnapshotPersistedRevision(snapshot, rootRevisionID)
	if token := strings.TrimSpace(params.Continuation); token != "" {
		cont, err := decodeActivityContinuation(token, sessionID)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		revision = cont.Revision
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
	if metaErr != nil {
		if cache.rootID != "" {
			rootID = cache.rootID
		}
	} else if cache.rootID == "" {
		cache.rootID = rootID
	}
	jobsPath := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
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
	// truncation with nothing to resume into. The fold reports whether the
	// journal was there at all, from the same stat it reads through, so
	// nothing here can see the file appear or vanish between a presence
	// check and the read.
	jobs, jobsEpoch, jobsAbsent, jobErr := loadCachedJobRecords(cache.ctx, jobsPath)
	if jobErr != nil && !required {
		return activityLoadedBase{}, jobErr
	}
	if cache.ctx.Err() != nil {
		return activityLoadedBase{}, cache.ctx.Err()
	}
	stable, delegates, diagnostics, err := loadHistoricalStableActivity(cache, stateDir, rootID, sessionID)
	if err != nil {
		return activityLoadedBase{}, err
	}
	degradedJobHistory := false
	if required && (jobErr != nil || jobsAbsent) {
		if len(stable) == 0 {
			if jobErr != nil {
				return activityLoadedBase{}, jobErr
			}
			return activityLoadedBase{}, fmt.Errorf("child session %q unavailable in state directory", sessionID)
		}
		// #5643: this session's own jobs journal is missing or unreadable, but
		// the shared root journal still attributes delegate rows to it. Degrade
		// to those rows instead of dropping the branch: #5012 keeps nested
		// delegates out of the root roster, so this branch is the only place
		// they can be loaded. The diagnostic keeps the failure visible on the
		// degraded session rather than silently presenting it as healthy.
		reason := "unavailable in state directory"
		if jobErr != nil {
			reason = fmt.Sprintf("unreadable: %v", jobErr)
		}
		diagnostics = append(diagnostics, fmt.Sprintf("child session %q job journal %s; showing delegates from the root journal", sessionID, reason))
		degradedJobHistory = true
	}
	return activityLoadedBase{snapshot: activitySessionSnapshot{
		SessionID:          sessionID,
		Ref:                encodeRef("", sessionID),
		Label:              activityLabelFromMeta(sessionID, meta, metaErr),
		RootID:             rootID,
		Revision:           activityRevisionFromMeta(meta),
		Jobs:               jobs,
		Delegates:          stable,
		Usage:              historicalActivityUsage(stateDir, sessionID, meta),
		Diagnostics:        diagnostics,
		DegradedJobHistory: degradedJobHistory,
		JobsEpoch:          jobsEpoch,
		DelegatesEpoch:     delegates.epoch,

		JobsJournalAbsent:          jobsAbsent,
		JobsJournalUnreadable:      jobErr != nil,
		DelegatesJournalAbsent:     delegates.absent,
		DelegatesJournalUnreadable: delegates.unreadable,
	}}, nil
}

// loadHistoricalStableActivity returns creatorSessionID's stable delegate
// rows from rootSessionID's shared delegate journal, via cache so the
// journal itself is folded at most once per root across the whole
// traversal (see historicalActivityCache), plus the fold-cache's current
// epoch for that journal (see activityContinuation.DelegatesEpoch).
func loadHistoricalStableActivity(cache *historicalActivityCache, stateDir, rootSessionID, creatorSessionID string) (map[string]delegateSnapshot, activityJournalState, []string, error) {
	idx, err := cache.rootDelegates(stateDir, rootSessionID)
	if err != nil {
		return nil, activityJournalState{}, nil, err
	}
	ids := idx.byCreator[creatorSessionID]
	rows := make(map[string]delegateSnapshot, len(ids))
	for _, id := range ids {
		rows[id] = captureDelegateSnapshot(idx.state[id])
	}
	return rows, idx.journal, idx.diagnostics, nil
}

// loadHistoricalStableActivityWithAttention is LoadSessionDelegateStatus'
// loader, reached from the hub's ThreadRead RPC — a separate, single-shot
// read of the same shared delegates.jsonl the recursive job-activity-tree
// walk indexes via historicalActivityCache.rootDelegates, so it shares the
// same fold cache, even though it doesn't share the traversal-local
// rootDelegateIndex (this is one read,
// not a multi-session traversal that benefits from memoizing the byCreator
// grouping too). Its own continuation contract has no epoch to check: the
// hub's ThreadRead RPC this serves has no pagination/continuation concept
// at all, so the epoch scanRootDelegateState returns is simply discarded
// here.
//
// It lists sessionID's subtree (sessionSubtreeDelegateIDs), as the live
// DetailedStatus does.
func loadHistoricalStableActivityWithAttention(ctx context.Context, stateDir, rootSessionID, sessionID string) (map[string]delegateSnapshot, []string, error) {
	state, _, diagnostics, err := scanRootDelegateState(ctx, stateDir, rootSessionID)
	if err != nil {
		return nil, nil, err
	}
	ids := sessionSubtreeDelegateIDs(state, sessionID)
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
