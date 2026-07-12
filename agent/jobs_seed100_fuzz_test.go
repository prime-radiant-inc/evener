//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// runAgentJobsSeed100 exercises deterministic guard, fault, recovery, and
// projection paths around the real durable job manager. It is called by the
// existing registered FuzzJobManagerErrorRecoveryProgram target.
func runAgentJobsSeed100(t *testing.T, text string) {
	t.Helper()
	if len(text) > 32 {
		text = text[:32]
	}
	seed100Jobs(t, text)
	seed100Nested(t)
	seed100Drain(t)
	seed100JobTools(t, text)
}

func seed100Jobs(t *testing.T, text string) {
	t.Helper()
	want := errors.New("seed fault")
	jm := newTestJM(t)
	freezeClock(jm)

	jm.appendEvent = func(jobstore.Event) error { return want }
	jm.appendEvents = nil
	if err := jm.appendJobEvents([]jobstore.Event{{}, {}}); !errors.Is(err, want) {
		t.Fatalf("append event fault = %v", err)
	}
	jm.appendEvent = jm.store.Append

	(*runningJob)(nil).closeDone()
	(*jobManager)(nil).noteJobActivity("job", "phase")
	jm.noteJobActivity("", "phase")
	jm.noteJobActivity("missing", "phase")
	jm.running["nil"] = nil
	_ = jm.liveWorkHandles()
	delete(jm.running, "nil")

	// Nil output and an already-closed output cover the append/read failures.
	if n, err := jm.appendJobOutput("missing", nil, []byte(text)); n != 0 || err != nil {
		t.Fatalf("nil append = %d, %v", n, err)
	}
	out, err := jobstore.OpenOutputNoSync(filepath.Join(t.TempDir(), "closed.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = jm.appendJobOutput("closed", out, []byte(text))
	_, _, _, _ = tailOutput(out, 1)
	_, _, _, _ = headOutput(out, 1)

	// Closed stores are the deterministic persistence fault boundary.
	closed := newTestJM(t)
	if err := closed.store.Close(); err != nil {
		t.Fatal(err)
	}
	_ = closed.reconcileLostJobs()
	_ = closed.clearUnrestoredActiveWatches()
	_, _ = closed.stop("missing")
	_ = closed.armPendingTerminalNotifications()

	// A delegate stop gate append failure is surfaced without signalling.
	rec := &jobstore.JobRecord{JobID: "delegate-stop", Type: jobstore.JobDelegate, DelegateID: "dlg_seed", Status: jobstore.StatusRunning}
	run := &runningJob{rec: rec, done: make(chan struct{}), signal: func() { t.Fatal("signalled after gate failure") }}
	jm.running[rec.JobID] = run
	jm.appendEvent = func(jobstore.Event) error { return want }
	if _, err := jm.stop(rec.JobID); !errors.Is(err, want) {
		t.Fatalf("stop gate fault = %v", err)
	}
	delete(jm.running, rec.JobID)
	jm.appendEvent = jm.store.Append

	// Finalize guards and a prepare failure require no asynchronous runtime.
	if err := jm.finalizeWithRunMode("missing", func(*runningJob) (jobstore.Status, string, *int, error) {
		return "", "", nil, want
	}, true); err != nil {
		t.Fatal(err)
	}
	prep := &runningJob{rec: &jobstore.JobRecord{JobID: "prepare", Status: jobstore.StatusRunning}, done: make(chan struct{})}
	jm.running["prepare"] = prep
	if err := jm.finalizeWithRunMode("prepare", func(*runningJob) (jobstore.Status, string, *int, error) {
		return "", "", nil, want
	}, true); !errors.Is(err, want) {
		t.Fatalf("prepare fault = %v", err)
	}
	delete(jm.running, "prepare")

	// Schema encoding/compilation/validation failures are distinct public states.
	for _, tc := range []struct{ value, schema any }{
		{map[string]any{"x": 1}, make(chan int)},
		{map[string]any{"x": 1}, map[string]any{"type": "not-a-real-type"}},
		{map[string]any{"x": 1}, map[string]any{"type": "string"}},
	} {
		if err := validateStructuredResult(tc.value, tc.schema); err == nil {
			t.Fatalf("schema %#v unexpectedly valid", tc.schema)
		}
	}

	// Mismatched terminal/run guards, nil callback guards, and forwarding faults.
	jm.armFinalizedJob(&runningJob{rec: &jobstore.JobRecord{JobID: "absent"}}, &terminalJob{})
	(*jobManager)(nil).markWatchOriginCallerCallbackDelivered("x")
	jm.markWatchOriginCallerCallbackDelivered("")
	jm.markWatchOriginCallerCallbackDelivered("missing")
	_ = jm.forwardPendingJobNotification(nil, nil)
	terminal := &terminalJob{notificationPendingAppended: true, notificationPending: jobstore.Event{}}
	jm.forward = func(jobstore.Event) error { return want }
	jm.parentJobID = "parent"
	frun := &runningJob{rec: &jobstore.JobRecord{JobID: "forward"}}
	if err := jm.forwardPendingJobNotification(frun, terminal); !errors.Is(err, want) {
		t.Fatalf("forward fault = %v", err)
	}

	// File helpers: invalid limits, open/stat failures, truncation, and metadata mismatch.
	missing := filepath.Join(t.TempDir(), "missing")
	_, _, _, _ = tailOutputFile(missing, -1, 0)
	_, _, _, _ = headOutputFile(missing, -1, 0)
	_, _, _, _ = tailOutputFile(missing, 1, 0)
	_, _, _, _ = headOutputFile(missing, 1, 0)
	dir := t.TempDir()
	_, _, _, _ = tailOutputFile(dir, 1, 0)
	_, _, _, _ = headOutputFile(dir, 1, 0)
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = tailOutputFile(path, 2, 9)
	_, _, _, _ = headOutputFile(path, 2, 9)
	_, _, _ = validatedOutputStatsForRecord(path, &jobstore.JobRecord{Status: jobstore.StatusCompleted, OutputBytes: 99})
	_ = cloneJobRecord(nil)

	jm.abandonRunningJob("missing")
	jm.closeStoreOnly()
}

func seed100Nested(t *testing.T) {
	t.Helper()
	bare := &Session{}
	_, _, _ = bare.nestedOrLocalJobManager("x")
	_, _, _, _, _ = (*Session)(nil).resolveDescendantJobOwner("x")
	_, _ = bare.stopNestedOrLocal("x")
	_ = bare.notControllableDescendantError("x")
	_, _ = bare.stopChildren("x")
	_ = bare.delegateChildSessionToCascade("x")
	_, _ = bare.stopDelegateSubtree(nil)
	_ = (*Session)(nil).directChildOwningDescendant("x")
	_ = bare.directDelegateJobForChild("x")

	root := newSession(t)
	closedChild := newSession(t)
	sub := &subagent{id: closedChild.ID(), sess: closedChild, closed: true}
	root.subagents.subs[sub.id] = sub
	_, _ = root.ownerJobManagerFor("missing")
	_ = root.delegateChildSessionToCascade("missing")

	jm := newTestJM(t)
	jm.forward = func(jobstore.Event) error { return errors.New("forward") }
	jm.parentJobID = "parent"
	_ = jm.forwardSnapshot(jobstore.Event{})
	if err := jm.store.Close(); err != nil {
		t.Fatal(err)
	}
	_ = jm.recoverForwardedTerminalEvents()
	_ = jm.recoverForwardedPendingNotifications()
	_ = jm.forwardEvent(jobstore.Event{})
	_ = jm.recoveredEventTime(&jobstore.JobRecord{})
}

func seed100Drain(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newSession(t)
	s.jobManager.running["delegate"] = &runningJob{rec: &jobstore.JobRecord{JobID: "delegate", Type: jobstore.JobDelegate}}
	_, _ = s.drainJobTree(ctx, make(chan time.Time))
	delete(s.jobManager.running, "delegate")
	_ = s.kickDriveTree(context.Background())
}

func seed100JobTools(t *testing.T, text string) {
	t.Helper()
	for _, fn := range []func() (any, error){
		func() (any, error) { return jobWatchTool(nil, nil, 1) },
		func() (any, error) { return jobStatusTool(nil, nil, 1) },
		func() (any, error) { return jobListTool(nil, nil, 1) },
		func() (any, error) { return jobStopTool(context.Background(), nil, nil, 1) },
	} {
		_, _ = fn()
	}

	// Validation branches are deterministic pure front-door parsing.
	watchCases := []map[string]any{
		{}, {"target": "x", "operation": "create"}, {"send": map[string]any{}, "operation": "create"},
		{"operation": "create"}, {"operation": "create", "source": "dlg_x"},
		{"operation": "list", "source": "x"}, {"operation": "inspect"},
		{"operation": "clear"}, {"operation": "wat", "source": "x"},
		{"operation": "create", "source": "*"}, {"operation": "create", "source": "x", "events": "bad"},
		{"operation": "create", "source": "x", "event_filter": "bad"},
		{"operation": "create", "source": "x", "event_filter": map[string]any{"unknown": "x"}},
		{"operation": "create", "source": "x", "event_filter": map[string]any{"tool_name": 1}},
	}
	for _, args := range watchCases {
		_, _ = watchArgsFromToolArgs(args)
	}
	_ = (&Session{}).watchListToolResultWithDescendantReceivers(jobWatchListToolResult{})
	_, _ = (&Session{}).inspectDescendantReceiverWatchByID("x")
	_, _, _ = (&Session{}).clearDescendantReceiverWatchByID("x")
	_ = (&Session{}).liveDescendantSessions()

	for _, args := range []map[string]any{{}, {"task": "x", "tasks": []any{"y"}}, {"tasks": "bad"}, {"tasks": []any{1}}} {
		_, _ = decodeDelegateArgs(args)
	}
	_ = validateJobGrepPattern(strings.Repeat("x", maxJobGrepPatternBytes+1), 10)
	_ = validateJobGrepPattern(strings.Repeat("\n", 100), 10)

	jm := newTestJM(t)
	if err := jm.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = (&Session{}).readJobOutputSnapshot(jm, &Session{}, "x", 1, false, nil)
	_, _, _ = (&Session{}).jobReadClosedStoreFallback(nil, errors.New("x"))
	_ = jobListDelegatesForJobs(&Session{}, nil, nil)

	// Marshal failures and bounding fallbacks.
	_, _ = marshalBoundedJSON(make(chan int), 1)
	_, _, _ = marshalBoundedJSONWithFit(make(chan int), 1)
	_, _, _ = marshalWithOutputLimit(1, 1, func(int) (string, error) { return "", errors.New("marshal") })
	_, _ = marshalBoundedDelegateResult(delegateToolResult{Output: ptrString(text), StructuredResult: make(chan int)}, 1)
	_, _ = marshalWatchResult(watchResult{EventFilter: &watchEventFilter{}, Send: &watchSendArgs{}}, 1)

	g := &jobGrepScan{lastTotal: 1}
	_ = g.step(jm, "x", regexp.MustCompile("x"), 1)
	_ = g.scanSegment([]byte(strings.Repeat("x", 4)), regexp.MustCompile("z"), 1)
	g.inDeadLine = true
	_ = g.scanSegment([]byte("tail"), regexp.MustCompile("z"), 1)
	_, _, _ = readJobOutputFrom(jm, "x", 0, 1)
	_ = projectJobOutputMatches(make([]jobstore.Match, maxJobGrepMatches+1))
	_ = projectJobRecordForViewer(nil, nil, &jobstore.JobRecord{})
	_ = projectDelegateRecord(nil)

	reg := tool.NewRegistry()
	_ = jobToolResultMaxChars(nil, "x")
	_ = jobToolResultMaxChars(reg, "x")
	enforceJobToolJSONLimits(nil)
	_ = reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: llm.ToolDefinition{Name: "job_status", Parameters: map[string]any{"type": "object"}}},
		Exec:  func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return nil, nil },
		Limit: schema.ToolOutputLimit{MaxChars: 1},
	})
	enforceJobToolJSONLimits(reg)

	// Ensure json itself sees the fuzz-derived text so the input is meaningful.
	_, _ = json.Marshal(text)
}
