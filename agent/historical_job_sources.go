package agent

import (
	"errors"
	"path/filepath"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

const maxRetainedJobSources = 64

// loadRetainedJobHistory loads each known owner journal independently, keeping
// source identity until jobstore.MergeJournals applies authority.
func loadRetainedJobHistory(stateDir, rootSessionID string) (map[string]*jobstore.JobRecord, jobstore.AuthorityDiagnostics, error) {
	if err := schema.ValidateSessionID(rootSessionID); err != nil {
		return nil, jobstore.AuthorityDiagnostics{}, err
	}
	type pending struct {
		id   string
		root bool
	}
	queue := []pending{{rootSessionID, true}}
	seen := make(map[string]bool)
	var sources []jobstore.JournalSource
	for len(queue) > 0 && len(sources) < maxRetainedJobSources {
		item := queue[0]
		queue = queue[1:]
		if seen[item.id] {
			continue
		}
		seen[item.id] = true
		if err := schema.ValidateSessionID(item.id); err != nil {
			continue
		}
		path := filepath.Join(jobsDir(stateDir, item.id), "jobs.jsonl")
		events, readDiag, err := jobstore.ReadEventsWithDiagnostics(path)
		if err != nil {
			if item.root {
				return nil, jobstore.AuthorityDiagnostics{}, err
			}
			sources = append(sources, jobstore.JournalSource{SessionID: item.id, Root: false, Available: true, Diagnostics: jobstore.ReadDiagnostics{Corrupt: true}})
			continue
		}
		available := events != nil
		sources = append(sources, jobstore.JournalSource{SessionID: item.id, Root: item.root, Available: available, Events: events, Diagnostics: readDiag})
		for _, event := range events {
			if event.Kind == jobstore.EventJobStarted && event.OwnerSessionID != "" && event.OwnerSessionID != item.id {
				if schema.ValidateSessionID(event.OwnerSessionID) == nil && !seen[event.OwnerSessionID] {
					queue = append(queue, pending{event.OwnerSessionID, false})
				}
			}
		}
	}
	if len(queue) > 0 {
		return nil, jobstore.AuthorityDiagnostics{}, errors.New("retained job history source limit exceeded")
	}
	return jobstore.MergeJournals(sources)
}
