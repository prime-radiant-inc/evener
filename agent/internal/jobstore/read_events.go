package jobstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// ReadEvents reads and decodes every event from a jobs.jsonl file WITHOUT
// opening a Store for append. It is the read-only forensic path: a caller that
// only inspects settled state (serf-doctor) uses this instead of Open, which
// opens read-write and creates the file. A missing file yields no events (not an
// error). An unparsable trailing line — an in-flight append racing the read — is
// tolerated, but any earlier malformed line is reported as corruption.
func ReadEvents(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jobstore: read %s: %w", path, err)
	}
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
			if i == len(lines)-1 {
				// Tolerate a partial trailing line from an in-flight append.
				break
			}
			return nil, fmt.Errorf("jobstore: parse event line %d in %s: %w", i+1, path, err)
		}
		events = append(events, e)
	}
	return events, nil
}
