package appitempaging

import (
	"encoding/base64"
	"encoding/json"

	"primeradiant.com/evener/appwire"
)

// ManagerFetchRangeBytes leaves room for base64 expansion, the origin-bearing
// continuation cursor, actor identity, and the outer Manager response envelope.
const ManagerFetchRangeBytes = 128 * 1024

// BuildFetchResponse slices one version-fenced item's canonical wire encoding.
func BuildFetchResponse(item appwire.ThreadItem, identity CursorIdentity, cursor FetchCursor) (appwire.SessionFetchResponse, error) {
	if item.Position == nil || *item.Position != cursor.Position {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	if item.Version != cursor.ItemVersion {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemChanged(item.Version)
	}
	canonical, err := json.Marshal(item)
	if err != nil {
		return appwire.SessionFetchResponse{}, err
	}
	return BuildFetchResponseBytes(canonical, identity, cursor)
}

// BuildFetchResponseBytes slices an item's already validated canonical wire
// encoding. The caller owns the position and version fence against its source.
func BuildFetchResponseBytes(canonical []byte, identity CursorIdentity, cursor FetchCursor) (appwire.SessionFetchResponse, error) {
	if cursor.Offset >= uint64(len(canonical)) {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	end := min(cursor.Offset+ManagerFetchRangeBytes, uint64(len(canonical)))
	response := appwire.SessionFetchResponse{
		Position:    cursor.Position,
		Version:     cursor.ItemVersion,
		TotalLength: uint64(len(canonical)),
		Offset:      cursor.Offset,
		Data:        base64.StdEncoding.EncodeToString(canonical[cursor.Offset:end]),
	}
	if end < uint64(len(canonical)) {
		cursor.Offset = end
		var err error
		response.FetchCursor, err = EncodeFetchCursor(identity, cursor)
		if err != nil {
			return appwire.SessionFetchResponse{}, err
		}
	}
	return response, nil
}
