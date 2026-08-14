package agent

import (
	"fmt"
	"path/filepath"
	"sort"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func rejectLegacyDelegateState(stateDir, rootSessionID string) error {
	path := filepath.Join(jobsDir(stateDir, rootSessionID), "jobs.jsonl")
	events, err := jobstore.ReadEvents(path)
	if err != nil {
		return fmt.Errorf("scan legacy delegate state: %w", err)
	}
	legacyJobIDs := legacyDelegateJobIDs(events)
	if watchID, ok := firstLegacyDelegateWatch(events, legacyJobIDs); ok {
		return fmt.Errorf("legacy_delegate_watch_state: watch %q is addressed through a legacy delegate job; use a fresh state root", watchID)
	}
	if len(legacyJobIDs) != 0 || containsLegacyDelegateLifecycle(events) {
		return fmt.Errorf("legacy_delegate_state: root session %q contains legacy delegate lifecycle state; use a fresh state root", rootSessionID)
	}
	return nil
}

func legacyDelegateJobIDs(events []jobstore.Event) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, event := range events {
		if event.Kind == jobstore.EventJobStarted && event.Type == jobstore.JobDelegate && event.JobID != "" {
			ids[event.JobID] = struct{}{}
		}
	}
	return ids
}

func containsLegacyDelegateLifecycle(events []jobstore.Event) bool {
	for _, event := range events {
		switch event.Kind {
		case jobstore.EventDelegateCreated, jobstore.EventDelegateStopGateClosed, jobstore.EventDelegateDisposed:
			return true
		}
		if event.Kind == jobstore.EventJobStarted && event.Type == jobstore.JobDelegate {
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
