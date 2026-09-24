package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStateHomeFor covers the stateHomeFor function (lines 173-186).
func TestStateHomeFor(t *testing.T) {
	// Valid layout: <stateHome>/evener/projects/<project-id>
	dir := filepath.Join(t.TempDir(), "evener", "projects", "myproject-0123456789")
	if got := stateHomeFor(dir); got == "" {
		t.Fatal("expected non-empty stateHome for valid layout")
	}

	// Invalid: not under evener/projects
	if got := stateHomeFor("/some/random/path"); got != "" {
		t.Fatalf("expected empty for random path, got %q", got)
	}

	// Invalid: missing "projects" segment
	if got := stateHomeFor(filepath.Join(t.TempDir(), "evener", "other")); got != "" {
		t.Fatalf("expected empty for non-projects, got %q", got)
	}

	// Invalid: missing "evener" segment
	if got := stateHomeFor(filepath.Join(t.TempDir(), "other", "projects", "x")); got != "" {
		t.Fatalf("expected empty for non-evener, got %q", got)
	}
}

// TestResolveTranscript_LegacyNamedBucketNoMatch covers a bare-id lookup in a
// legacy-named bucket dir whose session does not exist. After FU3 the bucket
// is no longer filtered by name; the lookup proceeds and returns "unknown
// session" because the transcript is absent, not because the bucket name was
// invalid.
func TestResolveTranscript_InvalidBucket(t *testing.T) {
	// A legacy-named dir under evener/projects (no matching session).
	dir := filepath.Join(t.TempDir(), "evener", "projects", "bad project")
	_, _, err := resolveTranscript("02wMz5Txv2enqVTitaig6F", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for unknown session in legacy-named bucket")
	}
}

// TestResolveTranscript_CurrentInvalidSessionID covers the invalid session
// ID for current (line 32-33).
func TestResolveTranscript_CurrentInvalidSessionID(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveTranscript("", dir, "../escaped")
	if err == nil {
		t.Fatal("expected error for invalid session ID")
	}
}

// TestResolveTranscript_Current covers the current-session happy path
// (lines 31-36).
func TestResolveTranscript_CurrentSession(t *testing.T) {
	dir := t.TempDir()
	path, ref, err := resolveTranscript("", dir, "02wMz5Txv2enqVTitaig6F")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "02wMz5Txv2enqVTitaig6F.transcript.jsonl") {
		t.Fatalf("path = %q", path)
	}
	if ref == "" {
		t.Fatal("expected non-empty ref")
	}
}

// TestResolveTranscript_PathSeparators covers the traversal guard (line
// 73-74).
func TestResolveTranscript_PathSeparators(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveTranscript("foo/bar", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil || !strings.Contains(err.Error(), "path separators") {
		t.Fatalf("error = %v, want path separators", err)
	}
}

// TestResolveTranscript_InvalidBareID covers the invalid bare session ID
// (line 78-79).
func TestResolveTranscript_InvalidBareID(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveTranscript("../escaped", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for invalid bare session ID")
	}
}

// TestResolveTranscript_NotFound covers the not-found error (line 116-117).
func TestResolveTranscript_NotFoundBare(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveTranscript("02wMz5Txv2enqVTitaig6F", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("error = %v, want unknown session", err)
	}
}

// TestResolveTranscript_LocalRefNotFound covers the local ref not-found
// path (line 66-67).
func TestResolveTranscript_LocalRefNotFound(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveTranscript("local:02wMz5Txv2enqVTitaig6F", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want not found", err)
	}
}

// TestResolveTranscript_LocalRefInvalidSessionID covers the local ref with
// invalid session ID (line 45-46).
func TestResolveTranscript_LocalRefInvalidSessionID(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveTranscript("local:../escaped", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for invalid session ID in ref")
	}
}

// TestResolveTranscript_ProjRefInvalidProjectID covers the proj ref with
// invalid project ID (line 49-50).
func TestResolveTranscript_ProjRefInvalidProjectID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evener", "projects", "myproject-0123456789")
	os.MkdirAll(dir, 0o755)
	_, _, err := resolveTranscript("proj:../bad:02wMz5Txv2enqVTitaig6F", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for invalid project ID in ref")
	}
}

// TestResolveTranscript_BadRef covers the decode-ref error (line 42-43).
func TestResolveTranscript_BadRef(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveTranscript("local:", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for bad ref")
	}
}

// TestEnumerateBuckets_GlobError covers the glob-error path (line 145-146).
func TestEnumerateBuckets_GlobError(t *testing.T) {
	orig := transcriptBucketGlob
	transcriptBucketGlob = func(pattern string) ([]string, error) {
		return nil, os.ErrInvalid
	}
	defer func() { transcriptBucketGlob = orig }()

	// Create the layout prefix so the Lstat prefix checks pass and Glob
	// is actually reached (prefix checks run before Glob after the reorder).
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "evener", "projects"), 0o755)
	_, err := enumerateBuckets(dir)
	if err == nil {
		t.Fatal("expected error for glob failure")
	}
}

// TestEnumerateBuckets_FiltersNonDirs covers the filter for non-directory
// matches (line 151-153).
func TestEnumerateBuckets_FiltersNonDirs(t *testing.T) {
	// Create a real directory structure.
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "evener", "projects", "myproject-0123456789")
	os.MkdirAll(projectDir, 0o755)
	// Create a file that would match the glob but is not a directory.
	fileDir := filepath.Join(dir, "evener", "projects")
	os.WriteFile(filepath.Join(fileDir, "notadir"), []byte("x"), 0o644)

	buckets, err := enumerateBuckets(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should include myproject-0123456789 but not notadir.
	found := false
	for _, b := range buckets {
		if filepath.Base(b) == "myproject-0123456789" {
			found = true
		}
		if filepath.Base(b) == "notadir" {
			t.Fatal("should not include non-directory match")
		}
	}
	if !found {
		t.Fatal("expected myproject-0123456789 in results")
	}
}

// TestParentBucketAndID_Current covers the current-session path (lines
// 203-210).
func TestParentBucketAndID_Current(t *testing.T) {
	dir := t.TempDir()
	bucket, id, scope, err := parentBucketAndID("", dir, "02wMz5Txv2enqVTitaig6F")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bucket != dir || id != "02wMz5Txv2enqVTitaig6F" || scope != scopeCurrentProject {
		t.Fatalf("bucket=%q id=%q scope=%q, want %q 02wMz5Txv2enqVTitaig6F %q", bucket, id, scope, dir, scopeCurrentProject)
	}
}

// TestParentBucketAndID_CurrentInvalidSessionID covers the invalid session
// ID for current (line 204-205).
func TestParentBucketAndID_CurrentInvalidSessionID(t *testing.T) {
	_, _, _, err := parentBucketAndID("", t.TempDir(), "../escaped")
	if err == nil {
		t.Fatal("expected error for invalid session ID")
	}
}

// TestParentBucketAndID_LegacyNamedBucket covers the current-session path with
// a legacy-named bucket dir. After FU3, buckets under evener/projects are no
// longer filtered by name, so parentBucketAndID succeeds.
func TestParentBucketAndID_LegacyNamedBucket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evener", "projects", "bad project")
	bucket, id, scope, err := parentBucketAndID("", dir, "02wMz5Txv2enqVTitaig6F")
	if err != nil {
		t.Fatalf("unexpected error for legacy-named bucket: %v", err)
	}
	if bucket != dir || id != "02wMz5Txv2enqVTitaig6F" || scope != scopeCurrentProject {
		t.Fatalf("bucket=%q id=%q scope=%q, want %q %q %q", bucket, id, scope, dir, "02wMz5Txv2enqVTitaig6F", scopeCurrentProject)
	}
}

// TestParentBucketAndID_BadRef covers the decode-ref error (line 214-215).
func TestParentBucketAndID_BadRef(t *testing.T) {
	_, _, _, err := parentBucketAndID("local:", t.TempDir(), "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for bad ref")
	}
}

// TestParentBucketAndID_InvalidSessionID covers the invalid session ID in
// ref (line 217-218).
func TestParentBucketAndID_InvalidSessionID(t *testing.T) {
	_, _, _, err := parentBucketAndID("local:../escaped", t.TempDir(), "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for invalid session ID")
	}
}

// TestParentBucketAndID_InvalidProjectID covers the invalid project ID in
// ref (line 221-222).
func TestParentBucketAndID_InvalidProjectID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evener", "projects", "myproject-0123456789")
	os.MkdirAll(dir, 0o755)
	_, _, _, err := parentBucketAndID("proj:../bad:02wMz5Txv2enqVTitaig6F", dir, "02wMz5Txv2enqVTitaig6F")
	if err == nil {
		t.Fatal("expected error for invalid project ID")
	}
}

// TestParentBucketAndID_LocalRef covers the local ref path (line 225-226).
func TestParentBucketAndID_LocalRef(t *testing.T) {
	dir := t.TempDir()
	bucket, id, scope, err := parentBucketAndID("local:02wMz5Txv47YP64RR3B9YJ", dir, "02wMz5Txv2enqVTitaig6F")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bucket != dir || id != "02wMz5Txv47YP64RR3B9YJ" || scope != scopeCurrentProject {
		t.Fatalf("bucket=%q id=%q scope=%q", bucket, id, scope)
	}
}
