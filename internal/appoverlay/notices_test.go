package appoverlay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func noticeKeys(o *Overlay) []string {
	var keys []string
	for _, item := range o.Snapshot() {
		if item.Kind == appwire.OverlayNotice {
			keys = append(keys, item.Key)
		}
	}
	return keys
}

func TestEachEphemeralEventBecomesANoticeOfItsKind(t *testing.T) {
	cases := []struct {
		data events.EventData
		kind appwire.ThreadItemEventKind
	}{
		{events.RoundTimings{Round: 1}, appwire.ThreadItemEventKindRoundTimings},
		{events.PromptLoadedData{Label: "AGENTS.md", Size: 10}, appwire.ThreadItemEventKindPromptLoaded},
		{events.PluginLoadedData{Name: "p"}, appwire.ThreadItemEventKindPluginLoaded},
		{events.LoopDetectionData{Message: "looping"}, appwire.ThreadItemEventKindLoopDetection},
		{events.ContextCompactionData{Layer: "L3"}, appwire.ThreadItemEventKindContextCompaction},
		{events.ForkSummaryData{Turn: 2}, appwire.ThreadItemEventKindForkSummary},
		{events.WarningData{Message: "careful"}, appwire.ThreadItemEventKindWarning},
		{events.ErrorData{Error: "diagnostic failed"}, appwire.ThreadItemEventKindError},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			o := newOverlay()
			o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
			item := upserted(t, one(t, o.Event(events.New(tc.data))))
			if item.Kind != appwire.OverlayNotice || item.Key != "notice:0" || item.TurnID != "t_1" {
				t.Fatalf("notice = %+v", item)
			}
			if item.Item.Type != "systemMessage" || item.Item.EventKind != tc.kind || item.Item.ID != item.Key || item.Item.TurnID != "t_1" {
				t.Fatalf("notice item = %+v, want a %s systemMessage", item.Item, tc.kind)
			}
			if item.Item.Text == "" && item.Item.Description == "" {
				t.Fatal("notice shows nothing")
			}
		})
	}
}

func TestWarningAndErrorNoticesShowTheirMessage(t *testing.T) {
	o := newOverlay()
	warning := upserted(t, one(t, o.Event(events.New(events.WarningData{Message: "context nearly full"}))))
	if warning.Item.Text != "context nearly full" {
		t.Fatalf("warning text = %q", warning.Item.Text)
	}
	failure := upserted(t, one(t, o.Event(events.New(events.ErrorData{Error: "fail-closed diagnostic"}))))
	if failure.Item.Text != "fail-closed diagnostic" {
		t.Fatalf("error text = %q", failure.Item.Text)
	}
}

func TestNoticeAnchorsFollowTheLastRecordedOrdinal(t *testing.T) {
	o := newOverlay()
	first := upserted(t, one(t, o.Event(events.New(events.LoopDetectionData{Message: "a"}))))
	if *first.Anchor != (appwire.ThreadItemPosition{Entry: 0, Item: appwire.NoticeAnchorItem, Sub: 0}) {
		t.Fatalf("anchor before any entry = %+v", *first.Anchor)
	}

	o.Recorded(assistantRecord(6, "t_1", "r_1", textPart("x")))
	second := upserted(t, one(t, o.Event(events.New(events.LoopDetectionData{Message: "b"}))))
	if second.Key != "notice:1" || *second.Anchor != (appwire.ThreadItemPosition{Entry: 7, Item: appwire.NoticeAnchorItem, Sub: 1}) {
		t.Fatalf("second notice = %s at %+v, want notice:1 at {7, 1<<30, 1}", second.Key, *second.Anchor)
	}
}

func TestTheRingKeepsFiftyRoundTimingsAndFiftyOthers(t *testing.T) {
	o := newOverlay()
	for i := range 60 {
		o.Event(events.New(events.RoundTimings{Round: i}))
		o.Event(events.New(events.LoopDetectionData{Message: fmt.Sprintf("loop %d", i)}))
	}
	var roundTimings, others int
	for _, item := range o.Snapshot() {
		if item.Item.EventKind == appwire.ThreadItemEventKindRoundTimings {
			roundTimings++
		} else {
			others++
		}
	}
	if roundTimings != maxRoundTimingsNotices || others != maxOtherNotices {
		t.Fatalf("ring holds %d round_timings and %d others, want %d and %d", roundTimings, others, maxRoundTimingsNotices, maxOtherNotices)
	}
	// The first ten of each kind were the oldest: notices 0..19.
	for n := range 20 {
		if o.Contains(fmt.Sprintf("notice:%d", n)) {
			t.Fatalf("notice:%d survived; the ring evicts the oldest first", n)
		}
	}
	if !o.Contains("notice:20") || !o.Contains("notice:119") {
		t.Fatal("the ring dropped a notice it should keep")
	}
}

func TestTheRingKeeps64KBOfEncodedNotices(t *testing.T) {
	o := newOverlay()
	for range 40 {
		o.Event(events.New(events.LoopDetectionData{Message: strings.Repeat("x", 2<<10)}))
	}
	keys := noticeKeys(o)
	if len(keys) == 0 || len(keys) >= 40 {
		t.Fatalf("ring holds %d of 40 2 KB notices, want fewer than 40", len(keys))
	}
	if o.notices.bytes > maxNoticeBytes {
		t.Fatalf("ring holds %d bytes, want at most %d", o.notices.bytes, maxNoticeBytes)
	}
	if keys[len(keys)-1] != "notice:39" {
		t.Fatalf("newest retained notice = %s, want notice:39", keys[len(keys)-1])
	}
}

func TestTheBudgetEvictsTheOldestNoticeAcrossOverlays(t *testing.T) {
	// Room for three of the notices below and not four: they differ in size
	// only by their key and anchor digits.
	probe := newOverlay()
	probe.Event(events.New(events.LoopDetectionData{Message: "aaaa"}))
	size := encodedSize(upserted(t, one(t, probe.Event(events.New(events.LoopDetectionData{Message: "aaaa"})))))
	budget := NewBudget(3*size + size/2)
	a, b := New(budget), New(budget)

	a.Event(events.New(events.LoopDetectionData{Message: "aaaa"}))
	a.Event(events.New(events.LoopDetectionData{Message: "bbbb"}))
	b.Event(events.New(events.LoopDetectionData{Message: "cccc"}))
	if keys := noticeKeys(a); len(keys) != 2 {
		t.Fatalf("a holds %v under the budget, want both", keys)
	}

	b.Event(events.New(events.LoopDetectionData{Message: "dddd"}))
	if a.Contains("notice:0") {
		t.Fatal("the daemon-wide budget kept the oldest notice past its limit")
	}
	if !a.Contains("notice:1") || !b.Contains("notice:0") || !b.Contains("notice:1") {
		t.Fatal("the budget evicted more than the oldest notice")
	}
	if keys := noticeKeys(a); len(keys) != 1 || keys[0] != "notice:1" {
		t.Fatalf("a's snapshot = %v, want only notice:1", keys)
	}
	if used := budgetUsed(budget); used != a.notices.bytes+b.notices.bytes {
		t.Fatalf("budget used = %d, want the %d bytes the two rings hold", used, a.notices.bytes+b.notices.bytes)
	}
}

func TestCloseReturnsTheOverlaysBytesToTheBudget(t *testing.T) {
	budget := NewBudget(DefaultBudgetBytes)
	a, b := New(budget), New(budget)
	a.Event(events.New(events.LoopDetectionData{Message: "a"}))
	b.Event(events.New(events.LoopDetectionData{Message: "b"}))
	before := budgetUsed(budget)

	a.Close()
	if used := budgetUsed(budget); used == 0 || used >= before {
		t.Fatalf("budget used = %d after closing a (was %d), want b's bytes only", used, before)
	}
	b.Close()
	if used := budgetUsed(budget); used != 0 {
		t.Fatalf("budget used = %d after closing both, want 0", used)
	}
	b.Close()
	none(t, b.Event(events.New(events.LoopDetectionData{Message: "after close"})))
	if used := budgetUsed(budget); used != 0 {
		t.Fatalf("a closed overlay charged the budget %d bytes", used)
	}
}

func TestRingEvictionReturnsBytesToTheBudget(t *testing.T) {
	budget := NewBudget(DefaultBudgetBytes)
	o := New(budget)
	for range 60 {
		o.Event(events.New(events.LoopDetectionData{Message: "loop"}))
	}
	if used := budgetUsed(budget); used != o.notices.bytes {
		t.Fatalf("budget used = %d, ring holds %d", used, o.notices.bytes)
	}
}

func budgetUsed(b *Budget) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func TestAWarningNoticeKeepsItsTitleMessageHintAndSource(t *testing.T) {
	o := newOverlay()
	item := upserted(t, one(t, o.Event(events.New(events.WarningData{Message: "context nearly full", Source: "hook", Title: "Context", Hint: "compact soon"}))))
	if item.Item.Description != "Context" || item.Item.Text != "context nearly full" {
		t.Fatalf("warning = %q / %q, want its title and message", item.Item.Description, item.Item.Text)
	}
	var raw map[string]map[string]string
	if err := json.Unmarshal(item.Item.Raw, &raw); err != nil {
		t.Fatalf("warning Raw is not JSON: %v", err)
	}
	if raw["warning"]["hint"] != "compact soon" || raw["warning"]["source"] != "hook" || raw["warning"]["title"] != "Context" {
		t.Fatalf("warning Raw = %v, want its source, title and hint", raw)
	}
}

func TestAnUnrecordedCancellationIsAWarningNotAnError(t *testing.T) {
	o := newOverlay()
	item := upserted(t, one(t, o.Event(events.New(events.ErrorData{Error: context.Canceled.Error()}))))
	if item.Item.EventKind != appwire.ThreadItemEventKindWarning || item.Item.Text != context.Canceled.Error() {
		t.Fatalf("cancellation notice = %s %q, want a warning", item.Item.EventKind, item.Item.Text)
	}
}

func TestBudgetEvictionFreesAnIdleOverlaysNotices(t *testing.T) {
	probe := newOverlay()
	probe.Event(events.New(events.LoopDetectionData{Message: "aaaa"}))
	size := encodedSize(upserted(t, one(t, probe.Event(events.New(events.LoopDetectionData{Message: "aaaa"})))))
	budget := NewBudget(3*size + size/2)
	idle, busy := New(budget), New(budget)
	idle.Event(events.New(events.LoopDetectionData{Message: "aaaa"}))
	idle.Event(events.New(events.LoopDetectionData{Message: "bbbb"}))

	busy.Event(events.New(events.LoopDetectionData{Message: "cccc"}))
	busy.Event(events.New(events.LoopDetectionData{Message: "dddd"}))

	// Read the idle overlay's ring without letting it run: the budget alone
	// must have dropped the evicted payload.
	budget.mu.Lock()
	retained := 0
	for _, n := range idle.notices.notices {
		if n.charge.item != nil {
			retained++
		}
	}
	budget.mu.Unlock()
	if retained != 1 {
		t.Fatalf("the idle overlay still references %d notice payloads, want 1 after the budget evicted its oldest", retained)
	}
}

func TestANoticeLargerThanTheBudgetIsDropped(t *testing.T) {
	budget := NewBudget(64)
	o := New(budget)
	none(t, o.Event(events.New(events.LoopDetectionData{Message: "a notice bigger than the whole budget"})))
	if keys := noticeKeys(o); len(keys) != 0 {
		t.Fatalf("ring = %v, want empty", keys)
	}
	if used := budgetUsed(budget); used != 0 {
		t.Fatalf("budget used = %d, want 0", used)
	}
}

func TestANoticeLargerThanTheRingIsDropped(t *testing.T) {
	o := newOverlay()
	none(t, o.Event(events.New(events.LoopDetectionData{Message: strings.Repeat("x", maxNoticeBytes)})))
	if keys := noticeKeys(o); len(keys) != 0 {
		t.Fatalf("ring = %v, want empty", keys)
	}
}
