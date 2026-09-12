package appitempaging

import (
	"fmt"

	"primeradiant.com/evener/appwire"
)

const TranscriptItemProjectionVersion uint16 = 1

// TranscriptItemKey returns the stable key shared by live and historical local
// transcript item projections.
func TranscriptItemKey(turnID string, position appwire.ThreadItemPosition) string {
	return fmt.Sprintf("apptranscript-item-v%d:%s:%d:%d", TranscriptItemProjectionVersion, turnID, position.Entry, position.Item)
}
