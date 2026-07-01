package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s4covWriteFile writes content to a fresh temp file and returns its path.
func s4covWriteFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp transcript: %v", err)
	}
	return path
}

// --- readTranscript ---

func TestS4covReadTranscript_HappyPath(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
{"kind":"entry","seq":1,"turn":{}}
{"kind":"entry","seq":2,"turn":{}}
{"kind":"api_call","seq":3}
{"kind":"entry","seq":4,"turn":{}}
`
	path := s4covWriteFile(t, content)
	header, entries, skipped, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if header.SessionID != "01SESS" {
		t.Fatalf("header session = %q, want 01SESS", header.SessionID)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (api_call skipped, not counted)", len(entries))
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
}

func TestS4covReadTranscript_CorruptLineCounted(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
{"kind":"entry","seq":1,"turn":{}}
{this is not valid json
{"kind":"entry","seq":2,"turn":{}}
`
	path := s4covWriteFile(t, content)
	_, entries, skipped, err := readTranscript(path)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
}

func TestS4covReadTranscript_EmptyFile(t *testing.T) {
	t.Parallel()
	path := s4covWriteFile(t, "")
	_, _, _, err := readTranscript(path)
	if err == nil || !strings.Contains(err.Error(), "transcript file is empty: no header") {
		t.Fatalf("err = %v, want 'transcript file is empty: no header'", err)
	}
}

func TestS4covReadTranscript_BadHeader(t *testing.T) {
	t.Parallel()
	path := s4covWriteFile(t, "{not valid json}\n")
	_, _, _, err := readTranscript(path)
	if err == nil || !strings.Contains(err.Error(), "parsing transcript header") {
		t.Fatalf("err = %v, want 'parsing transcript header'", err)
	}
}

func TestS4covReadTranscript_OpenFail(t *testing.T) {
	t.Parallel()
	_, _, _, err := readTranscript(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err == nil || !strings.Contains(err.Error(), "open transcript") {
		t.Fatalf("err = %v, want 'open transcript'", err)
	}
}

// --- readTranscriptFull ---

func TestS4covReadTranscriptFull_Mixed(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
{"kind":"entry","seq":1,"turn":{}}
{"kind":"api_call","seq":2}
{"kind":"mystery","seq":3}
{not parseable json
{"kind":"entry","seq":"not-a-number"}
`
	path := s4covWriteFile(t, content)
	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	if len(data.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1", len(data.Entries))
	}
	if len(data.APICalls) != 1 {
		t.Fatalf("APICalls = %d, want 1", len(data.APICalls))
	}
	// Three lines are skipped (unknown-kind, unparseable, entry-with-bad-body).
	if data.Skipped != 3 {
		t.Fatalf("Skipped = %d, want 3", data.Skipped)
	}
}

func TestS4covReadTranscriptFull_EmptyFile(t *testing.T) {
	t.Parallel()
	path := s4covWriteFile(t, "")
	_, err := readTranscriptFull(path)
	if err == nil || !strings.Contains(err.Error(), "transcript file is empty: no header") {
		t.Fatalf("err = %v, want 'transcript file is empty: no header'", err)
	}
}

func TestS4covReadTranscriptFull_BadHeader(t *testing.T) {
	t.Parallel()
	path := s4covWriteFile(t, "{bad header}\n")
	_, err := readTranscriptFull(path)
	if err == nil || !strings.Contains(err.Error(), "parsing transcript header") {
		t.Fatalf("err = %v, want 'parsing transcript header'", err)
	}
}

func TestS4covReadTranscriptFull_OpenFail(t *testing.T) {
	t.Parallel()
	_, err := readTranscriptFull(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err == nil || !strings.Contains(err.Error(), "open transcript") {
		t.Fatalf("err = %v, want 'open transcript'", err)
	}
}

// --- readStrictChildTranscript / validateStrictChildTranscript ---

func TestS4covStrictChildTranscript_HappyPath(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
{"kind":"entry","seq":1,"turn":{}}
{"kind":"api_call","seq":2}
`
	path := s4covWriteFile(t, content)
	data, err := readStrictChildTranscript(path, "01SESS", 0)
	if err != nil {
		t.Fatalf("readStrictChildTranscript: %v", err)
	}
	if data.Header.SessionID != "01SESS" {
		t.Fatalf("header session = %q, want 01SESS", data.Header.SessionID)
	}
	if len(data.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1 (retained)", len(data.Entries))
	}
	if len(data.APICalls) != 1 {
		t.Fatalf("APICalls = %d, want 1 (retained)", len(data.APICalls))
	}
}

func TestS4covStrictChildTranscript_ValidateDoesNotRetain(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
{"kind":"entry","seq":1,"turn":{}}
{"kind":"api_call","seq":2}
`
	path := s4covWriteFile(t, content)
	header, err := validateStrictChildTranscript(path, "01SESS", 0)
	if err != nil {
		t.Fatalf("validateStrictChildTranscript: %v", err)
	}
	if header.SessionID != "01SESS" {
		t.Fatalf("header session = %q, want 01SESS", header.SessionID)
	}
}

func TestS4covStrictChildTranscript_EmptyFile(t *testing.T) {
	t.Parallel()
	path := s4covWriteFile(t, "")
	_, err := readStrictChildTranscript(path, "01SESS", 0)
	if !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("err = %v, want errStrictChildTranscriptCorrupt", err)
	}
	if !strings.Contains(err.Error(), "transcript file is empty") {
		t.Fatalf("err = %v, want 'transcript file is empty'", err)
	}
}

func TestS4covStrictChildTranscript_HeaderKindWrong(t *testing.T) {
	t.Parallel()
	content := `{"kind":"entry","session_id":"01SESS"}
`
	path := s4covWriteFile(t, content)
	_, err := readStrictChildTranscript(path, "01SESS", 0)
	if !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("err = %v, want errStrictChildTranscriptCorrupt", err)
	}
	if !strings.Contains(err.Error(), "transcript header kind") {
		t.Fatalf("err = %v, want 'transcript header kind'", err)
	}
}

func TestS4covStrictChildTranscript_BadHeaderJSON(t *testing.T) {
	t.Parallel()
	path := s4covWriteFile(t, "{bad header json\n")
	_, err := readStrictChildTranscript(path, "01SESS", 0)
	if !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("err = %v, want errStrictChildTranscriptCorrupt", err)
	}
	if !strings.Contains(err.Error(), "parsing transcript header") {
		t.Fatalf("err = %v, want 'parsing transcript header'", err)
	}
}

func TestS4covStrictChildTranscript_SessionMismatch(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
`
	path := s4covWriteFile(t, content)
	_, err := readStrictChildTranscript(path, "01OTHER", 0)
	if !errors.Is(err, errStrictChildTranscriptSessionMismatch) {
		t.Fatalf("err = %v, want errStrictChildTranscriptSessionMismatch", err)
	}
}

func TestS4covStrictChildTranscript_CorruptNonFinalLine(t *testing.T) {
	t.Parallel()
	// A corrupt line that is NOT the final line (a valid entry follows) must
	// abort the whole read as corrupt.
	content := `{"kind":"header","session_id":"01SESS"}
{this is broken json
{"kind":"entry","seq":1,"turn":{}}
`
	path := s4covWriteFile(t, content)
	_, err := readStrictChildTranscript(path, "01SESS", 0)
	if !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("err = %v, want errStrictChildTranscriptCorrupt", err)
	}
	if !strings.Contains(err.Error(), "parsing transcript line") {
		t.Fatalf("err = %v, want 'parsing transcript line'", err)
	}
}

func TestS4covStrictChildTranscript_UnknownKind(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
{"kind":"mystery","seq":1}
`
	path := s4covWriteFile(t, content)
	_, err := readStrictChildTranscript(path, "01SESS", 0)
	if !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("err = %v, want errStrictChildTranscriptCorrupt", err)
	}
	if !strings.Contains(err.Error(), "unknown transcript line kind") {
		t.Fatalf("err = %v, want 'unknown transcript line kind'", err)
	}
}

func TestS4covStrictChildTranscript_LineExceedsMaxBytes(t *testing.T) {
	t.Parallel()
	content := `{"kind":"header","session_id":"01SESS"}
`
	path := s4covWriteFile(t, content)
	// A tiny maxLineBytes makes even the header line exceed the cap.
	_, err := readStrictChildTranscript(path, "01SESS", 4)
	if !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("err = %v, want errStrictChildTranscriptCorrupt", err)
	}
	if !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("err = %v, want 'exceeds 4 bytes'", err)
	}
}

func TestS4covStrictChildTranscript_FinalIncompleteTolerated(t *testing.T) {
	t.Parallel()
	// A truncated final line (no trailing newline, unparseable) is tolerated:
	// it increments Skipped and the read returns cleanly.
	content := `{"kind":"header","session_id":"01SESS"}
{"kind":"entry","seq":1,"turn":{}}
{"kind":"entry","seq":2,"tur`
	path := s4covWriteFile(t, content)
	data, err := readStrictChildTranscript(path, "01SESS", 0)
	if err != nil {
		t.Fatalf("readStrictChildTranscript: %v", err)
	}
	if data.Header.SessionID != "01SESS" {
		t.Fatalf("header session = %q, want 01SESS", data.Header.SessionID)
	}
	if data.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", data.Skipped)
	}
	if len(data.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1 (the one complete entry)", len(data.Entries))
	}
}

func TestS4covStrictChildTranscript_OpenFail(t *testing.T) {
	t.Parallel()
	_, err := readStrictChildTranscript(filepath.Join(t.TempDir(), "missing.jsonl"), "01SESS", 0)
	if err == nil || !strings.Contains(err.Error(), "open transcript") {
		t.Fatalf("err = %v, want 'open transcript'", err)
	}
}

// --- parentBucketAndID ---

func TestS4covParentBucketAndID(t *testing.T) {
	t.Parallel()

	t.Run("Empty", func(t *testing.T) {
		t.Parallel()
		bucket, id, scope, err := parentBucketAndID("", "/state/dir", "01CUR")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if bucket != "/state/dir" || id != "01CUR" || scope != scopeCurrentProject {
			t.Fatalf("got (%q,%q,%q)", bucket, id, scope)
		}
	})

	t.Run("Current", func(t *testing.T) {
		t.Parallel()
		bucket, id, scope, err := parentBucketAndID("current", "/state/dir", "01CUR")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if bucket != "/state/dir" || id != "01CUR" || scope != scopeCurrentProject {
			t.Fatalf("got (%q,%q,%q)", bucket, id, scope)
		}
	})

	t.Run("LocalRef", func(t *testing.T) {
		t.Parallel()
		bucket, id, scope, err := parentBucketAndID("local:01ABC", "/state/dir", "01CUR")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if bucket != "/state/dir" || id != "01ABC" || scope != scopeCurrentProject {
			t.Fatalf("got (%q,%q,%q)", bucket, id, scope)
		}
	})

	t.Run("ProjRef", func(t *testing.T) {
		t.Parallel()
		sh := newStateHome(t)
		current := newBucketUnder(t, sh)
		bucket, id, scope, err := parentBucketAndID("proj:abcdef:01PROJ", current, "01CUR")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := filepath.Join(sh, "serf", "projects", "abcdef")
		if bucket != want {
			t.Fatalf("bucket = %q, want %q", bucket, want)
		}
		if id != "01PROJ" || scope != scopeAllProjects {
			t.Fatalf("id=%q scope=%q", id, scope)
		}
	})

	t.Run("ProjRefFlatStateDir", func(t *testing.T) {
		t.Parallel()
		flat := filepath.Join(t.TempDir(), "flat")
		if err := os.MkdirAll(flat, 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := parentBucketAndID("proj:abcdef:01PROJ", flat, "01CUR")
		if err == nil || !strings.Contains(err.Error(), "no project root") {
			t.Fatalf("err = %v, want 'no project root'", err)
		}
	})

	t.Run("BadRef", func(t *testing.T) {
		t.Parallel()
		_, _, _, err := parentBucketAndID("proj:bad", "/state/dir", "01CUR")
		if err == nil {
			t.Fatalf("expected decodeRef error for malformed proj ref")
		}
	})

	t.Run("BareValidID", func(t *testing.T) {
		t.Parallel()
		bucket, id, scope, err := parentBucketAndID("01BARE", "/state/dir", "01CUR")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if bucket != "/state/dir" || id != "01BARE" || scope != scopeCurrentProject {
			t.Fatalf("got (%q,%q,%q)", bucket, id, scope)
		}
	})

	t.Run("BareInvalidID", func(t *testing.T) {
		t.Parallel()
		_, _, _, err := parentBucketAndID("bad/id", "/state/dir", "01CUR")
		if err == nil || !strings.Contains(err.Error(), "invalid session selector") {
			t.Fatalf("err = %v, want 'invalid session selector'", err)
		}
	})
}
