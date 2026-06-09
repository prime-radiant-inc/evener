package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

func TestCreateDelegateForegroundCompletesWithStructuredResult(t *testing.T) {
	var sawSchema bool
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				sawSchema = communicateOutputSchemaHasProperty(req, "summary")
				return communicateWithStructured("delegate prose", map[string]any{
					"message": "delegate prose",
					"summary": "structured summary",
					"count":   2,
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "summarize the work",
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
				"count":   map[string]any{"type": "number"},
			},
			"required": []string{"message", "summary"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" {
		t.Fatal("job_id is empty")
	}
	if res.Type != string(jobstore.JobDelegate) || res.Status != jobstore.StatusCompleted {
		t.Fatalf("result = %+v, want completed delegate", res)
	}
	if !strings.HasPrefix(res.TranscriptRef, "local:") {
		t.Fatalf("transcript_ref = %q, want local ref", res.TranscriptRef)
	}
	if !strings.Contains(res.Output, "delegate prose") {
		t.Fatalf("output = %q, want prose result", res.Output)
	}
	if !res.StructuredResultValid {
		t.Fatal("structured_result_valid = false, want true")
	}
	structured, ok := res.StructuredResult.(map[string]any)
	if !ok {
		t.Fatalf("structured_result has type %T, want map", res.StructuredResult)
	}
	if structured["summary"] != "structured summary" {
		t.Fatalf("structured_result = %+v, want summary", structured)
	}
	if !sawSchema {
		t.Fatal("child communicate tool did not receive delegate result schema")
	}

	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v, want one delegate job", jobs)
	}
	if jobs[0].JobID != res.JobID || jobs[0].Status != jobstore.StatusCompleted {
		t.Fatalf("job record = %+v, want completed job %s", jobs[0], res.JobID)
	}
}

func TestCreateDelegateBackgroundReturnsRunningJob(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithStructured("background complete", map[string]any{
					"message": "background complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "run in the background",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" || res.TranscriptRef == "" {
		t.Fatalf("result = %+v, want job_id and transcript_ref", res)
	}
	if res.Type != string(jobstore.JobDelegate) ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TimedOut {
		t.Fatalf("result = %+v, want running background delegate", res)
	}

	_, _ = sess.jobManager.stop(res.JobID)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestCreateDelegateForegroundTimeoutLeavesChildRunning(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithStructured("timeout child complete", map[string]any{
					"message": "timeout child complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "wait past foreground timeout",
		Background:     false,
		BlockTimeoutMS: 1000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
			},
			"required": []string{"message"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.Reason != "foreground_timeout" || !res.RunningInBackground || !res.TimedOut {
		t.Fatalf("result = %+v, want foreground_timeout/background/timed_out", res)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	sub.mu.Lock()
	cancelled := sub.cancelRequested
	running := sub.running
	sub.mu.Unlock()
	if cancelled || !running {
		t.Fatalf("child cancelled=%v running=%v, want not cancelled and running", cancelled, running)
	}

	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record after releasing child = %+v, want completed", rec)
	}
}

func TestDelegateStopMapsToCancelled(t *testing.T) {
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "run until stopped",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}

	out, err := jobStopTool(context.Background(), sess, map[string]any{
		"job_id":           res.JobID,
		"block":            true,
		"block_timeout_ms": 1000,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobStopTool: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal([]byte(out), &stop); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, out)
	}
	if stop.JobID != res.JobID || stop.Status != string(jobstore.StatusCancelled) || stop.Reason == nil || *stop.Reason != "stopped_by_parent" {
		t.Fatalf("job_stop = %+v, want cancelled/stopped_by_parent", stop)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent", rec)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	sub.mu.Lock()
	cancelRequested := sub.cancelRequested
	status := sub.status
	sub.mu.Unlock()
	if !cancelRequested || status != SubagentCancelled {
		t.Fatalf("child cancelRequested=%v status=%q, want cancelRequested=true status=%q", cancelRequested, status, SubagentCancelled)
	}
}

func TestCreateDelegateSignalCancelsChildAfterSubagentDrain(t *testing.T) {
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "run until signaled after drain",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	drained := sess.subagents.drainForClose()
	t.Cleanup(func() {
		for _, drainedSub := range drained {
			drainedSub.sess.close(false)
		}
	})
	if got := sess.subagents.get(childID); got != nil {
		t.Fatalf("subagent %s still tracked after drain", childID)
	}

	run := runningDelegateJob(t, sess.jobManager, res.JobID)
	run.signal()

	sub.mu.Lock()
	cancelRequested := sub.cancelRequested
	sub.mu.Unlock()
	if !cancelRequested {
		t.Fatal("delegate signal did not mark drained child cancelRequested")
	}

	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent after drained-map signal", rec)
	}
}

func TestCreateDelegateDurableRecordKeepsOutputPathAndTranscriptRef(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("durable delegate complete", map[string]any{
					"message": "durable delegate complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "write durable delegate metadata",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	sess.jobManager.mu.Lock()
	run := sess.jobManager.running[res.JobID]
	sess.jobManager.mu.Unlock()
	if run != nil {
		t.Fatalf("job %s still running after foreground completion", res.JobID)
	}
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.OutputPath == "" {
		t.Fatalf("durable record missing output_path: %+v", rec)
	}
	if rec.TranscriptRef != res.TranscriptRef {
		t.Fatalf("transcript_ref = %q, want %q", rec.TranscriptRef, res.TranscriptRef)
	}
}

func TestCreateDelegateDurableRecordKeepsStructuredResult(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("durable structured delegate complete", map[string]any{
					"summary": "durable",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "persist structured delegate result",
		Background:     true,
		BlockTimeoutMS: 5000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string"},
			},
			"required": []string{"summary"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	structured, ok := rec.StructuredResult.(map[string]any)
	if !ok || structured["summary"] != "durable" {
		t.Fatalf("durable structured result = %+v, want summary=durable", rec.StructuredResult)
	}
	output, _, _, err := sess.jobManager.readOutput(res.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read delegate output: %v", err)
	}
	if !strings.Contains(output, "durable structured delegate complete") {
		t.Fatalf("delegate output = %q, want prose copied to job output", output)
	}
}

func TestCreateDelegateMarksChildConsumedAfterDurableFinish(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("consume child result")
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("resume still works")
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "consume retained child after durable ownership",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not retained", childID)
	}
	sub.mu.Lock()
	consumed := sub.resultConsumed
	closed := sub.closed
	sub.mu.Unlock()
	if !consumed || closed {
		t.Fatalf("child consumed=%v closed=%v, want consumed and retained open", consumed, closed)
	}

	resume := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:        res.JobID,
		Message:       "resume after consumption",
		Background:    false,
		BackgroundSet: true,
	})
	if resume.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", resume.Err)
	}
	if resume.Status != jobstore.StatusCompleted || !strings.Contains(resume.Output, "resume still works") {
		t.Fatalf("resume result = %+v, want completed resumed delegate", resume)
	}
}

func TestDelegateNotificationCarriesTranscriptRef(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("notification delegate complete", map[string]any{
					"message": "notification delegate complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	var mu sync.Mutex
	var queued []jobNotification
	sess.jobManager.enqueue = func(n jobNotification) {
		mu.Lock()
		defer mu.Unlock()
		queued = append(queued, n)
	}

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "finish and notify",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" || res.TranscriptRef == "" {
		t.Fatalf("result = %+v, want job_id and transcript_ref", res)
	}

	var got []jobNotification
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got = append([]jobNotification(nil), queued...)
		mu.Unlock()
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("queued notifications = %+v, want exactly one", got)
	}

	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	n := got[0]
	if n.JobID != res.JobID {
		t.Fatalf("notification job_id = %q, want %q", n.JobID, res.JobID)
	}
	if n.JobType != string(jobstore.JobDelegate) {
		t.Fatalf("notification job_type = %q, want %q", n.JobType, jobstore.JobDelegate)
	}
	if n.TranscriptRef == "" || n.TranscriptRef != res.TranscriptRef || n.TranscriptRef != rec.TranscriptRef {
		t.Fatalf("notification transcript_ref = %q, result = %q, record = %q", n.TranscriptRef, res.TranscriptRef, rec.TranscriptRef)
	}
}

func TestCreateDelegateForegroundFinalizeFailureReturns(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("finalize failure child complete", map[string]any{
					"message": "finalize failure child complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	appendErr := errors.New("append failed")
	var finishAttempts atomic.Int32
	origAppend := sess.jobManager.appendEvent
	defer func() { sess.jobManager.appendEvent = origAppend }()
	sess.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			finishAttempts.Add(1)
			return appendErr
		}
		return origAppend(e)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan delegateResult, 1)
	go func() {
		done <- sess.createDelegate(ctx, delegateArgs{
			Task:           "finish while append fails",
			Background:     false,
			BlockTimeoutMS: 5000,
		})
	}()

	var res delegateResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("createDelegate hung after finalization append failure")
	}
	if res.Err == nil || !errors.Is(res.Err, appendErr) {
		t.Fatalf("result error = %v, want append failure", res.Err)
	}
	if res.Reason != "finalize_failed" {
		t.Fatalf("result = %+v, want finalize_failed", res)
	}
	if finishAttempts.Load() == 0 {
		t.Fatal("job_finished append was not attempted")
	}

	sess.jobManager.appendEvent = origAppend
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	if err := sess.finalizeDelegate(res.JobID, childID, nil); err != nil {
		t.Fatalf("cleanup finalizeDelegate: %v", err)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestFinalizeDelegateRetryAfterDurableFailureDoesNotDuplicateOutput(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  "retry complete",
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	appendErr := errors.New("job_finished failed")
	var finishAttempts atomic.Int32
	origAppend := parent.jobManager.appendEvent
	parent.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			if finishAttempts.Add(1) == 1 {
				return appendErr
			}
		}
		return origAppend(e)
	}
	err = parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	if !errors.Is(err, appendErr) {
		t.Fatalf("first finalizeDelegate error = %v, want %v", err, appendErr)
	}

	parent.jobManager.appendEvent = origAppend
	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("retry finalizeDelegate: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if got := strings.Count(output, "retry complete"); got != 1 {
		t.Fatalf("output contains delegate result %d times, want 1: %q", got, output)
	}
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record status = %q, want completed", rec.Status)
	}
}

func TestCreateDelegateForegroundOutputAppendFailureReturns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				close(started)
				<-release
				return communicateWithStructured("append failure child complete", map[string]any{
					"message": "append failure child complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	done := make(chan delegateResult, 1)
	go func() {
		done <- sess.createDelegate(context.Background(), delegateArgs{
			Task:           "finish while output append fails",
			Background:     false,
			BlockTimeoutMS: 5000,
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	run := waitForRunningDelegateJob(t, sess.jobManager)
	appendErr := run.output.Close()
	if appendErr != nil {
		t.Fatalf("close delegate output: %v", appendErr)
	}

	releaseOnce.Do(func() { close(release) })

	var res delegateResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("createDelegate hung after output append failure")
	}
	if res.Err == nil {
		t.Fatal("result error is nil, want output append failure")
	}
	if res.Reason != "finalize_failed" {
		t.Fatalf("result = %+v, want finalize_failed", res)
	}

	reopened, err := jobstore.OpenOutput(run.rec.OutputPath, 0)
	if err != nil {
		t.Fatalf("reopen delegate output: %v", err)
	}
	sess.jobManager.mu.Lock()
	run.output = reopened
	sess.jobManager.mu.Unlock()
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	if err := sess.finalizeDelegate(res.JobID, childID, nil); err != nil {
		t.Fatalf("cleanup finalizeDelegate: %v", err)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob(t *testing.T) {
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	}
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("second complete")
	})
	adapter.mu.Unlock()

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "run again",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "resumed" ||
		res.JobID == "" ||
		res.JobID == first.JobID ||
		res.ResumedFromJobID != first.JobID ||
		res.TranscriptRef != first.TranscriptRef ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground {
		t.Fatalf("result = %+v, want resumed new running delegate job from %s", res, first.JobID)
	}

	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCompleted || rec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("resumed record = %+v, want completed with same transcript ref", rec)
	}
	output, _, _, err := sess.jobManager.readOutput(res.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read resumed output: %v", err)
	}
	if !strings.Contains(output, "second complete") {
		t.Fatalf("resumed output = %q, want second run output", output)
	}
	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 2 {
		t.Fatalf("delegate jobs = %+v, want two durable jobs", jobs)
	}
}

func TestSendDelegateMessageTerminalDelegateForegroundResumeTimeoutLeavesChildRunning(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.JobID,
		Message:        "run again",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 1000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "resumed" ||
		res.JobID == "" ||
		res.JobID == first.JobID ||
		res.ResumedFromJobID != first.JobID ||
		res.Status != jobstore.StatusRunning ||
		res.Reason != "foreground_timeout" ||
		!res.RunningInBackground ||
		!res.TimedOut ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want resumed foreground timeout running in background", res)
	}
	select {
	case <-adapter.secondStarted:
	default:
		t.Fatal("resumed delegate did not start before foreground timeout")
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	sub.mu.Lock()
	cancelled := sub.cancelRequested
	running := sub.running
	sub.mu.Unlock()
	if cancelled || !running {
		t.Fatalf("child cancelled=%v running=%v, want not cancelled and running", cancelled, running)
	}

	_, _ = sess.jobManager.stop(res.JobID)
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestSendDelegateMessageTerminalDelegateFailOnFinished(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("terminal complete")
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish once",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:     first.JobID,
		Message:    "must be live",
		OnFinished: "fail",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_terminal") {
		t.Fatalf("error = %v, want target_terminal", res.Err)
	}
	if jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != 1 {
		t.Fatalf("delegate jobs = %+v, want no new job", jobs)
	}
}

func TestSendDelegateMessageObservedTerminalRunningRecordFailReturnsTargetTerminal(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  "already done",
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "already terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
			t.Fatalf("cleanup finalizeDelegate: %v", err)
		}
		waitForShellDone(t, parent.jobManager, run.rec.JobID)
	})

	before := parent.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := parent.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:     run.rec.JobID,
		Message:    "must still be live",
		OnFinished: "fail",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_terminal") {
		t.Fatalf("error = %v, want target_terminal", res.Err)
	}
	if strings.Contains(res.Err.Error(), "not_controllable") {
		t.Fatalf("error = %v, must not report not_controllable", res.Err)
	}
	after := parent.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
}

func TestSendDelegateMessageTerminalDelegateResumeSteersActiveRun(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	second := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "run again",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "resumed" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want resumed new running delegate job", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "steer current run",
	})
	if res.Err != nil {
		if strings.Contains(res.Err.Error(), "delegate_session_busy") {
			t.Fatalf("sendDelegateMessage returned delegate_session_busy: %v", res.Err)
		}
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "sent" ||
		res.JobID != second.JobID ||
		res.JobID == first.JobID ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want sent to active resumed delegate job %s", res, second.JobID)
	}
	after := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "steer current run" {
		t.Fatalf("steering queue = %+v, want steered message", queue)
	}

	_, _ = sess.jobManager.stop(second.JobID)
	waitForShellDone(t, sess.jobManager, second.JobID)
}

func TestSendDelegateMessageTerminalResumeWaitsForDelegateJobAttachment(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	attachStarted := make(chan struct{})
	releaseAttach := make(chan struct{})
	var attachOnce sync.Once
	origAppend := sess.jobManager.appendEvent
	defer func() { sess.jobManager.appendEvent = origAppend }()
	sess.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobStarted && e.Type == jobstore.JobDelegate && e.Task == "run again" {
			attachOnce.Do(func() { close(attachStarted) })
			<-releaseAttach
		}
		return origAppend(e)
	}

	firstResumeDone := make(chan sendMessageResult, 1)
	go func() {
		firstResumeDone <- sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  first.JobID,
			Message: "run again",
		})
	}()
	select {
	case <-attachStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate job attachment did not start")
	}

	secondResumeDone := make(chan sendMessageResult, 1)
	go func() {
		secondResumeDone <- sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  first.JobID,
			Message: "steer while attaching",
		})
	}()
	select {
	case res := <-secondResumeDone:
		t.Fatalf("second terminal resume returned before delegate job attached: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseAttach)
	var firstResume sendMessageResult
	select {
	case firstResume = <-firstResumeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first terminal resume did not return")
	}
	if firstResume.Err != nil {
		t.Fatalf("first terminal resume returned error: %v", firstResume.Err)
	}
	if firstResume.Action != "resumed" || firstResume.JobID == "" || firstResume.JobID == first.JobID {
		t.Fatalf("first terminal resume = %+v, want resumed new delegate job", firstResume)
	}

	var secondResume sendMessageResult
	select {
	case secondResume = <-secondResumeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second terminal resume did not return after attachment released")
	}
	if secondResume.Err != nil {
		t.Fatalf("second terminal resume returned error: %v", secondResume.Err)
	}
	if secondResume.Action != "sent" ||
		secondResume.JobID != firstResume.JobID ||
		secondResume.TranscriptRef != first.TranscriptRef {
		t.Fatalf("second terminal resume = %+v, want sent to active resumed job %s", secondResume, firstResume.JobID)
	}

	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "steer while attaching" {
		t.Fatalf("steering queue = %+v, want concurrent terminal resume steered", queue)
	}
	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 2 {
		t.Fatalf("delegate jobs = %+v, want original plus one resumed job", jobs)
	}

	_, _ = sess.jobManager.stop(firstResume.JobID)
	waitForShellDone(t, sess.jobManager, firstResume.JobID)
}

func TestSendDelegateMessageTerminalTargetFailDoesNotSteerLaterRun(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	second := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "run again",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "resumed" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want resumed new running delegate job", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:     first.JobID,
		Message:    "must not steer running job",
		OnFinished: "fail",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_terminal") {
		t.Fatalf("error = %v, want target_terminal", res.Err)
	}
	after := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	for _, entry := range sub.sess.SteeringQueueSnapshot() {
		if entry.Text == "must not steer running job" {
			t.Fatalf("terminal target message was steered to running job; queue = %+v", sub.sess.SteeringQueueSnapshot())
		}
	}

	_, _ = sess.jobManager.stop(second.JobID)
	waitForShellDone(t, sess.jobManager, second.JobID)
}

func TestSendDelegateMessageRunningDelegateTargetSteersWithoutNewJob(t *testing.T) {
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "stay running",
		Background: true,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "please adjust course",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "sent" ||
		res.JobID != first.JobID ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want sent to running delegate job", res)
	}
	after := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "please adjust course" {
		t.Fatalf("steering queue = %+v, want sent message", queue)
	}

	_, _ = sess.jobManager.stop(first.JobID)
	waitForShellDone(t, sess.jobManager, first.JobID)
}

func TestSendDelegateMessageRunningTargetHoldsRunLockThroughSteer(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	rec := &jobstore.JobRecord{
		JobID:         "job_atomic_delegate",
		Type:          jobstore.JobDelegate,
		Status:        jobstore.StatusRunning,
		TranscriptRef: encodeRef("", child.ID()),
	}

	child.mu.Lock()
	childLocked := true
	t.Cleanup(func() {
		if childLocked {
			child.mu.Unlock()
		}
	})

	done := make(chan sendMessageResult, 1)
	go func() {
		done <- parent.sendRunningDelegateMessage(rec.JobID, "atomic steer", rec)
	}()

	for deadline := time.Now().Add(time.Second); sub.mu.TryLock(); {
		sub.mu.Unlock()
		select {
		case res := <-done:
			t.Fatalf("sendRunningDelegateMessage returned before Steer could append: %+v", res)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("subagent run lock was not held while steering was blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}

	child.mu.Unlock()
	childLocked = false
	res := <-done
	if res.Err != nil {
		t.Fatalf("sendRunningDelegateMessage returned error: %v", res.Err)
	}
	queue := child.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "atomic steer" {
		t.Fatalf("steering queue = %+v, want atomic steer", queue)
	}
}

func TestFindRunningDelegateByTranscriptRefRejectsAmbiguousMatches(t *testing.T) {
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "stay running",
		Background: true,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}

	sess.jobManager.mu.Lock()
	sess.jobManager.running["job_duplicate_delegate"] = &runningJob{
		rec: &jobstore.JobRecord{
			JobID:         "job_duplicate_delegate",
			Type:          jobstore.JobDelegate,
			Status:        jobstore.StatusRunning,
			TranscriptRef: first.TranscriptRef,
		},
		durableStarted: true,
	}
	sess.jobManager.mu.Unlock()
	t.Cleanup(func() {
		sess.jobManager.mu.Lock()
		delete(sess.jobManager.running, "job_duplicate_delegate")
		sess.jobManager.mu.Unlock()
	})

	_, err := findRunningDelegateByTranscriptRef(sess.jobManager, first.TranscriptRef)
	if err == nil || !strings.Contains(err.Error(), "active_delegate_ambiguous") {
		t.Fatalf("findRunningDelegateByTranscriptRef error = %v, want active_delegate_ambiguous", err)
	}

	_, _ = sess.jobManager.stop(first.JobID)
	waitForShellDone(t, sess.jobManager, first.JobID)
}

func TestSendDelegateMessageAliasTargetDeliversRuntimeMessage(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess := newDelegateTestSession(t, c)

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "caller",
		Message: "runtime advisory",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Target != "caller" ||
		!res.Delivered ||
		res.Action != "sent" ||
		res.MessageType != "runtime" {
		t.Fatalf("result = %+v, want runtime delivered shape", res)
	}
	queue := sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "runtime advisory" {
		t.Fatalf("steering queue = %+v, want runtime advisory", queue)
	}
}

func TestSendDelegateMessageAliasFromSubagentSteersCaller(t *testing.T) {
	parent := newTestSession(t)
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	subCfg.spawn.parentSessionID = parent.ID()
	subCfg.spawn.parentSteer = parent.Steer
	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatalf("NewSession child: %v", err)
	}
	t.Cleanup(func() { child.Close() })

	res := child.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "caller",
		Message: "child advisory",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Target != "caller" || !res.Delivered || res.Action != "sent" || res.MessageType != "runtime" {
		t.Fatalf("result = %+v, want runtime alias delivery", res)
	}
	if queue := child.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("child steering queue = %+v, want no alias message", queue)
	}
	queue := parent.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "child advisory" {
		t.Fatalf("parent steering queue = %+v, want child advisory", queue)
	}
}

func newDelegateTestSession(t *testing.T, c *llm.Client) *Session {
	t.Helper()
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func runningDelegateJob(t *testing.T, jm *jobManager, jobID string) *runningJob {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		t.Fatalf("delegate job %s is not running", jobID)
	}
	return run
}

func waitForRunningDelegateJob(t *testing.T, jm *jobManager) *runningJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		for _, run := range jm.running {
			if run.rec.Type == jobstore.JobDelegate {
				jm.mu.Unlock()
				return run
			}
		}
		jm.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for running delegate job")
	return nil
}

type cancelAwareDelegateAdapter struct {
	name    string
	started chan struct{}
	once    sync.Once
}

func (a *cancelAwareDelegateAdapter) Name() string { return a.name }

func (a *cancelAwareDelegateAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.once.Do(func() { close(a.started) })
	<-ctx.Done()
	return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
}

func (a *cancelAwareDelegateAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

type resumeBlockingDelegateAdapter struct {
	name          string
	secondStarted chan struct{}
	once          sync.Once
	mu            sync.Mutex
	calls         int
}

func (a *resumeBlockingDelegateAdapter) Name() string { return a.name }

func (a *resumeBlockingDelegateAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()

	if call == 1 {
		resp := communicateWithDefaultOutput("first complete")
		resp.Provider = a.name
		if resp.Model == "" {
			resp.Model = req.Model
		}
		return resp, nil
	}

	a.once.Do(func() { close(a.secondStarted) })
	<-ctx.Done()
	return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
}

func (a *resumeBlockingDelegateAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func communicateWithStructured(message string, output map[string]any) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":     message,
		"await_reply": false,
		"output":      output,
	})
	return toolCallResponse(llm.ToolCallData{
		ID:        "delegate_communicate",
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	})
}

func communicateWithDefaultOutput(message string) llm.Response {
	return communicateWithStructured(message, map[string]any{
		"message":   message,
		"data":      map[string]any{},
		"artifacts": []string{},
	})
}

func communicateOutputSchemaHasProperty(req llm.Request, property string) bool {
	for _, td := range req.Tools {
		if td.Name != "communicate" {
			continue
		}
		params, ok := td.Parameters["properties"].(map[string]any)
		if !ok {
			return false
		}
		output, ok := params["output"].(map[string]any)
		if !ok {
			return false
		}
		props, ok := output["properties"].(map[string]any)
		if !ok {
			return false
		}
		_, ok = props[property]
		return ok
	}
	return false
}
