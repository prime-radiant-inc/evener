package main

import (
	"context"
	"encoding/json"
	"errors"
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

// TestHubJobsListNonLocalRefKeepsTheLiveError proves the past gate's
// local-ref requirement on the list path, the counterpart to
// TestHubJobsOutputNonLocalRefKeepsTheLiveError: jobs.jsonl under a project
// state dir is LOCAL session state, so a non-local ref must never be
// answered from it. The past index deliberately holds a session whose id is
// the codex ref's thread id — dropping the local-source check would serve
// another source's caller this local session's job list.
func TestHubJobsListNonLocalRefKeepsTheLiveError(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_local", description: "local past job", command: "make local"},
	})
	sources := appsource.NewRegistry()
	sources.Add(&jobsListSource{id: "codex", jobsErr: appwire.SessionUnavailable(threadNotFoundMessagePrefix + sessionID)})

	resp, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "codex:" + sessionID})
	if !isDeadSessionError(err) {
		t.Fatalf("hubJobsList = (%#v, %v), want the codex source's own dead-session error; local past state is not this ref's to serve", resp.Data, err)
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

// TestHubJobsOutputLiveDaemon is the output path's counterpart to
// TestHubJobsListLiveDaemon: a running daemon owns the job's live output
// buffer, so a successful live JobOutput response is passed through untouched
// and past is never consulted — even though a past index entry holds its own,
// different, persisted output for the very same job id.
func TestHubJobsOutputLiveDaemon(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "stale past job", command: "make stale", output: "0123456789"},
	})
	liveTail := agent.JobOutputTail{Tail: "live", TotalBytes: 44, RetainedStart: 40, Truncated: true}
	sources := appsource.NewRegistry()
	sources.Add(&jobsListSource{id: "local", outResp: appwire.JobsOutputResponse{Data: liveTail}})

	resp, err := hubJobsOutput(context.Background(), cfg, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if err != nil {
		t.Fatalf("hubJobsOutput: %v", err)
	}
	tail, ok := resp.Data.(agent.JobOutputTail)
	if !ok {
		t.Fatalf("resp.Data = %#v (%T), want agent.JobOutputTail", resp.Data, resp.Data)
	}
	if tail != liveTail {
		t.Fatalf("tail = %+v, want the live tail %+v (past must not be consulted)", tail, liveTail)
	}
}

// TestHubJobsOutputLiveErrorPropagates is the output path's counterpart to
// TestHubJobsListLiveErrorPropagates: a LIVE rendezvous entry whose endpoint
// is unreachable. The dial failure maps to a SessionUnavailable-SHAPED error
// ("local daemon unavailable: ...") that is NOT the dead-session condition;
// hubJobsOutput must propagate it rather than silently serving the stale
// past-index output tail.
func TestHubJobsOutputLiveErrorPropagates(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "stale past job", command: "make stale", output: "0123456789"},
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
	_, err := hubJobsOutput(ctx, cfg, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if err == nil {
		t.Fatal("hubJobsOutput returned nil error, want the dial failure (a live entry exists; the daemon has not exited)")
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
	if want := "job not found: " + "job_nope"; wire.Message != want {
		t.Fatalf("err message = %q, want %q", wire.Message, want)
	}
}

// TestHubJobsOutputWithoutPastIndexKeepsTheLiveError proves the first of
// hubJobsOutput's three past-gate misses: a hub configured without a past
// index has nothing to fall back to, so the dead-session error that triggered
// the fallback is what the caller gets — not an empty tail.
func TestHubJobsOutputWithoutPastIndexKeepsTheLiveError(t *testing.T) {
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	sources := newExitedLocalRegistry()

	_, err = hubJobsOutput(context.Background(), hubcore.WebConfig{}, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if !isDeadSessionError(err) {
		t.Fatalf("err = %v, want entryForRef's thread-not-found SessionUnavailable error, not an empty tail", err)
	}
}

// TestHubJobsOutputNonLocalRefKeepsTheLiveError proves the past gate's
// local-ref requirement: jobs.jsonl under a project state dir is LOCAL
// session state, so a non-local ref must never be answered from it. The past
// index deliberately holds a session whose id is the codex ref's thread id
// and whose persisted job id is the one requested — dropping the local-source
// check would serve another source's caller this local session's output.
func TestHubJobsOutputNonLocalRefKeepsTheLiveError(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "local past job", command: "make local", output: "0123456789"},
	})
	sources := appsource.NewRegistry()
	sources.Add(&jobsListSource{id: "codex", outErr: appwire.SessionUnavailable(threadNotFoundMessagePrefix + sessionID)})

	_, err := hubJobsOutput(context.Background(), cfg, sources, appwire.JobsOutputParams{Ref: "codex:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if !isDeadSessionError(err) {
		t.Fatalf("err = %v, want the codex source's own dead-session error; local past state is not this ref's to serve", err)
	}
}

// TestHubJobsOutputRefNotInPastIndexKeepsTheLiveError proves the last past-gate
// miss, mirroring TestHubJobsListDeadSessionNotInPastIndex for the output
// path: a ref the past index has never heard of surfaces the ORIGINAL
// SessionUnavailable thread-not-found error rather than an empty tail.
func TestHubJobsOutputRefNotInPastIndexKeepsTheLiveError(t *testing.T) {
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

	_, err = hubJobsOutput(context.Background(), cfg, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if !isDeadSessionError(err) {
		t.Fatalf("err = %v, want entryForRef's thread-not-found SessionUnavailable error, not an empty tail", err)
	}
}

// corruptPersistedJobsLog overwrites a seeded session's jobs.jsonl with a
// torn record line (newline-terminated, so jobstore's trailing-partial-line
// recovery leaves it alone and the reader must actually fail on it). This is
// a corrupt durable log, not a missing or empty one.
func corruptPersistedJobsLog(t *testing.T, stateDir, sessionID string) {
	t.Helper()
	path := filepath.Join(stateDir, "sessions", sessionID, "jobs.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"job_started","seq":1,"job_id":"job_`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHubJobsListCorruptJobsLogReturnsErrorNotEmptySuccess proves a corrupt
// jobs.jsonl surfaces as a real error rather than being laundered into an
// empty success or into the dead-session error that triggered the fallback.
// Either laundering would pass every other test in this file while hiding
// real data loss.
func TestHubJobsListCorruptJobsLogReturnsErrorNotEmptySuccess(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_a", description: "run the build", command: "make build"},
	})
	corruptPersistedJobsLog(t, stateDir, sessionID)
	sources := newExitedLocalRegistry()

	_, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if err == nil {
		t.Fatal("hubJobsList returned nil error for a corrupt jobs.jsonl, want a real error, not empty success")
	}
	if isDeadSessionError(err) {
		t.Fatalf("err = %v, want the jobs.jsonl read error; the dead-session error would report a readable session as simply gone", err)
	}
}

// dispatchHubJobsRPC drives one request through the hub app server's real RPC
// router (app_rpc.go registerMiscHandlers) instead of calling hubJobsList/
// hubJobsOutput directly: registration, HandleTyped's params decode, the
// registered closure's forwarding, and the router's error surface. Params
// arrive as a raw JSON literal on purpose — marshaling a params struct would
// rename its keys in lockstep with the wire tags and prove nothing about the
// contract a webui client actually sends. HubStateRoot is pinned to a temp
// dir so the constructed server's controllers never reach for real hub state.
func dispatchHubJobsRPC(t *testing.T, cfg hubcore.WebConfig, sources *appsource.Registry, method, params string) (any, error) {
	t.Helper()
	cfg.HubStateRoot = t.TempDir()
	server := newHubAppServer(cfg, sources)
	return server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: method,
		Params: json.RawMessage(params),
	})
}

// TestSerfJobsListRouteReachesTheHubHandler proves serf/jobs/list is wired to
// hubJobsList with this hub's cfg and sources: the route answers, the ref
// decodes, and the past-fallback list built from cfg.Past comes back as the
// typed JobsListResponse.
func TestSerfJobsListRouteReachesTheHubHandler(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_a", description: "run the build", command: "make build"},
	})

	raw, err := dispatchHubJobsRPC(t, cfg, newExitedLocalRegistry(), appwire.MethodSerfJobsList, `{"ref":"local:`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("dispatch %s: %v", appwire.MethodSerfJobsList, err)
	}
	resp, ok := raw.(appwire.JobsListResponse)
	if !ok {
		t.Fatalf("response = %#v (%T), want appwire.JobsListResponse", raw, raw)
	}
	jobs, ok := resp.Data.([]agent.JobSummary)
	if !ok || len(jobs) != 1 || jobs[0].JobID != "job_a" {
		t.Fatalf("resp.Data = %#v, want the seeded job (the route must reach hubJobsList with the decoded ref)", resp.Data)
	}
}

// TestSerfJobsOutputRouteDecodesJobIDAndMaxBytes drives serf/jobs/output at
// the same boundary. jobId and maxBytes are what no direct hubJobsOutput test
// can vouch for: they exist only on the wire, so a wrong JSON tag or a route
// closure forwarding less than it decoded would still answer — with the wrong
// job, or with the whole log.
func TestSerfJobsOutputRouteDecodesJobIDAndMaxBytes(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "noisy build", command: "make noisy", output: "0123456789"},
	})

	raw, err := dispatchHubJobsRPC(t, cfg, newExitedLocalRegistry(), appwire.MethodSerfJobsOutput, `{"ref":"local:`+sessionID+`","jobId":"job_x","maxBytes":4}`)
	if err != nil {
		t.Fatalf("dispatch %s: %v", appwire.MethodSerfJobsOutput, err)
	}
	resp, ok := raw.(appwire.JobsOutputResponse)
	if !ok {
		t.Fatalf("response = %#v (%T), want appwire.JobsOutputResponse", raw, raw)
	}
	tail, ok := resp.Data.(agent.JobOutputTail)
	if !ok {
		t.Fatalf("resp.Data = %#v (%T), want agent.JobOutputTail", resp.Data, resp.Data)
	}
	if tail.Tail != "6789" || tail.TotalBytes != 10 || !tail.Truncated {
		t.Fatalf("tail = %+v, want the last 4 of job_x's 10 bytes, truncated", tail)
	}
}

// TestSerfJobsOutputRouteMapsUnknownJobToInvalidParams proves the route's
// error surface: the handler's InvalidParams reaches the caller through
// Dispatch with its code and message intact, rather than arriving flattened
// into an internal error or paired with a response.
func TestSerfJobsOutputRouteMapsUnknownJobToInvalidParams(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "noisy build", command: "make noisy", output: "0123456789"},
	})

	raw, err := dispatchHubJobsRPC(t, cfg, newExitedLocalRegistry(), appwire.MethodSerfJobsOutput, `{"ref":"local:`+sessionID+`","jobId":"job_nope","maxBytes":4}`)
	if raw != nil {
		t.Fatalf("response = %#v, want no response alongside the error", raw)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("err = %v, want InvalidParams for a job id the persisted store has never heard of", err)
	}
	if want := "job not found: job_nope"; wire.Message != want {
		t.Fatalf("err message = %q, want %q", wire.Message, want)
	}
}

// TestHubJobsOutputCorruptJobsLogReturnsErrorNotUnknownJob proves the same for
// the output path, and that the read failure is not downgraded to the
// unknown-job InvalidParams: a corrupt log is the hub's problem to report,
// not the caller's to be blamed for.
func TestHubJobsOutputCorruptJobsLogReturnsErrorNotUnknownJob(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "noisy build", command: "make noisy", output: "0123456789"},
	})
	corruptPersistedJobsLog(t, stateDir, sessionID)
	sources := newExitedLocalRegistry()

	_, err := hubJobsOutput(context.Background(), cfg, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if err == nil {
		t.Fatal("hubJobsOutput returned nil error for a corrupt jobs.jsonl, want a real error, not an empty tail")
	}
	var wire appwire.WireError
	if errors.As(err, &wire) && wire.Code == appwire.CodeInvalidParams {
		t.Fatalf("err = %v, want the jobs.jsonl read error; InvalidParams blames the caller for the hub's unreadable log", err)
	}
	if isDeadSessionError(err) {
		t.Fatalf("err = %v, want the jobs.jsonl read error; the dead-session error would report a readable session as simply gone", err)
	}
}
