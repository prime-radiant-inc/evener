package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// Task 10: delegateResult echoes the resolved provider/model the child
// actually ran with, mirroring the existing sandbox echo. Delegate semantics
// (capture-at-spawn, explicit-arg pin, restore re-resolution) are UNCHANGED
// by this task; it only reports what already happened.

// TestCreateDelegate_ResultEchoesResolvedModel: (a) a plain delegate spawn
// echoes the resolved provider/model it ran with.
func TestCreateDelegate_ResultEchoesResolvedModel(t *testing.T) {
	t.Parallel()
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") })
	s := newDelegateTestSession(t, c)

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "do some work",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if res.Model != "openai/gpt-5.2" {
		t.Fatalf("res.Model = %q, want openai/gpt-5.2", res.Model)
	}

	out, err := marshalDelegateResult(res, 30000)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(out, `"model":"openai/gpt-5.2"`) {
		t.Errorf("marshaled result must include the model key, got %s", out)
	}
}

// TestCreateDelegate_SpawnAfterSwitchEchoesNewModel: (b) a delegate spawned
// after the parent switched models echoes the NEW model; one spawned before
// keeps its captured model. Inheritance semantics are unchanged (spec N7,
// G9) — this only asserts the echo tracks what was actually captured.
func TestCreateDelegate_SpawnAfterSwitchEchoesNewModel(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("first") },
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("second") },
	}})
	s := newDelegateTestSession(t, c)

	before := s.createDelegate(context.Background(), delegateArgs{
		Task:           "before switch",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if before.Err != nil {
		t.Fatalf("createDelegate before switch: %v", before.Err)
	}
	if before.Model != "openai/gpt-5.2" {
		t.Fatalf("before.Model = %q, want openai/gpt-5.2", before.Model)
	}

	if err := s.SetModel("gpt-5.3"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	after := s.createDelegate(context.Background(), delegateArgs{
		Task:           "after switch",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if after.Err != nil {
		t.Fatalf("createDelegate after switch: %v", after.Err)
	}
	if after.Model != "openai/gpt-5.3" {
		t.Fatalf("after.Model = %q, want openai/gpt-5.3 (new parent model captured at spawn)", after.Model)
	}
	if before.Model == after.Model {
		t.Fatalf("before/after echoed the same model %q, want the pre-switch delegate to keep its captured model", before.Model)
	}
}

// TestCreateDelegate_ExplicitModelArgEchoesPin: (c) an explicit model arg
// pins the child's model regardless of the parent's current profile, and
// the result echoes the pinned value.
func TestCreateDelegate_ExplicitModelArgEchoesPin(t *testing.T) {
	t.Parallel()
	c := delegateTestClient(func(req llm.Request) llm.Response { return communicateWithDefaultOutput("pinned") })
	s := newDelegateTestSession(t, c)

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "pin the model",
		Model:          "gpt-5.3",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if res.Model != "openai/gpt-5.3" {
		t.Fatalf("res.Model = %q, want openai/gpt-5.3 (explicit pin)", res.Model)
	}
}

// TestDelegateTerminalResult_RestoreEchoesDescriptorModel: (d) a restored
// delegate's terminal result echoes the persisted descriptor model
// (jobstore/record.go ResolvedProfileID/ResolvedModel), not the parent's
// current model.
func TestDelegateTerminalResult_RestoreEchoesDescriptorModel(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusCompleted, "exit_zero")
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	rec.DelegateRestore.ResolvedProfileID = "openai"
	rec.DelegateRestore.ResolvedModel = "restored-model"
	replaceStoredDelegateRecord(t, s, rec)

	run := &runningJob{rec: &jobstore.JobRecord{
		JobID:         rec.JobID,
		DelegateID:    rec.DelegateID,
		TranscriptRef: rec.TranscriptRef,
	}}
	res := delegateTerminalResult(s, s.jobManager, run)
	if res.Model != "openai/restored-model" {
		t.Fatalf("res.Model = %q, want openai/restored-model (persisted descriptor model)", res.Model)
	}
}
