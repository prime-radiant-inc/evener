package agent

import (
	"fmt"
	"os"
	"path/filepath"
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
	rec        *jobstore.JobRecord
	output     *jobstore.OutputStore
	signal     func()
	done       chan struct{}
	finalizing bool
	terminal   *terminalJob
	finalize   *finalizeAttempt
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
	arming      bool
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
	Status jobstore.Status
	Type   jobstore.JobType
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
		rec:    rec,
		output: output,
		signal: func() {},
		done:   make(chan struct{}),
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
		recs[jobID] = cloneJobRecord(run.rec)
	}
	jm.mu.Unlock()

	jobs := make([]*jobstore.JobRecord, 0, len(recs))
	for _, rec := range recs {
		if filter.Status != "" && rec.Status != filter.Status {
			continue
		}
		if filter.Type != "" && rec.Type != filter.Type {
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
	return jobs, nil
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
	outputPath := rec.OutputPath
	if outputPath == "" {
		outputPath = filepath.Join(jm.dir, "jobs", jobID+".log")
	}
	if _, err := os.Stat(outputPath); err != nil {
		return "", 0, false, fmt.Errorf("job %q output unavailable: %w", jobID, err)
	}
	output, err := jobstore.OpenOutput(outputPath, 0)
	if err != nil {
		return "", 0, false, err
	}
	defer output.Close()
	return tailOutput(output, tailBytes)
}

func (jm *jobManager) stop(jobID string) (*jobstore.JobRecord, error) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		if run.signal != nil {
			run.signal()
		}
		return cloneJobRecord(run.rec), nil
	}

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
	if terminal == nil {
		run.finalizing = true
	}
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
		jm.mu.Lock()
		if jm.running[run.rec.JobID] == run {
			run.finalizing = false
		}
		jm.mu.Unlock()
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
	if terminal.arming {
		jm.mu.Unlock()
		return nil
	}
	terminal.arming = true
	jm.mu.Unlock()

	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          terminal.endedAt,
		JobID:       run.rec.JobID,
		TerminalGen: terminal.generation,
	}); err != nil {
		jm.mu.Lock()
		if jm.running[run.rec.JobID] == run && run.terminal == terminal {
			terminal.arming = false
		}
		jm.mu.Unlock()
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
		if rec.Status.IsTerminal() && rec.NotifyState == jobstore.NotifyNotArmed && rec.TerminalGen != "" {
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
		if err := jm.appendEvent(jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          jm.now(),
			JobID:       rec.JobID,
			TerminalGen: rec.TerminalGen,
		}); err != nil {
			return err
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

func cloneJobRecord(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil {
		return nil
	}
	clone := *rec
	return &clone
}
