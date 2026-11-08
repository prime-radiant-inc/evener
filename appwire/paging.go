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
	if err := ValidateThreadWindowParams(params.ThreadWindowParams, params.ItemLimit); err != nil {
		return err
	}
	if params.IsPendingAskPage() {
		if err := ValidateAskFence(params.ExpectedAskRevision, params.ExpectedAskBootGeneration); err != nil {
			return err
		}
		if strings.TrimSpace(params.Ref) == "" || params.ExpectedAskRevision == nil {
			return InvalidParams("pending ask pages require a ref and an observed ask pair")
		}
		if params.Subscribe || params.ReplaceSubscription || params.IncludeTurns || params.HeldSnapshot != nil || params.AnswerContinuation != nil || params.TurnWindow != nil {
			return InvalidParams("pending ask pages cannot subscribe or request history")
		}
	}
	_, err := NormalizeTranscriptItemLimit(params.ItemLimit)
	return err
}

// ValidateThreadWindowParams validates the additive Manager window grammar.
// A zero value is the legacy request shape and keeps its existing meaning.
func ValidateThreadWindowParams(params ThreadWindowParams, itemLimit int) error {
	if params.TurnWindow == nil {
		if len(params.ItemTypes) != 0 || params.PreviewBytes != 0 || params.MessageBytes != 0 || params.WindowBudgetBytes != 0 {
			return InvalidParams("window selectors require turnWindow")
		}
		return nil
	}
	window := params.TurnWindow
	forms := 0
	for _, present := range []bool{window.Latest != 0, window.Turn != "", window.Since != "", window.Before != nil} {
		if present {
			forms++
		}
	}
	if forms != 1 {
		return InvalidParams("turnWindow requires exactly one window form")
	}
	if window.Latest < 0 || window.Latest > 0 && window.Limit != 0 {
		return InvalidParams("latest requires a positive count and no limit")
	}
	if window.Turn != "" && window.Limit != 0 {
		return InvalidParams("turn requires an id and no limit")
	}
	if window.Since != "" && window.Limit <= 0 {
		return InvalidParams("since requires a positive limit")
	}
	if window.Before != nil && (window.Before.TurnID == "" || window.Before.Limit <= 0 || window.Limit != 0) {
		return InvalidParams("before requires a turn id and positive limit")
	}
	if params.PreviewBytes < 0 || params.MessageBytes < 0 || params.WindowBudgetBytes < 0 {
		return InvalidParams("window byte bounds must be nonnegative")
	}
	if itemLimit < 0 || itemLimit > TranscriptItemPageLimit {
		return InvalidParams("windowed itemLimit must be between 0 and " + strconv.Itoa(TranscriptItemPageLimit))
	}
	for _, itemType := range params.ItemTypes {
		switch itemType {
		case "messages", "agentMessage", "userMessage", "steering", "reasoning", "systemMessage", "commandExecution":
		default:
			return InvalidParams("unknown transcript item type")
		}
	}
	return nil
}

// ValidateThreadTurnsListParams validates the item-only transcript list cursor
// and limit.
func ValidateThreadTurnsListParams(params ThreadTurnsListParams) error {
	if err := ValidateThreadWindowParams(params.ThreadWindowParams, params.ItemLimit); err != nil {
		return err
	}
	// A before boundary with no cursor pages before it in the thread's current
	// transcript.
	if params.Cursor == "" && params.Before == nil && params.TurnWindow == nil {
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
