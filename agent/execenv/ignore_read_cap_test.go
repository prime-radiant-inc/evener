package execenv

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"time"
)

// capThenStallFile supplies exactly its configured bytes, then blocks forever —
// the shape that makes a reader's own sentinel peek past its limit stall.
type capThenStallFile struct {
	remaining int
	blocked   chan struct{}
}

func (f *capThenStallFile) Read(p []byte) (int, error) {
	if f.remaining > 0 {
		n := min(len(p), f.remaining)
		for i := range p[:n] {
			p[i] = 'a'
		}
		f.remaining -= n
		return n, nil
	}
	<-f.blocked
	return 0, io.EOF
}

func (f *capThenStallFile) Close() error               { return nil }
func (f *capThenStallFile) Stat() (fs.FileInfo, error) { return capThenStallInfo{}, nil }

type capThenStallInfo struct{}

func (capThenStallInfo) Name() string       { return ".gitignore" }
func (capThenStallInfo) Size() int64        { return 0 }
func (capThenStallInfo) Mode() fs.FileMode  { return 0o644 }
func (capThenStallInfo) ModTime() time.Time { return time.Time{} }
func (capThenStallInfo) IsDir() bool        { return false }
func (capThenStallInfo) Sys() any           { return nil }

type capThenStallFS struct{ f *capThenStallFile }

func (s capThenStallFS) Open(string) (fs.File, error) { return s.f, nil }

// TestReadIgnoreFileRefusesAnOverCapStallingStream is M5: readIgnoreFile hands
// readIgnoreFileBytes its own over-cap sentinel (cap+1), and readBounded then
// peeked one byte past ITS limit — so a stream supplying exactly cap+1 bytes and
// then stalling blocked that peek until the stall deadline, and readIgnoreFile
// swallowed the stall as unreadable, losing the over-cap budget refusal. The
// stream must yield the refusal promptly instead.
func TestReadIgnoreFileRefusesAnOverCapStallingStream(t *testing.T) {
	// Not parallel: shrinks maxGlobIgnoreFileBytes and grepReadStallTimeout.
	origLimit := maxGlobIgnoreFileBytes
	maxGlobIgnoreFileBytes = 8
	t.Cleanup(func() { maxGlobIgnoreFileBytes = origLimit })
	origStall := grepReadStallTimeout
	grepReadStallTimeout = 50 * time.Millisecond
	t.Cleanup(func() { grepReadStallTimeout = origStall })

	f := &capThenStallFile{remaining: maxGlobIgnoreFileBytes + 1, blocked: make(chan struct{})}
	t.Cleanup(func() { close(f.blocked) })

	type result struct {
		read bool
		err  error
	}
	done := make(chan result, 1)
	go func() {
		_, read, err := readIgnoreFile(context.Background(), capThenStallFS{f: f}, ".gitignore", newGlobBudget("grep"))
		done <- result{read, err}
	}()
	select {
	case res := <-done:
		if res.err == nil {
			t.Fatalf("an over-cap ignore file that stalls at the cap returned no error (read=%v), want the over-cap budget refusal", res.read)
		}
		if _, ok := errors.AsType[*globBudgetError](res.err); !ok {
			t.Fatalf("over-cap ignore file err = %v, want a *globBudgetError", res.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readIgnoreFile on an over-cap stalling stream never returned: the second sentinel read stalled")
	}
}
