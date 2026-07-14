package doctor

import (
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
	return filepath.Join(base, "serf", "projects", hash)
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
	wantMeta := filepath.Join(bucket, "sessions", sidA+".meta.json")
	wantJobs := filepath.Join(bucket, "sessions", sidA, "jobs.jsonl")
	if got.TranscriptPath != wantTranscript {
		t.Errorf("TranscriptPath = %q, want %q", got.TranscriptPath, wantTranscript)
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
	base := t.TempDir() // base IS the bucket (no serf/projects under it)
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

// A proj:<hash>: ref disambiguates a sid that appears in multiple buckets.
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

func TestLocate_CleanBreakSkipsLegacyProjectAndSession(t *testing.T) {
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

	if _, err := Locate(base, "proj:0123456789abcdef:"+sidA); err == nil {
		t.Fatal("legacy project ref unexpectedly resolved")
	}
	got, err := Locate(base, "proj:project-new-0123456789:"+sidB)
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
	if string(after) != string(before) || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatal("legacy doctor fixture changed")
	}
}
