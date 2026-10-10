package searchresult

import "testing"

func TestEntriesEndAtTheFirstBlankLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, output, entries, notes string
		count                        int
	}{
		{name: "no result", output: "", count: 0},
		{name: "entries only", output: "a.go:1:x\nb.go:2:y", entries: "a.go:1:x\nb.go:2:y", count: 2},
		{name: "a trailing newline", output: "a.go\n", entries: "a.go\n", count: 1},
		{name: "a context separator is an entry", output: "1:x\n--\n4:y", entries: "1:x\n--\n4:y", count: 3},
		{
			name:    "entries, then a note",
			output:  WithNotes("1:x\n2:y", "[results truncated at 2; narrow the path or glob_filter, or raise max_results]"),
			entries: "1:x\n2:y", notes: "[results truncated at 2; narrow the path or glob_filter, or raise max_results]", count: 2,
		},
		{
			name:   "a note alone",
			output: WithNotes("", "0 matches; 3 dotfile/gitignored path(s) were excluded from the search"),
			notes:  "0 matches; 3 dotfile/gitignored path(s) were excluded from the search", count: 0,
		},
		{name: "two notes", output: WithNotes("a", "one", "two"), entries: "a", notes: "one\ntwo", count: 1},
		{name: "a note bracketed like an entry", output: "[id].tsx:3:x = [1]", entries: "[id].tsx:3:x = [1]", count: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries, notes := Split(tc.output)
			if entries != tc.entries || notes != tc.notes {
				t.Errorf("Split(%q) = %q, %q; want %q, %q", tc.output, entries, notes, tc.entries, tc.notes)
			}
			if got := EntryCount(tc.output); got != tc.count {
				t.Errorf("EntryCount(%q) = %d, want %d", tc.output, got, tc.count)
			}
		})
	}
}

func TestWithNotesPutsABlankLineBeforeTheNotes(t *testing.T) {
	t.Parallel()
	if got, want := WithNotes("a\nb", "n1", "n2"), "a\nb\n\nn1\nn2"; got != want {
		t.Errorf("WithNotes = %q, want %q", got, want)
	}
	if got, want := WithNotes("", "n"), "\n\nn"; got != want {
		t.Errorf("WithNotes with no entries = %q, want %q", got, want)
	}
}
