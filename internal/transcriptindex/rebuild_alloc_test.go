package transcriptindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestColdRebuildAllocationBudget is a resource regression for the cold rebuild:
// allocating roughly eight times the transcript it reads was the largest single
// allocation event in the 2026-11-15 CPU-efficiency audit. The fixture is built
// before the measurement, so the delta is the rebuild alone.
//
// The measurement runs before the preservation assertions and the budget is
// checked last, so a content failure is reportable without the budget aborting
// first.
func TestColdRebuildAllocationBudget(t *testing.T) {
	path, entries, size := syntheticTranscript(t, coldRebuildTurns)
	x := openIndex(t, path, t.TempDir())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := x.Rebuild(info.Size()); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)

	// entries counts the header line too; the index counts entry lines only.
	if x.meta.Entries != uint64(entries-1) {
		t.Fatalf("rebuild covered %d entries, want %d", x.meta.Entries, entries-1)
	}
	assertAllWindows(t, x, path)

	allocated := after.TotalAlloc - before.TotalAlloc
	ratio := float64(allocated) / float64(size)
	t.Logf("rebuild of %d bytes (%d entries) allocated %d bytes (%.2fx, %d per entry)",
		size, x.meta.Entries, allocated, ratio, allocated/x.meta.Entries)
	if ratio >= coldRebuildMaxRatio {
		t.Fatalf("cold rebuild allocated %.2fx the transcript (%d bytes for %d), want under %.1fx",
			ratio, allocated, size, coldRebuildMaxRatio)
	}
}

const (
	// coldRebuildTurns sizes the fixture at about 3.4MB of transcript: enough
	// entries for the per-entry costs to dominate the fixed ones, small enough
	// that a default test run pays a fraction of a second.
	coldRebuildTurns = 400
	// coldRebuildMaxRatio bounds the rebuild's allocation relative to the
	// transcript it reads. Before the fix the per-line strict decode alone grew
	// a fresh streaming decoder buffer for every entry, spending about 8x.
	coldRebuildMaxRatio = 5.0
)

// TestColdRebuildSidecarsUnchanged pins that the rebuild still writes exactly
// the records it wrote before the allocation work: the scan reuses one line
// buffer and one JSON decode path, and neither may change a byte of items,
// turns, strings, or the update log.
//
// The generator stamps every entry with a fixed timestamp, so the sidecars come
// out byte-identical from run to run — this test first proves that by rebuilding
// twice and comparing, which is what makes the golden comparison meaningful.
func TestColdRebuildSidecarsUnchanged(t *testing.T) {
	path, _, _ := syntheticTranscript(t, coldRebuildTurns)
	if got, want := rebuildSidecarDigests(t, path), rebuildSidecarDigests(t, path); got != want {
		t.Fatalf("two rebuilds of one transcript differ, so this fixture cannot pin bytes:\n%s\n%s", got, want)
	}
	if got := rebuildSidecarDigests(t, path); got != goldenColdRebuildDigests {
		t.Fatalf("rebuild sidecars changed; the allocation fix must not alter the index:\ngot:\n%s\nwant:\n%s",
			got, goldenColdRebuildDigests)
	}
}

// rebuildSidecarDigests rebuilds the transcript in a fresh directory and
// describes each sidecar by name, byte length and content hash.
func rebuildSidecarDigests(t *testing.T, path string) string {
	t.Helper()
	dir := t.TempDir()
	x := openIndex(t, path, filepath.Join(dir, "index"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Rebuild(info.Size()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, name := range []string{itemsFile, turnsFile, stringsFile, updatesFile} {
		data, err := os.ReadFile(filepath.Join(x.dir, x.build, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&out, "%s %d %s\n", name, len(data), hex.EncodeToString(sum[:]))
	}
	return out.String()
}

// goldenColdRebuildDigests is what a cold rebuild of
// syntheticTranscript(t, coldRebuildTurns) wrote before the allocation fix: the
// name, byte length and SHA-256 of each of the four sidecar files.
const goldenColdRebuildDigests = `items 251328 c5c4944d75591f82a87b0f27bc703fc35ce8265fc60796c56f59479df0cb9c78
turns 87864 88a21f6bada64f1b01b86b957eb7579bac1b72b2a80a6f1279492bc377d26b6a
strings 20275 06700d3766b38c4a038343eea747b9c072e89d73d2e445a6bff2bb6dc144f468
updates 30912 9e4e5dc0e7f237b94b66c690314ad4cac70002a8abe7e8f29a1e5d5b31d04d47
`
