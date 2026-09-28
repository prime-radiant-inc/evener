package hub

import (
	"slices"
	"strings"
	"unicode"

	"primeradiant.com/evener/appwire"
)

const (
	// searchSnippetLead is how much of a message a snippet keeps before its
	// first match, so the match reads in context.
	searchSnippetLead = 40
	// searchSnippetRunes bounds a snippet's text, ellipses aside: about two
	// lines on a phone.
	searchSnippetRunes = 160
)

// searchWord is one word of a snippet's line: runes [start, end).
type searchWord struct {
	start, end int
	match      bool
}

// searchSnippet is text as one line around its first word that a search word
// prefixes, letter case aside, cut to searchSnippetRunes at word breaks, with
// every such word marked (S14). Both the message's words and the search
// tokens split on the messages index's own tokenizer boundary (letters and
// digits; everything else, "_" included, a break) so a mark falls exactly
// where the index matched, even when a query token like "settle_race" holds
// an underscore that unicode61 splits into "settle" and "race" in the index.
// With no such word the snippet is the message's opening.
func searchSnippet(text string, tokens []string) []appwire.SearchSnippetPart {
	line := []rune(appwire.Excerpt(text, len(text)))
	words := searchWords(line, tokens)
	first := slices.IndexFunc(words, func(word searchWord) bool { return word.match })
	start := 0
	if first >= 0 && words[first].start > searchSnippetLead {
		// Back up searchSnippetLead runes, then forward to a word's start.
		lead := words[first].start - searchSnippetLead
		start = words[slices.IndexFunc(words, func(word searchWord) bool { return word.start >= lead })].start
	}
	end := min(len(line), start+searchSnippetRunes)
	if end < len(line) {
		// End at the last word break before the cut, but never before the
		// first match's end.
		cut, foundCut := end, false
		for _, word := range slices.Backward(words) {
			if word.end <= cut && (first < 0 || word.end >= words[first].end) {
				end = word.end
				foundCut = true
				break
			}
		}
		if !foundCut && first >= 0 && words[first].end > cut {
			// The first match's own word runs past the ordinary cut (a hash,
			// a URL, a line with no spaces): keep it whole rather than
			// cutting inside it and silently losing its mark below.
			end = words[first].end
		}
	}
	var parts []appwire.SearchSnippetPart
	add := func(text string, match bool) {
		if text == "" {
			return
		}
		if n := len(parts); n > 0 && parts[n-1].Match == match {
			parts[n-1].Text += text
			return
		}
		parts = append(parts, appwire.SearchSnippetPart{Text: text, Match: match})
	}
	if start > 0 {
		add("…", false)
	}
	at := start
	for _, word := range words {
		if !word.match || word.start < start || word.end > end {
			continue
		}
		add(string(line[at:word.start]), false)
		add(string(line[word.start:word.end]), true)
		at = word.end
	}
	add(string(line[at:end]), false)
	if end < len(line) {
		add("…", false)
	}
	if parts == nil {
		// An empty message (Texts returns "" for a hit whose message left the
		// index since Match) must still be a non-nil slice: SearchHit.Snippet
		// has no omitempty, so nil would encode as JSON null.
		parts = []appwire.SearchSnippetPart{}
	}
	return parts
}

// searchWords splits line into its words, marking each one a query word
// prefixes. isWord matches the messages index's unicode61 tokenizer, which
// treats "_" as a separator unlike hubcore.SearchTokens (shared with title
// and prompt search, where the index never splits on it); splitIndexWords
// below re-splits the query tokens the same way so both sides agree.
func searchWords(line []rune, tokens []string) []searchWord {
	var words []searchWord
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	matchWords := splitIndexWords(tokens)
	for i := 0; i < len(line); {
		if !isWord(line[i]) {
			i++
			continue
		}
		start := i
		for i < len(line) && isWord(line[i]) {
			i++
		}
		lower := strings.ToLower(string(line[start:i]))
		match := false
		for _, token := range matchWords {
			if strings.HasPrefix(lower, token) {
				match = true
				break
			}
		}
		words = append(words, searchWord{start: start, end: i, match: match})
	}
	return words
}

// splitIndexWords re-splits each search token on everything the messages
// index's unicode61 tokenizer treats as a separator (letters and digits are
// word characters, "_" included is not), the same way FTS5 re-tokenizes a
// bareword query term against the index: "settle_race" becomes "settle" and
// "race", matched independently rather than as one joined word.
func splitIndexWords(tokens []string) []string {
	var words []string
	for _, token := range tokens {
		words = append(words, strings.FieldsFunc(token, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})...)
	}
	return words
}
