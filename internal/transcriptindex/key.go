// Package transcriptindex is a seekable, derived index over one session
// transcript. It answers item-window reads by projecting only the entries that
// contribute to the returned items, found through fixed-size records in a
// sidecar directory, and never decodes a whole turn or file to do it.
//
// The projection follows the transcript read model (docs/superpowers/specs/
// 2026-09-25-transcript-read-model-design.md). Legacy entries project as
// today's file projection (apptranscript.ItemTurnsFromFile) does: the same
// grouping, turn and item ids, call-id merge, status, error and usage.
// New-format entries join the turn their TurnID names, take their status from
// completion and reopen entries, and complete the calls of their turn's
// awaiting ASSISTANT entry. Every item sits at {entry: ordinal + 1, item:
// part} of the entry and content part that opened it, and carries the version
// of the latest entry that contributed to it.
package transcriptindex

import (
	"fmt"

	"primeradiant.com/evener/appwire"
)

// keyVersion is the transcript key scheme ItemKey spells. Version 1 is
// appitempaging.TranscriptItemKey, which keys items by logical-group ordinal.
const keyVersion = 2

// ItemKey is the transcript key of the item at position in turn turnID:
// apptranscript-item-v2:<turnID>:<entry ordinal>:<part>. Header-derived
// prelude items, at entry 0, name "header" in place of the ordinal.
func ItemKey(turnID string, position appwire.ThreadItemPosition) string {
	if position.Entry == 0 {
		return fmt.Sprintf("apptranscript-item-v%d:%s:header:%d", keyVersion, turnID, position.Item)
	}
	return fmt.Sprintf("apptranscript-item-v%d:%s:%d:%d", keyVersion, turnID, position.Entry-1, position.Item)
}
