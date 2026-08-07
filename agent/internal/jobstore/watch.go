package jobstore

import (
	"bytes"
	"regexp"

	"primeradiant.com/serf/agent/provenance"
)

// outputMatchWindowBytes is the rolling scan window an output_match watch reads
// through. The matcher keeps the last outputMatchWindowBytes bytes it has
// already scanned and prepends them to every new chunk before running the
// pattern, so a match is found wherever it lands in the byte stream and a chunk
// boundary is never a match boundary.
//
// It is also the stated limit on match LENGTH: a single match longer than the
// window is not reported (there is no window that can hold it, and reporting it
// would depend on how the producer happened to chunk its writes). Documented
// for the model in the job_watch tool description and docs/job-control.md.
const outputMatchWindowBytes = 4096

// CompileOutputMatch compiles an output_match pattern the way the scanner runs
// it: multiline, so ^ and $ anchor at newlines within the scan window rather
// than only at the window's own edges. It is the single home for that decision,
// shared by the live stream and the attach-time level scan.
//
// The raw pattern is compiled first so a user error reports the user's own
// pattern rather than the wrapped form.
func CompileOutputMatch(pattern string) (*regexp.Regexp, error) {
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, err
	}
	return regexp.Compile("(?m:" + pattern + ")")
}

// OutputMatcher applies a regexp to a rolling byte window over an output stream.
// Lines are not a concept: output that never emits a newline (a progress bar, a
// JSON blob, a build log written without terminators) matches exactly like
// output that does.
type OutputMatcher struct {
	re *regexp.Regexp
	// carry is the tail of the already-scanned stream — at most
	// outputMatchWindowBytes bytes, ending at scanOffset — prepended to the next
	// chunk to form the scan window.
	carry []byte
	// carryProvenance is the causal provenance of the chunks that have
	// contributed to the window since the last reported match. It is reported
	// with that match and then cleared.
	carryProvenance *provenance.Causal
	// scanOffset is the stream offset just past the last byte scanned. Bytes
	// below it are covered — by an attach-time scan or by an earlier feed — so
	// FeedAt contributes nothing from them.
	scanOffset int64
	// feedOffset counts every byte handed to Feed, supplying the stream end
	// offset for callers that feed sequentially without tracking offsets.
	feedOffset int64
	// reported holds the extents already delivered (or covered by an attach-time
	// scan) that still lie inside the window, ascending and non-overlapping. A
	// newly found range that overlaps one of them is the same occurrence seen
	// again from a window that has slid, so it does not fire twice; ranges that
	// fall out of the window are pruned. This is what makes overlapping windows
	// safe.
	reported []matchRange
}

// matchRange is a half-open match extent in the stream's lifetime byte space.
type matchRange struct {
	start int64
	end   int64
}

// OutputMatch is a matched excerpt of output plus the causal provenance of the
// chunks that produced it.
type OutputMatch struct {
	// Text is the matched excerpt: the match widened to the start of the line it
	// begins on and out to the next newline, capped at outputMatchWindowBytes and
	// stripped of a single trailing '\r'. It never contains a newline.
	Text string
	// Start and End are the match's own extent in the stream's lifetime byte
	// space — not the excerpt's. They identify the occurrence: the matcher
	// reports each occurrence once, and two reported extents never overlap.
	Start      int64
	End        int64
	Provenance *provenance.Causal
}

// NewOutputMatcher returns a matcher over re. Callers that compile a
// model-supplied pattern should build re with CompileOutputMatch.
func NewOutputMatcher(re *regexp.Regexp) *OutputMatcher {
	return &OutputMatcher{re: re}
}

// Regexp returns the compiled pattern this matcher applies, so callers that
// already hold a matcher can reuse its regexp instead of recompiling.
func (m *OutputMatcher) Regexp() *regexp.Regexp { return m.re }

// SetScanOffset marks bytes at stream offsets below off as covered by an
// attach-time scan. FeedAt discards them, so output seen by both the scan and
// the stream cannot fire twice. It drops the window, which no longer abuts the
// new offset; SeedCarry restores it.
func (m *OutputMatcher) SetScanOffset(off int64) {
	m.scanOffset = off
	m.carry = m.carry[:0]
	m.carryProvenance = nil
	m.reported = nil
}

// SeedCarry primes the rolling window with output already covered by an
// attach-time scan — pass the retained bytes that end at the scan offset. Its
// last outputMatchWindowBytes bytes become the window, and every match already
// inside them is recorded as reported, so the attach scan and the live stream
// cannot both fire for the same bytes while a token straddling the attach
// boundary still matches once the rest of it arrives.
func (m *OutputMatcher) SeedCarry(scanned []byte) {
	if len(scanned) > outputMatchWindowBytes {
		scanned = scanned[len(scanned)-outputMatchWindowBytes:]
	}
	m.carry = append(m.carry[:0], scanned...)
	m.carryProvenance = nil
	m.reported = m.scanWindow(m.carry, m.scanOffset-int64(len(m.carry)))
}

// Feed appends newly produced output and returns matching excerpts. It assumes
// the stream is fed sequentially from its first byte: an internal byte counter
// supplies the end offset FeedAt needs, so bytes an attach-time scan already
// covered are skipped even when their chunk arrives after SetScanOffset.
func (m *OutputMatcher) Feed(chunk []byte) []string {
	m.feedOffset += int64(len(chunk))
	return m.FeedAt(chunk, m.feedOffset)
}

// FeedAt appends newly produced output whose final byte sits just before stream
// offset endOffset and returns matching excerpts. Chunks wholly below the scan
// offset are discarded; a straddling chunk contributes only its suffix at or
// beyond the scan offset.
func (m *OutputMatcher) FeedAt(chunk []byte, endOffset int64) []string {
	return outputMatchTexts(m.FeedAtWithProvenance(chunk, endOffset, nil))
}

// FeedAtWithProvenance is FeedAt plus causal provenance for the supplied chunk.
// Provenance accumulates across every chunk that has contributed to the window
// since the last reported match, so a match assembled from several chunks
// reports the union of all of them.
func (m *OutputMatcher) FeedAtWithProvenance(chunk []byte, endOffset int64, p *provenance.Causal) []OutputMatch {
	if len(chunk) == 0 || endOffset <= m.scanOffset {
		return nil
	}
	if start := endOffset - int64(len(chunk)); start < m.scanOffset {
		chunk = chunk[m.scanOffset-start:]
	}
	if p != nil {
		m.carryProvenance = provenance.Union(m.carryProvenance, p)
	}

	window := make([]byte, 0, len(m.carry)+len(chunk))
	window = append(window, m.carry...)
	window = append(window, chunk...)
	windowStart := endOffset - int64(len(window))

	fresh := unreportedRanges(m.scanWindow(window, windowStart), m.reported)
	var matches []OutputMatch
	for _, r := range fresh {
		matches = append(matches, OutputMatch{
			Text:       matchExcerpt(window, int(r.start-windowStart), int(r.end-windowStart)),
			Start:      r.start,
			End:        r.end,
			Provenance: provenance.Clone(m.carryProvenance),
		})
	}
	if len(matches) > 0 {
		m.carryProvenance = nil
	}

	m.scanOffset = endOffset
	if len(window) > outputMatchWindowBytes {
		window = window[len(window)-outputMatchWindowBytes:]
	}
	m.carry = append(m.carry[:0], window...)
	m.reported = pruneRanges(mergeRanges(m.reported, fresh), endOffset-int64(len(m.carry)))
	return matches
}

// ScanRetained walks data through the same rolling window the stream uses and
// returns the LAST matching excerpt. It is the level check used at attach and at
// terminal catch-up — "the output already contains the pattern" — and it does not
// require a trailing newline: a match in an unterminated tail counts.
//
// Retained output can be megabytes over a model-supplied pattern, so this never
// materialises every match. It walks overlapping windows of
// 2*outputMatchWindowBytes, stepping by outputMatchWindowBytes so any match
// within the bound is wholly inside some window — but BACKWARD, testing each
// window with a boolean Match first. Only the last window that actually contains
// a reportable match pays for match positions, so memory and allocation are
// O(window), not O(matches). A window whose only matches are over the length
// bound yields nothing and the walk keeps going back.
//
// It shares the stream's window size and match-length bound, so neither path can
// see a match the other calls too long. The two do NOT always agree on anchored
// patterns: ^ and $ also match at a window's own edges, and those edges fall on a
// fixed stride here but wherever chunks happen to land on the live path. An
// anchored pattern can therefore fire on one path and not the other.
//
// It is a pure read: it does not touch the window, the scan offset, the feed
// counter, or the reported set, so it composes with a subsequent FeedAt.
func (m *OutputMatcher) ScanRetained(data []byte) (last string, matched bool) {
	if len(data) == 0 {
		return "", false
	}
	for start := ((len(data) - 1) / outputMatchWindowBytes) * outputMatchWindowBytes; start >= 0; start -= outputMatchWindowBytes {
		window := data[start:min(start+2*outputMatchWindowBytes, len(data))]
		if !m.re.Match(anchorText(window)) {
			continue
		}
		found := m.scanWindow(window, int64(start))
		if len(found) == 0 {
			continue
		}
		r := found[len(found)-1]
		return matchExcerpt(window, int(r.start)-start, int(r.end)-start), true
	}
	return "", false
}

// scanWindow runs the pattern over window — whose first byte sits at stream
// offset windowStart — and returns every match short enough to report, in
// stream offsets, ascending.
func (m *OutputMatcher) scanWindow(window []byte, windowStart int64) []matchRange {
	if len(window) == 0 {
		return nil
	}
	locs := m.re.FindAllIndex(anchorText(window), -1)
	if len(locs) == 0 {
		return nil
	}
	ranges := make([]matchRange, 0, len(locs))
	for _, loc := range locs {
		if loc[1]-loc[0] > outputMatchWindowBytes {
			continue
		}
		ranges = append(ranges, matchRange{start: windowStart + int64(loc[0]), end: windowStart + int64(loc[1])})
	}
	return ranges
}

// anchorText returns the bytes the pattern is actually run against: the window
// with every CRLF's '\r' rewritten to '\n', so a line written by a CRLF producer
// ends where $ expects it to. The rewrite is length-preserving, so match indices
// still address the original window, and excerpts are always cut from the
// original bytes.
//
// RE2 defines both ^ and $ by '\n' and offers no line-terminator setting, so a
// rewrite that lets $ reach a CRLF line end necessarily lets ^ match one byte
// later as well. Two consequences on a CRLF stream, both documented for the model
// in docs/job-control.md: a bare `^$` sees one empty line per CRLF, and a pattern
// matching a literal "\r\n" cannot match. Trading those for "$ works at all on
// Windows-style output" is the better deal.
func anchorText(window []byte) []byte {
	i := bytes.IndexByte(window, '\r')
	if i < 0 {
		return window
	}
	out := append([]byte(nil), window...)
	for ; i < len(out)-1; i++ {
		if out[i] == '\r' && out[i+1] == '\n' {
			out[i] = '\n'
		}
	}
	return out
}

// matchExcerpt renders the reported text for a match at [start, end) in window:
// the line the match begins on, capped at outputMatchWindowBytes and stripped of
// a single trailing '\r'. It never contains a newline, so a match that spans
// lines reports only the first of them.
func matchExcerpt(window []byte, start, end int) string {
	lo := 0
	if i := bytes.LastIndexByte(window[:start], '\n'); i >= 0 {
		lo = i + 1
	}
	hi := len(window)
	if i := bytes.IndexByte(window[start:], '\n'); i >= 0 {
		hi = start + i
	}
	if hi-lo > outputMatchWindowBytes {
		lo = start
		hi = min(start+outputMatchWindowBytes, hi)
	}
	out := window[lo:hi]
	if len(out) > 0 && out[len(out)-1] == '\r' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// unreportedRanges returns the ranges in found — both slices ascending and
// internally non-overlapping — that are not the same occurrence as anything in
// reported, preserving order.
//
// "Same occurrence" is OVERLAP, not equality. A window that has slid re-matches
// the same occurrence at a different extent: a pattern that eats leftward
// (`x+READY`) starts at each new window's own start edge, and one that eats
// rightward (`READY.*`) ends at each new window's end. Equality dedup lets both
// fire once per chunk. Overlap dedup reports each occurrence exactly once and
// still fires on any match that reaches bytes no reported match covered — the
// price is that a second token falling inside the span of an already-reported
// greedy match is read as part of that match rather than as a new one.
func unreportedRanges(found, reported []matchRange) []matchRange {
	if len(reported) == 0 {
		return found
	}
	var fresh []matchRange
	j := 0
	for _, r := range found {
		// reported is ascending and non-overlapping, so everything ending before r
		// starts is behind us for every remaining r too.
		for j < len(reported) && reported[j].end < r.start {
			j++
		}
		seen := false
		for k := j; k < len(reported) && reported[k].start <= r.end; k++ {
			if sameOccurrence(r, reported[k]) {
				seen = true
				break
			}
		}
		if !seen {
			fresh = append(fresh, r)
		}
	}
	return fresh
}

// sameOccurrence reports whether two match extents describe the same occurrence:
// they overlap, or they are the identical (possibly empty) extent. The equality
// arm carries zero-length matches, which cannot overlap anything.
func sameOccurrence(a, b matchRange) bool {
	if a == b {
		return true
	}
	return a.start < b.end && b.start < a.end
}

// mergeRanges merges two ascending, mutually non-overlapping range slices into
// one ascending slice.
func mergeRanges(a, b []matchRange) []matchRange {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]matchRange, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if lessRange(a[i], b[j]) {
			out = append(out, a[i])
			i++
			continue
		}
		out = append(out, b[j])
		j++
	}
	return append(append(out, a[i:]...), b[j:]...)
}

// pruneRanges drops ranges that can no longer reappear because they have fallen
// out of the window starting at windowStart.
func pruneRanges(ranges []matchRange, windowStart int64) []matchRange {
	kept := ranges[:0]
	for _, r := range ranges {
		if r.end > windowStart {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func lessRange(a, b matchRange) bool {
	if a.start != b.start {
		return a.start < b.start
	}
	return a.end < b.end
}

func outputMatchTexts(matches []OutputMatch) []string {
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.Text)
	}
	return out
}
