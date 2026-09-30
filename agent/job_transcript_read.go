package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

const localJobProjectLookupLimit = 256

type localJobLocation struct {
	StateDir       string
	OwnerSessionID string
	Record         *jobstore.JobRecord
}

// localJobTrustedRoot bounds the symlink check for a local job's paths. A
// session with a state dir roots the check at that dir. A session with no state
// dir stores its jobs under the evener-owned "evener-jobs" base in os.TempDir();
// rooting at the temp dir keeps that base and the session dir inside the checked
// range, leaving the temp dir itself as the trusted ancestor — the flat-layout
// analogue of trusting a state dir's ancestors. Without it, an empty state dir
// roots the check at "" and the walk ascends to the filesystem root, refusing
// the read on a host whose temp dir sits behind a symlink (macOS /var →
// /private/var).
func localJobTrustedRoot(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return os.TempDir()
	}
	return stateDir
}

type localJobProjectDirectory interface {
	ReadDir(n int) ([]fs.DirEntry, error)
	Close() error
}

var openLocalJobProjectDirectory = func(path string) (localJobProjectDirectory, error) {
	return os.Open(path)
}

var lstatJobOutputFile = os.Lstat

// jobOutputOpenRoot returns the descriptor-walk root for a job output path in
// the evener projects layout, or "" when the path is not laid out that way.
//
// The root is the projects directory containing the path's bucket, not the
// state home: the locate step already validated the state home's evener/ and
// evener/projects/ prefixes and the bucket itself (validateLayoutPrefix), so
// rooting the walk above projects/ only re-walks ancestors the read does not
// need. Stopping at projects/ keeps the bucket a walked component, so the
// descriptor walk still refuses a bucket — or a sessions/, session, or jobs
// directory — swapped for a symlink after the locate, and the root open itself
// refuses a symlinked projects/ (O_NOFOLLOW), while an ancestor above the
// anchored root stays followable by design.
//
// Accepted residual: ancestors above the anchored root — the state home and
// evener/ — are followed, not no-followed. That is the lane's standing
// boundary, not new exposure: before this change the root was evener/ and the
// root open had no O_NOFOLLOW, so a symlinked evener/ was followed too; the
// transcript and api-log reads anchor lower still (at the bucket), leaving both
// evener/ and projects/ as followed ancestors. The state root is deliberately
// trusted so hosts with a symlinked $HOME/XDG_STATE_HOME still read, and
// validateLayoutPrefix Lstats evener/, projects/, and the bucket at locate time
// and refuses symlinks there. The residual is a swap of an ancestor above the
// anchored root inside the locate-to-open window, which #2594-round-3 accepted
// for this path.
func jobOutputOpenRoot(path string) string {
	candidate := filepath.Dir(filepath.Clean(path))
	for {
		if stateHome := stateHomeFor(candidate); stateHome != "" {
			return filepath.Join(stateHome, "evener", "projects")
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return ""
		}
		candidate = parent
	}
}

var openJobOutputFile = func(path string) (*os.File, error) {
	before, err := lstatJobOutputFile(path)
	if err != nil {
		return nil, fmt.Errorf("stat job output before open: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("job output %q is not a regular file", path)
	}
	var f *os.File
	if root := jobOutputOpenRoot(path); root != "" {
		f, err = execenv.OpenRegularBeneathRoot(path, root)
	} else {
		f, err = execenv.OpenRegularNoFollow(path)
	}
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat opened job output: %w", err)
	}
	if !os.SameFile(before, after) {
		_ = f.Close()
		return nil, fmt.Errorf("job output %q changed between validation and open", path)
	}
	return f, nil
}

var readLocalJobOutputSnapshot = func(path string, maxBytes int, fromHead bool) (jobstore.OutputSnapshot, error) {
	for attempt := range 2 {
		f, err := openJobOutputFile(path)
		if err != nil {
			return jobstore.OutputSnapshot{}, err
		}
		snapshot, readErr := jobstore.ReadOutputSnapshotFromFile(path, f, maxBytes, fromHead)
		_ = f.Close()
		if errors.Is(readErr, jobstore.ErrOutputChangedDuringRead) && attempt == 0 {
			continue
		}
		return snapshot, readErr
	}
	panic("unreachable")
}

var readLocalJobOutputWindowSnapshot = func(path string, offset int64, maxBytes int) (jobstore.OutputWindowSnapshot, error) {
	for attempt := range 2 {
		f, err := openJobOutputFile(path)
		if err != nil {
			return jobstore.OutputWindowSnapshot{}, err
		}
		snapshot, readErr := jobstore.ReadOutputWindowSnapshotFromFile(path, f, offset, maxBytes)
		_ = f.Close()
		if errors.Is(readErr, jobstore.ErrOutputChangedDuringRead) && attempt == 0 {
			continue
		}
		return snapshot, readErr
	}
	panic("unreachable")
}

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
	if err := symlinkErrorDeep(path, localJobTrustedRoot(stateDir)); err != nil {
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
	// Lstat the journal and require a regular file before ReadEvents opens
	// it: a FIFO or other non-regular non-symlink entry passes the symlink
	// check but blocks os.Open indefinitely (FIFO) or returns garbage.
	// This also narrows the residual TOCTOU window (below): a non-regular
	// entry swapped in between this check and ReadEvents is caught here.
	journalInfo, journalErr := os.Lstat(path)
	if journalErr != nil {
		// Not-found ONLY on ErrNotExist: a permission or I/O error means the
		// journal may exist but is unreadable, which must propagate rather
		// than be masked as "not found" — matching the retained-error
		// discipline this PR established for sibling corruption. Other
		// ErrNotExist-class misses (e.g. a removed journal) stay skip-worthy.
		if errors.Is(journalErr, os.ErrNotExist) {
			return localJobLocation{}, false, nil //nolint:nilerr // journal gone → not found (skip-worthy, not an error)
		}
		return localJobLocation{}, false, fmt.Errorf("read local job %q in project %q: stat journal: %w", jobID, filepath.Base(stateDir), journalErr)
	}
	if !journalInfo.Mode().IsRegular() {
		return localJobLocation{}, false, fmt.Errorf("read local job %q in project %q: journal is not a regular file", jobID, filepath.Base(stateDir))
	}
	// jobstore.ReadEvents opens the journal internally (afero.NewOsFs), so
	// there is a residual TOCTOU window between the checks above and the
	// internal open: a symlink or non-regular entry swapped in between
	// the Lstat and the open would be followed. Changing jobstore's API
	// to accept an fd is disproportionate (it touches the internal
	// package and every caller). The window is narrow — symlinkErrorDeep
	// pre-checks every component, validateLayoutPrefix (round 10)
	// validates the bucket dir, and the Lstat regular check (round 11)
	// rejects non-regular entries — so the residual risk is an in-window
	// swap from regular to non-regular, not a missing check.
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
	if err := symlinkErrorDeep(outputPath, localJobTrustedRoot(location.StateDir)); err != nil {
		return localJobRetainedTarget{}, fmt.Errorf("read local job %q: %w", jobID, err)
	}
	// Require a regular file: a FIFO or other non-regular non-symlink entry
	// passes the symlink check but blocks the downstream read (FIFO) or
	// returns garbage.
	outInfo, outErr := os.Lstat(outputPath)
	if outErr != nil {
		if errors.Is(outErr, os.ErrNotExist) {
			return localJobRetainedTarget{}, localJobRetainedMissingError(jobID)
		}
		return localJobRetainedTarget{}, fmt.Errorf("read local job %q: output missing: %w", jobID, outErr)
	}
	if !outInfo.Mode().IsRegular() {
		return localJobRetainedTarget{}, fmt.Errorf("read local job %q: output is not a regular file", jobID)
	}
	// Downstream output reads narrow the leaf window with their own
	// Lstat→anchored descriptor walk→SameFile check, then pass that descriptor
	// to jobstore. For an in-layout output the descriptor walk is anchored at
	// the projects directory (its own final component opened with O_NOFOLLOW),
	// so the projects directory, the bucket, and the directories below it
	// (sessions/, the session dir, jobs/) replaced by a symlink after
	// this locator's pre-walk is refused at open time. The frozen path-only read
	// seams cannot carry outInfo to that wrapper, so a regular-to-regular leaf
	// replacement, or a fully consistent directory-tree rename, after this
	// locator Lstat but before the wrapper Lstat is accepted as the wrapper's
	// baseline. That residual is explicit and accepted because changing those
	// seam signatures would break the fixed injection boundary; replacements
	// during the wrapper's own Lstat/open interval are refused.
	// Swaps of an ancestor above the anchored root (evener/ or the state home)
	// inside this locator's locate-to-open window are likewise accepted, the
	// same boundary the transcript and api-log reads carry (see jobOutputOpenRoot).
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
	snapshot, err := readLocalJobOutputSnapshot(target.OutputPath, readBytes, false)
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
