package schema

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNextBootGenerationIncreasesAcrossStarts(t *testing.T) {
	dir := t.TempDir()
	var got []uint64
	for range 3 {
		generation, err := NextBootGeneration(dir, "S1", 0)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, generation)
	}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("generations = %v, want [1 2 3]", got)
	}
	// Another session's counter is its own.
	other, err := NextBootGeneration(dir, "S2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if other != 1 {
		t.Fatalf("S2 generation = %d, want 1", other)
	}
}

// TestNextBootGenerationSurvivesACrashedWrite: a daemon killed mid-increment
// leaves its temp file behind and the committed counter unchanged; the next
// start still increments past the committed value. Nothing about a clean
// shutdown is needed for the counter to move.
func TestNextBootGenerationSurvivesACrashedWrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := NextBootGeneration(dir, "S1", 0); err != nil {
		t.Fatal(err)
	}
	path := BootGenerationPath(dir, "S1")
	if err := os.WriteFile(path+".tmp", []byte("99"), 0o644); err != nil {
		t.Fatal(err)
	}
	generation, err := NextBootGeneration(dir, "S1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if generation != 2 {
		t.Fatalf("generation after a crashed write = %d, want 2", generation)
	}
	if filepath.Dir(path) != filepath.Join(dir, sessionsSubdir) {
		t.Fatalf("counter %s is not beside the session's meta", path)
	}
}

func TestNextBootGenerationRefusesACorruptCounter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, sessionsSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BootGenerationPath(dir, "S1"), []byte("not a number"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NextBootGeneration(dir, "S1", 0); err == nil {
		t.Fatal("a corrupt counter was accepted")
	}
}

func TestNextBootGenerationRejectsUnsafeSessionIDs(t *testing.T) {
	if _, err := NextBootGeneration(t.TempDir(), "../escape", 0); err == nil {
		t.Fatal("an unsafe session id was accepted")
	}
}

// TestNextBootGenerationStartsAboveTheFloor: a session that takes over a
// served ref (thread/clear) must serve above the generation clients already
// hold for it, or they would ignore it as a lower one.
func TestNextBootGenerationStartsAboveTheFloor(t *testing.T) {
	dir := t.TempDir()
	generation, err := NextBootGeneration(dir, "S1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if generation != 6 {
		t.Fatalf("generation above floor 5 = %d, want 6", generation)
	}
	// A counter already past the floor keeps counting from itself.
	generation, err = NextBootGeneration(dir, "S1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if generation != 7 {
		t.Fatalf("generation = %d, want 7", generation)
	}
}
