package appwire

import (
	"strconv"
	"strings"
)

const TranscriptItemPageLimit = 40

// MaxPendingAskPageLimit bounds a pending-ask page. Its whole Manager response,
// including the clamped row, is measured against the 256 KiB read cap.
const MaxPendingAskPageLimit = 3244

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

// IsPendingAskPage distinguishes a page from an ordinary metadata read. Zero
// revisions, offsets and limits remain present through their pointer fields.
func (p ThreadReadParams) IsPendingAskPage() bool {
	return p.ExpectedAskRevision != nil || p.ExpectedAskBootGeneration != "" || p.Offset != nil || p.Limit != nil
}

// ValidateThreadReadParams validates transcript limits and the separate ask page
// grammar. A page never establishes a history window or subscription.
func ValidateThreadReadParams(params ThreadReadParams) error {
	if params.IsPendingAskPage() {
		if err := ValidateAskFence(params.ExpectedAskRevision, params.ExpectedAskBootGeneration); err != nil {
			return err
		}
		if strings.TrimSpace(params.Ref) == "" || params.ExpectedAskRevision == nil {
			return InvalidParams("pending ask pages require a ref and an observed ask pair")
		}
		if params.Subscribe || params.ReplaceSubscription || params.IncludeTurns || params.HeldSnapshot != nil || params.AnswerContinuation != nil {
			return InvalidParams("pending ask pages cannot subscribe or request history")
		}
	}
	_, err := NormalizeTranscriptItemLimit(params.ItemLimit)
	return err
}

// ValidateThreadTurnsListParams validates the item-only transcript list cursor
// and limit.
func ValidateThreadTurnsListParams(params ThreadTurnsListParams) error {
	// A before boundary with no cursor pages before it in the thread's current
	// transcript.
	if params.Cursor == "" && params.Before == nil {
		// Do not reword: remoteCursorlessBeforeRefusal in
		// cmd/evener-hub/internal/appsource/remote_hub_source.go matches this text
		// to recognize a remote hub that predates the before field.
		return InvalidParams("cursor or before is required for thread/turns/list")
	}
	// A cursor that is present must be one.
	if (params.Cursor != "" && strings.TrimSpace(params.Cursor) == "") || isLegacyNumericCursor(params.Cursor) {
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
