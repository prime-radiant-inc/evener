package appwire

import "testing"

// WindowTurns and PageTurns are two independently written turn slicers for the
// same on-disk turn list, serving two RPCs:
//
//   - WindowTurns (thread/read) returns the latest N turns plus a cursor to page
//     further back.
//   - PageTurns (turns/list) returns up to `limit` turns older than a cursor,
//     with its own clamp/parse/cursor logic.
//
// They MUST agree on the first window — PageTurns(all, "", N) is, by contract,
// the same newest-N slice WindowTurns(all, N) returns, with the same older
// cursor — and PageTurns walked from "" to exhaustion must reconstruct EVERY
// turn exactly once, oldest-first, with no gaps, dupes, or non-terminating
// cursor. The package's paging_test.go pins a few hand-picked examples; nothing
// asserts these two equivalences over arbitrary inputs. This is that
// differential + the paging-covers-everything invariant.

// FuzzTurnPagingEquivalence drives both turn slicers from one fuzzed (count,
// limit) pair and asserts:
//
//   - first-page differential (limit > 0): WindowTurns(all, limit) equals
//     PageTurns(all, "", limit) — same turn IDs in the same order AND the same
//     older/next cursor. A drift in either slicer's boundary math diverges here.
//   - pagination coverage (any limit): following PageTurns from the empty cursor
//     by NextCursor until it clears yields the turns back in exactly `all`'s
//     order with no duplication or loss, terminates via an empty cursor (not the
//     loop guard), and never repeats a cursor.
//
// ALLOW-LIST — NOT treated as divergence:
//   - the first-page differential is only asserted for limit > 0, because
//     WindowTurns treats limit <= 0 as "return everything, no cursor" while
//     PageTurns substitutes DefaultTurnPageSize for a non-positive limit; that
//     is a deliberate per-RPC default, not a slicer disagreement. The coverage
//     invariant still runs for non-positive limits (PageTurns must still page
//     the whole thread).
func FuzzTurnPagingEquivalence(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00, 0x01},
		{0x05, 0x03},
		{0x0a, 0x04},
		{0x40, 0x00}, // limit byte 0 → non-positive after mapping
		{0xff, 0xff},
		{0x1e, 0x1e}, // count == DefaultTurnPageSize boundary
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		count, limit := pagingParams(data)
		all := turnsN(count)

		if limit > 0 {
			wPage, wCursor := WindowTurns(all, limit)
			r := PageTurns(all, "", limit)
			if !equalIDs(ids(wPage), ids(r.Data)) {
				t.Fatalf("first-page differs: WindowTurns(%d,%d)=%v PageTurns(\"\",%d)=%v",
					count, limit, ids(wPage), limit, ids(r.Data))
			}
			if wCursor != r.NextCursor {
				t.Fatalf("first-page cursor differs: WindowTurns cursor=%q PageTurns NextCursor=%q (count=%d limit=%d)",
					wCursor, r.NextCursor, count, limit)
			}
		}

		assertPagingCoversAll(t, all, limit)
	})
}

// pagingParams derives a turn count and a page limit from the fuzz bytes. The
// limit is mapped into [-2, 0x3f] so the non-positive branches (legacy/default)
// are reachable; the count is capped so a fuzz iteration stays cheap.
func pagingParams(data []byte) (count, limit int) {
	count, limit = 0, 1
	if len(data) > 0 {
		count = int(data[0]) % 257 // 0..256, exercises the ==/just-over boundaries near typical sizes
	}
	if len(data) > 1 {
		limit = int(data[1])%0x42 - 2 // -2 .. 0x3f
	}
	return count, limit
}

// assertPagingCoversAll walks PageTurns from the empty cursor to exhaustion and
// checks that the visited turns reconstruct `all` exactly (oldest-first, no gaps
// or dupes), that paging terminates by clearing the cursor rather than by the
// loop guard, and that no cursor repeats.
func assertPagingCoversAll(t *testing.T, all []Turn, limit int) {
	t.Helper()
	var pages [][]Turn
	seen := map[string]bool{}
	cursor := ""
	// One page per turn is the worst case (limit 1); +2 gives headroom and still
	// bounds a runaway cursor.
	guard := len(all) + 2
	for i := 0; ; i++ {
		if i > guard {
			t.Fatalf("PageTurns did not terminate within %d pages (count=%d limit=%d)", guard, len(all), limit)
		}
		r := PageTurns(all, cursor, limit)
		pages = append(pages, r.Data)
		if r.NextCursor == "" {
			break
		}
		if seen[r.NextCursor] {
			t.Fatalf("PageTurns repeated cursor %q (count=%d limit=%d)", r.NextCursor, len(all), limit)
		}
		seen[r.NextCursor] = true
		cursor = r.NextCursor
	}

	// Pages come newest→oldest; reverse to rebuild oldest-first.
	var got []string
	for i := len(pages) - 1; i >= 0; i-- {
		got = append(got, ids(pages[i])...)
	}
	want := ids(all)
	if !equalIDs(got, want) {
		t.Fatalf("pagination did not reconstruct the thread:\n  got =%v\n  want=%v\n  (count=%d limit=%d)",
			got, want, len(all), limit)
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTurnPagingEquivalenceSanity is a fast, explicit seed check (no fuzzing):
// the first-page differential and full-coverage invariant must hold for a
// representative thread/limit.
func TestTurnPagingEquivalenceSanity(t *testing.T) {
	all := turnsN(10)
	for _, limit := range []int{1, 3, 4, 10, 30} {
		wPage, wCursor := WindowTurns(all, limit)
		r := PageTurns(all, "", limit)
		if !equalIDs(ids(wPage), ids(r.Data)) || wCursor != r.NextCursor {
			t.Fatalf("limit=%d: window=(%v,%q) page=(%v,%q)", limit, ids(wPage), wCursor, ids(r.Data), r.NextCursor)
		}
		assertPagingCoversAll(t, all, limit)
	}
	// Non-positive limits still page the whole thread.
	for _, limit := range []int{0, -1} {
		assertPagingCoversAll(t, all, limit)
	}
}
