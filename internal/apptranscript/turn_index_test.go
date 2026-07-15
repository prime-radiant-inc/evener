package apptranscript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestTurnCacheRebuildsSidecarWithInvalidToolCheckpoint(t *testing.T) {
	entries := []transcript.Entry{assistantToolCallEntry(1, "shared", "correct_tool", `{}`)}
	for seq := 2; seq <= toolNameCheckpointInterval; seq++ {
		entries = append(entries, userEntry(seq, fmt.Sprintf("entry-%d", seq)))
	}
	entries = append(entries, toolResultEntry(toolNameCheckpointInterval+1, "shared", "", "done"))
	path := writeEntries(t, entries...)
	indexPath := path + ".appwire-index.json"
	NewTurnCache().LatestFromFile(path, testMaxLineBytes, 1, boundedTestProjector)
	index, err := readTurnIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	record := &index.Records[toolNameCheckpointInterval]
	if record.ToolNamesBefore["shared"] != "correct_tool" {
		t.Fatalf("checkpoint fixture=%v", record.ToolNamesBefore)
	}
	record.ToolNamesBefore["shared"] = "wrong_tool"
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
		t.Fatalf("invalid checkpoint was reused: turns=%#v", got)
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
			previous := observeTurnIndexRead
			observeTurnIndexRead = func(stat ReadStats) {
				indexedBytes += stat.IndexedBytes
				projectedTurns += int64(stat.ProjectedTurns)
			}
			defer func() { observeTurnIndexRead = previous }()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				appendFile(b, path, numberedEntryLine(b, count+i+1, "appended"))
				cache.LatestFromFile(path, testMaxLineBytes, 40, boundedTestProjector)
			}
			reportReadMetrics(b, indexedBytes, projectedTurns)
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

func toolResultEntry(seq int, id, name, content string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: id, Name: name, Content: content}}}}}}
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
