package agent

import (
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"primeradiant.com/evener/agent/schema"
)

// Spike: EVENER_MEMORY_RANK=on adds, at each human-message boundary, the pages
// whose when line (else description) best matches the message and that the
// projected index does not show.
const (
	memoryRankEnv = "EVENER_MEMORY_RANK"
	// memoryRankLimit is the most pages one boundary lists.
	memoryRankLimit = 5
	// BM25 parameters.
	bm25K1 = 1.2
	bm25B  = 0.75
)

func memoryRankEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(memoryRankEnv)), "on")
}

var rankStopwords = func() map[string]bool {
	words := strings.Fields("a an and are as at be by for from has have i in is it its of on or that the this to was we with you your do does not no so if then than into can will should would add make")
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}()

// rankTokens lowercases text, splits it on non-alphanumerics and drops stopwords.
func rankTokens(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var tokens []string
	for _, f := range fields {
		if !rankStopwords[f] {
			tokens = append(tokens, f)
		}
	}
	return tokens
}

// memoryRankText is the text a page is matched on: its when line, else its description.
func memoryRankText(p memoryPage) string {
	if p.When != "" {
		return p.When
	}
	return p.Description
}

// bm25Scores scores each document against query. A document sharing no query
// term scores 0.
func bm25Scores(docs [][]string, query []string) []float64 {
	scores := make([]float64, len(docs))
	if len(docs) == 0 || len(query) == 0 {
		return scores
	}
	total := 0
	docFreq := map[string]int{}
	for _, doc := range docs {
		total += len(doc)
		seen := map[string]bool{}
		for _, t := range doc {
			if !seen[t] {
				seen[t] = true
				docFreq[t]++
			}
		}
	}
	avg := float64(total) / float64(len(docs))
	queryTerms := map[string]bool{}
	for _, t := range query {
		queryTerms[t] = true
	}
	for i, doc := range docs {
		tf := map[string]int{}
		for _, t := range doc {
			tf[t]++
		}
		for term := range queryTerms {
			f := float64(tf[term])
			if f == 0 {
				continue
			}
			n := float64(docFreq[term])
			idf := math.Log(1 + (float64(len(docs))-n+0.5)/(n+0.5))
			scores[i] += idf * f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*float64(len(doc))/avg))
		}
	}
	return scores
}

// rankedMemoryLines is the index lines (in the current rendering mode) of the
// up to memoryRankLimit best-matching pages for message that are not already
// among shownLines, best first, ties by path. It is nil when nothing matches.
func rankedMemoryLines(pages []memoryPage, shownLines []string, message string) []string {
	docs := make([][]string, len(pages))
	for i, p := range pages {
		docs[i] = rankTokens(memoryRankText(p))
	}
	scores := bm25Scores(docs, rankTokens(message))
	order := make([]int, 0, len(pages))
	for i := range pages {
		if scores[i] > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		if scores[order[a]] != scores[order[b]] {
			return scores[order[a]] > scores[order[b]]
		}
		return pages[order[a]].Path < pages[order[b]].Path
	})
	var lines []string
	for _, i := range order {
		if len(lines) == memoryRankLimit {
			break
		}
		if memoryPageShown(pages[i], shownLines) {
			continue
		}
		lines = append(lines, memoryIndexLine(pages[i]))
	}
	return lines
}

// memoryPageShown reports whether one of lines is p's index line.
func memoryPageShown(p memoryPage, lines []string) bool {
	for _, line := range lines {
		if memoryIndexLineFor(line, p.Path) {
			return true
		}
	}
	return false
}

// latestUserInputText is the text of the most recent human message in the history.
func (s *Session) latestUserInputText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range slices.Backward(s.history) {
		if turn.Kind == schema.TurnUserInput {
			return turn.Message.Text()
		}
	}
	return ""
}

// rankedMemorySection lists, for the boundary's message, the scope's pages
// that may bear on it and are not in the projected index shown.
func rankedMemorySection(scope string, pages []memoryPage, shown, message string) memoryContextSection {
	return func() string {
		lines := rankedMemoryLines(pages, memoryIndexLines(shown), message)
		if len(lines) == 0 {
			return ""
		}
		var b strings.Builder
		b.WriteString(strings.ToUpper(scope[:1]) + scope[1:] + " memory pages that may be relevant to this message (not in the index above):")
		for _, line := range lines {
			b.WriteString("\n- " + strconv.Quote(line))
		}
		return b.String()
	}
}
