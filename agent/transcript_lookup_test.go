package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
