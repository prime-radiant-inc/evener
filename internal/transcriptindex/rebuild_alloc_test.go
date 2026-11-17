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

	ceiling := coldRebuildMaxRatio()
	allocated := after.TotalAlloc - before.TotalAlloc
	ratio := float64(allocated) / float64(size)
	t.Logf("rebuild of %d bytes (%d entries) allocated %d bytes (%.2fx, %d per entry, ceiling %.1fx, race %v)",
		size, x.meta.Entries, allocated, ratio, allocated/x.meta.Entries, ceiling, raceDetectorEnabled)
	if ratio >= ceiling {
		t.Fatalf("cold rebuild allocated %.2fx the transcript (%d bytes for %d), want under %.1fx",
			ratio, allocated, size, ceiling)
	}
}

const (
	// coldRebuildTurns sizes the fixture at about 3.4MB of transcript: enough
	// entries for the per-entry costs to dominate the fixed ones, small enough
	// that a default test run pays a fraction of a second.
	coldRebuildTurns = 400

	// The two bounds below are deliberate literals, not values derived from the
	// implementation under test. Both lanes measured the same rebuild: the race
	// detector's own bookkeeping inflates every allocation, so one number cannot
	// serve both without either passing the regression or failing a healthy run.
	//
	// Plain: a healthy rebuild of this fixture spends about 4.0x, and the
	// pre-fix per-line strict decode — which grew a fresh streaming decoder
	// buffer for every entry — spent about 8.1x. 5.0 sits between them.
	coldRebuildMaxRatioPlain = 5.0

	// Race: both bounds below were measured, not guessed. Five runs of the
	// fixed tree spent 8.29, 8.41, 8.44, 8.29 and 8.38x — a 1.8% spread that
	// tops out at 8.44. The pre-fix tree spent 12.54x and 12.65x, because the
	// detector's inflation multiplies the per-entry decoder buffers the fix
	// removed. 11.0 therefore sits 1.30x above the worst healthy sample, a
	// little more relative room than the plain lane's 5.0 over 3.98 (1.26x),
	// and 12% below the cheaper of the two regressed samples, so the -race lane
	// still fails the tree this test exists to reject.
	//
	// The window between the two states is narrower under -race than plain
	// (8.44 to 12.54, against 3.98 to 8.12). If a loaded runner ever pushes the
	// healthy number past 11.0, that is evidence the -race arm can no longer
	// tell the states apart, not a reason to raise this again: at that point
	// the plain lane is carrying the real check and the -race arm is only a
	// gross-regression bound.
	coldRebuildMaxRatioRace = 11.0
)

// coldRebuildMaxRatio is this lane's ceiling, split by build tag so a -race run
// is held to a bound that is still tight enough to catch the regression.
func coldRebuildMaxRatio() float64 {
	if raceDetectorEnabled {
		return coldRebuildMaxRatioRace
	}
	return coldRebuildMaxRatioPlain
}

// TestColdRebuildSidecarsAreReproducible pins that the rebuild is a function of
// its input: the scan reuses one line buffer and one JSON decode path, so two
// rebuilds of one transcript must write byte-identical sidecars.
//
// It deliberately does not compare against a recorded digest of the bytes.
// That was useful while the allocation fix was in flight and "did the bytes
// stay identical" was the open question, but as a permanent gate it misfires:
// it fails on any intentional change to schema.Turn's or llm.Message's JSON
// shape, to the index record format, or to the fixture generator, and its
// message would blame the rebuild. The index format already has a deliberate
// version — formatVersion and projectionID, either of which rebuilds every
// index — so an intentional format change does not need a digest to announce
// itself, and TestColdRebuildAllocationBudget's assertAllWindows already proves
// a rebuilt index reads back correctly.
func TestColdRebuildSidecarsAreReproducible(t *testing.T) {
	path, _, _ := syntheticTranscript(t, coldRebuildTurns)
	if got, want := rebuildSidecarDigests(t, path), rebuildSidecarDigests(t, path); got != want {
		t.Fatalf("two rebuilds of one transcript differ, so the rebuild is not a function of its input:\n%s\n%s", got, want)
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
