package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// TestColdReadDerivesTotalsWithoutScan pins that the first daemonless read of a
// session with a current sidecar answers its token total and failed-tool-call
// count from the index, paying no transcript scan (#4667).
func TestColdReadDerivesTotalsWithoutScan(t *testing.T) {
	cfg, params, _ := seedLargePastThread(t, 200)

	original := pastDerivedTotalsScan
	scanCalls := 0
	pastDerivedTotalsScan = func(path string, fromEntryOrdinal int) (*appwire.EvenerUsage, int, error) {
		scanCalls++
		return original(path, fromEntryOrdinal)
	}
	defer func() { pastDerivedTotalsScan = original }()

	pastTranscriptCache = apptranscript.NewTurnCache()
	response, found, err := pastThreadReadResponse(context.Background(), cfg, params)
	if err != nil || !found {
		t.Fatalf("pastThreadReadResponse = %v, %v", err, found)
	}
	if scanCalls != 0 {
		t.Fatalf("cold read scanned the transcript %d time(s); the current sidecar should answer", scanCalls)
	}
	if response.Thread.Evener.Usage == nil || response.Thread.Evener.FailedToolCalls == nil {
		t.Fatalf("cold read did not stamp derived totals: usage=%v failures=%v",
			response.Thread.Evener.Usage, response.Thread.Evener.FailedToolCalls)
	}
}

// TestPastThreadTurnsListDoesNotDeriveIndexedTotals pins that a history page
// does not read the session's own-span totals from the index: thread/turns/list
// never returns them, so computing them per page is discarded work that grows
// with the session's history. The page's fragments must still be the indexed
// window's.
func TestPastThreadTurnsListDoesNotDeriveIndexedTotals(t *testing.T) {
	cfg, entry := seedPastItemPagingThread(t)
	ctx := context.Background()
	ref := localAppRef(entry.Meta.ID)

	// Obtain the cursor from the public page before asserting on the private
	// page the turns-list path builds from it.
	latestList, found, err := pastThreadTurnsList(ctx, cfg, appwire.ThreadTurnsListParams{Ref: ref, ItemLimit: 40})
	if err != nil || !found {
		t.Fatalf("latest turns/list = (%+v, %v, %v)", latestList, found, err)
	}
	if latestList.NextCursor == "" {
		t.Fatal("latest turns/list has no older cursor")
	}

	latest, err := pastEntryPageItems(ctx, entry, appwire.ThreadTurnsListParams{Ref: ref, ItemLimit: 40}, 40)
	if err != nil {
		t.Fatalf("latest page: %v", err)
	}
	requirePageFragments(t, "latest", latest, 34, 6)
	if !latest.Candidates[0].HasEarlierItems {
		t.Fatal("latest page's first fragment does not report earlier items")
	}

	older, err := pastEntryPageItems(ctx, entry, appwire.ThreadTurnsListParams{Ref: ref, ItemLimit: 40, Cursor: latestList.NextCursor}, 40)
	if err != nil {
		t.Fatalf("older cursor page: %v", err)
	}
	requirePageFragments(t, "older cursor", older, 7)

	before, err := pastEntryPageItems(ctx, entry, appwire.ThreadTurnsListParams{Ref: ref, ItemLimit: 40, Before: &latest.Candidates[0].Position}, 40)
	if err != nil {
		t.Fatalf("before page: %v", err)
	}
	requirePageFragments(t, "before", before, 7)

	for name, page := range map[string]pastItemPage{"latest": latest, "older cursor": older, "before": before} {
		if page.Derived != nil {
			t.Fatalf("%s page computed discarded session totals", name)
		}
	}
}

// requirePageFragments fails unless page breaks into fragments with exactly the
// given item counts, in order.
func requirePageFragments(t *testing.T, name string, page pastItemPage, want ...int) {
	t.Helper()
	var got []int
	for i := 0; i < len(page.Candidates); {
		turnID := page.Candidates[i].TurnID
		count := 0
		for i < len(page.Candidates) && page.Candidates[i].TurnID == turnID {
			count++
			i++
		}
		got = append(got, count)
	}
	if len(got) != len(want) {
		t.Fatalf("%s page fragments = %v, want %v", name, got, want)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s page fragments = %v, want %v", name, got, want)
	}
}

// seedMidTurnDivergencePastSession saves a fork-child-shaped session whose
// DivergenceTurn cuts inside a multi-entry turn, so the index refuses to derive
// its totals and a totals reader must fall back to the transcript scan. Each
// turn's entries share a TurnID, as a new-format transcript records them.
func seedMidTurnDivergencePastSession(t *testing.T) (hubcore.WebConfig, hubcore.PastEntry) {
	t.Helper()
	parentUsage := llm.Usage{InputTokens: 9000, OutputTokens: 900, TotalTokens: 9900}
	childUsage := llm.Usage{InputTokens: 4000, OutputTokens: 400, TotalTokens: 4400}
	entries := []schema.Turn{
		execution("turn_fork_0", schema.NewTurn(schema.TurnUserInput, llm.User("inherited question"))),
		execution("turn_fork_0", schema.Turn{
			Kind: schema.TurnAssistant, Message: llm.Assistant("inherited answer"), Usage: parentUsage,
		}),
		completedExecution("turn_fork_0"),
		execution("turn_fork_1", schema.NewTurn(schema.TurnUserInput, llm.User("own question"))),
		execution("turn_fork_1", schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "own answer"},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "call_fork_1", Name: "shell", Arguments: json.RawMessage(`{"argv":["false"]}`),
				}},
			}},
			Usage: childUsage,
		}),
		execution("turn_fork_1", schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					ToolCallID: "call_fork_1", Name: "shell", Content: "failed", IsError: true,
				},
			}}},
		}),
		completedExecution("turn_fork_1"),
	}
	for i := range 20 {
		turnID := fmt.Sprintf("turn_fill_%02d", i)
		entries = append(entries,
			execution(turnID, schema.NewTurn(schema.TurnUserInput, llm.User("filler"))),
			execution(turnID, schema.NewTurn(schema.TurnAssistant, llm.Assistant("filler"))),
		)
	}
	// Extra completed turns keep the page over one 40-item window so the guard
	// has a real older cursor to request. They carry no usage or failures.
	cfg, entry, _ := seedIndexedPastSession(t, entries...)
	// A cut at 3 lands inside the inherited turn, whose record spans entries
	// 1-3, so the index cannot express the suffix.
	entry.Meta.DivergenceTurn = 3
	entry.Meta.ParentSessionID = "02wMz5Txv5aIxgf9yVdd0P"
	if err := schema.SaveSessionMeta(entry.StateDir, entry.Meta); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg.Past.Find(entry.Meta.ID)
	if !ok {
		t.Fatal("rebuilt past index did not find the fork fixture")
	}
	return cfg, entry
}

// TestPastThreadTurnsListDoesNotScanFallbackTotals pins that a history page
// whose index cannot derive session totals does not fall back to the transcript
// scan either: the page discards the figures. thread/read on the same fixture
// must still run the fallback and report the fixture's own-span totals.
//
// pastDerivedTotalsScan and pastTranscriptCache are package globals, so this
// test is serial like TestColdReadDerivesTotalsWithoutScan.
func TestPastThreadTurnsListDoesNotScanFallbackTotals(t *testing.T) {
	cfg, entry := seedMidTurnDivergencePastSession(t)
	ctx := context.Background()
	ref := localAppRef(entry.Meta.ID)

	// Prove the fixture actually forces the fallback: the sidecar refuses the
	// mid-turn cut rather than answering it.
	path := pastTranscriptPath(entry)
	index, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatalf("open fixture index: %v", err)
	}
	_, _, derivedErr := index.DerivedTotals(entry.Meta.DivergenceTurn)
	if closeErr := index.Close(); closeErr != nil {
		t.Fatalf("close fixture index: %v", closeErr)
	}
	if !errors.Is(derivedErr, transcriptindex.ErrDerivedTotalsUnavailable) {
		t.Fatalf("fixture index derived totals error = %v, want ErrDerivedTotalsUnavailable", derivedErr)
	}

	original := pastDerivedTotalsScan
	scanCalls := 0
	pastDerivedTotalsScan = func(path string, fromEntryOrdinal int) (*appwire.EvenerUsage, int, error) {
		scanCalls++
		return original(path, fromEntryOrdinal)
	}
	t.Cleanup(func() { pastDerivedTotalsScan = original })
	pastTranscriptCache = apptranscript.NewTurnCache()

	latest, found, err := pastThreadTurnsList(ctx, cfg, appwire.ThreadTurnsListParams{Ref: ref, ItemLimit: 40})
	if err != nil || !found {
		t.Fatalf("latest turns/list = (%+v, %v, %v)", latest, found, err)
	}
	if len(latest.Data) == 0 {
		t.Fatal("latest turns/list returned no data")
	}
	if latest.NextCursor == "" {
		t.Fatal("latest turns/list has no older cursor")
	}
	older, found, err := pastThreadTurnsList(ctx, cfg, appwire.ThreadTurnsListParams{Ref: ref, ItemLimit: 40, Cursor: latest.NextCursor})
	if err != nil || !found {
		t.Fatalf("older turns/list = (%+v, %v, %v)", older, found, err)
	}
	if len(older.Data) == 0 {
		t.Fatal("older turns/list returned no data")
	}
	if scanCalls != 0 {
		t.Fatalf("history pages scanned the transcript %d time(s) for totals they discard", scanCalls)
	}

	// The same fixture's thread/read still owes the totals, so it must scan and
	// report the child's own span.
	pastTranscriptCache = apptranscript.NewTurnCache()
	scanCalls = 0
	read, found, err := pastThreadReadResponse(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: 40})
	if err != nil || !found {
		t.Fatalf("thread/read = (%+v, %v, %v)", read, found, err)
	}
	if scanCalls == 0 {
		t.Fatal("thread/read did not run the fallback totals scan")
	}
	wantUsage := appwire.EvenerUsage{InputTokens: 4000, OutputTokens: 400, TotalTokens: 4400}
	if read.Thread.Evener.Usage == nil || *read.Thread.Evener.Usage != wantUsage {
		t.Fatalf("thread/read usage = %+v, want the child's own span %+v", read.Thread.Evener.Usage, wantUsage)
	}
	if read.Thread.Evener.FailedToolCalls == nil || *read.Thread.Evener.FailedToolCalls != 1 {
		t.Fatalf("thread/read failed tool calls = %v, want 1", read.Thread.Evener.FailedToolCalls)
	}
}
