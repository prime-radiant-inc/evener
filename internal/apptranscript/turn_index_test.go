package apptranscript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

const testMaxLineBytes = 1 << 20

type boundedFixture struct {
	path      string
	remainder []byte
	times     []time.Time
}

func TestTurnCacheBoundedPagesMatchFullProjection(t *testing.T) {
	fixture := writeBoundedFixture(t)
	full := TurnsFromFile(fixture.path, testMaxLineBytes, sequentialTestProjector())
	cache := NewTurnCache()

	got, cursor := cache.LatestFromFile(fixture.path, testMaxLineBytes, 3, boundedTestProjector)
	want, wantCursor := appwire.WindowTurns(full, 3)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("latest turns differ\n got: %#v\nwant: %#v", got, want)
	}
	if cursor != wantCursor {
		t.Fatalf("cursor=%q want=%q", cursor, wantCursor)
	}

	page := cache.PageFromFile(fixture.path, testMaxLineBytes, cursor, 2, boundedTestProjector)
	wantPage := appwire.PageTurns(full, cursor, 2)
	if !reflect.DeepEqual(page.Turns, wantPage.Data) {
		t.Fatalf("page turns differ\n got: %#v\nwant: %#v", page.Turns, wantPage.Data)
	}
	if page.NextCursor != wantPage.NextCursor {
		t.Fatalf("next cursor=%q want=%q", page.NextCursor, wantPage.NextCursor)
	}

	wantIDs := []string{"turn_system", "turn_1", "turn_2", "turn_3", "turn_4", "turn_5"}
	if gotIDs := turnIDs(full); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("turn IDs=%v want=%v", gotIDs, wantIDs)
	}
	for i, ts := range fixture.times[:4] {
		turn := full[i+1]
		if turn.StartedAt == nil || *turn.StartedAt != ts.Unix() {
			t.Fatalf("%s StartedAt=%v want=%d", turn.ID, turn.StartedAt, ts.Unix())
		}
	}
	if full[2].Usage == nil || full[2].Usage.InputTokens != 11 || full[2].Usage.OutputTokens != 7 || full[2].Usage.TotalTokens != 18 {
		t.Fatalf("turn_2 usage=%+v", full[2].Usage)
	}
	failed := full[5]
	if failed.Status != appwire.TurnStatusFailed || failed.Error == nil {
		t.Fatalf("failed turn=%+v", failed)
	}
	if *failed.Error != (appwire.TurnError{Message: "provider exploded", Source: "provider", Title: "Provider error", Hint: "retry later"}) {
		t.Fatalf("failed error=%+v", failed.Error)
	}
	if got[0].Items[0].ToolName != "read_file" {
		t.Fatalf("bounded legacy tool-result name=%q want=%q", got[0].Items[0].ToolName, "read_file")
	}
}

func TestTurnCacheBoundedPagesCountOnlyVisibleTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "visibility.transcript.jsonl")
	entries := []transcript.Entry{
		{Kind: "entry", Seq: 1, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("visible-1")}},
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnCheckpoint}},
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("visible-2")}},
		{Kind: "entry", Seq: 4, Turn: schema.Turn{Kind: schema.TurnModelSwitch}},
		{Kind: "entry", Seq: 5, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("visible-3")}},
		{Kind: "entry", Seq: 6, Turn: schema.Turn{Kind: schema.TurnAssistant}},
		{Kind: "entry", Seq: 7, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("visible-4")}},
		{Kind: "entry", Seq: 8, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("visible-5")}},
	}
	var data []byte
	for _, entry := range entries {
		data = append(data, marshalEntryLine(t, entry)...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	cache := NewTurnCache()
	got, cursor := cache.LatestFromFile(path, testMaxLineBytes, 3, boundedTestProjector)
	want, wantCursor := appwire.WindowTurns(full, 3)
	if !reflect.DeepEqual(got, want) || cursor != wantCursor {
		t.Fatalf("latest got=(%v,%q) want=(%v,%q)", turnIDs(got), cursor, turnIDs(want), wantCursor)
	}
	if gotIDs := turnIDs(got); !reflect.DeepEqual(gotIDs, []string{"turn_5", "turn_7", "turn_8"}) {
		t.Fatalf("latest IDs=%v", gotIDs)
	}

	page := cache.PageFromFile(path, testMaxLineBytes, cursor, 2, boundedTestProjector)
	wantPage := appwire.PageTurns(full, cursor, 2)
	if !reflect.DeepEqual(page.Turns, wantPage.Data) || page.NextCursor != wantPage.NextCursor {
		t.Fatalf("page got=(%v,%q) want=(%v,%q)", turnIDs(page.Turns), page.NextCursor, turnIDs(wantPage.Data), wantPage.NextCursor)
	}
	if gotIDs := turnIDs(page.Turns); !reflect.DeepEqual(gotIDs, []string{"turn_1", "turn_3"}) {
		t.Fatalf("page IDs=%v", gotIDs)
	}
}

func TestTurnCacheBoundedPreludeFreezesAtFirstAPICall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prelude.transcript.jsonl")
	records := []any{
		transcript.Header{Kind: "header", FormatVersion: 1, SystemPrompt: "first header"},
		transcript.APICall{Kind: "api_call", Seq: 1},
		transcript.Header{Kind: "header", FormatVersion: 1, SystemPrompt: "later header"},
		userEntry(2, "hello"),
		userEntry(3, "later"),
	}
	var data []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	assertBoundedLatestMatchesFull(t, path, 3)
	cache := NewTurnCache()
	got, _ := cache.LatestFromFile(path, testMaxLineBytes, 3, boundedTestProjector)
	if text := turnText(got[0]); text != "first header" {
		t.Fatalf("prelude text=%q want=%q", text, "first header")
	}
	full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	page := cache.PageFromFile(path, testMaxLineBytes, "2", 2, boundedTestProjector)
	wantPage := appwire.PageTurns(full, "2", 2)
	if !reflect.DeepEqual(page.Turns, wantPage.Data) || page.NextCursor != wantPage.NextCursor {
		t.Fatalf("prelude page got=(%v,%q) want=(%v,%q)", turnIDs(page.Turns), page.NextCursor, turnIDs(wantPage.Data), wantPage.NextCursor)
	}
}

func TestTurnCacheBoundedPageUsesToolNamesAtRangeStart(t *testing.T) {
	t.Run("later call ID reuse does not rename earlier result", func(t *testing.T) {
		path := writeEntries(t,
			assistantToolCallEntry(1, "shared", "first_tool", `{}`),
			toolResultEntry(2, "shared", "", "first output"),
			userEntry(3, "boundary"),
			assistantToolCallEntry(4, "shared", "later_tool", `{}`),
		)
		assertBoundedLatestMatchesFull(t, path, 3)
	})

	t.Run("communicate result before range deletes call ID", func(t *testing.T) {
		path := writeEntries(t,
			assistantToolCallEntry(1, "shared", "communicate", `{"message":"sent"}`),
			toolResultEntry(2, "shared", "", "delivered"),
			userEntry(3, "boundary"),
			toolResultEntry(4, "shared", "", "orphan output"),
		)
		assertBoundedLatestMatchesFull(t, path, 2)
	})
}

func TestTurnCacheBoundedReadCompletesAppendedPartialLine(t *testing.T) {
	fixture := writeBoundedFixture(t)
	cache := NewTurnCache()
	before, _ := cache.LatestFromFile(fixture.path, testMaxLineBytes, 3, boundedTestProjector)
	if got := turnIDs(before); !reflect.DeepEqual(got, []string{"turn_3", "turn_4", "turn_5"}) {
		t.Fatalf("before append IDs=%v", got)
	}

	f, err := os.OpenFile(fixture.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(fixture.remainder, '\n')); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	full := TurnsFromFile(fixture.path, testMaxLineBytes, sequentialTestProjector())
	got, cursor := cache.LatestFromFile(fixture.path, testMaxLineBytes, 3, boundedTestProjector)
	want, wantCursor := appwire.WindowTurns(full, 3)
	if !reflect.DeepEqual(got, want) || cursor != wantCursor {
		t.Fatalf("after append got=(%#v,%q) want=(%#v,%q)", got, cursor, want, wantCursor)
	}
	if gotIDs := turnIDs(got); !reflect.DeepEqual(gotIDs, []string{"turn_4", "turn_5", "turn_6"}) {
		t.Fatalf("after append IDs=%v", gotIDs)
	}
	if got[2].StartedAt == nil || *got[2].StartedAt != fixture.times[4].Unix() {
		t.Fatalf("completed partial StartedAt=%v want=%d", got[2].StartedAt, fixture.times[4].Unix())
	}
}

func TestTurnCacheIndexesOnlyAppendedSuffixAndBoundsProjection(t *testing.T) {
	path := writeNumberedTranscript(t, 100)
	cache := NewTurnCache()
	var stats []ReadStats
	previous := observeTurnIndexRead
	observeTurnIndexRead = func(stat ReadStats) { stats = append(stats, stat) }
	t.Cleanup(func() { observeTurnIndexRead = previous })

	latest, cursor := cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
	if len(latest) != 40 || cursor != "60" {
		t.Fatalf("cold latest=(%d,%q) want=(40,%q)", len(latest), cursor, "60")
	}
	if got := stats[len(stats)-1]; got.IndexedBytes <= 0 || got.ProjectedTurns != 40 {
		t.Fatalf("cold stats=%+v want positive indexed bytes and 40 projected", got)
	}
	latest, cursor = cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
	if len(latest) != 40 || cursor != "60" {
		t.Fatalf("warm latest=(%d,%q) want=(40,%q)", len(latest), cursor, "60")
	}
	if got := stats[len(stats)-1]; got.IndexedBytes != 0 || got.ProjectedTurns != 40 {
		t.Fatalf("warm latest stats=%+v want indexed=0 projected=40", got)
	}

	line := numberedEntryLine(t, 101, strings.Repeat("x", 257))
	appendFile(t, path, line)
	latest, cursor = cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
	if len(latest) != 40 || cursor != "61" {
		t.Fatalf("appended latest=(%d,%q) want=(40,%q)", len(latest), cursor, "61")
	}
	if got := stats[len(stats)-1]; got.IndexedBytes != int64(len(line)) || got.ProjectedTurns != 40 {
		t.Fatalf("append stats=%+v want indexed=%d projected=40", got, len(line))
	}

	page := cache.PageFromFile(path, testMaxLineBytes, cursor, 30, boundedTestProjector)
	if len(page.Turns) != 30 || page.NextCursor != "31" {
		t.Fatalf("older page=(%d,%q) want=(30,%q)", len(page.Turns), page.NextCursor, "31")
	}
	if got := stats[len(stats)-1]; got.IndexedBytes != 0 || got.ProjectedTurns != 30 {
		t.Fatalf("warm page stats=%+v want indexed=0 projected=30", got)
	}
}

func TestTurnCacheAppendIndexWorkIsBoundedBySuffix(t *testing.T) {
	type observedWork struct {
		count int
		line  int
		stat  ReadStats
	}
	var work []observedWork
	for _, count := range []int{100, 1_000, 10_000} {
		path := writeNumberedTranscript(t, count)
		cache := NewTurnCache()
		cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
		basePath := path + ".appwire-index.json"
		baseBefore, err := os.ReadFile(basePath)
		if err != nil {
			t.Fatal(err)
		}

		line := numberedEntryLine(t, count+1, "appended")
		appendFile(t, path, line)
		var stat ReadStats
		previous := observeTurnIndexRead
		observeTurnIndexRead = func(got ReadStats) { stat = got }
		cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
		observeTurnIndexRead = previous

		baseAfter, err := os.ReadFile(basePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(baseAfter, baseBefore) {
			t.Fatalf("entries=%d: append rewrote the base snapshot (%d bytes before, %d after)", count, len(baseBefore), len(baseAfter))
		}
		if stat.IndexedBytes != int64(len(line)) || stat.ProjectedTurns != 40 {
			t.Fatalf("entries=%d: read stats=%+v want indexed=%d projected=40", count, stat, len(line))
		}
		if stat.indexBytesCopied > 8<<10 {
			t.Fatalf("entries=%d: copied %d index bytes, want <= %d suffix-bounded bytes", count, stat.indexBytesCopied, 8<<10)
		}
		if stat.indexBytesSerialized <= 0 || stat.indexBytesSerialized > 32<<10 {
			t.Fatalf("entries=%d: serialized %d index bytes, want 1..%d suffix-bounded bytes", count, stat.indexBytesSerialized, 32<<10)
		}
		if stat.indexBytesPersisted <= 0 || stat.indexBytesPersisted > 16<<10 {
			t.Fatalf("entries=%d: persisted %d index bytes, want 1..%d suffix-bounded bytes", count, stat.indexBytesPersisted, 16<<10)
		}
		work = append(work, observedWork{count: count, line: len(line), stat: stat})
	}

	baseline := work[0].stat
	for _, got := range work[1:] {
		if got.stat.indexBytesCopied > baseline.indexBytesCopied+8<<10 ||
			got.stat.indexBytesSerialized > baseline.indexBytesSerialized+8<<10 ||
			got.stat.indexBytesPersisted > baseline.indexBytesPersisted+8<<10 {
			t.Fatalf("entries=%d: append index work grew with history: baseline=%+v got=%+v", got.count, baseline, got.stat)
		}
	}
}

func TestTurnCacheToolHeavyAppendDoesNotPersistCumulativeResolver(t *testing.T) {
	type work struct {
		count  int
		bytes  uint64
		allocs float64
	}
	var observed []work
	for _, count := range []int{100, 1_000, 10_000} {
		t.Run(fmt.Sprintf("calls=%d", count), func(t *testing.T) {
			path := writeToolHeavyTranscript(t, count)
			cache := NewTurnCache()
			cache.LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
			line := numberedEntryLine(t, count+1, "fixed-size-append")
			appendFile(t, path, line)

			var stat ReadStats
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(got ReadStats) { stat = got }
			got, cursor := cache.LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
			observeTurnIndexRead = previous
			full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
			want, wantCursor := appwire.WindowTurns(full, 1)
			if !reflect.DeepEqual(got, want) || cursor != wantCursor {
				t.Fatalf("append projection got=(%#v,%q) want=(%#v,%q)", got, cursor, want, wantCursor)
			}

			if stat.resolverEntriesCopied != 0 || stat.indexBytesCopied != 0 || stat.journalRecords != 1 ||
				stat.indexBytesSerialized > 8<<10 || stat.indexBytesPersisted > 4<<10 {
				t.Fatalf("tool history leaked into append work: %+v", stat)
			}
			journal, err := os.ReadFile(path + ".appwire-index.json.journal")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(journal, []byte(`"tool_names"`)) {
				t.Fatalf("journal contains cumulative tool resolver (%d bytes)", len(journal))
			}

			seq := count + 2
			allocs := testing.AllocsPerRun(1, func() {
				appendFile(t, path, numberedEntryLine(t, seq, "fixed-size-append"))
				cache.LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
				seq++
			})
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			appendFile(t, path, numberedEntryLine(t, seq, "fixed-size-append"))
			cache.LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			if allocs > 256 || allocated > 64<<10 {
				t.Fatalf("actual append allocations grew with tool history: allocs=%.0f bytes=%d", allocs, allocated)
			}
			observed = append(observed, work{count: count, bytes: allocated, allocs: allocs})
		})
	}
	baseline := observed[0]
	for _, got := range observed[1:] {
		if got.bytes > baseline.bytes+16<<10 || got.allocs > baseline.allocs+32 {
			t.Fatalf("calls=%d actual append work grew with history: baseline=%+v got=%+v", got.count, baseline, got)
		}
	}
}

func TestTurnCacheToolProjectionSeedsMatchSequentialProjection(t *testing.T) {
	multiple := transcript.Entry{Kind: "entry", Seq: 5, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "ordinary", Content: "first"}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "communicate", Content: "hidden"}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "orphan", Content: "third"}},
	}}}}
	path := writeEntries(t,
		assistantToolCallEntry(1, "ordinary", "read_file", `{}`),
		assistantToolCallEntry(2, "communicate", "communicate", `{"message":"sent"}`),
		toolResultEntry(3, "communicate", "", "hidden"),
		toolResultEntry(4, "communicate", "", "orphan"),
		multiple,
	)
	full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	cache := NewTurnCache()
	got, cursor := cache.LatestFromFile(path, testMaxLineBytes, len(full), boundedTestProjector)
	if !reflect.DeepEqual(got, full) || cursor != "" {
		t.Fatalf("seed projection differs\n got: %#v\nwant: %#v", got, full)
	}
	index, err := readTurnIndex(path + ".appwire-index.json")
	if err != nil {
		t.Fatal(err)
	}
	if seed := index.Records[3].ToolSeed; seed == nil {
		t.Fatal("orphan missing-name result has no explicit seed")
	} else if got, ok := seed["communicate"]; !ok || got != "" {
		t.Fatalf("orphan seed=%v, want explicit empty resolution", seed)
	}
	if seed := index.Records[4].ToolSeed; seed["ordinary"] != "read_file" || seed["communicate"] != "" || seed["orphan"] != "" {
		t.Fatalf("multiple-result seed=%v", seed)
	}
}

func TestTurnCacheFailedAPICallSelectionIsVisibleRankBounded(t *testing.T) {
	for _, count := range []int{100, 1_000, 10_000} {
		t.Run(fmt.Sprintf("failed=%d", count), func(t *testing.T) {
			path := writeFailedAPICalls(t, count)
			cache := NewTurnCache()
			cache.LatestFromFile(path, testMaxLineBytes, 20, boundedTestProjector)
			var stat ReadStats
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(got ReadStats) { stat = got }
			got, cursor := cache.LatestFromFile(path, testMaxLineBytes, 20, boundedTestProjector)
			observeTurnIndexRead = previous
			full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
			want, wantCursor := appwire.WindowTurns(full, 20)
			if !reflect.DeepEqual(got, want) || cursor != wantCursor {
				t.Fatalf("latest differs: got=(%v,%q) want=(%v,%q)", turnIDs(got), cursor, turnIDs(want), wantCursor)
			}
			if stat.resolverHistoryVisits != 0 || stat.recordVisits > 80 {
				t.Fatalf("warm selection work=%+v, want no resolver replay and <=80 rope/rank visits", stat)
			}
		})
	}
}

func TestTurnCacheGrowthRewriteAnchorMismatchRebuilds(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh=%v", fresh), func(t *testing.T) {
			path := writeNumberedTranscript(t, 40)
			cache := NewTurnCache()
			cache.LatestFromFile(path, testMaxLineBytes, 5, boundedTestProjector)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 60; i++ {
				if _, err := file.Write(numberedEntryLine(t, i, fmt.Sprintf("replacement-%03d", i))); err != nil {
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fileIdentity(before) != fileIdentity(after) || after.Size() <= before.Size() {
				t.Fatalf("fixture identity/growth invalid: before=%d after=%d", before.Size(), after.Size())
			}
			if fresh {
				cache = NewTurnCache()
			}
			var stat ReadStats
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(got ReadStats) { stat = got }
			got, cursor := cache.LatestFromFile(path, testMaxLineBytes, 5, boundedTestProjector)
			observeTurnIndexRead = previous
			full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
			want, wantCursor := appwire.WindowTurns(full, 5)
			if !reflect.DeepEqual(got, want) || cursor != wantCursor || !stat.rebuilt {
				t.Fatalf("anchor recovery got=(%v,%q) stats=%+v want=(%v,%q) rebuild", turnIDs(got), cursor, stat, turnIDs(want), wantCursor)
			}
			if stat.anchorBytesRead <= 0 || stat.anchorBytesRead > 2*turnIndexAnchorBytes {
				t.Fatalf("anchor bytes=%d want 1..%d", stat.anchorBytesRead, 2*turnIndexAnchorBytes)
			}
		})
	}
}

func TestTurnCacheSidecarStoresRecordSeedsAndBoundedAnchors(t *testing.T) {
	path := writeEntries(t,
		assistantToolCallEntry(1, "call", "read_file", `{}`),
		toolResultEntry(2, "call", "", "done"),
	)
	NewTurnCache().LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
	data, err := os.ReadFile(path + ".appwire-index.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"tool_seed"`, `"first_anchor"`, `"tail_anchor"`} {
		if !bytes.Contains(data, []byte(key)) {
			t.Errorf("sidecar missing %s", key)
		}
	}
	if bytes.Contains(data, []byte(`"tool_names"`)) || bytes.Contains(data, []byte(`"tool_names_before"`)) {
		t.Errorf("sidecar retains cumulative/checkpoint tool state")
	}
}

func TestTurnCacheSuffixAdvancementDoesNotMutatePublishedHistory(t *testing.T) {
	path := writeNumberedTranscript(t, 100)
	cache := NewTurnCache()
	cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)

	cache.mu.Lock()
	published := *cache.entries[path].turnIndex
	cache.mu.Unlock()
	if published.recordCount() != 100 {
		t.Fatalf("published record count=%d want=100", published.recordCount())
	}

	appendFile(t, path, numberedEntryLine(t, 101, "appended"))
	advanced := make(chan struct{})
	go func() {
		cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
		close(advanced)
	}()

	oldTurns, oldProjected := projectIndexedRange(path, published, 60, 100, boundedTestProjector)
	<-advanced
	if published.recordCount() != 100 || published.logicalTurnCount() != 100 {
		t.Fatalf("suffix advancement mutated published history: records=%d turns=%d", published.recordCount(), published.logicalTurnCount())
	}
	if oldProjected != 40 || len(oldTurns) != 40 || turnText(oldTurns[len(oldTurns)-1]) != "entry-100" {
		t.Fatalf("published snapshot changed during advancement: projected=%d turns=%d tail=%q", oldProjected, len(oldTurns), turnText(oldTurns[len(oldTurns)-1]))
	}

	cache.mu.Lock()
	current := *cache.entries[path].turnIndex
	cache.mu.Unlock()
	if current.recordCount() != 101 || current.logicalTurnCount() != 101 {
		t.Fatalf("advanced index=(records=%d turns=%d) want=(101,101)", current.recordCount(), current.logicalTurnCount())
	}
}

func TestTurnCacheRestartLoadsBasePlusValidJournalDeltas(t *testing.T) {
	path := writeNumberedTranscript(t, 100)
	cache := NewTurnCache()
	cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
	basePath := path + ".appwire-index.json"
	baseBefore, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, numberedEntryLine(t, 101, "delta-one"))
	appendFile(t, path, assistantToolCallLine(t, 102, "restart-call", "restart_tool"))
	appendFile(t, path, toolResultLine(t, 103, "restart-call", "", "restart output"))
	cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)

	baseAfter, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(baseAfter, baseBefore) {
		t.Fatalf("append rewrote base snapshot: before=%d bytes after=%d", len(baseBefore), len(baseAfter))
	}
	journalPath := basePath + ".journal"
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read delta journal: %v", err)
	}
	if len(journal) == 0 || journal[len(journal)-1] != '\n' {
		t.Fatalf("delta journal is not a complete durable frame: %q", journal)
	}

	var stat ReadStats
	previous := observeTurnIndexRead
	observeTurnIndexRead = func(got ReadStats) { stat = got }
	t.Cleanup(func() { observeTurnIndexRead = previous })
	got, cursor := NewTurnCache().LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
	want, wantCursor := appwire.WindowTurns(TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector()), 40)
	if !reflect.DeepEqual(got, want) || cursor != wantCursor {
		t.Fatalf("restart got=(%#v,%q) want=(%#v,%q)", got, cursor, want, wantCursor)
	}
	if stat.IndexedBytes != 0 {
		t.Fatalf("restart rescanned authoritative transcript: stats=%+v", stat)
	}
	if got[len(got)-1].Items[0].ToolName != "restart_tool" {
		t.Fatalf("restart lost historical tool state: turn=%#v", got[len(got)-1])
	}
}

func TestTurnCacheTruncatedFinalJournalAcceptsPrefixAndRepairsSuffix(t *testing.T) {
	path := writeNumberedTranscript(t, 20)
	cache := NewTurnCache()
	cache.LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
	line21 := numberedEntryLine(t, 21, "delta-21")
	appendFile(t, path, line21)
	cache.LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
	line22 := numberedEntryLine(t, 22, "delta-22")
	appendFile(t, path, line22)
	cache.LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)

	journalPath := path + ".appwire-index.json.journal"
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read delta journal: %v", err)
	}
	if len(journal) == 0 || journal[len(journal)-1] != '\n' {
		t.Fatalf("journal fixture is not complete: %q", journal)
	}
	previousNewline := bytes.LastIndexByte(journal[:len(journal)-1], '\n')
	if previousNewline < 0 {
		t.Fatalf("journal has fewer than two frames: %q", journal)
	}
	validPrefixSize := previousNewline + 1
	truncatedSize := validPrefixSize + (len(journal)-validPrefixSize)/2
	if err := os.WriteFile(journalPath, journal[:truncatedSize], 0o644); err != nil {
		t.Fatal(err)
	}

	var stat ReadStats
	previous := observeTurnIndexRead
	observeTurnIndexRead = func(got ReadStats) { stat = got }
	t.Cleanup(func() { observeTurnIndexRead = previous })
	got, cursor := NewTurnCache().LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
	want, wantCursor := appwire.WindowTurns(TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector()), 10)
	if !reflect.DeepEqual(got, want) || cursor != wantCursor {
		t.Fatalf("truncated-journal recovery got=(%#v,%q) want=(%#v,%q)", got, cursor, want, wantCursor)
	}
	if stat.IndexedBytes != int64(len(line22)) {
		t.Fatalf("truncated-journal recovery stats=%+v want suffix indexed=%d", stat, len(line22))
	}
	repaired, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(repaired) == 0 || repaired[len(repaired)-1] != '\n' {
		t.Fatalf("repaired journal does not end at a complete frame: %q", repaired)
	}

	stat = ReadStats{}
	got, cursor = NewTurnCache().LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
	if !reflect.DeepEqual(got, want) || cursor != wantCursor || stat.IndexedBytes != 0 {
		t.Fatalf("second restart got=(%#v,%q) stats=%+v want=(%#v,%q) with no scan", got, cursor, stat, want, wantCursor)
	}
}

func TestTurnCacheRejectsCorruptOrChainMismatchedJournal(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, []byte) []byte
	}{
		{name: "corrupt JSON", mutate: func(t *testing.T, journal []byte) []byte {
			t.Helper()
			journal = append([]byte(nil), journal...)
			journal[0] = '!'
			return journal
		}},
		{name: "integrity chain mismatch", mutate: func(t *testing.T, journal []byte) []byte {
			t.Helper()
			journal = append([]byte(nil), journal...)
			const marker = `"previous_stamp":"`
			start := bytes.Index(journal, []byte(marker))
			if start < 0 {
				t.Fatalf("journal has no previous stamp: %q", journal)
			}
			start += len(marker)
			if journal[start] == '0' {
				journal[start] = '1'
			} else {
				journal[start] = '0'
			}
			return journal
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeNumberedTranscript(t, 20)
			cache := NewTurnCache()
			cache.LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
			appendFile(t, path, numberedEntryLine(t, 21, "durable delta"))
			cache.LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
			journalPath := path + ".appwire-index.json.journal"
			journal, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatalf("read delta journal: %v", err)
			}
			if err := os.WriteFile(journalPath, test.mutate(t, journal), 0o644); err != nil {
				t.Fatal(err)
			}

			var stat ReadStats
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(got ReadStats) { stat = got }
			t.Cleanup(func() { observeTurnIndexRead = previous })
			got, cursor := NewTurnCache().LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
			want, wantCursor := appwire.WindowTurns(TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector()), 10)
			if !reflect.DeepEqual(got, want) || cursor != wantCursor {
				t.Fatalf("journal recovery got=(%#v,%q) want=(%#v,%q)", got, cursor, want, wantCursor)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if stat.IndexedBytes != info.Size() {
				t.Fatalf("corrupt journal was partly trusted: stats=%+v want authoritative rebuild bytes=%d", stat, info.Size())
			}

			stat = ReadStats{}
			got, cursor = NewTurnCache().LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
			if !reflect.DeepEqual(got, want) || cursor != wantCursor || stat.IndexedBytes != 0 {
				t.Fatalf("post-rebuild restart got=(%#v,%q) stats=%+v want=(%#v,%q) with no scan", got, cursor, stat, want, wantCursor)
			}
		})
	}
}

func TestTurnCacheRebuildsIndexAfterTranscriptReplacement(t *testing.T) {
	path := writeNumberedTranscript(t, 4)
	cache := NewTurnCache()
	warm, _ := cache.LatestFromFile(path, testMaxLineBytes, 2, boundedTestProjector)
	if got := turnText(warm[len(warm)-1]); got != "entry-4" {
		t.Fatalf("warm latest text=%q want=%q", got, "entry-4")
	}

	replacement := append(numberedEntryLine(t, 1, "replacement-one"), numberedEntryLine(t, 2, "replacement-two")...)
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	got, cursor := cache.LatestFromFile(path, testMaxLineBytes, 10, boundedTestProjector)
	if cursor != "" || len(got) != 2 {
		t.Fatalf("replacement latest=(%d,%q) want=(2, empty)", len(got), cursor)
	}
	if texts := []string{turnText(got[0]), turnText(got[1])}; !reflect.DeepEqual(texts, []string{"replacement-one", "replacement-two"}) {
		t.Fatalf("replacement texts=%v", texts)
	}
}

func TestTurnCacheRebuildsIndexAfterSameSizeMiddleReplacementWithRestoredModTime(t *testing.T) {
	path := writeNumberedTranscript(t, 120)
	cache := NewTurnCache()
	cache.LatestFromFile(path, testMaxLineBytes, 3, boundedTestProjector)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldKind := []byte(`"kind":"USER_INPUT"`)
	replacementKind := []byte(`"kind":"CHECKPOINT"`)
	oldText := []byte("entry-60")
	replacementText := []byte("        ")
	if len(oldKind) != len(replacementKind) || len(oldText) != len(replacementText) || !bytes.Contains(data, oldText) {
		t.Fatal("invalid same-size replacement fixture")
	}
	entryStart := bytes.Index(data, oldText)
	kindStart := bytes.LastIndex(data[:entryStart], oldKind)
	if kindStart < 0 {
		t.Fatal("entry kind not found")
	}
	copy(data[kindStart:kindStart+len(oldKind)], replacementKind)
	copy(data[entryStart:entryStart+len(oldText)], replacementText)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("rewrite identity=(size=%d, mod=%v) want=(size=%d, mod=%v)", after.Size(), after.ModTime(), before.Size(), before.ModTime())
	}
	if fileIdentity(after) != fileIdentity(before) {
		t.Fatalf("rewrite replaced inode: before=%q after=%q", fileIdentity(before), fileIdentity(after))
	}

	full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	got, cursor := cache.LatestFromFile(path, testMaxLineBytes, 61, boundedTestProjector)
	want, wantCursor := appwire.WindowTurns(full, 61)
	if !reflect.DeepEqual(got, want) || cursor != wantCursor {
		t.Fatalf("same-size replacement got=(%v,%q) want=(%v,%q)", turnIDs(got), cursor, turnIDs(want), wantCursor)
	}
}

func TestTurnCacheRebuildsStructurallyValidMutatedSidecar(t *testing.T) {
	path := writeNumberedTranscript(t, 8)
	indexPath := path + ".appwire-index.json"
	NewTurnCache().LatestFromFile(path, testMaxLineBytes, 3, boundedTestProjector)
	index, err := readTurnIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	index.Records[len(index.Records)-1].Index = 99
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	var stat ReadStats
	previous := observeTurnIndexRead
	observeTurnIndexRead = func(got ReadStats) { stat = got }
	t.Cleanup(func() { observeTurnIndexRead = previous })
	got, cursor := NewTurnCache().LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
	if cursor != "7" || !reflect.DeepEqual(turnIDs(got), []string{"turn_8"}) || turnText(got[0]) != "entry-8" {
		t.Fatalf("mutated sidecar latest=(%v,%q,%q)", turnIDs(got), cursor, turnText(got[0]))
	}
	if stat.IndexedBytes <= 0 {
		t.Fatalf("mutated sidecar was reused instead of rebuilt: stats=%+v", stat)
	}
}

func TestTurnCacheRebuildsIndexWhenProjectionIdentityChanges(t *testing.T) {
	for _, test := range []struct {
		name        string
		secondCache func(*TurnCache) *TurnCache
	}{
		{name: "memory", secondCache: func(cache *TurnCache) *TurnCache { return cache }},
		{name: "disk", secondCache: func(*TurnCache) *TurnCache { return NewTurnCache() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeNumberedTranscript(t, 4)
			cache := NewTurnCache()
			first, cursor := cache.LatestFromFile(path, testMaxLineBytes, 3, projectionKeepingAllEntries)
			if cursor != "1" || !reflect.DeepEqual(turnIDs(first), []string{"turn_2", "turn_3", "turn_4"}) {
				t.Fatalf("first projection=(%v,%q)", turnIDs(first), cursor)
			}

			var stat ReadStats
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(got ReadStats) { stat = got }
			t.Cleanup(func() { observeTurnIndexRead = previous })
			got, cursor := test.secondCache(cache).LatestFromFile(path, testMaxLineBytes, 3, projectionKeepingOddEntries)
			if cursor != "" || !reflect.DeepEqual(turnIDs(got), []string{"turn_1", "turn_3"}) {
				t.Fatalf("changed projection=(%v,%q) want=([turn_1 turn_3], empty)", turnIDs(got), cursor)
			}
			if stat.IndexedBytes <= 0 {
				t.Fatalf("changed projection reused persisted visibility: stats=%+v", stat)
			}
		})
	}
}

func TestTurnCacheRebuildsSidecarWithInvalidToolSeed(t *testing.T) {
	entries := []transcript.Entry{assistantToolCallEntry(1, "shared", "correct_tool", `{}`)}
	for seq := 2; seq <= 128; seq++ {
		entries = append(entries, userEntry(seq, fmt.Sprintf("entry-%d", seq)))
	}
	entries = append(entries, toolResultEntry(129, "shared", "", "done"))
	path := writeEntries(t, entries...)
	indexPath := path + ".appwire-index.json"
	NewTurnCache().LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
	index, err := readTurnIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	record := &index.Records[128]
	if record.ToolSeed["shared"] != "correct_tool" {
		t.Fatalf("seed fixture=%v", record.ToolSeed)
	}
	record.ToolSeed["shared"] = "wrong_tool"
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, _ := NewTurnCache().LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
	if len(got) != 1 || len(got[0].Items) != 1 || got[0].Items[0].ToolName != "correct_tool" {
		t.Fatalf("invalid seed was reused: turns=%#v", got)
	}
}

func TestTurnCacheRebuildsMissingOrCorruptSidecar(t *testing.T) {
	path := writeNumberedTranscript(t, 8)
	indexPath := path + ".appwire-index.json"
	NewTurnCache().LatestFromFile(path, testMaxLineBytes, 3, boundedTestProjector)

	for _, test := range []struct {
		name   string
		mutate func(*testing.T)
	}{
		{name: "missing", mutate: func(t *testing.T) {
			if err := os.Remove(indexPath); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt", mutate: func(t *testing.T) {
			if err := os.WriteFile(indexPath, []byte("not-json"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Restore a valid sidecar before applying each mutation.
			NewTurnCache().LatestFromFile(path, testMaxLineBytes, 3, boundedTestProjector)
			test.mutate(t)
			var stat ReadStats
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(got ReadStats) { stat = got }
			t.Cleanup(func() { observeTurnIndexRead = previous })

			got, cursor := NewTurnCache().LatestFromFile(path, testMaxLineBytes, 3, boundedTestProjector)
			if cursor != "5" || len(got) != 3 || turnText(got[2]) != "entry-8" {
				t.Fatalf("rebuilt latest=(%d,%q,%q)", len(got), cursor, turnText(got[2]))
			}
			if stat.IndexedBytes <= 0 || stat.ProjectedTurns != 3 {
				t.Fatalf("rebuild stats=%+v", stat)
			}
			if _, err := readTurnIndex(indexPath); err != nil {
				t.Fatalf("rebuilt sidecar: %v", err)
			}
		})
	}
}

func TestTurnCacheReadsLineLargerThanScannerDefault(t *testing.T) {
	const maxLineBytes = 256 << 10
	text := strings.Repeat("large-line-", 8<<10)
	line := numberedEntryLine(t, 1, text)
	if len(line) <= bufioMaxScanTokenSizeForTest || len(line) >= maxLineBytes {
		t.Fatalf("fixture line bytes=%d want >%d and <%d", len(line), bufioMaxScanTokenSizeForTest, maxLineBytes)
	}
	path := filepath.Join(t.TempDir(), "large.transcript.jsonl")
	if err := os.WriteFile(path, line, 0o644); err != nil {
		t.Fatal(err)
	}

	got, cursor := NewTurnCache().LatestFromFile(path, maxLineBytes, 1, boundedTestProjector)
	if cursor != "" || len(got) != 1 || turnText(got[0]) != text {
		t.Fatalf("large-line latest=(%d,%q,text-bytes=%d)", len(got), cursor, len(turnText(got[0])))
	}
}

func TestTurnCacheBoundedReadsEvictBeyondDefaultPathLimit(t *testing.T) {
	dir := t.TempDir()
	cache := NewTurnCache()
	paths := make([]string, defaultTurnCacheSize+1)
	for i := range paths {
		path := filepath.Join(dir, fmt.Sprintf("%02d.transcript.jsonl", i))
		paths[i] = path
		if err := os.WriteFile(path, numberedEntryLine(t, 1, fmt.Sprintf("path-%d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		cache.LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
	}
	if len(cache.entries) != defaultTurnCacheSize {
		t.Fatalf("bounded cache entries=%d want=%d", len(cache.entries), defaultTurnCacheSize)
	}
	if _, ok := cache.entries[paths[0]]; ok {
		t.Fatalf("oldest bounded transcript remains cached")
	}
	if _, ok := cache.entries[paths[len(paths)-1]]; !ok {
		t.Fatalf("newest bounded transcript was evicted")
	}
}

const bufioMaxScanTokenSizeForTest = 64 * 1024

func BenchmarkTurnCacheLatest40(b *testing.B) {
	for _, count := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("entries=%d/cold", count), func(b *testing.B) {
			path := writeNumberedTranscript(b, count)
			indexPath := path + ".appwire-index.json"
			var indexedBytes int64
			var projectedTurns int64
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(stat ReadStats) {
				indexedBytes += stat.IndexedBytes
				projectedTurns += int64(stat.ProjectedTurns)
			}
			defer func() { observeTurnIndexRead = previous }()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
					b.Fatal(err)
				}
				cache := NewTurnCache()
				b.StartTimer()
				cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
			}
			reportReadMetrics(b, indexedBytes, projectedTurns)
		})

		b.Run(fmt.Sprintf("entries=%d/warm", count), func(b *testing.B) {
			path := writeNumberedTranscript(b, count)
			cache := NewTurnCache()
			cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
			var indexedBytes int64
			var projectedTurns int64
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(stat ReadStats) {
				indexedBytes += stat.IndexedBytes
				projectedTurns += int64(stat.ProjectedTurns)
			}
			defer func() { observeTurnIndexRead = previous }()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
			}
			reportReadMetrics(b, indexedBytes, projectedTurns)
		})
	}
}

func BenchmarkTurnCacheLatest40AfterAppend(b *testing.B) {
	for _, count := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("entries=%d", count), func(b *testing.B) {
			path := writeNumberedTranscript(b, count)
			cache := NewTurnCache()
			cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
			var indexedBytes int64
			var projectedTurns int64
			var indexBytesCopied int64
			var indexBytesSerialized int64
			var indexBytesPersisted int64
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(stat ReadStats) {
				indexedBytes += stat.IndexedBytes
				projectedTurns += int64(stat.ProjectedTurns)
				indexBytesCopied += stat.indexBytesCopied
				indexBytesSerialized += stat.indexBytesSerialized
				indexBytesPersisted += stat.indexBytesPersisted
			}
			defer func() { observeTurnIndexRead = previous }()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				appendFile(b, path, numberedEntryLine(b, count+i+1, "appended"))
				cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
			}
			reportReadMetrics(b, indexedBytes, projectedTurns)
			reportIndexWriteMetrics(b, indexBytesCopied, indexBytesSerialized, indexBytesPersisted)
		})
	}
}

func BenchmarkTurnCacheToolHeavyLatestAfterAppend(b *testing.B) {
	for _, count := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("calls=%d", count), func(b *testing.B) {
			path := writeToolHeavyTranscript(b, count)
			cache := NewTurnCache()
			cache.LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				appendFile(b, path, numberedEntryLine(b, count+i+1, "fixed-size-append"))
				cache.LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
			}
		})
	}
}

func BenchmarkTurnCacheOlder30(b *testing.B) {
	for _, count := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("entries=%d", count), func(b *testing.B) {
			path := writeNumberedTranscript(b, count)
			cache := NewTurnCache()
			_, cursor := cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
			var indexedBytes int64
			var projectedTurns int64
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(stat ReadStats) {
				indexedBytes += stat.IndexedBytes
				projectedTurns += int64(stat.ProjectedTurns)
			}
			defer func() { observeTurnIndexRead = previous }()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cache.PageFromFile(path, testMaxLineBytes, cursor, 30, boundedTestProjector)
			}
			reportReadMetrics(b, indexedBytes, projectedTurns)
		})
	}
}

func reportReadMetrics(b *testing.B, indexedBytes int64, projectedTurns int64) {
	if b.N == 0 {
		return
	}
	b.ReportMetric(float64(indexedBytes)/float64(b.N), "indexed_bytes/op")
	b.ReportMetric(float64(projectedTurns)/float64(b.N), "projected_turns/op")
}

func reportIndexWriteMetrics(b *testing.B, copied, serialized, persisted int64) {
	if b.N == 0 {
		return
	}
	b.ReportMetric(float64(copied)/float64(b.N), "index_copied_bytes/op")
	b.ReportMetric(float64(serialized)/float64(b.N), "index_serialized_bytes/op")
	b.ReportMetric(float64(persisted)/float64(b.N), "index_persisted_bytes/op")
}

func sequentialTestProjector() EntryProjector {
	toolNames := map[string]string{}
	return func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem {
		return boundedTestProjector(raw, turnID, turnIndex, toolNames)
	}
}

func boundedTestProjector(raw json.RawMessage, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
	var entry transcript.Entry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil
	}
	return ProjectTurn(turnID, turnIndex, entry.Turn, toolNames, nil, nil)
}

func projectionKeepingAllEntries(raw json.RawMessage, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
	return boundedTestProjector(raw, turnID, turnIndex, toolNames)
}

func projectionKeepingOddEntries(raw json.RawMessage, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
	if turnIndex%2 == 0 {
		return nil
	}
	return boundedTestProjector(raw, turnID, turnIndex, toolNames)
}

func assertBoundedLatestMatchesFull(t *testing.T, path string, limit int) {
	t.Helper()
	full := TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	got, cursor := NewTurnCache().LatestFromFile(path, testMaxLineBytes, limit, boundedTestProjector)
	want, wantCursor := appwire.WindowTurns(full, limit)
	if !reflect.DeepEqual(got, want) || cursor != wantCursor {
		t.Fatalf("latest got=(%#v,%q) want=(%#v,%q)", got, cursor, want, wantCursor)
	}
}

func writeEntries(t testing.TB, entries ...transcript.Entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "entries.transcript.jsonl")
	var data []byte
	for _, entry := range entries {
		data = append(data, marshalEntryLine(t, entry)...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func userEntry(seq int, text string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(text)}}
}

func assistantToolCallEntry(seq int, id, name, arguments string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: json.RawMessage(arguments)}}}}}}
}

func assistantToolCallLine(t testing.TB, seq int, id, name string) []byte {
	t.Helper()
	return marshalEntryLine(t, assistantToolCallEntry(seq, id, name, `{}`))
}

func toolResultEntry(seq int, id, name, content string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: id, Name: name, Content: content}}}}}}
}

func toolResultLine(t testing.TB, seq int, id, name, content string) []byte {
	t.Helper()
	return marshalEntryLine(t, toolResultEntry(seq, id, name, content))
}

func writeBoundedFixture(t *testing.T) boundedFixture {
	t.Helper()
	times := []time.Time{
		time.Unix(1_700_000_001, 0).UTC(),
		time.Unix(1_700_000_002, 0).UTC(),
		time.Unix(1_700_000_003, 0).UTC(),
		time.Unix(1_700_000_004, 0).UTC(),
		time.Unix(1_700_000_005, 0).UTC(),
	}
	header := transcript.Header{Kind: "header", FormatVersion: 1, SessionID: "fixture", SystemPrompt: "You are Serf."}
	firstCall := transcript.APICall{Kind: "api_call", Seq: 1, Request: llm.APILogRequest{Tools: []llm.ToolDefinition{{Name: "read_file", Description: "Read a file."}}}}
	entries := []transcript.Entry{
		{Kind: "entry", Seq: 2, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("first"), Timestamp: times[0]}},
		{Kind: "entry", Seq: 3, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_read", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}}}, Timestamp: times[1], Usage: llm.Usage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18}}},
		{Kind: "entry", Seq: 4, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_read", Content: "contents"}}}}, Timestamp: times[2]}},
		{Kind: "entry", Seq: 5, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("fourth"), Timestamp: times[3]}},
	}
	failedCall := transcript.APICall{Kind: "api_call", Seq: 6, Error: "provider exploded", Source: "provider", Title: "Provider error", Hint: "retry later"}
	partial := transcript.Entry{Kind: "entry", Seq: 7, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("completed later"), Timestamp: times[4]}}

	var data []byte
	for _, record := range []any{header, firstCall, entries[0], entries[1], entries[2], entries[3], failedCall} {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	data = append(data, []byte("{malformed\n")...)
	partialLine, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	cut := len(partialLine) / 2
	data = append(data, partialLine[:cut]...)

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return boundedFixture{path: path, remainder: append([]byte(nil), partialLine[cut:]...), times: times}
}

func turnIDs(turns []appwire.Turn) []string {
	ids := make([]string, len(turns))
	for i := range turns {
		ids[i] = turns[i].ID
	}
	return ids
}

func turnText(turn appwire.Turn) string {
	if len(turn.Items) == 0 {
		return ""
	}
	return turn.Items[0].Text
}

func writeNumberedTranscript(t testing.TB, count int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "numbered.transcript.jsonl")
	var data []byte
	for i := 1; i <= count; i++ {
		data = append(data, numberedEntryLine(t, i, fmt.Sprintf("entry-%d", i))...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeToolHeavyTranscript(t testing.TB, count int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool-heavy.transcript.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= count; i++ {
		if _, err := file.Write(assistantToolCallLine(t, i, fmt.Sprintf("call-%d", i), fmt.Sprintf("tool-%d", i))); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFailedAPICalls(t testing.TB, count int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "failed-api-calls.transcript.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= count; i++ {
		line, err := json.Marshal(transcript.APICall{Kind: "api_call", Seq: i, Error: fmt.Sprintf("failed-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		line = append(line, '\n')
		if _, err := file.Write(line); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func numberedEntryLine(t testing.TB, seq int, text string) []byte {
	t.Helper()
	entry := transcript.Entry{
		Kind: "entry",
		Seq:  seq,
		Turn: schema.Turn{
			Kind:      schema.TurnUserInput,
			Message:   llm.User(text),
			Timestamp: time.Unix(1_700_000_000+int64(seq), 0).UTC(),
		},
	}
	return marshalEntryLine(t, entry)
}

func marshalEntryLine(t testing.TB, entry transcript.Entry) []byte {
	t.Helper()
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func appendFile(t testing.TB, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
