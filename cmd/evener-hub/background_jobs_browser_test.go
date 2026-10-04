//go:build browserguard

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/test/e2e/fakellm"
)

const backgroundJobsBrowserCount = 151

// TestMain scrubs product EVENER_* variables and removes its redirected root.
// Capture the supplied evidence destination before that environment redirect.
var backgroundJobsArtifactRoot = os.Getenv("EVENER_SCRATCH_DIR")

// The only scripted boundary is the provider. Jobs, journal eligibility, paging,
// subscriptions, output and browser state all belong to the real stack.
func TestBackgroundJobsBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser guard requires Chrome and real daemons")
	}
	if _, err := fs.ReadFile(distFS(), "index.html"); err != nil {
		t.Fatalf("production frontend required, run make build-web: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute) // TRIPWIRE, never releases a producer barrier.
	defer cancel()
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	client := stack.dialRPC(ctx, t)
	artifactRoot := backgroundJobsArtifactRoot
	if artifactRoot == "" {
		artifactRoot = sandboxtest.KeptTempDir()
	}
	artifacts, err := os.MkdirTemp(artifactRoot, "backgroundjobsguard-")
	if err != nil {
		t.Fatal(err)
	}
	// Evidence is retained on success too, including cursor-bearing RPCs.
	t.Logf("background Jobs browser evidence: %s", artifacts)
	for _, name := range []string{"control.jsonl", "milestones.jsonl"} {
		if err := os.WriteFile(filepath.Join(artifacts, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	rounds := 200
	started, err := client.ThreadStart(ctx, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		Input:           []appwire.InputItem{{Type: "text", Text: "BACKGROUND_JOBS_PRODUCER"}},
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off", MaxRounds: &rounds},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootRef := threadRef(started.Thread)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = client.ThreadShutdown(cleanupCtx, appwire.ThreadShutdownParams{Ref: rootRef})
	})
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	next := func() *fakellm.Call {
		t.Helper()
		call, err := provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
		return call
	}
	call := next()
	tools, _ := call.Body["tools"].([]any)
	advertised := false
	for _, raw := range tools {
		entry, _ := raw.(map[string]any)
		function, _ := entry["function"].(map[string]any)
		if function["name"] == "shell" {
			advertised = true
			properties, _ := function["parameters"].(map[string]any)
			fields, _ := properties["properties"].(map[string]any)
			if fields["mode"] == nil || fields["background"] != nil {
				t.Fatal("generic shell must advertise the canonical mode field")
			}
		}
	}
	if !advertised {
		t.Fatal("first actual provider request does not advertise shell")
	}
	barrier := func(name string) string { return filepath.Join(stack.workDir, name) }
	waitFor := func(name string) string {
		return fmt.Sprintf("while ! test -e %q; do sleep 0.02; done; ", barrier(name))
	}
	for index := 0; index < backgroundJobsBrowserCount; index++ {
		command := fmt.Sprintf("printf 'BACKGROUND_JOB_%03d\\n'", index)
		switch index {
		case 0:
			command = "printf 'BACKGROUND_LATE_INITIAL\\n'; " + waitFor("refresh") +
				"printf 'BACKGROUND_LATE_REFRESH\\n'; " + waitFor("reconnect") +
				"printf 'BACKGROUND_LATE_RECONNECT\\n'; " + waitFor("finish") +
				"printf 'BACKGROUND_LATE_FINAL\\n'; exit 2"
		case 1:
			command = "printf 'BACKGROUND_STOP_OUTPUT\\n'; " + waitFor("never-release-stop")
		case 2:
			command += "; exit 3"
		case backgroundJobsBrowserCount - 1:
			command = waitFor("refresh-invalidate") + command
		}
		call.RespondToolCall("shell", map[string]any{
			"command": command, "mode": "background", "description": fmt.Sprintf("Background job %03d", index),
			"intent": "Producing real background Jobs for browser history and paging",
		})
		call = next()
		if index%50 == 0 {
			call.RespondToolCall("shell", map[string]any{
				"command": "printf 'FOREGROUND_RETAINED\\n'; printf '%10000s' ''", "mode": "foreground",
				"description": fmt.Sprintf("Foreground job %03d", index),
				"intent":      "Retaining real foreground output outside background Jobs",
			})
			call = next()
		}
	}
	jobs := backgroundJobsAwait(ctx, t, client, rootRef, func(rows []appwire.JobActivityJob) bool {
		return len(rows) == backgroundJobsBrowserCount
	})
	byDescription := make(map[string]appwire.JobActivityJob)
	for _, job := range jobs {
		if job.OwnerRef != rootRef || job.TranscriptRef != "job:"+job.JobID || !job.Background {
			t.Fatalf("actual background identity: %+v", job)
		}
		byDescription[job.Description] = job
	}
	late, stopped := byDescription["Background job 000"], byDescription["Background job 001"]
	if late.JobID == "" || stopped.JobID == "" {
		t.Fatal("real held producers missing")
	}
	call.RespondToolCall("job_stop", map[string]any{
		"target": stopped.JobID, "max_wait_ms": 10000, "intent": "Settling the actual stopped history case",
	})
	call = next()
	call.RespondToolCall("communicate", communicateArgs("BACKGROUND_JOBS_READY"))
	// Completion notices may wake the session later. Answer only at the provider
	// boundary; these replies neither invent a job nor settle the held command.
	providerDone := make(chan struct{})
	go func() {
		defer close(providerDone)
		for {
			call, err := provider.Next(ctx.Done())
			if err != nil {
				return
			}
			call.RespondToolCall("communicate", communicateArgs("BACKGROUND_JOBS_NOTICE"))
		}
	}()
	defer func() { cancel(); <-providerDone }()
	jobs = backgroundJobsAwait(ctx, t, client, rootRef, func(rows []appwire.JobActivityJob) bool {
		if len(rows) != backgroundJobsBrowserCount {
			return false
		}
		for _, job := range rows {
			if job.JobID != late.JobID && job.Description != "Background job 150" && !job.Terminal {
				return false
			}
		}
		return true
	})
	for _, job := range jobs {
		switch job.Description {
		case "Background job 000", "Background job 150":
			if job.Terminal || job.Status != "running" {
				t.Fatalf("barrier-held producer must remain live: %+v", job)
			}
		case "Background job 001":
			if job.Status != "cancelled" || !job.Terminal {
				t.Fatalf("real job_stop outcome: %+v", job)
			}
		case "Background job 002":
			if job.Status != "command_exited_nonzero" || job.ExitCode == nil || *job.ExitCode != 3 {
				t.Fatalf("real nonzero outcome: %+v", job)
			}
		default:
			if job.Status != "completed" {
				t.Fatalf("real successful outcome: %+v", job)
			}
		}
	}
	summary, err := client.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: rootRef})
	if err != nil || !summary.Jobs.Known || summary.Jobs.Total != backgroundJobsBrowserCount || summary.Jobs.Active != 2 {
		t.Fatalf("actual filtered counts: %+v, error %v", summary, err)
	}
	diagnostic := backgroundJobsDiagnostics(ctx, t, client, rootRef)
	if len(diagnostic) != backgroundJobsBrowserCount+4 {
		t.Fatalf("complete diagnostic membership = %d, want %d", len(diagnostic), backgroundJobsBrowserCount+4)
	}
	excluded := ""
	for _, job := range diagnostic {
		if strings.HasPrefix(job.Description, "Foreground job ") {
			if job.Background || !job.Terminal || job.OutputBytes < 10000 {
				t.Fatalf("real retained foreground: %+v", job)
			}
			excluded = job.JobID
		}
	}
	if excluded == "" {
		t.Fatal("retained foreground missing from complete diagnostic history")
	}
	output, err := client.JobOutput(ctx, appwire.JobsOutputParams{Ref: rootRef, JobID: excluded, MaxBytes: 20000})
	if err != nil || !strings.HasPrefix(output.Data.Tail, "FOREGROUND_RETAINED\n") {
		t.Fatalf("excluded output remains readable: %+v, error %v", output, err)
	}
	backgroundJobsWriteJSON(t, filepath.Join(artifacts, "producer.json"), map[string]any{
		"jobs": jobs, "summary": summary, "diagnostic": diagnostic, "excludedJobId": excluded,
	})
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Add(artifacts); err != nil {
		watcher.Close()
		t.Fatal(err)
	}
	controlDone := make(chan error, 1)
	go func() {
		defer watcher.Close()
		seen := 0
		for {
			body, err := os.ReadFile(filepath.Join(artifacts, "control.jsonl"))
			if err != nil {
				controlDone <- err
				return
			}
			if end := bytes.LastIndexByte(body, '\n'); end >= 0 {
				lines := bytes.Split(body[:end], []byte{'\n'})
				for ; seen < len(lines); seen++ {
					var record struct {
						Command string `json:"command"`
					}
					if err := json.Unmarshal(lines[seen], &record); err != nil {
						controlDone <- err
						return
					}
					if record.Command != "refresh" && record.Command != "reconnect" && record.Command != "finish" {
						controlDone <- fmt.Errorf("unexpected Jobs control %q", record.Command)
						return
					}
					if err := os.WriteFile(barrier(record.Command), nil, 0600); err != nil {
						controlDone <- err
						return
					}
					if record.Command == "refresh" {
						if err := backgroundJobsAwaitOutput(ctx, client, rootRef, late.JobID, "BACKGROUND_LATE_INITIAL\nBACKGROUND_LATE_REFRESH\n"); err != nil {
							controlDone <- err
							return
						}
						// The real terminal lifecycle publishes Jobs invalidation;
						// ordinary output appends do not own that signal.
						if err := os.WriteFile(barrier("refresh-invalidate"), nil, 0600); err != nil {
							controlDone <- err
							return
						}
					}
				}
			}
			select {
			case <-ctx.Done():
				controlDone <- nil
				return
			case err := <-watcher.Errors:
				controlDone <- err
				return
			case <-watcher.Events:
			}
		}
	}()
	defer func() {
		cancel()
		if err := <-controlDone; err != nil {
			t.Error(err)
		}
	}()
	fixture := map[string]any{
		"url": hubedge.AuthURLFor("http://"+stack.addr, stack.token), "artifactDir": artifacts,
		"controlPath": filepath.Join(artifacts, "control.jsonl"), "milestonePath": filepath.Join(artifacts, "milestones.jsonl"),
		"rootRef": rootRef, "laterJobId": late.JobID, "excludedJobId": excluded, "eligibleCount": backgroundJobsBrowserCount,
	}
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(artifacts, "driver.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	driver := exec.CommandContext(ctx, "node", "frontend/scripts/backgroundjobsguard/run.mjs")
	driver.Stdin = bytes.NewReader(body)
	driver.Stdout, driver.Stderr = log, log
	driver.WaitDelay = 15 * time.Second
	if err := driver.Run(); err != nil {
		skillGuardLogDriverTail(t, log.Name())
		t.Fatalf("real background Jobs journey: %v, evidence: %s", err, artifacts)
	}
	final := backgroundJobsAwait(ctx, t, client, rootRef, func(rows []appwire.JobActivityJob) bool {
		for _, row := range rows {
			if row.JobID == late.JobID {
				return row.Terminal
			}
		}
		return false
	})
	for _, job := range final {
		if job.JobID == late.JobID && (job.Status != "command_exited_nonzero" || job.ExitCode == nil || *job.ExitCode != 2) {
			t.Fatalf("held producer's final real outcome: %+v", job)
		}
	}
	output, err = client.JobOutput(ctx, appwire.JobsOutputParams{Ref: rootRef, JobID: late.JobID})
	if err != nil || output.Data.Tail != "BACKGROUND_LATE_INITIAL\nBACKGROUND_LATE_REFRESH\nBACKGROUND_LATE_RECONNECT\nBACKGROUND_LATE_FINAL\n" {
		t.Fatalf("actual held producer output: %+v, error %v", output, err)
	}
	backgroundJobsWriteJSON(t, filepath.Join(artifacts, "final-producer.json"), final)
	milestones, err := os.ReadFile(filepath.Join(artifacts, "milestones.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"closed-history-late-live", "quiet-later-history", "refresh-extent", "reconnect-extent", "reload-extent", "settled-output", "post-journey-errors"} {
		found := false
		for _, line := range strings.Split(string(milestones), "\n") {
			var record struct {
				Milestone string `json:"milestone"`
			}
			if json.Unmarshal([]byte(line), &record) == nil && record.Milestone == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing actual browser milestone %s", want)
		}
	}
}

func backgroundJobsAwaitOutput(ctx context.Context, client *appwire.Client, ref, jobID, want string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, err := client.JobOutput(ctx, appwire.JobsOutputParams{Ref: ref, JobID: jobID})
		if err != nil {
			return err
		}
		if output.Data.Tail == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func backgroundJobsAwait(ctx context.Context, t *testing.T, client *appwire.Client, ref string, ready func([]appwire.JobActivityJob) bool) []appwire.JobActivityJob {
	t.Helper()
	for {
		var rows []appwire.JobActivityJob
		cursor := ""
		for {
			page, err := client.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: ref, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Page.Issues) > 0 {
				t.Fatalf("actual Jobs page issues: %+v", page.Page)
			}
			rows = append(rows, page.Jobs...)
			if page.Page.Complete {
				break
			}
			if page.Page.NextCursor == "" || page.Page.NextCursor == cursor {
				t.Fatal("actual Jobs cursor does not advance")
			}
			cursor = page.Page.NextCursor
		}
		if ready(rows) {
			return rows
		}
		select {
		case <-ctx.Done():
			t.Fatalf("actual Jobs state did not arrive: %v, last rows %+v", ctx.Err(), rows)
		case _, ok := <-client.Notifications():
			if !ok {
				t.Fatal("actual Jobs subscription closed")
			}
		}
	}
}

func backgroundJobsDiagnostics(ctx context.Context, t *testing.T, client *appwire.Client, ref string) []appwire.JobActivityJob {
	t.Helper()
	var rows []appwire.JobActivityJob
	cursor := ""
	for {
		page, err := client.JobsList(ctx, appwire.JobsListParams{Ref: ref, Continuation: cursor})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(page.Data)
		if err != nil {
			t.Fatal(err)
		}
		var tree appwire.JobActivityTree
		if err := json.Unmarshal(body, &tree); err != nil {
			t.Fatal(err)
		}
		for _, entry := range tree.Root.Entries {
			if entry.Job != nil {
				rows = append(rows, *entry.Job)
			}
		}
		if !tree.Root.Branch.Truncated {
			return rows
		}
		if tree.Root.Branch.Continuation == "" || tree.Root.Branch.Continuation == cursor {
			t.Fatal("diagnostic cursor does not advance")
		}
		cursor = tree.Root.Branch.Continuation
	}
}

func backgroundJobsWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}
