package agent

import (
	"context"
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
// activity-tree loading. It is generous enough for any legitimate journal —
// the largest observed in production was 6.99 MiB / 562 jobs (#448) — but
// finite, so a corrupt or adversarial journal cannot force unbounded
// decoding before the response projection ever gets a chance to apply its
// own budget (activityMaxWorkUnits in jobs_activity.go).
var historicalJobScanLimits = jobstore.ScanLimits{MaxBytes: 32 << 20, MaxEvents: 100_000}

// historicalDelegateScanLimits is the same safety bound for the shared root
// delegates.jsonl.
var historicalDelegateScanLimits = delegatestore.ScanLimits{MaxBytes: 32 << 20, MaxEvents: 100_000}

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
}

func newHistoricalActivityCache(ctx context.Context) *historicalActivityCache {
	return &historicalActivityCache{ctx: ctx, delegateIndex: map[string]*rootDelegateIndex{}}
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
	if err != nil {
		return nil, err
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
		full, err := buildActivityFullSnapshot(root, map[string]bool{sessionID: true}, false, newHistoricalActivityCache(ctx))
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
	if _, err := historicalJobsStat(jobsPath); err != nil {
		if !os.IsNotExist(err) {
			return activityLoadedBase{}, err
		}
		if required {
			return activityLoadedBase{}, fmt.Errorf("child session %q unavailable in state directory", sessionID)
		}
	} else {
		var err error
		jobEvents, err = scanJobJournal(cache.ctx, jobsPath, historicalJobScanLimits)
		if err != nil {
			return activityLoadedBase{}, err
		}
	}
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
		Jobs:            jobstore.FoldOrdered(jobEvents),
		LiveJobs:        map[string]*jobstore.JobRecord{},
		StableDelegates: stable,
		Usage:           historicalActivityUsage(stateDir, sessionID, meta),
		Diagnostics:     diagnostics,
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

func loadHistoricalStableActivityWithAttention(stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, []string, error) {
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	events, readDiagnostics, err := delegatestore.ReadEventsWithDiagnostics(path)
	if err != nil {
		return nil, nil, err
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
