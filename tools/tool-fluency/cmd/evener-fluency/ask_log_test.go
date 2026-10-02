package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestApplyAskExchangesReportsMalformedLogLines: a corrupt line in the ask
// log is kept out of Asks but reported as an infra finding, so a short Asks
// beside a full AskUserCalls is explained rather than silent.
func TestApplyAskExchangesReportsMalformedLogLines(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{WorkDir: filepath.Join(dir, "work"), CanonicalToolCounts: map[string]int{"ask_user": 2}}
	if err := os.WriteFile(filepath.Join(dir, "asks.jsonl"), []byte(
		`{"question":"Ship today?","answer":"Yes"}`+"\n"+
			`{"question":"Which env?","answ`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyAskExchanges(&res, probeFile{Person: &personSpec{Brief: "b"}})

	if len(res.Asks) != 1 {
		t.Errorf("Asks = %+v, want the one well-formed pair", res.Asks)
	}
	if len(res.Findings) != 1 || res.Findings[0].Category != "infra" {
		t.Fatalf("Findings = %+v, want one infra finding for the malformed line", res.Findings)
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

// TestApplyAskExchangesReportsMissingAsksWhenCallsMade: the run made
// ask_user calls (AskUserCalls > 0) but the responder logged nothing -- a
// missing or empty asks.jsonl -- so the failure is surfaced as an infra
// finding pointing at the probe's stderr, where the responder's own error is
// printed.
func TestApplyAskExchangesReportsMissingAsksWhenCallsMade(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{
		WorkDir:             filepath.Join(dir, "work"),
		StderrPath:          filepath.Join(dir, "stderr.ndjson"),
		CanonicalToolCounts: map[string]int{"ask_user": 2},
	}

	applyAskExchanges(&res, probeFile{Person: &personSpec{Brief: "b"}})

	if len(res.Findings) != 1 || res.Findings[0].Category != "infra" {
		t.Fatalf("Findings = %+v, want one infra finding when asks were made but none logged", res.Findings)
	}
	if !strings.Contains(res.Findings[0].Detail, res.StderrPath) {
		t.Errorf("finding detail = %q, want it to point at the responder stderr %q", res.Findings[0].Detail, res.StderrPath)
	}
}

// TestApplyAskExchangesReportsUnreadableLog: an ask log that exists but
// cannot be read (not merely absent) is reported rather than silently
// treated as no asks.
func TestApplyAskExchangesReportsUnreadableLog(t *testing.T) {
	dir := t.TempDir()
	// asks.jsonl is a directory, so reading it fails with something other
	// than not-exist.
	if err := os.MkdirAll(filepath.Join(dir, "asks.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := probeResult{
		WorkDir:             filepath.Join(dir, "work"),
		StderrPath:          filepath.Join(dir, "stderr.ndjson"),
		CanonicalToolCounts: map[string]int{"ask_user": 1},
	}

	applyAskExchanges(&res, probeFile{Person: &personSpec{Brief: "b"}})

	if len(res.Findings) != 1 || res.Findings[0].Category != "infra" {
		t.Fatalf("Findings = %+v, want one infra finding for the unreadable log", res.Findings)
	}
	if !strings.Contains(res.Findings[0].Detail, res.StderrPath) {
		t.Errorf("finding detail = %q, want it to point at the responder stderr %q", res.Findings[0].Detail, res.StderrPath)
	}
}

// TestApplyAskExchangesCountsBlankRecordsMalformed: a JSON null or {} line is
// an empty question and answer, not a real exchange, so it is counted as
// malformed rather than attached to Asks as a blank pair.
func TestApplyAskExchangesCountsBlankRecordsMalformed(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{
		WorkDir:             filepath.Join(dir, "work"),
		CanonicalToolCounts: map[string]int{"ask_user": 3},
	}
	if err := os.WriteFile(filepath.Join(dir, "asks.jsonl"), []byte(
		`{"question":"q","answer":"a"}`+"\n"+
			`null`+"\n"+
			`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyAskExchanges(&res, probeFile{Person: &personSpec{Brief: "b"}})

	if len(res.Asks) != 1 {
		t.Errorf("Asks = %+v, want only the one real pair", res.Asks)
	}
	if len(res.Findings) != 1 || res.Findings[0].Category != "infra" {
		t.Fatalf("Findings = %+v, want one infra finding for the 2 malformed records", res.Findings)
	}
}

// TestApplyAskExchangesBlankOnlyLogReportsMalformedNotMissing: a log holding
// only blank records is a corrupt log, not a responder that never ran, so
// the malformed finding is the one reported -- not the "no asks" finding,
// whose stderr pointer would misdirect the operator.
func TestApplyAskExchangesBlankOnlyLogReportsMalformedNotMissing(t *testing.T) {
	dir := t.TempDir()
	res := probeResult{
		WorkDir:             filepath.Join(dir, "work"),
		StderrPath:          filepath.Join(dir, "stderr.ndjson"),
		CanonicalToolCounts: map[string]int{"ask_user": 1},
	}
	if err := os.WriteFile(filepath.Join(dir, "asks.jsonl"), []byte(`null`+"\n"+`{}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyAskExchanges(&res, probeFile{Person: &personSpec{Brief: "b"}})

	if len(res.Findings) != 1 || res.Findings[0].Title != "ask log has malformed lines" {
		t.Fatalf("Findings = %+v, want just the malformed-lines finding", res.Findings)
	}
}
