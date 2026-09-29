package agent

import (
	"reflect"
	"testing"
)

// TestParseAskUserCallArgumentsBatchForm checks that a batch-form ask_user
// call's raw arguments (as events.ToolCallStartData.ArgumentsJSON captures
// them, before normalizeAskArgs runs) parse into full questions, including
// each option's detail text — the piece askQuestion and PendingQuestion
// deliberately omit, per session_tools_ask.go's own doc comments.
func TestParseAskUserCallArgumentsBatchForm(t *testing.T) {
	raw := `{"questions":[{"header":"DB choice","question":"Which database?","options":[{"label":"Postgres","detail":"Battle-tested, relational"},{"label":"SQLite","detail":"Zero ops, embedded"}]}]}`
	got, err := ParseAskUserCallArguments([]byte(raw))
	if err != nil {
		t.Fatalf("ParseAskUserCallArguments: %v", err)
	}
	want := []AskUserQuestion{
		{
			Header:   "DB choice",
			Question: "Which database?",
			Options: []AskUserOption{
				{Label: "Postgres", Detail: "Battle-tested, relational"},
				{Label: "SQLite", Detail: "Zero ops, embedded"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestParseAskUserCallArgumentsShorthandForm checks that the single-question
// shorthand (question+options at top level, no "questions" array) is
// normalized the same way the live tool call is before it parses — a model
// can use either form.
func TestParseAskUserCallArgumentsShorthandForm(t *testing.T) {
	raw := `{"question":"Ship today?","options":[{"label":"Yes","detail":"Ready now"},{"label":"No","detail":"Needs more time"}]}`
	got, err := ParseAskUserCallArguments([]byte(raw))
	if err != nil {
		t.Fatalf("ParseAskUserCallArguments: %v", err)
	}
	if len(got) != 1 || got[0].Question != "Ship today?" || len(got[0].Options) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Options[0].Detail != "Ready now" {
		t.Fatalf("option detail lost: %+v", got[0].Options[0])
	}
}

// TestParseAskUserCallArgumentsInvalidJSON: malformed arguments are an
// error, not a silent empty result — a caller (the ask-responder loop) must
// be able to tell "nothing was pending" apart from "the call's own
// arguments were unparseable".
func TestParseAskUserCallArgumentsInvalidJSON(t *testing.T) {
	if _, err := ParseAskUserCallArguments([]byte("not json")); err == nil {
		t.Fatal("want an error for invalid JSON")
	}
}

// TestParseAskUserCallArgumentsSemanticViolation propagates
// normalizeAskArgs/parseAskQuestions-style semantic errors (duplicate
// labels), the same rule the live Exec enforces.
func TestParseAskUserCallArgumentsSemanticViolation(t *testing.T) {
	raw := `{"questions":[{"question":"Which?","options":[{"label":"A","detail":"x"},{"label":"A","detail":"y"}]}]}`
	if _, err := ParseAskUserCallArguments([]byte(raw)); err == nil {
		t.Fatal("want an error for duplicate option labels")
	}
}
