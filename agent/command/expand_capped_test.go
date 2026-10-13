package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// @file expansion runs synchronously during argument substitution, so its
// reads carry the whole-file read cap: an over-cap source degrades to the
// existing inline error marker instead of being ingested whole (issue #4556).
// The oracle is structural — the refusal must name the file, stay bounded,
// and carry none of the file's bytes — not the marker's wording.

// oversizeMarker marks the oversize fixture's bytes, so an ingest that
// streams them into the expansion shows up in the result. Deliberately not
// secret-shaped: it is plain words, so the secret scanner has nothing to flag.
const oversizeMarker = "oversize-fixture-marker "

// markerChunk is a little over 1 MiB of marker bytes, so whole-number writes
// march past the cap.
func markerChunk() []byte {
	return []byte(strings.Repeat(oversizeMarker, (1<<20)/len(oversizeMarker)+1))
}

// writeMarkerOversize writes a real (non-sparse) file of NUL-free marker
// bytes one byte past the whole-file read cap.
func writeMarkerOversize(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create oversize: %v", err)
	}
	defer f.Close()
	chunk := markerChunk()
	for written := int64(0); written < execenv.MaxWholeFileReadBytes; written += int64(len(chunk)) {
		if _, err := f.Write(chunk); err != nil {
			t.Fatalf("write oversize: %v", err)
		}
	}
}

func TestReadInlineFileDegradesAnOverCapFileToAMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.log")
	writeMarkerOversize(t, path)
	env := execenv.NewLocalExecutionEnvironment(dir)

	got := readInlineFile("huge.log", env)
	if strings.Contains(got, oversizeMarker) {
		t.Fatalf("readInlineFile(oversize) inlined the over-cap file's bytes")
	}
	if len(got) > maxInlineBytes {
		t.Fatalf("readInlineFile(oversize) returned %d bytes, want a bounded marker", len(got))
	}
	if !strings.Contains(got, "huge.log") {
		t.Fatalf("readInlineFile(oversize) = %q, want the refusal to name the file", got)
	}
}
