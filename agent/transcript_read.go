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

// ResumeHistory extracts the history needed for session resume from transcript entries.
// If a compaction turn (CHECKPOINT or SUMMARY) exists, returns [last compaction turn, ...subsequent turns].
// Otherwise returns all turns except the replay copies.
//
// A fold re-appends the PERSISTED forms of the pairs recorded during it just
// ahead of its compaction markers, stamped ContextReplay and tagged with the
// fold id every record of that fold carries, so the last-marker anchor does
// not drop them (publishFoldTransaction's tail rewrite). One attentionMu hold
// writes the whole run, so it is contiguous on disk and the anchored branch
// below can reassemble it from the anchor outward. Which side of the anchor
// those copies are the record of decides what resume must do with them:
//
//   - With an anchor, everything before it is discarded, so the copies are the
//     ONLY record of those turns and are kept.
//   - With no anchor nothing is discarded, so each copy sits in the same list
//     as the original it was copied from, and keeping both would replay that
//     stretch of conversation — tool calls and their results included — to the
//     model twice. They are dropped.
//
// Nothing returned carries the flag: the anchored branch clears it on the
// copies it keeps, and the no-anchor branch keeps only entries that never had
// it. Either way it describes a durable transcript entry's role, not a turn in
// the resumed session's history.
//
// The anchored branch rebuilds the fold's run in the order the fold HELD it,
// which is not the order it wrote it: the anchor and the rest of its records,
// the copies of the suffix the fold preserved, the steering it injected, then
// the copies of the turns recorded while the fold ran — which the publication
// merges in after everything the fold itself produced, though the transaction
// writes every copy first and the steering last.
func ResumeHistory(entries []transcript.Entry) []schema.Turn {
	// Scan backward for the last compaction turn.
	compactionIdx := -1
	for i := range slices.Backward(entries) {
		kind := entries[i].Turn.Kind
		if kind == schema.TurnCheckpoint || kind == schema.TurnSummary {
			compactionIdx = i
			break
		}
	}

	if compactionIdx < 0 {
		// No anchor: every original is still here, so a copy of one is a
		// duplicate rather than the only record of it.
		turns := make([]schema.Turn, 0, len(entries))
		for _, e := range entries {
			if e.Turn.ContextReplay {
				continue
			}
			turns = append(turns, resumedTurn(e.Turn))
		}
		repaired, _ := repairOrphanedToolResults(turns)
		return repaired
	}

	// Return the compaction turn + everything after it, plus the replay copies
	// this anchor's own fold wrote just before it. The fold's footprint is one
	// contiguous run of records sharing its id — copies first, then the
	// context-compaction records, markers and injected steering — so the run
	// is reassembled in the order the fold PUBLISHED it, which is not the
	// order it wrote it: the anchor and its records, the copies of the suffix
	// the fold preserved, the steering the fold injected, then the copies of
	// the turns recorded while it ran. That is the order the fold held in
	// memory, so a resume rebuilds the history the live session had.
	//
	// An untagged anchor is one written before the tag existed, when the
	// copies followed their marker and are already inside the range below;
	// foldRun returns the anchor alone, and this is exactly the old behaviour.
	foldStart, foldEnd := foldRun(entries, compactionIdx)
	result := make([]schema.Turn, 0, len(entries)-foldStart)
	for i := compactionIdx; i < foldEnd; i++ {
		// A CONTEXT_COMPACTION record is a record OF the fold, not a turn the
		// fold published: the history it describes is the one this anchor
		// already represents. Skipping it by kind keeps the resumed history
		// independent of where in its run the fold wrote it — the fold writes
		// those records after its markers so it can withhold them with the
		// anchor, and before the markers it never reached this loop at all.
		if entries[i].Turn.ContextReplay ||
			entries[i].Turn.Kind == schema.TurnSteering ||
			entries[i].Turn.Kind == schema.TurnContextCompaction {
			continue
		}
		result = append(result, resumedTurn(entries[i].Turn))
	}
	appendCopies := func(mergedTail bool) {
		for i := foldStart; i < foldEnd; i++ {
			if !entries[i].Turn.ContextReplay || entries[i].Turn.ContextReplayMergedTail != mergedTail {
				continue
			}
			result = append(result, resumedTurn(entries[i].Turn))
		}
	}
	// The published history is the fold's own result — its marker, the suffix
	// it preserved, then the steering it injected — with the turns recorded
	// while it ran merged in after all of that. The run on disk is written in
	// a different order (copies first, so a crash cannot leave an anchor with
	// nothing to replace what it discards), so the two copy groups go back
	// around the steering rather than in file order.
	appendCopies(false)
	for i := compactionIdx; i < foldEnd; i++ {
		if entries[i].Turn.ContextReplay || entries[i].Turn.Kind != schema.TurnSteering {
			continue
		}
		result = append(result, resumedTurn(entries[i].Turn))
	}
	appendCopies(true)
	anchorFold := entries[compactionIdx].Turn.CompactionFoldID
	for i := foldEnd; i < len(entries); i++ {
		// A copy claimed by some OTHER fold is a copy whose marker never
		// arrived — a later fold that wrote its tail and then crashed. Its
		// originals are after this anchor too, so it is a duplicate for the
		// same reason the no-anchor branch's are. Untagged copies are the old
		// ordering's, written after their own marker and inside this range on
		// purpose, so they stay.
		if entries[i].Turn.ContextReplay && entries[i].Turn.CompactionFoldID != "" && entries[i].Turn.CompactionFoldID != anchorFold {
			continue
		}
		result = append(result, resumedTurn(entries[i].Turn))
	}
	repaired, _ := repairOrphanedToolResults(result)
	return repaired
}

// resumedTurn strips the roles a record carries in the FILE from the turn a
// session gets back. ContextReplay and its merge-back mark say which half of a
// fold's run an entry was; CompactionFoldID says which fold wrote it. Neither
// is true of a turn in a running session's history, and leaving them on makes
// a history written down again — a fork child's inherited prefix — replay as
// a fold run that is not there.
func resumedTurn(turn schema.Turn) schema.Turn {
	turn.ContextReplay = false
	turn.ContextReplayMergedTail = false
	turn.CompactionFoldID = ""
	return turn
}

// foldRun reports the half-open span of entries written by the fold that owns
// the record at anchor. An untagged anchor owns only itself: records from
// before the tag existed carry no id to group by, and their copies already
// follow the marker rather than preceding it.
func foldRun(entries []transcript.Entry, anchor int) (start, end int) {
	foldID := entries[anchor].Turn.CompactionFoldID
	if foldID == "" {
		return anchor, anchor + 1
	}
	start, end = anchor, anchor+1
	for start > 0 && entries[start-1].Turn.CompactionFoldID == foldID {
		start--
	}
	for end < len(entries) && entries[end].Turn.CompactionFoldID == foldID {
		end++
	}
	return start, end
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
