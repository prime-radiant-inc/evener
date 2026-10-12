package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestRankTokens(t *testing.T) {
	got := strings.Join(rankTokens("Before changing Total's signature, and the Euro-format 12,05"), " ")
	if want := "before changing total s signature euro format 12 05"; got != want {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
}

func rankPage(slug, when string) memoryPage {
	return memoryPage{Path: slug + ".md", Title: slug, Description: "desc of " + slug, When: when}
}

func TestBM25PrefersRarerAndShorterMatches(t *testing.T) {
	docs := [][]string{
		rankTokens("formatting a receipt amount"),
		rankTokens("formatting a log line"),
		rankTokens("unrelated deploy step"),
	}
	scores := bm25Scores(docs, rankTokens("receipt formatting"))
	if !(scores[0] > scores[1] && scores[1] > 0 && scores[2] == 0) {
		t.Fatalf("scores = %v, want receipt page > formatting-only page > 0 = no match", scores)
	}
}

func TestRankedMemoryLines(t *testing.T) {
	t.Setenv(memoryWhenEnv, "only")
	pages := []memoryPage{
		rankPage("euro", "When formatting an amount for a receipt"),
		rankPage("tag", "When editing a doc comment on an exported function"),
		rankPage("shown", "When formatting a receipt total"),
	}
	shown := []string{memoryIndexLine(pages[2])}
	lines := rankedMemoryLines(pages, shown, "Add a FormatSavings receipt line")
	if len(lines) != 1 || !strings.Contains(lines[0], "When formatting an amount for a receipt") || !strings.HasPrefix(lines[0], "- [euro](euro.md)") {
		t.Fatalf("lines = %q, want only the euro page in the current mode (the shown page and non-matching tag page excluded)", lines)
	}
	if got := rankedMemoryLines(pages, shown, "zebra giraffe"); got != nil {
		t.Fatalf("no match listed %q", got)
	}
}

func TestRankedMemoryLinesCapsAtFive(t *testing.T) {
	var pages []memoryPage
	for i := range 9 {
		pages = append(pages, rankPage(fmt.Sprintf("p%d", i), "When writing a commit message"))
	}
	if got := rankedMemoryLines(pages, nil, "commit message"); len(got) != 5 {
		t.Fatalf("got %d lines, want 5", len(got))
	}
}
