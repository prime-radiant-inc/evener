package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

func TestSessionActivityBackgroundEligibilityMatchesPagesAndCounts(t *testing.T) {
	t.Parallel()
	stateDir, id := t.TempDir(), "eligiblejobs"
	savePastActivityMeta(t, stateDir, id, "Root")
	if err := os.MkdirAll(jobsDir(stateDir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(filepath.Join(jobsDir(stateDir, id), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i, background := range []bool{true, false, true, false, true} {
		raw := fmt.Sprintf(`{"kind":"job_started","job_id":"job_%d","type":"shell","owner_session_id":%q,"started_at":"2026-10-03T12:00:00Z","background":%t}`, i, id, background)
		var event jobstore.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 2}
	seen := make(map[string]bool)
	complete := false
	for attempt := range 20 {
		page, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 && len(page.Jobs) != 2 {
			t.Errorf("small first page capacity = %d, want two eligible rows", len(page.Jobs))
		}
		for _, row := range page.Jobs {
			if !row.Background || seen[row.JobID] {
				t.Errorf("unclassified or duplicate row: %+v", row)
			}
			seen[row.JobID] = true
		}
		if page.Page.Complete {
			complete = true
			break
		}
		if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
			t.Fatalf("incomplete page has no advancing continuation: %+v", page.Page)
		}
		params.Cursor = page.Page.NextCursor
	}
	if !complete || len(seen) != 3 || !seen["job_0"] || !seen["job_2"] || !seen["job_4"] || seen["job_1"] || seen["job_3"] {
		t.Errorf("eligible membership = %v, complete = %v", seen, complete)
	}
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 3 || summary.Jobs.Active != 3 {
		t.Errorf("counts disagree: %+v, error %v", summary.Jobs, err)
	}
}

func TestSessionActivityBackgroundExcludedGapHasBoundedContinuation(t *testing.T) {
	t.Parallel()
	stateDir, id := t.TempDir(), "excludedgap"
	savePastActivityMeta(t, stateDir, id, "Root")
	if err := os.MkdirAll(jobsDir(stateDir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(filepath.Join(jobsDir(stateDir, id), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const count = activityMaxWorkUnits + 5
	events := make([]jobstore.Event, count)
	for i := range events {
		at := time.Unix(int64(i+1), 0).UTC()
		events[i] = jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("job_gap_%04d", i), Type: jobstore.JobShell,
			OwnerSessionID: id, StartedAt: &at, Background: i == 0 || i == count-1}
	}
	if err := store.AppendBatch(events); err != nil {
		t.Fatal(err)
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 2}
	seen := make(map[string]bool)
	complete, yielded := false, false
	for range 20 {
		page, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Jobs {
			if !row.Background || seen[row.JobID] {
				t.Fatalf("excluded or duplicate row: %+v", row)
			}
			seen[row.JobID] = true
		}
		if page.Page.Complete {
			complete = true
			break
		}
		yielded = yielded || len(page.Jobs) == 0
		if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
			t.Fatalf("bounded gap did not advance: %+v", page.Page)
		}
		params.Cursor = page.Page.NextCursor
	}
	if !complete || !yielded || len(seen) != 2 || !seen["job_gap_0000"] || !seen[fmt.Sprintf("job_gap_%04d", count-1)] {
		t.Fatalf("gap lost membership/progress: seen %v, complete %v, yielded %v", seen, complete, yielded)
	}
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 2 || summary.Jobs.Active != 2 {
		t.Fatalf("gap counts disagree: %+v, error %v", summary.Jobs, err)
	}
}

func TestSessionActivityLegacyForegroundDataRemainReadable(t *testing.T) {
	t.Parallel()
	stateDir, id := t.TempDir(), identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(id)
	savePastActivityMeta(t, stateDir, id, "Root")
	outputPath := filepath.Join(jobsDir(stateDir, id), "jobs", jobID+".log")
	const output = "retained foreground evidence\nline two\n"
	seedLocalJob(t, stateDir, id, jobID, outputPath, output, true)
	journalPath := filepath.Join(jobsDir(stateDir, id), "jobs.jsonl")
	before := make(map[string][]byte)
	for _, path := range []string{journalPath, outputPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = append([]byte(nil), data...)
	}
	page, err := LoadSessionActivityJobs(t.Context(), stateDir, id, appwire.SessionActivityListParams{Ref: encodeRef("", id)})
	if err != nil || len(page.Jobs) != 0 || !page.Page.Complete {
		t.Errorf("unmarked foreground leaked into activity: %+v, error %v", page, err)
	}
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: encodeRef("", id)})
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 0 {
		t.Errorf("unmarked foreground leaked into counts: %+v, error %v", summary.Jobs, err)
	}
	tree, err := LoadSessionJobActivityTree(t.Context(), stateDir, id, appwire.JobsListParams{Ref: encodeRef("", id)})
	if err != nil || len(tree.Root.Entries) != 1 || tree.Root.Entries[0].Job == nil || tree.Root.Entries[0].Job.JobID != jobID {
		t.Fatalf("diagnostics lost foreground record: %+v, error %v", tree, err)
	}
	tail, found, err := LoadSessionJobOutputTail(stateDir, id, jobID, 0, 1024)
	if err != nil || !found || tail.Tail != output {
		t.Fatalf("direct output = %+v, found %v, error %v", tail, found, err)
	}
	transcript, err := readJobTranscript(&toolDeps{stateDir: stateDir}, "job:"+jobID, "", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil || !strings.Contains(envelope.Content, "```text\n"+output+"```") {
		t.Fatalf("transcript lost exact output bytes: %q, error %v", envelope.Content, err)
	}
	for path, original := range before {
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(current, original) {
			t.Fatalf("read changed retained file %s: error %v", path, err)
		}
	}
}

func TestSessionActivityRealForegroundProducersStayDiagnosticOnly(t *testing.T) {
	t.Parallel()
	for _, runtimeLimit := range []bool{false, true} {
		t.Run(fmt.Sprintf("runtime_limit_%t", runtimeLimit), func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
				StateDir: t.TempDir(), AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md"),
				testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			}))
			jm := s.jobManager
			se := s.env.(execenv.StreamingExecutor)
			args := shellArgs{Command: "printf actual-foreground-output", BlockTimeoutMS: 5000}
			var result shellResult
			if runtimeLimit {
				clk := agenttest.NewFakeClock()
				jm.clock = clk
				args.Command = "printf actual-foreground-output; touch ready; sleep 30"
				args.MaxRuntimeMS = 500
				results := make(chan shellResult, 1)
				go func() { results <- runShell(t.Context(), jm, se, args) }()
				clk.BlockUntil(2)
				awaitShellReady(t, se)
				clk.Advance(500 * time.Millisecond)
				result = receiveShellResult(t, results)
				if result.Status != string(jobstore.StatusStopped) || result.Reason != "run_timeout" {
					t.Fatalf("runtime outcome: %+v", result)
				}
			} else {
				result = runShell(t.Context(), jm, se, args)
				if result.Status != string(jobstore.StatusCompleted) || result.settle == nil {
					t.Fatalf("inline outcome: %+v", result)
				}
				result.JobID = result.settle(true)
			}
			if result.JobID == "" || result.RunningInBackground {
				t.Fatalf("foreground producer: %+v", result)
			}
			params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
			page, err := s.ListActivityJobs(t.Context(), params)
			if err != nil || len(page.Jobs) != 0 || !page.Page.Complete {
				t.Errorf("actual foreground leaked into activity: %+v, error %v", page, err)
			}
			summary, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: params.Ref})
			if err != nil || !summary.Jobs.Known || summary.Jobs.Total != 0 {
				t.Errorf("actual foreground leaked into counts: %+v, error %v", summary.Jobs, err)
			}
			if records := jm.list(listFilter{}); len(records) != 1 || records[0].JobID != result.JobID {
				t.Fatalf("diagnostics lost foreground handle: %+v", records)
			}
			tail, found, err := s.JobOutputTail(result.JobID, 0, 1024)
			if err != nil || !found || tail.Tail != "actual-foreground-output" {
				t.Fatalf("actual foreground output: %+v, found %v, error %v", tail, found, err)
			}
		})
	}
}
