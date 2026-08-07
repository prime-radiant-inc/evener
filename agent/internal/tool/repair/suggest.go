package repair

import (
	"fmt"
	"sort"
	"strings"
)

// maxAvailableListed caps the available-tools list so an unknown-tool error
// never floods the model's context.
const maxAvailableListed = 30

// SuggestToolName returns the closest name in available to requested within an
// edit-distance threshold of min(2, ceil(len(requested)/3)), or "" if none.
func SuggestToolName(requested string, available []string) string {
	top := TopMatches(requested, available, 1)
	if len(top) == 0 {
		return ""
	}
	return top[0]
}

// matchThreshold returns the edit-distance threshold used to decide whether a
// candidate is "close enough" to requested: min(2, ceil(len(requested)/3)).
func matchThreshold(requested string) int {
	threshold := (len(requested) + 2) / 3 // ceil(len/3)
	return max(1, min(2, threshold))
}

// TopMatches returns up to n candidates closest to requested by edit distance,
// within the same threshold SuggestToolName uses, sorted by distance and then
// lexically to break ties. Returns nil if no candidate is within threshold.
func TopMatches(requested string, candidates []string, n int) []string {
	threshold := matchThreshold(requested)
	type scored struct {
		name string
		dist int
	}
	var matches []scored
	for _, c := range candidates {
		if d := levenshtein(requested, c); d <= threshold {
			matches = append(matches, scored{c, d})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].dist != matches[j].dist {
			return matches[i].dist < matches[j].dist
		}
		return matches[i].name < matches[j].name
	})
	if len(matches) > n {
		matches = matches[:n]
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.name
	}
	return out
}

// UnknownToolMessage renders the model-facing error for an unknown tool name.
// requested and available must already be provider-visible names.
func UnknownToolMessage(requested string, available []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown tool: %q.", requested)
	if s := SuggestToolName(requested, available); s != "" {
		fmt.Fprintf(&b, " Did you mean %q?", s)
	}
	listed := available
	if len(listed) > maxAvailableListed {
		listed = listed[:maxAvailableListed]
	}
	fmt.Fprintf(&b, "\nAvailable tools: %s", strings.Join(listed, ", "))
	return b.String()
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
