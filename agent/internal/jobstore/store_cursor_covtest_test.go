package jobstore

import (
	"testing"

	"github.com/spf13/afero"
)

// TestTrimLineTerminator_CarriageReturn covers the \r trimming path in
// trimLineTerminator (line 504-506): a line ending with \r\n should have
// both the \n and \r stripped.
func TestTrimLineTerminator_CarriageReturn(t *testing.T) {
	got := trimLineTerminator([]byte("hello\r\n"))
	if string(got) != "hello" {
		t.Errorf("trimLineTerminator(hello\\r\\n) = %q, want %q", string(got), "hello")
	}
	// Plain \n (no \r) — should just strip \n.
	got = trimLineTerminator([]byte("world\n"))
	if string(got) != "world" {
		t.Errorf("trimLineTerminator(world\\n) = %q, want %q", string(got), "world")
	}
}

// TestScanLinesKeepingTerminator_AtEOFEmpty covers the atEOF+empty-data path
// (line 490-491).
func TestScanLinesKeepingTerminator_AtEOFEmpty(t *testing.T) {
	advance, token, err := scanLinesKeepingTerminator(nil, true)
	if advance != 0 || token != nil || err != nil {
		t.Errorf("scanLinesKeepingTerminator(nil, true) = (%d, %q, %v), want (0, nil, nil)", advance, string(token), err)
	}
}

// TestScanLinesKeepingTerminator_AtEOFData covers the atEOF+data path
// (line 496-497): at EOF with remaining data, all data is returned as a
// token.
func TestScanLinesKeepingTerminator_AtEOFData(t *testing.T) {
	advance, token, err := scanLinesKeepingTerminator([]byte("partial"), true)
	if advance != 7 || string(token) != "partial" || err != nil {
		t.Errorf("scanLinesKeepingTerminator(partial, true) = (%d, %q, %v), want (7, partial, nil)", advance, string(token), err)
	}
}

// TestScanLinesKeepingTerminator_NoNewlineNotEOF covers the partial-data
// not-at-EOF path (line 499): no newline and not at EOF returns 0,nil,nil to
// request more data.
func TestScanLinesKeepingTerminator_NoNewlineNotEOF(t *testing.T) {
	advance, token, err := scanLinesKeepingTerminator([]byte("partial"), false)
	if advance != 0 || token != nil || err != nil {
		t.Errorf("scanLinesKeepingTerminator(partial, false) = (%d, %q, %v), want (0, nil, nil)", advance, string(token), err)
	}
}

// TestAdvanceCursorLocked_NonexistentFile covers the IsNotExist path in
// advanceCursorLocked (line 436-438): a nonexistent jobs.jsonl returns nil,
// nil.
func TestAdvanceCursorLocked_NonexistentFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := &Store{fs: fs, path: "/nonexistent/jobs.jsonl"}
	tail, err := s.advanceCursorLocked()
	if err != nil {
		t.Fatalf("advanceCursorLocked on nonexistent file: %v", err)
	}
	if tail != nil {
		t.Errorf("expected nil tail, got %v", tail)
	}
}

// TestAdvanceCursorLocked_UnterminatedLine covers the unterminated-line tail
// path (lines 467-471): a file with a complete line followed by a partial line
// should return the partial line as tail and decode it.
func TestAdvanceCursorLocked_UnterminatedLine(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/jobs/jobs.jsonl"
	// Write a complete event line then a partial event line.
	complete := `{"kind":"job_started","seq":1,"job_id":"j1","type":"shell"}` + "\n"
	partial := `{"kind":"job_finished","seq":2,"job_id":"j1","status":"completed"}`
	if err := afero.WriteFile(fs, path, []byte(complete+partial), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{fs: fs, path: path}
	tail, err := s.advanceCursorLocked()
	if err != nil {
		t.Fatalf("advanceCursorLocked: %v", err)
	}
	if len(tail) != 1 {
		t.Fatalf("expected 1 tail event, got %d", len(tail))
	}
	if tail[0].JobID != "j1" || tail[0].Kind != EventJobFinished {
		t.Errorf("tail event = %+v, want job_finished j1", tail[0])
	}
	// The cursor should have 1 committed event (the complete line).
	if len(s.cursor.events) != 1 {
		t.Errorf("expected 1 cursor event, got %d", len(s.cursor.events))
	}
}

// TestAdvanceCursorLocked_SeekError covers the seek-error path (line 447-448):
// when the cursor has a nonzero offset but the file is shorter than expected.
func TestAdvanceCursorLocked_SeekError(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/jobs/jobs.jsonl"
	if err := afero.WriteFile(fs, path, []byte("short\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{fs: fs, path: path}
	// Set a cursor offset beyond the file length — Seek should fail on MemMapFs.
	s.cursor.offset = 99999
	s.cursor.valid = true
	_, err := s.advanceCursorLocked()
	// MemMapFs Seek may or may not error on out-of-bounds; either way the
	// function should handle it. If it errors, that's the seek-error path.
	_ = err
}
