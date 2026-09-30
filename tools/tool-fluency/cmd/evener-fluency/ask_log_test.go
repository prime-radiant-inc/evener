package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestApplyAskExchangesReadsLogIntoResult: a task with a person: block gets
// its --ask-responder question/answer pairs (asks.jsonl, beside the work
// dir) attached to the probe result, along with the ask_user call count
// already computed from the transcript's canonical tool counts.
func TestApplyAskExchangesReadsLogIntoResult(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{
		WorkDir:             filepath.Join(dir, "work"),
		CanonicalToolCounts: map[string]int{"ask_user": 2, "read_file": 3},
	}
	logPath := filepath.Join(dir, "asks.jsonl")
	if err := os.WriteFile(logPath, []byte(
		`{"question":"Ship today?","answer":"Yes"}`+"\n"+
			`{"question":"Which env?","answer":"staging"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyAskExchanges(&res, probeFile{Person: &personSpec{Brief: "b"}})

	if res.AskUserCalls != 2 {
		t.Errorf("AskUserCalls = %d, want 2", res.AskUserCalls)
	}
	want := []askExchange{
		{Question: "Ship today?", Answer: "Yes"},
		{Question: "Which env?", Answer: "staging"},
	}
	if !reflect.DeepEqual(res.Asks, want) {
		t.Errorf("Asks = %+v, want %+v", res.Asks, want)
	}
}

// TestApplyAskExchangesNoopWithoutPerson: a task with no person: block
// leaves the result untouched, even if a stray asks.jsonl exists.
func TestApplyAskExchangesNoopWithoutPerson(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{WorkDir: filepath.Join(dir, "work"), CanonicalToolCounts: map[string]int{"ask_user": 1}}
	if err := os.WriteFile(filepath.Join(dir, "asks.jsonl"), []byte(`{"question":"q","answer":"a"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyAskExchanges(&res, probeFile{})

	if res.AskUserCalls != 0 || res.Asks != nil {
		t.Errorf("result = %+v, want untouched with no person: block", res)
	}
}

// TestApplyAskExchangesMissingLogIsNotAnError: when the responder never ran
// (or the log was never created), the result gets a zero count and no asks,
// not an error.
func TestApplyAskExchangesMissingLogIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{WorkDir: filepath.Join(dir, "work")}

	applyAskExchanges(&res, probeFile{Person: &personSpec{Brief: "b"}})

	if res.AskUserCalls != 0 || res.Asks != nil {
		t.Errorf("result = %+v, want zero/nil with no log file", res)
	}
}
