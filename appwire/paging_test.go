package appwire

import "testing"

func turnsN(n int) []Turn {
	out := make([]Turn, n)
	for i := range out {
		out[i] = Turn{ID: "turn_" + itoa(i)}
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func ids(turns []Turn) []string {
	out := make([]string, len(turns))
	for i, t := range turns {
		out[i] = t.ID
	}
	return out
}

func TestWindowTurnsBoundsToLatest(t *testing.T) {
	all := turnsN(10)
	page, cursor := WindowTurns(all, 3)
	if got := ids(page); len(got) != 3 || got[0] != "turn_7" || got[2] != "turn_9" {
		t.Fatalf("window = %v, want latest 3 (turn_7..turn_9)", ids(page))
	}
	if cursor != "7" {
		t.Fatalf("olderCursor = %q, want 7", cursor)
	}
}

func TestWindowTurnsUnboundedWhenLimitZeroOrLarger(t *testing.T) {
	all := turnsN(4)
	for _, lim := range []int{0, -1, 4, 99} {
		page, cursor := WindowTurns(all, lim)
		if len(page) != 4 || cursor != "" {
			t.Fatalf("limit %d: page=%d cursor=%q, want all 4 and no cursor", lim, len(page), cursor)
		}
	}
}

func TestPageTurnsWalksBackwardToHead(t *testing.T) {
	all := turnsN(10)

	// Empty cursor starts from the newest turn.
	p1 := PageTurns(all, "", 4)
	if got := ids(p1.Data); len(got) != 4 || got[0] != "turn_6" || got[3] != "turn_9" {
		t.Fatalf("page1 = %v, want turn_6..turn_9", ids(p1.Data))
	}
	if p1.NextCursor != "6" {
		t.Fatalf("page1 nextCursor = %q, want 6", p1.NextCursor)
	}

	// Next page is the four turns just older.
	p2 := PageTurns(all, p1.NextCursor, 4)
	if got := ids(p2.Data); len(got) != 4 || got[0] != "turn_2" || got[3] != "turn_5" {
		t.Fatalf("page2 = %v, want turn_2..turn_5", ids(p2.Data))
	}
	if p2.NextCursor != "2" {
		t.Fatalf("page2 nextCursor = %q, want 2", p2.NextCursor)
	}

	// Final partial page reaches the head and clears the cursor.
	p3 := PageTurns(all, p2.NextCursor, 4)
	if got := ids(p3.Data); len(got) != 2 || got[0] != "turn_0" || got[1] != "turn_1" {
		t.Fatalf("page3 = %v, want turn_0..turn_1", ids(p3.Data))
	}
	if p3.NextCursor != "" {
		t.Fatalf("page3 nextCursor = %q, want empty (head reached)", p3.NextCursor)
	}
}

func TestPageTurnsEmptyAndClamped(t *testing.T) {
	if r := PageTurns(nil, "", 5); len(r.Data) != 0 || r.NextCursor != "" {
		t.Fatalf("empty thread: data=%d cursor=%q", len(r.Data), r.NextCursor)
	}
	// A cursor past the end clamps to the end; a garbage cursor is treated as
	// "from newest".
	all := turnsN(3)
	if r := PageTurns(all, "99", 10); len(r.Data) != 3 {
		t.Fatalf("over-cursor: data=%d, want 3", len(r.Data))
	}
	if r := PageTurns(all, "garbage", 10); len(r.Data) != 3 {
		t.Fatalf("garbage cursor: data=%d, want 3 (from newest)", len(r.Data))
	}
	// A negative cursor clamps the high bound to 0, yielding an empty page with
	// no further cursor. This exercises the hi < 0 branch.
	if r := PageTurns(all, "-5", 10); len(r.Data) != 0 || r.NextCursor != "" {
		t.Fatalf("negative cursor: data=%d cursor=%q, want empty page and no cursor", len(r.Data), r.NextCursor)
	}
}
