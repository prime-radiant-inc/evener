// Package searchresult is the shape of a search tool's result (grep, glob,
// memory_search): its entries, one per line, then any notes about the result
// (that the cap cut it short, that paths were left out) after a blank line.
// No entry is ever a blank line, so a reader counts a result's entries as its
// lines before the first one. The clients count them the same way
// (appwire-client/typescript/stepWords.ts searchResultCount), and the
// recorded tool calls in agent/testdata/toolwire pin both readers to the
// tools' real output.
package searchresult

import "strings"

// WithNotes is a result listing entries, then notes after a blank line. With
// no entries the result starts with that blank line, so a note never reads as
// an entry.
func WithNotes(entries string, notes ...string) string {
	return entries + "\n\n" + strings.Join(notes, "\n")
}

// Split cuts a result at its first blank line into its entries and the notes
// after it.
func Split(output string) (entries, notes string) {
	entries, notes, _ = strings.Cut(output, "\n\n")
	return entries, notes
}

// EntryCount is how many entries a result lists.
func EntryCount(output string) int {
	entries, _ := Split(output)
	entries = strings.TrimSuffix(entries, "\n")
	if entries == "" {
		return 0
	}
	return strings.Count(entries, "\n") + 1
}
