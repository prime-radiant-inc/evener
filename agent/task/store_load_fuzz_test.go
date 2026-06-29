package task

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/fuzz/edgeseeds"
)

// FuzzTaskStoreLoad drives TaskStore.Load — the package's real on-disk decode
// seam (json.Unmarshal of the persisted []Task). Input is the raw tasks JSON
// file bytes. Beyond no-panic it asserts a decode→re-encode→decode fixed point:
// the View() after a load, marshaled and loaded again, yields the same task list
// (a dropped or mis-tagged field would diverge). It also checks the nextID
// invariant: nextID is strictly greater than every loaded task ID.
func FuzzTaskStoreLoad(f *testing.F) {
	seeds := []string{
		`[{"id":1,"type":"implement","description":"do a thing","prompt":"p","status":"open"}]`,
		`[{"id":1,"status":"done","depends_on":[],"notes":["n1","n2"]},{"id":2,"status":"open","depends_on":[1]}]`,
		`[{"id":5,"type":"research","status":"in_progress","reasoning_effort":"high","insert":"parent_tasks"}]`,
		`[]`,
		`null`,
		`not json`,
		`{}`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	// Generic JSON decoder stressors (deep nesting, surrogates, dup keys, …).
	for _, s := range edgeseeds.JSON() {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "tasks", "fuzz.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write input: %v", err)
		}

		s := NewTaskStore(dir, "fuzz")
		s.path = path
		if err := s.Load(); err != nil {
			return // malformed JSON: no-panic floor proven, stop
		}

		view := s.View()

		// nextID invariant: greater than every loaded ID.
		for _, task := range view {
			if s.nextID <= task.ID {
				t.Fatalf("nextID %d not greater than loaded task ID %d", s.nextID, task.ID)
			}
		}

		// Re-encode the loaded view and load it again: a fixed point.
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("marshal view: %v", err)
		}
		path2 := filepath.Join(dir, "tasks", "fuzz2.json")
		if err := os.WriteFile(path2, encoded, 0o644); err != nil {
			t.Fatalf("write reencoded: %v", err)
		}
		s2 := NewTaskStore(dir, "fuzz2")
		s2.path = path2
		if err := s2.Load(); err != nil {
			t.Fatalf("reload of re-encoded tasks failed: %v\n encoded=%s", err, encoded)
		}
		encoded2, err := json.Marshal(s2.View())
		if err != nil {
			t.Fatalf("marshal reloaded view: %v", err)
		}
		if !bytes.Equal(encoded, encoded2) {
			t.Fatalf("task load round-trip not a fixed point:\n once =%s\n twice=%s", encoded, encoded2)
		}
	})
}
