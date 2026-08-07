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
	// accounted holds every match range found in the current window, in stream
	// offsets, ascending. A range found again in a later window has already been
	// reported, so it does not fire twice; ranges that fall out of the window are
	// pruned. This is what makes overlapping windows safe.
	accounted []matchRange
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
	Text       string
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
	m.accounted = nil
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
	m.accounted = m.scanWindow(m.carry, m.scanOffset-int64(len(m.carry)))
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

	found := m.scanWindow(window, windowStart)
	var matches []OutputMatch
	for _, r := range newRanges(found, m.accounted) {
		matches = append(matches, OutputMatch{
			Text:       matchExcerpt(window, int(r.start-windowStart), int(r.end-windowStart)),
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
	m.accounted = pruneRanges(found, endOffset-int64(len(m.carry)))
	return matches
}

// ScanRetained applies the matcher's regexp to data as a single window and
// returns the LAST matching excerpt. It is the level check used at attach — "the
// output already contains the pattern" — and honours the same match-length bound
// as the stream, so attach and stream agree on what can match. Unlike the old
// line matcher it does not require a trailing newline: a match in an
// unterminated tail counts.
//
// It is a pure read: it does not touch the window, the scan offset, or the feed
// counter, so it composes with a subsequent FeedAt.
func (m *OutputMatcher) ScanRetained(data []byte) (last string, matched bool) {
	found := m.scanWindow(data, 0)
	if len(found) == 0 {
		return "", false
	}
	r := found[len(found)-1]
	return matchExcerpt(data, int(r.start), int(r.end)), true
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
	if hi < end {
		end = hi
	}
	out := window[lo:hi]
	if len(out) > 0 && out[len(out)-1] == '\r' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// newRanges returns the ranges in found — both slices ascending — that are not
// already in accounted, preserving order.
func newRanges(found, accounted []matchRange) []matchRange {
	if len(accounted) == 0 {
		return found
	}
	var fresh []matchRange
	j := 0
	for _, r := range found {
		for j < len(accounted) && lessRange(accounted[j], r) {
			j++
		}
		if j < len(accounted) && accounted[j] == r {
			j++
			continue
		}
		fresh = append(fresh, r)
	}
	return fresh
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
