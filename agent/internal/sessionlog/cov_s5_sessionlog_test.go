package sessionlog

import (
	"testing"

	"github.com/spf13/afero"
)

// loadFromDisk reads existing entries and tolerates malformed lines (partial
// writes) by skipping them.
func TestCov_LoadFromDisk_SkipsMalformed(t *testing.T) {
	mem := afero.NewMemMapFs()
	path := "/state/sessions/s.log.jsonl"
	content := `{"turn":1,"action":"shell","summary":"a","outcome":"success"}
this is not json
{"turn":2,"action":"edit_file","summary":"b","outcome":"success"}
`
	if err := afero.WriteFile(mem, path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	log, err := newSessionLogFS(path, mem)
	if err != nil {
		t.Fatalf("newSessionLogFS: %v", err)
	}
	if log.Len() != 2 {
		t.Errorf("expected 2 valid entries (malformed line skipped), got %d", log.Len())
	}
}

// appendToDisk surfaces a directory-creation failure on a read-only fs.
func TestCov_AppendToDisk_MkdirError(t *testing.T) {
	ro := afero.NewReadOnlyFs(afero.NewMemMapFs())
	log, err := newSessionLogFS("/state/sessions/s.log.jsonl", ro)
	if err != nil {
		t.Fatalf("newSessionLogFS (no existing file) should succeed: %v", err)
	}
	if err := log.Append(SessionLogEntry{Turn: 1, Action: "shell", Summary: "x", Outcome: "success"}); err == nil {
		t.Fatal("Append on a read-only fs should surface the mkdir error")
	}
}

// appendToDisk persists entries that a later reload sees.
func TestCov_Append_PersistsAndReloads(t *testing.T) {
	mem := afero.NewMemMapFs()
	path := "/state/sessions/s.log.jsonl"
	log, err := newSessionLogFS(path, mem)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(SessionLogEntry{Turn: 1, Action: "shell", Summary: "did it", Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newSessionLogFS(path, mem)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Len() != 1 {
		t.Errorf("reloaded log has %d entries, want 1", reloaded.Len())
	}
}
