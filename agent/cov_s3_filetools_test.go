package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS3Cov_FileTools_ReadWriteEdit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSession(t, withDir(dir))

	// write_file creates a new file (no read-before-write warning for new files).
	res := s3cov_exec(t, s, "write_file", `{"file_path":"note.txt","content":"hello world\n"}`)
	if res.IsError {
		t.Fatalf("write_file error: %s", res.Output)
	}

	// read_file returns content and tracks the read.
	res = s3cov_exec(t, s, "read_file", `{"file_path":"note.txt","purpose":"inspect"}`)
	if res.IsError || !strings.Contains(res.Output, "hello world") {
		t.Fatalf("read_file: err=%v out=%s", res.IsError, res.Output)
	}

	// edit_file replaces text after a tracked read (no warning expected).
	res = s3cov_exec(t, s, "edit_file", `{"file_path":"note.txt","old_string":"hello","new_string":"goodbye"}`)
	if res.IsError {
		t.Fatalf("edit_file error: %s", res.Output)
	}
	got, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "goodbye world") {
		t.Fatalf("edit did not apply: %q", string(got))
	}
}

func TestS3Cov_FileTools_ReadBeforeWriteWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Pre-create a file the session never read, so write_file warns.
	if err := os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withDir(dir))

	res := s3cov_exec(t, s, "write_file", `{"file_path":"pre.txt","content":"new content\n"}`)
	if res.IsError {
		t.Fatalf("write_file error: %s", res.Output)
	}
	// The read-before-write guard prepends a WARNING to the success output.
	if !strings.Contains(res.Output, "WARNING: Writing to file that has not been read") {
		t.Fatalf("expected read-before-write warning, got: %q", res.Output)
	}
}
