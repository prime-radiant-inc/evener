package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/transcript"
)

var testNow = time.Date(2026, 7, 17, 18, 0, 0, 0, time.UTC)

func TestRunDryRunLeavesEligibleV1TranscriptUntouched(t *testing.T) {
	root := t.TempDir()
	body := validV1Transcript()
	path := writeTranscriptFixture(t, root, "dry-run", body, testNow.Add(-time.Hour))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--root", root, "--since", "120h"}, testNow, &stdout, &stderr); code != 0 {
		t.Fatalf("run code = %d, want 0; stderr = %q", code, stderr.String())
	}

	assertFileBytes(t, path, body)
	assertPathMissing(t, path+".v1.bak")
	if got := stdout.String(); !strings.Contains(got, "eligible=1") || !strings.Contains(got, "upgraded=0") {
		t.Fatalf("stdout = %q, want eligible=1 and upgraded=0", got)
	}
}

func TestRunApplyConvertsEntriesAndRetainsBackup(t *testing.T) {
	root := t.TempDir()
	body := validV1Transcript()
	path := writeTranscriptFixture(t, root, "apply", body, testNow.Add(-time.Hour))
	apiPath := strings.TrimSuffix(path, ".transcript.jsonl") + ".api.jsonl"
	apiBody := []byte("private api log\n")
	if err := os.WriteFile(apiPath, apiBody, 0o600); err != nil {
		t.Fatalf("write API log: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--root", root, "--since", "120h", "--apply"}, testNow, &stdout, &stderr); code != 0 {
		t.Fatalf("run code = %d, want 0; stderr = %q", code, stderr.String())
	}

	assertFileBytes(t, path+".v1.bak", body)
	assertFileBytes(t, apiPath, apiBody)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat replacement: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("replacement mode = %o, want 600", got)
	}

	lines := readCompleteLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("replacement lines = %d, want header plus two entries", len(lines))
	}
	header, err := transcript.DecodeHeader(lines[0])
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if header.FormatVersion != transcript.FormatVersion || header.SessionID != "session-apply" {
		t.Fatalf("header = version %d session %q, want version %d session-apply", header.FormatVersion, header.SessionID, transcript.FormatVersion)
	}
	first, err := transcript.DecodeEntry(lines[1])
	if err != nil {
		t.Fatalf("DecodeEntry first: %v", err)
	}
	second, err := transcript.DecodeEntry(lines[2])
	if err != nil {
		t.Fatalf("DecodeEntry second: %v", err)
	}
	if first.Seq != 0 || second.Seq != 1 {
		t.Fatalf("entry sequences = %d, %d; want 0, 1", first.Seq, second.Seq)
	}
	if got := stdout.String(); !strings.Contains(got, "upgraded=1") || !strings.Contains(got, "removed_api_calls=1") {
		t.Fatalf("stdout = %q, want upgraded=1 and removed_api_calls=1", got)
	}
}

func TestRunSkipsCurrentAndOldTranscripts(t *testing.T) {
	root := t.TempDir()
	current := bytes.Replace(validV1Transcript(), []byte(`"format_version":1`), []byte(`"format_version":2`), 1)
	currentPath := writeTranscriptFixture(t, root, "current", current, testNow.Add(-time.Hour))
	old := validV1Transcript()
	oldPath := writeTranscriptFixture(t, root, "old", old, testNow.Add(-121*time.Hour))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--root", root, "--since", "120h", "--apply"}, testNow, &stdout, &stderr); code != 0 {
		t.Fatalf("run code = %d, want 0; stderr = %q", code, stderr.String())
	}

	assertFileBytes(t, currentPath, current)
	assertFileBytes(t, oldPath, old)
	assertPathMissing(t, currentPath+".v1.bak")
	assertPathMissing(t, oldPath+".v1.bak")
	if got := stdout.String(); !strings.Contains(got, "skipped_current=1") || !strings.Contains(got, "skipped_old=1") {
		t.Fatalf("stdout = %q, want current and old skip counts", got)
	}
}

func TestRunRejectsMalformedUnknownAndIncompleteRecords(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "malformed header", body: []byte("{not-json}\n")},
		{name: "unknown record", body: []byte(`{"kind":"header","format_version":1,"session_id":"session-unknown"}` + "\n" + `{"kind":"mystery"}` + "\n")},
		{name: "incomplete final record", body: []byte(`{"kind":"header","format_version":1,"session_id":"session-incomplete"}` + "\n" + `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}}`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := writeTranscriptFixture(t, root, "invalid", tc.body, testNow.Add(-time.Hour))

			var stdout, stderr bytes.Buffer
			if code := run([]string{"--root", root, "--since", "120h", "--apply"}, testNow, &stdout, &stderr); code != 1 {
				t.Fatalf("run code = %d, want 1; stdout = %q stderr = %q", code, stdout.String(), stderr.String())
			}

			assertFileBytes(t, path, tc.body)
			assertPathMissing(t, path+".v1.bak")
			if !strings.Contains(stderr.String(), path) {
				t.Fatalf("stderr = %q, want transcript path %q", stderr.String(), path)
			}
		})
	}
}

func TestRunRejectsExistingBackup(t *testing.T) {
	root := t.TempDir()
	body := validV1Transcript()
	path := writeTranscriptFixture(t, root, "backup", body, testNow.Add(-time.Hour))
	backupBody := []byte("existing backup\n")
	if err := os.WriteFile(path+".v1.bak", backupBody, 0o600); err != nil {
		t.Fatalf("write existing backup: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--root", root, "--since", "120h", "--apply"}, testNow, &stdout, &stderr); code != 1 {
		t.Fatalf("run code = %d, want 1; stdout = %q stderr = %q", code, stdout.String(), stderr.String())
	}

	assertFileBytes(t, path, body)
	assertFileBytes(t, path+".v1.bak", backupBody)
}

func TestReplaceTranscriptRejectsChangedOriginal(t *testing.T) {
	root := t.TempDir()
	body := validV1Transcript()
	path := writeTranscriptFixture(t, root, "changed", body, testNow.Add(-time.Hour))
	prepared, err := prepareTranscript(path)
	if err != nil {
		t.Fatalf("prepareTranscript: %v", err)
	}
	changed := append(append([]byte(nil), body...), []byte("changed\n")...)
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatalf("change original: %v", err)
	}

	if err := replaceTranscript(prepared); err == nil {
		t.Fatal("replaceTranscript changed original error = nil, want error")
	}
	assertFileBytes(t, path, changed)
	assertPathMissing(t, path+".v1.bak")
}

func validV1Transcript() []byte {
	return []byte(strings.Join([]string{
		`{"kind":"header","format_version":1,"session_id":"session-apply","profile_id":"test","model":"test"}`,
		`{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}}`,
		`{"kind":"api_call","seq":1,"provider":"test","request":{"private":true}}`,
		`{"kind":"entry","seq":2,"turn":{"kind":"ASSISTANT"}}`,
		"",
	}, "\n"))
}

func writeTranscriptFixture(t *testing.T, root, id string, body []byte, modTime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, "project", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, id+".transcript.jsonl")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set transcript mtime: %v", err)
	}
	return path
}

func readCompleteLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("%s is not newline terminated", path)
	}
	parts := bytes.Split(data[:len(data)-1], []byte{'\n'})
	return parts
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s bytes changed\ngot:  %q\nwant: %q", path, got, want)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %s error = %v, want not exist", path, err)
	}
}
