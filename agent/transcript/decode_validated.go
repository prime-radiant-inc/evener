package transcript

import (
	"encoding/json"
	"fmt"
)

// DecodeValidatedEntry decodes an entry line whose bytes DecodeEntry has
// already accepted, such as a line a transcript index validated when it
// indexed it. It yields what DecodeEntry would, pre-flag machinery inference
// included, without DecodeEntry's strictness checks, which cost several times
// the decode itself on a large entry. Bytes nothing has validated go through
// DecodeEntry.
func DecodeValidatedEntry(line []byte) (Entry, error) {
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		return Entry{}, fmt.Errorf("decode transcript entry: %w", err)
	}
	if !entry.MachineryFlagged {
		inferPreFlagMachinery(&entry.Turn)
	}
	return entry, nil
}
