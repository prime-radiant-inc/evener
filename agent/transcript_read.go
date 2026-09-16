package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

const transcriptJSONLMaxLineBytes = 128 << 20

var openTranscriptFile = func(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

// readTranscript reads a semantic transcript-v2 JSONL file. Only an incomplete
// final line is skipped; corrupt complete lines and unsupported record kinds
// reject the whole file.
func readTranscript(path string) (transcript.Header, []transcript.Entry, int, error) {
	data, err := readSemanticTranscript(path, transcriptJSONLMaxLineBytes, true, false, nil)
	return data.Header, data.Entries, data.Skipped, err
}

// transcriptData holds all parsed content from a transcript JSONL file.
type transcriptData struct {
	Header     transcript.Header
	Entries    []transcript.Entry
	EntryLines [][]byte
	Skipped    int
}

var (
	errStrictChildTranscriptCorrupt         = errors.New("corrupt_child_transcript")
	errStrictChildTranscriptSessionMismatch = errors.New("transcript_session_mismatch")
)

// readTranscriptFull reads the full semantic transcript-v2 file.
func readTranscriptFull(path string) (transcriptData, error) {
	return readSemanticTranscript(path, transcriptJSONLMaxLineBytes, true, false, nil)
}

func readTranscriptFullWithEntryLines(path string) (transcriptData, error) {
	return readSemanticTranscript(path, transcriptJSONLMaxLineBytes, true, true, nil)
}

func readStrictChildTranscript(path, expectedSessionID string, maxLineBytes int) (transcriptData, error) {
	return readStrictChildTranscriptWithOptions(path, expectedSessionID, true, maxLineBytes)
}

func validateStrictChildTranscript(path, expectedSessionID string, maxLineBytes int) (transcript.Header, error) {
	data, err := readStrictChildTranscriptWithOptions(path, expectedSessionID, false, maxLineBytes)
	return data.Header, err
}

func readStrictChildTranscriptWithOptions(path, expectedSessionID string, retainLines bool, maxLineBytes int) (transcriptData, error) {
	data, err := readSemanticTranscript(path, maxLineBytes, retainLines, false, errStrictChildTranscriptCorrupt)
	if err != nil {
		return transcriptData{}, err
	}
	if data.Header.SessionID != expectedSessionID {
		return transcriptData{}, fmt.Errorf("%w: header session %q does not match %q", errStrictChildTranscriptSessionMismatch, data.Header.SessionID, expectedSessionID)
	}
	return data, nil
}

func readSemanticTranscript(path string, maxLineBytes int, retainEntries, retainEntryLines bool, corruptSentinel error) (transcriptData, error) {
	f, err := openTranscriptFile(path)
	if err != nil {
		return transcriptData{}, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	reader := bufio.NewReaderSize(f, 64*1024)
	var data transcriptData
	headerRead := false
	for {
		line, complete, bytesRead, readErr := transcript.ReadLine(reader, maxLineBytes)
		if readErr != nil {
			return transcriptData{}, wrapTranscriptCorrupt(corruptSentinel, "reading transcript", readErr)
		}
		if !complete {
			if bytesRead > 0 {
				data.Skipped++
			}
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			header, err := transcript.DecodeHeader(line)
			if err != nil {
				if errors.Is(err, transcript.ErrUnsupportedFormat) {
					return transcriptData{}, err
				}
				return transcriptData{}, wrapTranscriptCorrupt(corruptSentinel, "parsing transcript header", err)
			}
			data.Header = header
			headerRead = true
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			if errors.Is(err, transcript.ErrUnsupportedFormat) {
				return transcriptData{}, err
			}
			operation := "parsing transcript entry"
			if errors.Is(err, transcript.ErrInvalidRecordBoundary) {
				operation = "parsing transcript line"
			}
			return transcriptData{}, wrapTranscriptCorrupt(corruptSentinel, operation, err)
		}
		if retainEntries {
			data.Entries = append(data.Entries, entry)
		}
		if retainEntryLines {
			data.EntryLines = append(data.EntryLines, bytes.Clone(line))
		}
	}
	if !headerRead {
		if corruptSentinel != nil {
			return transcriptData{}, fmt.Errorf("%w: transcript file is empty", corruptSentinel)
		}
		return transcriptData{}, errors.New("transcript file is empty: no header")
	}
	return data, nil
}

func wrapTranscriptCorrupt(sentinel error, operation string, err error) error {
	if sentinel != nil {
		return fmt.Errorf("%w: %s: %w", sentinel, operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// lastMarkerAnchor is the index of the first entry a pre-#1200 (fold-record
// absent) resume keeps: the last compaction turn, or 0 when the transcript
// never compacted. It is the legacy anchor, kept only for transcripts written
// before the fold record existed — those carry a marker with no record, and
// resuming them exactly as the shipped daemon did (marker + everything after)
// avoids suddenly replaying their long-discarded prefix on the first restart
// after the upgrade. A2 transcripts carry a fold record and never reach it.
func lastMarkerAnchor(entries []transcript.Entry) int {
	for i := range slices.Backward(entries) {
		kind := entries[i].Turn.Kind
		if kind == schema.TurnCheckpoint || kind == schema.TurnSummary {
			return i
		}
	}
	return 0
}

// lastFoldRecordIndex is the index of the last TurnFoldRecord entry, or -1 when
// the transcript has none (never compacted, or compacted only by a pre-#1200
// build). The last record is the one whose published history is live.
func lastFoldRecordIndex(entries []transcript.Entry) int {
	for i := range slices.Backward(entries) {
		if entries[i].Turn.Kind == schema.TurnFoldRecord && entries[i].Turn.Fold != nil {
			return i
		}
	}
	return -1
}

// resumeTurns reconstructs the pre-repair resumed history and, for each turn,
// the index in entries it came from (its origin), so a caller can map a
// full-transcript position onto the resumed history. A fold record drives the
// reconstruction when present: the head marker(s) it names (Layers), then the
// pre-existing turns it kept (RetainedSeqs), each looked up by Seq, then every
// entry recorded after the record. Absent a record, the legacy last-marker
// anchor (or the whole transcript) does, contiguously from lastMarkerAnchor.
func resumeTurns(entries []transcript.Entry) (turns []schema.Turn, origins []int) {
	appendEntry := func(i int) {
		t := entries[i].Turn
		t.Seq = entries[i].Seq // seed the durable per-line id the fold names retained turns by
		turns = append(turns, t)
		origins = append(origins, i)
	}
	// A fold record is authoritative only when it is NEWER than the newest
	// compaction marker. If a later fold wrote its markers but its own record
	// write failed (the "restart resumes from the newest compaction marker"
	// warning), the last surviving record belongs to an OLDER fold; using it
	// would replay that fold's summarized-away turns plus the newer markers.
	// Treat such a stale record as absent and fall back to the last-marker
	// anchor (newest summary + everything after it) — a valid fold always
	// writes its record AFTER its own markers, so recordIdx > lastMarkerAnchor
	// exactly when the record is the newest fold's.
	if recordIdx := lastFoldRecordIndex(entries); recordIdx >= 0 && recordIdx > lastMarkerAnchor(entries) {
		rec := entries[recordIdx].Turn.Fold
		bySeq := make(map[int]int, len(entries))
		for i := range entries {
			bySeq[entries[i].Seq] = i
		}
		add := func(seq int) {
			// A negative sentinel (NoTranscriptEntrySeq) misses bySeq, and a
			// fold record must never name another fold record — skip both, so a
			// seq that a write bug pointed at the FOLD_RECORD entry (e.g. an
			// unspent seq reused after a failed marker write) can never inject
			// the record itself into history.
			if i, ok := bySeq[seq]; ok && entries[i].Turn.Kind != schema.TurnFoldRecord {
				appendEntry(i)
			}
		}
		for _, seq := range rec.Layers {
			add(seq)
		}
		for _, seq := range rec.RetainedSeqs {
			add(seq)
		}
		for i := recordIdx + 1; i < len(entries); i++ {
			// Everything after the last record is live post-fold history; a
			// stray fold record there (there should be none) is bookkeeping,
			// never a live turn.
			if entries[i].Turn.Kind == schema.TurnFoldRecord {
				continue
			}
			appendEntry(i)
		}
		return turns, origins
	}

	from := lastMarkerAnchor(entries)
	turns = make([]schema.Turn, 0, len(entries)-from)
	for i := from; i < len(entries); i++ {
		appendEntry(i)
	}
	return turns, origins
}

// resumeHistoryIndexed is ResumeHistory, also reporting each synthetic repair
// turn's insertion index in the returned turns (ascending, post-repair
// coordinates), so a caller mapping a transcript position onto the resumed
// history can shift it per insertion at or before that position, exactly as
// history_repair.go shifts the in-flight boundary.
func resumeHistoryIndexed(entries []transcript.Entry) ([]schema.Turn, []int) {
	repaired, insertedAt, _ := resumeHistoryReconstruct(entries)
	return repaired, insertedAt
}

// resumeHistoryReconstruct also returns the pre-repair origins (the entry index
// each pre-repair turn came from), which the fork-provenance boundary maps a
// persisted DivergenceTurn through — see RestoreSessionFromMetaWithConfig.
func resumeHistoryReconstruct(entries []transcript.Entry) (repaired []schema.Turn, insertedAt []int, origins []int) {
	turns, origins := resumeTurns(entries)
	repaired, _, insertedAt = repairOrphanedToolResultsIndexed(turns)
	return repaired, insertedAt, origins
}

// ResumeHistory extracts the history needed for session resume from transcript
// entries. When a compaction fold record exists, it rebuilds the exact history
// that fold left live: the marker(s) it names, the pre-existing turns it kept
// (by Seq), then everything recorded after the record. Absent a record, a
// legacy marker anchor returns [last compaction turn, ...subsequent], and an
// uncompacted transcript returns all turns.
func ResumeHistory(entries []transcript.Entry) []schema.Turn {
	turns, _ := resumeHistoryIndexed(entries)
	return turns
}

// reconcileSkillCompactionReceipts replays the typed compaction handoff
// receipts found in ALL decoded transcript entries into a persisted
// lifecycle snapshot that may be staler than the transcript (a crash or
// failed save between a winning publication and its metadata write). A
// restart must call this BEFORE ResumeHistory seeds the session's history:
// the receipts live on pre-marker entries too, which the resume anchor would
// otherwise discard along with every older turn.
//
// Only receipts originating from this session (SessionID match) and NEWER
// than the snapshot (Revision greater than the snapshot's) are applied: the
// snapshot already covers anything at or below its own revision, and
// re-applying a covered receipt could repeat a delivered operation or attach
// a retired selection to another fold. Application is generation-matched —
// a receipt only advances the operation with its own generation — and never
// rebuilds inventory or obligations from the receipt's captured selection,
// so concurrent inventory additions absent from that selection survive.
// Handoffs coalesce by publication identity, preserving each winning
// publication's final handoff (the summary phase that followed its
// checkpoint phase), with the final publication's handoff last. After the
// receipt pass, a slot still in the published phase is the live
// claim→delivery-flip window artifact (R19) and completes unconditionally.
// Durable reload-reminder turns are reconciled the same way: the reminder turn
// IS its handoff's admission, so a handoff whose reminder already landed must
// be consumed here rather than delivered a second time after the restart.
//
// Durable delivery-notification turns are reconciled here too, for the same
// reason (see reconcileSkillDeliveryNotifications): prepareSkillDelivery
// records its notification before it mutates or removes the matching
// obligation, so a failed metadata save leaves the snapshot holding a
// transition the transcript already committed.
func reconcileSkillCompactionReceipts(entries []transcript.Entry, snapshot *schema.SkillLifecycleSnapshot, sessionID string) {
	if snapshot == nil {
		return
	}
	for _, entry := range entries {
		state := entry.Turn.SkillState
		if state == nil || state.Compaction == nil {
			continue
		}
		receipt := *state.Compaction
		if receipt.SessionID != sessionID {
			continue // only this session's own receipts reconcile into its snapshot
		}
		if receipt.Revision <= snapshot.Revision {
			continue // the snapshot already covers this receipt's lifecycle revision
		}
		applySkillCompactionReceipt(snapshot, receipt)
	}
	// R19 slot-level completion: a persisted slot still in the published
	// phase can ONLY be the live claim→delivery-flip window artifact — the
	// live transaction always clears the slot before its own save, but a
	// concurrent metadata save inside that window persists the slot at the
	// receipt's OWN revision, so the revision-gated pass above can never
	// repair it and a crash there would wedge the cycle forever. Complete
	// it unconditionally, mirroring the live delivery flip: the slot and
	// its selection clear, and the publication's coalesced handoff
	// advances to delivered. Generation-safe by construction (the slot
	// carries its own publication identity); no transcript scan, no new
	// lock or transaction.
	if op := snapshot.PendingCompaction; op != nil && op.Phase == skillCompactionPhasePublished {
		snapshot.PendingCompaction = nil
		for i := range snapshot.PendingHandoffs {
			if snapshot.PendingHandoffs[i].Operation.PublicationID == op.PublicationID {
				snapshot.PendingHandoffs[i].Phase = skillCompactionReceiptDelivered
			}
		}
	}
	// A durable ReloadReminder turn is the handoff's admission: the live path
	// removes the handoff right after that turn's transcript write, so a crash
	// or failed save in between leaves a snapshot whose handoff would repeat the
	// reminder on the next prepare. The durable turn is the authority, so
	// consume the handoff it names. The live consumption removes by publication
	// identity, and a publication's identity is never reused, so this can only
	// retire the handoff that reminder already satisfied.
	deliveredReminders := map[string]bool{}
	for _, entry := range entries {
		if state := entry.Turn.SkillState; state != nil && state.ReloadReminder != nil {
			if id := state.ReloadReminder.PublicationID; id != "" {
				deliveredReminders[id] = true
			}
		}
	}
	if len(deliveredReminders) != 0 {
		kept := snapshot.PendingHandoffs[:0]
		for _, handoff := range snapshot.PendingHandoffs {
			if deliveredReminders[handoff.Operation.PublicationID] {
				continue
			}
			kept = append(kept, handoff)
		}
		snapshot.PendingHandoffs = kept
	}
	reconcileSkillDeliveryNotifications(entries, snapshot, sessionID)
}

// reconcileSkillDeliveryNotifications applies the typed delivery outcomes the
// durable transcript already records to the snapshot's pending obligations. It
// is the delivery half of the same crash / failed-save reconciliation as the
// compaction receipts above: prepareSkillDelivery appends its notification turn
// through the durable transcript door BEFORE it finalizes or identity-corrects
// the matching obligation in the live snapshot, so a snapshot staler than the
// transcript must adopt the transition the durable turn records. A failed
// outcome finalizes its obligation — the explanation turn is its delivery — and
// a delivered outcome adopts the corrected identity the turn carries, so the
// next dispatch revalidates against the bytes it actually delivered instead of
// re-deriving a second change notice. Without this a restart re-processed the
// same missing or changed skill and appended the same notification twice.
//
// Outcomes from another session are ignored. Invocation identities are unique
// per invocation within a session, so a matching outcome can only describe the
// same obligation; every unrelated pending obligation is left untouched. The
// LAST outcome recorded for an identity wins, because the transcript is
// chronological: a later carrier (a retry that finally reloaded the repaired
// source) supersedes an earlier failure notice for the same invocation, while
// an earlier pending carrier never erases the notification that follows it.
func reconcileSkillDeliveryNotifications(entries []transcript.Entry, snapshot *schema.SkillLifecycleSnapshot, sessionID string) {
	if snapshot == nil || len(snapshot.Obligations) == 0 {
		return
	}
	byInvocation := make(map[string]schema.SkillActivationOutcome)
	for _, entry := range entries {
		state := entry.Turn.SkillState
		if state == nil {
			continue
		}
		for _, outcome := range state.Outcomes {
			if outcome.InvocationID == "" {
				continue
			}
			if outcome.SessionID != "" && outcome.SessionID != sessionID {
				continue
			}
			byInvocation[outcome.InvocationID] = outcome
		}
	}
	if len(byInvocation) == 0 {
		return
	}
	kept := snapshot.Obligations[:0]
	for _, obligation := range snapshot.Obligations {
		outcome, ok := byInvocation[obligation.InvocationID]
		if !ok {
			kept = append(kept, obligation)
			continue
		}
		switch outcome.Status {
		case "failed":
			continue
		case "delivered":
			if outcome.Identity.Name != "" {
				obligation.Identity = outcome.Identity
			}
		}
		kept = append(kept, obligation)
	}
	snapshot.Obligations = kept
}

// applySkillCompactionReceipt advances a stale snapshot by one newer typed
// receipt, generation-matched so it can never attach a cancelled or claimed
// operation's outcome to a different fold's intent.
func applySkillCompactionReceipt(snapshot *schema.SkillLifecycleSnapshot, receipt schema.SkillCompactionReceipt) {
	snapshot.Revision = receipt.Revision
	if receipt.Operation.Generation != 0 && snapshot.PendingCompaction != nil &&
		snapshot.PendingCompaction.Generation == receipt.Operation.Generation {
		switch receipt.Phase {
		case skillCompactionReceiptDelivered:
			// The handoff completed: the operation must not be repeated —
			// clear the cycle's slot and its consumed selection.
			snapshot.PendingCompaction = nil
		case skillCompactionReceiptPublished:
			// The winning publication claimed the operation. The live
			// transaction defines delivery-complete as the slot cleared, the
			// selection consumed, and that publication's coalesced handoff
			// advanced to delivered (commitSkillCompactionPublication); a
			// crash before the delivery save must not change the
			// post-recovery state (R18), so reconciliation completes the
			// same delivery here. The receipt coalesces below as delivered.
			snapshot.PendingCompaction = nil
			receipt.Phase = skillCompactionReceiptDelivered
		case skillCompactionReceiptCancelled:
			// The retirement predates any metadata write that could have
			// recorded it: redo it, and only for its own generation.
			snapshot.PendingCompaction = nil
		}
	}
	// The handoff itself coalesces by publication identity, so the final
	// checkpoint/summary phase of each winning publication keeps exactly one
	// entry, the last one seen.
	receipt.Operation.Selection.Names = slices.Clone(receipt.Operation.Selection.Names)
	if id := receipt.Operation.PublicationID; id != "" {
		for i := range snapshot.PendingHandoffs {
			if snapshot.PendingHandoffs[i].Operation.PublicationID == id {
				snapshot.PendingHandoffs[i] = receipt
				return
			}
		}
	}
	snapshot.PendingHandoffs = append(snapshot.PendingHandoffs, receipt)
}
