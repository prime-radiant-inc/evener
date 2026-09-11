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
// Otherwise returns all turns.
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
		// No compaction: return all turns.
		turns := make([]schema.Turn, len(entries))
		for i, e := range entries {
			turns[i] = e.Turn
		}
		repaired, _ := repairOrphanedToolResults(turns)
		return repaired
	}

	// Return compaction turn + everything after it.
	result := make([]schema.Turn, 0, len(entries)-compactionIdx)
	for i := compactionIdx; i < len(entries); i++ {
		result = append(result, entries[i].Turn)
	}
	repaired, _ := repairOrphanedToolResults(result)
	return repaired
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
// checkpoint phase), with the final publication's handoff last.
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
			snapshot.PendingSelection = nil
		case skillCompactionReceiptPublished:
			// The winning publication claimed the operation but delivery did
			// not complete before the crash: it resumes delivery-only, and
			// its selection was consumed by the publication that claimed it.
			snapshot.PendingCompaction.Phase = skillCompactionPhasePublished
			snapshot.PendingCompaction.PublicationID = receipt.Operation.PublicationID
			snapshot.PendingSelection = nil
		case skillCompactionReceiptCancelled:
			// The retirement predates any metadata write that could have
			// recorded it: redo it, and only for its own generation.
			snapshot.PendingCompaction = nil
			snapshot.PendingSelection = nil
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
