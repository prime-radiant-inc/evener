package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
)

const transcriptJSONLMaxLineBytes = 128 << 20

var openTranscriptFile = func(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

// readTranscript reads a semantic transcript-v2 JSONL file. Only an incomplete
// final line is skipped; corrupt complete lines and unsupported record kinds
// reject the whole file.
func readTranscript(path string) (transcript.Header, []transcript.Entry, int, error) {
	data, err := readSemanticTranscript(path, transcriptJSONLMaxLineBytes, true, nil)
	return data.Header, data.Entries, data.Skipped, err
}

// transcriptData holds all parsed content from a transcript JSONL file.
type transcriptData struct {
	Header  transcript.Header
	Entries []transcript.Entry
	Skipped int
}

var (
	errStrictChildTranscriptCorrupt         = errors.New("corrupt_child_transcript")
	errStrictChildTranscriptSessionMismatch = errors.New("transcript_session_mismatch")
)

// readTranscriptFull reads the full semantic transcript-v2 file.
func readTranscriptFull(path string) (transcriptData, error) {
	return readSemanticTranscript(path, transcriptJSONLMaxLineBytes, true, nil)
}

func readStrictChildTranscript(path, expectedSessionID string, maxLineBytes int) (transcriptData, error) {
	return readStrictChildTranscriptWithOptions(path, expectedSessionID, true, maxLineBytes)
}

func validateStrictChildTranscript(path, expectedSessionID string, maxLineBytes int) (transcript.Header, error) {
	data, err := readStrictChildTranscriptWithOptions(path, expectedSessionID, false, maxLineBytes)
	return data.Header, err
}

func readStrictChildTranscriptWithOptions(path, expectedSessionID string, retainLines bool, maxLineBytes int) (transcriptData, error) {
	data, err := readSemanticTranscript(path, maxLineBytes, retainLines, errStrictChildTranscriptCorrupt)
	if err != nil {
		return transcriptData{}, err
	}
	if data.Header.SessionID != expectedSessionID {
		return transcriptData{}, fmt.Errorf("%w: header session %q does not match %q", errStrictChildTranscriptSessionMismatch, data.Header.SessionID, expectedSessionID)
	}
	return data, nil
}

func readSemanticTranscript(path string, maxLineBytes int, retainEntries bool, corruptSentinel error) (transcriptData, error) {
	f, err := openTranscriptFile(path)
	if err != nil {
		return transcriptData{}, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	reader := bufio.NewReaderSize(f, 64*1024)
	var data transcriptData
	headerRead := false
	for {
		line, readErr := readStrictTranscriptLine(reader, maxLineBytes)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return transcriptData{}, wrapTranscriptCorrupt(corruptSentinel, "reading transcript", readErr)
		}
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		finalIncomplete := errors.Is(readErr, io.EOF) && !bytes.HasSuffix(line, []byte{'\n'})
		if finalIncomplete {
			data.Skipped++
			break
		}
		line = bytes.TrimSpace(bytes.TrimSuffix(line, []byte{'\n'}))
		if len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if !headerRead {
			if err := json.Unmarshal(line, &data.Header); err != nil {
				return transcriptData{}, wrapTranscriptCorrupt(corruptSentinel, "parsing transcript header", err)
			}
			if err := transcript.ValidateHeader(data.Header); err != nil {
				return transcriptData{}, err
			}
			headerRead = true
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		var peek struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			return transcriptData{}, wrapTranscriptCorrupt(corruptSentinel, "parsing transcript line", err)
		}
		if err := transcript.ValidateRecordKind(peek.Kind); err != nil {
			return transcriptData{}, err
		}
		var entry transcript.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return transcriptData{}, wrapTranscriptCorrupt(corruptSentinel, "parsing transcript entry", err)
		}
		if retainEntries {
			data.Entries = append(data.Entries, entry)
		}
		if errors.Is(readErr, io.EOF) {
			break
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

func readStrictTranscriptLine(reader *bufio.Reader, maxLineBytes int) ([]byte, error) {
	if maxLineBytes <= 0 {
		maxLineBytes = transcriptJSONLMaxLineBytes
	}
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxLineBytes {
			return nil, fmt.Errorf("%w: transcript line exceeds %d bytes", errStrictChildTranscriptCorrupt, maxLineBytes)
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// ResumeHistory extracts the history needed for session resume from transcript entries.
// If a compaction turn (CHECKPOINT or SUMMARY) exists, returns [last compaction turn, ...subsequent turns].
// Otherwise returns all turns.
func ResumeHistory(entries []transcript.Entry) []schema.Turn {
	// Scan backward for the last compaction turn.
	compactionIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
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
