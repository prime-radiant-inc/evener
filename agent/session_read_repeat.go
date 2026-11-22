package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
)

// readStamp identifies the state of a file at the moment it was read.
type readStamp struct {
	size  int64
	mtime time.Time
}

// priorReadSpan is one line range (or one media payload) a session has read,
// with the file's state at that read and the turn of the first read of the
// span in the current, uncompacted window.
type priorReadSpan struct {
	startLine int    // 1-based inclusive; 0 for media reads
	endLine   int    // 1-based inclusive
	pages     string // PDF page selector; "" for non-PDF and whole-document reads
	stamp     readStamp
	// content hashes the bytes the read returned (the image bytes for media,
	// otherwise the recoverable output), so an atomic same-size, same-mtime
	// rewrite still reads as changed when its exact range is read again.
	content     [sha256.Size]byte
	turn        int
	repeatCount int
}

// maxPriorReadSpans caps the spans kept per path; the oldest are dropped.
const maxPriorReadSpans = 32

// readFilePath returns the resolved path a read_file call names, or false when
// the call's arguments do not name a path.
func (s *Session) readFilePath(call []byte) (string, bool) {
	var args struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal(call, &args) != nil {
		return "", false
	}
	if args.FilePath == "" {
		return "", false
	}
	return s.resolveFilePath(args.FilePath), true
}

// statReadStamp stats path on the host filesystem. That is the file the read
// sees only because LocalExecutionEnvironment is the sole ExecutionEnvironment;
// an environment whose files live elsewhere must not reuse this stat.
func statReadStamp(path string) (readStamp, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return readStamp{}, false
	}
	return readStamp{size: info.Size(), mtime: info.ModTime()}, true
}

// sameReadStamp reports whether a and b describe the same file state.
func sameReadStamp(a, b readStamp) bool {
	return a.size == b.size && a.mtime.Equal(b.mtime)
}

// noteReadFileRepeat marks a successful read_file result as unchanged when a
// range the session already read covers the range this read returned from a
// file whose size and mtime still match, then records this read. before is the
// file's stamp taken ahead of the read, so a write racing the read invalidates
// the next compare instead of being hidden by it.
//
// A covered read always keeps its full content: the note is a hint that the
// content is already in context, never a refusal to return it. After three
// repeats of the same span the note escalates to name the repeat count.
func (s *Session) noteReadFileRepeat(path string, before readStamp, res *tool.ExecResult) {
	// A past-EOF notice and an unnumbered tail read report no absolute line
	// range, and a clamped read withheld parts of long lines that a follow-up
	// limit=1 read must be free to fetch without a false repeat hint.
	if res.ReadPastEOF || res.ReadUnnumberedTail || res.ReadClamped {
		return
	}
	s.mu.Lock()
	turn := s.totalRounds
	s.mu.Unlock()

	nowHash := sha256.Sum256([]byte(res.RecoverableOutput))
	if len(res.ImageData) > 0 {
		// The recoverable output of an image read is only its short header;
		// the image bytes are what an unchanged re-read must match.
		nowHash = sha256.Sum256(res.ImageData)
	}

	s.readFilesMu.Lock()
	if s.priorReads == nil {
		s.priorReads = map[string][]priorReadSpan{}
	}
	spans := s.priorReads[path]
	// A different size or mtime means the file changed on disk: every span
	// recorded against the old state is stale and is dropped.
	for _, span := range spans {
		if !sameReadStamp(span.stamp, before) {
			spans = nil
			break
		}
	}
	for i := range spans {
		span := &spans[i]
		textRead := res.ReadStartLine > 0 && res.ReadEndLine >= res.ReadStartLine
		covered := false
		if textRead {
			covered = span.startLine > 0 && span.startLine <= res.ReadStartLine && span.endLine >= res.ReadEndLine &&
				(span.startLine != res.ReadStartLine || span.endLine != res.ReadEndLine || span.content == nowHash)
		} else if res.ReadPages != "" {
			// Sliced PDFs carry fresh trailer IDs per extraction, so matching on
			// the unchanged file stamp plus canonical page range avoids holding
			// a process-global PDF byte cache.
			covered = span.startLine == 0 && span.pages == res.ReadPages
		} else {
			covered = span.startLine == 0 && span.pages == res.ReadPages && span.content == nowHash
		}
		if !covered {
			continue
		}
		span.repeatCount++
		unchangedTurn := span.turn
		res.UnchangedSinceTurn = &unchangedTurn
		note := repeatReadNote(*span, res)
		s.priorReads[path] = spans
		s.readFilesMu.Unlock()
		res.Output += note
		return
	}

	span := priorReadSpan{stamp: before, content: nowHash, turn: turn}
	if res.ReadStartLine > 0 && res.ReadEndLine >= res.ReadStartLine {
		span.startLine, span.endLine = res.ReadStartLine, res.ReadEndLine
		spans = coalescePriorReadSpans(append(spans, span))
	} else {
		span.pages = res.ReadPages
		spans = append(spans, span)
	}
	if len(spans) > maxPriorReadSpans {
		spans = spans[len(spans)-maxPriorReadSpans:]
	}
	s.priorReads[path] = spans
	s.readFilesMu.Unlock()
}

// repeatReadNote is the hint appended to a covered repeat read. The first two
// repeats state what is covered; from the third repeat the note names the
// repeat count and tells the model to use the text it already has.
func repeatReadNote(span priorReadSpan, res *tool.ExecResult) string {
	if span.startLine > 0 && res.ReadEndLine >= res.ReadStartLine {
		if span.repeatCount < 3 {
			return fmt.Sprintf("\n[Unchanged since turn %d: lines %d-%d of this file were already read.]",
				span.turn, res.ReadStartLine, res.ReadEndLine)
		}
		return fmt.Sprintf("\n[Unchanged since turn %d (repeated %d times): lines %d-%d are already in context. Use the text already read rather than re-reading.]",
			span.turn, span.repeatCount, res.ReadStartLine, res.ReadEndLine)
	}
	if span.repeatCount < 3 {
		return fmt.Sprintf("\n[Unchanged since turn %d: this file was already read.]", span.turn)
	}
	return fmt.Sprintf("\n[Unchanged since turn %d (repeated %d times): this file is already in context. Use the version already read rather than re-reading.]",
		span.turn, span.repeatCount)
}

// coalescePriorReadSpans merges overlapping or adjacent text spans into their
// union, keeping the earliest turn. The union of two different reads has no
// single read whose content it matches, so its content hash is cleared; when
// the union is exactly one of the two ranges, that read's hash is kept.
func coalescePriorReadSpans(spans []priorReadSpan) []priorReadSpan {
	if len(spans) == 0 {
		return spans
	}
	// Media spans (startLine == 0) never merge and keep their place.
	textIndexes := make([]int, 0, len(spans))
	for i, span := range spans {
		if span.startLine > 0 {
			textIndexes = append(textIndexes, i)
		}
	}
	sort.SliceStable(textIndexes, func(a, b int) bool {
		x, y := spans[textIndexes[a]], spans[textIndexes[b]]
		if x.startLine != y.startLine {
			return x.startLine < y.startLine
		}
		return x.endLine < y.endLine
	})

	var (
		merged     []priorReadSpan
		groupFirst []int // original index of each merged span's earliest member
		groupOf    = make([]int, len(spans))
	)
	for i := range groupOf {
		groupOf[i] = -1
	}
	for _, idx := range textIndexes {
		span := spans[idx]
		if n := len(merged); n > 0 && merged[n-1].endLine+1 >= span.startLine {
			merged[n-1] = mergePriorReadSpans(merged[n-1], span)
			groupFirst[n-1] = min(groupFirst[n-1], idx)
			groupOf[idx] = n - 1
			continue
		}
		merged = append(merged, span)
		groupFirst = append(groupFirst, idx)
		groupOf[idx] = len(merged) - 1
	}
	// A merged span keeps the record of its earliest member.
	for g, first := range groupFirst {
		merged[g].stamp = spans[first].stamp
		merged[g].pages = spans[first].pages
	}

	// Restore the original interleaving with media spans: each merged span sits
	// where its earliest member was.
	out := make([]priorReadSpan, 0, len(merged)+len(spans)-len(textIndexes))
	for i, span := range spans {
		if span.startLine == 0 {
			out = append(out, span)
		} else if g := groupOf[i]; g >= 0 && groupFirst[g] == i {
			out = append(out, merged[g])
		}
	}
	return out
}

// mergePriorReadSpans merges b into a, keeping the earliest turn, the highest
// repeat count, and a's content only when a's range still covers the union.
func mergePriorReadSpans(a, b priorReadSpan) priorReadSpan {
	m := a
	m.startLine = min(a.startLine, b.startLine)
	m.endLine = max(a.endLine, b.endLine)
	m.turn = min(a.turn, b.turn)
	m.repeatCount = max(a.repeatCount, b.repeatCount)
	switch {
	case m.startLine == b.startLine && m.endLine == b.endLine:
		m.content = b.content
	case m.startLine == a.startLine && m.endLine == a.endLine:
		// a's own read still covers the union.
	default:
		m.content = [sha256.Size]byte{}
	}
	return m
}

// clearPriorReads forgets every range read before the current uncompacted
// window. It deliberately leaves s.readFiles alone: the read-before-write
// guard is about what the session has seen, not what its context still holds.
func (s *Session) clearPriorReads() {
	s.readFilesMu.Lock()
	s.priorReads = nil
	s.readFilesMu.Unlock()
}
