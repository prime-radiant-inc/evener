package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/identifier"
)

const localJobProjectLookupLimit = 256

type localJobLocation struct {
	StateDir       string
	OwnerSessionID string
	Record         *jobstore.JobRecord
}

type localJobProjectDirectory interface {
	ReadDir(n int) ([]fs.DirEntry, error)
	Close() error
}

var openLocalJobProjectDirectory = func(path string) (localJobProjectDirectory, error) {
	return os.Open(path)
}

func locateLocalJob(currentStateDir, jobID string) (localJobLocation, error) {
	ownerSessionID, err := identifier.JobOwnerSessionID(jobID)
	if err != nil {
		return localJobLocation{}, fmt.Errorf("invalid job identifier %q: %w", jobID, err)
	}
	if !validLocalBucketDir(currentStateDir) {
		return localJobLocation{}, fmt.Errorf("invalid local project bucket %q", filepath.Base(currentStateDir))
	}

	current, found, err := findLocalJobInProject(currentStateDir, ownerSessionID, jobID)
	if err != nil {
		return localJobLocation{}, err
	}
	if found {
		return current, nil
	}

	stateHome := stateHomeFor(currentStateDir)
	if stateHome == "" {
		return localJobLocation{}, errJobNotFound(jobID)
	}
	projectsPath := filepath.Join(stateHome, "serf", "projects")
	dir, err := openLocalJobProjectDirectory(projectsPath)
	if err != nil {
		return localJobLocation{}, fmt.Errorf("open local projects for job %q: %w", jobID, err)
	}
	defer func() { _ = dir.Close() }()

	var match localJobLocation
	haveMatch := false
	entriesRead := 0
	for entriesRead < localJobProjectLookupLimit {
		entries, readErr := dir.ReadDir(localJobProjectLookupLimit - entriesRead)
		if len(entries) > localJobProjectLookupLimit-entriesRead {
			return localJobLocation{}, fmt.Errorf("enumerate local projects for job %q: directory reader exceeded requested bound", jobID)
		}
		entriesRead += len(entries)
		for _, entry := range entries {
			if entry.Name() == filepath.Base(currentStateDir) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() || identifier.ValidateProjectID(entry.Name()) != nil {
				continue
			}
			stateDir := filepath.Join(projectsPath, entry.Name())
			candidate, found, err := findLocalJobInProject(stateDir, ownerSessionID, jobID)
			if err != nil {
				return localJobLocation{}, err
			}
			if !found {
				continue
			}
			if haveMatch {
				return localJobLocation{}, fmt.Errorf("job %q is ambiguous across local projects", jobID)
			}
			match = candidate
			haveMatch = true
		}
		if errors.Is(readErr, io.EOF) {
			return finishLocalJobLookup(match, haveMatch, jobID)
		}
		if readErr != nil {
			return localJobLocation{}, fmt.Errorf("enumerate local projects for job %q: %w", jobID, readErr)
		}
		if len(entries) == 0 {
			return localJobLocation{}, fmt.Errorf("enumerate local projects for job %q: directory reader made no progress", jobID)
		}
	}

	sentinel, readErr := dir.ReadDir(1)
	if len(sentinel) > 0 {
		return localJobLocation{}, fmt.Errorf("lookup_limit_exceeded: job %q exceeded %d local project entries", jobID, localJobProjectLookupLimit)
	}
	if errors.Is(readErr, io.EOF) {
		return finishLocalJobLookup(match, haveMatch, jobID)
	}
	if readErr != nil {
		return localJobLocation{}, fmt.Errorf("enumerate local projects for job %q: %w", jobID, readErr)
	}
	return localJobLocation{}, fmt.Errorf("enumerate local projects for job %q: sentinel read made no progress", jobID)
}

func findLocalJobInProject(stateDir, ownerSessionID, jobID string) (localJobLocation, bool, error) {
	path := filepath.Join(jobsDir(stateDir, ownerSessionID), "jobs.jsonl")
	events, err := jobstore.ReadEvents(path)
	if err != nil {
		return localJobLocation{}, false, fmt.Errorf("read local job %q in project %q: %w", jobID, filepath.Base(stateDir), err)
	}
	record := jobstore.Fold(events)[jobID]
	if record == nil {
		return localJobLocation{}, false, nil
	}
	if record.JobID != jobID || record.OwnerSessionID != ownerSessionID {
		return localJobLocation{}, false, fmt.Errorf("corrupt local job %q in project %q: record coordinates do not match owner %q", jobID, filepath.Base(stateDir), ownerSessionID)
	}
	return localJobLocation{StateDir: stateDir, OwnerSessionID: ownerSessionID, Record: record}, true, nil
}

func finishLocalJobLookup(match localJobLocation, found bool, jobID string) (localJobLocation, error) {
	if found {
		return match, nil
	}
	return localJobLocation{}, errJobNotFound(jobID)
}

func readLocalJobSnapshot(currentStateDir, jobID string, readBytes int) (jobReadOutputSnapshot, error) {
	location, err := locateLocalJob(currentStateDir, jobID)
	if err != nil {
		return jobReadOutputSnapshot{}, err
	}
	outputPath := filepath.Join(jobsDir(location.StateDir, location.OwnerSessionID), "jobs", jobID+".log")
	snapshot, err := jobstore.ReadOutputSnapshot(outputPath, readBytes, false)
	if errors.Is(err, jobstore.ErrOutputChangedDuringRead) {
		return jobReadOutputSnapshot{}, fmt.Errorf("output_changed_during_read: job %q: %w", jobID, err)
	}
	if errors.Is(err, os.ErrNotExist) {
		return jobReadOutputSnapshot{}, fmt.Errorf("output_unavailable: job %q retained output is missing or pruned: %w", jobID, err)
	}
	if err != nil {
		return jobReadOutputSnapshot{}, err
	}
	if location.Record.Status.IsTerminal() && location.Record.OutputBytes != snapshot.TotalBytes {
		return jobReadOutputSnapshot{}, fmt.Errorf(
			"corrupt local job %q: terminal output_bytes %d does not match snapshot total_bytes %d",
			jobID, location.Record.OutputBytes, snapshot.TotalBytes,
		)
	}
	return jobReadOutputSnapshot{
		Record:       location.Record,
		Content:      string(snapshot.Content),
		TotalBytes:   snapshot.TotalBytes,
		DroppedBytes: snapshot.RetainedStart,
		Truncated:    snapshot.Truncated,
	}, nil
}
