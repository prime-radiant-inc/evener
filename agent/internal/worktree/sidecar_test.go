package worktree

import (
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func testSidecar() Sidecar {
	return Sidecar{
		Name:            "feature/foo",
		Branch:          "feature/foo",
		BaseSHA:         "abc123",
		MergeTarget:     "main",
		OriginalRoot:    "/home/jesse/git/prime-radiant/serf",
		CreatorSession:  "01HXYZ",
		DelegateID:      "dlg_01HXYZ",
		WorktreeRemoved: true,
		TipSHAAtRemoval: "def456",
		CreatedAt:       "2026-07-03T12:00:00Z",
	}
}

func TestWriteSidecarExclThenRead(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	got, err := ReadSidecar(dir, sc.Name)
	if err != nil {
		t.Fatalf("ReadSidecar: %v", err)
	}
	if got != sc {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, sc)
	}
}

// TestWriteSidecarExclRoundTripsAllFields guards every field individually so
// a future field addition that forgets the json tag or the struct-literal
// wiring shows up as a specific failure, not just a struct-equality diff.
func TestWriteSidecarExclRoundTripsAllFields(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	got, err := ReadSidecar(dir, sc.Name)
	if err != nil {
		t.Fatalf("ReadSidecar: %v", err)
	}
	switch {
	case got.Name != sc.Name:
		t.Errorf("Name = %q, want %q", got.Name, sc.Name)
	case got.Branch != sc.Branch:
		t.Errorf("Branch = %q, want %q", got.Branch, sc.Branch)
	case got.BaseSHA != sc.BaseSHA:
		t.Errorf("BaseSHA = %q, want %q", got.BaseSHA, sc.BaseSHA)
	case got.MergeTarget != sc.MergeTarget:
		t.Errorf("MergeTarget = %q, want %q", got.MergeTarget, sc.MergeTarget)
	case got.OriginalRoot != sc.OriginalRoot:
		t.Errorf("OriginalRoot = %q, want %q", got.OriginalRoot, sc.OriginalRoot)
	case got.CreatorSession != sc.CreatorSession:
		t.Errorf("CreatorSession = %q, want %q", got.CreatorSession, sc.CreatorSession)
	case got.DelegateID != sc.DelegateID:
		t.Errorf("DelegateID = %q, want %q", got.DelegateID, sc.DelegateID)
	case got.WorktreeRemoved != sc.WorktreeRemoved:
		t.Errorf("WorktreeRemoved = %v, want %v", got.WorktreeRemoved, sc.WorktreeRemoved)
	case got.TipSHAAtRemoval != sc.TipSHAAtRemoval:
		t.Errorf("TipSHAAtRemoval = %q, want %q", got.TipSHAAtRemoval, sc.TipSHAAtRemoval)
	case got.CreatedAt != sc.CreatedAt:
		t.Errorf("CreatedAt = %q, want %q", got.CreatedAt, sc.CreatedAt)
	}
}

// TestSidecarJSONOmitsEmptyOptionalFields locks the spec §6 wire shape: the
// four optional fields (merge_target, delegate_id, worktree_removed,
// tip_sha_at_removal) carry omitempty and must not appear when zero-valued
// (a detached-HEAD creation or a non-delegate creation should not leave
// misleading zero-value fields on disk).
func TestSidecarJSONOmitsEmptyOptionalFields(t *testing.T) {
	dir := t.TempDir()
	sc := Sidecar{
		Name:           "a",
		Branch:         "a",
		BaseSHA:        "abc123",
		OriginalRoot:   "/repo",
		CreatorSession: "01HXYZ",
		CreatedAt:      "2026-07-03T12:00:00Z",
	}
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, EncodeSidecarName(sc.Name)+".json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, field := range []string{"merge_target", "delegate_id", "worktree_removed", "tip_sha_at_removal"} {
		if _, present := m[field]; present {
			t.Errorf("field %q present in JSON with zero value, want omitted: %s", field, raw)
		}
	}
	for _, field := range []string{"name", "branch", "base_sha", "original_root", "creator_session", "created_at"} {
		if _, present := m[field]; !present {
			t.Errorf("required field %q missing from JSON: %s", field, raw)
		}
	}
}

func TestWriteSidecarExclOnFilesystem(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	wantPath := filepath.Join(dir, "feature%2Ffoo.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected sidecar file at %s: %v", wantPath, err)
	}
}

// TestWriteSidecarExclLoserFailsWithIsExist is the spec §3 step 5 rationale:
// a second WriteSidecarExcl for the same name must fail cleanly with
// os.IsExist(err) true, never silently clobbering the winner's provenance.
func TestWriteSidecarExclLoserFailsWithIsExist(t *testing.T) {
	dir := t.TempDir()
	winner := testSidecar()
	winner.CreatorSession = "winner-session"
	if err := WriteSidecarExcl(dir, winner.Name, winner); err != nil {
		t.Fatalf("first WriteSidecarExcl: %v", err)
	}

	loser := testSidecar()
	loser.CreatorSession = "loser-session"
	err := WriteSidecarExcl(dir, loser.Name, loser)
	if err == nil {
		t.Fatalf("second WriteSidecarExcl succeeded, want os.IsExist error")
	}
	if !os.IsExist(err) {
		t.Fatalf("second WriteSidecarExcl error = %v, want os.IsExist(err) true", err)
	}

	// The winner's provenance must survive untouched.
	got, err := ReadSidecar(dir, winner.Name)
	if err != nil {
		t.Fatalf("ReadSidecar after loser: %v", err)
	}
	if got.CreatorSession != "winner-session" {
		t.Fatalf("winner's sidecar was clobbered: creator_session = %q, want %q", got.CreatorSession, "winner-session")
	}
}

func TestReadSidecarMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadSidecar(dir, "nonexistent")
	if err == nil {
		t.Fatalf("ReadSidecar on missing file = nil, want error")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("ReadSidecar error = %v, want os.IsNotExist(err) true", err)
	}
}

func TestReadSidecarGarbageContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := os.WriteFile(path, []byte("not json at all {{{"), 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	_, err := ReadSidecar(dir, "a")
	if err == nil {
		t.Fatalf("ReadSidecar on garbage content = nil, want error")
	}
}

func TestUpdateSidecarMutatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	sc.WorktreeRemoved = false
	sc.TipSHAAtRemoval = ""
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}

	err := UpdateSidecar(dir, sc.Name, func(s *Sidecar) {
		s.WorktreeRemoved = true
		s.TipSHAAtRemoval = "removed-tip-sha"
	})
	if err != nil {
		t.Fatalf("UpdateSidecar: %v", err)
	}

	got, err := ReadSidecar(dir, sc.Name)
	if err != nil {
		t.Fatalf("ReadSidecar after update: %v", err)
	}
	if !got.WorktreeRemoved {
		t.Fatalf("WorktreeRemoved = false after update, want true")
	}
	if got.TipSHAAtRemoval != "removed-tip-sha" {
		t.Fatalf("TipSHAAtRemoval = %q, want %q", got.TipSHAAtRemoval, "removed-tip-sha")
	}
	// Untouched fields survive the read-mutate-write cycle.
	if got.CreatorSession != sc.CreatorSession {
		t.Fatalf("CreatorSession = %q, want %q (unmutated field must survive)", got.CreatorSession, sc.CreatorSession)
	}
}

func TestUpdateSidecarMissingFile(t *testing.T) {
	dir := t.TempDir()
	err := UpdateSidecar(dir, "nonexistent", func(s *Sidecar) { s.WorktreeRemoved = true })
	if err == nil {
		t.Fatalf("UpdateSidecar on missing file = nil, want error")
	}
}

func TestDeleteSidecar(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	if err := DeleteSidecar(dir, sc.Name); err != nil {
		t.Fatalf("DeleteSidecar: %v", err)
	}
	if _, err := ReadSidecar(dir, sc.Name); !os.IsNotExist(err) {
		t.Fatalf("ReadSidecar after delete = %v, want os.IsNotExist(err) true", err)
	}
}

func TestDeleteSidecarMissingFile(t *testing.T) {
	dir := t.TempDir()
	err := DeleteSidecar(dir, "nonexistent")
	if err == nil {
		t.Fatalf("DeleteSidecar on missing file = nil, want error")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("DeleteSidecar error = %v, want os.IsNotExist(err) true", err)
	}
}

func TestListSidecarsReturnsAllManaged(t *testing.T) {
	dir := t.TempDir()
	names := []string{"a", "feature/foo", "dlg_01HXYZ"}
	for _, name := range names {
		sc := testSidecar()
		sc.Name = name
		if err := WriteSidecarExcl(dir, name, sc); err != nil {
			t.Fatalf("WriteSidecarExcl(%q): %v", name, err)
		}
	}

	got, err := ListSidecars(dir)
	if err != nil {
		t.Fatalf("ListSidecars: %v", err)
	}
	if len(got) != len(names) {
		t.Fatalf("ListSidecars returned %d entries, want %d: %+v", len(got), len(names), got)
	}
	seen := map[string]bool{}
	for _, sc := range got {
		seen[sc.Name] = true
	}
	for _, name := range names {
		if !seen[name] {
			t.Errorf("ListSidecars missing entry for %q, got %+v", name, got)
		}
	}
}

// TestListSidecarsSkipsNonJSONNoise covers spec §6's "unmanaged_meta"
// framing at the codec layer: any non-.json file in metaDir is noise (not a
// sidecar) and must not appear in the result or cause an error.
func TestListSidecarsSkipsNonJSONNoise(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	sc.Name = "a"
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	noise := []string{"README.md", ".DS_Store", "notes.txt", "a.json.bak"}
	for _, n := range noise {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("noise"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q): %v", n, err)
		}
	}
	// A subdirectory must also be skipped, not descended into or errored on.
	if err := os.Mkdir(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
		t.Fatalf("os.Mkdir: %v", err)
	}

	got, err := ListSidecars(dir)
	if err != nil {
		t.Fatalf("ListSidecars: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSidecars returned %d entries, want 1 (noise must be skipped): %+v", len(got), got)
	}
	if got[0].Name != "a" {
		t.Fatalf("ListSidecars entry = %+v, want Name %q", got[0], "a")
	}
}

// TestListSidecarsSkipsUndecodableNames covers a .json file whose basename
// is not valid EncodeSidecarName output (e.g. a stray "%" not part of a
// literal "%2F" triple) — this is exactly the unmanaged/stray sidecar case
// spec §6 calls out, and List must skip it rather than panic or error.
func TestListSidecarsSkipsUndecodableNames(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	sc.Name = "a"
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "100%done.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	got, err := ListSidecars(dir)
	if err != nil {
		t.Fatalf("ListSidecars: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSidecars returned %d entries, want 1 (undecodable name must be skipped): %+v", len(got), got)
	}
}

// TestListSidecarsSkipsGarbageJSON: a .json file with an encodable name but
// unparseable content must be skipped (List's contract is skip-with-no-panic,
// unlike ReadSidecar which errors for a single lookup).
func TestListSidecarsSkipsGarbageJSON(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	sc.Name = "a"
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	got, err := ListSidecars(dir)
	if err != nil {
		t.Fatalf("ListSidecars: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSidecars returned %d entries, want 1 (garbage JSON must be skipped): %+v", len(got), got)
	}
	if got[0].Name != "a" {
		t.Fatalf("ListSidecars entry = %+v, want Name %q", got[0], "a")
	}
}

func TestListSidecarsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := ListSidecars(dir)
	if err != nil {
		t.Fatalf("ListSidecars: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListSidecars on empty dir = %+v, want empty", got)
	}
}

func TestListSidecarsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := ListSidecars(dir)
	if err == nil {
		t.Fatalf("ListSidecars on missing dir = nil, want error")
	}
}

// TestSidecarAgeReflectsMtime backdates the sidecar file's mtime with
// os.Chtimes (no sleeping) and checks SidecarAge reports at least that much
// elapsed time — spec §5 sweep 2: age is judged by file mtime, never the
// recorded created_at wall-clock string.
func TestSidecarAgeReflectsMtime(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	path := filepath.Join(dir, EncodeSidecarName(sc.Name)+".json")
	backdate := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(path, backdate, backdate); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	age, err := SidecarAge(dir, sc.Name)
	if err != nil {
		t.Fatalf("SidecarAge: %v", err)
	}
	if age < 29*time.Minute {
		t.Fatalf("SidecarAge = %v, want at least ~30m", age)
	}
}

// TestSidecarAgeIgnoresCreatedAtField: a sidecar whose recorded created_at
// field is wildly different from the file's actual mtime must still report
// age from the mtime (rev-7 review: created_at is the creator's clock and
// defeats the grace under cross-machine skew — spec §5 sweep 2).
func TestSidecarAgeIgnoresCreatedAtField(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	sc.CreatedAt = "1999-01-01T00:00:00Z" // wildly stale recorded timestamp
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	// File mtime is "now" (unmodified since write), so age must be small
	// despite the ancient recorded created_at.
	age, err := SidecarAge(dir, sc.Name)
	if err != nil {
		t.Fatalf("SidecarAge: %v", err)
	}
	if age > time.Minute {
		t.Fatalf("SidecarAge = %v, want well under a minute (mtime is fresh, created_at must be ignored)", age)
	}
}

func TestSidecarAgeMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := SidecarAge(dir, "nonexistent")
	if err == nil {
		t.Fatalf("SidecarAge on missing file = nil, want error")
	}
}

func TestReconcileGraceIs15Minutes(t *testing.T) {
	if ReconcileGrace != 15*time.Minute {
		t.Fatalf("ReconcileGrace = %v, want 15m", ReconcileGrace)
	}
}

// TestEncodeSidecarNameUsedForFilename is a narrow contract check: the
// on-disk filename is exactly EncodeSidecarName(name)+".json", not some
// other encoding, since prune's reconciliation sweep (spec §5 sweep 2) walks
// metaDir and must decode filenames back to names via DecodeSidecarName.
func TestEncodeSidecarNameUsedForFilename(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	sc.Name = "deeply/nested/name"
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file, got %d: %+v", len(entries), entries)
	}
	want := EncodeSidecarName(sc.Name) + ".json"
	if entries[0].Name() != want {
		t.Fatalf("filename = %q, want %q", entries[0].Name(), want)
	}
	encoded := want[:len(want)-len(".json")]
	decoded, ok := DecodeSidecarName(encoded)
	if !ok || decoded != sc.Name {
		t.Fatalf("DecodeSidecarName(%q) = %q, %v, want %q, true", encoded, decoded, ok, sc.Name)
	}
}

// withTinyFileSizeLimit lowers the process's RLIMIT_FSIZE to n bytes for the
// duration of fn, restoring the original limit afterward. It is the one
// portable way to force a genuine os-level write failure (EFBIG) against a
// real file — encoding/json can never fail to Marshal a Sidecar (every field
// is a plain string or bool; there is no channel, func, complex, or NaN
// value for it to choke on), so WriteSidecarExcl's and UpdateSidecar's
// "encode" error branches are only reachable via the underlying io.Writer
// actually failing, not via a marshal-time type error.
//
// Exceeding RLIMIT_FSIZE delivers SIGXFSZ to the process; left at its
// default disposition that SIGNAL TERMINATES THE PROCESS rather than just
// failing the write (POSIX: write() only returns EFBIG instead of killing
// the caller when SIGXFSZ is caught or ignored). So this helper ignores
// SIGXFSZ for the duration of fn and restores the default disposition
// afterward — without that, this test would crash the whole `go test`
// binary instead of exercising the error-return path it's here to cover.
func withTinyFileSizeLimit(t *testing.T, n uint64, fn func()) {
	t.Helper()
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Skipf("Getrlimit(RLIMIT_FSIZE): %v", err)
	}
	tiny := syscall.Rlimit{Cur: n, Max: original.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &tiny); err != nil {
		t.Skipf("Setrlimit(RLIMIT_FSIZE, %d): %v", n, err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer func() {
		signal.Reset(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Fatalf("restore RLIMIT_FSIZE: %v", err)
		}
	}()
	fn()
}

// TestWriteSidecarExclEncodeFailure exercises WriteSidecarExcl's write-error
// path (the file is created successfully, but the subsequent JSON write to
// it fails) via a real, genuine os-level I/O failure: RLIMIT_FSIZE capped
// below the encoded sidecar's byte size triggers a real EFBIG from the
// kernel on write, exactly the class of failure ("disk full", "quota
// exceeded") this branch exists to surface.
func TestWriteSidecarExclEncodeFailure(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	withTinyFileSizeLimit(t, 4, func() {
		err := WriteSidecarExcl(dir, sc.Name, sc)
		if err == nil {
			t.Fatalf("WriteSidecarExcl under a 4-byte RLIMIT_FSIZE = nil, want a write error")
		}
	})
}

// TestUpdateSidecarEncodeFailure exercises UpdateSidecar's encode-error path
// the same way: the read succeeds (the sidecar already exists, written
// before the limit is lowered), but re-marshaling and writing it back
// overflows the tiny file-size limit.
func TestUpdateSidecarEncodeFailure(t *testing.T) {
	dir := t.TempDir()
	sc := testSidecar()
	if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
		t.Fatalf("WriteSidecarExcl: %v", err)
	}
	withTinyFileSizeLimit(t, 4, func() {
		err := UpdateSidecar(dir, sc.Name, func(s *Sidecar) { s.BaseSHA = "changed" })
		if err == nil {
			t.Fatalf("UpdateSidecar under a 4-byte RLIMIT_FSIZE = nil, want a write error")
		}
	})
}
