//go:build linux || darwin

package execenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The omission-count listings must count ripgrep's --files output without
// retaining it: a listing of any size costs its delimiters' count and one
// pipe block, never the listing itself (#4574 round 6, HIGH — the captured
// listing was the OOM class this PR closes, reached through the disclosure
// feature). The red-asserted property is the count path's retained memory: a
// large listing of NUL-delimited paths must stream through without the listing
// staying live, while the count still comes back exact — the pre-fix path
// materialized the whole listing in the captured stdout buffer before counting,
// so its footprint was the listing's size. Total allocation is the measure:
// each listed path is stat'd and then dropped, so the count costs one path's
// bytes at a time and never the stream.
//
// The listing names one real over-cap file, then repeats one path that does
// not exist: every delimiter is a path the arm stats, so the per-path work a
// real listing does is exercised, while the count it must return stays a
// number this fixture knows exactly.
func TestRgListingCountRetainsNothing(t *testing.T) {
	// Not parallel: the allocation measurement is only meaningful without
	// other tests running against it.
	root := t.TempDir()
	rg := filepath.Join(root, "rg")
	// The listing prints the over-cap file, then a path that does not exist
	// 65536 times: every one is delimited, so every one is stat'd and
	// dropped.
	script := "#!/bin/sh\nprintf '%s/big.log\\000' \"$4\"\ni=0\nwhile [ $i -lt 65536 ]; do printf 'missing/%s\\000' \"$i\"; i=$((i+1)); done\n"
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.log"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	stubGrepMaxFileBytes(t, 64)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	count, err := env.grepRgListFiles(t.Context(), rg, root, nil)
	if err != nil {
		t.Fatalf("large listing: %v", err)
	}
	if want := 1; count != want {
		t.Fatalf("large listing counted %d over-cap paths, want %d: only big.log is over the 64-byte cap", count, want)
	}
}

// The counter itself retains nothing: whatever chunk sizes the listing's bytes
// arrive in, it holds at most one path and one pipe block, and its result does
// not depend on how the stream was split. Materializing the listing instead
// would cost the stream. This is the retention assertion the listing test above
// cannot make, because stat only one path at a time and its cost per path is
// the filesystem's, not the counter's.
func TestRgListingCounterRetainsNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "over"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "under"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := grepMaxFileBytes
	grepMaxFileBytes = 64
	t.Cleanup(func() { grepMaxFileBytes = previous })

	// One path split across four writes must count the same as one whole write.
	whole := []byte("over\x00under\x00")
	split := &oversizePathCounter{ctx: t.Context(), base: root}
	for _, chunk := range [][]byte{[]byte("ov"), []byte("er\x00un"), []byte("de"), []byte("r\x00")} {
		if _, err := split.Write(chunk); err != nil {
			t.Fatalf("split write: %v", err)
		}
	}
	one := &oversizePathCounter{ctx: t.Context(), base: root}
	if _, err := one.Write(whole); err != nil {
		t.Fatalf("whole write: %v", err)
	}
	if split.n != one.n || split.n != 1 {
		t.Fatalf("split listing counted %d over-cap paths and the whole listing %d, want 1 each", split.n, one.n)
	}
	if len(split.partial) != 0 || len(one.partial) != 0 {
		t.Fatalf("the counter kept a partial path after the stream ended: %d and %d bytes", len(split.partial), len(one.partial))
	}

	// A stream that never delimits is not a listing of paths. It must fail
	// rather than grow: that buffer is the listing this path exists to never
	// retain.
	unterminated := &oversizePathCounter{ctx: t.Context(), base: root}
	chunk := bytes.Repeat([]byte{'a'}, 4096)
	total := 0
	for range 64 {
		n, err := unterminated.Write(chunk)
		if err != nil {
			t.Fatalf("unterminated write: %v", err)
		}
		total += n
		if len(unterminated.partial) > maxListedPathBytes {
			t.Fatalf("the counter held %d bytes with no delimiter in the stream, past the %d-byte path bound", len(unterminated.partial), maxListedPathBytes)
		}
	}
	if unterminated.failed == nil {
		t.Fatalf("an unterminated %d-byte stream was accepted, want a failure: it is not a listing of paths", total)
	}
}

// A search cancelled while the omission listings run reports the
// cancellation as its error, not successful output: the listings share the
// search's ctx, the second one never starts after the first was cancelled,
// and both ripgrep result branches check ctx before returning (#4574 round
// 6, MEDIUM). The marker file pins the cancel to mid-listing, the same
// deterministic technique TestGrepRipgrepStopsWhenContextIsCanceled uses.
func TestRgArmReportsCancellationDuringTheOmissionListings(t *testing.T) {
	// Not parallel: the marker poll and cancel must observe one search.
	root := t.TempDir()
	rg := filepath.Join(root, "rg")
	marker := rg + ".started"
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n\tcase \"$arg\" in\n\t--files) : > \"$0.started\"; sleep 60 ;;\n\tesac\ndone\n"
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := env.Grep(ctx, "needle", root, "", false, 100, "content")
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("omission listing never started")
		}
		select {
		case err := <-done:
			t.Fatalf("search ended before the listing could be cancelled: %v", err)
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("search cancelled mid-listing = %v, want context.Canceled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled search never returned")
	}
}
