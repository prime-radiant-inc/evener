package appwire

import "unicode"

// The bounds a session row's why text travels under (S1). The daemon cuts
// each value with Excerpt before it leaves the session, and the hub's
// navigation projection and schema hold rows to the same numbers.
const (
	// MaxQuestionTextRunes bounds the text of a session's first pending
	// question, a Needs you row's why line ("Question · keep or drop the
	// implied options?"): two lines on a phone, with room left for the
	// long-press preview.
	MaxQuestionTextRunes = 200
	// MaxQuestionOptionRunes bounds one option label. An ask_user label is a
	// choice, not a sentence.
	MaxQuestionOptionRunes = 80
	// MaxQuestionOptions is ask_user's own ceiling on options per question.
	MaxQuestionOptions = 5
	// MaxFailureTitleRunes bounds a failure's headline, a Failed row's why
	// line ("Provider error", "Usage limit reached").
	MaxFailureTitleRunes = 80
	// MaxMessageExcerptRunes bounds the opening of a session's last agent
	// message, a Finished row's why line (spec 18, S1: "about 200 characters").
	MaxMessageExcerptRunes = 200
	// MaxIntentRunes bounds what a tool call said it was doing, a Working
	// row's why line ("Reading the board's row tests."). The agent already
	// words each call's intent as one sentence, so this only holds a runaway
	// one to a row's length.
	MaxIntentRunes = 200
)

// BoundedPendingQuestion is a pending question cut to the wire's bounds: its
// text as one line of at most MaxQuestionTextRunes, and at most
// MaxQuestionOptions of its labels, each one line of at most
// MaxQuestionOptionRunes, leaving out a label with no text. The daemon applies
// it before a question leaves the session, and the hub again before a row
// carries one, so a remote host or an older daemon cannot widen a row.
func BoundedPendingQuestion(text string, labels []string, count int) PendingQuestion {
	question := PendingQuestion{Question: Excerpt(text, MaxQuestionTextRunes), Count: count}
	for _, label := range labels {
		if len(question.Options) == MaxQuestionOptions {
			break
		}
		if label = Excerpt(label, MaxQuestionOptionRunes); label != "" {
			question.Options = append(question.Options, label)
		}
	}
	return question
}

// Excerpt is text as one short line, the form every row why text takes on the
// wire (S1). Each run of whitespace or control characters, line breaks
// included, becomes one space; the ends are trimmed; invalid UTF-8 becomes
// U+FFFD. Text longer than maxRunes is cut to at most maxRunes runes ending in
// "…", at the last word break in the kept text's second half when there is
// one. It reads only as much of text as the excerpt needs, so a long message
// costs no more than a short one.
func Excerpt(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	line := make([]rune, 0, min(len(text), maxRunes+1))
	space := false
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = len(line) > 0
			continue
		}
		if space {
			line = append(line, ' ')
			space = false
		}
		line = append(line, r)
		if len(line) > maxRunes {
			return cutExcerpt(line, maxRunes)
		}
	}
	return string(line)
}

// cutExcerpt ends a line that runs past maxRunes: it keeps maxRunes-1 runes
// and an ellipsis. When the rune after the kept text is not a space, the kept
// text ends mid-word, so it backs up to the last word break in its second
// half; a single word longer than that is cut where it stands.
func cutExcerpt(line []rune, maxRunes int) string {
	keep := line[:maxRunes-1]
	if line[maxRunes-1] != ' ' {
		for i := len(keep) - 1; i >= len(keep)/2; i-- {
			if keep[i] == ' ' {
				keep = keep[:i]
				break
			}
		}
	}
	return string(keep) + "…"
}
