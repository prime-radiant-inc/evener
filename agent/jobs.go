package agent

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

type jobManager struct {
	mu          sync.Mutex
	dir         string
	sessionID   string
	store       *jobstore.Store
	running     map[string]*runningJob
	appendEvent func(jobstore.Event) error
	enqueue     func(jobNotification)
	now         func() time.Time
}

type runningJob struct {
	rec            *jobstore.JobRecord
	output         *jobstore.OutputStore
	signal         func()
	done           chan struct{}
	durableStarted bool
	stopStatus     jobstore.Status
	stopReason     string
	terminal       *terminalJob
	finalize       *finalizeAttempt
}

type finalizeAttempt struct {
	done chan struct{}
	err  error
}

type terminalJob struct {
	status      jobstore.Status
	reason      string
	exitCode    *int
	endedAt     time.Time
	outputBytes int64
	generation  string
}

type jobRuntimeHandle struct {
	jobID  string
	signal func()
	done   <-chan struct{}
	output *jobstore.OutputStore
}

// jobNotification is the durable-job analogue of subagentNotification.
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
	Status   jobstore.Status
	Statuses []jobstore.Status
	Type     jobstore.JobType
	Types    []jobstore.JobType
	Limit    int
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
	return &jobManager{
		dir:         dir,
		sessionID:   sessionID,
		store:       store,
		running:     make(map[string]*runningJob),
		appendEvent: store.Append,
		enqueue:     enqueue,
		now:         time.Now,
	}, nil
}

func (jm *jobManager) close() error {
	jm.mu.Lock()
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
	if err := jm.store.Close(); err != nil {
		if waitErr != nil {
			return fmt.Errorf("%w; close store: %w", waitErr, err)
		}
		return err
	}
	return waitErr
}

func (jm *jobManager) abandonRunningJobs() {
	jm.mu.Lock()
	running := make([]jobRuntimeHandle, 0, len(jm.running))
	for _, run := range jm.running {
		running = append(running, jobRuntimeHandle{
			jobID:  run.rec.JobID,
			output: run.output,
		})
		delete(jm.running, run.rec.JobID)
	}
	jm.mu.Unlock()
	for _, run := range running {
		if run.output != nil {
			_ = run.output.Close()
		}
	}
}

func (jm *jobManager) createShell(opts createShellOpts) (*jobstore.JobRecord, error) {
	startedAt := jm.now()
	jobID := jobstore.NewJobID()
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	rec := &jobstore.JobRecord{
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Status:           jobstore.StatusRunning,
		Command:          opts.Command,
		Description:      opts.Description,
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		StartedAt:        startedAt,
		OutputPath:       outputPath,
	}
	output, err := jobstore.OpenOutput(outputPath, 0)
	if err != nil {
		return nil, err
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            rec.JobID,
		Type:             rec.Type,
		Command:          rec.Command,
		Description:      rec.Description,
		OwnerSessionID:   rec.OwnerSessionID,
		VisibleToSession: rec.VisibleToSession,
		StartedAt:        &startedAt,
	}); err != nil {
		_ = output.Close()
		return nil, err
	}

	jm.mu.Lock()
	jm.running[jobID] = &runningJob{
		rec:            rec,
		output:         output,
		signal:         func() {},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	jm.mu.Unlock()
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
	return tailOutputFile(jm.outputPathForJob(rec, jobID), tailBytes)
}

func (jm *jobManager) grepOutput(jobID string, re *regexp.Regexp, limitBytes int) ([]jobstore.Match, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		return run.output.GrepLimit(re, limitBytes, maxJobGrepMatches)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return nil, err
	}
	rec := recs[jobID]
	if rec == nil {
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	return grepOutputFile(jm.outputPathForJob(rec, jobID), re, limitBytes)
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

	for _, finished := range jobstore.Reconcile(recs, live, jm.now()) {
		rec := recs[finished.JobID]
		if info, err := os.Stat(jm.outputPathForJob(rec, finished.JobID)); err == nil && !info.IsDir() {
			finished.OutputBytes = info.Size()
		}
		if err := jm.appendEvent(finished); err != nil {
			return err
		}
		if err := jm.appendEvent(jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          finished.TS,
			JobID:       finished.JobID,
			TerminalGen: finished.TerminalGen,
		}); err != nil {
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
		run.stopStatus = jobstore.StatusStopped
		run.stopReason = "stopped"
		signal := run.signal
		rec := cloneJobRecord(run.rec)
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
		err = jm.finishJob(run, status, reason, exitCode)
	} else {
		err = jm.armFinalizedJob(run, terminal)
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

	endedAt := jm.now()
	terminal := &terminalJob{
		status:      status,
		reason:      reason,
		exitCode:    exitCode,
		endedAt:     endedAt,
		outputBytes: outputBytes,
		generation:  jobstore.NewTerminalGeneration(),
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       run.rec.JobID,
		Status:      terminal.status,
		Reason:      terminal.reason,
		ExitCode:    terminal.exitCode,
		EndedAt:     &endedAt,
		OutputBytes: terminal.outputBytes,
		TerminalGen: terminal.generation,
	}); err != nil {
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
		run.rec.TerminalGen = terminal.generation
	}
	jm.mu.Unlock()

	return jm.armFinalizedJob(run, terminal)
}

func (jm *jobManager) armFinalizedJob(run *runningJob, terminal *terminalJob) error {
	jm.mu.Lock()
	if jm.running[run.rec.JobID] != run || run.terminal != terminal {
		jm.mu.Unlock()
		return nil
	}
	jm.mu.Unlock()

	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          terminal.endedAt,
		JobID:       run.rec.JobID,
		TerminalGen: terminal.generation,
	}); err != nil {
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
		if rec.NotifyState == jobstore.NotifyNotArmed {
			if err := jm.appendEvent(jobstore.Event{
				Kind:        jobstore.EventJobNotificationPending,
				TS:          jm.now(),
				JobID:       rec.JobID,
				TerminalGen: rec.TerminalGen,
			}); err != nil {
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

func tailOutputFile(path string, tailBytes int) (output string, total int64, truncated bool, err error) {
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
	total = info.Size()
	start := int64(0)
	if total > int64(tailBytes) {
		start = total - int64(tailBytes)
		truncated = true
	}
	if _, err := f.Seek(start, 0); err != nil {
		return "", total, truncated, err
	}
	buf := make([]byte, total-start)
	if len(buf) > 0 {
		if _, err := io.ReadFull(f, buf); err != nil {
			return "", total, truncated, fmt.Errorf("jobstore: read output: %w", err)
		}
	}
	return string(buf), total, truncated, nil
}

func grepOutputFile(path string, re *regexp.Regexp, limitBytes int) (matches []jobstore.Match, err error) {
	if limitBytes < 0 {
		return nil, fmt.Errorf("%w: limitBytes=%d", jobstore.ErrInvalidLimit, limitBytes)
	}
	if limitBytes == 0 {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output: %w", closeErr)
		}
	}()

	var offset int64
	budget := limitBytes
	r := bufio.NewReader(f)
	for {
		rawLine, err := r.ReadString('\n')
		if len(rawLine) > 0 {
			line := rawLine
			if line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}
			}
			if re.MatchString(line) {
				if len(line) > budget {
					break
				}
				matches = append(matches, jobstore.Match{ByteOffset: offset, Line: line})
				if len(matches) >= maxJobGrepMatches {
					break
				}
				budget -= len(line)
				if budget <= 0 {
					break
				}
			}
			offset += int64(len(rawLine))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("jobstore: read output line: %w", err)
		}
	}
	return matches, nil
}

func cloneJobRecord(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil {
		return nil
	}
	clone := *rec
	return &clone
}
