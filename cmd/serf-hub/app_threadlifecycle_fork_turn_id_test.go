package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// TestParseSourceTurnIDRejectsIDsThatAreNotTranscriptEntryIndexes (kata rk09)
// pins the stance thread/fork takes on a turn id that is not an entry index.
//
// parseSourceTurnID's result is handed straight to agent.ForkSessionAtUserTurn
// as a divergence position — a 1-based index into the parent transcript's
// entries. Only ids the transcript's own entry-index numbering produced mean
// anything there. A reserved client-mutation id (appwire.ClientMutationTurnID)
// and the synthetic prelude id are numbered by something else entirely, so
// there is no entry they name; refusing is the only answer that cannot cut a
// child session at a position the user never pointed at.
//
// The refusal today falls out of the numeric parse failing. Do not "fix" a
// failing fork by making this lenient — stripping the reserved namespace's
// marker would resolve turn_m7 to entry 7, which is an unrelated entry in
// every session where the two counters have diverged (which is all of them
// past the first turn or two).
func TestParseSourceTurnIDRejectsIDsThatAreNotTranscriptEntryIndexes(t *testing.T) {
	for _, raw := range []string{
		appwire.ClientMutationTurnID(1),
		appwire.ClientMutationTurnID(7),
		appwire.SystemPreludeTurnID,
	} {
		turn, err := parseSourceTurnID(raw)
		if err == nil {
			t.Fatalf("parseSourceTurnID(%q) = %d with no error; it names no transcript entry, so forking on it cuts the child at an unrelated position", raw, turn)
		}
		if !strings.Contains(err.Error(), "sourceTurnId") {
			t.Fatalf("parseSourceTurnID(%q) error = %q, want it to name the offending parameter", raw, err)
		}
	}

	// The ids the transcript's entry-index numbering does produce still parse,
	// in both the prefixed and bare forms the wire allows.
	for _, tc := range []struct {
		raw  string
		want int
	}{{"turn_4", 4}, {"4", 4}} {
		turn, err := parseSourceTurnID(tc.raw)
		if err != nil || turn != tc.want {
			t.Fatalf("parseSourceTurnID(%q) = (%d, %v), want (%d, nil)", tc.raw, turn, err, tc.want)
		}
	}
}

// TestHubRPCThreadForkAtBareEntryIndexCutsThatEntry (kata 0jhh) pins the whole
// path a client's transcript entry index travels: sourceTurnId "3" must reach
// agent.ForkSessionAtUserTurn as divergence position 3 and cut the child at
// the THIRD transcript entry — not at whatever entry a turn id happens to
// number.
//
// The distinction is invisible on a transcript replayed from disk, where
// internal/apptranscript numbers turn_N off the entry index itself, and that
// coincidence is why sending a turn id looked correct for years. It breaks the
// moment a turn is minted live: internal/appprojector's startTurn counts TURNS
// (one per user input, goal continuation, …) while the entry index counts
// ENTRIES (user, assistant, checkpoint, …), so the second user message of this
// parent is live turn_2 but transcript entry 3. Both numbers are pinned below
// against the same parent so a future change cannot quietly swap one for the
// other.
func TestHubRPCThreadForkAtBareEntryIndexCutsThatEntry(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
	// Three entries: 1 = USER "first task", 2 = ASSISTANT "first reply",
	// 3 = USER "second task".
	parentID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "local:" + parentID,
		SourceTurnID: "3",
		DeferInput:   true,
	})
	if err != nil {
		t.Fatalf("ThreadFork(sourceTurnId=3): %v", err)
	}
	// The entry at index 3 is the one that got cut: its text comes back for the
	// composer instead of being replayed into the child.
	if resp.OriginalInput != "second task" {
		t.Fatalf("originalInput=%q, want %q — the fork cut a different entry than the one named", resp.OriginalInput, "second task")
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.DivergenceTurn != 3 {
		t.Fatalf("child DivergenceTurn=%d, want 3", childMeta.DivergenceTurn)
	}
	if childMeta.ParentSessionID != parentID {
		t.Fatalf("child ParentSessionID=%q, want %q", childMeta.ParentSessionID, parentID)
	}

	// The live turn id for that very same user message is turn_2, and turn_2
	// names the ASSISTANT entry. Sending a live turn id where an entry index
	// belongs is what this parent proves cannot work.
	_, err = client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:          "local:" + parentID,
		SourceTurnID: "turn_2",
		DeferInput:   true,
	})
	if err == nil {
		t.Fatal("ThreadFork(sourceTurnId=turn_2) succeeded; entry 2 is the assistant reply, not the user message that turn owns")
	}
	if !strings.Contains(err.Error(), "not a USER_INPUT turn") {
		t.Fatalf("ThreadFork(sourceTurnId=turn_2) error = %v, want a not-a-user-turn refusal", err)
	}
}
