package hub

import (
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A row carries the opening of its session's last agent message (S1d): a
// Finished row's why line. The hub re-cuts what a daemon or a meta held to one
// line at the wire's bound, and a row with no message carries no key.
func TestNavigationRowsCarryTheLastMessage(t *testing.T) {
	rows := liveNavigationRows(t, []hubcore.TreeNode{
		{ID: "session-finished", Title: "finished", Kind: "session", State: "idle", LastMessage: "Three layouts are ready for review. I recommend B."},
		{ID: "session-wide", Title: "wide", Kind: "session", State: "idle", LastMessage: "## Summary\n\n" + strings.Repeat("It keeps the project first. ", 20)},
		{ID: "session-silent", Title: "silent", Kind: "session", State: "idle"},
	})
	if got, want := string(navigationSummaryJSONFields(t, rows["session-finished"])["last_message"]), `"Three layouts are ready for review. I recommend B."`; got != want {
		t.Fatalf("finished row last_message on the wire = %s, want %s", got, want)
	}
	wide := rows["session-wide"].LastMessage
	if !strings.HasPrefix(wide, "## Summary It keeps the project first.") || strings.ContainsAny(wide, "\r\n") ||
		utf8.RuneCountInString(wide) > appwire.MaxMessageExcerptRunes {
		t.Fatalf("wide row last message = %q, want one line cut to %d runes", wide, appwire.MaxMessageExcerptRunes)
	}
	if message, carried := navigationSummaryJSONFields(t, rows["session-silent"])["last_message"]; carried {
		t.Fatalf("a row with no message carries %s", message)
	}
}

// The hub schema holds a row's last message to the excerpt the projector's cut
// yields: one trimmed line of valid UTF-8 within the bound.
func TestNavigationSchemaBoundsTheLastMessage(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.LastMessage = strings.Repeat("é", appwire.MaxMessageExcerptRunes)
	if !navigationSessionValueValid(session) {
		t.Fatal("a last message at its bound was refused")
	}
	for name, message := range map[string]string{
		"past its bound":  strings.Repeat("é", appwire.MaxMessageExcerptRunes+1),
		"a line break":    "Three layouts\nare ready.",
		"a leading space": " Three layouts are ready.",
		"invalid UTF-8":   "ready\xff",
	} {
		session.LastMessage = message
		if navigationSessionValueValid(session) {
			t.Errorf("a last message with %s was accepted", name)
		}
	}
}
