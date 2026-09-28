package appitempaging

import (
	"fmt"

	"primeradiant.com/evener/appwire"
)

// TranscriptItemProjectionVersion names the projection behind item cursors; a
// cursor minted under another version is stale.
const TranscriptItemProjectionVersion uint16 = 2

// transcriptItemKeyVersion is the version TranscriptItemKey spells. It stays
// apart from the projection version: the transcript index's keys
// (transcriptindex.ItemKey) spell version 2, with an entry ordinal where
// these carry a logical-group position.
const transcriptItemKeyVersion = 1

// TranscriptItemKey returns the stable key shared by live and historical local
// transcript item projections.
func TranscriptItemKey(turnID string, position appwire.ThreadItemPosition) string {
	return fmt.Sprintf("apptranscript-item-v%d:%s:%d:%d", transcriptItemKeyVersion, turnID, position.Entry, position.Item)
}
