package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
)

const transcriptJSONLMaxLineBytes = 128 << 20

// readTranscript reads a transcript JSONL file, returning the header, all valid entries,
// and the count of skipped (corrupt/partial) lines. Callers can use the skipped count
// to decide whether to warn about data loss from crash recovery.
func readTranscript(path string) (transcript.Header, []transcript.Entry, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return transcript.Header{}, nil, 0, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is immaterial

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)

	// First line must be the header.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return transcript.Header{}, nil, 0, fmt.Errorf("reading transcript header: %w", err)
		}
		return transcript.Header{}, nil, 0, errors.New("transcript file is empty: no header")
	}

	var header transcript.Header
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return transcript.Header{}, nil, 0, fmt.Errorf("parsing transcript header: %w", err)
	}

	// Remaining lines are entries. Skip non-entry lines (e.g. api_call) and
	// any that fail to parse.
	var entries []transcript.Entry
	skipped := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry transcript.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip corrupt/partial lines (crash recovery).
			skipped++
			continue
		}
		if entry.Kind != "entry" {
			continue // skip non-entry lines (e.g. api_call)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return transcript.Header{}, nil, 0, fmt.Errorf("reading transcript: %w", err)
	}

	return header, entries, skipped, nil
}

// transcriptData holds all parsed content from a transcript JSONL file.
type transcriptData struct {
	Header   transcript.Header
	Entries  []transcript.Entry
	APICalls []transcript.APICall
	Skipped  int
}

// readTranscriptFull reads a transcript JSONL file, returning all line types:
// header, entries, and API calls. Corrupt/partial lines are counted in Skipped.
func readTranscriptFull(path string) (transcriptData, error) {
	f, err := os.Open(path)
	if err != nil {
		return transcriptData{}, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is immaterial

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)

	// First line must be the header.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return transcriptData{}, fmt.Errorf("reading transcript header: %w", err)
		}
		return transcriptData{}, errors.New("transcript file is empty: no header")
	}

	var data transcriptData
	if err := json.Unmarshal(scanner.Bytes(), &data.Header); err != nil {
		return transcriptData{}, fmt.Errorf("parsing transcript header: %w", err)
	}

	// Remaining lines are entries or api_calls. Dispatch by "kind" field.
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Peek at the kind field to decide which struct to unmarshal into.
		var peek struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			data.Skipped++
			continue
		}

		switch peek.Kind {
		case "entry":
			var entry transcript.Entry
			if err := json.Unmarshal(line, &entry); err != nil {
				data.Skipped++
				continue
			}
			data.Entries = append(data.Entries, entry)
		case "api_call":
			var call transcript.APICall
			if err := json.Unmarshal(line, &call); err != nil {
				data.Skipped++
				continue
			}
			data.APICalls = append(data.APICalls, call)
		default:
			data.Skipped++
		}
	}
	if err := scanner.Err(); err != nil {
		return transcriptData{}, fmt.Errorf("reading transcript: %w", err)
	}

	return data, nil
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
