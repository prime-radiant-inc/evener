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

// historicalJobScanLimits.MaxBytes bounds one session's jobs.jsonl scan
// during activity-tree loading. It is generous relative to any legitimate
// journal — the largest observed in production was 6.99 MiB / 562 jobs
// (#448) — but finite, so a single pathologically large record (as opposed
// to too many records — see MaxEvents below) can't force unbounded
// decoding. jobstore.Event carries only modest string fields (command,
// description, task, paths), so this is not expected to be reachable by
// legitimate use at all.
//
// MaxEvents is left at 0 (unlimited) here deliberately: loadHistoricalActivityBase
// computes it per call from the traversal's remaining activityMaxWorkUnits
// budget instead of a fixed constant (#448 finding 1) — that ceiling is
// already far smaller than any fixed safety valve would be, so a second,
// independent event ceiling here would only ever be redundant.
var historicalJobScanLimits = jobstore.ScanLimits{MaxBytes: 32 << 20}

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
// loadActivitySnapshotForParams call. rootID is the traversal's actual query
// root (the session the client asked for) — NOT re-derived per visited
// session from each one's own, possibly-empty JobTreeRootSessionID meta
// field — because it has to match what LoadSessionJobActivityTree passes
// projectBoundedActivityTree as decodeActivityContinuation's expected root:
// a continuation minted here (activityBudget.rootID, reused for
// LoadTruncatedContinuation) must validate against the exact same root a
// later request re-submitting it will check against.
func newHistoricalActivityCache(ctx context.Context, rootID string) *historicalActivityCache {
	return &historicalActivityCache{
		ctx:           ctx,
		delegateIndex: map[string]*rootDelegateIndex{},
		budget:        newBoundedActivityBudget(rootID, time.Time{}),
	}
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
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	events, readDiagnostics, err := scanDelegateJournal(c.ctx, path, historicalDelegateScanLimits)
	scanTruncated := false
	if err != nil {
		if !errors.Is(err, delegatestore.ErrScanLimitExceeded) {
			return nil, err
		}
		// Degrade to partial rather than hard-failing the WHOLE tree for
		// this root: a delegate_created event embeds the full frozen role
		// prompt and skill bodies (agent/internal/delegatestore/record.go),
		// so a long-lived, heavily-delegating root can plausibly approach
		// the ceiling through entirely legitimate use, not just adversarial
		// input (#448 ceilings review). events already holds whatever
		// ScanEvents decoded before the ceiling fired.
		scanTruncated = true
	}
	state, err := delegatestore.Fold(events)
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
	var diagnostics []string
	if readDiagnostics.TornTail {
		diagnostics = append(diagnostics, "delegate_journal_torn_tail: ignored unterminated trailing batch")
	}
	if scanTruncated {
		diagnostics = append(diagnostics, "delegate_journal_scan_truncated: exceeds the scan limit, some delegates may be missing")
	}
	idx := &rootDelegateIndex{byOwner: byOwner, state: state, diagnostics: diagnostics}
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
		full, err := buildActivityFullSnapshot(root, map[string]bool{sessionID: true}, false, newHistoricalActivityCache(ctx, sessionID), nil)
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
		// #448 finding 1: cap this session's OWN scan at what's left of the
		// tree-wide work-unit budget (not the fixed, much larger
		// historicalJobScanLimits.MaxEvents safety valve) — the same
		// ceiling activityMaxWorkUnits already applies to the response, just
		// enforced during decode instead of only after. A session that
		// already has more raw events than the whole remaining budget can
		// realistically fit is a session decoding will never fully use
		// anyway. Byte ceiling stays the fixed, work-unit-independent safety
		// valve: it bounds a different pathology (one huge record), not job
		// count.
		remaining := activityBudgetRemaining(cache.budget)
		if remaining == 0 {
			loadTruncated = true
		} else {
			limits := historicalJobScanLimits
			if remaining > 0 {
				limits.MaxEvents = remaining
			}
			events, err := scanJobJournal(cache.ctx, jobsPath, limits)
			if err != nil {
				if !errors.Is(err, jobstore.ErrScanLimitExceeded) {
					return activityLoadedBase{}, err
				}
				// Degrade to partial rather than hard-fail: events already
				// holds everything decoded before the ceiling fired (see
				// jobstore.ScanEvents' partial-result contract), so a
				// malformed or merely enormous tail past the point this
				// session's budget share would ever render doesn't sink the
				// whole tree — it marks this branch Truncated instead (see
				// LoadTruncated below / projectActivitySessionAt).
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
	activityConsumeWorkUnit(cache.budget, activityOwnedShellCount(sessionID, jobs))
	stable, diagnostics, err := loadHistoricalStableActivity(cache, stateDir, rootID, sessionID)
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
		LoadTruncated:   loadTruncated,
	}}, nil
}

// loadHistoricalStableActivity returns ownerSessionID's stable delegate rows
// from rootSessionID's shared delegate journal, via cache so the journal
// itself is scanned and folded at most once per root across the whole
// traversal (see historicalActivityCache).
func loadHistoricalStableActivity(cache *historicalActivityCache, stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, []string, error) {
	idx, err := cache.rootDelegates(stateDir, rootSessionID)
	if err != nil {
		return nil, nil, err
	}
	ids := idx.byOwner[ownerSessionID]
	rows := make(map[string]delegateSnapshot, len(ids))
	for _, id := range ids {
		rows[id] = captureDelegateSnapshot(idx.state[id])
	}
	return rows, idx.diagnostics, nil
}

// loadHistoricalStableActivityWithAttention is LoadSessionDelegateStatus'
// loader, reached from the hub's ThreadRead RPC — a separate, single-shot
// read of the same shared delegates.jsonl the recursive job-activity-tree
// walk indexes via historicalActivityCache.rootDelegates, so it gets the
// same bounded scanner and degrade-to-partial treatment (#448 regression
// review finding 2), even though it doesn't share that cache (this is one
// read, not a multi-session traversal that benefits from memoizing it).
func loadHistoricalStableActivityWithAttention(ctx context.Context, stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, []string, error) {
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	events, readDiagnostics, err := scanDelegateJournal(ctx, path, historicalDelegateScanLimits)
	scanTruncated := false
	if err != nil {
		if !errors.Is(err, delegatestore.ErrScanLimitExceeded) {
			return nil, nil, err
		}
		scanTruncated = true
	}
	state, err := delegatestore.Fold(events)
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
	var diagnostics []string
	if readDiagnostics.TornTail {
		diagnostics = append(diagnostics, "delegate_journal_torn_tail: ignored unterminated trailing batch")
	}
	if scanTruncated {
		diagnostics = append(diagnostics, "delegate_journal_scan_truncated: exceeds the scan limit, some delegates may be missing")
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
