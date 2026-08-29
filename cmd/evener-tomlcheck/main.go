// evener-tomlcheck enforces the project's wire-format naming convention for
// TOML data files: every key and table name is snake_case.
//
// Go struct tags are NOT checked here — golangci-lint's tagliatelle linter
// owns those (json/toml struct tags, with the camelCase wire-protocol
// overrides); see .golangci.yml and docs/developing-evener/conventions/naming.md.
//
// It prints one violation per line in `path:line: message` format and exits
// non-zero when violations exist.
//
// Run it via `make lint-naming` (or directly: `go run ./cmd/evener-tomlcheck`).
//
// A single key can opt out with a `# evener:naming-ignore` marker on the
// immediately preceding line. Use sparingly — every opt-out should also
// carry a comment explaining why.
package tomlcheck

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Violation describes one naming-convention failure with enough context to
// fix it from the message alone.
type Violation struct {
	File    string
	Line    int
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Message)
}

// defaultExcludes is the path-prefix list skipped by the walker. Paths are
// matched relative to the repo root with forward slashes.
var defaultExcludes = []string{
	"inspo/",
	"vendor/",
	"node_modules/",
	".git/",
}

// excludeSuffixes matches anywhere in the relative path.
var excludeSuffixes = []string{
	"/testdata/",
	"/.git/",
}

func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return runTOML(args, stdout, stderr)
}

var filepathAbs = filepath.Abs
var tomlRun = scanTOML
var filepathWalkDir = filepath.WalkDir
var filepathRel = filepath.Rel
var tomlFileChecker = checkTOMLFile

func runTOML(args []string, stdout, stderr io.Writer) int {
	var (
		root    string
		verbose bool
	)
	fs := flag.NewFlagSet("evener tomlcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&root, "root", ".", "repo root to scan")
	fs.BoolVar(&verbose, "v", false, "print scanned files")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	abs, err := filepathAbs(root)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "tomlcheck:", err)
		return 2
	}
	violations, err := tomlRun(abs, verbose)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "tomlcheck:", err)
		return 2
	}
	for _, v := range violations {
		_, _ = fmt.Fprintln(stdout, v)
	}
	if len(violations) > 0 {
		_, _ = fmt.Fprintf(stderr, "\n%d naming violation(s)\n", len(violations))
		return 1
	}
	return 0
}

// scanTOML walks root and returns every TOML violation it finds. Exposed for
// tests.
func scanTOML(root string, verbose bool) ([]Violation, error) {
	var out []Violation
	err := filepathWalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepathRel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			// Skip excluded directories outright.
			if rel != "." && (isExcluded(rel+"/") || strings.HasPrefix(filepath.Base(path), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if isExcluded(rel) {
			return nil
		}
		if strings.HasSuffix(path, ".toml") {
			if verbose {
				fmt.Fprintln(os.Stderr, "scan toml:", rel)
			}
			vs, err := tomlFileChecker(path, rel)
			if err != nil {
				return err
			}
			out = append(out, vs...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func isExcluded(rel string) bool {
	for _, p := range defaultExcludes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	for _, s := range excludeSuffixes {
		if strings.Contains(rel, s) {
			return true
		}
	}
	// Hidden dirs (anything whose path segment starts with a dot, e.g.
	// .github, .git) are walked at top level but otherwise skipped.
	for seg := range strings.SplitSeq(rel, "/") {
		if len(seg) > 1 && strings.HasPrefix(seg, ".") && seg != ".github" {
			return true
		}
	}
	return false
}

// --- naming predicates -----------------------------------------------------

// snake_case: lowercase letters/digits, optionally separated by single
// underscores. A single-word lowercase key (e.g. "model") matches.
var snakeRe = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)

func isSnakeCase(s string) bool { return snakeRe.MatchString(s) }

// toSnakeCase produces a best-effort snake_case suggestion. CamelCase is split
// on case boundaries; hyphens become underscores; result is lowercased.
func toSnakeCase(s string) string {
	parts := splitWords(s)
	for i := range parts {
		parts[i] = strings.ToLower(parts[i])
	}
	return strings.Join(parts, "_")
}

// splitWords decomposes a name into word parts. Recognises underscores,
// hyphens, and camelCase/PascalCase boundaries.
func splitWords(s string) []string {
	// First normalise underscores/hyphens into a single delimiter.
	tmp := strings.ReplaceAll(s, "_", " ")
	tmp = strings.ReplaceAll(tmp, "-", " ")
	// Now split camelCase: insert a space before each uppercase letter that
	// follows a lowercase letter, and at the boundary of an acronym run that
	// transitions back to lowercase (e.g. "HTTPRequest" -> "HTTP Request").
	var b strings.Builder
	runes := []rune(tmp)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			lower := func(r rune) bool { return r >= 'a' && r <= 'z' }
			upper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
			if (lower(prev) && upper(r)) || (upper(prev) && upper(r) && lower(next)) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	fields := strings.Fields(b.String())
	return fields
}

// --- TOML files ------------------------------------------------------------

const ignoreMarker = "evener:naming-ignore"

var tomlKeyLineRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_\-]*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_\-]*)*)\s*=`)

// skipTOMLQuoted returns the index just past the quoted span in runes that
// begins at i (runes[i] is the opening `"` or `'`). A `"`-quoted basic
// string may contain an escaped `\"`; a `'`-quoted literal string cannot
// contain `'` at all, so its first `'` after the opening one always closes
// it. An unterminated quote runs to the end of runes rather than panicking.
func skipTOMLQuoted(runes []rune, i int) int {
	quote := runes[i]
	i++
	for i < len(runes) && runes[i] != quote {
		if quote == '"' && runes[i] == '\\' && i+1 < len(runes) {
			i++
		}
		i++
	}
	if i < len(runes) {
		i++ // consume the closing quote
	}
	return i
}

// splitTOMLKeyPath splits a dotted TOML key path — the inside of a
// [table.header] or the left side of a `key = value` line — into its
// dot-separated segments, treating a dot inside a quoted segment as a
// literal character rather than a separator. Segments are trimmed of
// surrounding whitespace and empty segments are dropped; quoted segments
// are returned verbatim, including their quotes.
func splitTOMLKeyPath(path string) []string {
	var segments []string
	var cur strings.Builder
	flush := func() {
		if seg := strings.TrimSpace(cur.String()); seg != "" {
			segments = append(segments, seg)
		}
		cur.Reset()
	}
	runes := []rune(path)
	i := 0
	for i < len(runes) {
		switch r := runes[i]; r {
		case '.':
			flush()
			i++
		case '"', '\'':
			start := i
			i = skipTOMLQuoted(runes, i)
			cur.WriteString(string(runes[start:i]))
		default:
			cur.WriteRune(r)
			i++
		}
	}
	flush()
	return segments
}

// tomlTableHeaderPath recognizes a TOML table-header line — [path] or
// [[path]], with an optional trailing `# comment` — and returns the
// enclosed dotted key path. Like splitTOMLKeyPath, it is quote-aware: it
// scans for the closing `]`/`]]` outside any quoted span, so a `]` inside a
// quoted segment of path (e.g. the "[1m]" in a model id like
// "claude-sonnet-4-5[1m]") does not end the header early. ok is false when
// line is not a table-header line at all (including a malformed one, e.g.
// mismatched bracket counts or trailing content that isn't a comment).
func tomlTableHeaderPath(line string) (path string, ok bool) {
	rest := strings.TrimSpace(line)
	double := false
	switch {
	case strings.HasPrefix(rest, "[["):
		double = true
		rest = rest[2:]
	case strings.HasPrefix(rest, "["):
		rest = rest[1:]
	default:
		return "", false
	}
	runes := []rune(rest)
	for i := 0; i < len(runes); {
		switch runes[i] {
		case '"', '\'':
			i = skipTOMLQuoted(runes, i)
		case ']':
			pathRunes := runes[:i]
			i++
			if double {
				if i >= len(runes) || runes[i] != ']' {
					return "", false
				}
				i++
			}
			tail := strings.TrimSpace(string(runes[i:]))
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return "", false
			}
			return string(pathRunes), true
		default:
			i++
		}
	}
	return "", false
}

func checkTOMLFile(path, rel string) ([]Violation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Violation
	lines := strings.Split(string(data), "\n")

	inMultilineString := false
	multilineDelim := ""
	prevIgnore := false

	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)

		// Handle multi-line strings (triple-quoted). Don't lint inside them.
		if inMultilineString {
			if strings.Contains(line, multilineDelim) {
				inMultilineString = false
				multilineDelim = ""
			}
			continue
		}

		if trimmed == "" {
			prevIgnore = false
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if strings.Contains(trimmed, ignoreMarker) {
				prevIgnore = true
			}
			continue
		}

		// Table header: [section] or [[array.of.tables]]
		if headerPath, headerOK := tomlTableHeaderPath(line); headerOK {
			for _, key := range splitTOMLKeyPath(headerPath) {
				// Quoted keys are allowed verbatim — used for dotted keys
				// that intentionally embed dots/spaces.
				if strings.HasPrefix(key, `"`) || strings.HasPrefix(key, `'`) {
					continue
				}
				if !prevIgnore && !isSnakeCase(key) {
					out = append(out, Violation{
						File:    rel,
						Line:    lineNo,
						Message: fmt.Sprintf("toml table key %q must be snake_case (suggest %q)", key, toSnakeCase(key)),
					})
				}
			}
			prevIgnore = false
			continue
		}

		// Plain key = value lines.
		if m := tomlKeyLineRe.FindStringSubmatch(line); m != nil {
			for _, key := range splitTOMLKeyPath(m[1]) {
				// Quoted keys are allowed verbatim, same rule as table
				// headers above.
				if strings.HasPrefix(key, `"`) || strings.HasPrefix(key, `'`) {
					continue
				}
				if !prevIgnore && !isSnakeCase(key) {
					out = append(out, Violation{
						File:    rel,
						Line:    lineNo,
						Message: fmt.Sprintf("toml key %q must be snake_case (suggest %q)", key, toSnakeCase(key)),
					})
				}
			}
			// Watch for the start of a triple-quoted string so we don't
			// misparse content lines inside it.
			rest := line[strings.Index(line, "=")+1:]
			for _, delim := range []string{`"""`, `'''`} {
				if idx := strings.Index(rest, delim); idx >= 0 {
					if !strings.Contains(rest[idx+3:], delim) {
						inMultilineString = true
						multilineDelim = delim
					}
				}
			}
			prevIgnore = false
			continue
		}
		prevIgnore = false
	}
	return out, nil
}
