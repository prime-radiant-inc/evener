package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// parityDivergence is one kind of difference between the live view of a
// thread and the projection of its transcript file. It names no ids, text or
// counts, so the set a scenario produces is the same on every run.
type parityDivergence struct {
	// Class is "live-only-item", "file-only-item", "item-field", "turn-field"
	// or "turn-split".
	Class string
	// Subject is the item's type (with its event kind, for a systemMessage),
	// or for a turn, the subject of its first item.
	Subject string
	// Field is the differing wire field, for item-field and turn-field.
	Field string
}

func (d parityDivergence) String() string {
	return strings.TrimSuffix(fmt.Sprintf("%s %s %s", d.Class, d.Subject, d.Field), " ")
}

// knownDivergence is one row of a parity table: a divergence today's code
// produces, and the spec phase that removes it.
type knownDivergence struct {
	parityDivergence
	Phase int
	Why   string
}

type parityItem struct {
	turn int
	item appwire.ThreadItem
}

func flattenParityItems(turns []appwire.Turn) []parityItem {
	var items []parityItem
	for ti, turn := range turns {
		for _, item := range turn.Items {
			items = append(items, parityItem{turn: ti, item: item})
		}
	}
	return items
}

func paritySubject(item appwire.ThreadItem) string {
	if item.Type == "systemMessage" && item.EventKind != "" {
		return item.Type + "/" + string(item.EventKind)
	}
	return item.Type
}

// paritySignature is what makes a live item and a file item the same item:
// its kind and content, not its identity, which is what is under test.
func paritySignature(item appwire.ThreadItem) string {
	text := strings.TrimSpace(item.Text)
	if item.Type == "commandExecution" {
		text = ""
	}
	return strings.Join([]string{paritySubject(item), item.CallID, item.ToolName, text}, "\x00")
}

// diffParity aligns the live items with the file items and reports every kind
// of difference between them.
func diffParity(live, file []appwire.Turn) map[parityDivergence]struct{} {
	found := map[parityDivergence]struct{}{}
	add := func(class, subject, field string) {
		found[parityDivergence{Class: class, Subject: subject, Field: field}] = struct{}{}
	}
	liveItems, fileItems := flattenParityItems(live), flattenParityItems(file)
	pairs := alignParityItems(liveItems, fileItems)

	liveMatched := make([]bool, len(liveItems))
	fileMatched := make([]bool, len(fileItems))
	liveTurnTo := map[int][]int{}
	fileTurnFrom := map[int][]int{}
	for _, pair := range pairs {
		l, f := liveItems[pair[0]], fileItems[pair[1]]
		liveMatched[pair[0]], fileMatched[pair[1]] = true, true
		for _, field := range differingWireFields(l.item, f.item, nil) {
			add("item-field", paritySubject(l.item), field)
		}
		if !slices.Contains(liveTurnTo[l.turn], f.turn) {
			liveTurnTo[l.turn] = append(liveTurnTo[l.turn], f.turn)
		}
		if !slices.Contains(fileTurnFrom[f.turn], l.turn) {
			fileTurnFrom[f.turn] = append(fileTurnFrom[f.turn], l.turn)
		}
	}
	for i, matched := range liveMatched {
		if !matched {
			add("live-only-item", paritySubject(liveItems[i].item), "")
		}
	}
	for i, matched := range fileMatched {
		if !matched {
			add("file-only-item", paritySubject(fileItems[i].item), "")
		}
	}
	turnSubject := func(turn appwire.Turn) string {
		if len(turn.Items) == 0 {
			return "empty"
		}
		return paritySubject(turn.Items[0])
	}
	for liveTurn, fileTurns := range liveTurnTo {
		if len(fileTurns) > 1 {
			add("turn-split", "live turn spans file turns", "")
		}
		if len(fileTurns) != 1 || len(fileTurnFrom[fileTurns[0]]) != 1 {
			continue
		}
		for _, field := range differingWireFields(live[liveTurn], file[fileTurns[0]], []string{"items"}) {
			add("turn-field", turnSubject(live[liveTurn]), field)
		}
	}
	for _, liveTurns := range fileTurnFrom {
		if len(liveTurns) > 1 {
			add("turn-split", "file turn spans live turns", "")
		}
	}
	return found
}

// alignParityItems pairs live and file items with the longest common
// subsequence of their signatures, as index pairs in order.
func alignParityItems(live, file []parityItem) [][2]int {
	n, m := len(live), len(file)
	liveSigs, fileSigs := make([]string, n), make([]string, m)
	for i := range live {
		liveSigs[i] = paritySignature(live[i].item)
	}
	for j := range file {
		fileSigs[j] = paritySignature(file[j].item)
	}
	lengths := make([][]int, n+1)
	for i := range lengths {
		lengths[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if liveSigs[i] == fileSigs[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else {
				lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			}
		}
	}
	var pairs [][2]int
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case liveSigs[i] == fileSigs[j]:
			pairs = append(pairs, [2]int{i, j})
			i++
			j++
		case lengths[i+1][j] >= lengths[i][j+1]:
			i++
		default:
			j++
		}
	}
	return pairs
}

// parityClockFields hold wall-clock readings. The live view and the file take
// theirs at different moments (an event and the entry it wrote), usually in
// the same millisecond but not always, so they are compared for presence.
var parityClockFields = []string{"startedAt", "completedAt", "durationMs"}

// differingWireFields reports the JSON fields whose encoded values differ
// between a and b: the difference a client receiving each would see. Clock
// fields differ only when one side has the field and the other does not.
func differingWireFields(a, b any, ignore []string) []string {
	encode := func(v any) map[string]json.RawMessage {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		fields := map[string]json.RawMessage{}
		if err := json.Unmarshal(data, &fields); err != nil {
			panic(err)
		}
		return fields
	}
	left, right := encode(a), encode(b)
	var differing []string
	for field := range left {
		if _, ok := right[field]; !ok {
			right[field] = nil
		}
	}
	for field, value := range right {
		if slices.Contains(ignore, field) {
			continue
		}
		if slices.Contains(parityClockFields, field) {
			if (left[field] == nil) != (value == nil) {
				differing = append(differing, field)
			}
			continue
		}
		if !bytes.Equal(left[field], value) {
			differing = append(differing, field)
		}
	}
	slices.Sort(differing)
	return differing
}

// checkParity fails on an observed divergence the table does not list, and on
// a listed one that no longer occurs.
func checkParity(t testing.TB, observed map[parityDivergence]struct{}, table []knownDivergence) {
	t.Helper()
	listed := map[parityDivergence]bool{}
	for _, row := range table {
		listed[row.parityDivergence] = true
	}
	var unlisted, gone []string
	for divergence := range observed {
		if !listed[divergence] {
			unlisted = append(unlisted, divergence.String())
		}
	}
	for _, row := range table {
		if _, ok := observed[row.parityDivergence]; !ok {
			gone = append(gone, fmt.Sprintf("%s (phase %d: %s)", row, row.Phase, row.Why))
		}
	}
	slices.Sort(unlisted)
	slices.Sort(gone)
	if len(unlisted) > 0 {
		t.Errorf("new live-versus-file divergences (list each in the table with the phase that removes it):\n  %s", strings.Join(unlisted, "\n  "))
	}
	if len(gone) > 0 {
		t.Errorf("listed divergences that no longer occur (remove their rows):\n  %s", strings.Join(gone, "\n  "))
	}
}

func parityTurn(id string, items ...appwire.ThreadItem) appwire.Turn {
	for i := range items {
		items[i].TurnID = id
	}
	return appwire.Turn{ID: id, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull, Items: items}
}

func TestParityDiffOfIdenticalViewsIsEmpty(t *testing.T) {
	turns := []appwire.Turn{parityTurn("turn_1",
		appwire.ThreadItem{Type: "userMessage", ID: "u", Text: "hi"},
		appwire.ThreadItem{Type: "agentMessage", ID: "a", Text: "hello"},
	)}
	if got := diffParity(turns, turns); len(got) != 0 {
		t.Fatalf("identical views diverge: %v", got)
	}
}

func TestParityDiffReportsEachKindOfDivergence(t *testing.T) {
	live := []appwire.Turn{
		parityTurn("turn_1",
			appwire.ThreadItem{Type: "userMessage", ID: "u_live", Text: "hi"},
			appwire.ThreadItem{Type: "systemMessage", EventKind: appwire.ThreadItemEventKindHookCompleted, ID: "w", Text: "careful"},
			appwire.ThreadItem{Type: "agentMessage", ID: "a", Text: "hello"},
		),
		parityTurn("turn_2", appwire.ThreadItem{Type: "userMessage", ID: "u2", Text: "again"}),
	}
	failed := parityTurn("turn_2", appwire.ThreadItem{Type: "userMessage", ID: "u2", Text: "again"})
	failed.Status = appwire.TurnStatusFailed
	file := []appwire.Turn{
		parityTurn("turn_1", appwire.ThreadItem{Type: "userMessage", ID: "u_file", Text: "hi"}),
		parityTurn("turn_3", appwire.ThreadItem{Type: "agentMessage", ID: "a", Text: "hello"}),
		failed,
		parityTurn("turn_4", appwire.ThreadItem{Type: "systemMessage", EventKind: appwire.ThreadItemEventKindError, ID: "f", Text: "failed"}),
	}
	got := diffParity(live, file)
	want := []parityDivergence{
		{Class: "item-field", Subject: "userMessage", Field: "id"},
		{Class: "item-field", Subject: "agentMessage", Field: "turnId"},
		{Class: "live-only-item", Subject: "systemMessage/hook_completed"},
		{Class: "file-only-item", Subject: "systemMessage/error"},
		{Class: "turn-split", Subject: "live turn spans file turns"},
		{Class: "turn-field", Subject: "userMessage", Field: "status"},
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing divergence %s; got %v", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d divergences, want %d: %v", len(got), len(want), got)
	}
}

// recordingTB records Errorf calls so checkParity's own failures can be
// asserted.
type recordingTB struct {
	testing.TB
	errors []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestCheckParityFailsOnUnlistedAndStaleRows(t *testing.T) {
	observed := map[parityDivergence]struct{}{
		{Class: "item-field", Subject: "userMessage", Field: "id"}:         {},
		{Class: "live-only-item", Subject: "systemMessage/hook_completed"}: {},
	}
	table := []knownDivergence{
		{Class: "item-field", Subject: "userMessage", Field: "id", Phase: 3, Why: "identity"},
		{Class: "turn-split", Subject: "live turn spans file turns", Phase: 3, Why: "grouping"},
	}
	recorder := &recordingTB{TB: t}
	checkParity(recorder, observed, table)
	if len(recorder.errors) != 2 || !strings.Contains(recorder.errors[0], "live-only-item systemMessage/hook_completed") || !strings.Contains(recorder.errors[1], "turn-split live turn spans file turns") {
		t.Fatalf("checkParity errors = %q", recorder.errors)
	}
	clean := &recordingTB{TB: t}
	checkParity(clean, observed, append(table[:1:1], knownDivergence{Class: "live-only-item", Subject: "systemMessage/hook_completed", Phase: 3}))
	if len(clean.errors) != 0 {
		t.Fatalf("a matching table failed: %q", clean.errors)
	}
}

func TestParityDiffComparesClockFieldsByPresence(t *testing.T) {
	early, late := int64(1000), int64(1001)
	live := []appwire.Turn{parityTurn("turn_1",
		appwire.ThreadItem{Type: "userMessage", ID: "u", Text: "hi", StartedAt: &early},
		appwire.ThreadItem{Type: "agentMessage", ID: "a", Text: "hello", StartedAt: &early},
	)}
	file := []appwire.Turn{parityTurn("turn_1",
		appwire.ThreadItem{Type: "userMessage", ID: "u", Text: "hi", StartedAt: &late},
		appwire.ThreadItem{Type: "agentMessage", ID: "a", Text: "hello"},
	)}
	got := diffParity(live, file)
	want := parityDivergence{Class: "item-field", Subject: "agentMessage", Field: "startedAt"}
	if _, ok := got[want]; !ok || len(got) != 1 {
		t.Fatalf("diff = %v, want only %s", got, want)
	}
}
