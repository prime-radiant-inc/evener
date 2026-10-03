package memory

import (
	"strings"
	"testing"
	"time"
)

// These cases catch source reserialization and model-authored date authority.
func TestIndexPreservesProseAndDerivesDates(t *testing.T) {
	t.Parallel()
	source := "Intro SENTINEL_793\n\n## Testing\n\n- [Harness](harness.md): Run from root.\n"
	doc, err := parseIndex(source)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 3, 23, 0, 0, 0, time.UTC)
	updated := created.Add(2 * time.Hour)
	got := renderIndex(doc, map[string]PageMeta{"harness": {Created: created, Updated: updated}})
	want := "Intro SENTINEL_793\n\n## Testing\n\n- [Harness](harness.md): Run from root. (created 2026-10-03, updated 2026-10-04, reviewed never)\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A date marker inside ordinary prose must not reserve the whole summary.
func TestIndexPreservesInteriorCreatedProse(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 10, 3, 23, 0, 0, 0, time.UTC)
	for _, source := range []string{
		"Intro\n\n- [A](a.md): Built (created by CI), rerun nightly.\n",
		"Intro\n\n- [A](a.md): Built (created by CI), rerun nightly. (created 2001-01-01, updated 2002-01-01, reviewed never)\n",
	} {
		doc, err := parseIndex(source)
		if err != nil {
			t.Fatalf("valid interior prose rejected: %v", err)
		}
		if len(doc.Entries) != 1 || doc.Entries[0].Summary != "Built (created by CI), rerun nightly." {
			t.Fatalf("entries %#v", doc.Entries)
		}
		got := renderIndex(doc, map[string]PageMeta{"a": {Created: clock, Updated: clock}})
		want := "Intro\n\n- [A](a.md): Built (created by CI), rerun nightly. (created 2026-10-03, updated 2026-10-03, reviewed never)\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

// A nested parenthesis in prose is not a malformed date field list.
func TestIndexPreservesNestedCreatedProse(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, source := range []string{
		"- [A](a.md): S (created by CI (nightly))\n",
		"- [A](a.md): S (created by CI (nightly)) (created 2024-02-29, updated 2024-03-01, reviewed never)\n",
	} {
		doc, err := parseIndex(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Entries) != 1 || doc.Entries[0].Summary != "S (created by CI (nightly))" {
			t.Fatalf("entries %#v", doc.Entries)
		}
		want := "- [A](a.md): S (created by CI (nightly)) (created 2026-10-03, updated 2026-10-03, reviewed never)\n"
		if got := renderIndex(doc, map[string]PageMeta{"a": {Created: clock, Updated: clock}}); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

// Malformed delimiters must not turn a reserved terminal clause into prose.
func TestIndexRejectsMalformedTerminalDateDelimiters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, suffix string
	}{
		{"invalid calendar extra close", " (created 2026-99-01, updated 2026-10-03, reviewed never))"},
		{"valid calendar extra close", " (created 2026-10-03, updated 2026-10-03, reviewed never))"},
		{"multiple extra closes", " (created 2026-10-03, updated 2026-10-03, reviewed never)))"},
		{"extra open", " ((created 2026-10-03, updated 2026-10-03, reviewed never)"},
		{"nested open", " (created (2026-10-03, updated 2026-10-03, reviewed never)"},
		{"parenthesized created", " (created (2026-10-03), updated 2026-10-03, reviewed never)"},
		{"parenthesized updated", " (created 2026-10-03, updated (2026-10-03), reviewed never)"},
		{"parenthesized reviewed", " (created 2026-10-03, updated 2026-10-03, reviewed (never))"},
		{"missing close", " (created 2026-10-03, updated 2026-10-03, reviewed never"},
		{"malformed repeated clause", " (created nope)) (created 2026-10-03, updated 2026-10-03, reviewed never)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "- [A](a.md): S" + tc.suffix + "\n"
			doc, err := parseIndex(source)
			if err == nil {
				t.Fatalf("accepted malformed terminal date suffix: entries=%#v source=%q", doc.Entries, source)
			}
			assertCode(t, err, "invalid_input")
		})
	}
}

// Copied dates normalize without changing prose, whitespace or full-row ranges.
func TestIndexCopiedDateSuffixBytePreservation(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	reviewed := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, source, want string
		start, end         int
	}{
		{"LF", "α\n\n- [A](a.md): Built (created by CI), rerun nightly. (created 2024-02-29, updated 2024-03-01, reviewed 2024-03-02)\n", "α\n\n- [A](a.md): Built (created by CI), rerun nightly. (created 2026-10-03, updated 2026-10-04, reviewed 2026-10-05)\n", 4, 117},
		{"CRLF whitespace", "α\r\n\r\n- [A](a.md): Built (created by CI), rerun nightly. (created 2024-02-29, updated 2024-03-01, reviewed 2024-03-02) \t\r\n", "α\r\n\r\n- [A](a.md): Built (created by CI), rerun nightly. (created 2026-10-03, updated 2026-10-04, reviewed 2026-10-05) \t\r\n", 6, 122},
		{"unterminated whitespace", "α\n\n- [A](a.md): Built (created by CI), rerun nightly. (created 2024-02-29, updated 2024-03-01, reviewed 2024-03-02) \t", "α\n\n- [A](a.md): Built (created by CI), rerun nightly. (created 2026-10-03, updated 2026-10-04, reviewed 2026-10-05) \t", 4, 118},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := parseIndex(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Entries) != 1 {
				t.Fatalf("entries %#v", doc.Entries)
			}
			entry := doc.Entries[0]
			if entry.Summary != "Built (created by CI), rerun nightly." || entry.Start != tc.start || entry.End != tc.end || entry.Line != 3 {
				t.Fatalf("entry %#v, want range %d:%d on line 3", entry, tc.start, tc.end)
			}
			if got := renderIndex(doc, map[string]PageMeta{"a": {Created: created, Updated: updated, Reviewed: &reviewed}}); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIndexDatesCannotBeModelAuthority(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 10, 3, 23, 0, 0, 0, time.UTC)
	request := ApplyRequest{OperationID: "date", Pages: []PageOperation{{Kind: "put", PageID: "a", Body: str("body")}}, Index: str("- [A](a.md): S (created 2001-01-01, updated 2002-01-01, reviewed 2003-01-01)\n")}
	tx, changed, err := buildBatch(Snapshot{}, request, Actor{}, clock)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got := image(t, tx, "index.md")
	if string(got) != "- [A](a.md): S (created 2026-10-03, updated 2026-10-03, reviewed never)\n" {
		t.Fatalf("index %q", got)
	}
	for _, suffix := range []string{" (created 2026-99-01, updated 2026-10-03, reviewed never)", " (created nope)", " (created 2026-10-03, updated 2026-10-03, reviewed never) (created 2026-10-03, updated 2026-10-03, reviewed never)"} {
		request.Index = str("- [A](a.md): S" + suffix + "\n")
		_, _, err := buildBatch(Snapshot{}, request, Actor{}, clock)
		assertCode(t, err, "invalid_input")
	}
	request.Index = str("- [A](a.md): " + strings.Repeat("x", 16384-13))
	if len(*request.Index) != 16384 {
		t.Fatal(len(*request.Index))
	}
	_, _, err = buildBatch(Snapshot{}, request, Actor{}, clock)
	assertLimit(t, err, "index", 16441, 16384)
}

func TestIndexGrammarAndBytePreservation(t *testing.T) {
	t.Parallel()
	source := "Prose α\r\n\r\n## H\r\n\r\n- [A\\] &amp; B](a.md): S\\! &amp; T  \r\n\r\n```\n- [Fake](fake.md): ignored\n```\n"
	doc, err := parseIndex(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Entries) != 1 || doc.Entries[0].PageID != "a" || doc.Entries[0].Label != "A] & B" || doc.Entries[0].Summary != "S! & T" || doc.Entries[0].Line != 5 {
		t.Fatalf("entries %#v", doc.Entries)
	}
	clock := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	got := renderIndex(doc, map[string]PageMeta{"a": {Created: clock, Updated: clock}})
	want := "Prose α\r\n\r\n## H\r\n\r\n- [A\\] &amp; B](a.md): S\\! &amp; T (created 2026-10-03, updated 2026-10-03, reviewed never)  \r\n\r\n```\n- [Fake](fake.md): ignored\n```\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	for _, bad := range []string{"- [ ](a.md): S\n", "- [A](a.md): \n", "- [A](a.md) missing colon\n", "* [A](a.md): S\n", "1. [A](a.md): S\n", "- [A](a.md): S\n  continuation\n", "- [A](a.md#part): S\n", "- [A](a.md): S\xff\n"} {
		if _, err := parseIndex(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, inert := range []string{"Prose [A](a.md)\n", "## [A](a.md)\n", "    - [A](a.md): S\n", "> - [A](a.md): S\n", "- outer\n  - [A](a.md): S\n"} {
		doc, err := parseIndex(inert)
		if err != nil || len(doc.Entries) != 0 {
			t.Errorf("inert %q: %#v %v", inert, doc, err)
		}
	}
}

// Whole-row ranges let later consumers cut projections at entry boundaries.
func TestIndexEntryRanges(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		source, want string
		start, end   int
	}{
		{"Intro\n\n- [A](a.md): S\n", "Intro\n\n- [A](a.md): S (created 2026-10-03, updated 2026-10-03, reviewed never)\n", 7, 22},
		{"Intro\r\n\r\n- [A](a.md): S  \r\n", "Intro\r\n\r\n- [A](a.md): S (created 2026-10-03, updated 2026-10-03, reviewed never)  \r\n", 9, 27},
		{"α\n\n- [A](a.md): S", "α\n\n- [A](a.md): S (created 2026-10-03, updated 2026-10-03, reviewed never)", 4, 18},
	} {
		doc, err := parseIndex(tc.source)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Entries) != 1 || doc.Entries[0].Start != tc.start || doc.Entries[0].End != tc.end {
			t.Errorf("ranges %#v, want %d:%d", doc.Entries, tc.start, tc.end)
		}
		if got := renderIndex(doc, map[string]PageMeta{"a": {Created: clock, Updated: clock}}); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}
