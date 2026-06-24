package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

var errJobManagerClosing = errors.New("job manager is closing")

const maxPersistedStructuredResultJSONBytes = 1024 * 1024

const (
	structuredResultReasonSchemaValidationFailed = "schema_validation_failed"
	structuredResultReasonSchemaResultMissing    = "schema_result_missing"
	structuredResultReasonSchemaResultTooLarge   = "schema_result_too_large"
	structuredResultReasonSchemaCaptureFailed    = "schema_capture_failed"
	structuredResultReasonProjectionTooLarge     = "projection_too_large"
)

type jobManager struct {
	mu            sync.Mutex
	watchNotifyMu sync.Mutex
	dir           string
	// stateDir is the project state dir (parent of sessions/), where session
	// .meta.json files live. Empty for the temp/test fallback where no project
	// state dir is configured; observer-link stamping is skipped in that case.
	stateDir      string
	sessionID     string
	transcriptRef string
	store         *jobstore.Store
	running       map[string]*runningJob
	watches       map[watchKey]*watchConfig
	terminalFlush map[*watchConfig]bool
	// watchHistory is a bounded, latest-trimmed ring of watches that have left
	// the active set, surfaced by job_list so a fired-then-removed watch stays
	// legible. Guarded by jm.mu.
	watchHistory []watchHistoryEntry
	// lastFedOffset records the highest stream end offset fed to the output
	// matcher per job. The per-job output pump is single-goroutine, so this is
	// monotone; a regression signals a caller bug and the offending chunk is
	// dropped rather than corrupting the matcher's scan-offset accounting.
	// Guarded by jm.mu.
	lastFedOffset map[string]int64
	closing       bool
	appendEvent   func(jobstore.Event) error
	appendEvents  func([]jobstore.Event) error
	emit          func(events.EventKind, events.EventData, *provenance.Causal)
	forward       func(jobstore.Event) error
	parentJobID   string
	enqueue       func(jobNotification)
	// currentProvenance reports the owning session's active causal provenance at
	// call time. A job records this at creation so its detached lifecycle events
	// and terminal notification carry the origin of whatever input launched it.
	currentProvenance func() *provenance.Causal
	// wake kicks the owning session's drain loop (wired to Session.notify).
	// nil for test/restore-only managers. Observation paths call kick() after
	// persisting watch-send intent; they never deliver (spec §3).
	wake func()
	now  func() time.Time
	// closeGrace bounds how long closeRuntimeState waits for each still-running
	// job to finalize before abandoning it. Seeded from defaultCloseGrace at
	// construction so tests can shrink the graceful-shutdown window.
	closeGrace time.Duration
	// quietWindow and quietCheckInterval govern the delegate quiet watchdog.
	// Seeded from the package defaults at construction; per-manager so tests can
	// scale them on their own jobManager without mutating a shared global that
	// concurrently-running watchdogs would race on.
	quietWindow        time.Duration
	quietCheckInterval time.Duration
}

// defaultCloseGrace is the graceful-shutdown window closeRuntimeState gives a
// still-running job to finalize before abandoning it. Tests override it to keep
// teardown of jobs that never naturally terminate from costing real seconds.
var defaultCloseGrace = 5 * time.Second

func (jm *jobManager) setParentJobID(jobID string) {
	jm.mu.Lock()
	jm.parentJobID = jobID
	jm.mu.Unlock()
}

func (jm *jobManager) currentParentJobID() string {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.parentJobID
}

func (jm *jobManager) appendJobEvents(events []jobstore.Event) error {
	if len(events) == 0 {
		return nil
	}
	if len(events) == 1 || jm.appendEvents == nil {
		for _, event := range events {
			if err := jm.appendEvent(event); err != nil {
				return err
			}
		}
		return nil
	}
	return jm.appendEvents(events)
}

// currentCausalProvenance snapshots the owning session's active provenance for
// recording on a newly created job. nil when no provenance source is wired
// (test/restore-only managers) or the active set is empty.
func (jm *jobManager) currentCausalProvenance() *provenance.Causal {
	if jm == nil || jm.currentProvenance == nil {
		return nil
	}
	return provenance.Clone(jm.currentProvenance())
}

type runningJob struct {
	rec                     *jobstore.JobRecord
	output                  *jobstore.OutputStore
	signal                  func()
	done                    chan struct{}
	doneOnce                sync.Once
	durableStarted          bool
	stopStatus              jobstore.Status
	stopReason              string
	structured              any
	structuredCaptureFailed bool
	terminal                *terminalJob
	finalize                *finalizeAttempt
	delegateOutputAppended  bool
	delegateOutputWritten   int
	delegateResumeAssessed  bool
	afterDurableFinish      func()
	fromWatch               atomic.Bool
	callerCallbackDelivered atomic.Bool
	forwardDisabled         bool
	// watchdogStop, when non-nil, stops the quiet-job watchdog goroutine for a
	// running delegate. Closed once at finalize. quietNotified latches the
	// quiet notification to once-per-quiet-stretch; it is read/written under
	// jm.mu by the watchdog tick and cleared when activity resumes.
	watchdogStop    chan struct{}
	watchdogStopped sync.Once
	quietNotified   bool
	// treeSlot holds the tree-counter reservation for a running delegate turn
	// (spec §4). Set when the spawn/resume path reserves a slot for this run;
	// released exactly once when the run leaves jm.running (terminal finalize or
	// the abandon path). treeReservation.release is idempotent so
	// finalize-then-abandon (or a finalize retry) never double-releases.
	treeSlot *treeReservation
}

// treeReservation is a single tree-counter slot held by a running delegate turn.
// release is once-only so a slot is never returned twice even when multiple
// teardown paths (finalize, abandon, abandon-all) race on the same run.
type treeReservation struct {
	counter  *treeCounter
	released atomic.Bool
}

func (r *treeReservation) release() {
	if r == nil {
		return
	}
	if r.released.CompareAndSwap(false, true) {
		r.counter.release()
	}
}

type finalizeAttempt struct {
	done chan struct{}
	err  error
}

func (run *runningJob) closeDone() {
	if run == nil {
		return
	}
	run.doneOnce.Do(func() {
		close(run.done)
	})
}

// stopWatchdog stops the quiet-job watchdog goroutine (delegate jobs only).
// Idempotent and safe on a runningJob that never armed a watchdog.
func (run *runningJob) stopWatchdog() {
	if run == nil || run.watchdogStop == nil {
		return
	}
	run.watchdogStopped.Do(func() {
		close(run.watchdogStop)
	})
}

// stampLastActivityLocked records now() as the running job's most recent
// parent-observable activity. Callers must hold jm.mu. A stamp replaces the
// LastActivity pointer wholesale, so a record clone that shares the pointer is
// never mutated in place. No-op when the job is not running.
func (jm *jobManager) stampLastActivityLocked(jobID string) {
	run := jm.running[jobID]
	if run == nil || run.rec == nil {
		return
	}
	now := jm.now()
	run.rec.LastActivity = &now
}

func (jm *jobManager) noteJobActivity(jobID, phase string) {
	if jm == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	now := jm.now()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil || run.rec == nil || run.rec.Status.IsTerminal() {
		return
	}
	run.rec.LastActivity = &now
	if phase != "" {
		run.rec.Phase = phase
	}
}

type terminalJob struct {
	status                       jobstore.Status
	reason                       string
	exitCode                     *int
	endedAt                      time.Time
	outputBytes                  int64
	generation                   string
	finished                     jobstore.Event
	finishedForwarded            bool
	finishedEmitted              bool
	afterDurableFinishCalled     bool
	notificationPending          jobstore.Event
	notificationPendingAppended  bool
	notificationPendingForwarded bool
}

type jobRuntimeHandle struct {
	jobID     string
	signal    func()
	done      chan struct{}
	closeDone func()
	output    *jobstore.OutputStore
}

// jobNotification is the in-memory wake record for a durable job notification.
type jobNotification struct {
	JobID, JobType, Status, Reason, TranscriptRef string
	OutputBytes                                   int64
	ExitCode                                      *int
	// Provenance is the causal origin carried with this notification: the
	// triggering watch's lineage so the notification turn it drives stamps the
	// same origin and a same-watch retrigger is suppressed.
	Provenance *provenance.Causal
	// WatchSend marks this entry as a watch-send wake token: render-by-key
	// against the owning jobManager's CURRENT pending state at accept time
	// (spec §4.3). The frame text is deliberately NOT carried here.
	WatchSend *watchSendToken
	// receiverSessionID/receiverNotify route no-send watch notifications for
	// concrete descendant watches back to the ancestor session that installed
	// them. They are in-memory only; active watches are not restored without a
	// live installer.
	receiverSessionID string
	receiverNotify    func(jobNotification)
	// watchSendFrame is the rendered frame, populated only between filter and
	// format inside one accept pass (never persisted, never enqueued).
	watchSendFrame string
}

type createShellOpts struct {
	Command     string
	Description string
}

type listFilter struct {
	Status        jobstore.Status
	Statuses      []jobstore.Status
	Type          jobstore.JobType
	Types         []jobstore.JobType
	Limit         int
	IncludeNested bool
	// IncludeDescendants drives the recursive live-subtree walk in jobListTool
	// (spec §2). It is not consulted by listWithError, which lists a single
	// store; the walk reads each descendant store independently under its own
	// lock and merges with the owner-authoritative dedupe rule.
	IncludeDescendants bool
}

// jobsDir returns the per-session job directory: <stateDir>/sessions/<id>.
func jobsDir(stateDir, sessionID string) string {
	if strings.TrimSpace(stateDir) == "" {
		return filepath.Join(os.TempDir(), "serf-jobs", sessionID)
	}
	return filepath.Join(stateDir, "sessions", sessionID)
}

func newJobManager(stateDir, sessionID string, enqueue func(jobNotification)) (*jobManager, error) {
	dir := jobsDir(stateDir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o755); err != nil {
		return nil, err
	}
	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		return nil, err
	}
	jm := &jobManager{
		dir:                dir,
		stateDir:           stateDir,
		sessionID:          sessionID,
		transcriptRef:      encodeRef("", sessionID),
		store:              store,
		running:            make(map[string]*runningJob),
		watches:            make(map[watchKey]*watchConfig),
		lastFedOffset:      make(map[string]int64),
		appendEvent:        store.Append,
		appendEvents:       store.AppendBatch,
		enqueue:            enqueue,
		now:                time.Now,
		closeGrace:         defaultCloseGrace,
		quietWindow:        delegateQuietWindow,
		quietCheckInterval: delegateQuietCheckInterval,
	}
	if err := jm.restoreWatchSendPending(); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := jm.clearUnrestoredActiveWatches(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return jm, nil
}

func (jm *jobManager) close() error {
	runtimeErr := jm.closeRuntimeState()
	storeErr := jm.closeStoreOnly()
	return errors.Join(runtimeErr, storeErr)
}

func (jm *jobManager) closeRuntimeState() error {
	jm.watchNotifyMu.Lock()
	jm.mu.Lock()
	jm.closing = true
	targets := make([]watchConfigTerminalSnapshot, 0, len(jm.watches))
	for key, cfg := range jm.watches {
		targets = append(targets, watchConfigTerminalSnapshot{
			key:       key,
			cfg:       cfg,
			terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "job manager closed", jm.now()),
			endReason: "job_manager_closed",
		})
	}
	markWatchConfigSnapshotsRejectingLocked(targets)
	dropped := terminalSnapshots(targets)
	for cfg := range jm.terminalFlush {
		dropped = append(dropped, watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "job manager closed", jm.now()))
	}
	running := make([]jobRuntimeHandle, 0, len(jm.running))
	for _, run := range jm.running {
		run.stopWatchdog()
		running = append(running, jobRuntimeHandle{
			jobID:  run.rec.JobID,
			signal: run.signal,
			done:   run.done,
			output: run.output,
		})
	}
	jm.mu.Unlock()
	jm.watchNotifyMu.Unlock()

	err := jm.appendWatchTeardownBatch(dropped, targets)
	if err != nil {
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.closeWatchConfigSnapshots(targets)
	} else {
		jm.detachWatchConfigSnapshots(targets)
		jm.removeWatchSendTerminalSnapshots(dropped)
	}
	watchCleanupErr := err
	jm.mu.Lock()
	for _, handle := range running {
		if run := jm.running[handle.jobID]; run != nil && run.stopStatus == "" {
			run.stopStatus = jobstore.StatusCancelled
			run.stopReason = "stopped_by_parent"
		}
	}
	jm.mu.Unlock()
	for _, run := range running {
		if run.signal != nil {
			run.signal()
		}
	}
	deadline := time.NewTimer(jm.closeGrace)
	defer deadline.Stop()
	var waitErr error
waitLoop:
	for _, run := range running {
		select {
		case <-run.done:
		case <-deadline.C:
			waitErr = errors.New("job manager close timed out waiting for running jobs")
			jm.abandonRunningJobs()
			break waitLoop
		}
	}
	return errors.Join(watchCleanupErr, waitErr)
}

func (jm *jobManager) closeStoreOnly() error {
	if jm == nil || jm.store == nil {
		return nil
	}
	if err := jm.store.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

func (jm *jobManager) abandonRunningJobs() {
	jm.mu.Lock()
	running := make([]jobRuntimeHandle, 0, len(jm.running))
	var targets []watchConfigTerminalSnapshot
	for _, run := range jm.running {
		run.stopWatchdog()
		running = append(running, jobRuntimeHandle{
			jobID:     run.rec.JobID,
			done:      run.done,
			closeDone: run.closeDone,
			output:    run.output,
		})
		delete(jm.running, run.rec.JobID)
		run.treeSlot.release()
		targets = append(targets, jm.pruneWatchedTargetWatchesLocked(run.rec.JobID, "watched target pruned", jm.now())...)
	}
	jm.mu.Unlock()
	dropped := terminalSnapshots(targets)
	applied, err := jm.appendWatchSendTerminalSnapshots(dropped)
	if err != nil {
		jm.removeWatchSendTerminalSnapshots(applied)
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification("", "watch send prune cleanup failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
	} else {
		jm.detachWatchConfigSnapshots(targets)
		jm.removeWatchSendTerminalSnapshots(applied)
	}
	for _, run := range running {
		if run.output != nil {
			_ = run.output.Close()
		}
		if run.closeDone != nil {
			run.closeDone()
		}
	}
}

func (jm *jobManager) abandonRunningJob(jobID string) {
	jm.mu.Lock()
	run := jm.running[jobID]
	var targets []watchConfigTerminalSnapshot
	if run != nil {
		run.stopWatchdog()
		delete(jm.running, jobID)
		run.treeSlot.release()
		targets = jm.pruneWatchedTargetWatchesLocked(jobID, "watched target pruned", jm.now())
	}
	jm.mu.Unlock()
	if run == nil {
		return
	}
	dropped := terminalSnapshots(targets)
	applied, err := jm.appendWatchSendTerminalSnapshots(dropped)
	if err != nil {
		jm.removeWatchSendTerminalSnapshots(applied)
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(jobID, "watch send prune cleanup failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
	} else {
		jm.detachWatchConfigSnapshots(targets)
		jm.removeWatchSendTerminalSnapshots(applied)
	}
	if run.output != nil {
		_ = run.output.Close()
	}
	run.closeDone()
}

func (jm *jobManager) createShell(opts createShellOpts) (*jobstore.JobRecord, error) {
	startedAt := jm.now()
	jobID := jobstore.NewJobID()
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	parentJobID := jm.currentParentJobID()
	jobProvenance := jm.currentCausalProvenance()
	rec := &jobstore.JobRecord{
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Status:           jobstore.StatusRunning,
		Command:          opts.Command,
		Description:      opts.Description,
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		ParentJobID:      parentJobID,
		StartedAt:        startedAt,
		Phase:            jobPhaseProcessRunning,
		LastActivity:     &startedAt,
		OutputPath:       outputPath,
		Provenance:       provenance.Clone(jobProvenance),
	}
	output, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		return nil, err
	}
	run := &runningJob{
		rec:            rec,
		output:         output,
		signal:         func() {},
		done:           make(chan struct{}),
		durableStarted: true,
	}

	jm.mu.Lock()
	if jm.closing {
		jm.mu.Unlock()
		_ = output.Close()
		_ = os.Remove(outputPath)
		return nil, errJobManagerClosing
	}
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            rec.JobID,
		Type:             rec.Type,
		Command:          rec.Command,
		Description:      rec.Description,
		OwnerSessionID:   rec.OwnerSessionID,
		VisibleToSession: rec.VisibleToSession,
		ParentJobID:      rec.ParentJobID,
		StartedAt:        &startedAt,
		OutputPath:       rec.OutputPath,
		Provenance:       provenance.Clone(jobProvenance),
	}
	if err := jm.appendEvent(started); err != nil {
		jm.mu.Unlock()
		_ = output.Close()
		return nil, err
	}
	if err := jm.forwardLocked(started); err != nil {
		_ = output.Close()
		if terminalErr := jm.appendStartForwardFailure(rec.JobID, output, rec.Provenance); terminalErr != nil {
			run.forwardDisabled = true
			jm.running[jobID] = run
			jm.mu.Unlock()
			go jm.finalizeShellUntilDurable(jobID, jobstore.StatusFailed, "forward_failed", nil)
			return nil, errors.Join(err, terminalErr)
		}
		jm.mu.Unlock()
		return nil, errors.Join(errDelayedShellStartForwardFailed, err)
	}
	jm.running[jobID] = run
	jm.mu.Unlock()
	jm.emitJobStarted(started, run)
	return rec, nil
}

func (jm *jobManager) list(filter listFilter) []*jobstore.JobRecord {
	jobs, err := jm.listWithError(filter)
	if err != nil {
		return nil
	}
	return jobs
}

func (jm *jobManager) listWithError(filter listFilter) ([]*jobstore.JobRecord, error) {
	recs, err := jm.store.Load()
	if err != nil {
		return nil, err
	}

	jm.mu.Lock()
	for jobID, run := range jm.running {
		if !run.durableStarted {
			continue
		}
		rec := cloneJobRecord(run.rec)
		// OutputBytes on the live record is only stamped at terminal; a running
		// job's listing reports the live lifetime output count instead of 0.
		if rec.Status == jobstore.StatusRunning && run.output != nil {
			rec.OutputBytes = run.output.Len()
		}
		recs[jobID] = rec
	}
	jm.mu.Unlock()

	jobs := make([]*jobstore.JobRecord, 0, len(recs))
	for _, rec := range recs {
		if !filter.IncludeNested && rec.ParentJobID != "" && rec.OwnerSessionID != jm.sessionID {
			continue
		}
		if filter.Status != "" && rec.Status != filter.Status {
			continue
		}
		if len(filter.Statuses) > 0 && !statusAllowed(rec.Status, filter.Statuses) {
			continue
		}
		if filter.Type != "" && rec.Type != filter.Type {
			continue
		}
		if len(filter.Types) > 0 && !typeAllowed(rec.Type, filter.Types) {
			continue
		}
		jobs = append(jobs, cloneJobRecord(rec))
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].JobID < jobs[j].JobID
		}
		return jobs[i].StartedAt.After(jobs[j].StartedAt)
	})
	if filter.Limit > 0 && len(jobs) > filter.Limit {
		jobs = jobs[:filter.Limit]
	}
	return jobs, nil
}

func statusAllowed(status jobstore.Status, allowed []jobstore.Status) bool {
	for _, want := range allowed {
		if status == want {
			return true
		}
	}
	return false
}

func typeAllowed(jobType jobstore.JobType, allowed []jobstore.JobType) bool {
	for _, want := range allowed {
		if jobType == want {
			return true
		}
	}
	return false
}

func (jm *jobManager) emitJobStarted(e jobstore.Event, run *runningJob) {
	if jm == nil || jm.emit == nil {
		return
	}
	fromWatch := false
	if run != nil {
		fromWatch = run.fromWatch.Load()
	}
	jm.emit(events.EventJobStarted, events.JobStartedData{
		JobID:     e.JobID,
		JobType:   string(e.Type),
		Status:    string(jobstore.StatusRunning),
		FromWatch: fromWatch,
	}, e.Provenance)
}

func (jm *jobManager) emitJobFinished(e jobstore.Event, run *runningJob) {
	if jm == nil || jm.emit == nil {
		return
	}
	jobType := ""
	transcriptRef := ""
	fromWatch := false
	if run != nil && run.rec != nil {
		jobType = string(run.rec.Type)
		transcriptRef = run.rec.TranscriptRef
		fromWatch = run.fromWatch.Load()
	}
	jm.emit(events.EventJobFinished, events.JobFinishedData{
		JobID:         e.JobID,
		JobType:       jobType,
		Status:        string(e.Status),
		Reason:        e.Reason,
		ExitCode:      e.ExitCode,
		OutputBytes:   e.OutputBytes,
		TranscriptRef: transcriptRef,
		FromWatch:     fromWatch,
	}, e.Provenance)
}

// errJobNotFound is the shared not-found error for a job lookup by id. It points
// the model at job_list to recover: an unknown id is usually a guessed value or a
// foreground command whose output rode inline and kept no durable job — in both
// cases job_list shows the ids that actually exist.
func errJobNotFound(jobID string) error {
	return fmt.Errorf("job %q not found — use job_list to see this session's jobs", jobID)
}

func (jm *jobManager) readOutput(jobID string, tailBytes int) (content string, total int64, truncated bool, err error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		return tailOutput(run.output, tailBytes)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return "", 0, false, err
	}
	rec := recs[jobID]
	if rec == nil {
		return "", 0, false, errJobNotFound(jobID)
	}
	path := jm.outputPathForJob(rec, jobID)
	validatedTotal, _, err := validatedOutputStatsForRecord(path, rec)
	if err != nil {
		return "", 0, false, err
	}
	return tailOutputFile(path, tailBytes, validatedTotal)
}

func (jm *jobManager) readOutputHead(jobID string, headBytes int) (content string, total int64, truncated bool, err error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		return headOutput(run.output, headBytes)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return "", 0, false, err
	}
	rec := recs[jobID]
	if rec == nil {
		return "", 0, false, errJobNotFound(jobID)
	}
	path := jm.outputPathForJob(rec, jobID)
	validatedTotal, _, err := validatedOutputStatsForRecord(path, rec)
	if err != nil {
		return "", 0, false, err
	}
	return headOutputFile(path, headBytes, validatedTotal)
}

// readJobWindow reads either the head or the tail of a job's retained output
// depending on fromHead. When fromHead is true it delegates to readOutputHead,
// otherwise to readOutput (tail). This is the single dispatch point that lets
// callers pass a direction flag without knowing the underlying method names. It
// also reports dropped: the bytes permanently evicted off the head by retention.
func (jm *jobManager) readJobWindow(jobID string, bytes int, fromHead bool) (content string, total int64, dropped int64, truncated bool, err error) {
	if fromHead {
		content, total, truncated, err = jm.readOutputHead(jobID, bytes)
	} else {
		content, total, truncated, err = jm.readOutput(jobID, bytes)
	}
	if err != nil {
		return content, total, 0, truncated, err
	}
	dropped, err = jm.outputDropped(jobID)
	return content, total, dropped, truncated, err
}

// outputDropped returns the number of bytes permanently evicted off the head of
// jobID's output by the retention cap (0 when nothing has been pruned), for both
// the live store and the closed-file fallback.
func (jm *jobManager) outputDropped(jobID string) (int64, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		return run.output.RetainedStart(), nil
	}
	recs, err := jm.store.Load()
	if err != nil {
		return 0, err
	}
	rec := recs[jobID]
	if rec == nil {
		return 0, errJobNotFound(jobID)
	}
	path := jm.outputPathForJob(rec, jobID)
	_, retainedStart, err := validatedOutputStatsForRecord(path, rec)
	return retainedStart, err
}

func (jm *jobManager) appendJobOutput(jobID string, output *jobstore.OutputStore, b []byte) (int, error) {
	return jm.appendJobOutputWithProvenance(jobID, output, b, nil)
}

func (jm *jobManager) appendJobOutputWithProvenance(jobID string, output *jobstore.OutputStore, b []byte, p *provenance.Causal) (int, error) {
	if output == nil {
		return 0, nil
	}
	n, err := output.Append(b)
	if err == nil && n > 0 {
		// Len is the post-append lifetime byte count, the offset space the output
		// matcher scans in; pass it as the chunk's end offset.
		jm.feedJobOutputWithProvenance(jobID, b[:n], output.Len(), p)
	}
	return n, err
}

func (jm *jobManager) grepOutput(jobID string, re *regexp.Regexp) ([]jobstore.Match, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		// Scan the full retained output: maxJobOutputRetentionBytes caps retention, so it
		// doubles as the scan budget; the result stays bounded by maxJobGrepMatches and maxJobGrepLineBytes.
		return run.output.GrepLimitLineBytes(re, maxJobOutputRetentionBytes, maxJobGrepMatches, maxJobGrepLineBytes)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return nil, err
	}
	rec := recs[jobID]
	if rec == nil {
		return nil, errJobNotFound(jobID)
	}
	path := jm.outputPathForJob(rec, jobID)
	_, retainedStart, err := validatedOutputStatsForRecord(path, rec)
	if err != nil {
		return nil, err
	}
	// Full retained scan (same budget rationale as above).
	return grepOutputFile(path, re, maxJobOutputRetentionBytes, retainedStart)
}

func (jm *jobManager) reconcileLostJobs() error {
	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	watches, err := jm.store.LoadWatches()
	if err != nil {
		return err
	}

	jm.mu.Lock()
	live := make(map[string]bool, len(jm.running))
	for jobID := range jm.running {
		live[jobID] = true
	}
	jm.mu.Unlock()

	localRecs := make(map[string]*jobstore.JobRecord, len(recs))
	for jobID, rec := range recs {
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
			continue
		}
		localRecs[jobID] = rec
	}

	for _, finished := range jobstore.Reconcile(localRecs, live, jm.now()) {
		rec := localRecs[finished.JobID]
		if total, _, err := jobstore.OutputFileStats(jm.outputPathForJob(rec, finished.JobID)); err == nil {
			finished.OutputBytes = total
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		finished.Provenance = provenance.Clone(rec.Provenance)
		pending := jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          finished.TS,
			JobID:       finished.JobID,
			TerminalGen: finished.TerminalGen,
			Provenance:  provenance.Clone(rec.Provenance),
		}
		events := []jobstore.Event{finished, pending}
		events = append(events, recoveredTerminalWatchClearEvents(watches, finished.JobID, finished.TS)...)
		if err := jm.appendJobEvents(events); err != nil {
			return err
		}
		if jm.enqueue != nil {
			jm.enqueue(jobNotification{
				JobID:         finished.JobID,
				JobType:       string(rec.Type),
				Status:        string(finished.Status),
				Reason:        finished.Reason,
				TranscriptRef: rec.TranscriptRef,
				OutputBytes:   finished.OutputBytes,
				ExitCode:      finished.ExitCode,
				Provenance:    provenance.Clone(rec.Provenance),
			})
		}
	}
	return nil
}

func recoveredTerminalWatchClearEvents(watches map[string]*jobstore.WatchRecord, jobID string, ts time.Time) []jobstore.Event {
	return watchClearEventsForRecords(watches, ts, "auto_removed_terminal", func(watch *jobstore.WatchRecord) bool {
		return watch.Target == jobID
	})
}

func (jm *jobManager) clearUnrestoredActiveWatches() error {
	watches, err := jm.store.LoadWatches()
	if err != nil {
		return err
	}
	return jm.appendJobEvents(watchClearEventsForRecords(watches, jm.now(), "runtime_lost", func(*jobstore.WatchRecord) bool {
		return true
	}))
}

func watchClearEventsForRecords(watches map[string]*jobstore.WatchRecord, ts time.Time, endReason string, include func(*jobstore.WatchRecord) bool) []jobstore.Event {
	if len(watches) == 0 {
		return nil
	}
	var watchIDs []string
	for watchID, watch := range watches {
		if watch == nil || !watch.Active || watch.Generation == "" || !include(watch) {
			continue
		}
		watchIDs = append(watchIDs, watchID)
	}
	sort.Strings(watchIDs)
	events := make([]jobstore.Event, 0, len(watchIDs))
	for _, watchID := range watchIDs {
		watch := watches[watchID]
		events = append(events, jobstore.Event{
			Kind:    jobstore.EventWatchCleared,
			TS:      ts,
			WatchID: watchID,
			Watch: &jobstore.WatchEvent{
				Generation: watch.Generation,
				EndReason:  endReason,
			},
		})
	}
	return events
}

func (jm *jobManager) outputPathForJob(rec *jobstore.JobRecord, jobID string) string {
	if rec != nil && rec.OutputPath != "" {
		return rec.OutputPath
	}
	return filepath.Join(jm.dir, "jobs", jobID+".log")
}

// runningJobIDs returns a snapshot of the durably-started running job IDs. The
// snapshot is taken under jm.mu and the lock is released before the caller acts
// on it, so the stop cascade never holds a job-manager lock while it stops jobs
// or recurses into descendant stores (the leaf-lock discipline of the live walk).
func (jm *jobManager) runningJobIDs() []string {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	ids := make([]string, 0, len(jm.running))
	for jobID, run := range jm.running {
		if run.durableStarted {
			ids = append(ids, jobID)
		}
	}
	return ids
}

func (jm *jobManager) stop(jobID string) (*jobstore.JobRecord, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	if run != nil {
		rec := cloneJobRecord(run.rec)
		if run.finalize != nil || run.terminal != nil || run.rec.Status.IsTerminal() {
			jm.mu.Unlock()
			return rec, nil
		}
		if err := jm.appendDelegateStopGateForRecord(rec); err != nil {
			jm.mu.Unlock()
			return nil, err
		}
		run.stopStatus = jobstore.StatusCancelled
		run.stopReason = "stopped_by_parent"
		signal := run.signal
		rec.Status = run.stopStatus
		rec.Reason = run.stopReason
		jm.mu.Unlock()
		if signal != nil {
			signal()
		}
		return rec, nil
	}
	jm.mu.Unlock()

	recs, err := jm.store.Load()
	if err != nil {
		return nil, err
	}
	rec := recs[jobID]
	if rec == nil {
		return nil, errJobNotFound(jobID)
	}
	return cloneJobRecord(rec), nil
}

func (jm *jobManager) appendDelegateStopGateForRecord(rec *jobstore.JobRecord) error {
	if rec == nil || rec.Type != jobstore.JobDelegate || rec.DelegateID == "" {
		return nil
	}
	return jm.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateStopGateClosed,
		TS:         jm.now(),
		DelegateID: rec.DelegateID,
		Delegate: &jobstore.DelegateEvent{
			Generation: jobstore.NewDelegateGeneration(),
			StopJobID:  rec.JobID,
		},
	})
}

func (jm *jobManager) finalize(jobID string, status jobstore.Status, reason string, exitCode *int) error {
	return jm.finalizeWithRun(jobID, func(run *runningJob) (jobstore.Status, string, *int, error) {
		return status, reason, exitCode, nil
	})
}

// finalizeKeptSync finalizes a within-bound shell job that is being kept
// (complete-or-handle, spec §0.6). It fires watches but does NOT append
// EventJobNotificationPending or enqueue an owner notification — the model
// already received the complete result inline (spec §6.4d).
func (jm *jobManager) finalizeKeptSync(run *runningJob, status jobstore.Status, reason string, exitCode *int) error {
	jm.mu.Lock()
	terminal := run.terminal
	jm.mu.Unlock()
	if terminal == nil {
		var err error
		terminal, err = jm.writeFinishJob(run, status, reason, exitCode)
		if err != nil {
			return err
		}
	}
	if err := jm.forwardFinishedJob(run, terminal); err != nil {
		return err
	}
	jm.emitFinishedJob(run, terminal)
	jm.runAfterDurableFinish(run, terminal, run.afterDurableFinish)

	jm.mu.Lock()
	if jm.running[run.rec.JobID] != run || run.terminal != terminal {
		jm.mu.Unlock()
		return nil
	}
	watchRegistryEvents, expiredWatches, watchRootProvenance := jm.expireJobWatchesLocked(run.rec.JobID)
	if err := jm.appendWatchRegistryEvents(watchRegistryEvents); err != nil {
		jm.mu.Unlock()
		return err
	}
	watchNotifications, watchDeliveries := jm.completeExpiredJobWatchesLocked(run.rec.JobID, expiredWatches, watchRootProvenance)
	jm.mu.Unlock()
	jm.enqueueWatchNotifications(watchNotifications)
	jm.recordWatchSendsAndKick(watchDeliveries)
	// No EventJobNotificationPending, no jm.enqueue call: kept within-bound
	// jobs completed before the tool returned (spec §6.4d).

	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run && run.terminal == terminal {
		delete(jm.running, run.rec.JobID)
		run.treeSlot.release()
	}
	jm.mu.Unlock()
	run.stopWatchdog()
	_ = run.output.Close()
	run.closeDone()
	return nil
}

func (jm *jobManager) finalizeWithRun(jobID string, prepare func(*runningJob) (jobstore.Status, string, *int, error)) error {
	return jm.finalizeWithRunMode(jobID, prepare, true)
}

func (jm *jobManager) finalizeWithRunNoNotification(jobID string, prepare func(*runningJob) (jobstore.Status, string, *int, error)) error {
	return jm.finalizeWithRunMode(jobID, prepare, false)
}

func (jm *jobManager) finalizeWithRunMode(jobID string, prepare func(*runningJob) (jobstore.Status, string, *int, error), armNotification bool) error {
	jm.mu.Lock()
	run := jm.running[jobID]
	if run == nil {
		jm.mu.Unlock()
		return nil
	}
	if run.finalize != nil {
		attempt := run.finalize
		jm.mu.Unlock()
		<-attempt.done
		return attempt.err
	}
	attempt := &finalizeAttempt{done: make(chan struct{})}
	run.finalize = attempt
	terminal := run.terminal
	jm.mu.Unlock()

	var err error
	if terminal == nil {
		status, reason, exitCode, prepareErr := prepare(run)
		if prepareErr != nil {
			err = prepareErr
		} else if armNotification {
			err = jm.finishJob(run, status, reason, exitCode)
		} else {
			err = jm.finalizeKeptSync(run, status, reason, exitCode)
		}
	} else {
		if armNotification {
			err = jm.forwardFinishedJob(run, terminal)
			if err == nil {
				jm.emitFinishedJob(run, terminal)
				jm.runAfterDurableFinish(run, terminal, run.afterDurableFinish)
				err = jm.armFinalizedJob(run, terminal)
			}
		} else {
			err = jm.finalizeKeptSync(run, "", "", nil)
		}
	}

	attempt.err = err
	close(attempt.done)

	jm.mu.Lock()
	if jm.running[jobID] == run && run.finalize == attempt {
		run.finalize = nil
	}
	jm.mu.Unlock()

	return err
}

func (jm *jobManager) finishJob(run *runningJob, status jobstore.Status, reason string, exitCode *int) error {
	terminal, err := jm.writeFinishJob(run, status, reason, exitCode)
	if err != nil {
		return err
	}
	return jm.armFinalizedJob(run, terminal)
}

// writeFinishJob writes EventJobFinished and updates the in-memory record but
// does not arm owner notification. It is split out so finalizeKeptSync can
// finalize without notification-arming (spec §6.4d).
func (jm *jobManager) writeFinishJob(run *runningJob, status jobstore.Status, reason string, exitCode *int) (*terminalJob, error) {
	var outputBytes int64
	if _, total, _, err := run.output.Tail(0); err == nil {
		outputBytes = total
	}
	jm.mu.Lock()
	resultSchema := delegateResultSchema(run.rec)
	structured, structuredValid, structuredReason := boundedStructuredResult(run.structured, resultSchema, run.structuredCaptureFailed)
	afterDurableFinish := run.afterDurableFinish
	jm.mu.Unlock()

	endedAt := jm.now()
	terminal := &terminalJob{
		status:      status,
		reason:      reason,
		exitCode:    exitCode,
		endedAt:     endedAt,
		outputBytes: outputBytes,
		generation:  jobstore.NewTerminalGeneration(),
	}
	finished := jobstore.Event{
		Kind:                   jobstore.EventJobFinished,
		TS:                     endedAt,
		JobID:                  run.rec.JobID,
		Status:                 terminal.status,
		Reason:                 terminal.reason,
		ExitCode:               terminal.exitCode,
		EndedAt:                &endedAt,
		OutputBytes:            terminal.outputBytes,
		StructuredResult:       structured,
		StructuredResultValid:  structuredValid,
		StructuredResultReason: structuredReason,
		TerminalGen:            terminal.generation,
		Provenance:             provenance.Clone(run.rec.Provenance),
	}
	terminal.finished = finished
	if err := jm.appendEvent(finished); err != nil {
		return nil, err
	}

	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run {
		run.terminal = terminal
		run.rec.Status = terminal.status
		run.rec.Reason = terminal.reason
		run.rec.ExitCode = terminal.exitCode
		run.rec.EndedAt = &terminal.endedAt
		run.rec.OutputBytes = terminal.outputBytes
		run.rec.StructuredResult = structured
		run.rec.StructuredResultValid = structuredValid
		run.rec.StructuredResultReason = structuredReason
		run.rec.TerminalGen = terminal.generation
	}
	jm.mu.Unlock()

	if err := jm.forwardFinishedJob(run, terminal); err != nil {
		return nil, err
	}
	jm.emitFinishedJob(run, terminal)
	jm.runAfterDurableFinish(run, terminal, afterDurableFinish)
	return terminal, nil
}

func (jm *jobManager) forwardFinishedJob(run *runningJob, terminal *terminalJob) error {
	if terminal == nil || terminal.finishedForwarded {
		return nil
	}
	if jm.forwardDisabled(run) {
		return nil
	}
	if err := jm.forwardSnapshot(terminal.finished); err != nil {
		return err
	}
	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run && run.terminal == terminal {
		terminal.finishedForwarded = true
	}
	jm.mu.Unlock()
	return nil
}

func (jm *jobManager) emitFinishedJob(run *runningJob, terminal *terminalJob) {
	if terminal == nil || terminal.finishedEmitted {
		return
	}
	jm.emitJobFinished(terminal.finished, run)
	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run && run.terminal == terminal {
		terminal.finishedEmitted = true
	}
	jm.mu.Unlock()
}

func (jm *jobManager) runAfterDurableFinish(run *runningJob, terminal *terminalJob, callback func()) {
	if terminal == nil || terminal.afterDurableFinishCalled {
		return
	}
	if callback == nil {
		jm.mu.Lock()
		if jm.running[run.rec.JobID] == run && run.terminal == terminal {
			terminal.afterDurableFinishCalled = true
		}
		jm.mu.Unlock()
		return
	}
	callback()
	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run && run.terminal == terminal {
		terminal.afterDurableFinishCalled = true
	}
	jm.mu.Unlock()
}

func (jm *jobManager) appendStartForwardFailure(jobID string, output *jobstore.OutputStore, p *provenance.Causal) error {
	var outputBytes int64
	if output != nil {
		if _, total, _, err := output.Tail(0); err == nil {
			outputBytes = total
		}
	}
	endedAt := jm.now()
	return jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusFailed,
		Reason:      "forward_failed",
		EndedAt:     &endedAt,
		OutputBytes: outputBytes,
		TerminalGen: jobstore.NewTerminalGeneration(),
		Provenance:  provenance.Clone(p),
	})
}

func boundedStructuredResult(value any, resultSchema any, captureFailed bool) (any, *bool, string) {
	schemaRequested := resultSchema != nil
	if value == nil {
		if captureFailed {
			valid := false
			return nil, &valid, structuredResultReasonSchemaCaptureFailed
		}
		if schemaRequested {
			valid := false
			return nil, &valid, structuredResultReasonSchemaResultMissing
		}
		return nil, nil, ""
	}
	valid := true
	b, err := json.Marshal(value)
	if err != nil || len(b) > maxPersistedStructuredResultJSONBytes {
		valid = false
		reason := structuredResultReasonSchemaCaptureFailed
		if err == nil {
			reason = structuredResultReasonSchemaResultTooLarge
		}
		return nil, &valid, reason
	}
	if schemaRequested {
		if err := validateStructuredResult(value, resultSchema); err != nil {
			valid = false
			return nil, &valid, structuredResultReasonSchemaValidationFailed
		}
	}
	return value, &valid, ""
}

func validateStructuredResult(value any, resultSchema any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("schema validation panicked: %v", r)
		}
	}()

	b, err := json.Marshal(resultSchema)
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	const schemaURI = "urn:serf:delegate-result-schema"
	if err := c.AddResource(schemaURI, bytes.NewReader(b)); err != nil {
		return err
	}
	compiled, err := c.Compile(schemaURI)
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

func (jm *jobManager) armFinalizedJob(run *runningJob, terminal *terminalJob) error {
	jm.mu.Lock()
	if jm.running[run.rec.JobID] != run || run.terminal != terminal {
		jm.mu.Unlock()
		return nil
	}
	pendingAppended := terminal.notificationPendingAppended
	suppressOwnerNotification := !pendingAppended && run.fromWatch.Load() && run.callerCallbackDelivered.Load()
	var watchNotifications []jobNotification
	var watchDeliveries []watchSendDelivery
	var watchRegistryEvents []jobstore.Event
	var expiredWatches []expiredJobWatch
	var watchRootProvenance *provenance.Causal
	if !pendingAppended {
		watchRegistryEvents, expiredWatches, watchRootProvenance = jm.expireJobWatchesLocked(run.rec.JobID)
	}

	if !pendingAppended {
		if err := jm.appendWatchRegistryEvents(watchRegistryEvents); err != nil {
			jm.mu.Unlock()
			return err
		}
		watchNotifications, watchDeliveries = jm.completeExpiredJobWatchesLocked(run.rec.JobID, expiredWatches, watchRootProvenance)
		jm.mu.Unlock()
		jm.enqueueWatchNotifications(watchNotifications)
		jm.recordWatchSendsAndKick(watchDeliveries)
	} else {
		jm.mu.Unlock()
	}

	if !pendingAppended && !suppressOwnerNotification {
		pending := jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          terminal.endedAt,
			JobID:       run.rec.JobID,
			TerminalGen: terminal.generation,
			Provenance:  provenance.Clone(run.rec.Provenance),
		}
		if err := jm.appendEvent(pending); err != nil {
			return err
		}
		jm.mu.Lock()
		if jm.running[run.rec.JobID] == run && run.terminal == terminal {
			terminal.notificationPending = pending
			terminal.notificationPendingAppended = true
		}
		jm.mu.Unlock()
	}

	if !suppressOwnerNotification {
		if err := jm.forwardPendingJobNotification(run, terminal); err != nil {
			return err
		}
	}

	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run {
		delete(jm.running, run.rec.JobID)
		run.treeSlot.release()
	} else {
		jm.mu.Unlock()
		return nil
	}
	jm.mu.Unlock()

	run.stopWatchdog()
	_ = run.output.Close()

	// Enqueue the terminal owner notification BEFORE closing done: anything that
	// wakes on done (blocked reads, boundary checks) must find the notification
	// already queued, or a notification turn driven right after the wake drains
	// an empty queue and the delivery slips a boundary.
	if jm.enqueue != nil && !suppressOwnerNotification {
		jm.enqueue(jobNotification{
			JobID:         run.rec.JobID,
			JobType:       string(run.rec.Type),
			Status:        string(terminal.status),
			Reason:        terminal.reason,
			TranscriptRef: run.rec.TranscriptRef,
			OutputBytes:   terminal.outputBytes,
			ExitCode:      terminal.exitCode,
			Provenance:    provenance.Clone(run.rec.Provenance),
		})
	}
	run.closeDone()
	return nil
}

func (jm *jobManager) markWatchOriginCallerCallbackDelivered(jobID string) {
	if jm == nil || jobID == "" {
		return
	}
	jm.mu.Lock()
	run := jm.running[jobID]
	if run != nil && run.fromWatch.Load() {
		run.callerCallbackDelivered.Store(true)
	}
	jm.mu.Unlock()
}

func (jm *jobManager) forwardPendingJobNotification(run *runningJob, terminal *terminalJob) error {
	if terminal == nil || terminal.notificationPendingForwarded || !terminal.notificationPendingAppended {
		return nil
	}
	if jm.forwardDisabled(run) {
		return nil
	}
	if err := jm.forwardSnapshot(terminal.notificationPending); err != nil {
		return err
	}
	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run && run.terminal == terminal {
		terminal.notificationPendingForwarded = true
	}
	jm.mu.Unlock()
	return nil
}

func (jm *jobManager) forwardDisabled(run *runningJob) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return run != nil && run.forwardDisabled
}

func (jm *jobManager) armPendingTerminalNotifications() error {
	recs, err := jm.store.Load()
	if err != nil {
		return err
	}

	jobs := make([]*jobstore.JobRecord, 0, len(recs))
	for _, rec := range recs {
		// Restore re-arm filters to OWNED records (spec §3 settle). A forwarded
		// copy of a direct child's own job (OwnerSessionID is the child, not this
		// session) is a DRIVE SIGNAL, not the parent's render: re-arming it onto
		// the parent's rail would wake-storm the parent about jobs it does not own
		// at every restart. The child's own ledger re-arms in the child's own
		// restore and renders in the child's own turn; the parent drives it.
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
			continue
		}
		if rec.Status.IsTerminal() && rec.TerminalGen != "" &&
			(rec.NotifyState == jobstore.NotifyNotArmed || rec.NotifyState == jobstore.NotifyPending) {
			jobs = append(jobs, rec)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].JobID < jobs[j].JobID
		}
		return jobs[i].StartedAt.After(jobs[j].StartedAt)
	})

	for _, rec := range jobs {
		pending := jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          jm.now(),
			JobID:       rec.JobID,
			TerminalGen: rec.TerminalGen,
			Provenance:  provenance.Clone(rec.Provenance),
		}
		if rec.NotifyState == jobstore.NotifyNotArmed {
			if err := jm.appendEvent(pending); err != nil {
				return err
			}
		}
		if jm.enqueue != nil {
			jm.enqueue(jobNotification{
				JobID:         rec.JobID,
				JobType:       string(rec.Type),
				Status:        string(rec.Status),
				Reason:        rec.Reason,
				TranscriptRef: rec.TranscriptRef,
				OutputBytes:   rec.OutputBytes,
				ExitCode:      rec.ExitCode,
				Provenance:    provenance.Clone(rec.Provenance),
			})
		}
	}
	return nil
}

func tailOutput(output *jobstore.OutputStore, tailBytes int) (string, int64, bool, error) {
	b, total, truncated, err := output.Tail(tailBytes)
	if err != nil {
		return "", total, truncated, err
	}
	return string(b), total, truncated, nil
}

func headOutput(output *jobstore.OutputStore, headBytes int) (string, int64, bool, error) {
	b, total, truncated, err := output.Head(headBytes)
	if err != nil {
		return "", total, truncated, err
	}
	return string(b), total, truncated, nil
}

func validatedOutputStatsForRecord(path string, rec *jobstore.JobRecord) (total int64, retainedStart int64, err error) {
	total, retainedStart, err = jobstore.OutputFileStats(path)
	if err != nil {
		return 0, 0, err
	}
	if rec != nil && rec.Status.IsTerminal() && rec.OutputBytes != total {
		return 0, 0, fmt.Errorf("jobstore: output metadata total %d does not match job record total %d", total, rec.OutputBytes)
	}
	return total, retainedStart, nil
}

func tailOutputFile(path string, tailBytes int, total int64) (output string, totalBytes int64, truncated bool, err error) {
	if tailBytes < 0 {
		return "", 0, false, fmt.Errorf("%w: maxBytes=%d", jobstore.ErrInvalidLimit, tailBytes)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", 0, false, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return "", 0, false, fmt.Errorf("jobstore: stat output: %w", err)
	}
	retained := info.Size()
	totalBytes = total
	start := int64(0)
	if retained > int64(tailBytes) {
		start = retained - int64(tailBytes)
		truncated = true
	}
	if totalBytes > retained {
		truncated = true
	}
	if _, err := f.Seek(start, 0); err != nil {
		return "", totalBytes, truncated, err
	}
	buf := make([]byte, retained-start)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return "", totalBytes, truncated, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	return string(buf), totalBytes, truncated, nil
}

func headOutputFile(path string, headBytes int, total int64) (output string, totalBytes int64, truncated bool, err error) {
	if headBytes < 0 {
		return "", 0, false, fmt.Errorf("%w: maxBytes=%d", jobstore.ErrInvalidLimit, headBytes)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", 0, false, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return "", 0, false, fmt.Errorf("jobstore: stat output: %w", err)
	}
	retained := info.Size()
	totalBytes = total
	n := retained
	if n > int64(headBytes) {
		n = int64(headBytes)
		truncated = true
	}
	if totalBytes > retained {
		truncated = true
	}
	buf := make([]byte, n)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return "", totalBytes, truncated, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	return string(buf), totalBytes, truncated, nil
}

func grepOutputFile(path string, re *regexp.Regexp, limitBytes int, retainedStart int64) (matches []jobstore.Match, err error) {
	return jobstore.GrepFileLimitAt(path, re, limitBytes, maxJobGrepMatches, maxJobGrepLineBytes, retainedStart)
}

func cloneJobRecord(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil {
		return nil
	}
	clone := *rec
	return &clone
}
