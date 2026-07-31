package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/rendezvous"
)

// jobsListSource is a minimal appsource.Source test double that stubs the
// live path's ListJobs/JobOutput responses; every other (large) Source method
// falls through to the embedded relayLifecycleSource's stub implementation
// (app_rpc_test.go), mirroring taskListSource's existing override pattern.
type jobsListSource struct {
	relayLifecycleSource
	id       string
	jobsResp appwire.JobsListResponse
	jobsErr  error
	outResp  appwire.JobsOutputResponse
	outErr   error
}

func (s *jobsListSource) ID() string { return s.id }

func (s *jobsListSource) ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return s.jobsResp, s.jobsErr
}

func (s *jobsListSource) JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return s.outResp, s.outErr
}

// persistedJobFixture describes one durable job for seedPastSessionWithJobs:
// a completed shell job in jobs.jsonl, plus — when output is non-empty — the
// job's output log at the default <jobs>/<id>.log path with output_bytes
// recorded to match (validatedOutputStatsForRecord rejects a terminal record
// whose output_bytes disagrees with the output file's size).
type persistedJobFixture struct {
	id          string
	description string
	command     string
	output      string
}

// seedPastSessionWithJobs builds a past-indexed session (project state dir +
// meta.json, mirroring seedPastSessionWithTasks in app_tasks_test.go). When
// jobs is non-nil, it also writes the session's durable jobs.jsonl as raw
// JSONL event lines in jobstore.Event's wire shape — the same hand-written
// fixture convention writeHistoricalJobLog (app_threadread_test.go) already
// uses, since cmd/serf-hub cannot import agent/internal/jobstore.
func seedPastSessionWithJobs(t *testing.T, jobs []persistedJobFixture) (hubcore.WebConfig, string, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-jobs-0000000000")
	sessionDir := filepath.Join(stateDir, "sessions", "")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5",
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/project"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if jobs != nil {
		writePersistedJobsLog(t, stateDir, sessionID, now, jobs)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{Past: idx}, sessionID, stateDir
}

// writePersistedJobsLog writes one session's jobs.jsonl (job_started +
// job_finished per fixture entry, in order) and each fixture entry's output
// log. Seq is assigned in append order exactly as jobstore.Store.Append
// would, so the fold order the readers reconstruct matches a real store's.
func writePersistedJobsLog(t *testing.T, stateDir, sessionID string, now time.Time, jobs []persistedJobFixture) {
	t.Helper()
	dir := filepath.Join(stateDir, "sessions", sessionID)
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	seq := 0
	appendLine := func(v map[string]any) {
		seq++
		v["seq"] = seq
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	for i, job := range jobs {
		started := now.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano)
		ended := now.Add(time.Duration(i)*time.Second + time.Millisecond*500).UTC().Format(time.RFC3339Nano)
		appendLine(map[string]any{
			"kind":                  "job_started",
			"ts":                    started,
			"job_id":                job.id,
			"type":                  "shell",
			"status":                "running",
			"description":           job.description,
			"command":               job.command,
			"owner_session_id":      sessionID,
			"visible_to_session_id": sessionID,
			"started_at":            started,
		})
		finished := map[string]any{
			"kind":     "job_finished",
			"ts":       ended,
			"job_id":   job.id,
			"status":   "completed",
			"reason":   "exit_zero",
			"ended_at": ended,
		}
		if job.output != "" {
			outPath := filepath.Join(dir, "jobs", job.id+".log")
			if err := os.WriteFile(outPath, []byte(job.output), 0o644); err != nil {
				t.Fatal(err)
			}
			finished["output_bytes"] = int64(len(job.output))
		}
		appendLine(finished)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHubJobsListLiveDaemon proves a running daemon's jobstore is
// authoritative: even though a past index entry (with its own, different,
// persisted job) exists for the same session, a successful live ListJobs
// response is passed through untouched and past is never consulted.
func TestHubJobsListLiveDaemon(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_stale", description: "stale past job", command: "make stale"},
	})
	liveJobs := []agent.JobSummary{{JobID: "job_live", Type: "shell", Status: "running", Description: "live job", StartedAt: "2026-07-31T12:00:00Z"}}
	sources := appsource.NewRegistry()
	sources.Add(&jobsListSource{id: "local", jobsResp: appwire.JobsListResponse{Data: liveJobs}})

	resp, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubJobsList: %v", err)
	}
	jobs, ok := resp.Data.([]agent.JobSummary)
	if !ok || len(jobs) != 1 || jobs[0].JobID != "job_live" {
		t.Fatalf("resp.Data = %#v, want the live job (past must not be consulted)", resp.Data)
	}
}

// TestHubJobsListDeadSessionFallsBackToPast is the RED case: a session whose
// daemon has exited (no live rendezvous entry) must still serve its real
// persisted jobs from <StateDir>/sessions/<id>/jobs.jsonl, not the
// SessionUnavailable error entryForRef raises for a live-only lookup.
func TestHubJobsListDeadSessionFallsBackToPast(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_a", description: "run the build", command: "make build", output: "build ok\n"},
	})
	sources := newExitedLocalRegistry()

	resp, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubJobsList: %v", err)
	}
	jobs, ok := resp.Data.([]agent.JobSummary)
	if !ok {
		t.Fatalf("resp.Data = %#v (%T), want []agent.JobSummary", resp.Data, resp.Data)
	}
	if len(jobs) != 1 || jobs[0].JobID != "job_a" || jobs[0].Description != "run the build" {
		t.Fatalf("jobs = %+v, want one job %q", jobs, "run the build")
	}
	if jobs[0].Status != "completed" || !jobs[0].HasOutput || jobs[0].OutputBytes != int64(len("build ok\n")) {
		t.Fatalf("jobs[0] = %+v, want the completed fixture with its output bookkeeping", jobs[0])
	}
}

// TestHubJobsListDeadSessionNotInPastIndex proves a ref the past index has
// never heard of still surfaces the ORIGINAL SessionUnavailable
// thread-not-found error: the past path never serves an empty list for a
// session the hub cannot otherwise account for.
func TestHubJobsListDeadSessionNotInPastIndex(t *testing.T) {
	root := t.TempDir()
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: idx}
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	sources := newExitedLocalRegistry()

	_, err = hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if !isDeadSessionError(err) {
		t.Fatalf("err = %v, want entryForRef's thread-not-found SessionUnavailable error, not an empty list", err)
	}
}

// TestHubJobsListLiveErrorPropagates reproduces the real risk class from
// TestHubTasksList_LiveDialFailureIsNotMaskedByPast: a LIVE rendezvous entry
// whose endpoint is unreachable. The dial failure maps to a
// SessionUnavailable-SHAPED error ("local daemon unavailable: ...") that is
// NOT the dead-session condition; hubJobsList must propagate it rather than
// silently serving the stale past-index job list.
func TestHubJobsListLiveErrorPropagates(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_stale", description: "stale past job", command: "make stale"},
	})
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: rendezvous.Entry{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:1/rpc", // reserved port: dial fails ECONNREFUSED
			ThreadID:  sessionID,
			SessionID: sessionID,
		}}}
	}, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := hubJobsList(ctx, cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if err == nil {
		t.Fatal("hubJobsList returned nil error, want the dial failure (a live entry exists; the daemon has not exited)")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || !strings.Contains(wire.Message, "local daemon unavailable") {
		t.Fatalf("err = %v, want localDaemonDialError's \"local daemon unavailable\" SessionUnavailable (sanity check the reproduction hit the dial path, not something else)", err)
	}
	if isDeadSessionError(err) {
		t.Fatalf("err = %v misclassified as the dead-session condition; it is a dial failure against a LIVE entry", err)
	}
}

// TestHubJobsOutputDeadSessionFallsBackToPast proves the exited-session
// fallback reads the persisted job's output tail through
// agent.LoadSessionJobOutputTail: the last MaxBytes of the durable output
// file, with the total/truncation bookkeeping intact.
func TestHubJobsOutputDeadSessionFallsBackToPast(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "noisy build", command: "make noisy", output: "0123456789"},
	})
	sources := newExitedLocalRegistry()

	resp, err := hubJobsOutput(context.Background(), cfg, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if err != nil {
		t.Fatalf("hubJobsOutput: %v", err)
	}
	tail, ok := resp.Data.(agent.JobOutputTail)
	if !ok {
		t.Fatalf("resp.Data = %#v (%T), want agent.JobOutputTail", resp.Data, resp.Data)
	}
	if tail.Tail != "6789" || tail.TotalBytes != 10 || !tail.Truncated {
		t.Fatalf("tail = %+v, want the last 4 of 10 bytes, truncated", tail)
	}
}

// TestHubJobsOutputPastUnknownJob proves a job id absent from the persisted
// store is invalid params — the caller guessed — not an empty tail and not
// the dead-session error that triggered the fallback.
func TestHubJobsOutputPastUnknownJob(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "noisy build", command: "make noisy", output: "0123456789"},
	})
	sources := newExitedLocalRegistry()

	_, err := hubJobsOutput(context.Background(), cfg, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_nope", MaxBytes: 4})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("err = %v, want InvalidParams for a job id the persisted store has never heard of", err)
	}
	if want := fmt.Sprintf("job not found: %s", "job_nope"); wire.Message != want {
		t.Fatalf("err message = %q, want %q", wire.Message, want)
	}
}
