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
// order. (The sort key matches ripgrepOutputLines' path ordering, so the
// retained blocks are the ones that order would put first.)
//
// keep counts BLOCKS, not output lines: in content mode a match's context
// window rides with its path, so keeping maxResults+contextLines+1 blocks is
// enough for the caller's maxResults-LINE cut to select the same lines the
// unbounded path would. count and files_with_matches emit one line per block,
// so keep is maxResults there.
//
// budget caps the total retained bytes as a backstop for a pathological single
// record (one line as long as the whole budget). When a block or record cannot
// be retained, truncation is disclosed with the same retained-output note the
// native arm uses.
type rgRecordCollector struct {
	filesOnly bool
	oneFile   bool
	keep      int
	budget    int
	prefix    string // the searched directory's slash-terminated prefix

	blocks        map[string][]byte // path -> raw record bytes, arrival order
	retainedBytes int
	dropped       bool
	separated     bool

	cur        string // the path of the last complete record, for "--" grouping
	pendingSep bool

	buf []byte
}

func newRgRecordCollector(dir string, filesOnly, oneFile bool, keep, budget int) *rgRecordCollector {
	if keep < 1 {
		keep = 1
	}
	return &rgRecordCollector{
		filesOnly: filesOnly,
		oneFile:   oneFile,
		keep:      keep,
		budget:    budget,
		prefix:    strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator),
		blocks:    map[string][]byte{},
	}
}

// rgCollectorKeep is the number of ripgrep record blocks the collector retains:
// one output line per block in count and files_with_matches mode, and in content
// mode enough blocks to cover the caller's maxResults-LINE cut plus the context
// window a match's block can carry. Each is one past the cut, so the caller sees
// a line beyond maxResults and its own results-truncation note fires — the
// block-count drop is that cut, not the byte budget.
func rgCollectorKeep(outputMode string, maxResults, ctxLines int) int {
	if outputMode == "count" || outputMode == "files_with_matches" {
		return maxResults + 1
	}
	return maxResults + ctxLines + 1
}

// Write consumes the stream, parsing complete records as they arrive. It always
// reports a full write: a full pipe would stall the child.
func (c *rgRecordCollector) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	c.drain()
	return len(p), nil
}

// drain consumes every complete record in c.buf, leaving an incomplete trailing
// record buffered for the next Write. A record's shape is ripgrep's --null one:
// files_with_matches is `<path>\0`, content and count are `<path>\0<line>\n`,
// and a bare `--` line separates context groups. A bare line that is not `--`
// (a pathless continuation) belongs to the current block, as ripgrepOutputLines
// attaches it.
func (c *rgRecordCollector) drain() {
	for len(c.buf) > 0 {
		if c.oneFile {
			// A single named file's stream is plain newline-terminated lines.
			nl := bytes.IndexByte(c.buf, '\n')
			if nl < 0 {
				return
			}
			c.appendRaw("", c.buf[:nl+1])
			c.buf = c.buf[nl+1:]
			continue
		}
		// ripgrepOutputLines cuts at the NUL first: a record's path is
		// everything before it (newlines included), and only bytes whose first
		// NUL follows a newline are a pathless line like the "--" separator.
		nul := bytes.IndexByte(c.buf, 0)
		if nul < 0 {
			return
		}
		if path := c.buf[:nul]; !bytes.HasPrefix(path, []byte(c.prefix)) {
			nl := bytes.IndexByte(c.buf, '\n')
			if nl < 0 {
				return
			}
			if string(c.buf[:nl]) == "--" {
				c.pendingSep = true
			} else if c.cur != "" {
				c.appendRaw(c.cur, c.buf[:nl+1])
			}
			c.buf = c.buf[nl+1:]
			continue
		}
		if c.filesOnly {
			c.record(string(c.buf[:nul]), c.buf[:nul+1])
			c.buf = c.buf[nul+1:]
			continue
		}
		nl2 := bytes.IndexByte(c.buf[nul+1:], '\n')
		if nl2 < 0 {
			return
		}
		end := nul + 1 + nl2 + 1
		c.record(string(c.buf[:nul]), c.buf[:end])
		c.buf = c.buf[end:]
	}
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
// path beyond keep evicts the largest kept path when this one is smaller; a
// record past the byte budget is dropped. Either drop discloses truncation.
func (c *rgRecordCollector) appendRaw(path string, raw []byte) {
	b, ok := c.blocks[path]
	if !ok {
		if len(c.blocks) >= c.keep {
			if !c.makeRoom(path) {
				// A block beyond keep is the caller's maxResults cut, not the
				// byte budget: keep is one past the cut, so its own
				// results-truncation note still fires.
				return
			}
		}
		if c.retainedBytes+len(raw) > c.budget {
			c.dropped = true
			return
		}
		c.blocks[path] = append([]byte(nil), raw...)
		c.retainedBytes += len(raw)
		return
	}
	if c.retainedBytes+len(raw) > c.budget {
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

// retained renders the kept blocks as a ripgrep --null record stream in path
// order, blocks joined by the group separator when the stream carried one, for
// the existing line formatter to decode.
func (c *rgRecordCollector) retained() string {
	if c.oneFile {
		return string(c.blocks[""])
	}
	paths := make([]string, 0, len(c.blocks))
	for p := range c.blocks {
		paths = append(paths, p)
	}
	slices.SortFunc(paths, func(a, b string) int { return rgPathOrder(a, b) })
	var out []byte
	for i, p := range paths {
		if i > 0 && c.separated {
			out = append(out, '-', '-', '\n')
		}
		out = append(out, c.blocks[p]...)
	}
	return string(out)
}

// truncated reports that some record was left out: the caller discloses it with
// the retained-output note.
func (c *rgRecordCollector) truncated() bool { return c.dropped }

// rgPathOrder orders two paths the way ripgrepOutputLines sorts its blocks: one
// path segment at a time, so a directory's files come before a sibling file
// whose name extends the directory's ("ctx/m.txt" before "ctx.txt").
func rgPathOrder(a, b string) int {
	return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
}

func rgPathLess(a, b string) bool { return rgPathOrder(a, b) < 0 }
