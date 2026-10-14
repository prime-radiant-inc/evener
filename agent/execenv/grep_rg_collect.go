package execenv

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
)

// rgRecordCollector is the stdout sink for a ripgrep search. ripgrep emits a
// file's records together but in file-completion order (a parallel search), so
// a byte cap on the raw stream retains whatever prefix the emission happened to
// produce — two identical searches can then return different files even with
// max_results=1, where the pre-refactor flow (collect all, sort, cut) was
// deterministic. The collector instead parses the stream into per-path blocks
// as they arrive and keeps the keep blocks with the smallest paths, so the
// retained set depends only on the corpus and the query, never on emission
// order. (The sort key matches ripgrepOutputLines' path ordering, including its
// platform path separator, so the retained blocks are the ones that order would
// put first.)
//
// keep counts BLOCKS, not output lines: in content mode a match's context
// window rides with its path, so keeping maxResults+contextLines+1 blocks is
// enough for the caller's maxResults-LINE cut to select the same lines the
// unbounded path would. count and files_with_matches emit one line per block,
// so keep is maxResults+1 there. Each is one past the caller's cut, so the
// caller still sees a line beyond maxResults and its own results-truncation note
// fires; a block dropped for the count is that cut, not the byte budget.
//
// budget bounds BOTH what the collector retains and what it buffers pending a
// delimiter. Retention applies path priority to bytes as well as to the block
// count, so a byte overflow evicts the path-largest kept block rather than
// whichever record happened to arrive first; a record no eviction can fit is
// dropped (disclosed). Pending bytes are bounded the same way: an oversized or
// unterminated record is dropped and its remainder skipped, so its own size can
// never outgrow the cap, and the record after it is admitted normally. Every
// drop flows through one disclosure — the retained-output note the native arm
// uses.
type rgRecordCollector struct {
	filesOnly bool
	keep      int
	budget    int
	prefix    []byte // the searched directory's path-separator-terminated prefix

	blocks        map[string][]byte // path -> raw record bytes, arrival order
	retainedBytes int
	dropped       bool
	separated     bool

	cur        string // the path of the last complete record, for "--" grouping
	pendingSep bool

	// skipping is set when an oversized record's head was dropped: the rest of
	// that record is discarded until its delimiter, then parsing resumes with
	// the next record. skipSawNul records that the skipped record's path
	// delimiter (a NUL) has been seen, so the skip ends at the newline after it
	// rather than at an earlier newline inside the pathname.
	skipping   bool
	skipSawNul bool

	buf       []byte
	rawPrefix []byte
}

// rgCollectorDiagnosticBytes bounds the raw stream the collector keeps for a
// FAILED search's diagnostic message: a failed rg's stdout is shown to the model
// alongside its stderr, and that text is not parsed records. 64 KiB is far past
// any real rg diagnostic; the parsed records are the search's answer.
const rgCollectorDiagnosticBytes = 64 << 10

func newRgRecordCollector(dir string, filesOnly bool, keep, budget int) *rgRecordCollector {
	if keep < 1 {
		keep = 1
	}
	return &rgRecordCollector{
		filesOnly: filesOnly,
		keep:      keep,
		budget:    budget,
		prefix:    []byte(strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)),
		blocks:    map[string][]byte{},
	}
}

// rgCollectorKeep is the number of ripgrep record blocks the collector retains.
func rgCollectorKeep(outputMode string, maxResults, ctxLines int) int {
	if outputMode == "count" || outputMode == "files_with_matches" {
		return maxResults + 1
	}
	return maxResults + ctxLines + 1
}

// Write consumes the stream, parsing complete records as they arrive. It always
// reports a full write: a full pipe would stall the child.
func (c *rgRecordCollector) Write(p []byte) (int, error) {
	n := len(p)
	if room := rgCollectorDiagnosticBytes - len(c.rawPrefix); room > 0 {
		c.rawPrefix = append(c.rawPrefix, p[:min(len(p), room)]...)
	}
	if c.skipping {
		p = c.skipOversized(p)
	}
	if len(p) > 0 {
		c.buf = append(c.buf, p...)
		c.drain()
	}
	return n, nil
}

// drain consumes every complete unit in c.buf, then bounds what remains. It
// discriminates exactly as ripgrepOutputLines does — cut at the first NUL, and a
// record's path is everything before it (newlines included), admitted only when
// it starts with the searched directory's prefix. Any other bytes are a pathless
// line: the "--" group separator, a continuation of the current block, or —
// when a fake rg frames none of the stream — the whole output as one block.
func (c *rgRecordCollector) drain() {
	for c.drainUnit() {
	}
	if c.budget > 0 && len(c.buf) > c.budget {
		// An oversized or unterminated record: its own bytes would otherwise
		// grow the pending buffer without bound, since the budget is only
		// checked after a complete record accumulates. Drop it, disclose, and
		// skip its remainder so the NEXT record is admitted again.
		c.dropped = true
		c.skipSawNul = bytes.IndexByte(c.buf, 0) >= 0
		c.buf = nil
		c.skipping = true
	}
}

// drainUnit consumes one complete unit from c.buf, reporting whether it did. A
// unit it cannot complete stays buffered — including a record whose NUL has not
// arrived, whose pathname may contain newlines, so a newline now is not a
// boundary.
func (c *rgRecordCollector) drainUnit() bool {
	if len(c.buf) == 0 {
		return false
	}
	nul := bytes.IndexByte(c.buf, 0)
	if nul >= 0 && bytes.HasPrefix(c.buf[:nul], c.prefix) {
		if c.filesOnly {
			c.record(string(c.buf[:nul]), c.buf[:nul+1])
			c.buf = c.buf[nul+1:]
			return true
		}
		rest := c.buf[nul+1:]
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			return false
		}
		end := nul + 1 + nl + 1
		c.record(string(c.buf[:nul]), c.buf[:end])
		c.buf = c.buf[end:]
		return true
	}
	// A possible framed record mid-path must wait for its NUL, and a chunk
	// boundary inside the prefix must wait for the rest of it: neither is a
	// pathless line yet.
	if bytes.HasPrefix(c.buf, c.prefix) || bytes.HasPrefix(c.prefix, c.buf) {
		return false
	}
	nl := bytes.IndexByte(c.buf, '\n')
	if nl < 0 {
		return false
	}
	if string(c.buf[:nl]) == "--" {
		c.pendingSep = true
	} else {
		c.record(c.cur, c.buf[:nl+1])
	}
	c.buf = c.buf[nl+1:]
	return true
}

// skipOversized discards the remainder of a record whose head was dropped,
// returning the bytes after its delimiter — the next record's start. The
// delimiter is the newline that ends the record's one-line payload, which
// follows its path's NUL; a dropped head that held no NUL was a pathless line
// (or, in principle, a pathname longer than the whole budget), whose delimiter
// is the next newline.
func (c *rgRecordCollector) skipOversized(p []byte) []byte {
	for len(p) > 0 {
		if c.skipSawNul {
			if i := bytes.IndexByte(p, '\n'); i >= 0 {
				c.skipping = false
				return p[i+1:]
			}
			return nil
		}
		if i := bytes.IndexByte(p, 0); i >= 0 {
			c.skipSawNul = true
			p = p[i+1:]
			continue
		}
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			c.skipping = false
			return p[i+1:]
		}
		return nil
	}
	return nil
}

// record files one complete record under its path, first placing a pending
// group separator: one that precedes the same path's next record stays inside
// that block, one that precedes a different path marks the block separation.
func (c *rgRecordCollector) record(path string, raw []byte) {
	if c.pendingSep {
		if c.cur == path {
			if b, ok := c.blocks[path]; ok && c.retainedBytes+3 <= c.budget {
				c.blocks[path] = append(b, '-', '-', '\n')
				c.retainedBytes += 3
			}
		} else {
			c.separated = true
		}
		c.pendingSep = false
	}
	c.cur = path
	c.appendRaw(path, raw)
}

// appendRaw adds raw to path's block, creating the block if room allows. A new
// path beyond keep evicts the largest kept path when this one is smaller — that
// eviction is the caller's maxResults cut. A record no byte room can be made
// for is dropped and discloses truncation.
func (c *rgRecordCollector) appendRaw(path string, raw []byte) {
	b, ok := c.blocks[path]
	if !ok {
		if len(c.blocks) >= c.keep && !c.makeRoom(path) {
			return
		}
		if !c.makeByteRoom(path, len(raw)) {
			c.dropped = true
			return
		}
		c.blocks[path] = append([]byte(nil), raw...)
		c.retainedBytes += len(raw)
		return
	}
	if !c.makeByteRoom(path, len(raw)) {
		c.dropped = true
		return
	}
	c.blocks[path] = append(b, raw...)
	c.retainedBytes += len(raw)
}

// makeRoom evicts the largest kept path when path sorts before it, reporting
// whether a place was made. The scan is O(keep) and only runs once the map is
// full; keep is maxResults-sized, so it stays cheap.
func (c *rgRecordCollector) makeRoom(path string) bool {
	largest := ""
	for p := range c.blocks {
		if largest == "" || rgPathLess(largest, p) {
			largest = p
		}
	}
	if !rgPathLess(path, largest) {
		return false
	}
	c.retainedBytes -= len(c.blocks[largest])
	delete(c.blocks, largest)
	return true
}

// makeByteRoom evicts the path-largest kept blocks that sort AFTER path until
// need more bytes fit, reporting whether it made room. Retention is
// path-prioritized for bytes as well as for the block count, so a budget
// overflow keeps the path-earlier record whoever arrived first — without this,
// a byte overflow would restore the emission-order dependence the collector
// exists to remove.
func (c *rgRecordCollector) makeByteRoom(path string, need int) bool {
	for c.retainedBytes+need > c.budget {
		evict := ""
		for p := range c.blocks {
			if rgPathLess(path, p) && (evict == "" || rgPathLess(evict, p)) {
				evict = p
			}
		}
		if evict == "" {
			return false
		}
		c.retainedBytes -= len(c.blocks[evict])
		delete(c.blocks, evict)
	}
	return true
}

// retained renders the kept blocks as a ripgrep --null record stream in path
// order, blocks joined by the group separator when the stream carried one, for
// the existing line formatter to decode.
func (c *rgRecordCollector) retained() string {
	paths := make([]string, 0, len(c.blocks))
	for p := range c.blocks {
		paths = append(paths, p)
	}
	slices.SortFunc(paths, rgPathOrder)
	var out []byte
	for i, p := range paths {
		if i > 0 && c.separated {
			out = append(out, '-', '-', '\n')
		}
		out = append(out, c.blocks[p]...)
	}
	return string(out)
}

// truncated reports that a record was left out — by the block count, the byte
// budget, or the oversized-record drain: the caller discloses it with the
// retained-output note.
func (c *rgRecordCollector) truncated() bool { return c.dropped }

// diagnostics is the raw stream prefix a FAILED search shows beside its stderr.
func (c *rgRecordCollector) diagnostics() string { return string(c.rawPrefix) }

// flush consumes the bytes a stream ended on. A well-formed rg stream ends on a
// complete record, leaving nothing; a framed-less stream (a fake rg) ends on a
// final line with no newline, which belongs to its block, and a stream cut
// mid-record leaves a partial record this drops (disclosed as truncation).
func (c *rgRecordCollector) flush() {
	if len(c.buf) == 0 {
		return
	}
	if nul := bytes.IndexByte(c.buf, 0); nul >= 0 && bytes.HasPrefix(c.buf[:nul], c.prefix) {
		c.dropped = true
		c.buf = nil
		return
	}
	c.record(c.cur, c.buf)
	c.buf = nil
}

// rgPathOrder orders two paths the way ripgrepOutputLines sorts its blocks: one
// path segment at a time — split on the platform path separator, so the two
// agree on Windows as well as unix — so a directory's files come before a
// sibling file whose name extends the directory's ("ctx/m.txt" before
// "ctx.txt").
func rgPathOrder(a, b string) int {
	sep := string(filepath.Separator)
	return slices.Compare(strings.Split(a, sep), strings.Split(b, sep))
}

func rgPathLess(a, b string) bool { return rgPathOrder(a, b) < 0 }
