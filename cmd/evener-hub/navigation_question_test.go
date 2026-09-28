package hub

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A row asking a question names it: its text, the option labels and how many
// questions wait (S1b). The hub re-cuts what a daemon sent to the wire's
// bounds, so a remote host or an older daemon cannot widen a row, and a row
// with no question carries no key.
func TestNavigationRowsCarryThePendingQuestion(t *testing.T) {
	rows := liveNavigationRows(t, []hubcore.TreeNode{
		{ID: "session-asking", Title: "asking", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{
			Question: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 2,
		}},
		{ID: "session-wide", Title: "wide", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{
			Question: "Line one\nline two " + strings.Repeat("word ", 100), Options: []string{"A", "", "B", "C", "D", "E", "F"}, Count: 1,
		}},
		{ID: "session-working", Title: "working", Kind: "session", State: "active"},
	})

	want := &hubapi.NavigationQuestion{Text: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 2}
	if got := rows["session-asking"].Question; !reflect.DeepEqual(got, want) {
		t.Fatalf("asking row question = %+v, want %+v", got, want)
	}
	if got, want := string(navigationSummaryJSONFields(t, rows["session-asking"])["question"]), `{"text":"Keep or drop the implied options?","options":["Drop them","Keep them"],"count":2}`; got != want {
		t.Errorf("asking row question on the wire = %s, want %s", got, want)
	}
	wide := rows["session-wide"].Question
	if wide == nil || strings.ContainsAny(wide.Text, "\r\n") || utf8.RuneCountInString(wide.Text) > appwire.MaxQuestionTextRunes ||
		!reflect.DeepEqual(wide.Options, []string{"A", "B", "C", "D", "E"}) {
		t.Fatalf("wide row question = %+v, want one bounded line and five non-empty labels", wide)
	}
	if question, carried := navigationSummaryJSONFields(t, rows["session-working"])["question"]; carried {
		t.Fatalf("a row with no question carries %s", question)
	}
}

// A question the schema would refuse, which only a malformed daemon answer can
// carry, is dropped from its row instead of failing the whole resource; the
// row keeps its ask flag.
func TestNavigationRowsDropAQuestionTheSchemaRefuses(t *testing.T) {
	rows := liveNavigationRows(t, []hubcore.TreeNode{
		{ID: "session-blank", Title: "blank", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{Question: " \n ", Count: 1}},
		{ID: "session-uncounted", Title: "uncounted", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{Question: "Which?"}},
	})
	for _, id := range []string{"session-blank", "session-uncounted"} {
		row, listed := rows[id]
		if !listed || !row.AskPending || row.Question != nil {
			t.Errorf("%s = %+v (listed %v), want the asking row listed without a question", id, row.Question, listed)
		}
	}
}

// The hub schema refuses a question the codec would refuse, and text the
// projector's cut never yields.
func TestNavigationSchemaBoundsTheQuestion(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.Question = &hubapi.NavigationQuestion{
		Text:    strings.Repeat("é", appwire.MaxQuestionTextRunes),
		Options: []string{strings.Repeat("x", appwire.MaxQuestionOptionRunes), "B", "C", "D", "E"},
		Count:   1,
	}
	if !navigationSessionValueValid(session) {
		t.Fatal("a question at every bound was refused")
	}
	for name, question := range map[string]hubapi.NavigationQuestion{
		"empty text":                  {Count: 1},
		"text past its bound":         {Text: strings.Repeat("é", appwire.MaxQuestionTextRunes+1), Count: 1},
		"a line break":                {Text: "one\ntwo", Count: 1},
		"a leading space":             {Text: " Which?", Count: 1},
		"invalid UTF-8":               {Text: "ok\xff", Count: 1},
		"no question counted":         {Text: "Which?"},
		"six options":                 {Text: "Which?", Options: []string{"A", "B", "C", "D", "E", "F"}, Count: 1},
		"an empty option":             {Text: "Which?", Options: []string{""}, Count: 1},
		"an option past its bound":    {Text: "Which?", Options: []string{strings.Repeat("x", appwire.MaxQuestionOptionRunes+1)}, Count: 1},
		"an option with a line break": {Text: "Which?", Options: []string{"a\nb"}, Count: 1},
	} {
		session.Question = &question
		if navigationSessionValueValid(session) {
			t.Errorf("a question with %s was accepted", name)
		}
	}
}

// A cloned summary owns its question: a row handed to one reader cannot change
// another's.
func TestCloneNavigationSummaryOwnsTheQuestion(t *testing.T) {
	original := hubapi.NavigationSessionSummary{Question: &hubapi.NavigationQuestion{Text: "Which?", Options: []string{"A", "B"}, Count: 1}}
	clone := cloneNavigationSummary(original)
	original.Question.Options[0] = "changed"
	original.Question.Count = 9
	if clone.Question == nil || clone.Question.Options[0] != "A" || clone.Question.Count != 1 {
		t.Fatalf("clone question = %+v, want its own copy", clone.Question)
	}
}
