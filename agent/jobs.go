package agent

import (
	"bytes"
	"context"
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
	sessionID     string
	store         *jobstore.Store
	running       map[string]*runningJob
	watches       map[watchKey]*watchConfig
	terminalFlush map[*watchConfig]bool
	closing       bool
	appendEvent   func(jobstore.Event) error
	emit          func(events.EventKind, events.EventData)
	forward       func(jobstore.Event) error
	parentJobID   string
	enqueue       func(jobNotification)
	send          func(context.Context, sendMessageArgs) sendMessageResult
	now           func() time.Time
}

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

type runningJob struct {
	rec                     *jobstore.JobRecord
	output                  *jobstore.OutputStore
	signal                  func()
	done                    chan struct{}
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
	forwardDisabled         bool
}

type finalizeAttempt struct {
	done chan struct{}
	err  error
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
	jobID  string
	signal func()
	done   chan struct{}
	output *jobstore.OutputStore
}

// jobNotification is the in-memory wake record for a durable job notification.
type jobNotification struct {
	JobID, JobType, Status, Reason, TranscriptRef string
	OutputBytes                                   int64
	ExitCode                                      *int
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
		dir:         dir,
		sessionID:   sessionID,
		store:       store,
		running:     make(map[string]*runningJob),
		watches:     make(map[watchKey]*watchConfig),
		appendEvent: store.Append,
		enqueue:     enqueue,
		now:         time.Now,
	}
	if err := jm.restoreWatchSendPending(); err != nil {
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
			key:      key,
			cfg:      cfg,
			terminal: watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "job manager closed", jm.now()),
		})
	}
	markWatchConfigSnapshotsRejectingLocked(targets)
	dropped := terminalSnapshots(targets)
	for cfg := range jm.terminalFlush {
		dropped = append(dropped, watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "job manager closed", jm.now()))
	}
	running := make([]jobRuntimeHandle, 0, len(jm.running))
	for _, run := range jm.running {
		running = append(running, jobRuntimeHandle{
			jobID:  run.rec.JobID,
			signal: run.signal,
			done:   run.done,
			output: run.output,
		})
	}
	jm.mu.Unlock()
	jm.watchNotifyMu.Unlock()

	applied, err := jm.appendWatchSendTerminalSnapshots(dropped)
	if err != nil {
		jm.removeWatchSendTerminalSnapshots(applied)
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.closeWatchConfigSnapshots(targets)
	} else {
		jm.detachWatchConfigSnapshots(targets)
		jm.removeWatchSendTerminalSnapshots(applied)
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
	deadline := time.NewTimer(5 * time.Second)
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
		running = append(running, jobRuntimeHandle{
			jobID:  run.rec.JobID,
			done:   run.done,
			output: run.output,
		})
		delete(jm.running, run.rec.JobID)
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
		close(run.done)
	}
}

func (jm *jobManager) abandonRunningJob(jobID string) {
	jm.mu.Lock()
	run := jm.running[jobID]
	var targets []watchConfigTerminalSnapshot
	if run != nil {
		delete(jm.running, jobID)
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
	close(run.done)
}

func (jm *jobManager) createShell(opts createShellOpts) (*jobstore.JobRecord, error) {
	startedAt := jm.now()
	jobID := jobstore.NewJobID()
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	parentJobID := jm.currentParentJobID()
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
		OutputPath:       outputPath,
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
	}
	if err := jm.appendEvent(started); err != nil {
		jm.mu.Unlock()
		_ = output.Close()
		return nil, err
	}
	if err := jm.forwardLocked(started); err != nil {
		_ = output.Close()
		if terminalErr := jm.appendStartForwardFailure(rec.JobID, output); terminalErr != nil {
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
		recs[jobID] = cloneJobRecord(run.rec)
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
	})
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
	})
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
		return "", 0, false, fmt.Errorf("job %q not found", jobID)
	}
	path := jm.outputPathForJob(rec, jobID)
	validatedTotal, _, err := validatedOutputStatsForRecord(path, rec)
	if err != nil {
		return "", 0, false, err
	}
	return tailOutputFile(path, tailBytes, validatedTotal)
}

func (jm *jobManager) appendJobOutput(jobID string, output *jobstore.OutputStore, b []byte) (int, error) {
	if output == nil {
		return 0, nil
	}
	n, err := output.Append(b)
	if err == nil && n > 0 {
		jm.feedJobOutput(jobID, b[:n])
	}
	return n, err
}

func (jm *jobManager) grepOutput(jobID string, re *regexp.Regexp, limitBytes int) ([]jobstore.Match, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		return run.output.GrepLimitLineBytes(re, limitBytes, maxJobGrepMatches, maxJobGrepLineBytes)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return nil, err
	}
	rec := recs[jobID]
	if rec == nil {
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	path := jm.outputPathForJob(rec, jobID)
	_, retainedStart, err := validatedOutputStatsForRecord(path, rec)
	if err != nil {
		return nil, err
	}
	return grepOutputFile(path, re, limitBytes, retainedStart)
}

func (jm *jobManager) reconcileLostJobs() error {
	recs, err := jm.store.Load()
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
		if err := jm.appendEvent(finished); err != nil {
			return err
		}
		pending := jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          finished.TS,
			JobID:       finished.JobID,
			TerminalGen: finished.TerminalGen,
		}
		if err := jm.appendEvent(pending); err != nil {
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
			})
		}
	}
	return nil
}

func (jm *jobManager) outputPathForJob(rec *jobstore.JobRecord, jobID string) string {
	if rec != nil && rec.OutputPath != "" {
		return rec.OutputPath
	}
	return filepath.Join(jm.dir, "jobs", jobID+".log")
}

func (jm *jobManager) stop(jobID string) (*jobstore.JobRecord, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	if run != nil {
		run.stopStatus = jobstore.StatusCancelled
		run.stopReason = "stopped_by_parent"
		signal := run.signal
		rec := cloneJobRecord(run.rec)
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
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	return cloneJobRecord(rec), nil
}

func (jm *jobManager) finalize(jobID string, status jobstore.Status, reason string, exitCode *int) error {
	return jm.finalizeWithRun(jobID, func(run *runningJob) (jobstore.Status, string, *int, error) {
		return status, reason, exitCode, nil
	})
}

func (jm *jobManager) finalizeWithRun(jobID string, prepare func(*runningJob) (jobstore.Status, string, *int, error)) error {
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
		} else {
			err = jm.finishJob(run, status, reason, exitCode)
		}
	} else {
		err = jm.forwardFinishedJob(run, terminal)
		if err == nil {
			jm.emitFinishedJob(run, terminal)
			jm.runAfterDurableFinish(run, terminal, run.afterDurableFinish)
			err = jm.armFinalizedJob(run, terminal)
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
	}
	terminal.finished = finished
	if err := jm.appendEvent(finished); err != nil {
		return err
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
		return err
	}
	jm.emitFinishedJob(run, terminal)
	jm.runAfterDurableFinish(run, terminal, afterDurableFinish)
	return jm.armFinalizedJob(run, terminal)
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

func (jm *jobManager) appendStartForwardFailure(jobID string, output *jobstore.OutputStore) error {
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
	var watchNotifications []jobNotification
	var watchDeliveries []watchSendDelivery
	if !pendingAppended {
		watchNotifications, watchDeliveries = jm.expireJobWatchesLocked(run.rec.JobID)
	}
	jm.mu.Unlock()

	if !pendingAppended {
		watchDeliveries = jm.snapshotWatchSendFrames(watchDeliveries)
		jm.enqueueWatchNotifications(watchNotifications)
		if err := jm.deliverWatchSends(context.Background(), watchDeliveries); err != nil {
			return err
		}
		if err := jm.retryPendingWatchSendsForWatchTarget(context.Background(), run.rec.JobID); err != nil {
			return err
		}
		if err := jm.retryPendingWatchSendsForRunTarget(context.Background(), run.rec); err != nil {
			return err
		}
	}

	if !pendingAppended {
		pending := jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          terminal.endedAt,
			JobID:       run.rec.JobID,
			TerminalGen: terminal.generation,
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

	if err := jm.forwardPendingJobNotification(run, terminal); err != nil {
		return err
	}

	jm.mu.Lock()
	if jm.running[run.rec.JobID] == run {
		delete(jm.running, run.rec.JobID)
	} else {
		jm.mu.Unlock()
		return nil
	}
	jm.mu.Unlock()

	_ = run.output.Close()
	close(run.done)

	if jm.enqueue != nil {
		jm.enqueue(jobNotification{
			JobID:         run.rec.JobID,
			JobType:       string(run.rec.Type),
			Status:        string(terminal.status),
			Reason:        terminal.reason,
			TranscriptRef: run.rec.TranscriptRef,
			OutputBytes:   terminal.outputBytes,
			ExitCode:      terminal.exitCode,
		})
	}
	return nil
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
