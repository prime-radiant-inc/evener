package execenv

import "testing"

// TestRgRetainedRecordsDropsPartialRecords pins that the cut back to the last
// complete record uses ripgrep's actual record shape and the KNOWN output mode,
// not "last newline" and not a shape inferred from the retained text. A
// content/count record is `<path>\0<line>\n`, and a pathname may contain
// newlines (only its NUL terminates it), so a cut inside such a pathname must
// not be read as a record boundary and retained as a bogus row.
func TestRgRetainedRecordsDropsPartialRecords(t *testing.T) {
	t.Parallel()
	// Cut inside a newline-bearing pathname.
	pathCut := "a/b\x001:match\nweird\npath/fi"
	if got, want := rgRetainedRecords(pathCut, false, false), "a/b\x001:match\n"; got != want {
		t.Fatalf("content cut inside a newline-bearing path = %q, want %q", got, want)
	}
	// Cut inside a payload (after the path's NUL, before its newline).
	payloadCut := "a/b\x001:match\nweird\npath/file\x00partial"
	if got, want := rgRetainedRecords(payloadCut, false, false), "a/b\x001:match\n"; got != want {
		t.Fatalf("content cut inside a payload = %q, want %q", got, want)
	}
	// Cut inside the FIRST pathname, whose newline is the only newline before
	// the cut: no record completed, so nothing is retained (the mode is known to
	// be multi-file, so the missing NUL is not mistaken for single-file mode).
	firstPath := "weird\npath/fi"
	if got, want := rgRetainedRecords(firstPath, false, false), ""; got != want {
		t.Fatalf("cut inside the first pathname = %q, want %q", got, want)
	}
	// files_with_matches records end at the path's NUL; a partial trailing path
	// is dropped.
	filesCut := "weird\npath/one\x00weird\npath/tw"
	if got, want := rgRetainedRecords(filesCut, true, false), "weird\npath/one\x00"; got != want {
		t.Fatalf("files_with_matches cut = %q, want %q", got, want)
	}
	// A single named file carries no path: its records are plain lines.
	oneFile := "line one\nline two\npartial"
	if got, want := rgRetainedRecords(oneFile, false, true), "line one\nline two\n"; got != want {
		t.Fatalf("single-file cut = %q, want %q", got, want)
	}
}
