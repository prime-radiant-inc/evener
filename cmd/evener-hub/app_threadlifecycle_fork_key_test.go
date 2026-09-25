package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// TestParseSourceItemKeyRejectsMalformedKeys pins parseSourceItemKey's
// refusals that do not depend on reading the parent transcript: an empty
// key, a key that ParseItemKey cannot decode, and a header key (position
// {Entry: 0, ...}), which names no turn.
func TestParseSourceItemKeyRejectsMalformedKeys(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"turn_3", // the retired sourceTurnId spelling, not an item key
		"not-a-key",
		transcriptindex.ItemKey("turn_1", appwire.ThreadItemPosition{Entry: 0, Item: 0}), // header
	} {
		entry, err := parseSourceItemKey(raw)
		if err == nil {
			t.Fatalf("parseSourceItemKey(%q) = %d with no error, want a refusal", raw, entry)
		}
		if !strings.Contains(err.Error(), "sourceItemKey") {
			t.Fatalf("parseSourceItemKey(%q) error = %q, want it to name the offending parameter", raw, err)
		}
	}

	entry, err := parseSourceItemKey(transcriptindex.ItemKey("turn_4", appwire.ThreadItemPosition{Entry: 4, Item: 0}))
	if err != nil || entry != 4 {
		t.Fatalf("parseSourceItemKey(entry 4 key) = (%d, %v), want (4, nil)", entry, err)
	}
}

// TestHubRPCThreadForkAtItemKeyCutsThatEntry (the key-addressed twin of kata
// 0jhh) pins that thread/fork resolves sourceItemKey to the entry it names
// and cuts the child there, for a legacy transcript whose entries carry
// today's turn_<entryIndex> ids.
func TestHubRPCThreadForkAtItemKeyCutsThatEntry(t *testing.T) {
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

	thirdEntryKey := transcriptindex.ItemKey("turn_3", appwire.ThreadItemPosition{Entry: 3, Item: 0})
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: thirdEntryKey,
		DeferInput:    true,
	})
	if err != nil {
		t.Fatalf("ThreadFork(sourceItemKey=%q): %v", thirdEntryKey, err)
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

	// A key naming the ASSISTANT entry (entry 2) is refused: forking requires a
	// USER_INPUT entry.
	secondEntryKey := transcriptindex.ItemKey("turn_2", appwire.ThreadItemPosition{Entry: 2, Item: 0})
	_, err = client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: secondEntryKey,
		DeferInput:    true,
	})
	if err == nil {
		t.Fatal("ThreadFork(sourceItemKey naming entry 2) succeeded; entry 2 is the assistant reply, not a user message")
	}
	if !strings.Contains(err.Error(), "not a USER_INPUT turn") {
		t.Fatalf("ThreadFork(sourceItemKey naming entry 2) error = %v, want a not-a-user-turn refusal", err)
	}

	// A key whose ordinal names no entry in the parent transcript is refused.
	unknownEntryKey := transcriptindex.ItemKey("turn_99", appwire.ThreadItemPosition{Entry: 99, Item: 0})
	_, err = client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: unknownEntryKey,
		DeferInput:    true,
	})
	if err == nil {
		t.Fatal("ThreadFork(sourceItemKey naming entry 99) succeeded; the parent transcript has only 3 entries")
	}

	// The header key names no turn at all.
	headerKey := transcriptindex.ItemKey("turn_1", appwire.ThreadItemPosition{Entry: 0, Item: 0})
	_, err = client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: headerKey,
		DeferInput:    true,
	})
	if err == nil {
		t.Fatal("ThreadFork(sourceItemKey=header key) succeeded; the header names no turn")
	}
}

// TestHubRPCThreadForkAtItemKeyCutsNewFormatEntry is the new-format twin of
// TestHubRPCThreadForkAtItemKeyCutsThatEntry: entries carry the phase-2
// identity fields (schema.TurnFormatIdentity, a minted TurnID) instead of
// today's legacy turn_<entryIndex> numbering, and the fork still resolves the
// item key to the same 1-based entry ordinal.
func TestHubRPCThreadForkAtItemKeyCutsNewFormatEntry(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000001")
	parentID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewFormatForkParentSession(t, stateDir, parentID); err != nil {
		t.Fatal(err)
	}
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

	// Entry 3 (ordinal 2) is the second USER_INPUT entry, minted under its own
	// new-format turn id "t_second" rather than a legacy turn_<entryIndex>.
	thirdEntryKey := transcriptindex.ItemKey("t_second", appwire.ThreadItemPosition{Entry: 3, Item: 0})
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: thirdEntryKey,
		DeferInput:    true,
	})
	if err != nil {
		t.Fatalf("ThreadFork(sourceItemKey=%q): %v", thirdEntryKey, err)
	}
	if resp.OriginalInput != "second task" {
		t.Fatalf("originalInput=%q, want %q", resp.OriginalInput, "second task")
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.DivergenceTurn != 3 {
		t.Fatalf("child DivergenceTurn=%d, want 3", childMeta.DivergenceTurn)
	}
}

// writeNewFormatForkParentSession writes a three-entry transcript whose
// entries carry the phase-2 identity fields, mirroring
// buildRPCSessionWithWorkingDir's legacy three-entry shape (USER, ASSISTANT,
// USER) so the two tests are directly comparable.
func writeNewFormatForkParentSession(t *testing.T, stateDir, parentID string) error {
	t.Helper()
	workingDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		return err
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl"), transcript.Header{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5",
		WorkingDir: workingDir,
	})
	if err != nil {
		return err
	}
	entries := []schema.Turn{
		withNewFormatIdentity(schema.NewTurn(schema.TurnUserInput, llm.User("first task")), "t_first"),
		withNewFormatIdentity(schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply")), "t_first"),
		withNewFormatIdentity(schema.NewTurn(schema.TurnUserInput, llm.User("second task")), "t_second"),
	}
	for _, turn := range entries {
		if err := writer.Append(turn); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             parentID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: workingDir},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		TurnCount:      2,
		OriginalPrompt: "second task",
	})
}

func withNewFormatIdentity(turn schema.Turn, turnID string) schema.Turn {
	turn.Format = schema.TurnFormatIdentity
	turn.TurnID = turnID
	return turn
}
