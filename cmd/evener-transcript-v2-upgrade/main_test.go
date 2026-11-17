package transcriptv2upgrade

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/transcript"
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

// TestUpgradeReadsTranscriptsWithFewReads pins both passes' read granularity:
// one underlying read per 64 KiB, not one per bufio's 4 KiB default.
// Not parallel: overrides the transcript open seam.
func TestUpgradeReadsTranscriptsWithFewReads(t *testing.T) {
	root := t.TempDir()
	body := largeV1Transcript()
	path := writeTranscriptFixture(t, root, "large", body, testNow.Add(-time.Hour))
	headerBytes := bytes.IndexByte(body, '\n') + 1
	// Four 64 KiB fills is the floor at which a 4 KiB reader pays dozens of
	// reads where a 64 KiB one pays four, so the bound below separates them.
	if headerBytes < 4*64*1024 {
		t.Fatalf("fixture header is %d bytes; it must span at least four 64 KiB fills for the read bound to separate the two buffer sizes", headerBytes)
	}
	// The slack covers the EOF probe that ends a pass plus at most one fill
	// stranded by a line boundary. A 4 KiB reader pays over a hundred reads
	// on this fixture, far past the bound.
	readBound := (len(body)+64*1024-1)/(64*1024) + 2

	t.Run("inspect header", func(t *testing.T) {
		var reads int
		countTranscriptReads(t, &reads)
		version, _, err := inspectTranscriptHeader(path)
		if err != nil {
			t.Fatalf("inspectTranscriptHeader: %v", err)
		}
		if version != legacyFormatVersion {
			t.Fatalf("version = %d, want %d", version, legacyFormatVersion)
		}
		if reads == 0 {
			t.Fatal("inspectTranscriptHeader bypassed the transcript open seam, so its read count proves nothing")
		}
		if reads > readBound {
			t.Fatalf("inspectTranscriptHeader read a %d byte header with %d underlying reads; a 64 KiB buffer stays at or below %d", headerBytes, reads, readBound)
		}
	})

	t.Run("prepare transcript", func(t *testing.T) {
		var reads int
		countTranscriptReads(t, &reads)
		prepared, err := prepareTranscript(path)
		if err != nil {
			t.Fatalf("prepareTranscript: %v", err)
		}
		if got, want := bytes.Count(prepared.data, []byte{'\n'}), bytes.Count(body, []byte{'\n'}); got != want {
			t.Fatalf("converted %d records, want all %d", got, want)
		}
		if reads == 0 {
			t.Fatal("prepareTranscript bypassed the transcript open seam, so its read count proves nothing")
		}
		if reads > readBound {
			t.Fatalf("prepareTranscript read a %d byte transcript with %d underlying reads; a 64 KiB buffer stays at or below %d", len(body), reads, readBound)
		}
	})
}

// countTranscriptReads swaps the transcript open seam for one that counts the
// underlying reads a pass issues.
func countTranscriptReads(t *testing.T, reads *int) {
	t.Helper()
	origOpen := openTranscriptFile
	openTranscriptFile = func(path string) (transcriptFile, error) {
		file, err := origOpen(path)
		if err != nil {
			return nil, err
		}
		return &countingTranscriptFile{transcriptFile: file, reads: reads}, nil
	}
	t.Cleanup(func() { openTranscriptFile = origOpen })
}

// countingTranscriptFile counts the underlying reads a pass issues, so a test
// can pin a buffer's read granularity without timing anything.
type countingTranscriptFile struct {
	transcriptFile
	reads *int
}

func (c *countingTranscriptFile) Read(p []byte) (int, error) {
	*c.reads++
	return c.transcriptFile.Read(p)
}

// largeV1Transcript returns a v1 transcript of the shape the read-count test
// depends on: a header far larger than a 64 KiB buffer, so the header pass
// crosses many fills, and entries far smaller than a buffer, so every fill
// after the header is full — a partial fill per line would multiply the read
// count the bound derives. Only fields Header, Entry and schema.Turn declare
// appear, because every decode is strict.
func largeV1Transcript() []byte {
	const entryCount = 16
	lines := make([]string, 0, entryCount+1)
	lines = append(lines, fmt.Sprintf(`{"kind":"header","format_version":1,"session_id":"session-large","profile_id":"test","model":"test","system_prompt":%q}`,
		strings.Repeat("s", 512*1024)))
	for i := range entryCount {
		lines = append(lines, fmt.Sprintf(`{"kind":"entry","seq":%d,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":%q}]}}}`,
			i, strings.Repeat("x", 1024)))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
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
