package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
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

var readLocalJobOutputSnapshot = jobstore.ReadOutputSnapshot

var readLocalJobOutputWindowSnapshot = jobstore.ReadOutputWindowSnapshot

func locateLocalJob(currentStateDir, jobID string) (localJobLocation, error) {
	ownerSessionID, err := identifier.JobOwnerSessionID(jobID)
	if err != nil {
		return localJobLocation{}, fmt.Errorf("invalid job identifier %q: %w", jobID, err)
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
	// Validate the layout prefix before opening the projects directory:
	// os.Open follows symlinks, so a symlinked evener/ ancestor would let
	// the sibling sweep read entries from outside the state root.
	if err := validateLayoutPrefix(currentStateDir); err != nil {
		return localJobLocation{}, fmt.Errorf("local job %q: %w", jobID, err)
	}
	projectsPath := filepath.Join(stateHome, "evener", "projects")
	dir, err := openLocalJobProjectDirectory(projectsPath)
	if err != nil {
		return localJobLocation{}, fmt.Errorf("open local projects for job %q: %w", jobID, err)
	}
	defer func() { _ = dir.Close() }()

	var match localJobLocation
	haveMatch := false
	var retainedErr error // first genuine sibling error (corruption/unreadability)
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
			// Skip symlinks and non-directories, but do NOT filter by
			// ValidateProjectID — legacy- and foreign-named buckets hold real
			// jobs, mirroring enumerateBuckets (PR #2163's agent-side
			// counterpart).
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			stateDir := filepath.Join(projectsPath, entry.Name())
			candidate, found, err := findLocalJobInProject(stateDir, ownerSessionID, jobID)
			if err != nil {
				// jobstore.ReadEvents returns nil for a missing file, so a
				// non-nil error is genuine corruption or unreadability — not
				// "not the target". Retain the first such error; if the
				// lookup would finish not-found, surface it instead of masking
				// corruption as "job not found". When the target IS found
				// elsewhere, the error is discarded (stray-dir tolerance).
				if retainedErr == nil {
					retainedErr = err
				}
				continue
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
			return finishLocalJobLookup(match, haveMatch, retainedErr, jobID)
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
		return finishLocalJobLookup(match, haveMatch, retainedErr, jobID)
	}
	if readErr != nil {
		return localJobLocation{}, fmt.Errorf("enumerate local projects for job %q: %w", jobID, readErr)
	}
	return localJobLocation{}, fmt.Errorf("enumerate local projects for job %q: sentinel read made no progress", jobID)
}

func findLocalJobInProject(stateDir, ownerSessionID, jobID string) (localJobLocation, bool, error) {
	// Validate the layout prefix: symlinkErrorDeep below is rooted at
	// stateDir, so ancestors above it (evener/, evener/projects/) are not
	// checked. For sibling stateDirs from the enumerate sweep, the prefix
	// was already validated in locateLocalJob; for the current stateDir
	// (first call), no prior validation exists. A symlinked prefix means
	// the job journal is behind a symlink — treat as not-found (skip-
	// worthy), not an error: an unrelated symlinked ancestor should not
	// mask the honest "job not found" result.
	if prefixErr := validateLayoutPrefix(stateDir); prefixErr != nil {
		return localJobLocation{}, false, nil //nolint:nilerr // symlinked prefix is skip-worthy, not an error
	}
	path := filepath.Join(jobsDir(stateDir, ownerSessionID), "jobs.jsonl")
	// Reject symlinked sessions/ dirs before reading the job journal — a
	// symlinked sessions/ could point outside the state root.
	if err := symlinkErrorDeep(path, stateDir); err != nil {
		// If the journal file does not exist (even through the symlink),
		// treat as not-found: the bucket is an unrelated symlinked dir
		// with no target job, and the symlink error should not mask the
		// honest "job not found" result. os.Stat follows symlinks but
		// only reads metadata (not content), so this is safe for
		// existence checking. Other stat errors (permissions, etc.)
		// fall through and surface the symlink error.
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			return localJobLocation{}, false, nil
		}
		return localJobLocation{}, false, fmt.Errorf("read local job %q in project %q: %w", jobID, filepath.Base(stateDir), err)
	}
	// jobstore.ReadEvents opens the journal internally (afero.NewOsFs), so
	// there is a residual TOCTOU window between the symlinkErrorDeep check
	// above and the internal open: a symlink swapped in at the leaf or an
	// intermediate dir between the two calls would be followed. Changing
	// jobstore's API to accept an fd is disproportionate (it touches the
	// internal package and every caller). The window is narrow —
	// symlinkErrorDeep pre-checks every component, and validateLayoutPrefix
	// (round 10) validates the bucket dir itself — so the residual risk is
	// an in-window swap, not a missing check.
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

func finishLocalJobLookup(match localJobLocation, found bool, retainedErr error, jobID string) (localJobLocation, error) {
	if found {
		return match, nil
	}
	// If a sibling bucket had a genuine corruption/unreadability error and
	// the target was not found elsewhere, surface that error instead of
	// masking it as "job not found".
	if retainedErr != nil {
		return localJobLocation{}, retainedErr
	}
	return localJobLocation{}, errJobNotFound(jobID)
}

type localJobSnapshot struct {
	Record       *jobstore.JobRecord
	Content      string
	TotalBytes   int64
	DroppedBytes int64
	Truncated    bool
}

type localJobRetainedTarget struct {
	JobID      string
	Record     *jobstore.JobRecord
	OutputPath string
}

func locateLocalJobRetainedTarget(currentStateDir, jobID string) (localJobRetainedTarget, error) {
	location, err := locateLocalJob(currentStateDir, jobID)
	if err != nil {
		return localJobRetainedTarget{}, err
	}
	outputPath := filepath.Join(jobsDir(location.StateDir, location.OwnerSessionID), "jobs", jobID+".log")
	// Reject symlinked job output paths before reading — a symlinked
	// sessions/ dir could expose output from outside the state root.
	if err := symlinkErrorDeep(outputPath, location.StateDir); err != nil {
		return localJobRetainedTarget{}, fmt.Errorf("read local job %q: %w", jobID, err)
	}
	return localJobRetainedTarget{
		JobID:      jobID,
		Record:     location.Record,
		OutputPath: outputPath,
	}, nil
}

func localJobEnvelopeStatus(record *jobstore.JobRecord) string {
	if record != nil && record.Status.IsTerminal() {
		return "terminal"
	}
	return "running"
}

func validateLocalJobRetainedTotal(target localJobRetainedTarget, total int64) error {
	if target.Record != nil && target.Record.Status.IsTerminal() && target.Record.OutputBytes != total {
		return fmt.Errorf(
			"corrupt local job %q: terminal output_bytes %d does not match snapshot total_bytes %d",
			target.JobID, target.Record.OutputBytes, total,
		)
	}
	return nil
}

func localJobRetainedMissingError(jobID string) error {
	return fmt.Errorf("output_unavailable: job %q retained output is missing or pruned", jobID)
}

func localJobRetainedUnreadableError(jobID string) error {
	return fmt.Errorf("output_unavailable: job %q retained output could not be read", jobID)
}

func localJobRetainedChangedError(jobID string) error {
	return fmt.Errorf("output_changed_during_read: job %q", jobID)
}

func localJobRetainedReadError(target localJobRetainedTarget, offset int64, snapshot jobstore.OutputWindowSnapshot, err error) error {
	status := localJobEnvelopeStatus(target.Record)
	switch {
	case errors.Is(err, jobstore.ErrOutputPruned):
		return fmt.Errorf(
			"output_unavailable: job %q offset %d is no longer retained; first available offset is %d",
			target.JobID, offset, snapshot.RetainedStart,
		)
	case errors.Is(err, jobstore.ErrInvalidOffset):
		return fmt.Errorf(
			"invalid_request: offset_bytes %d is beyond EOF %d; valid byte interval is [%d,%d]; job_status=%s",
			offset, snapshot.TotalBytes, snapshot.RetainedStart, snapshot.TotalBytes, status,
		)
	case errors.Is(err, jobstore.ErrOutputChangedDuringRead):
		return localJobRetainedChangedError(target.JobID)
	case errors.Is(err, os.ErrNotExist):
		return localJobRetainedMissingError(target.JobID)
	default:
		return localJobRetainedUnreadableError(target.JobID)
	}
}

func readLocalJobRetainedMetadata(target localJobRetainedTarget) (jobstore.OutputSnapshot, error) {
	snapshot, err := readLocalJobOutputSnapshot(target.OutputPath, 0, true)
	if errors.Is(err, jobstore.ErrOutputChangedDuringRead) {
		return jobstore.OutputSnapshot{}, localJobRetainedChangedError(target.JobID)
	}
	if errors.Is(err, os.ErrNotExist) {
		return jobstore.OutputSnapshot{}, localJobRetainedMissingError(target.JobID)
	}
	if err != nil {
		return jobstore.OutputSnapshot{}, localJobRetainedUnreadableError(target.JobID)
	}
	if err := validateLocalJobRetainedTotal(target, snapshot.TotalBytes); err != nil {
		return jobstore.OutputSnapshot{}, err
	}
	return snapshot, nil
}

type localJobSearchSource struct {
	target localJobRetainedTarget
}

func (s localJobSearchSource) ReadWindow(offset int64, maxBytes int) (jobstore.OutputWindowSnapshot, error) {
	snapshot, err := readLocalJobOutputWindowSnapshot(s.target.OutputPath, offset, maxBytes)
	if err != nil {
		return snapshot, localJobRetainedReadError(s.target, offset, snapshot, err)
	}
	if err := validateLocalJobRetainedTotal(s.target, snapshot.TotalBytes); err != nil {
		return jobstore.OutputWindowSnapshot{}, err
	}
	return snapshot, nil
}

func readLocalJobSnapshot(currentStateDir, jobID string, readBytes int) (localJobSnapshot, error) {
	target, err := locateLocalJobRetainedTarget(currentStateDir, jobID)
	if err != nil {
		return localJobSnapshot{}, err
	}
	snapshot, err := jobstore.ReadOutputSnapshot(target.OutputPath, readBytes, false)
	if errors.Is(err, jobstore.ErrOutputChangedDuringRead) {
		return localJobSnapshot{}, localJobRetainedChangedError(jobID)
	}
	if errors.Is(err, os.ErrNotExist) {
		return localJobSnapshot{}, localJobRetainedMissingError(jobID)
	}
	if err != nil {
		return localJobSnapshot{}, localJobRetainedUnreadableError(jobID)
	}
	if err := validateLocalJobRetainedTotal(target, snapshot.TotalBytes); err != nil {
		return localJobSnapshot{}, err
	}
	return localJobSnapshot{
		Record:       target.Record,
		Content:      string(snapshot.Content),
		TotalBytes:   snapshot.TotalBytes,
		DroppedBytes: snapshot.RetainedStart,
		Truncated:    snapshot.Truncated,
	}, nil
}
