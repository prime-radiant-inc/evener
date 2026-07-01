package task

import (
	"testing"

	"github.com/spf13/afero"
)

// A read-only filesystem makes save's MkdirAll fail; Append must surface that
// error rather than silently losing the write.
func TestCov_Append_SaveErrorSurfaces(t *testing.T) {
	s := NewTaskStore(t.TempDir(), "s").SetFs(afero.NewReadOnlyFs(afero.NewMemMapFs()))
	_, err := s.Append([]TaskInput{{Description: "a", Prompt: "pa"}})
	if err == nil {
		t.Fatal("Append should surface the save error on a read-only fs")
	}
}

// Update likewise persists via save; a read-only fs surfaces the failure.
func TestCov_Update_SaveErrorSurfaces(t *testing.T) {
	mem := afero.NewMemMapFs()
	s := NewTaskStore(t.TempDir(), "s").SetFs(mem)
	added, err := s.Append([]TaskInput{{Description: "a", Prompt: "pa"}})
	if err != nil {
		t.Fatal(err)
	}
	// Swap to a read-only fs so the next persist fails.
	s.SetFs(afero.NewReadOnlyFs(mem))
	if err := s.Update([]TaskUpdate{{ID: added[0].ID, Status: TaskDone}}); err == nil {
		t.Fatal("Update should surface the save error on a read-only fs")
	}
}

// Load tolerates a missing file (fresh store) and round-trips a saved store.
func TestCov_Load_RoundTrip(t *testing.T) {
	mem := afero.NewMemMapFs()
	dir := t.TempDir()
	s := NewTaskStore(dir, "s").SetFs(mem)
	if err := s.Load(); err != nil {
		t.Fatalf("Load of a fresh store should be a no-op, got %v", err)
	}
	if _, err := s.Append([]TaskInput{{Description: "a", Prompt: "pa"}}); err != nil {
		t.Fatal(err)
	}
	// A second store over the same fs loads the persisted task.
	s2 := NewTaskStore(dir, "s").SetFs(mem)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s2.View()) != 1 {
		t.Errorf("reloaded store has %d tasks, want 1", len(s2.View()))
	}
}
