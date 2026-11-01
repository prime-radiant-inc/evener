package agent

import (
	"fmt"
	"path/filepath"
	"sort"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// legacyDelegateStateWarning reports retired delegate state in the root
// session's job journal. The state is left in place and ignored: stable
// delegates have their own store, and the retired rows have no live control
// path, so the session starts and the warning tells the user the old delegate
// work is not resumed. The second result is false when there is nothing to
// report or the journal could not be scanned (the job manager reports an
// unreadable journal itself).
func legacyDelegateStateWarning(stateDir, rootSessionID string) (events.WarningData, bool) {
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "jobs.jsonl")
	jobEvents, err := jobstore.ReadEvents(path)
	if err != nil {
		return events.WarningData{}, false
	}
	legacyJobIDs := legacyDelegateJobIDs(jobEvents)
	warning := events.WarningData{
		Source: "delegates",
		Code:   events.WarningCodeLegacyDelegateState,
		Title:  "retired delegate state ignored",
	}
	if watchID, ok := firstLegacyDelegateWatch(jobEvents, legacyJobIDs); ok {
		warning.Message = fmt.Sprintf("watch %q is addressed through a retired delegate job and is ignored; the journal row is kept", watchID)
		return warning, true
	}
	if len(legacyJobIDs) != 0 || containsLegacyDelegateLifecycle(jobEvents) {
		warning.Message = "this session's journal holds retired delegate lifecycle state; it is ignored and kept"
		return warning, true
	}
	return events.WarningData{}, false
}

func legacyDelegateJobIDs(events []jobstore.Event) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, event := range events {
		if event.Kind == jobstore.EventJobStarted && string(event.Type) == "delegate" && event.JobID != "" {
			ids[event.JobID] = struct{}{}
		}
	}
	return ids
}

func containsLegacyDelegateLifecycle(events []jobstore.Event) bool {
	for _, event := range events {
		switch string(event.Kind) {
		case "delegate_created", "delegate_stop_gate_closed", "delegate_disposed":
			return true
		}
		if event.Kind == jobstore.EventJobStarted && string(event.Type) == "delegate" {
			return true
		}
	}
	return false
}

func firstLegacyDelegateWatch(events []jobstore.Event, delegateJobIDs map[string]struct{}) (string, bool) {
	watchIDs := make([]string, 0)
	for _, event := range events {
		legacy := false
		if event.Watch != nil {
			_, targetLegacy := delegateJobIDs[event.Watch.Target]
			_, receiverLegacy := delegateJobIDs[event.Watch.SendTo]
			legacy = targetLegacy || receiverLegacy
		}
		if event.WatchSend != nil {
			_, targetLegacy := delegateJobIDs[event.WatchSend.Key.WatchTarget]
			_, resolvedTargetLegacy := delegateJobIDs[event.WatchSend.Key.ResolvedWatchedIdentity]
			_, resolvedReceiverLegacy := delegateJobIDs[event.WatchSend.Key.ResolvedSendTo]
			legacy = legacy || targetLegacy || resolvedTargetLegacy || resolvedReceiverLegacy
		}
		if legacy {
			watchID := event.WatchID
			if watchID == "" && event.WatchSend != nil {
				watchID = event.WatchSend.Key.WatchID
			}
			watchIDs = append(watchIDs, watchID)
		}
	}
	if len(watchIDs) == 0 {
		return "", false
	}
	sort.Strings(watchIDs)
	return watchIDs[0], true
}

// retiredDelegateJobIDs returns the ids of retired delegate job records in the
// job journal. Their rows stay in the journal and are never restored as live
// work.
func (jm *jobManager) retiredDelegateJobIDs() (map[string]struct{}, error) {
	records, err := jm.store.Load()
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{})
	for id, record := range records {
		if record != nil && string(record.Type) == delegateResourceType {
			ids[id] = struct{}{}
		}
	}
	return ids, nil
}

// addressedThroughRetiredDelegate reports whether a watch-send frame names a
// retired delegate job as its target, watched identity or receiver.
func addressedThroughRetiredDelegate(key jobstore.WatchSendKey, retired map[string]struct{}) bool {
	for _, id := range []string{key.WatchTarget, key.ResolvedWatchedIdentity, key.ResolvedSendTo} {
		if _, ok := retired[id]; ok {
			return true
		}
	}
	return false
}
