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
	"strconv"
	"strings"

	"primeradiant.com/evener/appwire"
)

// keyVersion is the transcript key scheme ItemKey spells. Version 1 is
// appitempaging.TranscriptItemKey, which keys items by logical-group ordinal.
const keyVersion = 2

// ItemKey is the transcript key of the item at position in turn turnID:
// apptranscript-item-v2:<turnID>:<entry ordinal>:<part>. Header-derived
// prelude items, at entry 0, are apptranscript-item-v2:prelude:header:<part>
// whatever turn shows them.
func ItemKey(turnID string, position appwire.ThreadItemPosition) string {
	if position.Entry == 0 {
		return fmt.Sprintf("%s%s%d", keyPrefix, headerKeyPrefix, position.Item)
	}
	return fmt.Sprintf("%s%s:%d:%d", keyPrefix, turnID, position.Entry-1, position.Item)
}

// keyPrefix is the fixed lead-in every ItemKey carries; ParseItemKey rejects
// anything that does not start with it, including keys stamped by an older
// keyVersion.
var keyPrefix = fmt.Sprintf("apptranscript-item-v%d:", keyVersion)

// headerKeyPrefix is the lead-in a header prelude key carries after
// keyPrefix, in place of a turn id and ordinal.
const headerKeyPrefix = "prelude:header:"

// ParseItemKey is ItemKey's inverse: it recovers the turn id and position a
// transcriptKey names. For a header prelude key it returns position {Entry:
// 0, Item: part} and an empty turnID, since the header key carries no turn
// id. For an entry key it returns {Entry: ordinal + 1, Item: part} and the
// turn id, splitting the trailing "<ordinal>:<part>" from the right so a turn
// id is never mistaken for part of that suffix.
func ParseItemKey(key string) (turnID string, position appwire.ThreadItemPosition, err error) {
	malformed := func() (string, appwire.ThreadItemPosition, error) {
		return "", appwire.ThreadItemPosition{}, fmt.Errorf("transcriptindex: malformed item key %q", key)
	}

	rest, ok := strings.CutPrefix(key, keyPrefix)
	if !ok {
		return malformed()
	}

	if headerRest, ok := strings.CutPrefix(rest, headerKeyPrefix); ok {
		part, perr := parseCanonicalUint(headerRest, 32)
		if perr != nil {
			return malformed()
		}
		return "", appwire.ThreadItemPosition{Entry: 0, Item: uint32(part)}, nil
	}

	partIdx := strings.LastIndex(rest, ":")
	if partIdx < 0 {
		return malformed()
	}
	turnAndOrdinal, partStr := rest[:partIdx], rest[partIdx+1:]
	ordinalIdx := strings.LastIndex(turnAndOrdinal, ":")
	if ordinalIdx < 0 {
		return malformed()
	}
	turnID, ordinalStr := turnAndOrdinal[:ordinalIdx], turnAndOrdinal[ordinalIdx+1:]
	if turnID == "" {
		return malformed()
	}
	// 63 bits, not 64: Entry is ordinal + 1, and this keeps that add from
	// wrapping back to 0 — which ParseItemKey would otherwise return as a
	// well-formed header position for a garbage ordinal near MaxUint64.
	ordinal, err := parseCanonicalUint(ordinalStr, 63)
	if err != nil {
		return malformed()
	}
	// 32 bits: Item is uint32, so a part beyond its range must be rejected
	// here rather than silently truncated by a uint32 conversion.
	part, err := parseCanonicalUint(partStr, 32)
	if err != nil {
		return malformed()
	}
	return turnID, appwire.ThreadItemPosition{Entry: ordinal + 1, Item: uint32(part)}, nil
}

// parseCanonicalUint parses s as the decimal, non-negative, bitSize-bounded
// integer ItemKey emits. strconv.ParseUint already rejects a leading sign and
// an out-of-range value; the leading-zero check on top of it rejects a
// numeral ItemKey would never produce (e.g. "007"), so no two differently
// spelled keys can ever name the same position.
func parseCanonicalUint(s string, bitSize int) (uint64, error) {
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("transcriptindex: non-canonical integer %q", s)
	}
	return strconv.ParseUint(s, 10, bitSize)
}
