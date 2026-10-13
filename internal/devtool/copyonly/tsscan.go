package copyonly

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The TypeScript scanner reads one line at a time, as inventory.py does, so it
// shares that tool's limits: a multi-line template literal or block comment
// can blur a test's boundaries, an assertion split across lines is classified
// by its first line, and assertion forms it doesn't recognise (expect.soft,
// expect.poll) are not checks.

const (
	tsString = "(?:\"((?:[^\"\\\\]|\\\\.)*)\"|'((?:[^'\\\\]|\\\\.)*)'|`((?:[^`\\\\$]|\\\\.)*)`)"
	tsRegex  = `/((?:[^/\\\n]|\\.)+)/[a-z]*`
)

var (
	// tsByText and tsMatcher find a copy oracle: text located by its wording,
	// or a matcher against a literal, a regex or an identifier. Groups 1-3 are
	// the string literal, 4 the regex, 5 the identifier.
	tsByText  = regexp.MustCompile(`\b(?:get|find|query)(?:All)?By(?:Text|Title)\(\s*(?:` + tsString + `|` + tsRegex + `|([A-Za-z_$][\w$.]*)\s*[,)])`)
	tsMatcher = regexp.MustCompile(`\.(?:toContain|toBe|toEqual|toMatch|toHaveTextContent|toStrictEqual)\(\s*(?:` + tsString + `|` + tsRegex + `|([A-Za-z_$][\w$.]*)\s*\))`)
	tsImport  = regexp.MustCompile(`import\s*(?:type\s*)?\{([^}]*)\}\s*from\s*['"]([^'"]+)['"]`)
	// copyIdent is an identifier named like copy (*_MESSAGE, *Label, ...).
	copyIdent = regexp.MustCompile(`(MESSAGE|LABEL|COPY|TEXT|TITLE|HINT|ERROR|PROMPT|USAGE|PLACEHOLDER|Message|Label|Copy|Text|Title|Hint|Error|Prompt|Usage|Placeholder)S?$`)

	tsTestStart = regexp.MustCompile(`^\s*(?:test|it)(?:\.(?:only|skip|concurrent|each\(.*?\)))?\s*\(\s*` + tsString)
	tsCheck     = regexp.MustCompile(`\bexpect\(|\b(?:get|find)(?:All)?By\w+\(`)
	// tsBehaviorOnLine is a check about something other than the wording,
	// even when a copy hit sits on the same line: an expected error, a count
	// or comparison, or text used only to find an element whose attribute or
	// state is then checked.
	tsBehaviorOnLine = regexp.MustCompile(`\berr\s*==\s*nil|getAttribute\(|toHaveAttribute|\.dataset\b|className|\.checked\b|\.disabled\b` +
		`|toHaveLength|toHaveBeenCalled|toBeGreaterThan|toBeLessThan|\blen\(|[=!<>]=?\s*-?\d+\b`)
	// tsNotInput is a line that carries no input to the code under test: a
	// comment or a failure diagnostic.
	tsNotInput = regexp.MustCompile(`^\s*(?://|/?\*)|\bt\.(?:Fatal|Error|Log|Skip)f?\(`)
	tsQuoted   = regexp.MustCompile("\"((?:[^\"\\\\]|\\\\.){3,})\"|'((?:[^'\\\\]|\\\\.){3,})'|`([^`]{3,})`")
)

// tsRegexPreceders are the characters after which a `/` starts a regex
// literal, not a division. `<` and `>` are left out: after them a `/` is JSX
// (`</div>`, or a child's text like `<span>/tmp</span>`), and a regex there is
// vanishingly rare.
const tsRegexPreceders = "(,=:[!&|?{};+-*%~^"

// tsExpressionKeywords take an expression, so a quote after one starts a
// string, not JSX text.
var tsExpressionKeywords = map[string]bool{
	"return": true, "case": true, "typeof": true, "in": true, "of": true, "await": true, "yield": true,
	"else": true, "throw": true, "void": true, "delete": true, "new": true, "instanceof": true, "do": true,
	// type positions: `value as "x"`, `satisfies "x"`, `extends "x"`
	"as": true, "satisfies": true, "extends": true, "keyof": true, "is": true,
}

// firstGroup returns the first of the groups lo..hi-1 that took part in the
// match m (a FindStringSubmatchIndex result), and whether one did. A group
// that matched the empty string took part.
func firstGroup(s string, m []int, lo, hi int) (string, bool) {
	for g := lo; g < hi; g++ {
		if m[2*g] >= 0 {
			return s[m[2*g]:m[2*g+1]], true
		}
	}
	return "", false
}

func (s *scanner) scanTS(path, text string) []Target {
	imported := tsCopyImports(text)
	var targets []Target
	for _, b := range tsTestBlocks(text) {
		if !copyPurposeName.MatchString(b.name) {
			continue
		}
		isCheck := make([]bool, len(b.lines))
		for i, line := range b.lines {
			isCheck[i] = tsCheck.MatchString(line)
		}
		if !slices.Contains(isCheck, true) {
			continue
		}
		var inputs []string
		for i, line := range b.lines[1:] {
			if !isCheck[i+1] && !tsNotInput.MatchString(line) {
				inputs = append(inputs, line)
			}
		}
		supplied := strings.Join(inputs, "\n")
		allCopy := true
		for i, line := range b.lines {
			if isCheck[i] && !s.tsCopyClaim(line, supplied, imported) {
				allCopy = false
				break
			}
		}
		if allCopy {
			targets = append(targets, Target{File: path, Test: b.name, Line: b.start})
		}
	}
	return targets
}

// tsCopyImports is the set of identifiers the file imports from code under
// test (not from test support) whose names say they are copy.
func tsCopyImports(text string) map[string]bool {
	imported := map[string]bool{}
	for _, m := range tsImport.FindAllStringSubmatch(text, -1) {
		names, src := m[1], m[2]
		if strings.Contains(src, "testing") || strings.HasPrefix(src, "vitest") || strings.Contains(src, "@testing-library") {
			continue
		}
		for n := range strings.SplitSeq(names, ",") {
			parts := strings.Split(strings.TrimSpace(n), " as ")
			if n := strings.TrimSpace(parts[len(parts)-1]); n != "" && copyIdent.MatchString(n) {
				imported[n] = true
			}
		}
	}
	return imported
}

// tsCopyClaim reports a check line that claims only that some copy appears:
// it carries a copy hit, nothing on it checks behavior, and none of its
// literals is text the test supplied.
func (s *scanner) tsCopyClaim(line, supplied string, imported map[string]bool) bool {
	if tsBehaviorOnLine.MatchString(line) {
		return false
	}
	for _, m := range tsQuoted.FindAllStringSubmatchIndex(line, -1) {
		if lit, _ := firstGroup(line, m, 1, 4); lit != "" && strings.Contains(supplied, lit) {
			return false
		}
	}
	return s.tsLineHasCopy(line, imported)
}

// tsLineHasCopy reports a copy oracle on the line: a by-text query or matcher
// against production copy or an imported copy constant.
func (s *scanner) tsLineHasCopy(line string, imported map[string]bool) bool {
	for _, rx := range []*regexp.Regexp{tsByText, tsMatcher} {
		for _, m := range rx.FindAllStringSubmatchIndex(line, -1) {
			if ident, ok := firstGroup(line, m, 5, 6); ok {
				if imported[ident] {
					return true
				}
				continue
			}
			probe, ok := firstGroup(line, m, 1, 4)
			if !ok {
				rx, _ := firstGroup(line, m, 4, 5)
				probe = regexCore(rx)
			}
			probe = strings.TrimSpace(probe)
			if !contractShaped(probe) && s.tsProd.contains(probe) {
				return true
			}
		}
	}
	return false
}

// tsBlock is one test(...) call: its name, the line it starts on, and its
// lines.
type tsBlock struct {
	name  string
	start int
	lines []string
}

// tsTestBlocks finds each test in a file. A test ends where its test(...)
// call closes, which also covers expression-bodied callbacks with no braces.
func tsTestBlocks(text string) []tsBlock {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var blocks []tsBlock
	for i := 0; i < len(lines); {
		m := tsTestStart.FindStringSubmatchIndex(lines[i])
		if m == nil {
			i++
			continue
		}
		name, _ := firstGroup(lines[i], m, 1, 4)
		depth, opened, j := 0, false, i
		for j < len(lines) {
			code := sanitizeTS(lines[j])
			depth += strings.Count(code, "(") - strings.Count(code, ")")
			opened = opened || strings.Contains(code, "(")
			j++
			if opened && depth <= 0 {
				break
			}
		}
		blocks = append(blocks, tsBlock{name: name, start: i + 1, lines: lines[i:j]})
		i = j
	}
	return blocks
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordBefore is the identifier-like word ending just before byte offset i,
// skipping spaces.
func wordBefore(line string, i int) string {
	j := i
	for j > 0 {
		r, size := utf8.DecodeLastRuneInString(line[:j])
		if !unicode.IsSpace(r) {
			break
		}
		j -= size
	}
	k := j
	for k > 0 {
		r, size := utf8.DecodeLastRuneInString(line[:k])
		if !isAlnum(r) && r != '_' && r != '$' {
			break
		}
		k -= size
	}
	return line[k:j]
}

// sanitizeTS returns the line with the contents of strings, regex literals
// and comments replaced by spaces. Its length is unchanged, so offsets found
// in it apply to the original line; parentheses inside text no longer count
// as code.
func sanitizeTS(line string) string {
	out := []byte(line)
	n := len(line)
	blank := func(a, b int) {
		for k := a; k < min(b, n); k++ {
			out[k] = ' '
		}
	}
	var prev rune // 0: nothing yet
	for i := 0; i < n; {
		c, size := utf8.DecodeRuneInString(line[i:])
		if c == '/' && i+1 < n && line[i+1] == '/' {
			blank(i, n)
			break
		}
		// A quote straight after a word is JSX text ("Don't retry"), unless
		// that word is a keyword that takes an expression (`return "x"`).
		if (c == '"' || c == '\'' || c == '`') &&
			(c == '`' || !isAlnum(prev) || tsExpressionKeywords[wordBefore(line, i)]) {
			j := i + 1
			for j < n && rune(line[j]) != c {
				if line[j] == '\\' {
					j += 2
				} else {
					j++
				}
			}
			blank(i+1, j)
			i, prev = j+1, c
			continue
		}
		if c == '/' && (prev == 0 || strings.ContainsRune(tsRegexPreceders, prev)) {
			j, inClass := i+1, false
			for j < n && (line[j] != '/' || inClass) {
				if line[j] == '\\' {
					j += 2
					continue
				}
				inClass = (inClass || line[j] == '[') && line[j] != ']'
				j++
			}
			blank(i+1, j)
			i, prev = j+1, '/'
			continue
		}
		if !unicode.IsSpace(c) {
			prev = c
		}
		i += size
	}
	return string(out)
}
