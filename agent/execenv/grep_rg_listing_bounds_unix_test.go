//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The omission-count listings must count ripgrep's --files output without
// retaining it: a listing of any size costs its delimiters' count and one
// pipe block, never the listing itself (#4574 round 6, HIGH — the captured
// listing was the OOM class this PR closes, reached through the disclosure
// feature). The red-asserted property is the count path's allocation: a
// 16 MiB listing of NUL-delimited paths must stream through with total
// allocation under a fixed bound (2 MiB) while the count still comes back
// exact — the pre-fix path materialized the whole listing in the captured
// stdout buffer before counting, so its allocation was the listing's size.
func TestRgListingCountRetainsNothing(t *testing.T) {
	// Not parallel: the allocation measurement is only meaningful without
	// other tests running against it.
	root := t.TempDir()
	rg := filepath.Join(root, "rg")
	script := "#!/bin/sh\nhead -c 16777216 /dev/zero\n"
	if err := os.WriteFile(rg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return rg, nil }

	const bound = 2 << 20
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	count, err := env.grepRgListFiles(t.Context(), rg, root, nil, false)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("16 MiB listing: %v", err)
	}
	if want := 16777216; count != want {
		t.Fatalf("16 MiB listing counted %d delimiters, want %d", count, want)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > bound {
		t.Fatalf("counting a 16 MiB listing allocated %d bytes, want at most %d: the listing must not be retained", allocated, bound)
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
