package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func task7LocalSession(t *testing.T) (*Session, string) {
	t.Helper()
	workspace := t.TempDir()
	s, err := NewSession(
		newArtifactTestClient(),
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(workspace),
		SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		},
	)
	if err != nil {
		t.Fatalf("new local session: %v", err)
	}
	t.Cleanup(s.Close)
	return s, workspace
}

func task7ExecTool(t *testing.T, s *Session, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s args: %v", name, err)
	}
	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "task7-" + name,
		Name:      name,
		Arguments: raw,
	}, "")
	if res.IsError {
		t.Fatalf("%s failed: %s", name, res.Output)
	}
	return res
}

func task7ExecutedToolStarts(s *Session, toolName string) int {
	count := 0
	for {
		select {
		case event := <-s.events:
			if event.Kind != events.EventToolCallStart {
				continue
			}
			data, ok := event.Data.(events.ToolCallStartData)
			if ok && data.ToolName == toolName {
				count++
			}
		default:
			return count
		}
	}
}

func TestRecoverableGrepReceiptReplayEndToEnd(t *testing.T) {
	s, workspace := task7LocalSession(t)
	const pattern = `RECOVER_[0-9]{3}`
	const matchCount = 70
	var fixture strings.Builder
	for i := range matchCount {
		for before := range 3 {
			fmt.Fprintf(&fixture, "before-%03d-%d\n", i, before)
		}
		fmt.Fprintf(&fixture, "RECOVER_%03d\n", i)
		for after := range 3 {
			fmt.Fprintf(&fixture, "after-%03d-%d\n", i, after)
		}
	}
	path := filepath.Join(workspace, "recoverable-grep.txt")
	if err := os.WriteFile(path, []byte(fixture.String()), 0o600); err != nil {
		t.Fatalf("write grep fixture: %v", err)
	}

	grep := task7ExecTool(t, s, "grep", map[string]any{
		"pattern":          pattern,
		"path":             "recoverable-grep.txt",
		"output_mode":      "content",
		"context_lines":    3,
		"case_insensitive": false,
		"max_results":      1000,
	})
	if !grep.Truncated {
		t.Fatalf("grep was not generically truncated; output lines=%d", strings.Count(grep.Output, "\n")+1)
	}
	receiptOccurrences := regexp.MustCompile(`artifact:[0-9a-f]{32}`).FindAllString(grep.Output, -1)
	receipts := compactToolNames(receiptOccurrences)
	if len(receipts) != 1 {
		t.Fatalf("grep unique receipt count = %d from %d occurrences, want exactly one; output tail=%q", len(receipts), len(receiptOccurrences), grep.Output[max(0, len(grep.Output)-500):])
	}
	if strings.Contains(grep.Output, "RECOVER_069") {
		t.Fatal("grep preview unexpectedly contains the match selected to prove replay of omitted output")
	}

	replayed := task7ExecTool(t, s, "read_transcript", map[string]any{
		"transcript_ref": receipts[0],
		"output_match":   pattern,
		"context_lines":  3,
	})
	if replayed.Truncated {
		t.Fatalf("artifact search was generically truncated: %s", replayed.Output)
	}
	var search retainedSearchResult
	if err := json.Unmarshal(toolResultJSON(replayed), &search); err != nil {
		t.Fatalf("decode artifact replay: %v (output: %s)", err, replayed.Output)
	}
	if !search.SearchComplete || search.Continuation != nil {
		t.Fatalf("artifact replay incomplete: %+v", search)
	}
	if len(search.Matches) != matchCount {
		t.Fatalf("artifact replay matches = %d, want %d", len(search.Matches), matchCount)
	}
	for i, match := range search.Matches {
		firstLine := i*7 + 1
		wantLine := fmt.Sprintf("%d:RECOVER_%03d", firstLine+3, i)
		wantBefore := []string{
			fmt.Sprintf("%d-before-%03d-0", firstLine, i),
			fmt.Sprintf("%d-before-%03d-1", firstLine+1, i),
			fmt.Sprintf("%d-before-%03d-2", firstLine+2, i),
		}
		wantAfter := []string{
			fmt.Sprintf("%d-after-%03d-0", firstLine+4, i),
			fmt.Sprintf("%d-after-%03d-1", firstLine+5, i),
			fmt.Sprintf("%d-after-%03d-2", firstLine+6, i),
		}
		if match.Line != wantLine || !slices.Equal(match.Before, wantBefore) || !slices.Equal(match.After, wantAfter) {
			t.Fatalf("match %d = %+v, want line %q before %v after %v", i, match, wantLine, wantBefore, wantAfter)
		}
	}
	if search.Matches[matchCount-1].Line != "487:RECOVER_069" {
		t.Fatalf("omitted grep match was not recovered: last=%+v", search.Matches[matchCount-1])
	}
	if got := task7ExecutedToolStarts(s, "grep"); got != 1 {
		t.Fatalf("executed grep calls = %d, want exactly one after artifact replay", got)
	}
}

func task7RunningOutput(t *testing.T, s *Session, jobID string) *jobstore.OutputStore {
	t.Helper()
	s.jobManager.mu.Lock()
	run := s.jobManager.running[jobID]
	s.jobManager.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("running job %q has no live output store", jobID)
	}
	return run.output
}

func task7AwaitOutputBytes(t *testing.T, output *jobstore.OutputStore, want int64) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if got := output.Len(); got >= want {
			if got != want {
				t.Fatalf("output bytes = %d, want exactly %d", got, want)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("output bytes = %d, want %d", output.Len(), want)
		case <-tick.C:
		}
	}
}

func task7DecodeRetainedPage(t *testing.T, res tool.ExecResult) retainedPageEnvelope {
	t.Helper()
	var page retainedPageEnvelope
	if err := json.Unmarshal(toolResultJSON(res), &page); err != nil {
		t.Fatalf("decode retained page: %v (output: %s)", err, res.Output)
	}
	return page
}

func TestRecoverableRunningJobEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripted running-job fixture uses POSIX shell syntax")
	}
	s, dir := task7LocalSession(t)
	initialPath := filepath.Join(dir, "initial")
	appendRelease := filepath.Join(dir, "append-release")
	exitRelease := filepath.Join(dir, "exit-release")
	const priorMatch = "needle complete\n"
	initial := priorMatch + strings.Repeat("x", retainedOutputPageBytes-len(priorMatch)) + "before\nneedle"
	const appended = " complete\n"
	partialLineStart := int64(retainedOutputPageBytes + len("before\n"))
	if err := os.WriteFile(initialPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial output: %v", err)
	}
	command := "cat " + shellQuote(initialPath) +
		"; while [ ! -e " + shellQuote(appendRelease) + " ]; do sleep 0.01; done" +
		"; printf ' complete\\n'" +
		"; while [ ! -e " + shellQuote(exitRelease) + " ]; do sleep 0.01; done"
	startedRes := task7ExecTool(t, s, "shell", map[string]any{"command": command, "background": true})
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(startedRes), &started); err != nil || started.JobID == "" {
		t.Fatalf("decode started job: job=%q err=%v output=%s", started.JobID, err, startedRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(started.JobID)
		waitForShellDone(t, s.jobManager, started.JobID)
	})
	output := task7RunningOutput(t, s, started.JobID)
	task7AwaitOutputBytes(t, output, int64(len(initial)))

	ref := "job:" + started.JobID
	first := task7DecodeRetainedPage(t, task7ExecTool(t, s, "read_transcript", map[string]any{
		"transcript_ref": ref,
		"offset_bytes":   0,
	}))
	if first.JobStatus != "running" || first.Page.Data != initial[:retainedOutputPageBytes] || first.Continuation == nil || first.Continuation.OffsetBytes != retainedOutputPageBytes {
		t.Fatalf("first running page = %+v", first)
	}
	continuation := first.Continuation.OffsetBytes

	deferredRes := task7ExecTool(t, s, "read_transcript", map[string]any{
		"transcript_ref": ref,
		"output_match":   "^needle complete$",
	})
	var deferred retainedSearchResult
	if err := json.Unmarshal(toolResultJSON(deferredRes), &deferred); err != nil {
		t.Fatalf("decode deferred search: %v", err)
	}
	if deferred.JobStatus != "running" || deferred.OffsetBytes != 0 || deferred.TotalBytes != int64(len(initial)) ||
		!deferred.SearchComplete || len(deferred.Matches) != 1 || deferred.Matches[0].Line != "needle complete" ||
		deferred.Matches[0].LineStartByte != 0 || deferred.Continuation == nil || deferred.Continuation.OffsetBytes != partialLineStart {
		t.Fatalf("partial-line search was not deferred: %+v", deferred)
	}
	searchContinuation := deferred.Continuation.OffsetBytes

	if err := os.WriteFile(appendRelease, nil, 0o600); err != nil {
		t.Fatalf("release append: %v", err)
	}
	task7AwaitOutputBytes(t, output, int64(len(initial)+len(appended)))
	second := task7DecodeRetainedPage(t, task7ExecTool(t, s, "read_transcript", map[string]any{
		"transcript_ref": ref,
		"offset_bytes":   continuation,
	}))
	if second.JobStatus != "running" || second.Page.OffsetBytes != continuation || second.Page.TotalBytes != int64(len(initial)+len(appended)) ||
		second.Page.Data != initial[continuation:]+appended || second.Continuation != nil {
		t.Fatalf("continued page = %+v, want exact appended suffix", second)
	}
	if reconstructed := first.Page.Data + second.Page.Data; reconstructed != initial+appended {
		t.Fatalf("page reconstruction differs at byte stream boundary: got %d bytes want %d", len(reconstructed), len(initial)+len(appended))
	}

	completedLineRes := task7ExecTool(t, s, "read_transcript", map[string]any{
		"transcript_ref": ref,
		"output_match":   "^needle complete$",
		"offset_bytes":   searchContinuation,
	})
	var completedLine retainedSearchResult
	if err := json.Unmarshal(toolResultJSON(completedLineRes), &completedLine); err != nil {
		t.Fatalf("decode completed-line search: %v", err)
	}
	if completedLine.JobStatus != "running" || completedLine.OffsetBytes != partialLineStart ||
		completedLine.TotalBytes != int64(len(initial)+len(appended)) || !completedLine.SearchComplete || completedLine.Continuation != nil ||
		len(completedLine.Matches) != 1 || completedLine.Matches[0].Line != "needle complete" ||
		completedLine.Matches[0].LineStartByte != partialLineStart {
		t.Fatalf("completed partial line was not evaluated exactly once: %+v", completedLine)
	}
	if status := readJobStatus(t, s, started.JobID); status.Status != "running" {
		t.Fatalf("job_status before exit = %+v, want running", status)
	}

	if err := os.WriteFile(exitRelease, nil, 0o600); err != nil {
		t.Fatalf("release exit: %v", err)
	}
	waitForShellDone(t, s.jobManager, started.JobID)
	if status := readJobStatus(t, s, started.JobID); status.Status != "completed" {
		t.Fatalf("job_status after exit = %+v, want completed", status)
	}
	terminal := task7DecodeRetainedPage(t, task7ExecTool(t, s, "read_transcript", map[string]any{
		"transcript_ref": ref,
		"offset_bytes":   int64(len(initial) + len(appended)),
	}))
	if terminal.JobStatus != "terminal" || terminal.Page.OffsetBytes != int64(len(initial)+len(appended)) ||
		terminal.Page.TotalBytes != int64(len(initial)+len(appended)) || terminal.Page.BytesReturned != 0 ||
		terminal.Page.Data != "" || terminal.Continuation != nil {
		t.Fatalf("terminal EOF page = %+v", terminal)
	}
	if !slices.Equal([]string{first.JobStatus, second.JobStatus, terminal.JobStatus}, []string{"running", "running", "terminal"}) {
		t.Fatalf("dishonest retained status transition: %q -> %q -> %q", first.JobStatus, second.JobStatus, terminal.JobStatus)
	}
}
