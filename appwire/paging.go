package appwire

import (
	"strconv"
	"strings"
)

const TranscriptItemPageLimit = 40

// NormalizeTranscriptItemLimit applies the item-mode default and maximum. A
// non-positive request means the protocol default; positive values are bounded
// by the public page maximum. The returned limit is valid only when the error
// is nil; on error it is zero and must not be used.
func NormalizeTranscriptItemLimit(limit int) (int, error) {
	if limit <= 0 {
		return TranscriptItemPageLimit, nil
	}
	if limit > TranscriptItemPageLimit {
		return 0, InvalidParams("itemLimit must be between 1 and " + strconv.Itoa(TranscriptItemPageLimit))
	}
	return limit, nil
}

// ValidateThreadReadParams validates the item-only transcript read limit.
func ValidateThreadReadParams(params ThreadReadParams) error {
	_, err := NormalizeTranscriptItemLimit(params.ItemLimit)
	return err
}

// ValidateThreadTurnsListParams validates the item-only transcript list cursor
// and limit.
func ValidateThreadTurnsListParams(params ThreadTurnsListParams) error {
	if strings.TrimSpace(params.Cursor) == "" {
		return InvalidParams("cursor is required for thread/turns/list")
	}
	if isLegacyNumericCursor(params.Cursor) {
		return InvalidParams("cursor must be an opaque item cursor")
	}
	_, err := NormalizeTranscriptItemLimit(params.ItemLimit)
	return err
}

func isLegacyNumericCursor(cursor string) bool {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return false
	}
	if cursor[0] == '+' || cursor[0] == '-' {
		cursor = cursor[1:]
	}
	if cursor == "" {
		return false
	}
	for i := 0; i < len(cursor); i++ {
		if cursor[i] < '0' || cursor[i] > '9' {
			return false
		}
	}
	return true
}

// ValidateTranscriptItemTurns verifies the mandatory metadata on an item-mode
// turn fragment. It deliberately does not impose ordering or uniqueness; those
// source invariants belong to the atomic pager.
func ValidateTranscriptItemTurns(turns []Turn) error {
	for _, turn := range turns {
		if turn.ItemsView != TurnItemsViewFragment {
			return InvalidParams("item-mode turns must use itemsView \"fragment\"")
		}
		for _, item := range turn.Items {
			if item.TranscriptKey == "" {
				return InvalidParams("item-mode items require transcriptKey")
			}
			if item.Position == nil {
				return InvalidParams("item-mode items require position")
			}
		}
	}
	return nil
}

// ValidateThreadReadItemResponse verifies an item-mode read response before it
// is sent to a client.
func ValidateThreadReadItemResponse(response ThreadReadResponse) error {
	return ValidateTranscriptItemTurns(response.Thread.Turns)
}

// ValidateThreadTurnsListItemResponse verifies an item-mode list response
// before it is sent to a client.
func ValidateThreadTurnsListItemResponse(response ThreadTurnsListResponse) error {
	return ValidateTranscriptItemTurns(response.Data)
}
