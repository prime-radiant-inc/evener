package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestTreeCounterReserveRelease verifies the atomic check-and-reserve logic:
// 16 reservations succeed, the 17th fails, releasing one allows another to succeed.
//
// Red today: type treeCounter does not exist.
func TestTreeCounterReserveRelease(t *testing.T) {
	c := newTreeCounter()

	// Reserve up to cap (16) — all must succeed.
	for i := range 16 {
		if !c.reserve() {
			t.Fatalf("reserve %d: expected true (under cap), got false", i+1)
		}
	}

	// 17th reservation must fail — at cap.
	if c.reserve() {
		t.Fatal("reserve 17: expected false (at cap), got true")
	}

	// Release one slot.
	c.release()

	// Now one reservation should succeed again.
	if !c.reserve() {
		t.Fatal("reserve after release: expected true, got false")
	}
}

// TestTreeCounterSharedAcrossTree verifies that a child session's treeCounter
// is the SAME pointer as the one threaded through the root's spawnConfig.
// Reservations made on the root counter are visible via the child session's
// counter because they share a pointer.
//
// Approach: construct a root session (parentSessionID == "") and a child
// session (parentSessionID set), both built via NewSession. The root mints a
// fresh treeCounter; the child inherits the pointer via cfg.spawn.treeCounter.
// Assert both sessions hold the same pointer.
//
// Red today: treeCounter field does not exist on Session or spawnConfig.
func TestTreeCounterSharedAcrossTree(t *testing.T) {
	stateDir := t.TempDir()
	workDir := t.TempDir()
	c := llm.NewClient()
	env := execenv.NewLocalExecutionEnvironment(workDir)

	// Build a root session — parentSessionID == "" triggers minting.
	rootCfg := SessionConfig{
		StateDir:         stateDir,
		NoProjectPrompts: true,
	}
	root, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, rootCfg)
	if err != nil {
		t.Fatalf("NewSession (root): %v", err)
	}
	defer root.Close()

	rootCounter := root.treeCounter
	if rootCounter == nil {
		t.Fatal("root session treeCounter is nil; expected a minted counter")
	}

	// Build a child session carrying the root's counter pointer.
	childStateDir := t.TempDir()
	childCfg := SessionConfig{
		StateDir:         childStateDir,
		NoProjectPrompts: true,
	}
	childCfg.spawn.parentSessionID = "root-session-id"
	childCfg.spawn.depth = 1
	childCfg.spawn.treeCounter = rootCounter

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, childCfg)
	if err != nil {
		t.Fatalf("NewSession (child): %v", err)
	}
	defer child.Close()

	// The child must hold the SAME pointer.
	if child.treeCounter != rootCounter {
		t.Fatalf("child treeCounter %p != root treeCounter %p; pointer not shared", child.treeCounter, rootCounter)
	}

	// Demonstrate shared state: reserve via root counter, verify child counter
	// (same pointer) reflects the reservation.
	if !rootCounter.reserve() {
		t.Fatal("root counter reserve: expected true")
	}
	// child.treeCounter IS rootCounter, so the count is already reflected.
	// Release to keep the counter balanced.
	child.treeCounter.release()
}
