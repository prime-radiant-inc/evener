// Package transcriptindex is a seekable, derived index over one session
// transcript. It answers item-window reads by projecting only the entries that
// contribute to the returned items, found through fixed-size records in a
// sidecar directory, and never decodes a whole turn or file to do it.
//
// The projection it reproduces is today's file projection
// (apptranscript.ItemTurnsFromFile): the same grouping, turn and item ids,
// call-id merge, status, error and usage. Only positions and keys follow the
// transcript read model's entry-ordinal scheme (docs/superpowers/specs/
// 2026-09-25-transcript-read-model-design.md): an item sits at {entry: ordinal
// + 1, item: part} of the entry and content part that opened it.
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
