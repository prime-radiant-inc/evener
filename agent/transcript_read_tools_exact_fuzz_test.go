//go:build serffuzz

package agent

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
)

func FuzzTranscriptReadToolsExactCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		trteReadFailures(t)
		trteJobRequestFailures(t)
	})
}

func trteReadFailures(t *testing.T) {
	t.Helper()
	want := errors.New("injected transcript read failure")
	oldOpen := openTranscriptFile
	t.Cleanup(func() { openTranscriptFile = oldOpen })

	for _, tc := range []struct {
		name string
		body string
		read func(string) error
	}{
		{name: "lenient-header", read: func(path string) error { _, _, _, err := readTranscript(path); return err }},
		{name: "lenient-body", body: `{"kind":"header"}` + "\n", read: func(path string) error { _, _, _, err := readTranscript(path); return err }},
		{name: "full-header", read: func(path string) error { _, err := readTranscriptFull(path); return err }},
		{name: "full-body", body: `{"kind":"header"}` + "\n", read: func(path string) error { _, err := readTranscriptFull(path); return err }},
		{name: "strict-header", read: func(path string) error { _, err := readStrictChildTranscript(path, "child", 64); return err }},
		{name: "strict-body", body: `{"kind":"header","session_id":"child"}` + "\n", read: func(path string) error { _, err := readStrictChildTranscript(path, "child", 64); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			openTranscriptFile = func(string) (io.ReadCloser, error) {
				return io.NopCloser(&trteFailReader{prefix: []byte(tc.body), err: want}), nil
			}
			if err := tc.read("ignored"); !errors.Is(err, want) {
				t.Fatalf("error = %v, want injected failure", err)
			}
		})
	}
	openTranscriptFile = oldOpen

	root := t.TempDir()
	path := filepath.Join(root, "window.jsonl")
	body := `{"kind":"header","session_id":"child"}` + "\n" +
		`{"kind":"entry","turn":{"kind":"USER_INPUT"}}` + "\n" +
		`{"kind":"entry","turn":{"kind":"ASSISTANT"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if _, err := readMarkdown(path, "local:child", schema.SessionMeta{ID: "child"}, "0-0", nil); err != nil {
		t.Fatalf("read partial markdown: %v", err)
	}

	oldRawLines := readRawLinesForRange
	readRawLinesForRange = func(string, int, int) (string, int, int, bool, error) {
		return "", 0, 0, false, want
	}
	if _, err := readRaw(path, "local:child", ""); !errors.Is(err, want) {
		t.Fatalf("raw second-pass error = %v, want injected failure", err)
	}
	readRawLinesForRange = oldRawLines
}

type trteFailReader struct {
	prefix []byte
	err    error
}

func (r *trteFailReader) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, r.err
}

func trteJobRequestFailures(t *testing.T) {
	t.Helper()
	jm, err := newJobManagerNoSync(t.TempDir(), "session", nil)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(func() { _ = jm.close() })
	deps := &toolDeps{jobManager: jm}

	for _, tc := range []struct {
		deps              *toolDeps
		ref, format, want string
	}{
		{ref: "job:x", format: "", want: "job manager is not available"},
		{deps: deps, ref: "job:x", format: formatJSONL, want: "not supported"},
		{deps: deps, ref: "job: ", format: formatMarkdown, want: "must be job:<job_id>"},
		{deps: deps, ref: "job:missing", format: "", want: "not found"},
	} {
		_, err := readJobTranscript(tc.deps, tc.ref, "", tc.format)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("readJobTranscript(%q, %q) error = %v, want %q", tc.ref, tc.format, err, tc.want)
		}
	}

	now := time.Unix(1, 0).UTC()
	if err := jm.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: "no-output",
		Type: jobstore.JobShell, OwnerSessionID: "session", VisibleToSession: "session",
		StartedAt: &now,
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if _, err := readJobTranscript(deps, "job:no-output", "", formatMarkdown); err == nil {
		t.Fatal("readJobTranscript accepted a job with no output file")
	}
}
