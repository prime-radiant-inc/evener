package hub

import (
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A row names its session's model by the name model/list gives it (S17): the
// Board's "Show model on Board rows" and a row's last line. A dated snapshot
// id names its family, as the picker does; an over-long id is cut to the label
// bound; and a row with no model carries no key.
func TestNavigationRowsCarryTheModelName(t *testing.T) {
	rows := liveNavigationRows(t, []hubcore.TreeNode{
		{ID: "session-gpt", Title: "gpt", Kind: "session", State: "idle", Model: "gpt-5.6"},
		{ID: "session-dated", Title: "dated", Kind: "session", State: "idle", Model: "claude-opus-4-7-20260101"},
		{ID: "session-long", Title: "long", Kind: "session", State: "idle", Model: strings.Repeat("m", maxNavigationLabelRunes+40)},
		{ID: "session-unknown", Title: "unknown", Kind: "session", State: "idle"},
	})
	if got, want := string(navigationSummaryJSONFields(t, rows["session-gpt"])["model_name"]), `"Gpt 5.6"`; got != want {
		t.Fatalf("model_name on the wire = %s, want %s", got, want)
	}
	if got, want := rows["session-dated"].ModelName, prettifyModelDisplayName("claude-opus-4-7-20260101"); got != want || got != "Claude Opus 4 7" {
		t.Fatalf("dated model name = %q, want the picker's %q", got, want)
	}
	if got := utf8.RuneCountInString(rows["session-long"].ModelName); got != maxNavigationLabelRunes {
		t.Fatalf("long model name has %d runes, want the %d-rune label bound", got, maxNavigationLabelRunes)
	}
	if name, carried := navigationSummaryJSONFields(t, rows["session-unknown"])["model_name"]; carried {
		t.Fatalf("a row with no model carries %s", name)
	}
}

// The hub schema holds a row's model name to the label bound the projector
// cuts it to.
func TestNavigationSchemaBoundsTheModelName(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.ModelName = strings.Repeat("é", maxNavigationLabelRunes)
	if !navigationSessionValueValid(session) {
		t.Fatal("a model name at its bound was refused")
	}
	session.ModelName = strings.Repeat("é", maxNavigationLabelRunes+1)
	if navigationSessionValueValid(session) {
		t.Fatal("a model name past its bound was accepted")
	}
}
