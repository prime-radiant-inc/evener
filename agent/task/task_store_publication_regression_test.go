package task

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/fuzz/fault"
)

func TestTaskStore_RejectedMutationPreservesPublishedState(t *testing.T) {
	t.Parallel()

	for _, failAt := range []int{0, 1, 2, 3} {
		t.Run("append/fault-"+string(rune('0'+failAt)), func(t *testing.T) {
			base := afero.NewMemMapFs()
			store := NewTaskStore("/state", "append").SetFs(base)
			before := store.View()
			store.SetFs(fault.FS(base, fault.FromBytes(taskFaultPlan(failAt))))
			if _, err := store.Append([]TaskInput{{Description: "new", Prompt: "prompt"}}); err == nil {
				t.Fatalf("Append fault %d unexpectedly succeeded", failAt)
			}
			if got := store.View(); !reflect.DeepEqual(got, before) {
				t.Fatalf("live state after rejected Append = %#v, want %#v", got, before)
			}
			assertReloadedTasks(t, base, store.path, before)

			store.SetFs(base)
			added, err := store.Append([]TaskInput{{Description: "new", Prompt: "prompt"}})
			if err != nil || len(added) != 1 || added[0].ID != 1 {
				t.Fatalf("retry Append = %#v, %v; want one task with ID 1", added, err)
			}
			assertReloadedTasks(t, base, store.path, store.View())
		})

		t.Run("update/fault-"+string(rune('0'+failAt)), func(t *testing.T) {
			base := afero.NewMemMapFs()
			store := NewTaskStore("/state", "update").SetFs(base)
			added, err := store.Append([]TaskInput{{Description: "existing", Prompt: "prompt"}})
			if err != nil {
				t.Fatal(err)
			}
			before := store.View()
			diskBefore, err := afero.ReadFile(base, store.path)
			if err != nil {
				t.Fatal(err)
			}
			store.SetFs(fault.FS(base, fault.FromBytes(taskFaultPlan(failAt))))
			if err := store.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone, Notes: "done"}}); err == nil {
				t.Fatalf("Update fault %d unexpectedly succeeded", failAt)
			}
			if got := store.View(); !reflect.DeepEqual(got, before) {
				t.Fatalf("live state after rejected Update = %#v, want %#v", got, before)
			}
			assertBytesUnchanged(t, base, store.path, diskBefore)
			assertReloadedTasks(t, base, store.path, before)

			store.SetFs(base)
			if err := store.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone, Notes: "done"}}); err != nil {
				t.Fatalf("retry Update: %v", err)
			}
			got := store.View()
			if len(got) != 1 || got[0].Status != TaskDone || !reflect.DeepEqual(got[0].Notes, []string{"done"}) {
				t.Fatalf("retry Update state = %#v, want one done task with one note", got)
			}
			assertReloadedTasks(t, base, store.path, got)
		})

		t.Run("populate/fault-"+string(rune('0'+failAt)), func(t *testing.T) {
			base := afero.NewMemMapFs()
			store := NewTaskStore("/state", "populate").SetFs(base)
			before := store.View()
			store.SetFs(fault.FS(base, fault.FromBytes(taskFaultPlan(failAt))))
			if err := store.PopulateFromTemplates([]TaskTemplate{{Title: "seed", Prompt: "prompt"}}, nil); err == nil {
				t.Fatalf("PopulateFromTemplates fault %d unexpectedly succeeded", failAt)
			}
			if got := store.View(); !reflect.DeepEqual(got, before) {
				t.Fatalf("live state after rejected population = %#v, want %#v", got, before)
			}
			assertReloadedTasks(t, base, store.path, before)

			store.SetFs(base)
			if err := store.PopulateFromTemplates([]TaskTemplate{{Title: "seed", Prompt: "prompt"}}, nil); err != nil {
				t.Fatalf("retry PopulateFromTemplates: %v", err)
			}
			got := store.View()
			if len(got) != 1 || got[0].ID != 1 || got[0].Status != TaskInProgress {
				t.Fatalf("retry population state = %#v", got)
			}
			assertReloadedTasks(t, base, store.path, got)
		})
	}
}

func assertReloadedTasks(t *testing.T, fs afero.Fs, path string, want []Task) {
	t.Helper()
	reloaded := NewTaskStore("/state", "reload").SetFs(fs)
	reloaded.path = path
	if err := reloaded.Load(); err != nil {
		t.Fatalf("independent reload: %v", err)
	}
	if got := reloaded.View(); !reflect.DeepEqual(normalizeTaskTimes(got), normalizeTaskTimes(want)) {
		t.Fatalf("reloaded state = %#v, want %#v", got, want)
	}
}

func normalizeTaskTimes(tasks []Task) []Task {
	cloned := cloneTasks(tasks)
	for i := range cloned {
		for _, stamp := range []*time.Time{cloned[i].CreatedAt, cloned[i].UpdatedAt, cloned[i].CompletedAt} {
			if stamp != nil {
				*stamp = stamp.UTC()
			}
		}
	}
	return cloned
}

func assertBytesUnchanged(t *testing.T, fs afero.Fs, path string, want []byte) {
	t.Helper()
	got, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read persisted bytes: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("persisted bytes changed: got %q, want %q", got, want)
	}
}

func TestTaskStore_LoadFailurePreservesOriginalBytesAndFencesMutation(t *testing.T) {
	t.Parallel()
	base := afero.NewMemMapFs()
	store := NewTaskStore("/state", "broken").SetFs(base)
	original := []byte("{malformed task json")
	if err := afero.WriteFile(base, store.path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err == nil {
		t.Fatal("Load unexpectedly accepted malformed JSON")
	}

	if got := store.View(); len(got) != 0 {
		t.Fatalf("failed Load exposed tasks = %#v, want empty unpublished state", got)
	}
	for name, mutate := range map[string]func() error{
		"append": func() error {
			_, err := store.Append([]TaskInput{{Description: "overwrite", Prompt: "prompt"}})
			return err
		},
		"update": func() error {
			return store.Update([]TaskUpdate{{ID: 1, Status: TaskDone}})
		},
		"populate": func() error {
			return store.PopulateFromTemplates([]TaskTemplate{{Title: "overwrite", Prompt: "prompt"}}, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := mutate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unavailable") {
				t.Fatalf("mutation error = %v, want unavailable fence", err)
			}
			assertBytesUnchanged(t, base, store.path, original)
		})
	}

	// External repair is the existing recovery path: a subsequent successful
	// Load clears the unavailable fence without adding a repair API.
	if err := afero.WriteFile(base, store.path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(); err != nil {
		t.Fatalf("Load after external repair: %v", err)
	}
	if _, err := store.Append([]TaskInput{{Description: "recovered", Prompt: "prompt"}}); err != nil {
		t.Fatalf("Append after successful repaired Load: %v", err)
	}
}
