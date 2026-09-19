package agent

import (
	"path/filepath"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
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
	availableSources := 0
	truncated := false
	for len(queue) > 0 {
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
			if availableSources >= maxRetainedJobSources {
				truncated = true
				break
			}
			availableSources++
			sources = append(sources, jobstore.JournalSource{SessionID: item.id, Root: false, Available: true, Diagnostics: jobstore.ReadDiagnostics{Corrupt: true}})
			continue
		}
		available := events != nil
		if available {
			// Only readable journals consume the source budget. A large tree
			// of missing descendants must not trip the cap, and past the cap we
			// degrade to a truncated-but-usable projection rather than failing
			// the whole load.
			if availableSources >= maxRetainedJobSources {
				truncated = true
				break
			}
			availableSources++
		}
		sources = append(sources, jobstore.JournalSource{SessionID: item.id, Root: item.root, Available: available, Events: events, Diagnostics: readDiag})
		for _, event := range events {
			if event.Kind == jobstore.EventJobStarted {
				owners := []string{event.OwnerSessionID}
				if embedded, err := identifier.JobOwnerSessionID(event.JobID); err == nil {
					owners = append(owners, embedded)
				}
				for _, owner := range owners {
					if schema.ValidateSessionID(owner) == nil && owner != item.id && !seen[owner] {
						queue = append(queue, pending{owner, false})
					}
				}
			}
		}
	}
	records, diagnostics, err := jobstore.MergeJournals(sources)
	if err != nil {
		return nil, diagnostics, err
	}
	if truncated {
		diagnostics.Incomplete = true
		diagnostics.TruncatedSources = append(diagnostics.TruncatedSources, rootSessionID)
	}
	return records, diagnostics, nil
}
