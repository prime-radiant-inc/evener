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
			turns = append(turns, e.Turn)
		}
		repaired, _ := repairOrphanedToolResults(turns)
		return repaired
	}

	// Return the compaction turn + everything after it, plus the replay copies
	// this anchor's own fold wrote just before it. The fold's footprint is one
	// contiguous run of records sharing its id — copies first, then the
	// context-compaction records, markers and injected steering — so the run
	// is reassembled in the order the fold published it: the fold's own
	// records from the anchor onward, then its copies, then everything after.
	// That is the order the fold held in memory, so a resume rebuilds the
	// history the live session had.
	//
	// An untagged anchor is one written before the tag existed, when the
	// copies followed their marker and are already inside the range below;
	// foldRun returns the anchor alone, and this is exactly the old behaviour.
	foldStart, foldEnd := foldRun(entries, compactionIdx)
	result := make([]schema.Turn, 0, len(entries)-foldStart)
	for i := compactionIdx; i < foldEnd; i++ {
		if entries[i].Turn.ContextReplay {
			continue
		}
		result = append(result, entries[i].Turn)
	}
	for i := foldStart; i < foldEnd; i++ {
		if !entries[i].Turn.ContextReplay {
			continue
		}
		turn := entries[i].Turn
		turn.ContextReplay = false
		result = append(result, turn)
	}
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
		turn := entries[i].Turn
		turn.ContextReplay = false
		result = append(result, turn)
	}
	repaired, _ := repairOrphanedToolResults(result)
	return repaired
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
