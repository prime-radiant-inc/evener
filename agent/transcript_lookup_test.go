package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cleanBreakProjectID = "Users-jesse-serf-0123456789"
	cleanBreakSessionID = "02wMz5TxvEMoJEDTDGOTil"
)

// newStateHome creates a shared stateHome temp dir for all buckets in a test.
// Returns the stateHome path.
func newStateHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// newBucketUnder creates a new project state dir under the given stateHome.
// Returns the bucket state dir (i.e. <stateHome>/serf/projects/<projectID>).
func newBucketUnder(t *testing.T, stateHome string) string {
	t.Helper()
	// Use a unique name based on a random temp dir suffix to avoid collisions.
	tmp := t.TempDir()
	projectID := "test-" + hexHash(tmp)[:10]
	dir := filepath.Join(stateHome, "serf", "projects", projectID)
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatalf("newBucketUnder: %v", err)
	}
	return dir
}

// newBucket creates a fresh stateHome and one bucket under it.
func newBucket(t *testing.T) string {
	t.Helper()
	return newBucketUnder(t, newStateHome(t))
}

// writeTranscript creates a minimal transcript file for sessionID in bucket dir.
func writeTranscript(t *testing.T, bucketDir, sessionID string) {
	t.Helper()
	sessDir := filepath.Join(bucketDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("writeTranscript mkdir: %v", err)
	}
	path := filepath.Join(sessDir, sessionID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatalf("writeTranscript write: %v", err)
	}
}

// --- stateHomeFor ---

func TestStateHomeFor_ProjectBucket(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	bucket := newBucketUnder(t, sh)
	got := stateHomeFor(bucket)
	if got != sh {
		t.Fatalf("expected %q, got %q", sh, got)
	}
}

func TestStateHomeFor_FlatDir(t *testing.T) {
	t.Parallel()
	flat := filepath.Join(t.TempDir(), "flat")
	if err := os.MkdirAll(flat, 0o755); err != nil {
		t.Fatal(err)
	}
	got := stateHomeFor(flat)
	if got != "" {
		t.Fatalf("expected empty string for flat dir, got %q", got)
	}
}

// --- enumerateBuckets ---

func TestEnumerateBuckets_ReturnsBucketRoots(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	a := newBucketUnder(t, sh)
	b := newBucketUnder(t, sh)

	buckets, err := enumerateBuckets(sh)
	if err != nil {
		t.Fatalf("enumerateBuckets: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d: %v", len(buckets), buckets)
	}

	// Verify we get roots (not .../sessions)
	for _, b := range buckets {
		if strings.HasSuffix(b, "sessions") {
			t.Errorf("bucket %q ends with 'sessions'; should be the root", b)
		}
	}

	// Verify both known dirs are in the result
	found := map[string]bool{a: false, b: false}
	for _, bucket := range buckets {
		found[bucket] = true
	}
	for dir, ok := range found {
		if !ok {
			t.Errorf("bucket dir %q not in result", dir)
		}
	}
}

func TestEnumerateBuckets_EmptyStaleHome(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	buckets, err := enumerateBuckets(sh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(buckets) != 0 {
		t.Fatalf("expected 0 buckets, got %d", len(buckets))
	}
}

// --- resolveTranscript ---

func TestResolveTranscript_Current(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	writeTranscript(t, dir, "02wMz5TxvEMoJEDTDGOTil")
	path, ref, err := resolveTranscript("", dir, "02wMz5TxvEMoJEDTDGOTil")
	if err != nil || ref != "local:02wMz5TxvEMoJEDTDGOTil" || !strings.HasSuffix(path, "02wMz5TxvEMoJEDTDGOTil.transcript.jsonl") {
		t.Fatalf("path=%q ref=%q err=%v", path, ref, err)
	}
}

func TestResolveTranscript_CurrentKeyword(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	writeTranscript(t, dir, "02wMz5TxvEMoJEDTDGOTil")
	path, ref, err := resolveTranscript("current", dir, "02wMz5TxvEMoJEDTDGOTil")
	if err != nil || ref != "local:02wMz5TxvEMoJEDTDGOTil" || !strings.HasSuffix(path, "02wMz5TxvEMoJEDTDGOTil.transcript.jsonl") {
		t.Fatalf("path=%q ref=%q err=%v", path, ref, err)
	}
}

func TestResolveTranscript_LocalRefVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		sessionID string
		selector  string
		wantRef   string
	}{
		{
			name:      "ExplicitLocalRef",
			sessionID: "02wMz5Txv2enqVTitaig6F",
			selector:  "local:02wMz5Txv2enqVTitaig6F",
			wantRef:   "local:02wMz5Txv2enqVTitaig6F",
		},
		{
			name:      "BareIDInCurrentBucket",
			sessionID: "02wMz5Txv47YP64RR3B9YJ",
			selector:  "02wMz5Txv47YP64RR3B9YJ",
			wantRef:   "local:02wMz5Txv47YP64RR3B9YJ",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := newBucket(t)
			writeTranscript(t, dir, c.sessionID)
			path, ref, err := resolveTranscript(c.selector, dir, "02wMz5TxvEMoJEDTDGOTil")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref != c.wantRef {
				t.Fatalf("expected ref %q, got %q", c.wantRef, ref)
			}
			if !strings.HasSuffix(path, c.sessionID+".transcript.jsonl") {
				t.Fatalf("unexpected path %q", path)
			}
		})
	}
}

func TestResolveTranscript_BareIDInOtherBucket(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	a := newBucketUnder(t, sh)
	b := newBucketUnder(t, sh)
	// Only write the transcript in b, not in a.
	writeTranscript(t, b, "02wMz5Txv5aIxgf9yVdd0N")

	path, ref, err := resolveTranscript("02wMz5Txv5aIxgf9yVdd0N", a, "02wMz5TxvEMoJEDTDGOTil")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bHash := filepath.Base(b)
	expectedRef := "proj:" + bHash + ":02wMz5Txv5aIxgf9yVdd0N"
	if ref != expectedRef {
		t.Fatalf("expected ref %q, got %q", expectedRef, ref)
	}
	if !strings.HasSuffix(path, "02wMz5Txv5aIxgf9yVdd0N.transcript.jsonl") {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestResolveTranscript_AmbiguousBareID(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	a := newBucketUnder(t, sh)
	b := newBucketUnder(t, sh)
	writeTranscript(t, a, "02wMz5Txv733WHFsVy66SR")
	writeTranscript(t, b, "02wMz5Txv733WHFsVy66SR")
	_, _, err := resolveTranscript("02wMz5Txv733WHFsVy66SR", a, "02wMz5TxvEMoJEDTDGOTil")
	bHash := filepath.Base(b)
	expectedBRef := "proj:" + bHash + ":02wMz5Txv733WHFsVy66SR"
	if err == nil || !strings.Contains(err.Error(), "local:02wMz5Txv733WHFsVy66SR") || !strings.Contains(err.Error(), expectedBRef) {
		t.Fatalf("expected ambiguity error with candidates local:02wMz5Txv733WHFsVy66SR and %s, got %v", expectedBRef, err)
	}
}

func TestResolveTranscript_UnknownBareID(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	_, _, err := resolveTranscript("02wMz5TxvEMoJEDTDGOTil", dir, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil {
		t.Fatal("expected error for unknown session id")
	}
	if !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("expected 'unknown session' in error, got %v", err)
	}
}

func TestResolveTranscript_TraversalSelector(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	_, _, err := resolveTranscript("../etc/passwd", dir, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil {
		t.Fatal("expected error for traversal-like selector")
	}
}

func TestResolveTranscript_ExplicitLocalRefMissing(t *testing.T) {
	t.Parallel()
	dir := newBucket(t)
	// local: ref for a session that doesn't exist
	_, _, err := resolveTranscript("local:02wMz5Txv8Vo4rqb3QYZuV", dir, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil {
		t.Fatal("expected error for missing local ref")
	}
}

func TestResolveTranscript_ExplicitProjRef(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	a := newBucketUnder(t, sh)
	b := newBucketUnder(t, sh)
	writeTranscript(t, b, "02wMz5Txv9yYdSRJat13MZ")
	bHash := filepath.Base(b)
	ref := "proj:" + bHash + ":02wMz5Txv9yYdSRJat13MZ"
	path, gotRef, err := resolveTranscript(ref, a, "02wMz5TxvEMoJEDTDGOTil")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotRef != ref {
		t.Fatalf("expected ref %q, got %q", ref, gotRef)
	}
	if !strings.HasSuffix(path, "02wMz5Txv9yYdSRJat13MZ.transcript.jsonl") {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestResolveTranscript_ProjRefFlatStateDir(t *testing.T) {
	t.Parallel()
	// A flat state dir (not under serf/projects/<hash>) has no project root,
	// so a proj:<hash>:<id> ref must return the "no project root" error.
	flat := filepath.Join(t.TempDir(), "flatstate")
	if err := os.MkdirAll(filepath.Join(flat, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := resolveTranscript("proj:project-a-0123456789:02wMz5TxvBRJC3228LTWod", flat, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil || !strings.Contains(err.Error(), "no project root") {
		t.Fatalf("expected 'no project root' error for proj ref in flat dir, got %v", err)
	}
}

func TestResolveTranscript_FlatStateDirBareIDOnly(t *testing.T) {
	t.Parallel()
	// A flat dir (not under serf/projects/<hash>) means stateHomeFor returns "".
	// Bare ID search should only look in the current bucket.
	flat := filepath.Join(t.TempDir(), "flatstate")
	if err := os.MkdirAll(filepath.Join(flat, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, flat, "02wMz5TxvCu3kdckfnw0Gh")
	path, ref, err := resolveTranscript("02wMz5TxvCu3kdckfnw0Gh", flat, "02wMz5TxvEMoJEDTDGOTil")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != "local:02wMz5TxvCu3kdckfnw0Gh" {
		t.Fatalf("expected local ref, got %q", ref)
	}
	if !strings.HasSuffix(path, "02wMz5TxvCu3kdckfnw0Gh.transcript.jsonl") {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestResolveTranscript_CleanBreakSkipsLegacyLocalState(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	legacyBucket := filepath.Join(stateHome, "serf", "projects", "0123456789abcdef")
	newBucket := filepath.Join(stateHome, "serf", "projects", cleanBreakProjectID)
	if err := os.MkdirAll(filepath.Join(legacyBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(newBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyBucket, "sessions", "01ARZ3NDEKTSV4RRFFQ69G5FAV.transcript.jsonl")
	legacyBytes := []byte("legacy transcript\n")
	if err := os.WriteFile(legacyPath, legacyBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	legacyInfo, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, legacyBucket, cleanBreakSessionID)
	writeTranscript(t, newBucket, cleanBreakSessionID)

	if _, _, err := resolveTranscript("local:01ARZ3NDEKTSV4RRFFQ69G5FAV", legacyBucket, cleanBreakSessionID); err == nil {
		t.Fatal("legacy local session unexpectedly resolved")
	}
	path, ref, err := resolveTranscript("proj:"+cleanBreakProjectID+":"+cleanBreakSessionID, newBucket, "")
	if err != nil {
		t.Fatalf("new local session did not resolve: %v", err)
	} else if path == "" || ref != "proj:"+cleanBreakProjectID+":"+cleanBreakSessionID {
		t.Fatalf("resolved path/ref = %q/%q", path, ref)
	}

	gotBytes, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, legacyBytes) || !gotInfo.ModTime().Equal(legacyInfo.ModTime()) {
		t.Fatalf("legacy fixture changed: bytes=%q mtime=%v want bytes=%q mtime=%v", gotBytes, gotInfo.ModTime(), legacyBytes, legacyInfo.ModTime())
	}
}

func TestEnumerateBuckets_CleanBreakSkipsLegacyProjectBucket(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	for _, projectID := range []string{"0123456789abcdef", cleanBreakProjectID} {
		if err := os.MkdirAll(filepath.Join(stateHome, "serf", "projects", projectID, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	buckets, err := enumerateBuckets(stateHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || filepath.Base(buckets[0]) != cleanBreakProjectID {
		t.Fatalf("buckets = %v, want only %q", buckets, cleanBreakProjectID)
	}
}
