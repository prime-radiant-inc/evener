package agent

import (
	"fmt"
	"os"
	"path/filepath"
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

// historicalActivityUsage sums a retained session's own token usage from its
// transcript. nil (not zero) when the transcript carries no usage, so the wire
// omits the field and the UI hides the token cluster rather than rendering
// ↑0 ↓0.
func historicalActivityUsage(stateDir, sessionID string, meta schema.SessionMeta) *appwire.SerfUsage {
	path := filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
	total, err := activityUsageCache.UsageTotalFromFile(path, transcriptJSONLMaxLineBytes, meta.DivergenceTurn)
	if err != nil {
		return nil
	}
	return total
}

// LoadSessionJobActivityTree loads and projects a session's persisted job activity tree.
func LoadSessionJobActivityTree(stateDir, sessionID string, params appwire.JobsListParams) (appwire.JobActivityTree, error) {
	if err := validateActivityRootRef(params.Ref, sessionID); err != nil {
		return appwire.JobActivityTree{}, err
	}
	root := activitySessionLocator{stateDir: stateDir, sessionID: sessionID}
	snapshot, startDepth, err := loadActivitySnapshotForParams(root, params)
	if err != nil {
		return appwire.JobActivityTree{}, err
	}
	rootRevisionID := strings.TrimSpace(snapshot.RootID)
	if rootRevisionID == "" {
		rootRevisionID = sessionID
	}
	revision := activitySnapshotPersistedRevision(snapshot, rootRevisionID)
	if strings.TrimSpace(params.Continuation) != "" {
		full, err := buildActivityFullSnapshot(root, map[string]bool{sessionID: true}, false)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		revision = activitySnapshotPersistedRevision(full, rootRevisionID)
	}
	return projectBoundedActivityTree(*snapshot, sessionID, startDepth, revision, time.Now().UTC())
}

func loadHistoricalActivityBase(stateDir, sessionID string, required bool) (activityLoadedBase, error) {
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
		jobEvents, err = jobstore.ReadEvents(jobsPath)
		if err != nil {
			return activityLoadedBase{}, err
		}
	}
	stable, diagnostics, err := loadHistoricalStableActivity(stateDir, rootID, sessionID)
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

func loadHistoricalStableActivity(stateDir, rootSessionID, ownerSessionID string) (map[string]delegateSnapshot, []string, error) {
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	events, readDiagnostics, err := delegatestore.ReadEventsWithDiagnostics(path)
	if err != nil {
		return nil, nil, err
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		return nil, nil, err
	}
	rows := make(map[string]delegateSnapshot)
	for id, aggregate := range state {
		if aggregate == nil || aggregate.Descriptor.OwnerSessionID != ownerSessionID {
			continue
		}
		rows[id] = captureDelegateSnapshot(aggregate)
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
