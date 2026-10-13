package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/searchresult"
)

// stubGrepMaxFileBytes lowers the per-file read cap for a test and restores
// it when it ends, so a fixture file can exceed the cap without megabytes.
func stubGrepMaxFileBytes(t *testing.T, n int) {
	t.Helper()
	orig := grepMaxFileBytes
	grepMaxFileBytes = n
	t.Cleanup(func() { grepMaxFileBytes = orig })
}

// neverEndingReader is a source whose Read never returns EOF — the shape
// /proc/kcore and /dev/zero both present: a regular file that never runs out.
type neverEndingReader struct{ b byte }

func (r neverEndingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

// countingReader counts how many bytes its wrapped reader actually handed
// over, so a test can prove a capped read stopped rather than drained.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// grepCappedRead ends an effectively-unbounded source at the cap: one byte
// more than grepMaxFileBytes is read, the reader is released, and the file is
// reported over-cap rather than searched partially. A source at or under the
// cap comes back whole and byte-for-byte.
func TestGrepCappedReadEndsAnUnboundedSource(t *testing.T) {
	t.Parallel()
	counting := &countingReader{r: neverEndingReader{b: 'x'}}
	data, err := grepCappedRead(counting)
	if !errors.Is(err, errGrepFileTooLarge) {
		t.Fatalf("unbounded read: err = %v, want errGrepFileTooLarge", err)
	}
	if len(data) != 0 {
		t.Fatalf("over-cap read returned %d bytes, want none", len(data))
	}
	if want := int64(grepMaxFileBytes) + 1; counting.n != want {
		t.Fatalf("read %d bytes from a never-ending source, want exactly %d", counting.n, want)
	}

	// A source exactly at the cap is a file, not an over-cap stream.
	bounded := bytes.Repeat([]byte("needle\n"), grepMaxFileBytes/7+1)[:grepMaxFileBytes]
	counting = &countingReader{r: bytes.NewReader(bounded)}
	data, err = grepCappedRead(counting)
	if err != nil {
		t.Fatalf("at-cap read: %v", err)
	}
	if !bytes.Equal(data, bounded) {
		t.Fatalf("at-cap read returned %d bytes, want the whole %d", len(data), len(bounded))
	}
}

// The walk skips a file past the read cap the way it skips a binary one —
// without a match, without spending a result — and says so in a note, on the
// off arm; a file named as the target past the cap is a deliberate request,
// so it is searched in bounded windows rather than skipped (#4574 round 4).
func TestGrepSkipsFilesPastTheReadCap(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	root := t.TempDir()
	small := filepath.Join(root, "small.txt")
	big := filepath.Join(root, "big.txt")
	if err := os.WriteFile(small, []byte("needle small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, bytes.Repeat([]byte("needle big\n"), 10), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", root, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "small.txt" {
		t.Fatalf("walk past the read cap = %q, want only %q", got, "small.txt")
	}
	if notes == "" {
		t.Error("walk past the read cap omitted the over-cap note")
	}
	got, err = env.Grep(t.Context(), "needle", root, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes = searchresult.Split(got)
	if entries != "small.txt:1:needle small" {
		t.Fatalf("content past the read cap = %q, want only %q", got, "small.txt:1:needle small")
	}
	if notes == "" {
		t.Error("content walk past the read cap omitted the over-cap note")
	}

	got, err = env.Grep(t.Context(), "needle", big, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	var bigWant []string
	for i := 1; i <= 10; i++ {
		bigWant = append(bigWant, fmt.Sprintf("%d:needle big", i))
	}
	if want := strings.Join(bigWant, "\n"); got != want {
		t.Fatalf("named over-cap file = %q, want %q from the bounded-window search", got, want)
	}
	got, err = env.Grep(t.Context(), "needle", small, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1:needle small"; got != want {
		t.Fatalf("named under-cap file = %q, want %q", got, want)
	}
}

// The sandboxed arm's walk and named-file path apply the same cap, note,
// and bounded-window search of a named over-cap file.
func TestSandboxedGrepSkipsFilesPastTheReadCap(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes swaps the package's read cap.
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	small := filepath.Join(worktree, "small.txt")
	big := filepath.Join(worktree, "big.txt")
	if err := os.WriteFile(small, []byte("needle small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, bytes.Repeat([]byte("needle big\n"), 10), 0o600); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)

	got, err := env.Grep(t.Context(), "needle", worktree, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	entries, notes := searchresult.Split(got)
	if entries != "small.txt" {
		t.Fatalf("sandboxed walk past the read cap = %q, want only %q", got, "small.txt")
	}
	if notes == "" {
		t.Error("sandboxed walk past the read cap omitted the over-cap note")
	}
	got, err = env.Grep(t.Context(), "needle", big, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	var bigWant []string
	for i := 1; i <= 10; i++ {
		bigWant = append(bigWant, fmt.Sprintf("%d:needle big", i))
	}
	if want := strings.Join(bigWant, "\n"); got != want {
		t.Fatalf("sandboxed named over-cap file = %q, want %q from the bounded-window search", got, want)
	}
}
