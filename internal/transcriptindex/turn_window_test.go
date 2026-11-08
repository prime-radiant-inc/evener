package transcriptindex

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

func TestResolveTurnWindowSelectsIdentifiedAndLegacyTurns(t *testing.T) {
	x := openTurnWindowFixture(t)

	for _, tc := range []struct {
		name      string
		selector  appwire.ThreadTurnWindow
		wantIDs   []string
		wantTexts []string
	}{
		{
			name:      "latest turns stay chronological",
			selector:  appwire.ThreadTurnWindow{Latest: 2},
			wantIDs:   []string{"t_identified_2", "t_identified_3"},
			wantTexts: []string{"identified two", "identified answer two", "identified three", "identified answer three"},
		},
		{
			name:      "latest window can span legacy and identified turns",
			selector:  appwire.ThreadTurnWindow{Latest: 4},
			wantIDs:   []string{"turn_3", "t_identified_1", "t_identified_2", "t_identified_3"},
			wantTexts: []string{"legacy two", "legacy answer two", "identified one", "identified answer one", "identified two", "identified answer two", "identified three", "identified answer three"},
		},
		{
			name:      "exact identified turn is inclusive",
			selector:  appwire.ThreadTurnWindow{Turn: "t_identified_1"},
			wantIDs:   []string{"t_identified_1"},
			wantTexts: []string{"identified one", "identified answer one"},
		},
		{
			name:      "exact legacy turn is inclusive",
			selector:  appwire.ThreadTurnWindow{Turn: "turn_3"},
			wantIDs:   []string{"turn_3"},
			wantTexts: []string{"legacy two", "legacy answer two"},
		},
		{
			name:      "since excludes boundary and starts with oldest unseen",
			selector:  appwire.ThreadTurnWindow{Since: "turn_3", Limit: 2},
			wantIDs:   []string{"t_identified_1", "t_identified_2"},
			wantTexts: []string{"identified one", "identified answer one", "identified two", "identified answer two"},
		},
		{
			name:      "since identified boundary is exclusive",
			selector:  appwire.ThreadTurnWindow{Since: "t_identified_2", Limit: 2},
			wantIDs:   []string{"t_identified_3"},
			wantTexts: []string{"identified three", "identified answer three"},
		},
		{
			name:      "before excludes boundary and returns nearest older turns chronologically",
			selector:  appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "t_identified_2", Limit: 2}},
			wantIDs:   []string{"turn_3", "t_identified_1"},
			wantTexts: []string{"legacy two", "legacy answer two", "identified one", "identified answer one"},
		},
		{
			name:      "before legacy boundary is exclusive",
			selector:  appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn_3", Limit: 2}},
			wantIDs:   []string{"turn_1"},
			wantTexts: []string{"legacy one", "legacy answer one"},
		},
		{
			name:      "since newest is empty",
			selector:  appwire.ThreadTurnWindow{Since: "t_identified_3", Limit: 3},
			wantIDs:   []string{},
			wantTexts: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := x.ResolveTurnWindow(tc.selector)
			if err != nil {
				t.Fatal(err)
			}
			assertResolvedTurnWindow(t, got, tc.wantIDs, tc.wantTexts)
		})
	}
}

func TestResolveTurnWindowUnknownIDReportsCurrentFrontier(t *testing.T) {
	x := openTurnWindowFixture(t)

	_, err := x.ResolveTurnWindow(appwire.ThreadTurnWindow{Since: "t_missing", Limit: 2})
	var notFound *TurnNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error type = %T, want *TurnNotFoundError", err)
	}
	want := TurnFrontier{OldestTurnID: "turn_1", NewestTurnID: "t_identified_3", TurnCount: 5}
	if notFound.TurnID != "t_missing" || notFound.Frontier != want {
		t.Fatalf("not-found facts = %+v, want id t_missing and frontier %+v", notFound, want)
	}
}

func TestSnapshotResolveTurnWindowUsesTheSharedSelection(t *testing.T) {
	x := openTurnWindowFixture(t)
	latest, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}

	var got ResolvedTurnWindow
	err = x.ReadAt(latest.Length, func(snapshot *Snapshot) error {
		var err error
		got, err = snapshot.ResolveTurnWindow(appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "t_identified_2", Limit: 2}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertResolvedTurnWindow(t, got,
		[]string{"turn_3", "t_identified_1"},
		[]string{"legacy two", "legacy answer two", "identified one", "identified answer one"},
	)
}

func TestCatchUpPreservesEmptyCheckpointInTurnFrontier(t *testing.T) {
	fx := fixture{
		header: transcript.Header{SessionID: "turn_window_catchup"},
		lines: []fixtureLine{
			entryLine(opens("turn_visible", schema.TurnSpanExecution, user("visible"))),
			entryLine(opens("turn_empty", schema.TurnSpanExecution, standalone(schema.TurnCheckpoint, ""))),
		},
	}
	header, lines := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, append(header, lines[0]...), 0o600); err != nil {
		t.Fatal(err)
	}
	x := openIndex(t, path, t.TempDir())
	appendBytes(t, path, lines[1])
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.CatchUpTo(info.Size()); err != nil {
		t.Fatal(err)
	}

	got, err := x.ResolveTurnWindow(appwire.ThreadTurnWindow{Turn: "turn_empty"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.TurnIDs, []string{"turn_empty"}) || len(got.Candidates) != 0 {
		t.Fatalf("resolved empty turn = IDs %v, candidates %v", got.TurnIDs, got.Candidates)
	}
	wantFrontier := TurnFrontier{OldestTurnID: "turn_visible", NewestTurnID: "turn_empty", TurnCount: 2}
	if got.Frontier != wantFrontier {
		t.Fatalf("frontier = %+v, want %+v", got.Frontier, wantFrontier)
	}
}

func openTurnWindowFixture(t testing.TB) *Index {
	t.Helper()
	fx := fixture{
		name:   "turn windows",
		header: transcript.Header{SessionID: "turn_windows"},
		lines: []fixtureLine{
			entryLine(user("legacy one")),
			entryLine(assistant(text("legacy answer one"))),
			entryLine(user("legacy two")),
			entryLine(assistant(text("legacy answer two"))),
			entryLine(opens("t_identified_1", schema.TurnSpanExecution, user("identified one"))),
			entryLine(inTurn("t_identified_1", assistant(text("identified answer one")))),
			entryLine(opens("t_identified_2", schema.TurnSpanExecution, user("identified two"))),
			entryLine(inTurn("t_identified_2", assistant(text("identified answer two")))),
			entryLine(opens("t_identified_3", schema.TurnSpanExecution, user("identified three"))),
			entryLine(inTurn("t_identified_3", assistant(text("identified answer three")))),
		},
	}
	return openIndex(t, writeFixture(t, fx), t.TempDir())
}

func assertResolvedTurnWindow(t testing.TB, got ResolvedTurnWindow, wantIDs, wantTexts []string) {
	t.Helper()
	if !reflect.DeepEqual(got.TurnIDs, wantIDs) {
		t.Fatalf("turn ids = %v, want %v", got.TurnIDs, wantIDs)
	}
	texts := make([]string, len(got.Candidates))
	for i := range got.Candidates {
		texts[i] = got.Candidates[i].Item.Text
	}
	if !reflect.DeepEqual(texts, wantTexts) {
		t.Fatalf("candidate texts = %v, want %v", texts, wantTexts)
	}
	if got.Frontier != (TurnFrontier{OldestTurnID: "turn_1", NewestTurnID: "t_identified_3", TurnCount: 5}) {
		t.Fatalf("frontier = %+v", got.Frontier)
	}
}
