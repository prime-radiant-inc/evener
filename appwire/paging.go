package appwire

import "strconv"

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
	lo := hi - limit
	if lo < 0 {
		lo = 0
	}
	next := ""
	if lo > 0 {
		next = strconv.Itoa(lo)
	}
	return ThreadTurnsListResponse{Data: all[lo:hi], NextCursor: next}
}
