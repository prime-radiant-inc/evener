package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/apptranscript"
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
	return projectBoundedActivityTree(*snapshot, sessionID, startDepth, revision)
}

func loadHistoricalActivityBase(stateDir, sessionID string, required bool) (activityLoadedBase, error) {
	meta, metaErr := schema.LoadSessionMeta(stateDir, sessionID)
	jobsPath := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := historicalJobsStat(jobsPath); err != nil {
		if os.IsNotExist(err) {
			if required {
				return activityLoadedBase{}, fmt.Errorf("child session %q unavailable in state directory", sessionID)
			}
			return activityLoadedBase{snapshot: activitySessionSnapshot{
				SessionID: sessionID,
				Ref:       encodeRef("", sessionID),
				Label:     activityLabelFromMeta(sessionID, meta, metaErr),
				RootID:    activityRootIDFromMeta(sessionID, meta),
				Revision:  activityRevisionFromMeta(meta),
				Jobs:      []*jobstore.JobRecord{},
				LiveJobs:  map[string]*jobstore.JobRecord{},
				Delegates: map[string]*jobstore.DelegateRecord{},
				Usage:     historicalActivityUsage(stateDir, sessionID, meta),
			}}, nil
		}
		return activityLoadedBase{}, err
	}
	store, err := historicalJobsOpen(jobsPath)
	if err != nil {
		return activityLoadedBase{}, err
	}
	defer func() { _ = store.Close() }()
	ordered, err := store.LoadOrdered()
	if err != nil {
		return activityLoadedBase{}, err
	}
	delegates, err := loadHistoricalActivityDelegates(jobsPath)
	if err != nil {
		return activityLoadedBase{}, err
	}
	return activityLoadedBase{snapshot: activitySessionSnapshot{
		SessionID: sessionID,
		Ref:       encodeRef("", sessionID),
		Label:     activityLabelFromMeta(sessionID, meta, metaErr),
		RootID:    activityRootIDFromMeta(sessionID, meta),
		Revision:  activityRevisionFromMeta(meta),
		Jobs:      ordered,
		LiveJobs:  map[string]*jobstore.JobRecord{},
		Delegates: delegates,
		Usage:     historicalActivityUsage(stateDir, sessionID, meta),
	}}, nil
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

func loadHistoricalActivityDelegates(path string) (map[string]*jobstore.DelegateRecord, error) {
	store, err := historicalJobsOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	loader, ok := store.(interface {
		LoadDelegates() (map[string]*jobstore.DelegateRecord, error)
	})
	if !ok {
		eventsLoader, ok := store.(interface {
			LoadEvents() ([]jobstore.Event, error)
		})
		if !ok {
			return nil, fmt.Errorf("jobstore at %s cannot load delegates", path)
		}
		events, err := eventsLoader.LoadEvents()
		if err != nil {
			return nil, err
		}
		return jobstore.FoldDelegates(events), nil
	}
	return loader.LoadDelegates()
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
