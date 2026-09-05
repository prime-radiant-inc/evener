package appwire

import "strconv"

const TranscriptItemPageLimit = 40

// DefaultTurnPageSize is the turn-page size used when a turns/list request
// omits a positive limit.
const DefaultTurnPageSize = 30

// WindowTurns returns the latest turnLimit turns (oldest-first) for a bounded
// thread/read, plus an olderCursor for paging further back via PageTurns. A
// turnLimit <= 0, or one no smaller than the turn count, returns all turns and
// an empty cursor (no windowing — the legacy full-read behavior).
func WindowTurns(all []Turn, turnLimit int) (page []Turn, olderCursor string) {
	if turnLimit <= 0 || len(all) <= turnLimit {
		return all, ""
	}
	lo := len(all) - turnLimit
	return all[lo:], strconv.Itoa(lo)
}

// PageTurns returns up to limit turns older than cursor (oldest-first within the
// page) plus the nextCursor for the page just before it. An empty or
// unparseable cursor starts from the newest turn; a cursor past the end clamps
// to it. nextCursor is empty once the oldest turn has been reached.
//
// Cursors are front-anchored positions (index 0 = oldest turn), so they stay
// valid as new turns append to the end — the common live-session case.
func PageTurns(all []Turn, cursor string, limit int) ThreadTurnsListResponse {
	if limit <= 0 {
		limit = DefaultTurnPageSize
	}
	hi := len(all)
	if cursor != "" {
		if c, err := strconv.Atoi(cursor); err == nil {
			hi = c
		}
	}
	if hi > len(all) {
		hi = len(all)
	}
	if hi < 0 {
		hi = 0
	}
	lo := max(hi-limit, 0)
	next := ""
	if lo > 0 {
		next = strconv.Itoa(lo)
	}
	return ThreadTurnsListResponse{Data: all[lo:hi], NextCursor: next}
}

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

func validateTranscriptPageUnit(unit TranscriptPageUnit) error {
	switch unit {
	case "", TranscriptPageUnitTurn, TranscriptPageUnitItem:
		return nil
	default:
		return InvalidParams("pageUnit must be \"turn\" or \"item\"")
	}
}

// ValidateThreadReadParams rejects ambiguous item/turn paging combinations.
// The zero page unit and zero item limit retain the legacy request shape.
func ValidateThreadReadParams(params ThreadReadParams) error {
	if err := validateTranscriptPageUnit(params.PageUnit); err != nil {
		return err
	}
	if params.PageUnit == TranscriptPageUnitItem {
		if params.TurnLimit != 0 {
			return InvalidParams("turnLimit and itemLimit cannot be supplied together")
		}
		_, err := NormalizeTranscriptItemLimit(params.ItemLimit)
		return err
	}
	if params.ItemLimit != 0 {
		return InvalidParams("itemLimit requires pageUnit \"item\"")
	}
	return nil
}

// ValidateThreadTurnsListParams rejects ambiguous item/turn paging
// combinations while leaving numeric legacy cursors and limits untouched.
func ValidateThreadTurnsListParams(params ThreadTurnsListParams) error {
	if err := validateTranscriptPageUnit(params.PageUnit); err != nil {
		return err
	}
	if params.PageUnit == TranscriptPageUnitItem {
		if params.Limit != 0 {
			return InvalidParams("limit and itemLimit cannot be supplied together")
		}
		if _, err := NormalizeTranscriptItemLimit(params.ItemLimit); err != nil {
			return err
		}
		if params.Cursor == "" {
			return InvalidParams("cursor is required for item-mode thread/turns/list")
		}
		return nil
	}
	if params.ItemLimit != 0 {
		return InvalidParams("itemLimit requires pageUnit \"item\"")
	}
	return nil
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
	if response.PageUnit != TranscriptPageUnitItem {
		return InvalidParams("item-mode response must use pageUnit \"item\"")
	}
	return ValidateTranscriptItemTurns(response.Thread.Turns)
}

// ValidateThreadTurnsListItemResponse verifies an item-mode list response
// before it is sent to a client.
func ValidateThreadTurnsListItemResponse(response ThreadTurnsListResponse) error {
	if response.PageUnit != TranscriptPageUnitItem {
		return InvalidParams("item-mode response must use pageUnit \"item\"")
	}
	return ValidateTranscriptItemTurns(response.Data)
}
