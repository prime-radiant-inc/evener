package transcriptindex

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestServingCoverageBoundsAppendDuringCatchUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "coverage"})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	appendTurn := func(id string) {
		t.Helper()
		if err := writer.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: id, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(id)}); err != nil {
			t.Fatal(err)
		}
	}
	appendTurn("A")
	index, err := Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	moment := time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	var length int64
	var asOf time.Time
	err = index.ReadObserved(func() time.Time { appendTurn("B"); return moment }, func(snapshot *Snapshot, observed time.Time) error {
		window, err := snapshot.Latest(1)
		length, asOf = window.Length, observed
		if err == nil && window.LatestExecutionTurnID != "A" {
			t.Errorf("observed frontier=%q want A", window.LatestExecutionTurnID)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if length != info.Size() || !asOf.Equal(moment) {
		t.Fatalf("coverage=%d,%v want %d,%v", length, asOf, info.Size(), moment)
	}
	if err := index.ReadAt(length, func(snapshot *Snapshot) error {
		window, err := snapshot.Latest(1)
		if err == nil && window.LatestExecutionTurnID != "A" {
			t.Errorf("covered frontier=%q want A", window.LatestExecutionTurnID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	secondMoment := moment.Add(time.Minute)
	if err := index.ReadObserved(func() time.Time { appendTurn("C"); return secondMoment }, func(snapshot *Snapshot, asOf time.Time) error {
		window, err := snapshot.Latest(1)
		if err == nil && (window.LatestExecutionTurnID != "B" || !asOf.Equal(secondMoment)) {
			t.Errorf("observed view frontier=%q asOf=%v, want B at %v", window.LatestExecutionTurnID, asOf, secondMoment)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServingOlderWindowNamesViewExecutionAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "page-anchor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "C"} {
		if err := writer.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: id, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	latest, err := index.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	older, err := index.Before(latest.Candidates[0].Position, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Candidates) != 1 || older.Candidates[0].TurnID != "B" {
		t.Fatalf("older=%+v", older)
	}
	if older.LatestExecutionTurnID != "C" {
		t.Fatalf("older view frontier=%q want C", older.LatestExecutionTurnID)
	}
}

func TestServingCoverageRejectsReplacedFileDuringCatchUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "coverage"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "A", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("A")}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "index")
	built, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := built.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := OpenRefusing(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := bytes.ReplaceAll(original, []byte(`"A"`), []byte(`"B"`))
	if bytes.Equal(original, replacement) {
		t.Fatal("fixture found no A entry")
	}
	if err := os.WriteFile(path+".replacement", replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	moment := time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	clock := func() time.Time {
		if err := os.Rename(path+".replacement", path); err != nil {
			t.Fatal(err)
		}
		// The normal repair owner may finish before this serving read catches up.
		other, err := Open(path, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := other.Close(); err != nil {
			t.Fatal(err)
		}
		return moment
	}
	called := false
	err = index.ReadObserved(clock, func(*Snapshot, time.Time) error { called = true; return nil })
	if called || !errors.Is(err, ErrRebuildRequired) {
		t.Fatalf("replacement serving callback=%v error=%v", called, err)
	}
	// The new-cut refusal must not poison the legacy repair/read path.
	if err := index.CatchUp(); err != nil {
		t.Fatal(err)
	}
	recoveryMoment := moment.Add(time.Second)
	if err := index.ReadObserved(func() time.Time { return recoveryMoment }, func(snapshot *Snapshot, asOf time.Time) error {
		window, err := snapshot.Latest(1)
		if err == nil && (window.LatestExecutionTurnID != "B" || !asOf.Equal(recoveryMoment)) {
			t.Errorf("replacement recovery frontier=%q asOf=%v", window.LatestExecutionTurnID, asOf)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
