// Package copyonly finds tests that exist only to check wording, the ratchet
// behind docs/developing-evener/testing.md's "Copy Is Not a Test Oracle". The
// root copyonly_audit_test.go runs Scan over the repository and fails on any
// target missing from allowlist.tsv.
//
// A target is a test whose name says its subject is wording (it shows,
// renders, labels, says, ...) and whose every check is a copy claim. A check
// is a copy claim when it compares output with a copy literal and with nothing
// else: an expected error, a count or comparison, an attribute or state read
// from an element found by its text, or a comparison with a machine-contract
// token makes it a behavior check. A copy literal is one that also appears in
// production source (or, for Go, in a production text asset such as an
// embedded prompt), is not shaped like an identifier, code or markup, and is
// not text the test itself supplied: seeing a test's own fixture come back out
// is a real behavioral check.
//
// This is the Go port of tools/copy-oracle/inventory.py --copy-only. Go tests
// are read through go/ast, so conditions split across lines and raw strings
// holding braces are understood exactly. TypeScript tests keep the Python
// tool's line-based scanner.
package copyonly

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Target is a test that exists only to check wording: the file it lives in
// (slash-separated, relative to the scanned root), its name, and the line it
// starts on.
type Target struct {
	File string
	Test string
	Line int
}

// tsTrees are the TypeScript source trees: tests in them are scanned, and
// their non-test sources are the TypeScript production corpus. mobile/src is
// the shared tree mobile-native typechecks and tests.
var tsTrees = []string{"cmd/evener-hub/frontend/src/", "mobile-native/", "mobile/src/", "appwire-client/typescript/"}

var (
	tsSource = regexp.MustCompile(`\.(ts|tsx|mts)$`)
	// tsTestSupport marks TypeScript files that are tests or test support, so
	// none of them is production copy.
	tsTestSupport = regexp.MustCompile(`(/__tests__/|TestUtils\.|/testing/|/src/dev/)`)
	tsTestFile    = regexp.MustCompile(`\.(test|spec)\.`)
)

// assetExts are the non-Go text assets (embedded prompts, templates) whose
// phrases count as copy for Go tests.
var assetExts = []string{".md", ".tmpl", ".txt", ".gotmpl", ".prompt"}

// Scan reports every target among the tests git tracks in the checkout at
// root, sorted by file and line. Tracked files are read as they are in the
// working tree. Untracked files are left out, so a scratch file another test
// is writing can't change the answer; a new test counts once it is added.
func Scan(root string) ([]Target, error) {
	files, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	var goTests, tsTests, goProd, tsProd, assets []string
	for _, f := range files {
		switch {
		case strings.HasSuffix(f, "_test.go"):
			goTests = append(goTests, f)
		case strings.HasSuffix(f, ".go"):
			goProd = append(goProd, f)
		case inTSTree(f) && tsSource.MatchString(f) && !strings.HasSuffix(f, ".d.ts"):
			if tsTestFile.MatchString(f) {
				tsTests = append(tsTests, f)
			} else if !tsTestSupport.MatchString(f) {
				tsProd = append(tsProd, f)
			}
		case isAsset(f):
			assets = append(assets, f)
		}
	}
	s := newScanner(readAll(root, goProd), readAll(root, tsProd), readAll(root, assets))

	var targets []Target
	for _, f := range slices.Concat(goTests, tsTests) {
		src, err := os.ReadFile(filepath.Join(root, f))
		if os.IsNotExist(err) {
			continue // tracked but deleted in the working tree
		}
		if err != nil {
			return nil, fmt.Errorf("copyonly: %w", err)
		}
		found, err := s.scanFile(f, string(src))
		if err != nil {
			return nil, fmt.Errorf("copyonly: %s: %w", f, err)
		}
		targets = append(targets, found...)
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].File != targets[j].File {
			return targets[i].File < targets[j].File
		}
		return targets[i].Line < targets[j].Line
	})
	return targets, nil
}

// listFiles lists root's files as git sees them, minus vendored dependencies.
func listFiles(root string) ([]string, error) {
	cmd := exec.CommandContext(context.Background(), "git", "-C", root, "ls-files", "-z")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("copyonly: git ls-files in %s: %w: %s", root, err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" && !strings.Contains(f, "node_modules/") {
			files = append(files, f)
		}
	}
	return files, nil
}

func inTSTree(f string) bool {
	for _, tree := range tsTrees {
		if strings.HasPrefix(f, tree) {
			return true
		}
	}
	return false
}

// isAsset reports a production text asset. The docs and test data are prose
// and fixtures, not copy the product shows.
func isAsset(f string) bool {
	if strings.HasPrefix(f, "docs/") || strings.Contains(f, "/testdata/") {
		return false
	}
	for _, ext := range assetExts {
		if strings.HasSuffix(f, ext) {
			return true
		}
	}
	return false
}

// readAll joins the files' contents with newlines. A file that cannot be read
// (deleted in the working tree) contributes nothing.
func readAll(root string, files []string) string {
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

// corpus answers "does this literal appear in this text?". A miss scans the
// whole text and the same literal recurs across many tests, so answers are
// memoized.
type corpus struct {
	text string
	memo map[string]bool
}

func newCorpus(text string) *corpus {
	return &corpus{text: text, memo: map[string]bool{}}
}

func (c *corpus) contains(lit string) bool {
	found, ok := c.memo[lit]
	if !ok {
		found = strings.Contains(c.text, lit)
		c.memo[lit] = found
	}
	return found
}

// scanner holds the production corpora a test's literals are looked up in.
type scanner struct {
	goProd, tsProd, assets *corpus
}

func newScanner(goProd, tsProd, assets string) *scanner {
	return &scanner{goProd: newCorpus(goProd), tsProd: newCorpus(tsProd), assets: newCorpus(assets)}
}

// scanFile reports the targets in one test file, Go or TypeScript by its
// name.
func (s *scanner) scanFile(path, src string) ([]Target, error) {
	if strings.HasSuffix(path, ".go") {
		return s.scanGo(path, src)
	}
	return s.scanTS(path, src), nil
}

var (
	// contractToken is an identifier, path, flag or other machine-contract value.
	// Python's \w is Unicode-aware, hence the explicit classes.
	contractToken = regexp.MustCompile(`^[-\p{L}\p{N}_.:/@#=%+]*$`)
	// labelWord is a capitalized word ("Welcome", "Retry"): a label, not an
	// identifier.
	labelWord = regexp.MustCompile(`^[A-Z][a-z]+[.!?…]?$`)
	// codeLike literals are shaped like code, shell commands, markup or wire
	// syntax: format contracts even when they contain spaces.
	codeLike  = regexp.MustCompile("[{};=<>\"`$\\\\]|^(?:cat|wc|uname|id|mkdir|rm|ls|sh|bash|git|go|npm|npx|test|printf|echo|chmod|mv|cp|tar|export|func|type|package|import|const|var|set|exec|cd|env|sudo|kill|pgrep|ps|tmux|ssh|curl|make)\\b")
	hasLetter = regexp.MustCompile(`[A-Za-z]`)
	// copyPurposeName is a test name that says its subject is wording.
	copyPurposeName = regexp.MustCompile(`(?i)show|display|render|label|title|text|copy|message|word|say|mention|hint|placeholder|tooltip|caption|usage|help|heading|banner|describ|phrase|string`)
)

// contractShaped reports a literal that is a machine contract (or too short
// or letterless to be wording) rather than copy a person reads.
func contractShaped(lit string) bool {
	if utf8.RuneCountInString(lit) < 3 || !hasLetter.MatchString(lit) || codeLike.MatchString(lit) {
		return true
	}
	return contractToken.MatchString(lit) && !labelWord.MatchString(lit)
}

// regexCore is the longest run of literal text in a regex, so the regex can
// be looked up as copy. An escaped character (`\.`, `\?`) is literal text; a
// class escape (`\d`) or an unescaped metacharacter ends the run. Ties go to
// the first run.
func regexCore(rx string) string {
	var runs []string
	var run strings.Builder
	end := func() {
		runs = append(runs, run.String())
		run.Reset()
	}
	rs := []rune(rx)
	for i := 0; i < len(rs); i++ {
		switch ch := rs[i]; {
		case ch == '\\':
			i++
			if i >= len(rs) {
				continue
			}
			if strings.ContainsRune("dDsSwWbB", rs[i]) {
				end()
			} else {
				run.WriteRune(rs[i])
			}
		case strings.ContainsRune("^$.*+?()[]{}|", ch):
			end()
		default:
			run.WriteRune(ch)
		}
	}
	end()
	best := runs[0]
	for _, r := range runs[1:] {
		if utf8.RuneCountInString(r) > utf8.RuneCountInString(best) {
			best = r
		}
	}
	return best
}
