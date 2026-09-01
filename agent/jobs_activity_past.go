package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

// activityUsageCache memoizes per-transcript cumulative usage totals (keyed by
// file identity) so repeat activity-tree fetches don't rescan every retained
// child transcript.
var activityUsageCache = apptranscript.NewTurnCache()

// historicalJobScanLimits bounds one session's jobs.jsonl scan during
// activity-tree loading, independent of activityMaxWorkUnits (roborev
// finding on #807 — an earlier round tied MaxEvents directly to the
// traversal's remaining work-unit budget instead: remaining is a count of
// roughly one unit per RENDERED job record, but a raw scan reads every
// jobstore event kind, including non-job-record ones like watch events
// (jobstore.isJobRecordEventKind) that consume raw-event budget without
// ever counting as work, and a single job record itself can span several
// events — started, finished, message-sent, notification... So a session
// with many non-job events ahead of its real jobs could have its scan
// ceiling hit long before any of those later, legitimate jobs were ever
// read, even with plenty of the work budget those jobs would have used
// left unspent. The scan's own ceiling is now a fixed, generous safety
// valve, not a proxy for how many job records the traversal still wants.
//
// MaxBytes is generous relative to any legitimate journal — the largest
// observed in production was 6.99 MiB / 562 jobs (#448) — but finite, so a
// single pathologically large record can't force unbounded decoding.
// MaxEvents at 100,000 is 50x activityMaxWorkUnits: generous headroom for
// multi-event jobs and non-job events, while still a real safety valve
// against truly pathological input.
var historicalJobScanLimits = jobstore.ScanLimits{MaxBytes: 32 << 20, MaxEvents: 100_000}

// historicalDelegateScanLimits bounds the shared root delegates.jsonl scan.
// Unlike jobs.jsonl, this ceiling is NOT tied to activityMaxWorkUnits — a
// delegate isn't "work" in that sense, and a delegate_created event embeds
// the FULL FrozenRolePrompt/FrozenSkillBodies text (not a reference) on
// every occurrence (agent/internal/delegatestore/record.go), so its size is
// dominated by prompt/skill content rather than event count. At a
// conservative ~10 KB/event (a couple of skill bodies plus a role prompt) an
// append-only journal — this store has no compaction or rotation — could
// plausibly approach a much smaller ceiling over months of legitimate, heavy
// delegation, with no adversarial input at all (#448 ceilings review).
// 128 MiB / ~10 KB ≈ 13,000 delegate-lifetime events of headroom, which is
// intentionally generous: hitting it now degrades to a partial, diagnosed
// result (historicalActivityCache.rootDelegates) rather than the total,
// hard failure an earlier, tighter ceiling risked, so the cost of erring
// generous here is a slower scan on truly pathological input, not lost
// data.
var historicalDelegateScanLimits = delegatestore.ScanLimits{MaxBytes: 128 << 20, MaxEvents: 200_000}

// scanJobJournal and scanDelegateJournal are package vars — like
// historicalJobsStat below — so tests can count or intercept scans (proving
// the shared delegate journal is read once per root, or that cancellation
// stops before a later session's file is opened) without instrumenting the
// filesystem.
var scanJobJournal = jobstore.ScanEvents
var scanDelegateJournal = delegatestore.ScanEvents

// rootDelegateIndex is one root's delegates.jsonl decoded and folded once,
// then indexed by OwnerSessionID so every visited session in the tree looks
// up its own rows without re-reading or re-folding the shared journal.
type rootDelegateIndex struct {
	byOwner     map[string][]string // ownerSessionID -> sorted delegate IDs
	state       delegatestore.State
	diagnostics []string
	// truncated reports that the shared delegate journal scan hit its own
	// ceiling and state is a partial prefix, not the complete set of
	// delegates (roborev finding on #807: this used to surface only as a
	// diagnostic string, leaving Branch.Truncated false and
	// Counts.Complete potentially true despite delegates having been
	// silently dropped — see loadHistoricalActivityBase, which now ORs
	// this into LoadTruncated).
	truncated bool
}

// historicalActivityCache threads cancellation and shares per-traversal
// state — the root delegate index above — through the recursive historical
// loaders that both the live and persisted activity-tree entry points share.
// It is created fresh for one loadActivitySnapshotForParams call and never
// outlives it, the same traversal-local scope activityBudget already uses in
// jobs_activity.go.
type historicalActivityCache struct {
	ctx           context.Context
	delegateIndex map[string]*rootDelegateIndex // rootSessionID -> index, lazy
	// budget bounds the aggregate LOAD-phase traversal — decoded job records
	// and visited-session depth — across this whole cache's lifetime (#448
	// finding 1). It reuses activityBudget/activityConsumeWorkUnit, the same
	// primitives projectBoundedActivityTree already applies afterward, so
	// load-phase and projection-phase accounting can never drift apart: they
	// count the identical thing (owned, shell-typed job records; one
	// delegate visit) the same way, just one phase earlier. rootID/now are
	// irrelevant here (only read by the projection-side continuation/status
	// helpers this budget instance never calls).
	budget *activityBudget
}

// newHistoricalActivityCache builds a fresh, traversal-scoped cache for one
// loadActivitySnapshotForParams call. rootID only flows into
// newBoundedActivityBudget for parity with that constructor's other
// (projection-side) caller — the load-phase budget itself never reads
// activityBudget.rootID, since #448's load-phase truncation mints no
// continuation (see the LoadTruncated doc comment in jobs_activity.go).
func newHistoricalActivityCache(ctx context.Context, rootID string) *historicalActivityCache {
	return &historicalActivityCache{
		ctx:           ctx,
		delegateIndex: map[string]*rootDelegateIndex{},
		budget:        newBoundedActivityBudget(rootID, time.Time{}),
	}
}

// scanRootDelegateState reads and folds rootSessionID's shared
// delegates.jsonl under the bounded scanner, returning the folded state and
// the diagnostics the scan itself produced. Both readers of that journal —
// the memoized traversal index (rootDelegates) and the single-shot status
// read (loadHistoricalStableActivityWithAttention) — go through here so the
// scan ceiling, the degrade-to-partial rule, and the diagnostic wording stay
// defined once instead of drifting between two copies.
//
// When the ceiling fires the scan degrades to partial rather than
// hard-failing the whole read: a delegate_created event embeds the full
// frozen role prompt and skill bodies
// (agent/internal/delegatestore/record.go), so a long-lived,
// heavily-delegating root can plausibly approach the ceiling through
// entirely legitimate use, not just adversarial input (#448 ceilings
// review). ScanEvents already returns whatever it decoded before the ceiling
// fired, so that prefix is folded and reported with a scan_truncated
// diagnostic.
func scanRootDelegateState(ctx context.Context, stateDir, rootSessionID string) (delegatestore.State, []string, bool, error) {
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	events, readDiagnostics, err := scanDelegateJournal(ctx, path, historicalDelegateScanLimits)
	scanTruncated := false
	if err != nil {
		if !errors.Is(err, delegatestore.ErrScanLimitExceeded) {
			return nil, nil, false, err
		}
		scanTruncated = true
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		return nil, nil, false, err
	}
	var diagnostics []string
	if readDiagnostics.TornTail {
		diagnostics = append(diagnostics, "delegate_journal_torn_tail: ignored unterminated trailing batch")
	}
	if scanTruncated {
		diagnostics = append(diagnostics, "delegate_journal_scan_truncated: exceeds the scan limit, some delegates may be missing")
	}
	return state, diagnostics, scanTruncated, nil
}

// rootDelegates returns rootSessionID's delegate index, scanning and folding
// delegates.jsonl on the first request for that root and reusing the result
// for every later visited session sharing the same root (#448: this file was
// previously re-read and re-folded once per visited session, making loading
// O(sessions x delegate events)).
func (c *historicalActivityCache) rootDelegates(stateDir, rootSessionID string) (*rootDelegateIndex, error) {
	if idx, ok := c.delegateIndex[rootSessionID]; ok {
		return idx, nil
	}
	state, diagnostics, truncated, err := scanRootDelegateState(c.ctx, stateDir, rootSessionID)
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
	idx := &rootDelegateIndex{byOwner: byOwner, state: state, diagnostics: diagnostics, truncated: truncated}
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
	snapshot, startDepth, err := loadActivitySnapshotForParams(ctx, root, params)
	if err != nil {
		return appwire.JobActivityTree{}, err
	}
	rootRevisionID := strings.TrimSpace(snapshot.RootID)
	if rootRevisionID == "" {
		rootRevisionID = sessionID
	}
	revision := activitySnapshotPersistedRevision(snapshot, rootRevisionID)
	if strings.TrimSpace(params.Continuation) != "" {
		full, err := buildActivityFullSnapshot(root, map[string]bool{sessionID: true}, false, newHistoricalActivityCache(ctx, sessionID))
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		revision = activitySnapshotPersistedRevision(full, rootRevisionID)
	}
	return projectBoundedActivityTree(*snapshot, sessionID, startDepth, revision, time.Now().UTC())
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
	var jobEvents []jobstore.Event
	loadTruncated := false
	if _, err := historicalJobsStat(jobsPath); err != nil {
		if !os.IsNotExist(err) {
			return activityLoadedBase{}, err
		}
		if required {
			return activityLoadedBase{}, fmt.Errorf("child session %q unavailable in state directory", sessionID)
		}
	} else {
		// #448 finding 1: skip this session's scan entirely once the
		// tree-wide load budget is already fully spent by earlier sessions
		// — nothing it loads would ever be rendered anyway. This is a pure
		// "should I even visit this session" traversal-breadth check,
		// unrelated to the scan's OWN raw ceiling below (roborev finding on
		// #807: remaining, a work-unit count, must not be used as the raw
		// scanner's MaxEvents — see historicalJobScanLimits' doc comment).
		if activityBudgetRemaining(cache.budget) == 0 {
			loadTruncated = true
		} else {
			events, err := scanJobJournal(cache.ctx, jobsPath, historicalJobScanLimits)
			if err != nil {
				if !errors.Is(err, jobstore.ErrScanLimitExceeded) {
					return activityLoadedBase{}, err
				}
				// Degrade to partial rather than hard-fail: events already
				// holds everything decoded before the ceiling fired (see
				// jobstore.ScanEvents' partial-result contract), so a
				// malformed or merely enormous tail past
				// historicalJobScanLimits' own (work-budget-independent)
				// ceiling doesn't sink the whole tree — it marks this
				// branch Truncated instead (see LoadTruncated below /
				// projectActivitySessionAt).
				loadTruncated = true
			}
			jobEvents = events
		}
	}
	jobs := jobstore.FoldOrdered(jobEvents)
	// Consume from the SAME shared budget the scan above was sized against,
	// using the identical accounting projection's own budget will apply to
	// this same data (activityOwnedShellCount mirrors activityOwnedRecords +
	// the JobShell case in projectActivitySessionAt) — so aggregate work
	// across every session this traversal visits is bounded, not just this
	// one file's own scan.
	if count := activityOwnedShellCount(sessionID, jobs); !activityConsumeWorkUnit(cache.budget, count) && cache.budget != nil && cache.budget.bounded {
		// The session's own records exceed what's left: refuse-without-consuming
		// (the contract projection relies on) would leave the budget unspent and
		// every later sibling's journal would still be opened. Saturate instead,
		// so activityBudgetRemaining reports 0 for the rest of the traversal.
		cache.budget.usedWork = cache.budget.maxWorkUnits
	}
	stable, diagnostics, delegatesTruncated, err := loadHistoricalStableActivity(cache, stateDir, rootID, sessionID)
	if err != nil {
		return activityLoadedBase{}, err
	}
	// roborev finding on #807: a truncated delegate journal used to surface
	// only as a diagnostic string, leaving Branch.Truncated false (a client
	// could see Counts.Complete=true despite delegates having been
	// silently dropped). Folding it into the same LoadTruncated flag the
	// jobs scan uses gets it the identical wire treatment (Truncated=true,
	// no continuation — see markActivitySessionTruncated) without a
	// parallel mechanism.
	loadTruncated = loadTruncated || delegatesTruncated
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
		LoadTruncated:   loadTruncated,
	}}, nil
}

// loadHistoricalStableActivity returns ownerSessionID's stable delegate rows
// from rootSessionID's shared delegate journal, via cache so the journal
// itself is scanned and folded at most once per root across the whole
// traversal (see historicalActivityCache).
func loadHistoricalStableActivity(cache *historicalActivityCache, stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, []string, bool, error) {
	idx, err := cache.rootDelegates(stateDir, rootSessionID)
	if err != nil {
		return nil, nil, false, err
	}
	ids := idx.byOwner[ownerSessionID]
	rows := make(map[string]delegateSnapshot, len(ids))
	for _, id := range ids {
		rows[id] = captureDelegateSnapshot(idx.state[id])
	}
	return rows, idx.diagnostics, idx.truncated, nil
}

// loadHistoricalStableActivityWithAttention is LoadSessionDelegateStatus'
// loader, reached from the hub's ThreadRead RPC — a separate, single-shot
// read of the same shared delegates.jsonl the recursive job-activity-tree
// walk indexes via historicalActivityCache.rootDelegates, so it gets the
// same bounded scanner and degrade-to-partial treatment (#448 regression
// review finding 2), even though it doesn't share that cache (this is one
// read, not a multi-session traversal that benefits from memoizing it).
func loadHistoricalStableActivityWithAttention(ctx context.Context, stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, []string, error) {
	state, diagnostics, _, err := scanRootDelegateState(ctx, stateDir, rootSessionID)
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
		// The delegate journal scan above is itself ctx-aware (#448), but a
		// root can have many stable delegates, and an eligible one costs a
		// transcript file read (delegateTranscriptPathFromRef +
		// readExistingDelegateAttentionFold) — checking ctx only before the
		// scan started would let this loop keep doing that work well after
		// a caller has given up (roborev finding on #448).
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
