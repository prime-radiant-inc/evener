package main

import (
	"regexp"
	"strings"
	"unicode"
)

// proseCounts tallies the writing tics and opaque identifiers in agent
// prose. The prose rewrite is judged by these counts per 1,000 words.
type proseCounts struct {
	Words       int `json:"words"`
	EmDashes    int `json:"em_dashes"`
	Contrastive int `json:"contrastive"`
	BoldLabels  int `json:"bold_labels"`
	Headers     int `json:"headers"`
	Arrows      int `json:"arrows"`
	Shouting    int `json:"shouting"`
	OpaqueIDs   int `json:"opaque_ids"`
}

func (c *proseCounts) add(o proseCounts) {
	c.Words += o.Words
	c.EmDashes += o.EmDashes
	c.Contrastive += o.Contrastive
	c.BoldLabels += o.BoldLabels
	c.Headers += o.Headers
	c.Arrows += o.Arrows
	c.Shouting += o.Shouting
	c.OpaqueIDs += o.OpaqueIDs
}

var (
	fencedCodeRe = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe = regexp.MustCompile("`[^`\n]*`")
	wordRe       = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}'’_-]*`)
	emDashRe     = regexp.MustCompile(`—| – | -- `)
	// The three shapes of the "X, not Y" tic: "a guide, not a rule",
	// "not just X but Y", and "it isn't X, it's Y".
	contrastiveRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i),\s+not\s+(?:a |an |the |just |only |merely )?[\p{L}\p{N}]`),
		regexp.MustCompile(`(?i)\bnot\s+(?:just|only|merely)\b[^.;:!?\n]{0,60}?\bbut\b`),
		regexp.MustCompile(`(?i)\b(?:isn't|is not|aren't|are not|wasn't|was not)\b[^.;:!?\n]{0,60}?[,;—]\s*(?:it's|it is|they're|they are|that's|this is)\b`),
	}
	boldLabelRe = regexp.MustCompile(`(?m)^\s*(?:(?:[-*+]|\d+[.)])\s+)?\*\*[^*\n]+(?::\*\*|\*\*\s*[:—–-])`)
	headerRe    = regexp.MustCompile(`(?m)^#{1,6}\s`)
	arrowRe     = regexp.MustCompile(`→|⇒|->|=>`)
	shoutingRe  = regexp.MustCompile(`\b(?:NEVER|ALWAYS|MUST|CRITICAL|IMPORTANT|NOT|ONLY)\b`)
	opaqueIDRes = []*regexp.Regexp{
		regexp.MustCompile(`#\d+\b`),                                                                    // issue and pull request numbers
		regexp.MustCompile(`\b(?:Task|Step|Phase|Item|Finding)\s+\d+\b`),                                // numbered work items
		regexp.MustCompile(`\b[A-Z]{1,2}\d{1,3}\b`),                                                     // short codes such as T3 or D23
		regexp.MustCompile(`\b(?:job_[0-9A-Za-z]{22}_[0-9A-Za-z]{12}|(?:dlg|watch)_[0-9A-Za-z]{22})\b`), // evener job, delegate, and watch ids
	}
	hexRe    = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	base62Re = regexp.MustCompile(`\b[0-9A-Za-z]{22}\b`)
)

// countProse counts one piece of agent prose. Tics and words are counted
// outside code, since code spans hold commands and output. Identifiers are
// counted in inline code too, because an id in backticks is just as opaque
// to the reader; only fenced blocks are skipped.
func countProse(text string) proseCounts {
	noFences := fencedCodeRe.ReplaceAllString(text, " ")
	prose := inlineCodeRe.ReplaceAllString(noFences, " ")
	c := proseCounts{
		Words:      len(wordRe.FindAllString(prose, -1)),
		EmDashes:   len(emDashRe.FindAllString(prose, -1)),
		BoldLabels: len(boldLabelRe.FindAllString(prose, -1)),
		Headers:    len(headerRe.FindAllString(prose, -1)),
		Arrows:     len(arrowRe.FindAllString(prose, -1)),
		Shouting:   len(shoutingRe.FindAllString(prose, -1)),
	}
	for _, re := range contrastiveRes {
		c.Contrastive += len(re.FindAllString(prose, -1))
	}
	for _, re := range opaqueIDRes {
		c.OpaqueIDs += len(re.FindAllString(noFences, -1))
	}
	// A commit hash mixes digits and letters; a word or a number alone does not.
	for _, m := range hexRe.FindAllString(noFences, -1) {
		if strings.ContainsAny(m, "0123456789") && strings.ContainsAny(m, "abcdef") {
			c.OpaqueIDs++
		}
	}
	for _, m := range base62Re.FindAllString(noFences, -1) {
		if hasDigitUpperLower(m) {
			c.OpaqueIDs++
		}
	}
	return c
}

// hasDigitUpperLower reports whether s mixes digits with upper- and lowercase
// letters, the shape of a session id.
func hasDigitUpperLower(s string) bool {
	var digit, upper, lower bool
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		}
	}
	return digit && upper && lower
}
