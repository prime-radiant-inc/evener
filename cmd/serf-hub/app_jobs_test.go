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
)

// jobsListSource is a minimal appsource.Source test double that stubs the
// live path's ListJobs/JobOutput responses; every other (large) Source method
// falls through to the embedded relayLifecycleSource's stub implementation
// (app_rpc_test.go), mirroring taskListSource's existing override pattern.
type jobsListSource struct {
	relayLifecycleSource
	id           string
	jobsResp     appwire.JobsListResponse
	jobsErr      error
	jobsParams   appwire.JobsListParams
	outResp      appwire.JobsOutputResponse
	outErr       error
	outputParams appwire.JobsOutputParams
}

func (s *jobsListSource) ID() string { return s.id }

func (s *jobsListSource) ListJobs(_ context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	s.jobsParams = params
	return s.jobsResp, s.jobsErr
}

func (s *jobsListSource) JobOutput(_ context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	s.outputParams = params
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

func seedPastSessionWithActivity(t *testing.T, childJobs int) (hubcore.WebConfig, string, string, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-jobs-0000000000")
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	childID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	for sessionID, name := range map[string]string{rootID: "Root", childID: "Child"} {
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID:        sessionID,
			ProfileID: "openai",
			Model:     "gpt-5",
			Name:      name,
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	writePersistedActivityLog(t, stateDir, rootID, now, []map[string]any{
		{
			"kind":        "delegate_created",
			"ts":          now.Format(time.RFC3339Nano),
			"delegate_id": "dlg_child",
			"delegate": map[string]any{
				"child_session_id":   childID,
				"transcript_ref":     "local:" + childID,
				"owner_session_id":   rootID,
				"visible_session_id": rootID,
				"generation":         "gen_1",
				"resumable":          true,
			},
		},
		{
			"kind":                  "job_started",
			"ts":                    now.Add(time.Second).Format(time.RFC3339Nano),
			"job_id":                "job_delegate_child",
			"type":                  "delegate",
			"status":                "running",
			"task":                  "inspect child",
			"owner_session_id":      rootID,
			"visible_to_session_id": rootID,
			"delegate_id":           "dlg_child",
			"transcript_ref":        "local:" + childID,
			"started_at":            now.Add(time.Second).Format(time.RFC3339Nano),
		},
		{
			"kind":     "job_finished",
			"ts":       now.Add(2 * time.Second).Format(time.RFC3339Nano),
			"job_id":   "job_delegate_child",
			"status":   "completed",
			"reason":   "exit_zero",
			"ended_at": now.Add(2 * time.Second).Format(time.RFC3339Nano),
		},
		{
			"kind":                  "job_started",
			"ts":                    now.Add(3 * time.Second).Format(time.RFC3339Nano),
			"job_id":                "job_root_shell",
			"type":                  "shell",
			"status":                "running",
			"description":           "root shell",
			"command":               "make root",
			"owner_session_id":      rootID,
			"visible_to_session_id": rootID,
			"started_at":            now.Add(3 * time.Second).Format(time.RFC3339Nano),
		},
		{
			"kind":     "job_finished",
			"ts":       now.Add(4 * time.Second).Format(time.RFC3339Nano),
			"job_id":   "job_root_shell",
			"status":   "completed",
			"reason":   "exit_zero",
			"ended_at": now.Add(4 * time.Second).Format(time.RFC3339Nano),
		},
	})
	childEvents := make([]map[string]any, 0, childJobs*2)
	for i := range childJobs {
		started := now.Add(time.Duration(i+10) * time.Second)
		ended := started.Add(500 * time.Millisecond)
		jobID := fmt.Sprintf("job_child_%04d", i)
		startedText := started.Format(time.RFC3339Nano)
		endedText := ended.Format(time.RFC3339Nano)
		childEvents = append(childEvents,
			map[string]any{
				"kind":                  "job_started",
				"ts":                    startedText,
				"job_id":                jobID,
				"type":                  "shell",
				"status":                "running",
				"description":           fmt.Sprintf("child shell %d", i),
				"command":               fmt.Sprintf("echo child-%d", i),
				"owner_session_id":      childID,
				"visible_to_session_id": childID,
				"started_at":            startedText,
			},
			map[string]any{
				"kind":     "job_finished",
				"ts":       endedText,
				"job_id":   jobID,
				"status":   "completed",
				"reason":   "exit_zero",
				"ended_at": endedText,
			},
		)
	}
	writePersistedActivityLog(t, stateDir, childID, now, childEvents)
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{Past: idx}, rootID, childID, stateDir
}

func writePersistedActivityLog(t *testing.T, stateDir, sessionID string, _ time.Time, events []map[string]any) {
	t.Helper()
	dir := filepath.Join(stateDir, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, len(events))
	for i, event := range events {
		event["seq"] = i + 1
		b, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustActivityTree(t *testing.T, data any) appwire.JobActivityTree {
	t.Helper()
	tree, ok := data.(appwire.JobActivityTree)
	if !ok {
		t.Fatalf("activity tree = %#v (%T), want appwire.JobActivityTree", data, data)
	}
	return tree
}

func findActivityDelegate(t *testing.T, session appwire.JobActivitySession, childID string) appwire.JobActivityDelegate {
	t.Helper()
	for _, entry := range session.Entries {
		if entry.Delegate != nil && entry.Delegate.ChildSessionID == childID {
			return *entry.Delegate
		}
	}
	t.Fatalf("no delegate for child %q in %+v", childID, session.Entries)
	return appwire.JobActivityDelegate{}
}

// TestHubJobsListLiveDaemon proves a running daemon's jobstore is
// authoritative: even though a past index entry (with its own, different,
// persisted job) exists for the same session, a successful live ListJobs
// response is passed through untouched and past is never consulted.
func TestHubJobsListLiveDaemon(t *testing.T) {
	cfg, sessionID, childID, _ := seedPastSessionWithActivity(t, 1)
	liveTree := appwire.JobActivityTree{Root: appwire.JobActivitySession{
		SessionID: sessionID,
		Ref:       "local:" + sessionID,
		Entries: []appwire.JobActivityEntry{{Kind: "delegate", Delegate: &appwire.JobActivityDelegate{
			DelegateID:     "dlg_live",
			ChildSessionID: childID,
			ChildRef:       "local:" + childID,
		}}},
	}}
	source := &jobsListSource{id: "local", jobsResp: appwire.JobsListResponse{Data: liveTree}}
	sources := appsource.NewRegistry()
	sources.Add(source)

	resp, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID, Continuation: "live-next"})
	if err != nil {
		t.Fatalf("hubJobsList: %v", err)
	}
	tree := mustActivityTree(t, resp.Data)
	if tree.Root.SessionID != liveTree.Root.SessionID || tree.Root.Ref != liveTree.Root.Ref {
		t.Fatalf("resp.Data = %#v, want the live tree root (past must not be consulted)", resp.Data)
	}
	if len(tree.Root.Entries) != 1 || tree.Root.Entries[0].Delegate == nil || tree.Root.Entries[0].Delegate.DelegateID != "dlg_live" {
		t.Fatalf("resp.Data = %#v, want the live delegate entry", resp.Data)
	}
	if source.jobsParams.Ref != "local:"+sessionID || source.jobsParams.Continuation != "live-next" {
		t.Fatalf("live params = %+v", source.jobsParams)
	}
	if delegate := tree.Root.Entries[0].Delegate; delegate == nil || delegate.ChildSessionID != childID {
		t.Fatalf("live delegate = %+v", tree.Root.Entries[0].Delegate)
	}
}

// TestHubJobsListDeadSessionFallsBackToPast is the RED case: a session whose
// daemon has exited (no live rendezvous entry) must still serve its recursive
// persisted activity tree from jobs.jsonl, not the SessionUnavailable error
// entryForRef raises for a live-only lookup.
func TestHubJobsListDeadSessionFallsBackToPast(t *testing.T) {
	cfg, sessionID, childID, _ := seedPastSessionWithActivity(t, 1)
	sources := newExitedLocalRegistry()

	resp, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubJobsList: %v", err)
	}
	tree := mustActivityTree(t, resp.Data)
	if tree.Root.SessionID != sessionID || tree.Root.Ref != "local:"+sessionID {
		t.Fatalf("root = %+v", tree.Root)
	}
	if len(tree.Root.Entries) != 2 {
		t.Fatalf("root entries = %+v", tree.Root.Entries)
	}
	delegate := findActivityDelegate(t, tree.Root, childID)
	if delegate.Child == nil {
		t.Fatalf("delegate child missing: %+v", delegate)
	}
	if delegate.Child.SessionID != childID || delegate.Child.Ref != "local:"+childID {
		t.Fatalf("child = %+v", delegate.Child)
	}
	if len(delegate.Child.Entries) != 1 || delegate.Child.Entries[0].Job == nil || delegate.Child.Entries[0].Job.OwnerRef != "local:"+childID {
		t.Fatalf("child entries = %+v", delegate.Child.Entries)
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
	cfg, sessionID, _, _ := seedPastSessionWithActivity(t, 1)
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
	cfg, sessionID, _, _ := seedPastSessionWithActivity(t, 1)
	sourceErr := appwire.SessionUnavailable("local daemon unavailable: broken pipe")
	sources := appsource.NewRegistry()
	sources.Add(&jobsListSource{id: "local", jobsErr: sourceErr})

	_, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if err == nil {
		t.Fatal("hubJobsList returned nil error, want the live daemon failure (only the dead-session sentinel may fall back)")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Message != "local daemon unavailable: broken pipe" {
		t.Fatalf("err = %v, want the live local source error preserved", err)
	}
	if isDeadSessionError(err) {
		t.Fatalf("err = %v misclassified as the dead-session condition; it is a live-source failure", err)
	}
}

func TestHubJobsListContinuationFallsBackToPastUnchanged(t *testing.T) {
	cfg, sessionID, childID, _ := seedPastSessionWithActivity(t, 2002)
	sources := newExitedLocalRegistry()

	first, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID})
	if err != nil {
		t.Fatalf("hubJobsList first page: %v", err)
	}
	firstTree := mustActivityTree(t, first.Data)
	delegate := findActivityDelegate(t, firstTree.Root, childID)
	if delegate.Child == nil || !delegate.Child.Branch.Truncated || delegate.Child.Branch.Continuation == "" {
		t.Fatalf("first child branch = %+v child = %+v", delegate.Branch, delegate.Child)
	}
	continued, err := hubJobsList(context.Background(), cfg, sources, appwire.JobsListParams{Ref: "local:" + sessionID, Continuation: delegate.Child.Branch.Continuation})
	if err != nil {
		t.Fatalf("hubJobsList continuation: %v", err)
	}
	continuedTree := mustActivityTree(t, continued.Data)
	continuedDelegate := findActivityDelegate(t, continuedTree.Root, childID)
	if continuedDelegate.Child == nil || continuedDelegate.Child.SessionID != childID {
		t.Fatalf("continued delegate child = %+v", continuedDelegate.Child)
	}
	if len(continuedDelegate.Child.Entries) == 0 {
		t.Fatalf("continued child entries = %+v", continuedDelegate.Child.Entries)
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
// TestHubJobsListLiveErrorPropagates: only the precise dead-session sentinel
// may fall back. Any other live-source failure must be propagated as-is rather
// than silently serving stale persisted output.
func TestHubJobsOutputLiveErrorPropagates(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithJobs(t, []persistedJobFixture{
		{id: "job_x", description: "stale past job", command: "make stale", output: "0123456789"},
	})
	sourceErr := appwire.SessionUnavailable("local daemon unavailable: broken pipe")
	sources := appsource.NewRegistry()
	sources.Add(&jobsListSource{id: "local", outErr: sourceErr})

	_, err := hubJobsOutput(context.Background(), cfg, sources, appwire.JobsOutputParams{Ref: "local:" + sessionID, JobID: "job_x", MaxBytes: 4})
	if err == nil {
		t.Fatal("hubJobsOutput returned nil error, want the live daemon failure (only the dead-session sentinel may fall back)")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Message != "local daemon unavailable: broken pipe" {
		t.Fatalf("err = %v, want the live local source error preserved", err)
	}
	if isDeadSessionError(err) {
		t.Fatalf("err = %v misclassified as the dead-session condition; it is a live-source failure", err)
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
	cfg, sessionID, _, stateDir := seedPastSessionWithActivity(t, 1)
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
// decodes, and the past-fallback activity tree built from cfg.Past comes back
// as the typed JobsListResponse.
func TestSerfJobsListRouteReachesTheHubHandler(t *testing.T) {
	cfg, sessionID, childID, _ := seedPastSessionWithActivity(t, 1)

	raw, err := dispatchHubJobsRPC(t, cfg, newExitedLocalRegistry(), appwire.MethodSerfJobsList, `{"ref":"local:`+sessionID+`"}`)
	if err != nil {
		t.Fatalf("dispatch %s: %v", appwire.MethodSerfJobsList, err)
	}
	resp, ok := raw.(appwire.JobsListResponse)
	if !ok {
		t.Fatalf("response = %#v (%T), want appwire.JobsListResponse", raw, raw)
	}
	tree := mustActivityTree(t, resp.Data)
	delegate := findActivityDelegate(t, tree.Root, childID)
	if tree.Root.SessionID != sessionID || delegate.Child == nil || delegate.Child.SessionID != childID {
		t.Fatalf("resp.Data = %#v, want the seeded activity tree (the route must reach hubJobsList with the decoded ref)", resp.Data)
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
