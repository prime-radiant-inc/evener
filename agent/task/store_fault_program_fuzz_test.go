package task

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/fuzz/fault"
)

// FuzzTaskStoreFaultProgram drives the real TaskStore persistence boundary
// through deterministic, fuzzer-selected filesystem fault schedules. The
// program keeps all paths fixed inside an in-memory filesystem: it varies task
// data and the fault-plan rotation, but never reads or writes host state.
//
// Besides the no-panic floor, the program asserts that failed saves are visible
// to callers, do not report a successful durable write, leave rename temporary
// files cleaned up, and do not replace an already-persisted task snapshot.
func FuzzTaskStoreFaultProgram(f *testing.F) {
	for _, seed := range []struct {
		description string
		variant     uint8
	}{
		{description: "implement task", variant: 0},
		{description: "repair dependency", variant: 1},
		{description: "verify result", variant: 2},
		{description: "", variant: 3},
	} {
		f.Add(seed.description, seed.variant)
	}

	f.Fuzz(func(t *testing.T, description string, variant uint8) {
		description = taskFaultDescription(description)
		input := TaskInput{
			Type:        []TaskType{TaskTypeImplement, TaskTypeResearch, TaskTypeVerify, TaskTypeFix}[int(variant)%4],
			Description: description,
			Prompt:      "deterministic task-store fuzz program",
		}

		taskFaultHealthyRoundTrip(t, input)
		taskFaultLifecycleMatrix(t, input)
		for failAt := 0; failAt < 4; failAt++ {
			taskFaultAppendFailure(t, input, failAt)
			taskFaultUpdateFailure(t, input, failAt)
		}
		for failAt := 0; failAt < 2; failAt++ {
			taskFaultLoadFailure(t, input, failAt)
		}
	})
}

func taskFaultHealthyRoundTrip(t *testing.T, input TaskInput) {
	t.Helper()
	base := afero.NewMemMapFs()
	s := taskFaultStore(base)
	added, err := s.Append([]TaskInput{input})
	if err != nil || len(added) != 1 {
		t.Fatalf("healthy Append = %#v, %v", added, err)
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone, Notes: "done"}}); err != nil {
		t.Fatalf("healthy Update: %v", err)
	}

	reloaded := taskFaultStore(base)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("healthy Load: %v", err)
	}
	view := reloaded.View()
	// encoding/json replaces each invalid UTF-8 byte with U+FFFD while encoding
	// strings. Converting through []rune computes the same durable value without
	// excluding malformed strings from the fuzz input space.
	durableDescription := string([]rune(input.Description))
	if len(view) != 1 || view[0].Description != durableDescription || view[0].Status != TaskDone || view[0].CompletedAt == nil || len(view[0].Notes) != 1 {
		t.Fatalf("healthy reload = %#v", view)
	}
}

func taskFaultLifecycleMatrix(t *testing.T, input TaskInput) {
	t.Helper()
	base := afero.NewMemMapFs()
	s := taskFaultStore(base)
	added, err := s.Append([]TaskInput{
		input,
		{Type: TaskTypeResearch, Description: "second", Prompt: "p"},
		{Type: TaskTypeVerify, Description: "third", Prompt: "p"},
	})
	if err != nil || len(added) != 3 {
		t.Fatalf("matrix setup Append = %#v, %v", added, err)
	}

	// The public Append path must reject every invalid dependency shape before
	// it changes durable state: self-reference, unknown target, and a cycle that
	// appears only when two pending tasks are considered together.
	if _, err := s.Append([]TaskInput{{Description: "self", Prompt: "p", DependsOn: []int{s.nextID}}}); err == nil {
		t.Fatal("Append accepted a self dependency")
	}
	if _, err := s.Append([]TaskInput{{Description: "unknown", Prompt: "p", DependsOn: []int{999}}}); err == nil {
		t.Fatal("Append accepted an unknown dependency")
	}
	firstPending := s.nextID
	if _, err := s.Append([]TaskInput{
		{Description: "cycle-a", Prompt: "p", DependsOn: []int{firstPending + 1}},
		{Description: "cycle-b", Prompt: "p", DependsOn: []int{firstPending}},
	}); err == nil {
		t.Fatal("Append accepted a pending dependency cycle")
	}
	dependent, err := s.Append([]TaskInput{{Description: "dependent", Prompt: "p", DependsOn: []int{added[0].ID}}})
	if err != nil || len(dependent) != 1 {
		t.Fatalf("Append valid dependency = %#v, %v", dependent, err)
	}

	if err := s.Update([]TaskUpdate{{
		ID:              added[0].ID,
		Status:          TaskDone,
		Notes:           "completed",
		ReasoningEffort: "high",
	}}); err != nil {
		t.Fatalf("Update completed task: %v", err)
	}
	if err := s.Update([]TaskUpdate{{ID: added[1].ID, Status: TaskCancelled}}); err != nil {
		t.Fatalf("Update cancelled task: %v", err)
	}
	if total, done := s.Progress(); total != 4 || done != 1 {
		t.Fatalf("Progress = (%d, %d), want (4, 1)", total, done)
	}
	eligible := s.NextEligible()
	if len(eligible) != 2 || eligible[0].ID != added[2].ID || eligible[1].ID != dependent[0].ID {
		t.Fatalf("NextEligible = %#v", eligible)
	}
	if _, ok := s.CurrentInProgress(); ok {
		t.Fatal("CurrentInProgress reported a task before one was started")
	}
	if err := s.Update([]TaskUpdate{{ID: added[2].ID, Status: TaskInProgress}}); err != nil {
		t.Fatalf("Update in-progress task: %v", err)
	}
	if current, ok := s.CurrentInProgress(); !ok || current.ID != added[2].ID {
		t.Fatalf("CurrentInProgress = %#v, %v", current, ok)
	}

	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: "invalid"}}); err == nil {
		t.Fatal("Update accepted an invalid status")
	}
	if err := s.Update([]TaskUpdate{{ID: 999, Status: TaskOpen}}); err == nil {
		t.Fatal("Update accepted an unknown task ID")
	}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskOpen}, {ID: added[1].ID, Status: TaskInProgress}}); err == nil {
		t.Fatal("Update accepted multiple in-progress tasks")
	}
	self := []int{added[0].ID}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone, DependsOn: &self}}); err == nil {
		t.Fatal("Update accepted a self dependency")
	}
	unknown := []int{999}
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone, DependsOn: &unknown}}); err == nil {
		t.Fatal("Update accepted an unknown dependency")
	}
	toSecond := []int{added[1].ID}
	toFirst := []int{added[0].ID}
	if err := s.Update([]TaskUpdate{
		{ID: added[0].ID, Status: TaskDone, DependsOn: &toSecond},
		{ID: added[1].ID, Status: TaskCancelled, DependsOn: &toFirst},
	}); err == nil {
		t.Fatal("Update accepted a projected dependency cycle")
	}

	populated := taskFaultStore(afero.NewMemMapFs())
	if err := populated.PopulateFromTemplates([]TaskTemplate{
		{Title: "first", Prompt: "p"},
		{Title: "placeholder", Prompt: "p", Insert: "parent_tasks"},
		{Title: "last", Prompt: "p", Type: string(TaskTypeFix)},
	}, []TaskTemplate{{Title: "parent", Prompt: "p", Type: string(TaskTypeResearch), ReasoningEffort: "medium"}}); err != nil {
		t.Fatalf("PopulateFromTemplates: %v", err)
	}
	populatedView := populated.View()
	if len(populatedView) != 3 || populatedView[0].Status != TaskInProgress || populatedView[1].Description != "parent" || populatedView[2].Type != TaskTypeFix {
		t.Fatalf("PopulateFromTemplates view = %#v", populatedView)
	}
	if err := populated.PopulateFromTemplates([]TaskTemplate{{Title: "ignored", Prompt: "p"}}, nil); err != nil {
		t.Fatalf("second PopulateFromTemplates: %v", err)
	}
	if got := populated.View(); len(got) != len(populatedView) {
		t.Fatalf("second PopulateFromTemplates changed store: %#v", got)
	}

	if hasCycle(map[int][]int{1: {2, 3}, 2: {3}, 3: {}}) {
		t.Fatal("hasCycle reported a cycle in an acyclic graph")
	}
	if !hasCycle(map[int][]int{1: {2}, 2: {3}, 3: {1}}) {
		t.Fatal("hasCycle missed a directed cycle")
	}

	// Exercise replacement entries in both halves of validateDependencies.
	// Production IDs are unique, but the validator deliberately accepts the
	// task being replaced in either the committed or pending slice.
	replacement := taskFaultStore(afero.NewMemMapFs())
	replacement.tasks = []Task{{ID: 1, Status: TaskOpen}}
	if err := replacement.validateDependencies(1, nil, nil); err != nil {
		t.Fatalf("validate committed replacement: %v", err)
	}
	if err := replacement.validateDependencies(2, nil, []Task{{ID: 2, Status: TaskOpen}}); err != nil {
		t.Fatalf("validate pending replacement: %v", err)
	}

	deps := []int{added[0].ID}
	if err := s.Update([]TaskUpdate{{ID: dependent[0].ID, Status: TaskOpen, DependsOn: &deps}}); err != nil {
		t.Fatalf("Update dependencies: %v", err)
	}

	// The apply loop defensively retains an unknown-ID check even though the
	// projected validation normally proves it unreachable. A deterministic
	// clock callback models state loss between two applications and covers that
	// final guard without races or host effects.
	disappearing := taskFaultStore(afero.NewMemMapFs())
	disappearing.tasks = []Task{{ID: 1, Status: TaskOpen}, {ID: 2, Status: TaskOpen}}
	disappearing.nextID = 3
	disappearing.SetClock(func() time.Time {
		disappearing.tasks = disappearing.tasks[:1]
		return time.Unix(1_700_000_100, 0).UTC()
	})
	if err := disappearing.Update([]TaskUpdate{{ID: 1, Status: TaskDone}, {ID: 2, Status: TaskCancelled}}); err == nil {
		t.Fatal("Update accepted a task that disappeared during application")
	}

	invalidTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	marshalFailure := taskFaultStore(afero.NewMemMapFs())
	marshalFailure.tasks = []Task{{ID: 1, Status: TaskOpen, CreatedAt: &invalidTime}}
	if err := marshalFailure.save(); err == nil {
		t.Fatal("save accepted a timestamp JSON cannot represent")
	}
}

func taskFaultAppendFailure(t *testing.T, input TaskInput, failAt int) {
	t.Helper()
	base := afero.NewMemMapFs()
	s := taskFaultStore(fault.FS(base, fault.FromBytes(taskFaultPlan(failAt))))
	added, err := s.Append([]TaskInput{input})
	if !errors.Is(err, fault.ErrInjected) || len(added) != 1 {
		t.Fatalf("Append fault %d = %#v, %v", failAt, added, err)
	}
	taskFaultRequireAbsent(t, base, s.path, "failed Append persisted final file")
	if failAt == 3 {
		taskFaultRequireAbsent(t, base, s.path+".tmp", "rename failure left temporary file")
	}
}

func taskFaultUpdateFailure(t *testing.T, input TaskInput, failAt int) {
	t.Helper()
	base := afero.NewMemMapFs()
	s := taskFaultStore(base)
	added, err := s.Append([]TaskInput{input})
	if err != nil || len(added) != 1 {
		t.Fatalf("setup Append = %#v, %v", added, err)
	}

	s.SetFs(fault.FS(base, fault.FromBytes(taskFaultPlan(failAt))))
	err = s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone}})
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("Update fault %d = %v", failAt, err)
	}

	reloaded := taskFaultStore(base)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload after failed Update: %v", err)
	}
	view := reloaded.View()
	if len(view) != 1 || view[0].Status != TaskOpen || view[0].CompletedAt != nil {
		t.Fatalf("failed Update replaced durable snapshot: %#v", view)
	}
	if failAt == 3 {
		taskFaultRequireAbsent(t, base, s.path+".tmp", "rename failure left temporary file")
	}
}

func taskFaultLoadFailure(t *testing.T, input TaskInput, failAt int) {
	t.Helper()
	base := afero.NewMemMapFs()
	seed := taskFaultStore(base)
	if _, err := seed.Append([]TaskInput{input}); err != nil {
		t.Fatalf("setup Append: %v", err)
	}

	broken := taskFaultStore(fault.FS(base, fault.FromBytes(taskFaultPlan(failAt))))
	if err := broken.Load(); !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("Load fault %d = %v", failAt, err)
	}
}

func taskFaultStore(fs afero.Fs) *TaskStore {
	var tick int64
	return NewTaskStore("/state", "fault").
		SetFs(fs).
		SetClock(func() time.Time {
			tick++
			return time.Unix(1_700_000_000+tick, 0).UTC()
		})
}

func taskFaultPlan(failAt int) []byte {
	plan := make([]byte, failAt+5)
	for i := range plan {
		plan[i] = 1
	}
	plan[failAt] = 0
	return plan
}

func taskFaultRequireAbsent(t *testing.T, fs afero.Fs, path, message string) {
	t.Helper()
	exists, err := afero.Exists(fs, path)
	if err != nil || exists {
		t.Fatalf("%s: exists=%v err=%v", message, exists, err)
	}
}

func taskFaultDescription(raw string) string {
	const maxBytes = 256
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
	}
	if raw == "" {
		return "empty description"
	}
	return fmt.Sprintf("task:%s", raw)
}
