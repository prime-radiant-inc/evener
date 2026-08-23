package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunBadFlag covers the flag-parse error path.
func TestRunBadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-unknown"}, testNow, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

// TestRunPositionalArgs covers the positional-args rejection path.
func TestRunPositionalArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"extra"}, testNow, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments are not accepted") {
		t.Fatalf("stderr missing positional args rejection: %s", stderr.String())
	}
}

// TestRunEmptyRoot covers the empty-root validation path.
func TestRunEmptyRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, testNow, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--root is required") {
		t.Fatalf("stderr missing root required: %s", stderr.String())
	}
}

// TestRunNonPositiveSince covers the negative-since validation path.
func TestRunNonPositiveSince(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", "/tmp", "-since", "0s"}, testNow, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--since must be positive") {
		t.Fatalf("stderr missing since positive: %s", stderr.String())
	}
}

// TestRunNonexistentRoot covers the upgradeRoot error path.
func TestRunNonexistentRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", filepath.Join(t.TempDir(), "nonexistent")}, testNow, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "discover transcripts") {
		t.Fatalf("stderr missing discover error: %s", stderr.String())
	}
}

// TestRunEmptyRootDir covers the path where root exists but has no transcripts.
func TestRunEmptyRootDir(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root}, testNow, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "candidates=0") {
		t.Fatalf("stdout missing candidates=0: %s", stdout.String())
	}
}

// TestInspectTranscriptHeaderMissingFile covers the open error path.
func TestInspectTranscriptHeaderMissingFile(t *testing.T) {
	_, _, err := inspectTranscriptHeader(filepath.Join(t.TempDir(), "nonexistent.jsonl"))
	if err == nil {
		t.Fatalf("inspectTranscriptHeader on missing file should error")
	}
}

// TestInspectTranscriptHeaderEmptyFile covers the empty-transcript path.
func TestInspectTranscriptHeaderEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := inspectTranscriptHeader(path)
	if err == nil || !strings.Contains(err.Error(), "empty transcript") {
		t.Fatalf("inspectTranscriptHeader on empty file err = %v, want empty transcript", err)
	}
}

// TestInspectTranscriptHeaderIncomplete covers the incomplete-header path.
func TestInspectTranscriptHeaderIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incomplete.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := inspectTranscriptHeader(path)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("inspectTranscriptHeader on incomplete err = %v, want incomplete", err)
	}
}

// TestInspectTranscriptHeaderBadJSON covers the decode-error path.
func TestInspectTranscriptHeaderBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "badjson.jsonl")
	if err := os.WriteFile(path, []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := inspectTranscriptHeader(path)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("inspectTranscriptHeader on bad JSON err = %v, want decode error", err)
	}
}

// TestInspectTranscriptHeaderWrongKind covers the wrong-kind path.
func TestInspectTranscriptHeaderWrongKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrongkind.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"entry","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := inspectTranscriptHeader(path)
	if err == nil || !strings.Contains(err.Error(), "not a transcript header") {
		t.Fatalf("inspectTranscriptHeader wrong kind err = %v", err)
	}
}

// TestPrepareTranscriptMissingFile covers the open error path.
func TestPrepareTranscriptMissingFile(t *testing.T) {
	_, err := prepareTranscript(filepath.Join(t.TempDir(), "nonexistent.jsonl"))
	if err == nil {
		t.Fatalf("prepareTranscript on missing file should error")
	}
}

// TestDiscoverTranscriptsMissingRoot covers the walk-error path.
func TestDiscoverTranscriptsMissingRoot(t *testing.T) {
	_, err := discoverTranscripts(filepath.Join(t.TempDir(), "nonexistent"))
	if err == nil {
		t.Fatalf("discoverTranscripts on missing root should error")
	}
}

// TestDiscoverTranscriptsWithFiles covers the happy path.
func TestDiscoverTranscriptsWithFiles(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "proj", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "a.transcript.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := discoverTranscripts(root)
	if err != nil {
		t.Fatalf("discoverTranscripts: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("paths = %v, want 1", paths)
	}
}
