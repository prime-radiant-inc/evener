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
	mu        sync.Mutex
	dir       string
	sessionID string
	store     *jobstore.Store
	running   map[string]*runningJob
	enqueue   func(jobNotification)
	now       func() time.Time
}

type runningJob struct {
	rec    *jobstore.JobRecord
	output *jobstore.OutputStore
	signal func()
	done   chan struct{}
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
		dir:       dir,
		sessionID: sessionID,
		store:     store,
		running:   make(map[string]*runningJob),
		enqueue:   enqueue,
		now:       time.Now,
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
	if err := jm.store.Append(jobstore.Event{
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
		return nil, err
	}
	output, err := jobstore.OpenOutput(outputPath, 0)
	if err != nil {
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
	recs, err := jm.store.Load()
	if err != nil {
		return nil
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
	return jobs
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

func (jm *jobManager) finalize(jobID string, status jobstore.Status, reason string, exitCode *int) {
	jm.mu.Lock()
	run := jm.running[jobID]
	if run != nil {
		delete(jm.running, jobID)
	}
	jm.mu.Unlock()
	if run == nil {
		return
	}

	var outputBytes int64
	if _, total, _, err := run.output.Tail(0); err == nil {
		outputBytes = total
	}
	_ = run.output.Close()

	endedAt := jm.now()
	terminalGen := jobstore.NewTerminalGeneration()
	_ = jm.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      status,
		Reason:      reason,
		ExitCode:    exitCode,
		EndedAt:     &endedAt,
		OutputBytes: outputBytes,
		TerminalGen: terminalGen,
	})
	close(run.done)

	_ = jm.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          endedAt,
		JobID:       jobID,
		TerminalGen: terminalGen,
	})
	if jm.enqueue != nil {
		jm.enqueue(jobNotification{
			JobID:         jobID,
			JobType:       string(run.rec.Type),
			Status:        string(status),
			Reason:        reason,
			TranscriptRef: run.rec.TranscriptRef,
			OutputBytes:   outputBytes,
			ExitCode:      exitCode,
		})
	}
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
