package hub

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func snippetText(parts []appwire.SearchSnippetPart) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

// A short message is the whole snippet, on one line, with every word a search
// word prefixes marked, letter case aside.
func TestSearchSnippetMarksEveryMatchingWord(t *testing.T) {
	got := searchSnippet("The Settle pass\nraces the settled drain.", []string{"settl", "drain"})
	want := []appwire.SearchSnippetPart{
		{Text: "The "}, {Text: "Settle", Match: true}, {Text: " pass races the "},
		{Text: "settled", Match: true}, {Text: " "}, {Text: "drain", Match: true}, {Text: "."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snippet = %+v, want %+v", got, want)
	}
}

// A long message is cut around its first match: some words before it, the
// rest up to the bound, both cuts at word breaks and marked with an ellipsis.
func TestSearchSnippetCutsAroundTheFirstMatch(t *testing.T) {
	text := strings.Repeat("lead ", 40) + "the settle pass " + strings.Repeat("tail ", 60)
	got := searchSnippet(text, []string{"settle"})
	line := snippetText(got)
	if !strings.HasPrefix(line, "…lead") || !strings.HasSuffix(line, "tail…") {
		t.Fatalf("snippet %q, want word-aligned cuts on both sides", line)
	}
	if n := utf8.RuneCountInString(line); n > searchSnippetRunes+2 {
		t.Fatalf("snippet is %d runes, want at most %d and two ellipses", n, searchSnippetRunes)
	}
	if lead := strings.Index(line, "settle"); lead > searchSnippetLead+2 {
		t.Fatalf("the match sits %d bytes in, want at most %d runes of lead", lead, searchSnippetLead)
	}
}

// With no word to mark (the index matched in a way the words do not show) the
// snippet is the message's opening.
func TestSearchSnippetWithoutAMatchIsTheOpening(t *testing.T) {
	got := searchSnippet(strings.Repeat("word ", 100), []string{"settle"})
	line := snippetText(got)
	if len(got) != 1 || got[0].Match || !strings.HasPrefix(line, "word") || !strings.HasSuffix(line, "…") {
		t.Fatalf("snippet = %+v, want the unmarked opening, cut", got)
	}
}

// An empty message (Texts documents that a hit whose message left the index
// since Match comes back as "") must still produce a non-nil slice: the wire
// type has no omitempty on SearchHit.Snippet, so a nil slice would encode as
// JSON null against a client's non-nullable array type.
func TestSearchSnippetOfEmptyTextIsNeverNil(t *testing.T) {
	if got := searchSnippet("", []string{"settle"}); got == nil {
		t.Fatalf("searchSnippet(\"\", ...) = nil, want a non-nil (possibly empty) slice")
	}
}

// A query word containing an underscore (e.g. "settle_race") must mark a
// message that holds its parts as separate words, the way the messages
// index's unicode61 tokenizer actually matched it: it treats "_" as a
// separator, so the query rule (hubcore.SearchTokens) splits it into "settle"
// and "race" and the snippet marks both, never the joined identifier.
func TestSearchSnippetMarksWordsAnUnderscoreTokenSplitInTheIndex(t *testing.T) {
	got := searchSnippet("the settle race pass", hubcore.SearchTokens("settle_race"))
	want := []appwire.SearchSnippetPart{
		{Text: "the "}, {Text: "settle", Match: true}, {Text: " "}, {Text: "race", Match: true}, {Text: " pass"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snippet = %+v, want %+v", got, want)
	}
}

// A single matched word longer than searchSnippetRunes (a hash, a URL, a
// stack-trace line with no spaces) must still be marked whole rather than cut
// mid-word and silently losing its mark: the cut never lands before the
// match's own end.
func TestSearchSnippetKeepsALongMatchWhole(t *testing.T) {
	long := strings.Repeat("x", searchSnippetRunes+40)
	got := searchSnippet("before "+long+" after", []string{"x"})
	var marked strings.Builder
	for _, part := range got {
		if part.Match {
			marked.WriteString(part.Text)
		}
	}
	if marked.String() != long {
		t.Fatalf("marked text = %q, want the whole long match %q", marked.String(), long)
	}
}
