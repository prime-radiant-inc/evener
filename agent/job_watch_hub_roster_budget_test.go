package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
)

const testRosterSegmentRunes = 57344
const testRosterDroppedRefs = 64

func rosterJSONFieldForTest[T any](t *testing.T, fields map[string]json.RawMessage, key string) T {
	t.Helper()
	var value T
	if raw, ok := fields[key]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("roster field %s: %v", key, err)
		}
	}
	return value
}

func rosterSegmentForTest(frame string) string {
	var out strings.Builder
	for line := range strings.SplitSeq(frame, "\n") {
		if strings.HasPrefix(line, "roster_") || strings.HasPrefix(line, "recovery: ") {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func rosterDropForTest(t *testing.T, frame string) (int, []string) {
	t.Helper()
	count, err := strconv.Atoi(rosterFrameField(frame, "roster_dropped: "))
	if err != nil {
		t.Fatalf("missing structured dropped count: %v", err)
	}
	var refs []string
	if err := json.Unmarshal([]byte(rosterFrameField(frame, "roster_dropped_refs: ")), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs) > testRosterDroppedRefs || len(refs) > count {
		t.Fatalf("dropped=%d refs=%d", count, len(refs))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rosterFrameField(frame, "roster_recovery: ")), &fields); err != nil {
		t.Fatal(err)
	}
	recovery := struct {
		PerRef     []string
		SmallFleet string
		Unlisted   int
	}{
		PerRef:     rosterJSONFieldForTest[[]string](t, fields, "perRef"),
		SmallFleet: rosterJSONFieldForTest[string](t, fields, "smallFleet"),
		Unlisted:   rosterJSONFieldForTest[int](t, fields, "unlisted"),
	}
	if len(recovery.PerRef) != 2 || recovery.PerRef[0] != "job_status" || recovery.PerRef[1] != "read_transcript" || recovery.SmallFleet != "job_list" || recovery.Unlisted != count-len(refs) {
		t.Fatalf("recovery ladder=%+v", recovery)
	}
	return count, refs
}

func TestHubRosterSegmentBoundsWholeEntriesAndCoalescing(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	makeBatch := func(prefix string) []appwire.RosterEvent {
		rows := make([]appwire.RosterEvent, 180)
		for i := range rows {
			rows[i] = appwire.RosterEvent{AttentionEntry: appwire.AttentionEntry{Ref: fmt.Sprintf("remote:%s-%03d", prefix, i), ID: fmt.Sprintf("%s-%03d", prefix, i), Title: strings.Repeat("x", 500), Level: "needs_you"}, Kind: "needs_you"}
		}
		return rows
	}
	var inputDropped int
	var previousKept int
	var previous string
	for i, prefix := range []string{"a", "b", "c"} {
		rows := makeBatch(prefix)
		soloJM, soloID := rosterTestWatch(t)
		if err := soloJM.onHubRoster(soloID, rosterUpdate(1, i == 0, rows...)); err != nil {
			t.Fatal(err)
		}
		soloEvents := rosterTestEvents(t, soloJM, soloID)
		soloFrame := soloEvents[0].Frame
		soloDropped, _ := rosterDropForTest(t, soloFrame)
		soloKept := len(rosterFrameEntries(t, soloFrame))
		if err := jm.onHubRoster(id, rosterUpdate(uint64(i+1), i == 0, rows...)); err != nil {
			t.Fatal(err)
		}
		events := rosterTestEvents(t, jm, id)
		frame := events[len(events)-1].Frame
		if n := utf8.RuneCountInString(rosterSegmentForTest(frame)); n > testRosterSegmentRunes {
			t.Fatalf("batch %d segment=%d runes", i, n)
		}
		kept := rosterFrameEntries(t, frame)
		dropped, refs := rosterDropForTest(t, frame)
		if i == 0 {
			if dropped != len(rows)-len(kept) || len(refs) != testRosterDroppedRefs {
				t.Fatalf("first dropped=%d kept=%d refs=%d", dropped, len(kept), len(refs))
			}
		} else {
			// Every batch has disjoint refs. The already-truncated predecessor's
			// missing entries must remain counted beside this merge's own drops.
			wantDropped := inputDropped + soloDropped + previousKept + soloKept - len(kept)
			if dropped != wantDropped {
				t.Fatalf("merge dropped=%d want=%d", dropped, wantDropped)
			}
			if strings.Count(frame, "\nroster_recovery: ") != 1 {
				t.Fatal("merge did not preserve exactly one recovery field")
			}
		}
		if rosterFrameField(frame, "roster_resync: ") != "" {
			t.Fatal("truncation recovery falsely became authoritative resync")
		}
		if i > 0 && previous != "" {
			// The journal uses the real pending-send coalescer, not a test merger.
			found := false
			for _, row := range kept {
				if strings.HasPrefix(row.Ref, "remote:a-") {
					found = true
				}
			}
			if !found {
				t.Fatal("coalescing discarded the predecessor population")
			}
		}
		inputDropped = dropped
		previousKept = len(kept)
		previous = frame
	}
}

func TestHubRosterSegmentMeasuresCompositeEncodings(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"fresh", "mutation"} {
		t.Run(form, func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), nil)
			jm := root.jobManager
			jm.enqueue = root.enqueueJobNotification
			watch, err := jm.configureHubRosterWatch(t.Context(), "local:manager", "")
			if err != nil {
				t.Fatal(err)
			}
			id := watch.WatchID
			complete, truncated := false, true
			ask := appwire.AttentionEntry{Ref: "remote:asking-lane", RosterInstanceID: "instance-token", ID: "thread-token", Title: strings.Repeat("q", 200), Project: "/home/jesse/git/prime-radiant-inc/evener", Level: "needs_you", AskPending: true, AskBootGeneration: "100", AskRevision: 4, CompositeTotal: 11, CalllessCount: 2, AskPageable: true, PublishedAt: 1700000000123, PendingQuestion: &appwire.PendingQuestion{Question: strings.Repeat("q", 200), Options: []string{strings.Repeat("o", 80), strings.Repeat("p", 80)}, Count: 30, Complete: &complete, Truncated: &truncated}}
			for i := range 11 {
				turn := fmt.Sprintf("0123456789abcdefghijk%03d", i)
				if form == "mutation" {
					turn = fmt.Sprintf("turn_m%d", i)
				}
				ask.CompositeList = append(ask.CompositeList, appwire.PendingAskCall{TurnID: turn, CallID: fmt.Sprintf("call_%024d", i)})
			}
			ask.PendingQuestion.TurnID, ask.PendingQuestion.CallID = ask.CompositeList[0].TurnID, ask.CompositeList[0].CallID
			for _, population := range []int{32, 256} {
				rows := []appwire.RosterEvent{{AttentionEntry: ask, Kind: "needs_you"}}
				for i := 1; i < population; i++ {
					rows = append(rows, appwire.RosterEvent{AttentionEntry: appwire.AttentionEntry{Ref: fmt.Sprintf("local:%022d", i), ID: fmt.Sprintf("%022d", i), RosterInstanceID: fmt.Sprintf("%022d", i), Title: fmt.Sprintf("lane-%d", i), Project: "/home/jesse/git/prime-radiant-inc/evener", Level: "idle", PublishedAt: 1700000000123}, Kind: "idle"})
				}
				if err := jm.onHubRoster(id, rosterUpdate(uint64(population), population == 32, rows...)); err != nil {
					t.Fatal(err)
				}
				events := rosterTestEvents(t, jm, id)
				frame := events[len(events)-1].Frame
				segment := rosterSegmentForTest(frame)
				body := strings.TrimSuffix(frame, segment)
				if n := utf8.RuneCountInString(segment); n > testRosterSegmentRunes {
					t.Fatalf("%d-row segment=%d", population, n)
				}
				if n := utf8.RuneCountInString(body); n > 4096 {
					t.Fatalf("body=%d runes", n)
				}
				var rawRows []map[string]json.RawMessage
				if err := json.Unmarshal([]byte(rosterFrameField(frame, "roster_events: ")), &rawRows); err != nil {
					t.Fatal(err)
				}
				encodedRows := make([]struct {
					CompositeList    []appwire.PendingAskCall
					CompositeTotal   int
					TruncatedList    bool
					ListReason       string
					CalllessCount    int
					RosterInstanceID string
					ID               string
				}, len(rawRows))
				for i, fields := range rawRows {
					encodedRows[i].CompositeList = rosterJSONFieldForTest[[]appwire.PendingAskCall](t, fields, "compositeList")
					encodedRows[i].CompositeTotal = rosterJSONFieldForTest[int](t, fields, "compositeTotal")
					encodedRows[i].TruncatedList = rosterJSONFieldForTest[bool](t, fields, "truncatedList")
					encodedRows[i].ListReason = rosterJSONFieldForTest[string](t, fields, "listReason")
					encodedRows[i].CalllessCount = rosterJSONFieldForTest[int](t, fields, "calllessCount")
					encodedRows[i].RosterInstanceID = rosterJSONFieldForTest[string](t, fields, "rosterInstanceId")
					encodedRows[i].ID = rosterJSONFieldForTest[string](t, fields, "threadId")
				}
				if len(encodedRows) == 0 || len(encodedRows[0].CompositeList) != 10 || encodedRows[0].CompositeTotal != 11 || !encodedRows[0].TruncatedList || encodedRows[0].ListReason != "pageable" || encodedRows[0].CalllessCount != 2 || encodedRows[0].RosterInstanceID != "instance-token" || encodedRows[0].ID != "thread-token" {
					t.Fatalf("asking payload=%+v", encodedRows)
				}
				for i, composite := range encodedRows[0].CompositeList {
					if composite != ask.CompositeList[i] {
						t.Fatalf("composite %d=%+v want=%+v", i, composite, ask.CompositeList[i])
					}
				}
				delivered, _, _ := root.filterDeliverableJobNotifications(root.drainJobNotifications())
				if len(delivered) != 1 {
					t.Fatalf("model-visible deliveries=%d", len(delivered))
				}
				modelFrame := formatJobNotificationBlock(delivered[0].notification, notificationExcerpt{}, false)
				if rosterSegmentForTest(modelFrame) != segment {
					t.Fatal("notification formatting changed the bounded roster segment")
				}
				t.Logf("%s population=%d body=%d segment=%d fullFrameBytes=%d modelFrameBytes=%d kept=%d", form, population, utf8.RuneCountInString(body), utf8.RuneCountInString(segment), len([]byte(frame)), len([]byte(modelFrame)), len(encodedRows))
			}
		})
	}
}

func TestHubRosterSegmentWholeEntryBoundary(t *testing.T) {
	t.Parallel()
	frameFor := func(title string) string {
		jm, id := rosterTestWatch(t)
		row := appwire.RosterEvent{AttentionEntry: appwire.AttentionEntry{Ref: "local:boundary", Title: title, Level: "needs_you"}, Kind: "needs_you"}
		if err := jm.onHubRoster(id, rosterUpdate(1, true, row)); err != nil {
			t.Fatal(err)
		}
		return rosterTestEvents(t, jm, id)[0].Frame
	}
	// Adding ASCII title data increases the real encoding one rune per byte.
	// Measure fixed envelope overhead, independently of the budget algorithm.
	padding := testRosterSegmentRunes - utf8.RuneCountInString(rosterSegmentForTest(frameFor("")))
	exact := frameFor(strings.Repeat("x", padding))
	if len(rosterFrameEntries(t, exact)) != 1 || utf8.RuneCountInString(rosterSegmentForTest(exact)) != testRosterSegmentRunes || rosterFrameField(exact, "roster_dropped: ") != "" {
		t.Fatal("the exact-boundary entry did not fit intact")
	}
	over := frameFor(strings.Repeat("x", padding+1))
	count, refs := rosterDropForTest(t, over)
	if len(rosterFrameEntries(t, over)) != 0 || count != 1 || len(refs) != 1 || refs[0] != "local:boundary" {
		t.Fatal("one-rune overflow was not a whole-entry drop")
	}
	current := frameFor("replacement-token")
	replaced := mergeHubRosterFrames(over, current)
	if replaced != current {
		t.Fatal("authoritative initial retained predecessor entries or dropped markers")
	}
}

func TestHubRosterMergePreservesCurrentBody(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	row := appwire.RosterEvent{AttentionEntry: appwire.AttentionEntry{Ref: "local:body-token", Level: "idle"}, Kind: "idle"}
	if err := jm.onHubRoster(id, rosterUpdate(1, false, row)); err != nil {
		t.Fatal(err)
	}
	frame := rosterTestEvents(t, jm, id)[0].Frame
	body := "message-token\n\n" + strings.TrimSuffix(frame, rosterSegmentForTest(frame))
	current := body + rosterSegmentForTest(frame)
	merged := mergeHubRosterFrames(frame, current)
	if got := strings.TrimSuffix(merged, rosterSegmentForTest(merged)); got != body {
		t.Fatalf("coalescing changed body: got=%q want=%q", got, body)
	}
}
