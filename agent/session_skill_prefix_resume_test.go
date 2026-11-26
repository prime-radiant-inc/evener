package agent

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// TestRestoreReconcilesSkillStateRecordedBeforeTheCompactionAnchor pins the
// contract issue #6418's optimization must not break: a resume folds the
// transcript's PREFIX — every entry before the last compaction anchor — into
// durable state. Two of those folds are the skill lifecycle reconciliations,
// and they are the reason a resume cannot simply start decoding at the anchor.
//
// The fixture is shaped like a real crash: the transcript records a compaction
// receipt and a delivery outcome BEFORE the anchor, while the persisted
// metadata snapshot is staler than both (a crash or failed save between the
// durable turn and its metadata write). Restore must adopt the transcript's
// records:
//
//   - the published receipt completes the stale snapshot's pending handoff,
//     which a prefix-skipping read would leave pending and re-present;
//   - the failed delivery outcome finalizes the pending obligation, which a
//     prefix-skipping read would leave owed and deliver again.
//
// Both facts live strictly before the anchor, so this test fails the moment
// the resume scan stops decoding the prefix. It passes on the unmodified
// resume path, and that is the point: it is a guard, not a new behavior.
func TestRestoreReconcilesSkillStateRecordedBeforeTheCompactionAnchor(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	if err := os.MkdirAll(schema.SessionsDir(stateDir), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessionID := identifier.MustNewSessionID()

	writer, err := transcript.NewWriter(transcriptPath(stateDir, sessionID), transcript.Header{
		SessionID: sessionID, ProfileID: "openai", Model: "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}

	// The stale snapshot's pending handoff, matched by publication identity.
	const publicationID = "pub-prefix-1"
	const invocationID = "inv-prefix-1"

	// Prefix entry 0: the compaction handoff receipt. Phase "published" is the
	// live claim→delivery-flip window artifact reconcileSkillCompactionReceipts
	// completes; a skipped prefix leaves it outstanding.
	receiptTurn := schema.NewTurn(schema.TurnCheckpoint, llm.User("checkpoint carrying the handoff"))
	receiptTurn.SkillState = &schema.SkillTurnState{Compaction: &schema.SkillCompactionReceipt{
		Revision:  9,
		SessionID: sessionID,
		Phase:     skillCompactionPhasePublished,
		Operation: schema.SkillCompactionOperation{
			Generation:    3,
			Origin:        "automatic",
			Phase:         skillCompactionPhasePublished,
			PublicationID: publicationID,
		},
	}}
	if _, err := writer.Record(receiptTurn, transcript.RecordOptions{Door: transcript.DoorDurable}); err != nil {
		_ = writer.Close()
		t.Fatalf("append the pre-anchor compaction receipt: %v", err)
	}

	// Prefix entry 1: the delivery notification. prepareSkillDelivery writes
	// this turn before it mutates the obligation, so a stale snapshot still
	// holds an obligation the transcript has already finalized.
	deliveryTurn := schema.NewTurn(schema.TurnToolResults, llm.User("skill delivery failed"))
	deliveryTurn.SkillState = &schema.SkillTurnState{Outcomes: []schema.SkillActivationOutcome{{
		Revision:     10,
		SessionID:    sessionID,
		InvocationID: invocationID,
		Status:       "failed",
		ErrorCode:    "source_missing",
		Identity:     schema.SkillContentIdentity{Name: "opaque"},
	}}}
	if _, err := writer.Record(deliveryTurn, transcript.RecordOptions{Door: transcript.DoorDurable}); err != nil {
		_ = writer.Close()
		t.Fatalf("append the pre-anchor delivery outcome: %v", err)
	}

	// The anchor: everything above is prefix the resume would discard if it
	// started decoding here.
	anchor := schema.NewTurn(schema.TurnCheckpoint, llm.User("compaction anchor"))
	if _, err := writer.Record(anchor, transcript.RecordOptions{Door: transcript.DoorDurable}); err != nil {
		_ = writer.Close()
		t.Fatalf("append the compaction anchor: %v", err)
	}
	// A post-anchor turn, so the retained window is not empty.
	if _, err := writer.Record(schema.NewTurn(schema.TurnUserInput, llm.User("after the anchor")), transcript.RecordOptions{Door: transcript.DoorDurable}); err != nil {
		_ = writer.Close()
		t.Fatalf("append the post-anchor input: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	// The persisted snapshot is staler than the transcript: it still holds the
	// published operation and the obligation the transcript already settled.
	staleOperation := &schema.SkillCompactionOperation{
		Generation:    3,
		Origin:        "automatic",
		Phase:         skillCompactionPhasePublished,
		PublicationID: publicationID,
	}
	meta := schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5.2",
		Skills: &schema.SkillLifecycleSnapshot{
			Revision:          8,
			Inventory:         map[string]schema.SkillInventoryEntry{},
			PendingCompaction: staleOperation,
			PendingHandoffs: []schema.SkillCompactionReceipt{{
				Revision:  8,
				SessionID: sessionID,
				Phase:     skillCompactionPhasePublished,
				Operation: schema.SkillCompactionOperation{
					Generation:    3,
					Origin:        "automatic",
					Phase:         skillCompactionPhasePublished,
					PublicationID: publicationID,
				},
			}},
			Obligations: []schema.SkillDeliveryObligation{{
				InvocationID: invocationID,
				ToolCallID:   "call-prefix-1",
				Identity:     schema.SkillContentIdentity{Name: "opaque", FileDigest: "digest-before"},
				Route:        "model_tool",
			}},
		},
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save session metadata: %v", err)
	}

	restored, err := RestoreSessionFromMetaWithConfig(
		llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta,
		RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer restored.Close()

	// The published receipt must have advanced the snapshot's revision and
	// completed the operation: a prefix-skipping resume leaves it pending.
	restored.mu.Lock()
	lifecycle := restored.skillLifecycle.Clone()
	restored.mu.Unlock()

	if lifecycle.Revision < 9 {
		t.Fatalf("restored lifecycle revision = %d, want at least the pre-anchor receipt's 9; the prefix receipt was not reconciled", lifecycle.Revision)
	}
	if lifecycle.PendingCompaction != nil {
		t.Fatalf("restored PendingCompaction = %+v, want it completed by the pre-anchor published receipt", lifecycle.PendingCompaction)
	}
	if len(lifecycle.PendingHandoffs) != 1 {
		t.Fatalf("restored PendingHandoffs = %+v, want the one coalesced handoff", lifecycle.PendingHandoffs)
	}
	if got := lifecycle.PendingHandoffs[0].Phase; got != skillCompactionReceiptDelivered {
		t.Fatalf("restored handoff phase = %q, want %q: the pre-anchor published receipt completes delivery", got, skillCompactionReceiptDelivered)
	}

	// The failed delivery outcome must have finalized the obligation.
	if len(lifecycle.Obligations) != 0 {
		t.Fatalf("restored Obligations = %+v, want none: the pre-anchor failed outcome finalizes it", lifecycle.Obligations)
	}
}
