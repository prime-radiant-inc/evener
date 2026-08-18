//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzJobtoolsLifecycleProgram drives the job-tool lifecycle through a real
// parent Session and a real durable shell job.
//
// The program verifies durable job identity across the tool results, stable
// list rendering on unchanged state, and the watch create/list/inspect/clear
// lifecycle. The existing FuzzJobtoolsExec owns adversarial handler maps; this
// target specifically covers the public wrapper and shell lifecycle.
func FuzzJobtoolsLifecycleProgram(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{255, 254, 253, 252, 251, 250, 249, 248})
	// The fixed replay drives every shell, list, and watch-message control below.
	// Short corpus inputs still exercise defaulting.
	for readMode := byte(0); readMode < 6; readMode++ {
		f.Add([]byte{
			1, 2, 3, 4, 0, 1, 2, // message and shell controls
			readMode & 1, // include_nested
			readMode,     // read-window mode
			5 - readMode, // watch message
		})
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &jtlpReader{data: data}
		root := jtlpNewRootSession(t)
		freezeClock(root.jobManager)

		for _, name := range []string{"job_status", "job_list", "job_watch"} {
			if registered := root.reg.Get(name); registered == nil || registered.Exec == nil {
				t.Fatalf("jobtools lifecycle: %q is not executable", name)
			}
		}

		shell := jtlpSeedTerminalShell(t, root, r)

		statusCall := jtlpExecute(t, root, "job_status", map[string]any{"target": shell.JobID})
		if statusCall.IsError {
			t.Fatalf("job_status(%q) failed: %s", shell.JobID, statusCall.Output)
		}
		var status jobStatusResult
		jtlpDecode(t, statusCall, &status)
		if status.JobID != shell.JobID || status.Status == "" || !utf8.ValidString(status.TranscriptRef) {
			t.Fatalf("job_status(%q) = %+v", shell.JobID, status)
		}

		listArgs := map[string]any{"limit": float64(defaultJobListLimit), "include_nested": r.bool()}
		first := jtlpExecute(t, root, "job_list", listArgs)
		second := jtlpExecute(t, root, "job_list", listArgs)
		if first.IsError || second.IsError {
			t.Fatalf("job_list errors = (%q, %q)", first.Output, second.Output)
		}
		if len(first.ToolState) == 0 || len(second.ToolState) == 0 {
			t.Fatalf("job_list public wrapper omitted structured state: (%q, %q)", first.ToolState, second.ToolState)
		}
		var list, secondList jobListResult
		jtlpDecode(t, first, &list)
		jtlpDecode(t, second, &secondList)
		if list.Count != len(list.Items) || !jtlpContainsJob(list.Items, shell.JobID) {
			t.Fatalf("job_list state = %#v, want shell row", list)
		}
		if secondList.Count != list.Count || !jtlpContainsJob(secondList.Items, shell.JobID) {
			t.Fatalf("second job_list state = %#v, want same stable shell identity as %#v", secondList, list)
		}

		watchCall := jtlpExecute(t, root, "job_watch", map[string]any{
			"operation": "create",
			"source":    "self",
			"events":    []string{"communicate"},
		})
		if watchCall.IsError {
			t.Fatalf("job_watch create failed: %s", watchCall.Output)
		}
		var watch jobWatchToolResult
		jtlpDecode(t, watchCall, &watch)
		if watch.WatchID == "" || !watch.Watching {
			t.Fatalf("job_watch create = %+v", watch)
		}

		listWatch := jtlpExecute(t, root, "job_watch", map[string]any{"operation": "list"})
		if listWatch.IsError {
			t.Fatalf("job_watch list failed: %s", listWatch.Output)
		}
		var watches jobWatchListToolResult
		jtlpDecode(t, listWatch, &watches)
		if !jtlpContainsWatch(watches.Watches, watch.WatchID) {
			t.Fatalf("job_watch list = %+v, want %q", watches, watch.WatchID)
		}

		inspect := jtlpExecute(t, root, "job_watch", map[string]any{"operation": "inspect", "watch_id": watch.WatchID})
		if inspect.IsError {
			t.Fatalf("job_watch inspect failed: %s", inspect.Output)
		}
		var inspected jobWatchInspectToolResult
		jtlpDecode(t, inspect, &inspected)
		if !inspected.Watching || inspected.WatchID != watch.WatchID {
			t.Fatalf("job_watch inspect = %+v", inspected)
		}

		cleared := jtlpExecute(t, root, "job_watch", map[string]any{"operation": "clear", "watch_id": watch.WatchID})
		if cleared.IsError {
			t.Fatalf("job_watch clear failed: %s", cleared.Output)
		}
		jtlpDecode(t, cleared, &watch)
		if watch.Watching {
			t.Fatalf("job_watch clear left watch active: %+v", watch)
		}
		if got := root.jobManager.watchCount(); got != 0 {
			t.Fatalf("job_watch clear left %d live watches", got)
		}
		listedAfterClear := jtlpExecute(t, root, "job_watch", map[string]any{"operation": "list"})
		if listedAfterClear.IsError {
			t.Fatalf("job_watch list after clear failed: %s", listedAfterClear.Output)
		}
		var watchesAfterClear jobWatchListToolResult
		jtlpDecode(t, listedAfterClear, &watchesAfterClear)
		if jtlpContainsWatch(watchesAfterClear.Watches, watch.WatchID) {
			t.Fatalf("job_watch list retained cleared watch %q: %+v", watch.WatchID, watchesAfterClear)
		}
		inspectAfterClear := jtlpExecute(t, root, "job_watch", map[string]any{"operation": "inspect", "watch_id": watch.WatchID})
		if inspectAfterClear.IsError {
			t.Fatalf("job_watch inspect after clear failed: %s", inspectAfterClear.Output)
		}
		var clearedInspect jobWatchInspectToolResult
		jtlpDecode(t, inspectAfterClear, &clearedInspect)
		if clearedInspect.Watching || clearedInspect.WatchID != watch.WatchID {
			t.Fatalf("job_watch inspect retained cleared watch: %+v", clearedInspect)
		}
	})
}

type jtlpReader struct {
	data []byte
	pos  int
}

func (r *jtlpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *jtlpReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *jtlpReader) bool() bool { return r.next()&1 == 1 }

func jtlpMessage(r *jtlpReader) string {
	return []string{"alpha", "needle", "multi line", "unicode", "quoted value"}[r.intn(5)]
}

func jtlpNewRootSession(t *testing.T) *Session {
	t.Helper()
	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		clock:            agenttest.NewFakeClock(),
	}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		environmentInfo:     jtlpEnvironmentInfo,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
		metaFS:              afero.NewMemMapFs(),
	}
	root := newSession(t, withConfig(cfg))
	return root
}

func jtlpEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "jtlp",
		OSVersion:  "jtlp",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func jtlpExecute(t *testing.T, s *Session, name string, args map[string]any) tooldefs.ExecResult {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal %s args: %v", name, err)
	}
	return s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "jtlp-" + name,
		Name:      name,
		Arguments: b,
		Type:      "function",
	})
}

func jtlpDecode(t *testing.T, res tooldefs.ExecResult, out any) {
	t.Helper()
	if err := json.Unmarshal(toolResultJSON(res), out); err != nil {
		t.Fatalf("decode %s result %q: %v", res.ToolName, res.Output, err)
	}
}

func jtlpDecodeValue(t *testing.T, value any, out any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal tool state: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode tool state %q: %v", b, err)
	}
}

func jtlpSeedTerminalShell(t *testing.T, s *Session, r *jtlpReader) *jobstore.JobRecord {
	t.Helper()
	rec, err := s.jobManager.createShell(createShellOpts{Command: "jtlp shell " + jtlpMessage(r)})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	s.jobManager.mu.Lock()
	run := s.jobManager.running[rec.JobID]
	s.jobManager.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("missing shell runtime for %q", rec.JobID)
	}
	output := []byte("first line\nneedle " + jtlpMessage(r) + "\nlast line\n")
	if _, err := s.jobManager.appendJobOutput(rec.JobID, run.output, output); err != nil {
		t.Fatalf("append shell output: %v", err)
	}
	if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "jtlp_done", nil); err != nil {
		t.Fatalf("finalize shell: %v", err)
	}
	return rec
}

func jtlpContainsJob(jobs []jobListEntry, id string) bool {
	for _, job := range jobs {
		if job.JobID == id {
			return true
		}
	}
	return false
}

func jtlpContainsWatch(watches []jobWatchInspectToolResult, id string) bool {
	for _, watch := range watches {
		if watch.WatchID == id {
			return true
		}
	}
	return false
}
