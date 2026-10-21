package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// warmHistoryAllocationMarginBytes is the frozen budget for one history page's
// allocated bytes: the warm page over a 4,096-turn fixture may exceed the warm
// page over a 256-turn fixture by at most this much. Calibrated on the unchanged
// aggregation, which exceeded it by reading every turn record for totals the
// page discards.
const warmHistoryAllocationMarginBytes = 256 << 10

// seedCompletedTurnsPastThread writes a new-format transcript of completed
// turns — user input, assistant answer carrying usage, completion, all sharing
// one TurnID per round — and builds its persisted index sidecar. It returns the
// config that finds the session, its past entry, and the transcript path.
func seedCompletedTurnsPastThread(tb testing.TB, rounds int) (hubcore.WebConfig, hubcore.PastEntry, string) {
	tb.Helper()
	root := tb.TempDir()
	stateDir := filepath.Join(root, "projects", "project-turns-0000000000")
	sessionID := identifier.MustNewSessionID()
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		tb.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "anthropic", Model: "claude-opus-4-5", TurnCount: rounds,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/project"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "anthropic", Model: "claude-opus-4-5",
	})
	if err != nil {
		tb.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	usage := llm.Usage{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100}
	for i := range rounds {
		turnID := fmt.Sprintf("turn_%06d", i)
		if err := writer.Append(execution(turnID, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("request %d", i))))); err != nil {
			tb.Fatal(err)
		}
		if err := writer.Append(execution(turnID, schema.Turn{
			Kind: schema.TurnAssistant, Message: llm.Assistant(fmt.Sprintf("answer %d", i)), Usage: usage,
		})); err != nil {
			tb.Fatal(err)
		}
		if err := writer.Append(completedExecution(turnID)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	indexPastTranscriptForTest(tb, stateDir, sessionID)
	index := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := index.Rebuild(); err != nil {
		tb.Fatal(err)
	}
	entry, ok := index.Find(sessionID)
	if !ok {
		tb.Fatal("seeded completed-turn session not found")
	}
	return hubcore.WebConfig{Past: index}, entry, path
}

// expectedTurnsPage is the authored content of one fixed 40-item page: the 20
// two-item turns the fixture wrote, from the round number alone.
type expectedTurnsPage struct {
	turnIDs   []string
	userTexts []string
	answer    []string
}

// completedTurnsPageExpectation derives the page a fixed item limit returns
// from the fixture's round count, outside any measured region. The latest page
// carries the last 20 rounds; the older-cursor page carries the 20 before them.
func completedTurnsPageExpectation(rounds int, page string) expectedTurnsPage {
	start := rounds - 20
	if page == "older-cursor" {
		start = rounds - 40
	}
	want := expectedTurnsPage{
		turnIDs:   make([]string, 20),
		userTexts: make([]string, 20),
		answer:    make([]string, 20),
	}
	for i := range want.turnIDs {
		round := start + i
		want.turnIDs[i] = fmt.Sprintf("turn_%06d", round)
		want.userTexts[i] = fmt.Sprintf("request %d", round)
		want.answer[i] = fmt.Sprintf("answer %d", round)
	}
	return want
}

// requireTurnsListPage fails unless the page is exactly the fixture-authored
// 20 two-item turns with their authored IDs and text.
func requireTurnsListPage(tb testing.TB, response appwire.ThreadTurnsListResponse, want expectedTurnsPage) {
	tb.Helper()
	if len(response.Data) != len(want.turnIDs) {
		tb.Fatalf("turns/list page = %d turns, want %d", len(response.Data), len(want.turnIDs))
	}
	items := 0
	for i, turn := range response.Data {
		if turn.ID != want.turnIDs[i] {
			tb.Fatalf("turn %d id = %q, want %q", i, turn.ID, want.turnIDs[i])
		}
		if len(turn.Items) != 2 {
			tb.Fatalf("turn %s items = %d, want 2", turn.ID, len(turn.Items))
		}
		items += len(turn.Items)
		if turn.Items[0].Type != "userMessage" || turn.Items[0].Text != want.userTexts[i] {
			tb.Fatalf("turn %s first item = %q/%q, want userMessage/%q", turn.ID, turn.Items[0].Type, turn.Items[0].Text, want.userTexts[i])
		}
		if turn.Items[1].Type != "agentMessage" || turn.Items[1].Text != want.answer[i] {
			tb.Fatalf("turn %s second item = %q/%q, want agentMessage/%q", turn.ID, turn.Items[1].Type, turn.Items[1].Text, want.answer[i])
		}
	}
	if items != 40 {
		tb.Fatalf("turns/list page = %d items, want 40", items)
	}
}

func readTurnsListPage(tb testing.TB, cfg hubcore.WebConfig, params appwire.ThreadTurnsListParams, want expectedTurnsPage) {
	tb.Helper()
	response, found, err := pastThreadTurnsList(context.Background(), cfg, params)
	if err != nil || !found {
		tb.Fatalf("pastThreadTurnsList = (%+v, %v, %v)", response, found, err)
	}
	requireTurnsListPage(tb, response, want)
}

// TestPastThreadTurnsListWarmAllocationDoesNotScaleWithHistory pins that a warm
// history page's allocated bytes do not grow with the session's completed-turn
// count. The unchanged implementation read every turn record to derive session
// totals the page discards, so the 4,096-turn page exceeded the 256-turn page
// by far more than the frozen margin.
func TestPastThreadTurnsListWarmAllocationDoesNotScaleWithHistory(t *testing.T) {
	smallCfg, smallEntry, _ := seedCompletedTurnsPastThread(t, 256)
	largeCfg, largeEntry, _ := seedCompletedTurnsPastThread(t, 4096)

	type fixture struct {
		name   string
		rounds int
		cfg    hubcore.WebConfig
		ref    string
	}
	fixtures := []fixture{
		{name: "warm-256", rounds: 256, cfg: smallCfg, ref: localAppRef(smallEntry.Meta.ID)},
		{name: "warm-4096", rounds: 4096, cfg: largeCfg, ref: localAppRef(largeEntry.Meta.ID)},
	}
	latestParams := func(f fixture) appwire.ThreadTurnsListParams {
		return appwire.ThreadTurnsListParams{Ref: f.ref, ItemLimit: 40}
	}
	// Obtain each cursor from the same public page a client would, before
	// measuring; the cursor page itself is what both implementations page.
	olderParams := map[string]appwire.ThreadTurnsListParams{}
	for _, f := range fixtures {
		latest, found, err := pastThreadTurnsList(context.Background(), f.cfg, latestParams(f))
		if err != nil || !found || latest.NextCursor == "" {
			t.Fatalf("%s latest page = (%+v, %v, %v)", f.name, latest, found, err)
		}
		olderParams[f.name] = appwire.ThreadTurnsListParams{Ref: f.ref, ItemLimit: 40, Cursor: latest.NextCursor}
	}

	const calls = 20
	measure := func(f fixture, params appwire.ThreadTurnsListParams, page string) uint64 {
		want := completedTurnsPageExpectation(f.rounds, page)
		// Warm the index handle and the page setup before measuring.
		readTurnsListPage(t, f.cfg, params, want)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range calls {
			readTurnsListPage(t, f.cfg, params, want)
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / calls
	}

	smallLatest := measure(fixtures[0], latestParams(fixtures[0]), "latest")
	largeLatest := measure(fixtures[1], latestParams(fixtures[1]), "latest")
	smallOlder := measure(fixtures[0], olderParams[fixtures[0].name], "older-cursor")
	largeOlder := measure(fixtures[1], olderParams[fixtures[1].name], "older-cursor")
	t.Logf("warm allocated bytes/call: latest 256=%d 4096=%d, older 256=%d 4096=%d",
		smallLatest, largeLatest, smallOlder, largeOlder)

	if largeLatest > smallLatest+warmHistoryAllocationMarginBytes {
		t.Fatalf("latest page allocation scales with history: 4,096 turns=%d B/call, 256 turns=%d B/call, margin=%d B",
			largeLatest, smallLatest, warmHistoryAllocationMarginBytes)
	}
	if largeOlder > smallOlder+warmHistoryAllocationMarginBytes {
		t.Fatalf("older-cursor page allocation scales with history: 4,096 turns=%d B/call, 256 turns=%d B/call, margin=%d B",
			largeOlder, smallOlder, warmHistoryAllocationMarginBytes)
	}
}

// BenchmarkPastThreadTurnsListWarm measures the warm page-read path over a
// 4,000/25,000-round by latest/older-cursor matrix. Each size fixture is built
// and both pages warmed once per size, outside every timed region.
func BenchmarkPastThreadTurnsListWarm(b *testing.B) {
	for _, rounds := range []int{4_000, 25_000} {
		b.Run(fmt.Sprintf("rounds=%d", rounds), func(b *testing.B) {
			cfg, entry, _ := seedCompletedTurnsPastThread(b, rounds)
			latestParams := appwire.ThreadTurnsListParams{Ref: localAppRef(entry.Meta.ID), ItemLimit: 40}
			latestWant := completedTurnsPageExpectation(rounds, "latest")
			latest, found, err := pastThreadTurnsList(context.Background(), cfg, latestParams)
			if err != nil || !found || latest.NextCursor == "" {
				b.Fatalf("latest page = (%+v, %v, %v)", latest, found, err)
			}
			olderParams := appwire.ThreadTurnsListParams{Ref: latestParams.Ref, ItemLimit: 40, Cursor: latest.NextCursor}
			olderWant := completedTurnsPageExpectation(rounds, "older-cursor")
			readTurnsListPage(b, cfg, latestParams, latestWant)
			readTurnsListPage(b, cfg, olderParams, olderWant)

			b.Run("page=latest", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					readTurnsListPage(b, cfg, latestParams, latestWant)
				}
			})
			b.Run("page=older-cursor", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					readTurnsListPage(b, cfg, olderParams, olderWant)
				}
			})
		})
	}
}

// BenchmarkPastThreadTurnsListColdHandle measures the first page read of a
// fresh process: the fixture's sidecar is built and closed before timing, and
// no hub index handle has adopted it. Run each sample as
// -benchtime=1x -count=1 in its own process; the read that follows is the
// measured one, and the fixture's content is asserted after the timer stops.
func BenchmarkPastThreadTurnsListColdHandle(b *testing.B) {
	for _, rounds := range []int{4_000, 25_000} {
		b.Run(fmt.Sprintf("rounds=%d", rounds), func(b *testing.B) {
			cfg, entry, path := seedCompletedTurnsPastThread(b, rounds)
			if info, err := os.Stat(path); err == nil {
				b.Logf("transcript %s: %.1f MiB", filepath.Base(path), float64(info.Size())/(1<<20))
			}
			params := appwire.ThreadTurnsListParams{Ref: localAppRef(entry.Meta.ID), ItemLimit: 40}
			want := completedTurnsPageExpectation(rounds, "latest")
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				response, found, err := pastThreadTurnsList(context.Background(), cfg, params)
				b.StopTimer()
				if err != nil || !found {
					b.Fatalf("pastThreadTurnsList = (%+v, %v, %v)", response, found, err)
				}
				requireTurnsListPage(b, response, want)
				b.StartTimer()
			}
		})
	}
}
