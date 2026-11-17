package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readWindowNULFloor is the size a fixture must exceed for a bounded read to
// scan it rather than load it. It is a literal (128 KiB) rather than the
// implementation's readWindowWholeBelow, so this test keeps failing for its own
// reason if that threshold moves: the fixture is built against the size the
// contract names, not against the constant a regression could lower.
const readWindowNULFloor = 128 << 10

// TestReadFileWindowScopesTheBinaryRefusal pins the decision read_window.go's
// readWindowWholeBelow comment states: above the whole-file threshold the NUL
// refusal covers only the bytes the read scans, so a NUL past the window comes
// back as text, while a NUL in or before the window still refuses. At or below
// the threshold the read loads the whole file, so a NUL anywhere in it — even
// one past the requested window — still refuses.
//
// The fixtures are text rows with a NUL tail, never a sparse file: a hole reads
// back as NULs, which would refuse for the wrong reason and make the "returns
// rows" case pass while proving nothing.
func TestReadFileWindowScopesTheBinaryRefusal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	const row = "ordinary text row of a large log file\n"
	pad := strings.Repeat(row, readWindowNULFloor/len(row)+1)
	if len(pad) <= readWindowNULFloor {
		t.Fatalf("pad is %d bytes, want past the %d-byte whole-file threshold", len(pad), readWindowNULFloor)
	}

	write := func(t *testing.T, name, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	read := func(t *testing.T, path string, off, lim int) (string, error) {
		t.Helper()
		return env.ReadFileCtx(t.Context(), path, &off, &lim)
	}

	t.Run("NUL past the window returns the rows", func(t *testing.T) {
		t.Parallel()
		path := write(t, "nul-tail.log", pad+"binary\x00tail\n")
		got, err := read(t, path, 1, 2)
		if err != nil {
			t.Fatalf("windowed read of a file whose NUL is past the window: %v", err)
		}
		off, lim := 1, 2
		want := NumberLinesBytes([]byte(pad+"binary\x00tail\n"), &off, &lim)
		if got != want {
			t.Fatalf("window = %q, want %q", got, want)
		}
	})

	t.Run("NUL inside the window refuses", func(t *testing.T) {
		t.Parallel()
		path := write(t, "nul-window.log", "first\nbinary\x00line\n"+pad)
		_, err := read(t, path, 1, 2)
		if err == nil || !strings.Contains(err.Error(), "binary file (NUL byte)") {
			t.Fatalf("windowed read whose NUL is inside the window = %v, want the binary-file refusal", err)
		}
	})

	t.Run("NUL before the window refuses", func(t *testing.T) {
		t.Parallel()
		path := write(t, "nul-before.log", "binary\x00line\nsecond\n"+pad)
		_, err := read(t, path, 3, 1)
		if err == nil || !strings.Contains(err.Error(), "binary file (NUL byte)") {
			t.Fatalf("windowed read whose NUL is in the skipped prefix = %v, want the binary-file refusal", err)
		}
	})

	t.Run("NUL past the window still refuses at or below the threshold", func(t *testing.T) {
		t.Parallel()
		// Well under readWindowNULFloor, so the whole-file path runs and its
		// NUL check covers every byte — including the one past line 2.
		path := write(t, "small-nul-tail.log", "first\nsecond\nbinary\x00tail\n")
		_, err := read(t, path, 1, 2)
		if err == nil || !strings.Contains(err.Error(), "binary file (NUL byte)") {
			t.Fatalf("windowed read of a small file with a trailing NUL = %v, want the binary-file refusal", err)
		}
	})
}
