package transcriptindex

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func newFormatFixture(t testing.TB, name string) fixture {
	t.Helper()
	for _, fx := range fixtures() {
		if fx.name == name {
			return fx
		}
	}
	t.Fatalf("no fixture %q", name)
	return fixture{}
}

// indexedCandidates indexes the fixture's first n lines (all of them when n
// is negative) and returns every candidate, oldest first.
func indexedCandidates(t testing.TB, fx fixture, n int) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	if n >= 0 {
		fx.lines = fx.lines[:n]
	}
	x := openIndex(t, writeFixture(t, fx), t.TempDir())
	var all []appitempaging.TranscriptItemCandidate
	window, err := x.Latest(appwire.TranscriptItemPageLimit)
	for {
		if err != nil {
			t.Fatal(err)
		}
		all = append(window.Candidates, all...)
		if !window.HasOlder {
			return all
		}
		window, err = x.Before(window.Candidates[0].Position, appwire.TranscriptItemPageLimit)
	}
}

func turnOf(t testing.TB, candidates []appitempaging.TranscriptItemCandidate, turnID string) appwire.Turn {
	t.Helper()
	for _, c := range candidates {
		if c.TurnID == turnID {
			return c.Turn
		}
	}
	t.Fatalf("no item of turn %s", turnID)
	return appwire.Turn{}
}

func itemsOf(candidates []appitempaging.TranscriptItemCandidate, turnID string) []appwire.ThreadItem {
	var items []appwire.ThreadItem
	for _, c := range candidates {
		if c.TurnID == turnID {
			items = append(items, c.Item)
		}
	}
	return items
}

func TestOpenExecutionDisplaysInProgress(t *testing.T) {
	fx := newFormatFixture(t, "open and reclaimed")
	open := turnOf(t, indexedCandidates(t, fx, 2), "turn_m15")
	if open.Status != appwire.TurnStatusInProgress || open.CompletedAt != nil || open.DurationMS != nil {
		t.Fatalf("open execution = %s, want inProgress with no completion", dump(open))
	}
}

func TestReopenMarkerOpensACompletedTurnAgain(t *testing.T) {
	fx := newFormatFixture(t, "open and reclaimed")
	// Through turn_m16's reopen marker: it failed, then recovery reclaimed it.
	failed := turnOf(t, indexedCandidates(t, fx, 9), "turn_m16")
	if failed.Status != appwire.TurnStatusFailed || failed.DurationMS == nil || *failed.DurationMS != 100 {
		t.Fatalf("before the reopen = %s, want failed after 100 ms", dump(failed))
	}
	reopened := turnOf(t, indexedCandidates(t, fx, 10), "turn_m16")
	if reopened.Status != appwire.TurnStatusInProgress || reopened.CompletedAt != nil || reopened.DurationMS != nil || reopened.Error != nil {
		t.Fatalf("after the reopen = %s, want inProgress with no completion", dump(reopened))
	}
}

func TestReclaimedTurnTakesItsLastCompletion(t *testing.T) {
	candidates := indexedCandidates(t, newFormatFixture(t, "open and reclaimed"), -1)
	for turnID, want := range map[string]struct {
		duration, completedAt int64
	}{
		"turn_m15": {5000, fixtureClock.Add(45e9).UnixMilli()},
		"turn_m16": {2500, fixtureClock.Add(48e9).UnixMilli()},
	} {
		turn := turnOf(t, candidates, turnID)
		if turn.Status != appwire.TurnStatusCompleted || turn.Error != nil || turn.DurationMS == nil || *turn.DurationMS != want.duration || turn.CompletedAt == nil || *turn.CompletedAt != want.completedAt {
			t.Fatalf("%s = %s, want completed after %d ms at %d", turnID, dump(turn), want.duration, want.completedAt)
		}
	}
	if items := itemsOf(candidates, "turn_m15"); len(items) != 3 {
		t.Fatalf("reclaimed turn items = %s, want its input and both runs' answers", dump(items))
	}
}

func TestFailedExecutionAndItsRetry(t *testing.T) {
	candidates := indexedCandidates(t, newFormatFixture(t, "failed execution and retry"), -1)
	failed := turnOf(t, candidates, "turn_m13")
	if failed.Status != appwire.TurnStatusFailed || failed.Error == nil || failed.Error.Message != "model refused" || failed.Error.Title != "Provider error" {
		t.Fatalf("failed turn = %s, want failed with the TURN_FAILURE diagnostic", dump(failed))
	}
	if retry := turnOf(t, candidates, "turn_m14"); retry.Status != appwire.TurnStatusCompleted || retry.Error != nil {
		t.Fatalf("retry = %s, want its own completed turn", dump(retry))
	}
}

func TestDeliveryInsideAnExecutionIsItsOwnTurn(t *testing.T) {
	candidates := indexedCandidates(t, newFormatFixture(t, "delivery inside an execution"), -1)
	delivered := itemsOf(candidates, "t_delivery12")
	if len(delivered) != 1 || delivered[0].Type != "steering" || turnOf(t, candidates, "t_delivery12").Status != appwire.TurnStatusCompleted {
		t.Fatalf("delivery turn items = %s, want one completed steering item", dump(delivered))
	}
	var call, orphan, namedOrphan *appwire.ThreadItem
	for _, item := range itemsOf(candidates, "turn_m12") {
		switch item.CallID {
		case "d1":
			call = &item
		case "zz":
			orphan = &item
		case "zy":
			namedOrphan = &item
		}
	}
	// The call sits at the ASSISTANT entry (ordinal 1, part 1), completed by
	// the TOOL_RESULTS recorded after the delivery.
	if call == nil || call.Output != "listing" || call.Status != appwire.TurnStatusCompleted || *call.Position != (appwire.ThreadItemPosition{Entry: 2, Item: 1}) || call.Version != 4 {
		t.Fatalf("call item = %s, want the ASSISTANT call completed by its result", dump(call))
	}
	if orphan == nil || orphan.ToolName != "" || *orphan.Position != (appwire.ThreadItemPosition{Entry: 4, Item: 1}) {
		t.Fatalf("result with no call = %s, want its own nameless item at the result", dump(orphan))
	}
	if namedOrphan == nil || namedOrphan.ToolName != "grep" {
		t.Fatalf("named result with no call = %s, want its own item", dump(namedOrphan))
	}
}

func TestNewFormatItemsAndTurnsCarryVersions(t *testing.T) {
	fx := newFormatFixture(t, "new format session")
	candidates := indexedCandidates(t, fx, -1)
	var input, call, communicate *appitempaging.TranscriptItemCandidate
	for i, c := range candidates {
		switch {
		case c.Item.Type == "userMessage":
			input = &candidates[i]
		case c.Item.CallID == "f1":
			call = &candidates[i]
		case c.Item.CallID == "fk1":
			if c.Item.Type != "agentMessage" {
				t.Fatalf("the communicate call projected %s, want only its COMMUNICATE message", dump(c.Item))
			}
			communicate = &candidates[i]
		}
	}
	// Ordinals: 2 input, 3 assistant, 4 communicate, 5 results, 6 answer, 7 completion.
	if input == nil || input.Item.Version != 3 {
		t.Fatalf("input item = %s, want version 3", dump(input))
	}
	// The call keeps its opener's identity: id and round.
	if call == nil || call.Item.Version != 6 || call.Item.ToolName != "read_file" || call.Item.Output != "file body" || call.Item.RoundID != "r_10" || call.Item.ID != "item_tool_4_3" {
		t.Fatalf("call item = %s, want the nameless result folded in at version 6", dump(call))
	}
	if communicate == nil || communicate.Item.Text != "told" || communicate.Item.Version != 5 {
		t.Fatalf("communicate item = %s, want the delivered message at version 5", dump(communicate))
	}
	turn := call.Turn
	if turn.Version != 8 || turn.Status != appwire.TurnStatusCompleted || turn.DurationMS == nil || *turn.DurationMS != 4000 {
		t.Fatalf("turn = %s, want completed at version 8 after 4000 ms", dump(turn))
	}
	if turn.Usage == nil || turn.Usage.InputTokens != 14 || turn.Usage.TotalTokens != 20 || turn.Usage.CacheReadTokens != 2 {
		t.Fatalf("turn usage = %s, want both rounds summed", dump(turn.Usage))
	}
	if call.Model != "gpt-next" {
		t.Fatalf("candidate model = %q, want the turn's latest model", call.Model)
	}
}

func TestEchoingCommunicateProjectsNothing(t *testing.T) {
	items := itemsOf(indexedCandidates(t, newFormatFixture(t, "communicate echoes"), -1), "turn_m20")
	var texts []string
	for _, item := range items {
		if item.Type == "commandExecution" {
			t.Fatalf("a communicate call or result projected a tool item: %s", dump(item))
		}
		if item.Type == "agentMessage" {
			texts = append(texts, item.Text)
		}
	}
	if want := []string{"same words", "more", "other words"}; dump(texts) != dump(want) {
		t.Fatalf("messages = %q, want %q", texts, want)
	}
}

func TestFoldCopiesConsumeOnlyTheirOrdinal(t *testing.T) {
	fx := newFormatFixture(t, "fold copies")
	candidates := indexedCandidates(t, fx, -1)
	if items := itemsOf(candidates, "turn_m19"); len(items) != 3 {
		t.Fatalf("folded turn items = %s, want the originals only", dump(items))
	}
	if folded := turnOf(t, candidates, "turn_m19"); folded.Version != 4 {
		t.Fatalf("folded turn version = %d, want its completion's 4", folded.Version)
	}
	after := itemsOf(candidates, "t_gap19")
	if len(after) != 2 || after[1].Version != 9 || *after[1].Position != (appwire.ThreadItemPosition{Entry: 9}) {
		t.Fatalf("gap after the fold = %s, want the hook at entry ordinal 8", dump(after))
	}
}

func TestGapAndPreludeTurnsAreComplete(t *testing.T) {
	gaps := indexedCandidates(t, newFormatFixture(t, "gap turns"), -1)
	if items := itemsOf(gaps, "t_gap11"); len(items) != 3 {
		t.Fatalf("gap turn items = %s, want both hooks and the model switch", dump(items))
	}
	for _, id := range []string{"t_gap11", "t_gap11b"} {
		if turn := turnOf(t, gaps, id); turn.Status != appwire.TurnStatusCompleted || turn.CompletedAt != nil {
			t.Fatalf("gap turn %s = %s, want completed", id, dump(turn))
		}
	}
	session := indexedCandidates(t, newFormatFixture(t, "new format session"), -1)
	var prelude []appitempaging.TranscriptItemCandidate
	for _, c := range session {
		if c.TurnID == appwire.SystemPreludeTurnID && c.Position.Entry > 0 {
			prelude = append(prelude, c)
		}
	}
	if len(prelude) != 2 || prelude[0].Turn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("prelude entries = %s, want two completed items", dump(prelude))
	}
}

func TestNoticesProjectInTheirTurns(t *testing.T) {
	candidates := indexedCandidates(t, newFormatFixture(t, "notices"), -1)
	kinds := map[appwire.ThreadItemEventKind]string{}
	for _, c := range candidates {
		kinds[c.Item.EventKind] = c.TurnID
	}
	for kind, turnID := range map[appwire.ThreadItemEventKind]string{
		appwire.ThreadItemEventKindToolRepair: "t_gap21", appwire.ThreadItemEventKindGoalEnded: "t_gap21",
		appwire.ThreadItemEventKindTurnLimit: "t_gap21", appwire.ThreadItemEventKindSkillActivated: "turn_m21",
	} {
		if kinds[kind] != turnID {
			t.Fatalf("notice %s in turn %q, want %q (all: %v)", kind, kinds[kind], turnID, kinds)
		}
	}
}

// TestReplayOverAnAwaitingEntryAheadRebuilds: a crash after an extension
// rewrote a turn's awaiting ASSISTANT entry in place, past what the meta
// counts, leaves a TOOL_RESULTS entry to replay against a later round. The
// index rebuilds rather than orphan the result.
func TestReplayOverAnAwaitingEntryAheadRebuilds(t *testing.T) {
	fx := fixture{header: everything().header, lines: []fixtureLine{
		entryLine(opens("turn_m30", schema.TurnSpanExecution, user("two rounds"))),
		entryLine(inTurn("turn_m30", assistant(call("a1", "read_file", `{}`)))),
		entryLine(inTurn("turn_m30", results(result("a1", "read_file", "first round")))),
		entryLine(inTurn("turn_m30", assistant(call("a2", "grep", `{}`)))),
		entryLine(inTurn("turn_m30", results(result("a2", "grep", "second round")))),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	appendBytes(t, path, joinLines(lines[:2]))
	x := openIndex(t, path, dir)
	saved, err := os.ReadFile(filepath.Join(liveBuild(t, dir), metaFile))
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, joinLines(lines[2:]))
	catchUp(t, x)
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveBuild(t, dir), metaFile), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	replayed := openIndex(t, path, dir)
	if replayed.rebuilds != 1 {
		t.Fatalf("builds = %d, want a rebuild for the result whose round the turn moved past", replayed.rebuilds)
	}
	assertAllWindows(t, replayed, path)
}

// Header-derived prelude items are keyed apptranscript-item-v2:prelude:header:<part>
// at {entry: 0, item: part}, with no version: the header never changes.
func TestHeaderPreludeItemsKeyAtTheHeader(t *testing.T) {
	candidates := indexedCandidates(t, newFormatFixture(t, "new format session"), -1)
	header := candidates[0]
	if header.Position != (appwire.ThreadItemPosition{Entry: 0, Item: 0}) || header.Item.TranscriptKey != "apptranscript-item-v2:prelude:header:0" || header.Item.Version != 0 {
		t.Fatalf("header prelude item = %s, want key apptranscript-item-v2:prelude:header:0 at entry 0, version 0", dump(header.Item))
	}
	if got := ItemKey("anything", appwire.ThreadItemPosition{Entry: 0, Item: 2}); got != "apptranscript-item-v2:prelude:header:2" {
		t.Fatalf("ItemKey at the header = %q", got)
	}
}

// The prelude's entries join the prelude turn: turn_system, completed, as
// written with TurnKind "prelude".
func TestPreludeEntriesJoinTheCompletedPreludeTurn(t *testing.T) {
	session := indexedCandidates(t, newFormatFixture(t, "new format session"), -1)
	var entries int
	for _, c := range session {
		if c.Position.Entry == 0 {
			continue
		}
		if c.Item.Text == "prelude environment" || c.Item.Text == "prelude notes" {
			entries++
			if c.TurnID != appwire.SystemPreludeTurnID || c.Turn.Status != appwire.TurnStatusCompleted {
				t.Fatalf("prelude entry item in turn %s (%s), want the completed %s", c.TurnID, c.Turn.Status, appwire.SystemPreludeTurnID)
			}
		}
	}
	if entries != 2 {
		t.Fatalf("found %d prelude entry items, want 2", entries)
	}
	line := []byte(`{"kind":"entry","seq":1,"turn":{"kind":"ENVIRONMENT","message":{"role":"user","content":[{"kind":"text","text":"env"}]},"format":1,"turn_id":"turn_system","turn_kind":"prelude"}}`)
	entry, err := transcript.DecodeEntry(line)
	if err != nil || entry.Turn.TurnKind != schema.TurnSpanPrelude {
		t.Fatalf("decode a written prelude entry = %+v, %v", entry.Turn.TurnKind, err)
	}
}

// A tool item carries the ordinal + 1 of the TOOL_RESULTS entry that
// completed it; its key and position stay the opener's.
func TestToolItemsCarryTheirCompletingEntry(t *testing.T) {
	candidates := indexedCandidates(t, newFormatFixture(t, "new format session"), -1)
	for _, c := range candidates {
		if c.Item.CallID == "f1" {
			// Ordinals: 3 assistant, 5 results.
			if c.Item.CompletedAtEntry != 6 || c.Position != (appwire.ThreadItemPosition{Entry: 4, Item: 3}) {
				t.Fatalf("call item = %s, want completedAtEntry 6 at the opener's position", dump(c.Item))
			}
			return
		}
	}
	t.Fatal("no call item")
}

// A call whose execution turn completed with no TOOL_RESULTS for it (a crash
// before results; resume records an interrupted completion) projects as
// interrupted: the completion contributes to the item, raising its version.
func TestCallWithoutResultsIsInterruptedAtCompletion(t *testing.T) {
	fx := newFormatFixture(t, "interrupted call")
	before := indexedCandidates(t, fx, len(fx.lines)-1)
	after := indexedCandidates(t, fx, -1)
	find := func(candidates []appitempaging.TranscriptItemCandidate) appwire.ThreadItem {
		for _, c := range candidates {
			if c.Item.CallID == "ic1" {
				return c.Item
			}
		}
		t.Fatal("no call item")
		return appwire.ThreadItem{}
	}
	if call := find(before); call.Status == appwire.TurnStatusInterrupted {
		t.Fatalf("call before the completion = %s, want not yet interrupted", dump(call))
	}
	call := find(after)
	completionVersion := uint64(len(fx.lines))
	if call.Status != appwire.TurnStatusInterrupted || call.Version != completionVersion || call.CompletedAtEntry != 0 {
		t.Fatalf("call after the completion = %s, want interrupted at version %d with no completing results", dump(call), completionVersion)
	}
	for _, c := range after {
		if c.Item.CallID == "ic2" && c.Item.Status == appwire.TurnStatusInterrupted {
			t.Fatalf("the call that got results = %s, want it completed", dump(c.Item))
		}
	}
}
