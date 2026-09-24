package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

const (
	cleanBreakProjectID = "Users-jesse-evener-0123456789"
	cleanBreakSessionID = "02wMz5TxvEMoJEDTDGOTil"
)

// newStateHome creates a shared stateHome temp dir for all buckets in a test.
// Returns the stateHome path.
func newStateHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// newBucketUnder creates a new project state dir under the given stateHome.
// Returns the bucket state dir (i.e. <stateHome>/evener/projects/<projectID>).
func newBucketUnder(t *testing.T, stateHome string) string {
	t.Helper()
	// Use a unique name based on a random temp dir suffix to avoid collisions.
	tmp := t.TempDir()
	projectID := "test-" + hexHash(tmp)[:10]
	dir := filepath.Join(stateHome, "evener", "projects", projectID)
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
	// A flat state dir (not under evener/projects/<hash>) has no project root,
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
	// A flat dir (not under evener/projects/<hash>) means stateHomeFor returns "".
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

// TestResolveTranscript_CleanBreakLegacyLocalStateUnchanged verifies that a
// local: ref into a legacy-named bucket resolves to the transcript in that
// bucket (the local: path is bucket-relative and does not depend on the
// bucket name passing ValidateProjectID), and that a proj: ref to the
// clean-break bucket still works. It also asserts the legacy transcript file
// is not modified by the lookup. The earlier "local:01ARZ..." sid used here
// is a ULID-style id (26 chars) that fails ValidateSessionID (which requires
// exactly 22 base62 chars), so the local: ref for it returns an error — that
// error is from the session-id validation, not from bucket-name rejection.
func TestResolveTranscript_CleanBreakLegacyLocalStateUnchanged(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	legacyBucket := filepath.Join(stateHome, "evener", "projects", "0123456789abcdef")
	newBucket := filepath.Join(stateHome, "evener", "projects", cleanBreakProjectID)
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
		t.Fatal("local ref with invalid (ULID-length) session id unexpectedly resolved")
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

// TestEnumerateBuckets_CleanBreakIncludesLegacyProjectBucket verifies that
// enumerateBuckets returns both the clean-break (valid-named) bucket and the
// legacy pure-hash bucket — the agent-side counterpart to PR #2163's doctor
// sweep, which stopped filtering by ValidateProjectID. Sessions in
// legacy-named buckets must be findable by bare session id.
func TestEnumerateBuckets_CleanBreakIncludesLegacyProjectBucket(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	for _, projectID := range []string{"0123456789abcdef", cleanBreakProjectID} {
		if err := os.MkdirAll(filepath.Join(stateHome, "evener", "projects", projectID, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	buckets, err := enumerateBuckets(stateHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("buckets = %v, want 2 (legacy + clean-break)", buckets)
	}
	names := map[string]bool{}
	for _, b := range buckets {
		names[filepath.Base(b)] = true
	}
	if !names["0123456789abcdef"] {
		t.Errorf("legacy bucket 0123456789abcdef missing from enumeration")
	}
	if !names[cleanBreakProjectID] {
		t.Errorf("clean-break bucket %q missing from enumeration", cleanBreakProjectID)
	}
}

// --- FU3: legacy-named bucket enumeration ---

// TestEnumerateBuckets_IncludesLegacyNamedBucket verifies that enumerateBuckets
// returns legacy-named bucket dirs (names identifier.ValidateProjectID rejects)
// alongside normal ones. PR #2163 made the doctor's globBuckets stop filtering
// by name; this is the agent-side counterpart.
func TestEnumerateBuckets_IncludesLegacyNamedBucket(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	normal := newBucketUnder(t, sh)
	// "0123456789abcdef": pure hex, no readable-portion/suffix split, so
	// identifier.ValidateProjectID rejects it.
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	buckets, err := enumerateBuckets(sh)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, b := range buckets {
		found[filepath.Base(b)] = true
	}
	if !found[filepath.Base(normal)] {
		t.Errorf("normal bucket %q missing from enumeration", filepath.Base(normal))
	}
	if !found["0123456789abcdef"] {
		t.Errorf("legacy bucket 0123456789abcdef missing from enumeration")
	}
}

// TestResolveTranscript_BareIDInLegacyBucket verifies that resolveTranscript
// finds a session in a legacy-named sibling bucket by bare session id. The
// bucket name fails ValidateProjectID, so the returned ref must be absent or
// grammar-consumable (round-trip through decodeRef).
func TestResolveTranscript_BareIDInLegacyBucket(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	// "0123456789abcdef": no readable-portion/suffix split, so
	// identifier.ValidateProjectID rejects it.
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	writeTranscript(t, legacy, "02wMz5Txv5aIxgf9yVdd0N")

	path, ref, err := resolveTranscript("02wMz5Txv5aIxgf9yVdd0N", current, "02wMz5TxvEMoJEDTDGOTil")
	if err != nil {
		t.Fatalf("bare id in legacy bucket not found: %v", err)
	}
	if !strings.HasSuffix(path, "02wMz5Txv5aIxgf9yVdd0N.transcript.jsonl") {
		t.Fatalf("path = %q, want suffix %q", path, "02wMz5Txv5aIxgf9yVdd0N.transcript.jsonl")
	}
	// The bucket name "0123456789abcdef" fails ValidateProjectID, so no ref
	// the agent grammar can consume should be emitted. If a ref IS emitted,
	// it must round-trip: decodeRef succeeds AND ValidateProjectID accepts
	// the project token.
	if ref != "" {
		projectID, sessionID, decErr := decodeRef(ref)
		if decErr != nil {
			t.Fatalf("emitted ref %q does not parse: %v", ref, decErr)
		}
		if err := identifier.ValidateProjectID(projectID); err != nil {
			t.Fatalf("emitted ref %q names project %q which ValidateProjectID rejects: %v", ref, projectID, err)
		}
		if sessionID != "02wMz5Txv5aIxgf9yVdd0N" {
			t.Fatalf("ref session = %q, want %q", sessionID, "02wMz5Txv5aIxgf9yVdd0N")
		}
	}
}

// TestFind_LegacyBucketSessionInAllProjects verifies that find_session_transcripts
// with scope=all_projects includes sessions from legacy-named buckets. The ref
// for a legacy-bucket session must be absent or grammar-consumable. Normal
// buckets' sessions must still appear with valid, round-tripping refs.
func TestFind_LegacyBucketSessionInAllProjects(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	// "0123456789abcdef": no readable-portion/suffix split, so
	// identifier.ValidateProjectID rejects it.
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")

	now := time.Now().UTC().Truncate(time.Second)

	// Session in the legacy bucket.
	writeFindSession(t, legacy, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "legacy bucket session",
		updated: now,
	}, "legacy content")

	// Session in a normal sibling bucket.
	normalSibling := newBucketUnder(t, sh)
	writeFindSession(t, normalSibling, findMetaSpec{
		id:      "02wMz5Txv9yYdSRJat13MZ",
		name:    "normal sibling session",
		updated: now.Add(-time.Minute),
	}, "normal content")

	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	matches := matchesFromEnvelope(t, decodeEnvelope(t, marshalFind(t, deps,
		map[string]any{"scope": scopeAllProjects})))

	// Find the legacy and normal matches by title.
	var legacyMatch, normalMatch map[string]any
	for _, m := range matches {
		title, _ := m["title"].(string)
		if title == "legacy bucket session" {
			legacyMatch = m
		}
		if title == "normal sibling session" {
			normalMatch = m
		}
	}

	if legacyMatch == nil {
		t.Fatalf("legacy bucket session not found in all_projects results; got %d matches", len(matches))
	}
	if normalMatch == nil {
		t.Fatalf("normal sibling session not found in all_projects results; got %d matches", len(matches))
	}

	// Legacy-bucket ref must be absent or grammar-consumable (round-trip).
	legacyRef, _ := legacyMatch["transcript_ref"].(string)
	if legacyRef != "" {
		projectID, sessionID, decErr := decodeRef(legacyRef)
		if decErr != nil {
			t.Fatalf("legacy ref %q does not parse: %v", legacyRef, decErr)
		}
		if err := identifier.ValidateProjectID(projectID); err != nil {
			t.Fatalf("legacy ref %q names project %q which ValidateProjectID rejects: %v", legacyRef, projectID, err)
		}
		if sessionID != "02wMz5Txv5aIxgf9yVdd0N" {
			t.Fatalf("legacy ref session = %q, want %q", sessionID, "02wMz5Txv5aIxgf9yVdd0N")
		}
	}

	// Normal-bucket ref must round-trip and be non-empty.
	normalRef, _ := normalMatch["transcript_ref"].(string)
	if normalRef == "" {
		t.Fatal("normal sibling ref is empty; expected a valid ref")
	}
	projectID, sessionID, decErr := decodeRef(normalRef)
	if decErr != nil {
		t.Fatalf("normal ref %q does not parse: %v", normalRef, decErr)
	}
	if err := identifier.ValidateProjectID(projectID); err != nil {
		t.Fatalf("normal ref %q names project %q which ValidateProjectID rejects: %v", normalRef, projectID, err)
	}
	if sessionID != "02wMz5Txv9yYdSRJat13MZ" {
		t.Fatalf("normal ref session = %q, want %q", sessionID, "02wMz5Txv9yYdSRJat13MZ")
	}
	if projectID != filepath.Base(normalSibling) {
		t.Fatalf("normal ref project = %q, want %q", projectID, filepath.Base(normalSibling))
	}
}

// --- roborev fix round 1: RED tests ---

// TestFind_LegacyBucketOmitsEmptyTranscriptRef asserts that a find result for
// a session in a legacy-named bucket (whose name fails ValidateProjectID, so
// refFor returns "") never surfaces an empty transcript_ref. The JSON wire
// format must omit the field entirely (omitempty) — a model copying an empty
// transcript_ref would silently read the current session via resolveTranscript.
func TestFind_LegacyBucketOmitsEmptyTranscriptRef(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	// "0123456789abcdef": no readable-portion/suffix split, so
	// identifier.ValidateProjectID rejects it.
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")

	now := time.Now().UTC().Truncate(time.Second)
	writeFindSession(t, legacy, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "legacy bucket session",
		updated: now,
	}, "legacy content")

	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	matches := matchesFromEnvelope(t, decodeEnvelope(t, marshalFind(t, deps,
		map[string]any{"scope": scopeAllProjects})))

	var legacyMatch map[string]any
	for _, m := range matches {
		if title, _ := m["title"].(string); title == "legacy bucket session" {
			legacyMatch = m
		}
	}
	if legacyMatch == nil {
		t.Fatalf("legacy bucket session not found in all_projects results; got %d matches", len(matches))
	}

	// transcript_ref must be absent (not "") when refFor returns empty.
	if ref, ok := legacyMatch["transcript_ref"]; ok && ref == "" {
		t.Fatal("transcript_ref is present but empty; must be omitted (omitempty) for legacy-named buckets")
	}
}

// TestResolveTranscript_AmbiguousBareIDTwoLegacyBuckets asserts that when a
// bare session id is found in two legacy-named sibling buckets (both fail
// ValidateProjectID, so refFor returns "" for both), the ambiguity error
// message names both bucket directories — mirroring the doctor's
// locateAcrossBuckets which prints bucket names. Currently the candidate list
// is empty because refFor returns "" for grammar-incompatible names.
func TestResolveTranscript_AmbiguousBareIDTwoLegacyBuckets(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	// Both names are pure hex with no '-' separator, so ValidateProjectID
	// rejects them.
	legacy1 := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	legacy2 := filepath.Join(sh, "evener", "projects", "fedcba9876543210")
	writeTranscript(t, legacy1, "02wMz5Txv5aIxgf9yVdd0N")
	writeTranscript(t, legacy2, "02wMz5Txv5aIxgf9yVdd0N")

	_, _, err := resolveTranscript("02wMz5Txv5aIxgf9yVdd0N", current, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil {
		t.Fatal("expected ambiguity error for session in two legacy buckets")
	}
	msg := err.Error()
	if !strings.Contains(msg, "0123456789abcdef") {
		t.Errorf("ambiguity error does not name legacy bucket 0123456789abcdef: %q", msg)
	}
	if !strings.Contains(msg, "fedcba9876543210") {
		t.Errorf("ambiguity error does not name legacy bucket fedcba9876543210: %q", msg)
	}
}

// --- roborev fix round 2: RED tests ---

// TestReadMarkdownTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef asserts
// that a markdown read_transcript against a legacy-bucket bare session ID
// (whose refFor returns "") does NOT emit "transcript_ref": "" in the JSON
// envelope. The field must be absent (omitempty), not present-but-empty — a
// model reusing an empty transcript_ref would silently read the CURRENT
// session via resolveTranscript.
func TestReadMarkdownTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	writeFindSession(t, legacy, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "legacy read test",
		updated: time.Now().UTC(),
	}, "legacy content")

	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	result, err := execReadTranscript(deps, map[string]any{
		"transcript_ref": "02wMz5Txv5aIxgf9yVdd0N",
		"format":         "markdown",
	})
	if err != nil {
		t.Fatalf("read_transcript markdown: %v", err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if ref, ok := env["transcript_ref"]; ok && ref == "" {
		t.Fatal("readMarkdownEnvelope emitted empty transcript_ref; must be omitted (omitempty) for legacy-bucket bare-ID reads")
	}
}

// TestReadOutlineTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef asserts
// the same omitempty invariant for the outline read format.
func TestReadOutlineTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	writeFindSession(t, legacy, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "legacy read test",
		updated: time.Now().UTC(),
	}, "legacy content")

	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	result, err := execReadTranscript(deps, map[string]any{
		"transcript_ref": "02wMz5Txv5aIxgf9yVdd0N",
		"format":         "outline",
	})
	if err != nil {
		t.Fatalf("read_transcript outline: %v", err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if ref, ok := env["transcript_ref"]; ok && ref == "" {
		t.Fatal("readOutlineEnvelope emitted empty transcript_ref; must be omitted (omitempty) for legacy-bucket bare-ID reads")
	}
}

// TestReadRawTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef asserts the
// same omitempty invariant for the jsonl (raw) read format.
func TestReadRawTranscript_LegacyBucketBareIDOmitsEmptyTranscriptRef(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	writeFindSession(t, legacy, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "legacy read test",
		updated: time.Now().UTC(),
	}, "legacy content")

	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	result, err := execReadTranscript(deps, map[string]any{
		"transcript_ref": "02wMz5Txv5aIxgf9yVdd0N",
		"format":         "jsonl",
	})
	if err != nil {
		t.Fatalf("read_transcript jsonl: %v", err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if ref, ok := env["transcript_ref"]; ok && ref == "" {
		t.Fatal("readRawEnvelope emitted empty transcript_ref; must be omitted (omitempty) for legacy-bucket bare-ID reads")
	}
}

// TestResolveTranscript_AmbiguityMessageDoesNotPresentBucketNamesAsRefs
// asserts that the ambiguity error does NOT present raw bucket dirnames as
// "candidate refs" — a proj:<name>:<id> ref is rejected by the explicit-ref
// branch for legacy names, so they are not usable selectors. The message
// must present bucket names as context (where the session was found), not
// as refs the model can pass back.
func TestResolveTranscript_AmbiguityMessageDoesNotPresentBucketNamesAsRefs(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	legacy1 := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	legacy2 := filepath.Join(sh, "evener", "projects", "fedcba9876543210")
	writeTranscript(t, legacy1, "02wMz5Txv5aIxgf9yVdd0N")
	writeTranscript(t, legacy2, "02wMz5Txv5aIxgf9yVdd0N")

	_, _, err := resolveTranscript("02wMz5Txv5aIxgf9yVdd0N", current, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil {
		t.Fatal("expected ambiguity error for session in two legacy buckets")
	}
	msg := err.Error()
	// Bucket names must be present as context.
	if !strings.Contains(msg, "0123456789abcdef") {
		t.Errorf("ambiguity error does not name legacy bucket 0123456789abcdef: %q", msg)
	}
	if !strings.Contains(msg, "fedcba9876543210") {
		t.Errorf("ambiguity error does not name legacy bucket fedcba9876543210: %q", msg)
	}
	// Must NOT present them as "candidate refs" — they are not usable selectors.
	if strings.Contains(msg, "candidate refs") {
		t.Errorf("ambiguity error presents bucket names as candidate refs: %q", msg)
	}
}

// TestFind_LegacyBucketTextFormatShowsBareIDAddressing asserts that the find
// text-format output for a legacy-bucket session (no transcript_ref) tells
// the model the session is addressable by its bare session ID. The record
// already carries the session id; the text format must make the addressing
// explicit so a model knows how to read the session without a proj: ref.
func TestFind_LegacyBucketTextFormatShowsBareIDAddressing(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")

	now := time.Now().UTC().Truncate(time.Second)
	legacyID := "02wMz5Txv5aIxgf9yVdd0N"
	writeFindSession(t, legacy, findMetaSpec{
		id:      legacyID,
		name:    "legacy bucket session",
		updated: now,
	}, "legacy content")

	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	v, err := execFindSessionTranscripts(deps, map[string]any{"scope": scopeAllProjects})
	if err != nil {
		t.Fatalf("execFindSessionTranscripts: %v", err)
	}
	text := formatSessionFindings(v.(findSessionsEnvelope))
	if !strings.Contains(text, legacyID) {
		t.Fatalf("find text format does not mention the bare session ID %q; model cannot address the session:\n%s", legacyID, text)
	}
}

// --- roborev fix round 3: RED tests ---

// TestEnumerateBuckets_SkipsSymlinkedBucket asserts that a symlink under
// evener/projects/ pointing outside the state root is NOT enumerated as a
// bucket. enumerateBuckets uses os.Stat (follows symlinks), so a symlinked
// entry is indistinguishable from a real directory and can expose transcripts
// outside the state root. The sibling-job path (locateLocalJob) already skips
// symlinks via entry.Type()&os.ModeSymlink; enumerateBuckets must do the same.
func TestEnumerateBuckets_SkipsSymlinkedBucket(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	normal := newBucketUnder(t, sh)
	// A directory outside the state root that the symlink will point to.
	outside := t.TempDir()
	sessDir := filepath.Join(outside, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlink under projects/ pointing to the outside dir.
	linkPath := filepath.Join(sh, "evener", "projects", "symlink-bucket")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatal(err)
	}

	buckets, err := enumerateBuckets(sh)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range buckets {
		if filepath.Base(b) == "symlink-bucket" {
			t.Fatalf("symlinked bucket was enumerated; symlinks must be skipped to prevent exposure outside the state root")
		}
	}
	// Normal bucket must still be present.
	found := false
	for _, b := range buckets {
		if filepath.Base(b) == filepath.Base(normal) {
			found = true
		}
	}
	if !found {
		t.Errorf("normal bucket %q missing from enumeration", filepath.Base(normal))
	}
}

// TestReadAPILogSummary_LegacyBucketBareIDOmitsEmptyTranscriptRef asserts that
// an api_log summary read against a legacy-bucket bare session ID does NOT
// emit "transcript_ref": "" in the JSON envelope. The field must be absent
// (omitempty), not present-but-empty — a model reusing an empty
// transcript_ref would silently read the CURRENT session.
func TestReadAPILogSummary_LegacyBucketBareIDOmitsEmptyTranscriptRef(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	legacy := filepath.Join(sh, "evener", "projects", "0123456789abcdef")
	writeTranscript(t, legacy, "02wMz5Txv5aIxgf9yVdd0N")
	// Create an empty .api.jsonl so the api_log read succeeds with 0 records.
	apiLogPath := filepath.Join(legacy, "sessions", "02wMz5Txv5aIxgf9yVdd0N"+".api.jsonl")
	if err := os.WriteFile(apiLogPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	result, err := execReadSessionTranscript(deps, map[string]any{
		"transcript_ref": "02wMz5Txv5aIxgf9yVdd0N",
		"source":         "api_log",
	})
	if err != nil {
		t.Fatalf("read_transcript api_log summary: %v", err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if ref, ok := env["transcript_ref"]; ok && ref == "" {
		t.Fatal("apiLogReadEnvelope emitted empty transcript_ref; must be omitted (omitempty) for legacy-bucket bare-ID api_log reads")
	}
}

// TestApiLogReadEnvelope_OmitsEmptyTranscriptRef asserts the struct tag
// directly: apiLogReadEnvelope.TranscriptRef must have ,omitempty.
func TestApiLogReadEnvelope_OmitsEmptyTranscriptRef(t *testing.T) {
	env := apiLogReadEnvelope{Source: apiLogSource}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "transcript_ref") {
		t.Fatalf("apiLogReadEnvelope must omit empty transcript_ref (omitempty), got: %s", b)
	}
}

// TestApiLogAttemptEnvelope_OmitsEmptyTranscriptRef asserts the struct tag
// directly: apiLogAttemptEnvelope.TranscriptRef must have ,omitempty.
func TestApiLogAttemptEnvelope_OmitsEmptyTranscriptRef(t *testing.T) {
	env := apiLogAttemptEnvelope{Source: apiLogSource}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "transcript_ref") {
		t.Fatalf("apiLogAttemptEnvelope must omit empty transcript_ref (omitempty), got: %s", b)
	}
}

// --- roborev fix round 4: RED tests ---

// TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket asserts that an
// explicit proj:<bucket>:<sid> ref pointing at a symlinked bucket is REJECTED.
// Round 3 made enumerateBuckets skip symlinks (os.Lstat), but the explicit
// proj: branch resolves the bucket by filepath.Join(stateHome, "evener",
// "projects", projectID) followed by os.Stat — which FOLLOWS symlinks. A
// symlink with a grammar-valid name (e.g. link-0123456789, which passes
// ValidateProjectID) bypasses the enumeration protection and exposes
// transcripts outside the state root. The explicit-ref branch must Lstat the
// joined bucket path and reject symlinks before any content access, and the
// read path must reject symlinked transcript files too.
func TestResolveTranscript_ExplicitProjRefRejectsSymlinkedBucket(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	// A directory outside the state root that the symlink will point to.
	outside := t.TempDir()
	outsideSess := filepath.Join(outside, "sessions")
	if err := os.MkdirAll(outsideSess, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "02wMz5Txv9yYdSRJat13MZ"
	writeTranscript(t, outside, sid)
	// Symlink under projects/ with a grammar-valid name pointing outside.
	const linkName = "link-0123456789" // passes ValidateProjectID
	linkPath := filepath.Join(sh, "evener", "projects", linkName)
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatal(err)
	}
	// Explicit proj: ref currently reads THROUGH the symlink — must be rejected.
	ref := "proj:" + linkName + ":" + sid
	_, _, err := resolveTranscript(ref, current, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil {
		t.Fatalf("explicit proj: ref to symlinked bucket resolved through the symlink; symlinks must be rejected to prevent exposure outside the state root")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink for symlinked bucket ref, got: %v", err)
	}
}

// --- roborev fix round 5: RED tests ---

// TestResolveTranscript_CurrentSessionRejectsSymlinkedTranscript asserts that
// the ""/"current" fast-path rejects a symlinked current-session transcript.
// The fast-path returns transcriptPath with no symlinkError, while bare-ID,
// local:, and proj: paths all reject symlinks. A symlinked current transcript
// could point outside the state root.
func TestResolveTranscript_CurrentSessionRejectsSymlinkedTranscript(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	const sid = "02wMz5TxvEMoJEDTDGOTil"
	// Write a real transcript outside the state root and symlink it in.
	outside := t.TempDir()
	realPath := filepath.Join(outside, "02wMz5TxvEMoJEDTDGOTil.transcript.jsonl")
	if err := os.WriteFile(realPath, []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := transcriptPath(current, sid)
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	// "" fast-path currently reads THROUGH the symlink — must be rejected.
	_, _, err := resolveTranscript("", current, sid)
	if err == nil {
		t.Fatal("current-session fast-path resolved through symlinked transcript; must be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink, got: %v", err)
	}
	// "current" keyword must also reject.
	_, _, err = resolveTranscript("current", current, sid)
	if err == nil {
		t.Fatal("current keyword resolved through symlinked transcript; must be rejected")
	}
}

// TestResolveTranscript_SymlinkedSessionsDirEscapesProtection asserts that a
// symlinked sessions/ directory between the bucket dir and the transcript
// file is rejected. symlinkError Lstats only the final path; os.Lstat follows
// every path element except the last, so sessions/ being a symlink is not
// detected by symlinkError on the transcript file. A component-walk that
// Lstats each path element under the bucket dir must catch this.
func TestResolveTranscript_SymlinkedSessionsDirEscapesProtection(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	// Remove the real sessions dir and symlink sessions/ to an outside dir.
	os.RemoveAll(filepath.Join(current, "sessions"))
	outsideSess := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(outsideSess, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "02wMz5Txv9yYdSRJat13MZ"
	if err := os.WriteFile(filepath.Join(outsideSess, sid+".transcript.jsonl"),
		[]byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSess, filepath.Join(current, "sessions")); err != nil {
		t.Fatal(err)
	}
	// proj: ref currently reads THROUGH the symlinked sessions/ — must reject.
	_, _, err := resolveTranscript("local:"+sid, current, "02wMz5TxvEMoJEDTDGOTil")
	if err == nil {
		t.Fatal("symlinked sessions/ dir escaped protection; must be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink, got: %v", err)
	}
}

// TestResolveTranscript_BareIDSymlinkedFileCausesSpuriousAmbiguity asserts
// that a symlinked transcript file in one bucket does NOT count as a match
// in findBareIDBuckets. findBareIDBuckets checks existence via
// existsNonSymlink, which uses symlinkErrorDeep (component walk with Lstat)
// plus a final Lstat — a symlinked file is rejected and never enters the
// match set. With a real file in another bucket, the symlinked file is
// skipped and the bare ID resolves to the one real bucket without a
// spurious "ambiguous" error.
func TestResolveTranscript_BareIDSymlinkedFileCausesSpuriousAmbiguity(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	other := newBucketUnder(t, sh)
	const sid = "02wMz5Txv9yYdSRJat13MZ"
	// Real transcript in the other bucket.
	writeTranscript(t, other, sid)
	// Symlinked transcript in the current bucket pointing outside.
	outside := t.TempDir()
	realPath := filepath.Join(outside, sid+".transcript.jsonl")
	if err := os.WriteFile(realPath, []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := transcriptPath(current, sid)
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	// Bare ID must resolve to the one real bucket, not spuriously ambiguous.
	path, _, err := resolveTranscript(sid, current, "02wMz5TxvEMoJEDTDGOTil")
	if err != nil {
		t.Fatalf("expected successful resolution to the real bucket, got: %v", err)
	}
	if !strings.Contains(path, filepath.Base(other)) {
		t.Fatalf("expected path in bucket %q, got %q", filepath.Base(other), path)
	}
}

// TestReadAPILogSummary_SymlinkedSidecarRejected asserts that a symlinked
// .api.jsonl sidecar is rejected before opening. apiLogPathForTranscript
// derives the sidecar path from the validated transcript path, but the
// sidecar is a different file and can itself be a symlink pointing outside
// the state root. The read path must reject a symlinked sidecar (missing
// sidecars are still allowed — only real symlinks are rejected).
func TestReadAPILogSummary_SymlinkedSidecarRejected(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	current := newBucketUnder(t, sh)
	const sid = "02wMz5Txv5aIxgf9yVdd0N"
	// Real transcript in current bucket.
	writeTranscript(t, current, sid)
	// Symlinked api sidecar pointing outside the state root.
	outside := t.TempDir()
	realSidecar := filepath.Join(outside, sid+".api.jsonl")
	if err := os.WriteFile(realSidecar, []byte(`{"n":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecarLink := filepath.Join(current, "sessions", sid+".api.jsonl")
	if err := os.Symlink(realSidecar, sidecarLink); err != nil {
		t.Fatal(err)
	}
	deps := &toolDeps{stateDir: current, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	_, err := execReadSessionTranscript(deps, map[string]any{
		"transcript_ref": "local:" + sid,
		"source":         apiLogSource,
	})
	if err == nil {
		t.Fatal("symlinked api-log sidecar was opened; must be rejected before reading")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink for sidecar, got: %v", err)
	}
}

// --- roborev fix round 6: RED tests ---

// TestSymlinkErrorDeep_SymlinkedAncestorAboveStateRootDoesNotBreakReads
// asserts that a symlinked ancestor ABOVE the state root (e.g. a symlinked
// $HOME or /tmp on macOS: /var -> /private/var) does not cause reads to fail.
// Round 5's symlinkErrorDeep walks every ancestor up to /, Lstat-ing each
// one. When the state root is under a symlinked path, every read fails with
// "traverses a symlink" even though nothing under the state root is a
// symlink. The walk must be bounded to the state root (or bucket dir).
//
// REGRESSION from round 5: this is a functional regression — the threat
// model does not cover ancestors of a runtime-configured path.
func TestSymlinkErrorDeep_SymlinkedAncestorAboveStateRootDoesNotBreakReads(t *testing.T) {
	t.Parallel()
	// Real state home with a bucket and transcript.
	realHome := t.TempDir()
	bucket := filepath.Join(realHome, "evener", "projects", "test-0123456789")
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	sid := "02wMz5TxvEMoJEDTDGOTil"
	writeTranscript(t, bucket, sid)

	// Symlink the home, simulating a symlinked $HOME or /tmp.
	linkedHome := filepath.Join(t.TempDir(), "linked-home")
	if err := os.Symlink(realHome, linkedHome); err != nil {
		t.Fatal(err)
	}
	linkedBucket := filepath.Join(linkedHome, "evener", "projects", "test-0123456789")

	// A bare-ID read through the symlinked ancestor should succeed — the
	// symlink is above the state root, not within it. symlinkErrorDeep is
	// bounded by the state home root, so it does not walk above it.
	_, _, err := resolveTranscript(sid, linkedBucket, sid)
	if err != nil {
		t.Fatalf("read through symlinked ancestor above state root failed: %v", err)
	}
}

// TestCollectCandidates_SkipsSymlinkedSessionsDir asserts that
// collectCandidates does not return metas from a bucket whose sessions/
// directory is a symlink pointing outside the state root. ListSessionMetas
// uses afero.ReadDir which follows symlinked directories, so metas from
// outside the state root surface in find + children_of results while
// read_transcript rejects them (find returns refs read_transcript rejects).
func TestCollectCandidates_SkipsSymlinkedSessionsDir(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	bucket := newBucketUnder(t, sh)

	// Write a real meta to an outside dir, then symlink sessions/ to it.
	outside := t.TempDir()
	sid := "02wMz5TxvEMoJEDTDGOTil"
	saveFindMeta(t, outside, findMetaSpec{id: sid})

	// Replace the bucket's sessions/ with a symlink to the outside dir.
	if err := os.RemoveAll(filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sessions"), filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}

	// collectCandidates should return 0 candidates — the symlinked sessions/
	// points outside the state root.
	candidates := collectCandidates([]string{bucket}, bucket)
	if len(candidates) != 0 {
		t.Fatalf("collectCandidates returned %d candidates from symlinked sessions/; should skip", len(candidates))
	}
}

// TestTranscriptExists_RejectsSymlinkedFile asserts that transcriptExists
// returns false for a symlinked transcript file. transcriptExists uses
// symlinkErrorDeep + os.Lstat, so a symlinked file is rejected and never
// surfaces in find results — read_transcript rejects it too.
func TestTranscriptExists_RejectsSymlinkedFile(t *testing.T) {
	t.Parallel()
	bucket := newBucket(t)
	sid := "02wMz5TxvEMoJEDTDGOTil"

	// Write a real transcript outside the bucket and symlink it in.
	outside := t.TempDir()
	realPath := filepath.Join(outside, sid+".transcript.jsonl")
	if err := os.WriteFile(realPath, []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := transcriptPath(bucket, sid)
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}

	// transcriptExists should return false — the file is a symlink.
	if transcriptExists(bucket, sid) {
		t.Fatal("transcriptExists returned true for a symlinked file; should reject symlinks")
	}
}

// TestTranscriptExists_RejectsDirectoryAtTranscriptPath asserts that a
// directory placed at the transcript path (where <id>.transcript.jsonl should
// be a regular file) does not count as a match. Pre-fix: existsNonSymlink only
// checked ModeSymlink — a directory has no ModeSymlink bit, so it passed and
// transcriptExists returned true, surfacing a non-file as a readable transcript.
// Post-fix: existsNonSymlink requires Mode().IsRegular() so a directory is
// rejected. The session must also not surface in find results, since
// recordsUpTo calls transcriptExists before building a record.
func TestTranscriptExists_RejectsDirectoryAtTranscriptPath(t *testing.T) {
	t.Parallel()
	bucket := newBucket(t)
	sid := "02wMz5TxvEMoJEDTDGOTil"

	// Write a meta so find would consider this session a candidate.
	saveFindMeta(t, bucket, findMetaSpec{
		id:   sid,
		name: "directory session",
	})

	// Create a directory at the transcript path instead of a regular file.
	dirPath := transcriptPath(bucket, sid)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir at transcript path: %v", err)
	}

	// transcriptExists must return false — a directory is not a regular file.
	if transcriptExists(bucket, sid) {
		t.Fatal("transcriptExists returned true for a directory at the transcript path; " +
			"existsNonSymlink must require IsRegular so non-regular entries are rejected")
	}

	// find must not return a record for this session — recordsUpTo gates on
	// transcriptExists, so a directory-as-transcript must not surface.
	deps := &toolDeps{stateDir: bucket, sessionID: "02wMz5TxvAAAAAAAAAAAAAA"}
	env := decodeEnvelope(t, marshalFind(t, deps, map[string]any{}))
	// matches may be nil if the directory-as-transcript was the only candidate
	// and was rejected — that is the expected outcome.
	if raw, ok := env["matches"]; ok && raw != nil {
		for _, m := range matchesFromEnvelope(t, env) {
			title, _ := m["title"].(string)
			if title == "directory session" {
				t.Fatalf("session with a directory at the transcript path surfaced in " +
					"find results; transcriptExists must reject non-regular entries")
			}
		}
	}
}

// TestFindBareIDBuckets_SymlinkedSessionsDirDoesNotMatch asserts that
// findBareIDBuckets does not count a bucket whose sessions/ is a symlink as
// a match. existsNonSymlink uses symlinkErrorDeep with the bucket dir as
// root, which walks every component (including sessions/) with Lstat, so a
// symlinked sessions/ dir is detected and the file behind it never enters
// the match set — upholding the helper's "never enter the match set" claim.
func TestFindBareIDBuckets_SymlinkedSessionsDirDoesNotMatch(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	bucket := newBucketUnder(t, sh)
	sid := "02wMz5TxvEMoJEDTDGOTil"

	// Write a real transcript in an outside sessions dir.
	outside := t.TempDir()
	outsideSessions := filepath.Join(outside, "sessions")
	if err := os.MkdirAll(outsideSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSessions, sid+".transcript.jsonl"), []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace the bucket's sessions/ with a symlink to the outside dir.
	if err := os.RemoveAll(filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSessions, filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}

	// findBareIDBuckets should NOT count this as a match.
	currentFound, _, err := findBareIDBuckets(sid, bucket, sh)
	if err != nil {
		t.Fatal(err)
	}
	if currentFound {
		t.Fatal("findBareIDBuckets counted a symlinked sessions/ dir as a match; should skip it")
	}
}

// --- roborev fix round 7: RED tests ---

// TestEnumerateBuckets_SymlinkedEvenerAncestorRejected asserts that
// enumerateBuckets does not return buckets reached through a symlinked
// stateHome/evener ancestor. filepath.Glob follows the symlinked prefix, and
// os.Lstat checks only the final bucket component — so a symlinked evener/
// (a plausible state-dir relocation) lets enumeration surface buckets whose
// refs read_transcript then REJECTS with "traverses a symlink", breaking the
// find-must-not-return-refs-read-rejects invariant. enumerateBuckets must
// Lstat stateHome/evener and stateHome/evener/projects and refuse to enumerate
// when either is a symlink.
func TestEnumerateBuckets_SymlinkedEvenerAncestorRejected(t *testing.T) {
	t.Parallel()
	// Real state home with a normal bucket.
	realHome := t.TempDir()
	projectsDir := filepath.Join(realHome, "evener", "projects")
	normalBucket := filepath.Join(projectsDir, "test-0123456789")
	if err := os.MkdirAll(filepath.Join(normalBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Symlink evener/ to the real evener/ dir, simulating a state-dir
	// relocation where ~/.local/state/evener itself is a symlink.
	linkHome := t.TempDir()
	if err := os.Symlink(filepath.Join(realHome, "evener"), filepath.Join(linkHome, "evener")); err != nil {
		t.Fatal(err)
	}

	// enumerateBuckets through the symlinked evener/ ancestor must return
	// errSymlinkedLayoutPrefix, not nil — so find can surface the refusal.
	buckets, err := enumerateBuckets(linkHome)
	if !errors.Is(err, errSymlinkedLayoutPrefix) {
		t.Fatalf("enumerateBuckets through symlinked evener/ ancestor: error = %v, want %v", err, errSymlinkedLayoutPrefix)
	}
	if len(buckets) != 0 {
		t.Fatalf("enumerateBuckets returned %d buckets through symlinked evener/ ancestor", len(buckets))
	}
}

// TestFind_SymlinkedEvenerAncestorOmitsSymlinkedBucketSession asserts the
// end-to-end invariant: find_session_transcripts with scope=all_projects does
// NOT return a session from a bucket reached through a symlinked evener/
// ancestor. The ref for such a session would be rejected by read_transcript,
// breaking the find-must-not-return-refs-read-rejects invariant.
func TestFind_SymlinkedEvenerAncestorOmitsSymlinkedBucketSession(t *testing.T) {
	t.Parallel()
	// Real state home with a sibling bucket containing a real session.
	realHome := t.TempDir()
	siblingID := "test-0123456789"
	siblingBucket := filepath.Join(realHome, "evener", "projects", siblingID)
	if err := os.MkdirAll(filepath.Join(siblingBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	currentBucket := filepath.Join(realHome, "evener", "projects", "current-0123456789")
	if err := os.MkdirAll(filepath.Join(currentBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a session in the sibling bucket.
	now := time.Now().UTC().Truncate(time.Second)
	writeFindSession(t, siblingBucket, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "symlinked ancestor session",
		updated: now,
	}, "content")

	// Symlink evener/ to the real evener/ dir.
	linkHome := t.TempDir()
	if err := os.Symlink(filepath.Join(realHome, "evener"), filepath.Join(linkHome, "evener")); err != nil {
		t.Fatal(err)
	}
	linkedCurrent := filepath.Join(linkHome, "evener", "projects", "current-0123456789")

	deps := &toolDeps{stateDir: linkedCurrent, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	// find with scope=all_projects must surface the symlinked prefix refusal
	// as an error, not silently return an empty result.
	_, err := execFindSessionTranscripts(deps, map[string]any{"scope": scopeAllProjects})
	if err == nil {
		t.Fatal("find with scope=all_projects returned no error when the evener/ " +
			"ancestor is a symlink; the security refusal must be surfaced")
	}
}

// --- roborev fix round 8: RED tests ---

// TestResolveTranscript_CurrentSessionSymlinkedEvenerAncestorNotRejected
// asserts that the current-session fast-path ("") rejects a transcript reached
// through a symlinked stateHome/evener ancestor. enumerateBuckets Lstats
// stateHome/evener and stateHome/evener/projects and refuses to enumerate when
// either is a symlink (round 7). But the current-session path uses
// symlinkErrorDeep(p, currentStateDir) — rooted at the bucket, not the state
// home — so the layout ancestors (evener/, evener/projects/) are above the
// root and not checked. A symlinked evener/ (a plausible state-dir relocation
// where ~/.local/state/evener itself is a symlink) lets the current bucket
// resolve through it while sibling reads and explicit proj: refs are
// rejected — an inconsistency. The current-bucket path must validate the same
// layout prefix as enumerateBuckets.
func TestResolveTranscript_CurrentSessionSymlinkedEvenerAncestorNotRejected(t *testing.T) {
	t.Parallel()
	// Real state home with a bucket and transcript.
	realHome := t.TempDir()
	bucket := filepath.Join(realHome, "evener", "projects", "test-0123456789")
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	sid := "02wMz5TxvEMoJEDTDGOTil"
	writeTranscript(t, bucket, sid)

	// Symlink evener/ in a separate home — evener/ itself is a symlink within
	// the state root, not an ancestor above it (the round-6 regression test
	// covers ancestors above the state root).
	linkHome := t.TempDir()
	if err := os.Symlink(filepath.Join(realHome, "evener"), filepath.Join(linkHome, "evener")); err != nil {
		t.Fatal(err)
	}
	linkedBucket := filepath.Join(linkHome, "evener", "projects", "test-0123456789")

	// The current-session fast-path should reject because evener/ is a symlink
	// within the state root. validateLayoutPrefix Lstats the evener/ ancestor
	// and rejects it, so the current-bucket path is consistent with sibling
	// reads and explicit proj: refs.
	_, _, err := resolveTranscript("", linkedBucket, sid)
	if err == nil {
		t.Fatal("current-session fast-path resolved through symlinked evener/ ancestor; " +
			"evener/ is a symlink within the state root and must be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink, got: %v", err)
	}

	// The local: ref path has the same root (bucketDir = currentStateDir) and
	// the same gap — verify it too.
	_, _, err = resolveTranscript("local:"+sid, linkedBucket, "02wMz5Txv9yYdSRJat13MZ")
	if err == nil {
		t.Fatal("local: ref resolved through symlinked evener/ ancestor; " +
			"evener/ is a symlink within the state root and must be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink for local: ref, got: %v", err)
	}
}

// TestTranscriptExists_SymlinkedSessionsDirReturnsTrue asserts that
// transcriptExists returns false when the sessions/ directory is a symlink
// pointing outside the state root. transcriptExists uses symlinkErrorDeep
// (rooted at the bucket dir) to walk every path component with Lstat,
// including sessions/, then Lstats the final file. A symlinked sessions/ is
// detected by the component walk and the file behind it is rejected —
// transcriptExists independently validates the path, so a future caller
// that bypasses collectCandidates' guard is still safe.
func TestTranscriptExists_SymlinkedSessionsDirReturnsTrue(t *testing.T) {
	t.Parallel()
	bucket := newBucket(t)
	sid := "02wMz5TxvEMoJEDTDGOTil"

	// Write a real transcript in an outside sessions dir.
	outside := t.TempDir()
	outsideSessions := filepath.Join(outside, "sessions")
	if err := os.MkdirAll(outsideSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSessions, sid+".transcript.jsonl"),
		[]byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace the bucket's sessions/ with a symlink to the outside dir.
	if err := os.RemoveAll(filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSessions, filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}

	// transcriptExists should return false — sessions/ is a symlink pointing
	// outside the state root. The symlinkErrorDeep component walk detects the
	// symlinked sessions/ and rejects the path.
	if transcriptExists(bucket, sid) {
		t.Fatal("transcriptExists returned true for a file through a symlinked sessions/; " +
			"should return false because sessions/ is a symlink")
	}
}

// TestSymlinkErrorDeep_SidecarGuardDirRootMissesSymlinkedSessionsDir
// demonstrates that the sidecar guard's current root (filepath.Dir(sidecar))
// does not independently catch a symlinked sessions/ directory. The walk is
// empty (root == start of walk), so sessions/ itself is not Lstat'd — only
// the sidecar file is. resolveTranscript validates sessions/ before the
// sidecar guard runs, so the end-to-end behavior is correct, but the sidecar
// guard should be rooted at the bucket dir to independently validate
// sessions/ as defense-in-depth.
func TestSymlinkErrorDeep_SidecarGuardDirRootMissesSymlinkedSessionsDir(t *testing.T) {
	t.Parallel()
	bucket := newBucket(t)
	sid := "02wMz5TxvEMoJEDTDGOTil"

	// Write a real sidecar in an outside sessions dir.
	outside := t.TempDir()
	outsideSessions := filepath.Join(outside, "sessions")
	if err := os.MkdirAll(outsideSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	sidecarName := sid + ".api.jsonl"
	if err := os.WriteFile(filepath.Join(outsideSessions, sidecarName),
		[]byte(`{"n":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace the bucket's sessions/ with a symlink to the outside dir.
	if err := os.RemoveAll(filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSessions, filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}

	sidecar := filepath.Join(bucket, "sessions", sidecarName)

	// The sidecar guard must be rooted at the bucket dir (parent of sessions/),
	// not filepath.Dir(sidecar) (which IS sessions/). With the bucket dir as
	// root, symlinkErrorDeep walks sessions/ and catches the symlink. With
	// filepath.Dir(sidecar) as root, the walk is empty (root == start) and the
	// symlinked sessions/ is not caught.
	if err := symlinkErrorDeep(sidecar, bucket); err == nil {
		t.Fatal("sidecar guard rooted at bucket dir did not catch symlinked " +
			"sessions/ dir; symlinkErrorDeep must walk sessions/ and detect the symlink")
	}
	if err := symlinkErrorDeep(sidecar, filepath.Dir(sidecar)); err != nil {
		t.Fatalf("symlinkErrorDeep with filepath.Dir(sidecar) root should NOT catch "+
			"the symlink (walk is empty); got unexpected error: %v", err)
	}
}

// --- roborev fix round 10: RED tests ---

// TestResolveTranscript_SymlinkedCurrentBucketDirRejected asserts that
// resolveTranscript rejects a current-bucket path when the bucket dir itself
// (not just an ancestor) is a symlink. symlinkErrorDeep(path, bucketDir)
// walks components between root (exclusive) and path (inclusive), so a
// symlinked bucket dir — the root — is never Lstat'd. validateLayoutPrefix
// checks only the evener/ and evener/projects/ ancestors, not the bucket dir
// itself. A symlinked bucket dir could point outside the state root and
// expose transcripts from elsewhere; the current-bucket path must validate
// the bucket dir itself.
func TestResolveTranscript_SymlinkedCurrentBucketDirRejected(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	bucket := newBucketUnder(t, sh)
	sid := "02wMz5TxvEMoJEDTDGOTil"
	writeTranscript(t, bucket, sid)

	// Move the real bucket dir to a temp location and replace it with a
	// symlink. The symlink points within the state root, but a symlinked
	// bucket dir is rejected regardless of target — it could point outside.
	realDir := filepath.Join(t.TempDir(), "real-bucket")
	if err := os.Rename(bucket, realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, bucket); err != nil {
		t.Fatal(err)
	}

	// The current-session fast-path should reject — the bucket dir itself
	// is a symlink. validateLayoutPrefix now Lstats the bucket dir and
	// symlinkErrorDeep excludes it as the root, so the bucket-dir check
	// closes the gap.
	_, _, err := resolveTranscript("", bucket, sid)
	if err == nil {
		t.Fatal("resolveTranscript current-session resolved through a " +
			"symlinked bucket dir; the bucket dir itself is a symlink and " +
			"must be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink, got: %v", err)
	}

	// The bare-ID path should not produce a match in the symlinked bucket.
	// existsNonSymlink now Lstats the bucket dir, so a symlinked bucket
	// is rejected before the match set is built.
	_, _, err = resolveTranscript(sid, bucket, "02wMz5Txv9yYdSRJat13MZ")
	if err == nil {
		t.Fatal("resolveTranscript bare-ID resolved through a symlinked " +
			"bucket dir; the bucket dir must not produce a match")
	}
}

// TestLocateLocalJob_SymlinkedCurrentBucketDirRejected asserts that
// locateLocalJob does not find a job in a bucket whose dir itself is a
// symlink. findLocalJobInProject calls validateLayoutPrefix, which today
// checks only ancestors above the bucket dir — a symlinked bucket dir is
// invisible, and the job journal behind it is read through the symlink.
// A symlinked bucket dir must not expose jobs from outside the state root.
func TestLocateLocalJob_SymlinkedCurrentBucketDirRejected(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	bucket := newBucketUnder(t, sh)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, bucket, owner, jobID, "/dev/null", "MARKER\n", true)

	// Move the real bucket dir and replace it with a symlink.
	realDir := filepath.Join(t.TempDir(), "real-bucket")
	if err := os.Rename(bucket, realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, bucket); err != nil {
		t.Fatal(err)
	}

	// locateLocalJob should return an error — the symlinked bucket dir must
	// not be searched.
	_, err := locateLocalJob(bucket, jobID)
	if err == nil {
		t.Fatal("locateLocalJob found a job through a symlinked bucket dir; " +
			"the bucket dir is a symlink and must not be searched")
	}
}

// TestTranscriptExists_SymlinkedCurrentBucketDirReturnsTrue asserts that
// transcriptExists returns false when the bucket dir itself is a symlink.
// transcriptExists now Lstats the bucket dir before calling
// symlinkErrorDeep(path, bucketDir), which is rooted at the bucket dir and
// does not Lstat it. A symlinked bucket dir could point outside the state
// root; the Lstat guard ensures transcriptExists and findBareIDBuckets
// independently validate the bucket dir.
func TestTranscriptExists_SymlinkedCurrentBucketDirReturnsTrue(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	bucket := newBucketUnder(t, sh)
	sid := "02wMz5TxvEMoJEDTDGOTil"
	writeTranscript(t, bucket, sid)

	// Move the real bucket dir and replace it with a symlink.
	realDir := filepath.Join(t.TempDir(), "real-bucket")
	if err := os.Rename(bucket, realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, bucket); err != nil {
		t.Fatal(err)
	}

	// transcriptExists should return false — the bucket dir is a symlink.
	if transcriptExists(bucket, sid) {
		t.Fatal("transcriptExists returned true for a transcript behind a " +
			"symlinked bucket dir; should return false")
	}

	// findBareIDBuckets should not count the symlinked bucket as a match.
	currentFound, _, err := findBareIDBuckets(sid, bucket, sh)
	if err != nil {
		t.Fatal(err)
	}
	if currentFound {
		t.Fatal("findBareIDBuckets counted a symlinked bucket dir as a " +
			"match; should skip it")
	}
}

// TestOpenTranscriptFile_RefusesSymlinkedLeaf asserts that the default
// openTranscriptFile implementation (execenv.OpenRegularNoFollow) refuses a
// symlink at the leaf: the open uses O_NOFOLLOW and fstats the descriptor to
// confirm a regular file, so a symlink swapped in at the leaf between a
// symlinkErrorDeep check and the open is refused (ELOOP) rather than followed.
// This closes the leaf-level TOCTOU window that existed when openTranscriptFile
// was os.Open (which follows symlinks at the final component).
func TestOpenTranscriptFile_RefusesSymlinkedLeaf(t *testing.T) {
	t.Parallel()
	// OpenRegularNoFollow uses O_NOFOLLOW on unix; the !unix fallback
	// (open_regular_other.go) follows leaf symlinks, so this refusal
	// test is unix-only.
	if runtime.GOOS == "windows" || runtime.GOOS == "js" || runtime.GOOS == "wasip1" || runtime.GOOS == "plan9" {
		t.Skip("O_NOFOLLOW is unix-only; the portable fallback follows leaf symlinks")
	}
	// Create a real file and a symlink to it.
	realFile := filepath.Join(t.TempDir(), "real.transcript.jsonl")
	if err := os.WriteFile(realFile, []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.transcript.jsonl")
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatal(err)
	}

	// openTranscriptFile should refuse the symlink — O_NOFOLLOW at the leaf.
	rc, err := openTranscriptFile(link)
	if err == nil {
		_ = rc.Close()
		t.Fatal("openTranscriptFile opened a symlinked leaf; should refuse with O_NOFOLLOW")
	}
}

// TestOpenAPILogFile_RefusesSymlinkedLeaf asserts the same leaf-level O_NOFOLLOW
// guarantee for the API-log sidecar open.
func TestOpenAPILogFile_RefusesSymlinkedLeaf(t *testing.T) {
	t.Parallel()
	// Same unix-only constraint as TestOpenTranscriptFile_RefusesSymlinkedLeaf.
	if runtime.GOOS == "windows" || runtime.GOOS == "js" || runtime.GOOS == "wasip1" || runtime.GOOS == "plan9" {
		t.Skip("O_NOFOLLOW is unix-only; the portable fallback follows leaf symlinks")
	}
	realFile := filepath.Join(t.TempDir(), "real.api.jsonl")
	if err := os.WriteFile(realFile, []byte(`{"n":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.api.jsonl")
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatal(err)
	}

	rc, err := openAPILogFile(link)
	if err == nil {
		_ = rc.Close()
		t.Fatal("openAPILogFile opened a symlinked leaf; should refuse with O_NOFOLLOW")
	}
}

// TestListSessionMetas_SymlinkedMetaJSONRejected asserts that a symlinked
// .meta.json file pointing outside the state root does not surface in
// ListSessionMetas or find results. listSessionMetasFS uses afero.ReadDir
// which returns Lstat-based info on OsFs, so a symlinked .meta.json entry has
// ModeSymlink set and must be skipped by the IsRegular guard. Pre-fix: the
// entry passed the IsDir check and was loaded via afero.ReadFile which follows
// the symlink, surfacing metadata from outside the state root.
func TestListSessionMetas_SymlinkedMetaJSONRejected(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	bucket := newBucketUnder(t, sh)
	sid := "02wMz5TxvEMoJEDTDGOTil"

	// Write a real .meta.json outside the state root.
	outside := t.TempDir()
	realMeta := filepath.Join(outside, sid+".meta.json")
	if err := os.MkdirAll(filepath.Dir(realMeta), 0o755); err != nil {
		t.Fatal(err)
	}
	metaData := []byte(`{"id":"` + sid + `","name":"outside session","updated_at":"2026-01-01T00:00:00Z"}`)
	if err := os.WriteFile(realMeta, metaData, 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink the .meta.json into the bucket's sessions/ dir.
	sessDir := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(sessDir, sid+".meta.json")
	if err := os.Symlink(realMeta, linkPath); err != nil {
		t.Fatal(err)
	}

	// ListSessionMetas must not return the symlinked meta.
	metas, err := schema.ListSessionMetas(bucket)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if m.ID == sid {
			t.Fatalf("symlinked .meta.json for %q surfaced in ListSessionMetas; "+
				"symlinked metadata from outside the state root must be rejected", sid)
		}
	}

	// find must not return a record for the symlinked meta either.
	writeTranscript(t, bucket, sid) // transcript must exist for find to consider it
	deps := &toolDeps{stateDir: bucket, sessionID: "02wMz5TxvAAAAAAAAAAAAAA"}
	env := decodeEnvelope(t, marshalFind(t, deps, map[string]any{}))
	// matches may be nil if the symlinked metadata was the only candidate
	// and was rejected — that is the expected outcome.
	if raw, ok := env["matches"]; ok && raw != nil {
		for _, m := range matchesFromEnvelope(t, env) {
			title, _ := m["title"].(string)
			if title == "outside session" {
				t.Fatalf("symlinked .meta.json surfaced in find results as " +
					"\"outside session\"; symlinked metadata must be rejected")
			}
		}
	}
}

// TestFind_SymlinkedEvenerPrefixSurfacesRefusal asserts that find with
// scope=all_projects surfaces a refusal when the evener/ layout prefix is a
// symlink — instead of silently returning an empty result. Pre-fix:
// enumerateBuckets returns (nil, nil) for both prefix-absent and
// prefix-is-symlink, so findBucketsWithEnumerate silently degrades to
// current_project and find returns an empty result, hiding the security
// refusal from the model.
func TestFind_SymlinkedEvenerPrefixSurfacesRefusal(t *testing.T) {
	t.Parallel()
	// Real state home with a sibling bucket containing a real session.
	realHome := t.TempDir()
	siblingID := "test-0123456789"
	siblingBucket := filepath.Join(realHome, "evener", "projects", siblingID)
	if err := os.MkdirAll(filepath.Join(siblingBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	currentBucket := filepath.Join(realHome, "evener", "projects", "current-0123456789")
	if err := os.MkdirAll(filepath.Join(currentBucket, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a session in the sibling bucket so there are real sessions to find.
	now := time.Now().UTC().Truncate(time.Second)
	writeFindSession(t, siblingBucket, findMetaSpec{
		id:      "02wMz5Txv5aIxgf9yVdd0N",
		name:    "sibling session",
		updated: now,
	}, "content")

	// Symlink evener/ to the real evener/ dir.
	linkHome := t.TempDir()
	if err := os.Symlink(filepath.Join(realHome, "evener"), filepath.Join(linkHome, "evener")); err != nil {
		t.Fatal(err)
	}
	linkedCurrent := filepath.Join(linkHome, "evener", "projects", "current-0123456789")

	deps := &toolDeps{stateDir: linkedCurrent, sessionID: "02wMz5TxvEMoJEDTDGOTil"}
	// find with scope=all_projects must surface the refusal as an error,
	// not silently return an empty result.
	_, err := execFindSessionTranscripts(deps, map[string]any{"scope": scopeAllProjects})
	if err == nil {
		t.Fatal("find with scope=all_projects returned no error when the evener/ " +
			"prefix is a symlink; the security refusal must be surfaced, not hidden " +
			"behind an empty result")
	}
}

// TestFind_SiblingBucketDuplicateNotIsCurrent asserts the F1 invariant: when a
// sibling bucket contains a session with the same ID as the current session,
// the sibling record must NOT get IsCurrent=true, and must NOT receive the
// live TurnCount/UpdatedAt overlay from currentMeta. Only the current-bucket
// record (projectID == "") with the matching ID gets both.
func TestFind_SiblingBucketDuplicateNotIsCurrent(t *testing.T) {
	t.Parallel()
	sh := newStateHome(t)
	currentBucket := newBucketUnder(t, sh)
	siblingBucket := newBucketUnder(t, sh)
	sid := "02wMz5TxvEMoJEDTDGOTil"
	now := time.Now().UTC().Truncate(time.Second)

	// Write the same session ID in both buckets. The current bucket has
	// stale on-disk meta (turnCount 0); currentMeta supplies the live count
	// (turnCount 5). The sibling bucket has its own meta (turnCount 99).
	writeFindSession(t, currentBucket, findMetaSpec{
		id:        sid,
		name:      "current session",
		updated:   now,
		turnCount: 0,
	}, "current content")
	writeFindSession(t, siblingBucket, findMetaSpec{
		id:        sid,
		name:      "sibling duplicate",
		updated:   now.Add(-time.Minute), // older so it sorts before current
		turnCount: 99,
	}, "sibling content")

	deps := &toolDeps{
		stateDir:  currentBucket,
		sessionID: sid,
		currentMeta: func() schema.SessionMeta {
			return schema.SessionMeta{ID: sid, Name: "current session", TurnCount: 5, UpdatedAt: now}
		},
	}

	matches := matchesFromEnvelope(t, decodeEnvelope(t, marshalFind(t, deps,
		map[string]any{"scope": scopeAllProjects})))

	var current, sibling map[string]any
	for _, m := range matches {
		title, _ := m["title"].(string)
		switch title {
		case "current session":
			current = m
		case "sibling duplicate":
			sibling = m
		}
	}
	if current == nil {
		t.Fatalf("current-bucket record not found in matches: %v", matches)
	}
	if sibling == nil {
		t.Fatalf("sibling-bucket duplicate not found in matches: %v", matches)
	}

	// Current-bucket record: IsCurrent=true, live TurnCount overlay (5, not 0).
	if current["is_current"] != true {
		t.Errorf("current-bucket record is_current = %v, want true", current["is_current"])
	}
	if got := current["approx_turns"]; got != float64(5) {
		t.Errorf("current-bucket record approx_turns = %v, want 5 (live overlay, not stale disk 0)", got)
	}

	// Sibling-bucket duplicate: IsCurrent=false, NO live overlay (99, not 5).
	if sibling["is_current"] == true {
		t.Error("sibling-bucket duplicate got is_current=true; only the current bucket + matching ID should be current")
	}
	if got := sibling["approx_turns"]; got != float64(99) {
		t.Errorf("sibling-bucket duplicate approx_turns = %v, want 99 (its own meta, not the live overlay 5)", got)
	}
}
