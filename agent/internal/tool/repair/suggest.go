package repair

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/internal/tool/wirenames"
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

// UnknownToolKind classifies why a requested tool name is not callable.
type UnknownToolKind string

const (
	// UnknownToolKindMissing: no tool by that name is registered.
	UnknownToolKindMissing UnknownToolKind = "unknown_tool"
	// UnknownToolKindSystemMessage: the name is a tag Evener injects into
	// messages, not a tool.
	UnknownToolKindSystemMessage UnknownToolKind = "system_message"
)

// toolNameAliases maps names other harnesses use to the Evener tools that do
// the same job, in suggestion order. Edit distance cannot bridge these.
var toolNameAliases = map[string][]string{
	"exec_command":       {"shell"},
	"bash":               {"shell"},
	"run_shell_command":  {"shell"},
	"execute_command":    {"shell"},
	"str_replace_editor": {"edit_file", "apply_patch"},
	"edit":               {"edit_file", "apply_patch"},
}

// systemMessageTags are names of tags Evener injects into messages that
// models try to call as tools.
var systemMessageTags = map[string]bool{
	"job-notification": true,
	"job_notification": true,
}

// UnknownToolDiagnosis says what an unknown tool name is and which registered
// tools the model probably meant.
type UnknownToolDiagnosis struct {
	Kind        UnknownToolKind
	Suggestions []string
}

// DiagnoseUnknownTool classifies requested against the registered names in
// available. Aliases are checked before edit distance, and only registered
// targets are suggested.
func DiagnoseUnknownTool(requested string, available []string) UnknownToolDiagnosis {
	key := strings.ToLower(strings.TrimSpace(requested))
	if systemMessageTags[key] {
		return UnknownToolDiagnosis{Kind: UnknownToolKindSystemMessage}
	}
	var suggestions []string
	for _, target := range toolNameAliases[key] {
		// The name the model just called is not a suggestion, and only the
		// session's own surface name for the tool is.
		for _, name := range wirenames.NamesOf(target) {
			if name != requested && slices.Contains(available, name) {
				suggestions = append(suggestions, name)
				break
			}
		}
	}
	if len(suggestions) == 0 {
		if s := SuggestToolName(requested, available); s != "" {
			suggestions = []string{s}
		}
	}
	return UnknownToolDiagnosis{Kind: UnknownToolKindMissing, Suggestions: suggestions}
}

// UnknownToolMessage renders the model-facing error for an unknown tool name.
// requested and available must already be provider-visible names.
func UnknownToolMessage(requested string, available []string) string {
	diagnosis := DiagnoseUnknownTool(requested, available)
	var b strings.Builder
	fmt.Fprintf(&b, "unknown tool: %q.", requested)
	if diagnosis.Kind == UnknownToolKindSystemMessage {
		b.WriteString(" That is a system message Evener adds to your conversation, not a tool; read it and do not call it.")
		return b.String()
	}
	if len(diagnosis.Suggestions) > 0 {
		quoted := make([]string, len(diagnosis.Suggestions))
		for i, name := range diagnosis.Suggestions {
			quoted[i] = fmt.Sprintf("%q", name)
		}
		fmt.Fprintf(&b, " Did you mean %s?", strings.Join(quoted, " or "))
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
