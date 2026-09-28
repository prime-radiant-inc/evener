package schema

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListSessionMetasWithErrors_ReportsCorruptMeta proves the forensic
// enumeration surfaces a metadata file that ListSessionMetas silently drops:
// the corrupt file's filename-derived id comes back as a load failure while a
// valid sibling still lists. ListSessionMetas must stay tolerant, so the same
// fixture yields only the valid meta through it.
func TestListSessionMetasWithErrors_ReportsCorruptMeta(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const validID = "02wMz5Txv1C3Hut0M8GCeB"
	const corruptID = "02wMz5Txv2enqVTitaig6F"
	if err := SaveSessionMetaWithFS(sessionMetaFS, dir, SessionMeta{ID: validID}); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(sessionsDir, corruptID+".meta.json")
	if err := os.WriteFile(corruptPath, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, failures, err := ListSessionMetasWithErrors(dir)
	if err != nil {
		t.Fatalf("ListSessionMetasWithErrors: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != validID {
		t.Fatalf("metas = %+v, want only the valid meta %s", metas, validID)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %+v, want exactly the corrupt meta %s", failures, corruptID)
	}
	if failures[0].ID != corruptID {
		t.Errorf("failure id = %q, want %q (filename-derived)", failures[0].ID, corruptID)
	}
	if failures[0].Error == nil {
		t.Error("failure should carry the load error")
	}

	// The tolerant listing is unchanged: the corrupt meta is still skipped.
	metas, err = ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != validID {
		t.Fatalf("ListSessionMetas = %+v, want only the valid meta %s", metas, validID)
	}
}

// TestListSessionMetasWithErrors_ReportsNonRegularMeta proves a valid-id
// .meta.json that is present but not a regular file (a symlink here) is named
// as a load failure instead of silently dropped, while the tolerant listing
// keeps rejecting it and never follows the link.
func TestListSessionMetasWithErrors_ReportsNonRegularMeta(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "02wMz5Txv1C3Hut0M8GCeB"
	target := filepath.Join(t.TempDir(), sid+".meta.json")
	if err := os.WriteFile(target, []byte(`{"id":"`+sid+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(sessionsDir, sid+".meta.json")); err != nil {
		t.Fatal(err)
	}

	metas, failures, err := ListSessionMetasWithErrors(dir)
	if err != nil {
		t.Fatalf("ListSessionMetasWithErrors: %v", err)
	}
	if len(metas) != 0 {
		t.Fatalf("metas = %+v, want none (symlinked meta must not load)", metas)
	}
	if len(failures) != 1 || failures[0].ID != sid || failures[0].Error == nil {
		t.Fatalf("failures = %+v, want one naming %s", failures, sid)
	}

	// The tolerant listing still rejects the symlinked meta outright.
	metas, err = ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(metas) != 0 {
		t.Fatalf("ListSessionMetas = %+v, want none", metas)
	}
}
