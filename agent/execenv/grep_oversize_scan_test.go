package execenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/searchresult"
)

// neverEndingSeeker is an injectable large-file source for the streaming
// scan: every Read hands over one window of matching text and the source
// never ends, so a test can watch the scan's windows happen and cancel it
// mid-scan. The reads counter is what makes the cancellation observable.
type neverEndingSeeker struct {
	window []byte
	reads  atomic.Int64
}

func (r *neverEndingSeeker) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return copy(p, r.window), nil
}

func (r *neverEndingSeeker) Seek(int64, int) (int64, error) { return 0, nil }

// A search cancelled while the streaming scan is mid-file must stop reading
// between windows and report the cancellation (#4574 round 5, medium 1:
// ordinary regular files take no read deadline, so without a check between
// windows a cancelled scan reads the whole file — and a continuously growing
// log scans indefinitely).
func TestGrepStreamScanStopsWhenCanceledMidScan(t *testing.T) {
	// Not parallel: stubGrepStreamWindowBytes swaps the package's window.
	stubGrepStreamWindowBytes(t, 16)
	a, err := newGrepAccum("needle", false, 100, "content", 0)
	if err != nil {
		t.Fatal(err)
	}
	src := &neverEndingSeeker{window: []byte("needle line here\n")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.feedLargeFile(ctx, src)
		done <- err
	}()

	// Let the scan deliver a few windows, then cancel it.
	deadline := time.Now().Add(5 * time.Second)
	for src.reads.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("streaming scan never read its first windows")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled mid-scan stream = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled scan kept scanning: feedLargeFile never returned")
	}
	frozen := src.reads.Load()
	time.Sleep(20 * time.Millisecond)
	if got := src.reads.Load(); got > frozen+1 {
		t.Fatalf("scan read %d more windows after cancellation (stopped at %d), want it stopped", got-frozen, frozen)
	}
}

// A search already cancelled when the scan starts reads nothing.
func TestGrepStreamScanRefusesACanceledSearch(t *testing.T) {
	// Not parallel: stubGrepStreamWindowBytes swaps the package's window.
	stubGrepStreamWindowBytes(t, 16)
	a, err := newGrepAccum("needle", false, 100, "content", 0)
	if err != nil {
		t.Fatal(err)
	}
	src := &neverEndingSeeker{window: []byte("needle line here\n")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.feedLargeFile(ctx, src); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled-before-start scan = %v, want context.Canceled", err)
	}
	if src.reads.Load() != 0 {
		t.Fatalf("canceled scan read %d windows, want none", src.reads.Load())
	}
}

// The sandboxed named-file path must hand a cancellation that lands during
// the streaming scan back as the search's error, not convert it into
// successful empty output (#4574 round 5, medium 2). The one-byte window over
// a megabyte of file keeps the scan running long past the cancellation, so
// the mid-scan landing is deterministic.
func TestSandboxedGrepStreamSearchReturnsTheCancellationError(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes and stubGrepStreamWindowBytes swap
	// the package's cap and window.
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	big := filepath.Join(worktree, "big.log")
	if err := os.WriteFile(big, bytes.Repeat([]byte("filler line\n"), 90_000), 0o600); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	stubGrepStreamWindowBytes(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := env.Grep(ctx, "needle", big, "", false, 100, "content")
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled sandboxed stream search = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("canceled sandboxed stream search never returned")
	}
}

// The streaming scan admits an over-cap line by its length alone, never by
// where the read windows happen to fall: a matching line of exactly the read
// cap is searched at every window size, one a byte past it is searched at
// none, and a preceding newline — which shifts every window boundary —
// changes only the line's number, not whether it is searched (#4574 round 5,
// medium 4).
func TestGrepStreamAdmitsOverCapLinesByLengthNotAlignment(t *testing.T) {
	// Not parallel: stubGrepMaxFileBytes and stubGrepStreamWindowBytes swap
	// the package's cap and window.
	root := t.TempDir()
	atCap := "needle" + strings.Repeat("x", 64-len("needle"))
	pastCap := "needle" + strings.Repeat("x", 64-len("needle")+1)
	filler := strings.Repeat("plain line\n", 10)
	files := map[string]string{
		"atcap.log":   atCap + "\n" + filler,
		"pastcap.log": pastCap + "\n" + filler,
		"shift.log":   "\n" + atCap + "\n" + filler,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	stubGrepMaxFileBytes(t, 64)
	for _, window := range []int{8, 16, 32, 64, 65, 1000} {
		stubGrepStreamWindowBytes(t, window)
		got, err := env.Grep(t.Context(), "needle", filepath.Join(root, "atcap.log"), "", false, 100, "content")
		if err != nil {
			t.Fatalf("window %d: at-cap line: %v", window, err)
		}
		if want := "1:" + atCap; got != want {
			t.Errorf("window %d: at-cap matching line = %q, want %q", window, got, want)
		}
		got, err = env.Grep(t.Context(), "needle", filepath.Join(root, "pastcap.log"), "", false, 100, "content")
		if err != nil {
			t.Fatalf("window %d: past-cap line: %v", window, err)
		}
		entries, notes := searchresult.Split(got)
		if entries != "" {
			t.Errorf("window %d: past-cap matching line = %q, want it skipped at every window size", window, got)
		}
		if notes == "" {
			t.Errorf("window %d: past-cap matching line omitted the incompleteness note", window)
		}
	}
	for _, window := range []int{8, 1000} {
		stubGrepStreamWindowBytes(t, window)
		got, err := env.Grep(t.Context(), "needle", filepath.Join(root, "shift.log"), "", false, 100, "content")
		if err != nil {
			t.Fatalf("window %d: shifted line: %v", window, err)
		}
		if want := "2:" + atCap; got != want {
			t.Errorf("window %d: shifted at-cap matching line = %q, want %q: a preceding newline must not change admission", window, got, want)
		}
	}
}
