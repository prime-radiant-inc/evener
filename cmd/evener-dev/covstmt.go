// The covstmt subcommand counts statements in Go coverage profiles, so the
// shell coverage runners (coverage-floor.sh, e2e-cover.sh) invoke the same Go
// primitive the repo's tests pin instead of a drifted Python duplicate.
// Orchestration stays in shell; only the counting moved.
//
// `--gaps` reuses that same primitive for the ranking report coverage-gaps.sh
// prints: the shell script keeps its flags, usage, and validation, and the Go
// side owns the per-block parse + dedup + any-hit union the ranking is built
// on — the one thing a second implementation could silently diverge on.
package dev

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/internal/devtool/covstmt"
)

// covstmtMain implements `evener dev covstmt PROFILE...`: one line per profile,
// "covered total" — the shape coverage-floor.sh and e2e-cover.sh read with
// `read -r covered total` for the test, fuzz, and union tracks at once.
func covstmtMain(args []string) int {
	return covstmtRun(args, os.Stdout, os.Stderr)
}

func covstmtRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("evener dev covstmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	gaps := fs.Bool("gaps", false, "report ranked uncovered-statement gaps for one profile")
	union := fs.Bool("union", false, "count PROFILE... as ONE profile: per-position dedup, any-hit union")
	by := fs.String("by", "package", "gaps grouping: package or file")
	top := fs.Int("top", 25, "gaps: show at most this many rows")
	zero := fs.Bool("zero", false, "gaps: only wholly-uncovered units")
	in := fs.String("in", "", "gaps: list uncovered blocks inside files matching this substring")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: evener dev covstmt PROFILE [PROFILE ...]")
		_, _ = fmt.Fprintln(stderr, "  prints \"covered total\" per profile, one line each")
		_, _ = fmt.Fprintln(stderr, "  --union  print ONE line counting all PROFILEs as a per-position union")
		_, _ = fmt.Fprintln(stderr, "  --gaps  rank where the profile's uncovered statements are (one PROFILE)")
		_, _ = fmt.Fprintln(stderr, "          --by package|file  --top N  --zero  --in SUBSTRING")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// The gaps-only flags have no meaning in count mode. Silently ignoring them
	// would let a forgotten --gaps print counts where the caller expected a
	// ranking, so a stray one is a usage error rather than a no-op.
	if !*gaps {
		var stray []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "by", "top", "zero", "in":
				stray = append(stray, "--"+f.Name)
			}
		})
		if len(stray) > 0 {
			sort.Strings(stray)
			_, _ = fmt.Fprintf(stderr, "evener dev covstmt: --gaps is required for %s\n", strings.Join(stray, ", "))
			fs.Usage()
			return 2
		}
	}
	profiles := fs.Args()
	// The count mode takes one or more profiles; --gaps is a single profile's
	// ranking, so more than one is a usage error.
	if len(profiles) == 0 || (*gaps && len(profiles) != 1) {
		fs.Usage()
		return 2
	}
	if *gaps {
		if *union {
			_, _ = fmt.Fprintln(stderr, "evener dev covstmt: --union and --gaps are mutually exclusive")
			fs.Usage()
			return 2
		}
		return covstmtGapsRun(profiles[0], *by, *top, *zero, *in, stdout, stderr)
	}
	if *union {
		// One line for the whole set: a block deduped by position across every
		// profile, covered if ANY hit it. This replaces concatenating the files
		// and counting the concatenation, so a profile whose last line lacks a
		// newline cannot fuse with the next profile's header and drop a block.
		covered, total, err := covstmt.StmtCountsUnion(profiles)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "evener dev covstmt: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "%d %d\n", covered, total)
		return 0
	}
	var lines []string
	// Count every profile before printing any line: stdout is all-or-nothing,
	// so a consumer reading N lines for N profiles either gets all N or
	// nothing plus a non-zero exit. Emitting as it counts would hand a
	// partial line set to a `read` that cannot tell it was short.
	for _, p := range profiles {
		covered, total, err := covstmt.StmtCounts(p)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "evener dev covstmt: %s: %v\n", p, err)
			return 1
		}
		lines = append(lines, fmt.Sprintf("%d %d", covered, total))
	}
	for _, line := range lines {
		_, _ = fmt.Fprintln(stdout, line)
	}
	return 0
}

// covstmtGapsRun reproduces the ranking report scripts/coverage/coverage-gaps.sh
// used to print from embedded Python. The counting (parse, position dedup,
// any-hit union) is covstmt.Blocks; the ranking and formatting are ported
// byte-for-byte so the script's output is unchanged.
func covstmtGapsRun(profile, by string, top int, zeroOnly bool, inPattern string, stdout, stderr io.Writer) int {
	if by != "package" && by != "file" {
		_, _ = fmt.Fprintf(stderr, "evener dev covstmt: --by must be package or file (got %s)\n", by)
		return 2
	}
	if top < 0 {
		// A negative count is a typo, not "show nothing": clamping it to zero
		// would exit 0 with an empty report, which reads as a clean run. Reject
		// it like any other unusable flag value, so the caller sees the mistake.
		_, _ = fmt.Fprintf(stderr, "evener dev covstmt: --top must not be negative (got %d)\n", top)
		return 2
	}
	blocks, err := covstmt.Blocks(profile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener dev covstmt: %s: %v\n", profile, err)
		return 1
	}
	if inPattern != "" {
		gapsIn(blocks, inPattern, top, stdout)
		return 0
	}
	gapsAggregate(blocks, by, top, zeroOnly, stdout)
	return 0
}

// gapsIn lists the uncovered BLOCKS inside files matching pattern, biggest
// first, so a file with a known gap turns straight into a list of line ranges
// to go read. Aggregates say which file to work on; this says where in it.
func gapsIn(blocks []covstmt.Block, pattern string, top int, w io.Writer) {
	type row struct {
		ns        int
		file      string
		startLine int
		endLine   int
	}
	var rows []row
	total := 0
	// Match the way Python did: both the pattern (argv, raw bytes in Go) and
	// each file name are viewed through os.fsdecode's surrogateescape, then
	// compared as rune sequences. A byte scan would let a raw 0xa9 pattern byte
	// match the 0xc3 0xa9 tail of 'é', which Python does not.
	patternRunes := surrogateEscapeRunes(pattern)
	for _, b := range blocks {
		if b.Covered || !containsRunes(surrogateEscapeRunes(b.File), patternRunes) {
			continue
		}
		rows = append(rows, row{ns: b.StmtCount, file: b.File, startLine: b.StartLine, endLine: b.EndLine})
		total += b.StmtCount
	}
	// Python's `rows.sort(reverse=True)` on (ns, file, start, end): every field
	// descending, so ties are totally ordered and the output is stable.
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.ns != b.ns {
			return a.ns > b.ns
		}
		if a.file != b.file {
			return a.file > b.file
		}
		if a.startLine != b.startLine {
			return a.startLine > b.startLine
		}
		return a.endLine > b.endLine
	})
	if len(rows) == 0 {
		_, _ = fmt.Fprintf(w, "no uncovered blocks in files matching %s\n", pyRepr(pattern))
		return
	}
	_, _ = fmt.Fprintf(w, "%8s  %s\n", "STMTS", "location")
	for i, r := range rows {
		if i >= top {
			break
		}
		_, _ = fmt.Fprintf(w, "%8d  %s:%d-%d\n", r.ns, r.file, r.startLine, r.endLine)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "showing %d of %d uncovered blocks (%d statements) in files matching %s\n",
		min(top, len(rows)), len(rows), total, pyRepr(pattern))
}

// gapsAggregate ranks packages (or files) by their UNCOVERED statement count,
// not by percentage: a 40%-covered file holding 12 statements is noise next to
// a 90%-covered one holding 900.
func gapsAggregate(blocks []covstmt.Block, by string, top int, zeroOnly bool, w io.Writer) {
	type unit struct {
		covered int
		total   int
	}
	units := make(map[string]*unit)
	for _, b := range blocks {
		name := b.File
		if by != "file" {
			name = packageOf(b.File)
		}
		u := units[name]
		if u == nil {
			u = &unit{}
			units[name] = u
		}
		u.total += b.StmtCount
		if b.Covered {
			u.covered += b.StmtCount
		}
	}

	type row struct {
		missing int
		total   int
		covered int
		name    string
	}
	var rows []row
	grandMissing, grandTotal := 0, 0
	for name, u := range units {
		missing := u.total - u.covered
		grandMissing += missing
		grandTotal += u.total
		if missing == 0 {
			continue
		}
		if zeroOnly && u.covered != 0 {
			continue
		}
		rows = append(rows, row{missing: missing, total: u.total, covered: u.covered, name: name})
	}
	// Python's `rows.sort(reverse=True)` on (missing, total, covered, name).
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.missing != b.missing {
			return a.missing > b.missing
		}
		if a.total != b.total {
			return a.total > b.total
		}
		if a.covered != b.covered {
			return a.covered > b.covered
		}
		return a.name > b.name
	})

	_, _ = fmt.Fprintf(w, "%8s %8s %8s  %s\n", "MISSING", "total", "cov%", by)
	for i, r := range rows {
		if i >= top {
			break
		}
		_, _ = fmt.Fprintf(w, "%8d %8d %7.1f%%  %s\n", r.missing, r.total, pctOf(r.covered, r.total), r.name)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "showing %d of %d %ss with gaps; %d uncovered of %d statements overall (%.1f%%)\n",
		min(top, len(rows)), len(rows), by, grandMissing, grandTotal, pctOf(grandTotal-grandMissing, grandTotal))
}

// packageOf mirrors Python's `f.rsplit("/", 1)[0]`: the directory containing a
// file, or the file path itself when it has no slash (so a top-level profile
// path groups as its own package rather than under ".").
func packageOf(file string) string {
	if i := strings.LastIndex(file, "/"); i >= 0 {
		return file[:i]
	}
	return file
}

// pctOf mirrors Python's `100.0 * cov / tot if tot else 0.0`.
func pctOf(covered, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100.0 * float64(covered) / float64(total)
}

// surrogateEscapeRunes decodes s the way Python's os.fsdecode does: a byte that
// is not part of a valid UTF-8 sequence becomes U+DC00+byte. It gives argv (raw
// bytes in Go) the same Unicode view Python saw, so repr() and substring
// matching agree with it.
func surrogateEscapeRunes(s string) []rune {
	runes := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			runes = append(runes, 0xDC00+rune(s[i]))
			i++
			continue
		}
		runes = append(runes, r)
		i += size
	}
	return runes
}

// containsRunes reports whether haystack contains needle as a contiguous rune
// subsequence. Matching runes (not bytes) is what keeps --in equivalent to
// Python's `in_pattern not in f`: a raw 0xa9 argv byte is U+DCA9 and must not
// match the 0xc3 0xa9 that ends 'é', where a byte scan would falsely match.
func containsRunes(haystack, needle []rune) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// pyRepr renders s the way Python's repr() does: single-quoted, switching to
// double quotes when the string contains a single quote but no double quote,
// with backslashes, the chosen quote, and every non-printable rune escaped.
// Escaping the non-printables (\xNN, \uNNNN, \UNNNNNNNN) matters even though no
// coverage file path carries one: an --in pattern is caller-supplied, and a raw
// ESC or NUL would otherwise reach the terminal and diverge from the Python
// this report replaced.
//
// Python decodes argv with surrogateescape, so a byte that is not valid UTF-8
// arrives as U+DC80..U+DCFF and repr() renders it as \udcXX. os.Args in Go holds
// the raw bytes, and ranging over the string would replace them with U+FFFD;
// decode the same way Python does so a non-UTF-8 pattern reprs identically.
func pyRepr(s string) string {
	runes := surrogateEscapeRunes(s)
	decoded := string(runes)
	quote := '\''
	if strings.Contains(decoded, "'") && !strings.Contains(decoded, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range runes {
		switch {
		case r == '\\' || r == quote:
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			_, _ = fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			_, _ = fmt.Fprintf(&b, `\u%04x`, r)
		default:
			_, _ = fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}
