package transcriptindex

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

const testMaxLineBytes = 128 << 20

// readFixtureFile strictly decodes a transcript the way the whole-file readers
// do: blank lines are skipped, an unterminated tail is dropped. An entry line
// a fixture deliberately corrupted (corruptEntryLine) takes its place as a
// zero turn, its decode error in unreadable at the same index; any other line
// that does not decode fails the test.
func readFixtureFile(t testing.TB, path string) (transcript.Header, []schema.Turn, map[int]error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	reader := bufio.NewReaderSize(f, 64<<10)
	var header transcript.Header
	var entries []schema.Turn
	unreadable := map[int]error{}
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
			if !bytes.Contains(line, []byte(deliberatelyUnreadable)) {
				t.Fatalf("fixture entry %d does not decode: %v", len(entries), err)
			}
			unreadable[len(entries)] = err
		}
		entries = append(entries, entry.Turn)
	}
	return header, entries, unreadable
}

// referenceTurn is one turn of the reference projection, with what the
// candidate list needs beside the wire turn: the model for cost, and the
// turn's identity (its index), which two turns sharing an id keep apart.
type referenceTurn struct {
	turn  appwire.Turn
	model string
}

// referenceProjection is the whole-file projection the index reproduces,
// written directly over the whole entry list rather than through the index's
// builder. Legacy entries follow today's rules: the production grouping,
// per-entry projection, call-id merge and turn stamp. New-format entries
// follow the read model's: turns by TurnID in first-appearance order, status
// from the latest completion or reopen, each tool result folded into the call
// of the same id in its turn's latest ASSISTANT entry with tool calls,
// communicate messages that echo that entry's last text run dropped, and fold
// copies passed over. Every item is keyed by the entry and content part that
// opened it and versioned by the latest entry that contributed to it.
func referenceProjection(t testing.TB, path string) []referenceTurn {
	t.Helper()
	header, entries, unreadable := readFixtureFile(t, path)
	type turnState struct {
		id        string
		legacy    bool
		entries   []schema.Turn // legacy: the group's entries, for its stamp
		items     []appwire.ThreadItem
		positions []appwire.ThreadItemPosition
		calls     map[string]int // legacy: call id -> item
		version   uint64
		model     string
		// New format.
		execution  bool
		open       bool
		completion *schema.TurnCompletionInfo
		failure    *schema.Turn
		startedAt  *int64
		usage      llm.Usage
		// awaiting is the turn's latest ASSISTANT entry with tool calls:
		// each call id's first call there, its name, and the item it
		// projected (-1 for none).
		awaiting      *schema.Turn
		awaitingItem  map[string]int
		awaitingNames map[string]string
	}
	var turns []*turnState
	byID := map[string]*turnState{}
	var grouper apptranscript.TurnGrouper
	legacyNames := map[string]string{}
	add := func(s *turnState, item appwire.ThreadItem, entryIndex, part int) int {
		item.Version = uint64(entryIndex)
		s.items = append(s.items, item)
		s.positions = append(s.positions, appwire.ThreadItemPosition{Entry: uint64(entryIndex), Item: uint32(part)})
		return len(s.items) - 1
	}
	for i, entry := range entries {
		entryIndex := i + 1
		if decodeErr, ok := unreadable[i]; ok {
			// A turn of its own holding one error notice; it closes the
			// legacy group before it.
			grouper = apptranscript.TurnGrouper{}
			s := &turnState{id: fmt.Sprintf("turn_unreadable_%d", i), version: uint64(entryIndex)}
			turns = append(turns, s)
			add(s, appwire.ThreadItem{
				Type:        "systemMessage",
				ID:          fmt.Sprintf("item_unreadable_%d", i),
				Description: "Unreadable transcript entry",
				Text:        fmt.Sprintf("transcript entry %d could not be read: %s", i, decodeErr),
				Status:      appwire.TurnStatusCompleted,
				EventKind:   appwire.ThreadItemEventKindError,
			}, entryIndex, 0)
			continue
		}
		if entry.Format != schema.TurnFormatIdentity {
			if entry.Kind.TranscriptOnly() {
				continue // takes an entry index, and nothing else
			}
			id, isNew := grouper.Place(&entry, entryIndex)
			if isNew {
				turns = append(turns, &turnState{id: id, legacy: true, calls: map[string]int{}})
			}
			s := turns[len(turns)-1]
			s.entries = append(s.entries, entry)
			s.version = uint64(entryIndex)
			if entry.Model != "" {
				s.model = entry.Model
			}
			items, parts := apptranscript.ProjectTurnParts(id, entryIndex, entry, legacyNames, nil, apptranscript.ToolResultOutputImages)
			for j, item := range items {
				if apptranscript.MergesByCallID(item) {
					if at, ok := s.calls[item.CallID]; ok {
						completedAt := s.items[at].CompletedAtEntry
						s.items[at] = apptranscript.MergeThreadItems(s.items[at], item)
						s.items[at].Version, s.items[at].CompletedAtEntry = uint64(entryIndex), completedAt
						if entry.Kind == schema.TurnTool || entry.Kind == schema.TurnToolResults {
							s.items[at].CompletedAtEntry = uint64(entryIndex)
						}
						continue
					}
					s.calls[item.CallID] = len(s.items)
				}
				add(s, item, entryIndex, parts[j])
			}
			continue
		}
		if entry.OriginalOrdinal != nil {
			continue // a fold copy: its original already projected
		}
		grouper = apptranscript.TurnGrouper{}
		s := byID[entry.TurnID]
		if s == nil {
			execution := entry.TurnKind == schema.TurnSpanExecution || entry.TurnKind == ""
			s = &turnState{id: entry.TurnID, execution: execution, open: execution}
			turns = append(turns, s)
			byID[entry.TurnID] = s
		}
		s.version = uint64(entryIndex)
		if entry.Model != "" {
			s.model = entry.Model
		}
		if s.startedAt == nil && !entry.Timestamp.IsZero() {
			ms := entry.Timestamp.UnixMilli()
			s.startedAt = &ms
		}
		s.usage = s.usage.Add(entry.Usage)
		switch entry.Kind {
		case schema.TurnCompletion:
			if s.execution {
				info := schema.TurnCompletionInfo{}
				if entry.Completion != nil {
					info = *entry.Completion
				}
				s.open, s.completion = false, &info
				// Calls of the awaiting entry that no results completed
				// were interrupted.
				for _, at := range s.awaitingItem {
					if at >= 0 && s.items[at].CompletedAtEntry == 0 {
						s.items[at].Status = appwire.TurnStatusInterrupted
						s.items[at].Version = uint64(entryIndex)
					}
				}
			}
		case schema.TurnReopen:
			if s.execution {
				s.open, s.completion = true, nil
			}
		case schema.TurnFailure:
			failure := entry
			s.failure = &failure
		}
		seed := map[string]string{}
		if entry.Kind == schema.TurnTool || entry.Kind == schema.TurnToolResults {
			for _, part := range entry.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.Name == "" {
					if name, ok := s.awaitingNames[part.ToolResult.ToolCallID]; ok {
						seed[part.ToolResult.ToolCallID] = name
					}
				}
			}
		}
		if entry.Kind == schema.TurnCommunicate && entry.Communicate != nil && s.awaiting != nil &&
			apptranscript.EchoesAssistantText(referenceLastTextRun(s.awaiting.Message.Content), entry.Communicate.Message) {
			continue
		}
		items, parts := apptranscript.ProjectEntryParts(s.id, entryIndex, entry, seed, nil, apptranscript.ToolResultOutputImages)
		added := map[int]int{} // part -> item
		for j, item := range items {
			if entry.Kind == schema.TurnTool || entry.Kind == schema.TurnToolResults {
				if at, ok := s.awaitingItem[item.CallID]; ok && at >= 0 && apptranscript.MergesByCallID(item) {
					// The call keeps its id and round through its results.
					call := s.items[at]
					s.items[at] = apptranscript.MergeThreadItems(call, item)
					s.items[at].ID, s.items[at].RoundID, s.items[at].Version = call.ID, call.RoundID, uint64(entryIndex)
					s.items[at].CompletedAtEntry = uint64(entryIndex)
					continue
				}
			}
			added[parts[j]] = add(s, item, entryIndex, parts[j])
		}
		if entry.Kind == schema.TurnAssistant {
			calls := map[string]int{}
			names := map[string]string{}
			for part, content := range entry.Message.Content {
				if content.Kind != llm.ContentToolCall || content.ToolCall == nil {
					continue
				}
				if _, seen := calls[content.ToolCall.ID]; seen {
					continue
				}
				at, ok := added[part]
				if !ok {
					at = -1
				}
				calls[content.ToolCall.ID], names[content.ToolCall.ID] = at, content.ToolCall.Name
			}
			if len(calls) > 0 {
				awaiting := entry
				s.awaiting, s.awaitingItem, s.awaitingNames = &awaiting, calls, names
			}
		}
	}
	var out []referenceTurn
	if prelude := apptranscript.PreludeTurn(header); prelude != nil {
		for i := range prelude.Items {
			position := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
			prelude.Items[i].Position = &position
			prelude.Items[i].TranscriptKey = fmt.Sprintf("apptranscript-item-v2:prelude:header:%d", i)
		}
		out = append(out, referenceTurn{turn: *prelude})
	}
	for _, s := range turns {
		turn := appwire.Turn{ID: s.id, ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted, Version: s.version}
		for j := range s.items {
			position := s.positions[j]
			s.items[j].TurnID = s.id
			s.items[j].Position = &position
			s.items[j].TranscriptKey = ItemKey(s.id, position)
		}
		turn.Items = s.items
		if s.legacy {
			apptranscript.StampGroupedTurn(&turn, s.entries)
		} else {
			switch {
			case !s.execution:
			case s.open:
				turn.Status = appwire.TurnStatusInProgress
			case s.completion.Status == schema.TurnFailed:
				turn.Status = appwire.TurnStatusFailed
				if s.failure != nil {
					apptranscript.StampTurnFailure(&turn, *s.failure)
				}
			case s.completion.Status == schema.TurnInterrupted:
				turn.Status = appwire.TurnStatusInterrupted
			}
			if s.execution && !s.open {
				duration := s.completion.DurationMS
				turn.DurationMS = &duration
				if !s.completion.CompletedAt.IsZero() {
					completedAt := s.completion.CompletedAt.UnixMilli()
					turn.CompletedAt = &completedAt
				}
			}
			turn.StartedAt = s.startedAt
			turn.Usage = appwire.EvenerUsageFromLLM(s.usage)
		}
		out = append(out, referenceTurn{turn: turn, model: s.model})
	}
	return out
}

// referenceLastTextRun is the text of the last maximal run of consecutive
// text parts.
func referenceLastTextRun(content []llm.ContentPart) string {
	var runs []string
	inRun := false
	for _, part := range content {
		if part.Kind != llm.ContentText {
			inRun = false
			continue
		}
		if !inRun {
			runs = append(runs, "")
			inRun = true
		}
		runs[len(runs)-1] += part.Text
	}
	if len(runs) == 0 {
		return ""
	}
	return runs[len(runs)-1]
}

// referenceTurns is the reference projection's wire turns that hold items:
// the turns a reader sees.
func referenceTurns(t testing.TB, path string) []appwire.Turn {
	t.Helper()
	return shownTurns(referenceProjection(t, path))
}

func shownTurns(projection []referenceTurn) []appwire.Turn {
	var turns []appwire.Turn
	for _, turn := range projection {
		if len(turn.turn.Items) > 0 {
			turns = append(turns, turn.turn)
		}
	}
	return turns
}

// allTurns is every turn of the projection, including those with no items
// yet, whose summaries an index still updates.
func allTurns(projection []referenceTurn) []appwire.Turn {
	var turns []appwire.Turn
	for _, turn := range projection {
		turns = append(turns, turn.turn)
	}
	return turns
}

// referenceCandidates is the reference projection as the chronological
// candidate list a window read returns: items in position order, each
// candidate's Turn carrying no items, and HasEarlierItems/HasLaterItems
// reporting whether the neighbouring item belongs to the same turn.
func referenceCandidates(t testing.TB, path string) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	return candidatesOf(referenceProjection(t, path))
}

func candidatesOf(projection []referenceTurn) []appitempaging.TranscriptItemCandidate {
	var candidates []appitempaging.TranscriptItemCandidate
	var turnOf []int // each candidate's turn, by index
	for i, turn := range projection {
		scalars := turn.turn
		scalars.Items = nil
		for _, item := range turn.turn.Items {
			candidates = append(candidates, appitempaging.TranscriptItemCandidate{
				TurnID: scalars.ID, Turn: scalars, Item: item, Position: *item.Position, Model: turn.model,
			})
			turnOf = append(turnOf, i)
		}
	}
	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		pa, pb := candidates[a].Position, candidates[b].Position
		return cmp.Or(cmp.Compare(pa.Entry, pb.Entry), cmp.Compare(pa.Item, pb.Item))
	})
	sorted := make([]appitempaging.TranscriptItemCandidate, len(order))
	for i, at := range order {
		sorted[i] = candidates[at]
		sorted[i].HasEarlierItems = i > 0 && turnOf[order[i-1]] == turnOf[at]
		sorted[i].HasLaterItems = i+1 < len(order) && turnOf[order[i+1]] == turnOf[at]
	}
	return sorted
}

// stripPositions clears what today's projection lacks: positions, keys and
// versions.
func stripPositions(turns []appwire.Turn) []appwire.Turn {
	for i := range turns {
		turns[i].Version = 0
		for j := range turns[i].Items {
			turns[i].Items[j].Position = nil
			turns[i].Items[j].TranscriptKey = ""
			turns[i].Items[j].Version = 0
			turns[i].Items[j].CompletedAtEntry = 0
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
		if !fx.legacy() {
			continue // today's projection has no new-format rules
		}
		t.Run(fx.name, func(t *testing.T) {
			path := writeFixture(t, fx)
			toolNames := map[string]string{}
			today, err := apptranscript.ItemTurnsFromFile(path, testMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
				return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, apptranscript.ToolResultOutputImages)
			})
			if err != nil {
				t.Fatal(err)
			}
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
