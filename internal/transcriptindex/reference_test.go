package transcriptindex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
)

const testMaxLineBytes = 128 << 20

// readFixtureFile strictly decodes a transcript the way the whole-file readers
// do: blank lines are skipped, an unterminated tail is dropped.
func readFixtureFile(t testing.TB, path string) (transcript.Header, []schema.Turn) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	reader := bufio.NewReaderSize(f, 64<<10)
	var header transcript.Header
	var entries []schema.Turn
	headerRead := false
	for {
		line, complete, _, err := transcript.ReadLine(reader, testMaxLineBytes)
		if err != nil {
			t.Fatal(err)
		}
		if !complete {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			if header, err = transcript.DecodeHeader(line); err != nil {
				t.Fatal(err)
			}
			headerRead = true
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry.Turn)
	}
	return header, entries
}

// referenceTurns is today's whole-file projection with the spec's positions:
// the production grouping, per-entry projection, call-id merge and turn stamp,
// with each item keyed by the entry and content part that opened it.
func referenceTurns(t testing.TB, path string) []appwire.Turn {
	t.Helper()
	header, entries := readFixtureFile(t, path)
	type group struct {
		id        string
		entries   []schema.Turn
		items     []appwire.ThreadItem
		positions []appwire.ThreadItemPosition
		calls     map[string]int
	}
	var groups []*group
	var grouper apptranscript.TurnGrouper
	reg := apptranscript.NewToolCallRegistry()
	for i, entry := range entries {
		entryIndex := i + 1
		id, isNew := grouper.Place(&entry, entryIndex)
		if isNew {
			groups = append(groups, &group{id: id, calls: map[string]int{}})
		}
		g := groups[len(groups)-1]
		g.entries = append(g.entries, entry)
		items, parts := apptranscript.ProjectTurnParts(id, entryIndex, entry, reg, nil, apptranscript.ToolResultOutputImages)
		for j, item := range items {
			if apptranscript.MergesByCallID(item) {
				if at, ok := g.calls[item.CallID]; ok {
					g.items[at] = apptranscript.MergeThreadItems(g.items[at], item)
					continue
				}
				g.calls[item.CallID] = len(g.items)
			}
			g.items = append(g.items, item)
			g.positions = append(g.positions, appwire.ThreadItemPosition{Entry: uint64(entryIndex), Item: uint32(parts[j])})
		}
	}
	var turns []appwire.Turn
	if prelude := apptranscript.PreludeTurn(header); prelude != nil {
		for i := range prelude.Items {
			position := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
			prelude.Items[i].Position = &position
			prelude.Items[i].TranscriptKey = ItemKey(prelude.ID, position)
		}
		turns = append(turns, *prelude)
	}
	for _, g := range groups {
		if len(g.items) == 0 {
			continue
		}
		turn := appwire.Turn{ID: g.id, ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted}
		for j := range g.items {
			position := g.positions[j]
			g.items[j].TurnID = g.id
			g.items[j].Position = &position
			g.items[j].TranscriptKey = ItemKey(g.id, position)
		}
		turn.Items = g.items
		apptranscript.StampGroupedTurn(&turn, g.entries)
		turns = append(turns, turn)
	}
	// Flush a communicate call the transcript ends on with no result yet,
	// matching what every production reader does (server/appwire_turns.go,
	// cmd/evener-hub/app_threadread.go): reg still holds its CommRawArgs, and
	// FlushUnpairedCommunicates appends the delivered message to the last
	// turn. Re-key the flushed item(s) in this package's v2 scheme —
	// FlushUnpairedCommunicates sets appitempaging's v1 key, matching its own
	// production callers, not this file's ItemKey.
	if len(turns) > 0 {
		before := len(turns[len(turns)-1].Items)
		if apptranscript.FlushUnpairedCommunicates(&turns, reg) {
			last := &turns[len(turns)-1]
			for i := before; i < len(last.Items); i++ {
				last.Items[i].TranscriptKey = ItemKey(last.ID, *last.Items[i].Position)
			}
		}
	}
	return turns
}

// referenceCandidates is referenceTurns as the chronological candidate list a
// window read returns: each candidate's Turn carries no items.
func referenceCandidates(t testing.TB, path string) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	candidates, err := appitempaging.CandidatesFromTurns(referenceTurns(t, path))
	if err != nil {
		t.Fatal(err)
	}
	for i := range candidates {
		candidates[i].Turn.Items = nil
	}
	return candidates
}

func stripPositions(turns []appwire.Turn) []appwire.Turn {
	for i := range turns {
		for j := range turns[i].Items {
			turns[i].Items[j].Position = nil
			turns[i].Items[j].TranscriptKey = ""
		}
	}
	return turns
}

func dump(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

func TestReferenceEqualsTodaysFileProjectionApartFromPositions(t *testing.T) {
	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			path := writeFixture(t, fx)
			reg := apptranscript.NewToolCallRegistry()
			today, err := apptranscript.ItemTurnsFromFile(path, testMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
				return apptranscript.ProjectTurn(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
			})
			if err != nil {
				t.Fatal(err)
			}
			// Every production caller flushes after projecting (e.g.
			// server/appwire_turns.go's appTurnProjectionFromTranscriptFile);
			// ItemTurnsFromFile alone does not, so match that here too.
			apptranscript.FlushUnpairedCommunicates(&today, reg)
			reference := referenceTurns(t, path)
			if len(reference) == 0 {
				t.Fatal("fixture projected no turns")
			}
			if !reflect.DeepEqual(stripPositions(today), stripPositions(reference)) {
				t.Fatalf("reference diverges from today's projection:\ntoday: %s\nref:   %s", dump(today), dump(reference))
			}
		})
	}
}

// TestFixturesCoverTheShapes guards the corpus itself: each legacy shape the
// index has to reproduce is present in the "everything" fixture's reference.
func TestFixturesCoverTheShapes(t *testing.T) {
	all := fixtures()
	path := writeFixture(t, all[len(all)-1])
	var outputImage, resolvedName, threeContributors, failedWithError, interrupted, prelude bool
	for _, turn := range referenceTurns(t, path) {
		prelude = prelude || turn.ID == appwire.SystemPreludeTurnID
		failedWithError = failedWithError || (turn.Status == appwire.TurnStatusFailed && turn.Error != nil && turn.Error.Message == "provider exploded")
		interrupted = interrupted || turn.Status == appwire.TurnStatusInterrupted
		for _, item := range turn.Items {
			outputImage = outputImage || len(item.OutputImages) > 0
			resolvedName = resolvedName || (item.CallID == "o2" && item.ToolName == "grep")
			threeContributors = threeContributors || (item.CallID == "r1" && item.Output == "fourth contributor" && item.ArgumentsJSON == `{"pattern":"x"}`)
		}
	}
	for name, covered := range map[string]bool{
		"output image": outputImage, "nameless result resolved from an earlier turn": resolvedName,
		"call with several contributor entries": threeContributors, "failed turn with error": failedWithError,
		"interrupted turn": interrupted, "prelude": prelude,
	} {
		if !covered {
			t.Errorf("fixture corpus no longer covers: %s", name)
		}
	}
}
