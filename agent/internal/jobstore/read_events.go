package jobstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// ReadDiagnostics describes damage observed while reading a durable log.
type ReadDiagnostics struct {
	TornTail bool
	Corrupt  bool
}

// ReadEvents reads and decodes every event from a jobs.jsonl file WITHOUT
// opening a Store for append. It is the read-only forensic path: a caller that
// only inspects settled state (evener-doctor) uses this instead of Open, which
// opens read-write and creates the file. A missing file yields no events (not an
// error). An unterminated, syntactically incomplete trailing line — an
// in-flight append racing the read — is tolerated, but durable or definitively
// malformed input is reported as corruption.
func ReadEvents(path string) ([]Event, error) {
	events, _, err := ReadEventsWithDiagnostics(path)
	return events, err
}

// ReadEventsWithDiagnostics is ReadEvents with explicit integrity evidence.
func ReadEventsWithDiagnostics(path string) ([]Event, ReadDiagnostics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ReadDiagnostics{}, nil
		}
		return nil, ReadDiagnostics{}, fmt.Errorf("jobstore: read %s: %w", path, err)
	}
	trailingLineTerminated := len(data) > 0 && data[len(data)-1] == '\n'
	diagnostics := ReadDiagnostics{TornTail: len(data) > 0 && !trailingLineTerminated}
	lines := bytes.Split(data, []byte{'\n'})
	// A trailing newline produces a final empty element; drop it so the last
	// real line is correctly identified for partial-line tolerance.
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}
	events := make([]Event, 0, len(lines))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			if i == len(lines)-1 && !trailingLineTerminated && isIncompleteTrailingJSON(line, err) {
				// Tolerate only an unterminated, syntactically incomplete final
				// line from an in-flight append. A newline-terminated or
				// definitively malformed final record is durable corruption.
				diagnostics.TornTail = true
				break
			}
			diagnostics.Corrupt = true
			return nil, diagnostics, fmt.Errorf("jobstore: parse event line %d in %s: %w", i+1, path, err)
		}
		events = append(events, e)
	}
	return events, diagnostics, nil
}
