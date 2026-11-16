package transcriptindex

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// TestOldBuildWithoutSidecarsServesAndGainsThem pins the slot table as derived
// state: a build written before the file existed must serve every read through
// the turn table, and its next commit must mint the table, without the rebuild
// a corrupt sidecar would force.
func TestOldBuildWithoutSidecarsServesAndGainsThem(t *testing.T) {
	path := writeFixture(t, fixture{header: transcript.Header{SessionID: "old_build"}, lines: []fixtureLine{
		entryLine(opens("turn_a", schema.TurnSpanExecution, user("first question"))),
		entryLine(inTurn("turn_a", withUsage(assistant(text("first answer")), 5, 2, 0, 7))),
		entryLine(completion("turn_a", schema.TurnCompleted, 1, 1)),
		entryLine(opens("turn_b", schema.TurnSpanExecution, user("second question"))),
		entryLine(inTurn("turn_b", withUsage(assistant(text("second answer")), 3, 1, 0, 4))),
		entryLine(completion("turn_b", schema.TurnCompleted, 2, 1)),
	}})
	dir := filepath.Join(t.TempDir(), "index")
	x := openIndex(t, path, dir)

	// Stand for a build that predates the file: drop it and the meta that names
	// the table, exactly as the older writer left the build.
	if name := x.meta.TurnSlotsFile; name != "" {
		if err := os.Remove(filepath.Join(dir, x.build, name)); err != nil {
			t.Fatal(err)
		}
	}
	x.closeTurnSlots()
	x.meta.TurnSlotsFile, x.meta.TurnSlotsCapacity = "", 0
	// publishMeta alone, not writeMeta: writeMeta is the commit that mints the
	// table, so going through it would leave this a current build.
	if err := x.publishMeta(); err != nil {
		t.Fatal(err)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopened, the old build answers the read from the turn table.
	reopened := openIndex(t, path, dir)
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.turnSlots != nil {
		t.Fatal("a build naming no slot table opened one")
	}
	window, err := reopened.ResolveTurnWindow(appwire.ThreadTurnWindow{Turn: "turn_b"})
	if err != nil {
		t.Fatalf("ResolveTurnWindow on a build without the table: %v", err)
	}
	if len(window.TurnIDs) != 1 || window.TurnIDs[0] != "turn_b" {
		t.Fatalf("turn window ids = %v, want [turn_b]", window.TurnIDs)
	}

	// The next commit mints the table, so the build gains it with no rebuild:
	// one O(turns) migration, then extensions are incremental again.
	appended := []fixtureLine{
		entryLine(opens("turn_c", schema.TurnSpanExecution, user("third question"))),
		entryLine(inTurn("turn_c", assistant(text("third answer")))),
		entryLine(completion("turn_c", schema.TurnCompleted, 3, 1)),
	}
	_, encoded := fixture{header: transcript.Header{SessionID: "old_build"}, lines: appended}.encode(t)
	appendBytes(t, path, joinLines(encoded))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.CatchUpTo(info.Size()); err != nil {
		t.Fatalf("CatchUpTo: %v", err)
	}
	if reopened.turnSlots == nil {
		t.Fatal("commit did not mint the slot table")
	}
}
