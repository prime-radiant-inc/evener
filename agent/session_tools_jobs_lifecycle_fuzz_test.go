//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzJobtoolsLifecycleProgram drives the job-tool lifecycle through a real
// parent Session, a real durable shell job, and a live delegated child. The
// child model is parked at the provider boundary so a delegate_send can always
// steer a live turn without a host process, network call, or wall-clock race.
//
// The program verifies durable job identity across the tool results, stable list
// rendering on unchanged state, bounded/readable output, the live-steer wait
// contract, parent callback routing, and the watch create/list/inspect/clear
// lifecycle. The existing FuzzJobtoolsExec owns adversarial handler maps; this
// target specifically covers the public wrapper and cross-session lifecycle.
// job_read_output intentionally is not model-registered, so its real handler is
// called directly, matching the production boundary enforced by its unit test.
func FuzzJobtoolsLifecycleProgram(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{255, 254, 253, 252, 251, 250, 249, 248})
	// The fixed replay drives all eleven controls consumed below: every invalid
	// delegate mode, both nested-list states, every read-window mode, and each
	// watch-message variant. Short corpus inputs still exercise defaulting.
	for readMode := byte(0); readMode < 6; readMode++ {
		f.Add([]byte{
			readMode % 5,        // invalid delegate shape
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

		for _, name := range []string{"delegate", "delegate_send", "job_status", "job_list", "job_watch"} {
			if registered := root.reg.Get(name); registered == nil || registered.Exec == nil {
				t.Fatalf("jobtools lifecycle: %q is not executable", name)
			}
		}

		// A malformed delegate request must fail before it mints a child or a job.
		beforeChildren := len(root.subagents.sessions())
		beforeJobs := len(root.jobManager.list(listFilter{}))
		invalidDelegate, err := delegateTool(context.Background(), root, jtlpInvalidDelegateArgs(r), jobToolResultDefaultMaxChar)
		if err == nil || invalidDelegate != "" {
			t.Fatalf("invalid delegate = (%q, %v), want empty result plus error", invalidDelegate, err)
		}
		if got := len(root.subagents.sessions()); got != beforeChildren {
			t.Fatalf("invalid delegate changed child count from %d to %d", beforeChildren, got)
		}
		if got := len(root.jobManager.list(listFilter{})); got != beforeJobs {
			t.Fatalf("invalid delegate changed job count from %d to %d", beforeJobs, got)
		}

		createdCall := jtlpExecute(t, root, "delegate", map[string]any{
			"task":                 jtlpMessage(r),
			"delegation_allowance": float64(0),
		})
		if createdCall.IsError {
			t.Fatalf("delegate tool failed: %s", createdCall.Output)
		}
		var created delegateToolResult
		jtlpDecode(t, createdCall, &created)
		if created.JobID == "" || created.DelegateID == "" || created.Status != string(jobstore.StatusRunning) || !created.RunningInBackground {
			t.Fatalf("delegate result = %+v, want a live background delegate", created)
		}
		if _, rec, err := root.nestedOrLocalJobManager(created.JobID); err != nil || rec == nil || rec.DelegateID != created.DelegateID || rec.Type != jobstore.JobDelegate {
			t.Fatalf("delegate durable record = (%+v, %v), want live delegate %q", rec, err, created.DelegateID)
		}
		_, childID, err := decodeRef(created.TranscriptRef)
		if err != nil {
			t.Fatalf("decode delegate transcript ref %q: %v", created.TranscriptRef, err)
		}
		child := root.subagents.get(childID)
		if child == nil || child.sess == nil || child.sess.cfg.spawn.parentSessionID != root.ID() {
			t.Fatalf("delegate child linkage missing for %q", childID)
		}

		// The child is deliberately still live, so a positive wait is ignored rather
		// than blocking. That is the user-facing contract of a live steer.
		steerCall := jtlpExecute(t, root, "delegate_send", map[string]any{
			"to":          created.DelegateID,
			"message":     jtlpMessage(r),
			"max_wait_ms": float64(1 + r.intn(7)),
		})
		if steerCall.IsError {
			t.Fatalf("delegate_send live steer failed: %s", steerCall.Output)
		}
		var steered delegateSendResult
		jtlpDecode(t, steerCall, &steered)
		if steered.Action != "steered" || steered.Status != string(jobstore.StatusRunning) || steered.WaitIgnoredReason == "" {
			t.Fatalf("delegate_send live steer = %+v, want running steered result with wait note", steered)
		}

		// Public delegate_send refuses caller, while the child runtime route itself
		// is allowed and must enqueue its callback on the real parent session.
		if got, err := delegateSendTool(context.Background(), child.sess, map[string]any{
			"to": "caller", "message": jtlpMessage(r),
		}, jobToolResultDefaultMaxChar); err == nil || got != "" {
			t.Fatalf("public caller delegate_send = (%#v, %v), want clean rejection", got, err)
		}
		callbackText := "callback " + jtlpMessage(r)
		callback := child.sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target: runtimeMessageAliasCaller, Message: callbackText,
		})
		if callback.Err != nil || !callback.Delivered || callback.MessageType != "runtime" {
			t.Fatalf("child callback = %+v, want delivered runtime callback", callback)
		}
		queue := root.SteeringQueueSnapshot()
		if len(queue) == 0 || queue[len(queue)-1].Text != callbackText {
			t.Fatalf("parent steering queue = %+v, want callback %q", queue, callbackText)
		}

		shell := jtlpSeedTerminalShell(t, root, r)

		for _, jobID := range []string{shell.JobID, created.JobID} {
			statusCall := jtlpExecute(t, root, "job_status", map[string]any{"job_id": jobID})
			if statusCall.IsError {
				t.Fatalf("job_status(%q) failed: %s", jobID, statusCall.Output)
			}
			var status jobStatusResult
			jtlpDecode(t, statusCall, &status)
			if status.JobID != jobID || status.Status == "" || !utf8.ValidString(status.TranscriptRef) {
				t.Fatalf("job_status(%q) = %+v", jobID, status)
			}
		}
		if rejected := jtlpExecute(t, root, "job_status", map[string]any{"job_id": created.DelegateID}); !rejected.IsError {
			t.Fatal("job_status accepted a delegate_id")
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
		if string(toolResultJSON(first)) != string(toolResultJSON(second)) || first.Output != second.Output {
			t.Fatalf("job_list changed without a mutation\nfirst:  %s\nsecond: %s", toolResultJSON(first), toolResultJSON(second))
		}
		var list jobListResult
		jtlpDecode(t, first, &list)
		if list.Count != len(list.Jobs) || !jtlpContainsJob(list.Jobs, shell.JobID) || !jtlpContainsJob(list.Jobs, created.JobID) {
			t.Fatalf("job_list state = %#v, want shell and delegate rows", list)
		}

		readValue, err := jobReadOutputTool(context.Background(), root, jtlpReadArgs(r, shell.JobID), jobToolResultDefaultMaxChar)
		if err != nil {
			t.Fatalf("job_read_output failed: %v", err)
		}
		readState, ok := readValue.(tooldefs.StateResult)
		if !ok {
			t.Fatalf("job_read_output type = %T", readValue)
		}
		var read jobReadOutputResult
		jtlpDecodeValue(t, readState.State, &read)
		if read.JobID != shell.JobID || !utf8.ValidString(read.Content) || read.TotalBytes == 0 {
			t.Fatalf("job_read_output = %+v", read)
		}
		if read.Grep != nil && !strings.Contains(strings.Join(jtlpMatchLines(read.Matches), "\n"), "needle") {
			t.Fatalf("grep read omitted the seeded needle: %+v", read)
		}

		watchCall := jtlpExecute(t, root, "job_watch", map[string]any{
			"operation":    "create",
			"source":       created.JobID,
			"output_match": "watch-" + jtlpMessage(r),
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

func jtlpInvalidDelegateArgs(r *jtlpReader) map[string]any {
	switch r.intn(5) {
	case 0:
		return map[string]any{"task": "   "}
	case 1:
		return map[string]any{"task": "valid", "sandbox_net": "false"}
	case 2:
		return map[string]any{"task": "valid", "max_wait_ms": float64(-1)}
	case 3:
		return map[string]any{"task": "valid", "delegation_allowance": float64(-1)}
	default:
		return map[string]any{"task": "valid", "isolation": "unsupported"}
	}
}

func jtlpNewRootSession(t *testing.T) *Session {
	t.Helper()
	gate := make(chan struct{})
	var release sync.Once
	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		LLMSleep:         func(context.Context, time.Duration) error { return nil },
		clock:            agenttest.NewFakeClock(),
	}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		environmentInfo:     jtlpEnvironmentInfo,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
		childClientFactory: func() *llm.Client {
			client := llm.NewClient()
			client.Register(&agenttest.ScriptedAdapter{
				Provider: "openai",
				Responder: func(llm.Request) llm.Response {
					<-gate
					return agenttest.FinalResponse("lifecycle child complete")
				},
			})
			return client
		},
	}
	root := newSession(t, withConfig(cfg))
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		if root.jobManager != nil {
			root.jobManager.abandonRunningJobs()
		}
	})
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

func jtlpReadArgs(r *jtlpReader, jobID string) map[string]any {
	args := map[string]any{"job_id": jobID}
	switch r.intn(6) {
	case 0:
		args["grep"] = "needle"
	case 1:
		args["head_lines"] = float64(1)
	case 2:
		args["tail_lines"] = float64(1)
	case 3:
		args["head_lines"] = float64(1)
		args["tail_lines"] = float64(1)
	case 4:
		args["from_line"] = float64(1)
		args["line_count"] = float64(1)
	}
	return args
}

func jtlpContainsJob(jobs []jobListEntry, id string) bool {
	for _, job := range jobs {
		if job.JobID == id {
			return true
		}
	}
	return false
}

func jtlpMatchLines(matches *[]jobOutputMatch) []string {
	if matches == nil {
		return nil
	}
	lines := make([]string, 0, len(*matches))
	for _, match := range *matches {
		lines = append(lines, match.Line)
	}
	return lines
}

func jtlpContainsWatch(watches []jobWatchInspectToolResult, id string) bool {
	for _, watch := range watches {
		if watch.WatchID == id {
			return true
		}
	}
	return false
}
