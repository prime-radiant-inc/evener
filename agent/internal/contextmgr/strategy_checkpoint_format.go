package contextmgr

import (
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const (
	predictiveCheckpointStart = "[CONTEXT CHECKPOINT - PREDICTIVE]\n"
	sessionLogCheckpointStart = "[CONTEXT CHECKPOINT - SESSION LOG]\n"
)

func strategyCheckpointOpening(text string) (opening, heading string) {
	switch {
	case strings.HasPrefix(text, predictiveCheckpointStart):
		return predictiveCheckpointStart, "## Predicted Context"
	case strings.HasPrefix(text, sessionLogCheckpointStart):
		return sessionLogCheckpointStart, "## Session Log"
	default:
		return "", ""
	}
}

// sessionLogEarlierContext keeps independent earlier context, but not a log
// snapshot whose complete body is already present in the current cumulative
// log. A shorter or different log cannot supersede the old snapshot.
func sessionLogEarlierContext(earlier []string, current string) []string {
	var kept []string
	for _, summary := range earlier {
		if strings.HasPrefix(summary, sessionLogCheckpointStart) {
			previous, ok := parseStrategyCheckpoint(summary)
			if ok && len(previous.conversation) == 0 && len(previous.earlier) == 0 && strings.Contains(current, previous.modelText) {
				continue
			}
		}
		kept = append(kept, summary)
	}
	return kept
}

func renderStrategyCheckpoint(opening, heading, modelText, carry string, earlier []string, meta *CompactionMeta) string {
	// Earlier-summary extraction, like conversation extraction, trims only
	// surrounding whitespace. Render that canonical form on the first fold too.
	canonicalEarlier := make([]string, 0, len(earlier))
	for _, summary := range earlier {
		canonicalEarlier = append(canonicalEarlier, strings.TrimSpace(summary))
	}
	return opening + meta.compactionPointer("checkpoint") + renderCheckpointEarlierSummaries(canonicalEarlier) +
		renderCompactionModelText(heading, modelText) + carry + "[END CHECKPOINT]"
}

// carriesStrategyPartnerMessages recognizes only the layout we render. Legacy
// raw logs may quote checkpoint examples: their headings cannot establish who
// said something. Fencing the entire free-text body protects the new format.
func carriesStrategyPartnerMessages(text string) bool {
	_, ok := parseStrategyCheckpoint(text)
	return ok
}

type parsedStrategyCheckpoint struct {
	modelText    string
	earlier      []string
	conversation []checkpointConversationEntry
}

func parseStrategyCheckpoint(text string) (parsedStrategyCheckpoint, bool) {
	opening, heading := strategyCheckpointOpening(text)
	if opening == "" {
		return parsedStrategyCheckpoint{}, false
	}
	start, body, end, ok := markdownSectionSpan(text, heading)
	if !ok {
		return parsedStrategyCheckpoint{}, false
	}
	modelText, ok := soleFencedText(text[body:end])
	if !ok {
		return parsedStrategyCheckpoint{}, false
	}
	prefix := strings.TrimPrefix(text[:start], opening)
	if line, after, found := strings.Cut(prefix, "\n"); found && strings.HasPrefix(line, "Earlier history was compacted into this checkpoint ") {
		prefix = after
	}
	earlier := extractCheckpointEarlierSummaries(text)
	if prefix != renderCheckpointEarlierSummaries(earlier) {
		return parsedStrategyCheckpoint{}, false
	}
	entries := extractMarkdownConversation(text)
	for _, entry := range entries {
		if entry.Role != "user" {
			return parsedStrategyCheckpoint{}, false
		}
	}
	if text[start:] != renderCompactionModelText(heading, modelText+"\n")+renderCheckpointConversation(entries)+"[END CHECKPOINT]" {
		return parsedStrategyCheckpoint{}, false
	}
	return parsedStrategyCheckpoint{modelText: modelText, earlier: earlier, conversation: entries}, true
}

// strategyCheckpointSource keeps ambiguous older checkpoints as model context.
// A matching section alone cannot prove that a model's format example was a
// human message. Leave default checkpoint parsing and budgets unchanged.
func strategyCheckpointSource(history []schema.Turn) []schema.Turn {
	source := append([]schema.Turn(nil), history...)
	for i := range source {
		turn := &source[i]
		if turn.Kind != schema.TurnUserInput && turn.Kind != schema.TurnCheckpoint {
			continue
		}
		text := turn.Message.Text()
		if turn.Kind == schema.TurnUserInput && isStrategyCompactionText(text) {
			turn.Kind = schema.TurnCheckpoint
			if strings.HasPrefix(text, summaryMarkerStart) {
				turn.Kind = schema.TurnSummary
			}
		}
		if turn.Kind != schema.TurnCheckpoint {
			continue
		}
		if opening, _ := strategyCheckpointOpening(text); opening != "" {
			continue // the collector checks this format's provenance itself
		}
		if !isRenderedDeterministicCheckpoint(text) {
			turn.Kind = schema.TurnSummary // opaque model context, never user carry
		}
	}
	return source
}

func collectStrategyCheckpointData(history []schema.Turn, cutoff int, resultToolName string) checkpointData {
	source := strategyCheckpointSource(history[:cutoff])
	return collectCheckpointData(source, len(source), resultToolName)
}

// isRenderedDeterministicCheckpoint accepts the renderer's fenced sections
// beside only its known metadata lines. Unknown surrounding prose makes the
// entire record opaque, including legacy examples of those sections.
func isRenderedDeterministicCheckpoint(text string) bool {
	body, ok := strings.CutPrefix(text, "[CONTEXT CHECKPOINT]\n")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(strings.TrimSuffix(body, "\n"), "[END CHECKPOINT]")
	if !ok {
		return false
	}
	earlier := renderCheckpointEarlierSummaries(extractCheckpointEarlierSummaries(text))
	conversation := renderCheckpointConversation(extractMarkdownConversation(text))
	notes := renderCheckpointWorkingNotes(extractCheckpointWorkingNotes(text))
	if earlier == "" && conversation == "" && notes == "" {
		return false
	}
	if body, ok = strings.CutPrefix(body, earlier); !ok {
		return false
	}
	for _, section := range []string{notes, conversation} {
		if section != "" {
			if body, ok = strings.CutSuffix(body, "\n"+section); !ok {
				return false
			}
		}
	}
	if body == "" {
		return true
	}
	// formatCheckpoint emits each optional field once, in this order, with no
	// blank lines. Indented lines belong only to the shell-results field, and
	// only when they are actual quoted command/exit records.
	metadata, ok := strings.CutSuffix(body, "\n")
	if !ok {
		return false
	}
	prefixes := []string{"Earlier history was compacted into this checkpoint ", "Files modified: ", "Files attempted but not applied: ", "Actions taken: ", "Last shell results:", "Activated skills: "}
	const shellField = 4
	field, shellResults := -1, 0
	for line := range strings.SplitSeq(metadata, "\n") {
		if field == shellField && isCheckpointShellResult(line) {
			shellResults++
			if shellResults > 3 {
				return false
			}
			continue
		}
		next := -1
		for i, prefix := range prefixes {
			if strings.HasPrefix(line, prefix) {
				next = i
				break
			}
		}
		if next <= field || (field == shellField && shellResults == 0) {
			return false
		}
		value := strings.TrimPrefix(line, prefixes[next])
		switch next {
		case 0:
			if !isCheckpointPointer(line) {
				return false
			}
		case 3:
			if !isCheckpointActionCounts(value) {
				return false
			}
		case shellField:
			if value != "" {
				return false
			}
		default:
			if strings.TrimSpace(value) == "" {
				return false
			}
		}
		field = next
	}
	return field != shellField || shellResults > 0
}

func isCheckpointShellResult(line string) bool {
	record, ok := strings.CutPrefix(line, "  ")
	if !ok || !strings.HasPrefix(record, "\"") {
		return false
	}
	quoted, err := strconv.QuotedPrefix(record)
	if err != nil {
		return false
	}
	command, err := strconv.Unquote(quoted)
	if err != nil || strconv.Quote(command) != quoted {
		return false
	}
	exit, ok := strings.CutPrefix(record[len(quoted):], " → exit ")
	if !ok || exit == "" {
		return false
	}
	if exit == "?" {
		return true
	}
	for _, c := range exit {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isCheckpointActionCounts(value string) bool {
	totalText, counts, ok := strings.Cut(value, " tool calls (")
	if !ok {
		return false
	}
	remaining, err := strconv.Atoi(totalText)
	if err != nil || remaining <= 0 || strconv.Itoa(remaining) != totalText {
		return false
	}
	counts, ok = strings.CutSuffix(counts, ")")
	if !ok {
		return false
	}
	previous := ""
	for count := range strings.SplitSeq(counts, ", ") {
		number, name, ok := strings.Cut(count, " ")
		n, err := strconv.Atoi(number)
		if !ok || err != nil || n < 0 || n > remaining || strconv.Itoa(n) != number || name <= previous {
			return false
		}
		remaining -= n
		previous = name
	}
	return remaining == 0
}

func isCheckpointPointer(line string) bool {
	_, rest, ok := strings.Cut(line, " This session's id is ")
	if !ok {
		return false
	}
	sessionID, _, ok := strings.Cut(rest, ".")
	if !ok || sessionID == "" {
		return false
	}
	for _, tools := range [][]string{nil, {"read_transcript"}, {"find_session_transcripts"}, {"read_transcript", "find_session_transcripts"}} {
		meta := &CompactionMeta{SessionID: sessionID, AvailableTranscriptTools: tools}
		if line+"\n" == meta.compactionPointer("checkpoint") {
			return true
		}
	}
	return false
}

// strategyCheckpointFold keeps earlier context whole on predictor failure.
// Ordinary checkpoint detail and partner-message fitting retain their budgets.
func strategyCheckpointFold(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) ([]schema.Turn, []schema.Turn) {
	if attentionTransparentTurnCount(history) <= preserveRecent {
		return history, nil
	}
	cutoff := foldCutoff(history, preserveRecent)
	if cutoff < 0 {
		return history, nil
	}
	data := collectStrategyCheckpointData(history, cutoff, resultToolName)
	earlier := data.earlierSummaries
	data.earlierSummaries = nil
	text := formatCheckpoint(data, meta, maxCheckpointChars)
	text = strings.Replace(text, "[CONTEXT CHECKPOINT]\n", "[CONTEXT CHECKPOINT]\n"+renderCheckpointEarlierSummaries(earlier), 1)
	result := make([]schema.Turn, 0, 1+len(history)-cutoff)
	result = append(result, schema.NewTurn(schema.TurnCheckpoint, llm.User(text)))
	result = append(result, history[cutoff:]...)
	return result, history[:cutoff:cutoff]
}
