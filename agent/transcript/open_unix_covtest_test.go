package transcript

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenTranscriptAppendFile_NonRegular covers the non-regular-file branch
// (open_unix.go:27-30): opening a device special file (which succeeds with
// O_RDWR|O_NOFOLLOW) and then detecting it is not a regular file.
func TestOpenTranscriptAppendFile_NonRegular(t *testing.T) {
	// /dev/null is a character device — open succeeds, but Stat().IsRegular()
	// returns false, triggering the "not a regular file" error.
	_, err := openTranscriptAppendFile("/dev/null")
	if err == nil {
		t.Fatal("expected error opening non-regular file as transcript")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want 'not a regular file'", err)
	}
}

// TestOpenTranscriptAppendFile_NotExist covers the open-error branch
// (open_unix.go:14-15): a missing path returns an error.
func TestOpenTranscriptAppendFile_NotExist(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.jsonl")
	_, err := openTranscriptAppendFile(missing)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
