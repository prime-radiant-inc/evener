package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sidA  = "02wMz5TxvEMoJEDTDGOTil"
	sidB  = "02wMz5TxvEMoJEDTDGOTim"
	hash1 = "Project-one-0123456789"
	hash2 = "Project-two-0123456789"
)

// writeSession lays out a session under bucketDir: the flat transcript + meta
// files and the per-session jobs.jsonl SUBDIR.
func writeSession(t *testing.T, bucketDir, sid string) {
	t.Helper()
	sess := filepath.Join(bucketDir, "sessions")
	writeFile(t, filepath.Join(sess, sid+".transcript.jsonl"),
		`{"kind":"header","session_id":"`+sid+`"}`+"\n")
	writeFile(t, filepath.Join(sess, sid+".meta.json"), `{"id":"`+sid+`"}`)
	writeFile(t, filepath.Join(sess, sid, "jobs.jsonl"), "")
}

// stateHomeBucket returns the bucket dir for a hash under an XDG state home.
func stateHomeBucket(base, hash string) string {
	return filepath.Join(base, "evener", "projects", hash)
}

func TestLocate_StateHomeLayout_BareID(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)

	got, err := Locate(base, sidA)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	wantTranscript := filepath.Join(bucket, "sessions", sidA+".transcript.jsonl")
	wantAPILog := filepath.Join(bucket, "sessions", sidA+".api.jsonl")
	wantMeta := filepath.Join(bucket, "sessions", sidA+".meta.json")
	wantJobs := filepath.Join(bucket, "sessions", sidA, "jobs.jsonl")
	if got.TranscriptPath != wantTranscript {
		t.Errorf("TranscriptPath = %q, want %q", got.TranscriptPath, wantTranscript)
	}
	if got.APILogPath != wantAPILog {
		t.Errorf("APILogPath = %q, want %q", got.APILogPath, wantAPILog)
	}
	if got.MetaPath != wantMeta {
		t.Errorf("MetaPath = %q, want %q", got.MetaPath, wantMeta)
	}
	if got.JobsPath != wantJobs {
		t.Errorf("JobsPath = %q, want %q", got.JobsPath, wantJobs)
	}
	if got.ProjectID != hash1 {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, hash1)
	}
	if got.TranscriptRef != "proj:"+hash1+":"+sidA {
		t.Errorf("TranscriptRef = %q, want proj:%s:%s", got.TranscriptRef, hash1, sidA)
	}
}

// The client-mutation store is the artifact that proves whether a user's input
// reached the daemon, so locate must name it. It sits in a bucket-level
// mutations/ dir — a sibling of sessions/, not a file under it.
func TestLocate_MutationsPathIsBucketLevel(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)

	got, err := Locate(base, sidA)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	want := filepath.Join(bucket, "mutations", sidA+".json")
	if got.MutationsPath != want {
		t.Errorf("MutationsPath = %q, want %q", got.MutationsPath, want)
	}
}

// The jobs.jsonl SUBDIR form is the load-bearing §8 correction: it must never be
// built by suffixing ".jobs.jsonl" onto the transcript path.
func TestLocate_JobsPathIsSubdirNotSuffix(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)

	got, err := Locate(base, sidA)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if strings.HasSuffix(got.JobsPath, sidA+".jobs.jsonl") {
		t.Fatalf("JobsPath %q used the wrong (suffix) form; want sessions/<sid>/jobs.jsonl subdir", got.JobsPath)
	}
	if filepath.Base(filepath.Dir(got.JobsPath)) != sidA {
		t.Fatalf("JobsPath %q parent dir is not the per-session subdir <sid>/", got.JobsPath)
	}
}

func TestLocate_OverrideLayout_BareID(t *testing.T) {
	base := t.TempDir() // base IS the bucket (no evener/projects under it)
	writeSession(t, base, sidA)

	got, err := Locate(base, sidA)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	wantJobs := filepath.Join(base, "sessions", sidA, "jobs.jsonl")
	if got.JobsPath != wantJobs {
		t.Errorf("JobsPath = %q, want %q", got.JobsPath, wantJobs)
	}
	if got.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty in override layout", got.ProjectID)
	}
	if got.TranscriptRef != "local:"+sidA {
		t.Errorf("TranscriptRef = %q, want local:%s", got.TranscriptRef, sidA)
	}
}

func TestLocate_ProjRef(t *testing.T) {
	base := t.TempDir()
	writeSession(t, stateHomeBucket(base, hash1), sidA)

	got, err := Locate(base, "proj:"+hash1+":"+sidA)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got.ProjectID != hash1 || got.SessionID != sidA {
		t.Errorf("got projectID=%q sid=%q, want %q/%q", got.ProjectID, got.SessionID, hash1, sidA)
	}
}

func TestLocate_LocalRef(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)

	got, err := Locate(base, "local:"+sidA)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got.SessionID != sidA {
		t.Errorf("SessionID = %q, want %q", got.SessionID, sidA)
	}
	// local:<sid> resolves by searching buckets; the session lives in a hashed
	// bucket so TranscriptRef must be the proj: form, not local:.
	if got.TranscriptRef != "proj:"+hash1+":"+sidA {
		t.Errorf("TranscriptRef = %q, want proj:%s:%s", got.TranscriptRef, hash1, sidA)
	}
	if got.ProjectID != hash1 {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, hash1)
	}
	wantJobs := filepath.Join(bucket, "sessions", sidA, "jobs.jsonl")
	if got.JobsPath != wantJobs {
		t.Errorf("JobsPath = %q, want %q", got.JobsPath, wantJobs)
	}
}

func TestLocate_AmbiguousBareID(t *testing.T) {
	base := t.TempDir()
	writeSession(t, stateHomeBucket(base, hash1), sidA)
	writeSession(t, stateHomeBucket(base, hash2), sidA) // same sid, two buckets

	_, err := Locate(base, sidA)
	if err == nil {
		t.Fatal("want ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), hash1) || !strings.Contains(err.Error(), hash2) {
		t.Errorf("ambiguity error should list both candidate buckets, got: %v", err)
	}
}

// A proj:<project-id>: ref disambiguates a sid that appears in multiple buckets.
func TestLocate_ProjRefDisambiguates(t *testing.T) {
	base := t.TempDir()
	writeSession(t, stateHomeBucket(base, hash1), sidA)
	writeSession(t, stateHomeBucket(base, hash2), sidA)

	got, err := Locate(base, "proj:"+hash2+":"+sidA)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got.ProjectID != hash2 {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, hash2)
	}
}

func TestLocate_NotFound(t *testing.T) {
	base := t.TempDir()
	writeSession(t, stateHomeBucket(base, hash1), sidA)

	_, err := Locate(base, sidB)
	if err == nil {
		t.Fatal("want not-found error, got nil")
	}
}

func TestLocate_RejectTraversal(t *testing.T) {
	base := t.TempDir()
	for _, bad := range []string{"../etc", "..", "a/b", "local:../x", "proj:../h:" + sidA, "proj:" + hash1 + ":../x"} {
		if _, err := Locate(base, bad); err == nil {
			t.Errorf("Locate(%q) = nil error, want rejection", bad)
		}
	}
}

func TestLocate_EmptySelector(t *testing.T) {
	base := t.TempDir()
	if _, err := Locate(base, ""); err == nil {
		t.Error("empty selector should error (no current session for a standalone tool)")
	}
	if _, err := Locate(base, "current"); err == nil {
		t.Error("'current' selector should error for a standalone tool")
	}
}

// TestLocate_ProjRefsResolveWithoutMutatingState is the clean-break contract
// after the legacy-addressability fix: buckets with legacy (non-Project.ID)
// names and well-formed names are BOTH addressable by explicit proj: refs,
// and resolving either never mutates the on-disk state it reads — the
// doctor stays a read-only forensics tool even over legacy layouts.
func TestLocate_ProjRefsResolveWithoutMutatingState(t *testing.T) {
	base := t.TempDir()
	legacyBucket := stateHomeBucket(base, "0123456789abcdef")
	newBucket := stateHomeBucket(base, "project-new-0123456789")
	writeSession(t, legacyBucket, sidA)
	writeSession(t, newBucket, sidB)
	legacyPath := filepath.Join(legacyBucket, "sessions", sidA+".transcript.jsonl")
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Locate(base, "proj:0123456789abcdef:"+sidA)
	if err != nil {
		t.Fatalf("legacy project ref must resolve: %v", err)
	}
	if got.ProjectID != "0123456789abcdef" || got.SessionID != sidA {
		t.Fatalf("legacy ref resolved project/session = %q/%q", got.ProjectID, got.SessionID)
	}
	got, err = Locate(base, "proj:project-new-0123456789:"+sidB)
	if err != nil {
		t.Fatalf("new project/session did not resolve: %v", err)
	}
	if got.ProjectID != "project-new-0123456789" || got.SessionID != sidB {
		t.Fatalf("resolved project/session = %q/%q", got.ProjectID, got.SessionID)
	}

	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatal("legacy doctor fixture changed")
	}
}

// TestLocate_LegacyBucketProjRefResolves is the reviewer's test: a bucket
// directory whose name predates (or simply ignores) identifier.Project's
// well-formedness is still enumerated by the sweep, so it must also be
// addressable by an explicit proj: ref. Findable by bare id but not
// nameable by ref would leave a located session impossible to follow up.
func TestLocate_LegacyBucketProjRefResolves(t *testing.T) {
	base := t.TempDir()
	legacyBucket := stateHomeBucket(base, "0123456789abcdef")
	writeSession(t, legacyBucket, sidA)

	got, err := Locate(base, "proj:0123456789abcdef:"+sidA)
	if err != nil {
		t.Fatalf("Locate legacy-named bucket by explicit ref: %v", err)
	}
	if got.ProjectID != "0123456789abcdef" || got.SessionID != sidA {
		t.Errorf("resolved project/session = %q/%q, want 0123456789abcdef/%s", got.ProjectID, got.SessionID, sidA)
	}
	if want := filepath.Join(legacyBucket, "sessions", sidA+".transcript.jsonl"); got.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want %q", got.TranscriptPath, want)
	}
}

// TestLocate_ProjRefTraversalTokensRejected pins that the relaxed project-id
// policy refuses exactly the traversal shapes: path separators, the dot
// components, empty, and NUL. Everything else is a directory name the sweep
// may legitimately enumerate.
func TestLocate_ProjRefTraversalTokensRejected(t *testing.T) {
	base := t.TempDir()
	writeSession(t, stateHomeBucket(base, hash1), sidA)
	for _, bad := range []string{
		"proj:../h:" + sidA,
		"proj:..:" + sidA,
		"proj:.:" + sidA,
		"proj:a/b:" + sidA,
		"proj:a\\b:" + sidA,
		"proj:a\x00b:" + sidA,
		"proj::" + sidA,
	} {
		if _, err := Locate(base, bad); err == nil {
			t.Errorf("Locate(%q) = nil error, want traversal rejection", bad)
		}
	}
}

// TestLocate_NonConsumableBucketOmitsRefButSelectorAddresses proves the
// emission contract for buckets whose directory names the shared agent ref
// grammar rejects: locate emits NO transcript ref (never a handle the
// agent-side transcript tools would refuse), while the session stays fully
// addressable through the doctor's own selector grammar — a bare id sweeps
// to it and the explicit proj: ref re-locates. Follow-up reads for such
// buckets go through the doctor's transcript command, not read_transcript.
func TestLocate_NonConsumableBucketOmitsRefButSelectorAddresses(t *testing.T) {
	base := t.TempDir()
	legacyBucket := stateHomeBucket(base, "0123456789abcdef")
	writeSession(t, legacyBucket, sidA)

	first, err := Locate(base, sidA)
	if err != nil {
		t.Fatalf("bare-id Locate into legacy bucket: %v", err)
	}
	if first.TranscriptRef != "" {
		t.Fatalf("TranscriptRef = %q, want empty — a legacy-named bucket is not consumable by the agent ref grammar", first.TranscriptRef)
	}
	if first.ProjectID != "0123456789abcdef" {
		t.Fatalf("ProjectID = %q, want the bucket name", first.ProjectID)
	}
	second, err := Locate(base, "proj:0123456789abcdef:"+sidA)
	if err != nil {
		t.Fatalf("explicit legacy proj ref must still address the bucket: %v", err)
	}
	if second.TranscriptPath != first.TranscriptPath || second.ProjectID != first.ProjectID {
		t.Errorf("round trip moved: first=%+v second=%+v", first, second)
	}
}

// TestLocate_ColonNamedBucketOmitsRefButSelectorAddresses is the colon name
// class of the emission contract: no ref is emitted (validIDToken rejects
// colons), and the doctor's own last-colon selector grammar still addresses
// the bucket — the round-2 reviewer's addressability, kept honest at the
// emission site.
func TestLocate_ColonNamedBucketOmitsRefButSelectorAddresses(t *testing.T) {
	base := t.TempDir()
	colonBucket := stateHomeBucket(base, "a:b")
	writeSession(t, colonBucket, sidA)

	first, err := Locate(base, sidA)
	if err != nil {
		t.Fatalf("bare-id Locate into colon-named bucket: %v", err)
	}
	if first.TranscriptRef != "" {
		t.Fatalf("TranscriptRef = %q, want empty — colons are not consumable by the agent ref grammar", first.TranscriptRef)
	}
	second, err := Locate(base, "proj:a:b:"+sidA)
	if err != nil {
		t.Fatalf("explicit colon proj ref must still address the bucket: %v", err)
	}
	if second.TranscriptPath != first.TranscriptPath || second.ProjectID != first.ProjectID {
		t.Errorf("round trip moved: first=%+v second=%+v", first, second)
	}
}
