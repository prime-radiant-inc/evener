package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/afero"
)

// A read_file call that names an offset or a limit renders only the lines it
// was asked for, so it scans the file instead of loading it: the bytes before
// the window are counted for newlines and then dropped, and the scan stops at
// the byte that ends the window's last line (or counts remaining newlines to
// EOF on files <= ReadTotalLinesScanCap without scanning post-window bytes for
// NUL). Negative offsets read from the end of the file, seeking backward in
// readWindowChunk steps on large io.ReadSeeker files.

const (
	// readWindowChunk is the buffer a windowed read scans with. Large enough
	// that per-chunk overhead disappears against the disk read, small enough
	// that the memory a windowed read holds does not follow the file's size.
	readWindowChunk = 64 << 10

	// readWindowHeadBytes is how much of the file head a windowed read looks at
	// before deciding the file is an image or a document. Twelve covers the
	// longest magic signature detectImageFormat tests (WebP's RIFF....WEBP).
	readWindowHeadBytes = 12
)

// readWindowWholeBelow is the file size at or below which a bounded read takes
// the whole-file path anyway: below it there is nothing to save, and the
// whole-file path is the one whose NUL refusal covers every byte of the file.
// Above it the binary refusal covers every byte the read scans, which ends with
// the window's last line, so a NUL past the window is not seen and the window
// comes back as text. TestReadFileWindowScopesTheBinaryRefusal pins the decision.
//
// A variable, like MaxWholeFileReadBytes, so a test can force the scanning path
// onto a fixture small enough to compare byte for byte with the whole-file path.
var readWindowWholeBelow int64 = 2 * readWindowChunk

// errReadBinaryWindow reports a NUL byte among the bytes a windowed read
// scanned. The caller turns it into read_file's binary refusal, which names the
// path as the caller spelled it.
var errReadBinaryWindow = errors.New("binary content in the read window")

// errReadBinaryFile is read_file's binary refusal. The message carries the path
// the caller spelled, so the rendered text differs per call; callers that need
// to tell this refusal from the read cap match the sentinel with errors.Is
// rather than the wording.
var errReadBinaryFile = errors.New("binary file (NUL byte)")

// lineWindowRequested reports whether a call asked for a bounded window or a
// negative tail offset. Zero offset/limit values stay whole-file defaults.
func lineWindowRequested(offsetLine, limitLines *int) bool {
	return (offsetLine != nil && *offsetLine != 0) || (limitLines != nil && *limitLines > 0)
}

type readWindowParams struct {
	requestedWindow  bool
	explicitLimit    bool
	singleLineEscape bool
	tailMode         bool
	start            int   // 1-based start line when !tailMode
	tailN            int64 // positive tail line count when tailMode
	effLimit         int
}

func resolveReadWindowParams(offsetLine, limitLines *int) readWindowParams {
	p := readWindowParams{
		requestedWindow:  lineWindowRequested(offsetLine, limitLines),
		explicitLimit:    limitLines != nil && *limitLines > 0,
		singleLineEscape: limitLines != nil && *limitLines == 1,
		start:            1,
		effLimit:         DefaultReadLines,
	}
	if offsetLine != nil && *offsetLine != 0 {
		if *offsetLine < 0 {
			p.tailMode = true
			if *offsetLine == math.MinInt {
				p.tailN = math.MaxInt64
			} else {
				p.tailN = -int64(*offsetLine)
			}
		} else {
			p.start = *offsetLine
		}
	}
	if p.explicitLimit {
		p.effLimit = *limitLines
	}
	return p
}

// tailScanNeedLines returns how many newlines from EOF a negative-offset scan
// must locate. When limit is omitted, the caller can never receive more than
// DefaultReadLines lines, so scanning stops at min(tailN, DefaultReadLines).
func (p readWindowParams) tailScanNeedLines() int64 {
	if p.explicitLimit {
		return p.tailN
	}
	limit := int64(max(DefaultReadLines, 1))
	return min(p.tailN, limit)
}

// clampLineBytes clamps raw to maxRunes via utf8.DecodeRune without modifying
// short or invalid UTF-8 byte sequences under the cap.
func clampLineBytes(raw []byte, maxRunes int) (prefix []byte, clamped bool, totalRunes int) {
	if maxRunes <= 0 || len(raw) <= maxRunes {
		return raw, false, len(raw)
	}
	pos := 0
	cutPos := -1
	runes := 0
	for pos < len(raw) {
		_, size := utf8.DecodeRune(raw[pos:])
		pos += size
		runes++
		if runes == maxRunes {
			cutPos = pos
		}
	}
	if runes <= maxRunes {
		return raw, false, runes
	}
	return raw[:cutPos], true, runes
}

// clampLineString is clampLineBytes over a string without copying short or
// oversized strings before clamping.
func clampLineString(raw string, maxRunes int) (prefix string, clamped bool, totalRunes int) {
	if maxRunes <= 0 || len(raw) <= maxRunes {
		return raw, false, len(raw)
	}
	pos := 0
	cutPos := -1
	runes := 0
	for pos < len(raw) {
		_, size := utf8.DecodeRuneInString(raw[pos:])
		pos += size
		runes++
		if runes == maxRunes {
			cutPos = pos
		}
	}
	if runes <= maxRunes {
		return raw, false, runes
	}
	return raw[:cutPos], true, runes
}

// truncateLineUTF8Bytes slices raw at the last valid rune boundary <= maxBytes.
func truncateLineUTF8Bytes(raw []byte, maxBytes int) []byte {
	if maxBytes <= 0 {
		return nil
	}
	if len(raw) <= maxBytes {
		return raw
	}
	pos := 0
	for pos < len(raw) {
		_, size := utf8.DecodeRune(raw[pos:])
		if pos+size > maxBytes {
			break
		}
		pos += size
	}
	return raw[:pos]
}

// truncateLineUTF8String slices raw at the last valid rune boundary <= maxBytes.
func truncateLineUTF8String(raw string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(raw) <= maxBytes {
		return raw
	}
	pos := 0
	for pos < len(raw) {
		_, size := utf8.DecodeRuneInString(raw[pos:])
		if pos+size > maxBytes {
			break
		}
		pos += size
	}
	return raw[:pos]
}

func digitCount(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		d++
		n /= 10
	}
	return d
}

func rowPrefixLen(lineNum int, unnumbered bool) int {
	if unnumbered {
		return 0
	}
	return digitCount(lineNum) + 1 // digits + ('\t' or ':')
}

type preparedRow struct {
	num        int
	content    []byte // clamped content (or full content when singleLineEscape)
	clamped    bool
	rawLineLen int
	origRaw    []byte // retained only when clamped or when single row might need byte-slicing
}

func prepareWindowRow(lineNum int, unnumbered bool, raw []byte, singleLineEscape bool, maxLineChars int) preparedRow {
	rawLen := len(raw)
	if singleLineEscape {
		return preparedRow{
			num:        lineNum,
			content:    raw,
			rawLineLen: rawLen,
			origRaw:    raw,
		}
	}
	prefix, wasClamped, totalRunes := clampLineBytes(raw, maxLineChars)
	if !wasClamped {
		return preparedRow{
			num:        lineNum,
			content:    raw,
			rawLineLen: rawLen,
			origRaw:    raw,
		}
	}
	var suffix string
	if !unnumbered && lineNum > 0 {
		suffix = fmt.Sprintf("... [line truncated: %d chars total; read with offset=%d limit=1 for full line]", totalRunes, lineNum)
	} else {
		suffix = fmt.Sprintf("... [line truncated: %d chars total]", totalRunes)
	}
	out := make([]byte, len(prefix)+len(suffix))
	copy(out, prefix)
	copy(out[len(prefix):], suffix)
	return preparedRow{
		num:        lineNum,
		content:    out,
		clamped:    true,
		rawLineLen: rawLen,
		origRaw:    raw,
	}
}

func (r preparedRow) renderedLen(unnumbered bool) int {
	return rowPrefixLen(r.num, unnumbered) + len(r.content) + 1
}

type renderedWindow struct {
	body               string
	startLine          int
	endLine            int
	shownCount         int
	clampedLines       int
	stoppedByBytes     bool
	singleLineOverflow bool
	singleLineNum      int
	singleLineRawLen   int
}

const (
	readRegistryBackstopPadding = 2000
	readFooterTrailingReserve   = 256
)

func renderPreparedRows(rows []preparedRow, unnumbered bool, maxBytes int, tailKeepLast bool, displayPath string, headerReserve int) renderedWindow {
	if len(rows) == 0 {
		return renderedWindow{}
	}
	kept := rows
	stoppedByBytes := false
	if tailKeepLast {
		total := 0
		startIdx := len(rows)
		for i := len(rows) - 1; i >= 0; i-- {
			rl := rows[i].renderedLen(unnumbered)
			if i < len(rows)-1 && total+rl > maxBytes {
				stoppedByBytes = true
				break
			}
			total += rl
			startIdx = i
			if i == len(rows)-1 && total > maxBytes {
				break
			}
		}
		kept = rows[startIdx:]
	}

	var singleLineOverflow bool
	var singleLineNum, singleLineRawLen int
	if len(kept) == 1 && kept[0].renderedLen(unnumbered) > maxBytes {
		r := &kept[0]
		raw := r.origRaw
		if raw == nil {
			raw = r.content
		}
		effectiveMax := maxBytes
		if displayPath != "" {
			maxTotal := DefaultReadMaxBytes + readRegistryBackstopPadding
			footerCap := max(256, maxTotal-headerReserve-1024)
			footer := formatSingleLineOverflowFooter(r.num, r.rawLineLen, displayPath, unnumbered, footerCap)
			effectiveMax = min(maxBytes, max(0, maxTotal-headerReserve-len(footer)))
		}
		budget := max(0, effectiveMax-rowPrefixLen(r.num, unnumbered)-1)
		r.content = truncateLineUTF8Bytes(raw, budget)
		r.clamped = false
		stoppedByBytes = true
		singleLineOverflow = true
		singleLineNum = r.num
		singleLineRawLen = r.rawLineLen
	}

	useColon := false
	if !unnumbered {
		for _, r := range kept {
			if len(r.content) > 0 && r.content[0] == '\t' {
				useColon = true
				break
			}
		}
	}
	sep := byte('\t')
	if useColon {
		sep = ':'
	}

	clampedCount := 0
	totalSize := 0
	for _, r := range kept {
		if r.clamped {
			clampedCount++
		}
		totalSize += r.renderedLen(unnumbered)
	}

	var out strings.Builder
	out.Grow(totalSize)
	var numBuf [24]byte
	for _, r := range kept {
		if !unnumbered {
			b := strconv.AppendInt(numBuf[:0], int64(r.num), 10)
			out.Write(b)
			out.WriteByte(sep)
		}
		out.Write(r.content)
		out.WriteByte('\n')
	}

	res := renderedWindow{
		body:               out.String(),
		shownCount:         len(kept),
		clampedLines:       clampedCount,
		stoppedByBytes:     stoppedByBytes,
		singleLineOverflow: singleLineOverflow,
		singleLineNum:      singleLineNum,
		singleLineRawLen:   singleLineRawLen,
	}
	if !unnumbered {
		res.startLine = kept[0].num
		res.endLine = kept[len(kept)-1].num
	}
	return res
}

func boundedFooterPath(path string, maxBytes int) string {
	if path == "" {
		return "file"
	}
	if maxBytes <= 0 || len(path) <= maxBytes {
		return path
	}
	if maxBytes <= 3 {
		return path[len(path)-maxBytes:]
	}
	keep := len(path) - (maxBytes - 3)
	for keep < len(path) && !utf8.RuneStart(path[keep]) {
		keep++
	}
	return "..." + path[keep:]
}

func maxFooterPathBytes() int {
	return min(4096, max(128, DefaultReadMaxBytes/4))
}

func readBodyBudget(displayPath string, unnumbered bool, p readWindowParams) (bodyMaxBytes, headerReserve int) {
	maxTotal := DefaultReadMaxBytes + readRegistryBackstopPadding
	if unnumbered {
		headerReserve = len(formatTailHeader(displayPath, p, max(DefaultReadLines, 1), true, false)) + 16
		budget := min(DefaultReadMaxBytes, max(0, maxTotal-headerReserve-readFooterTrailingReserve))
		return budget, headerReserve
	}
	return DefaultReadMaxBytes, 0
}

func formatTailHeader(displayPath string, p readWindowParams, shownCount int, stoppedByBytes, stoppedByLines bool) string {
	pathLabel := boundedFooterPath(displayPath, maxFooterPathBytes())
	if p.tailMode && p.explicitLimit {
		return fmt.Sprintf("[Tail slice of %s: showing %d lines starting %d lines from end (file >2 MiB, absolute line numbers omitted)]\n",
			pathLabel, shownCount, p.tailN)
	}
	switch {
	case stoppedByBytes:
		return fmt.Sprintf("[Tail of %s: last %d lines (file >2 MiB, %d-byte output cap reached; absolute line numbers omitted)]\n",
			pathLabel, shownCount, DefaultReadMaxBytes)
	case stoppedByLines:
		return fmt.Sprintf("[Tail of %s: last %d lines (file >2 MiB, %d-line limit reached; absolute line numbers omitted)]\n",
			pathLabel, shownCount, DefaultReadLines)
	default:
		return fmt.Sprintf("[Tail of %s: last %d lines (file >2 MiB, absolute line numbers omitted)]\n",
			pathLabel, shownCount)
	}
}

func formatSingleLineOverflowFooter(lineNum, rawLineLen int, displayPath string, unnumbered bool, maxFooterBytes int) string {
	if !unnumbered && lineNum > 0 {
		if displayPath != "" {
			withSed := fmt.Sprintf("\n[Line %d is %d bytes, exceeding the %d-byte single-read cap. To inspect further bytes: sed -n '%dp' %s | head -c %d]",
				lineNum, rawLineLen, DefaultReadMaxBytes, lineNum, ShellEscapeArgs(displayPath), DefaultReadMaxBytes)
			if maxFooterBytes <= 0 || len(withSed) <= maxFooterBytes {
				return withSed
			}
		}
		return fmt.Sprintf("\n[Line %d is %d bytes, exceeding the %d-byte single-read cap.]",
			lineNum, rawLineLen, DefaultReadMaxBytes)
	}
	return fmt.Sprintf("\n[Tail line is %d bytes, exceeding the %d-byte single-read cap.]",
		rawLineLen, DefaultReadMaxBytes)
}

func emptyFileNotice(displayPath string) string {
	if displayPath == "" {
		return "[File is empty (0 bytes).]"
	}
	return fmt.Sprintf("[File %s is empty (0 bytes).]", boundedFooterPath(displayPath, maxFooterPathBytes()))
}

func pastEOFNotice(displayPath string, totalLines, requestedOffset int) string {
	if displayPath == "" {
		return fmt.Sprintf("[File has %d lines; requested offset=%d is past end of file.]", totalLines, requestedOffset)
	}
	return fmt.Sprintf("[File %s has %d lines; requested offset=%d is past end of file.]", boundedFooterPath(displayPath, maxFooterPathBytes()), totalLines, requestedOffset)
}

type readFooterOpts struct {
	displayPath    string
	params         readWindowParams
	totalLines     int // exact total lines when known, 0 when >ReadTotalLinesScanCap and not scanned to EOF
	unnumbered     bool
	moreRemain     bool
	stoppedByBytes bool
	stoppedByLines bool
	headerReserve  int
}

func finalizeReadOutput(rw renderedWindow, opts readFooterOpts) (string, ReadFileMeta) {
	p := opts.params
	stoppedByBytes := opts.stoppedByBytes || rw.stoppedByBytes
	stoppedByLines := opts.stoppedByLines && !stoppedByBytes

	var truncated bool
	var truncatedBy string
	if rw.singleLineOverflow || stoppedByBytes {
		truncated = true
		truncatedBy = "chars"
	} else if stoppedByLines {
		truncated = true
		truncatedBy = "lines"
	}

	meta := ReadFileMeta{
		StartLine:    rw.startLine,
		EndLine:      rw.endLine,
		TotalLines:   opts.totalLines,
		Truncated:    truncated,
		TruncatedBy:  truncatedBy,
		ClampedLines: rw.clampedLines,
		TailMode:     p.tailMode,
	}

	var out strings.Builder
	if opts.unnumbered {
		out.WriteString(formatTailHeader(opts.displayPath, p, rw.shownCount, stoppedByBytes, stoppedByLines))
	}
	out.WriteString(rw.body)

	// Attach trailing footer/notice.
	switch {
	case rw.singleLineOverflow:
		maxTotal := DefaultReadMaxBytes + readRegistryBackstopPadding
		footerCap := max(256, maxTotal-opts.headerReserve-1024)
		out.WriteString(formatSingleLineOverflowFooter(rw.singleLineNum, rw.singleLineRawLen, opts.displayPath, opts.unnumbered, footerCap))
	case opts.unnumbered:
		if p.tailMode && p.explicitLimit && opts.moreRemain {
			rem := max(int64(1), p.tailN-int64(rw.shownCount))
			fmt.Fprintf(&out, "\n[%d more lines remain in tail slice; pass offset=-%d to continue.]", rem, rem)
		}
	case p.tailMode && !p.explicitLimit:
		if stoppedByBytes || stoppedByLines {
			earlierStart := max(1, rw.startLine-rw.shownCount)
			earlierLimit := max(1, rw.startLine-earlierStart)
			reason := fmt.Sprintf("%d-byte cap reached", DefaultReadMaxBytes)
			if stoppedByLines {
				reason = fmt.Sprintf("%d-line limit reached", DefaultReadLines)
			}
			fmt.Fprintf(&out, "\n[Showing last %d lines (lines %d-%d of %d total; %s). Read earlier lines with offset=%d limit=%d.]",
				rw.shownCount, rw.startLine, rw.endLine, opts.totalLines, reason, earlierStart, earlierLimit)
		} else if opts.totalLines > 0 {
			fmt.Fprintf(&out, "\n[Showing lines %d-%d of %d total (end of file).]", rw.startLine, rw.endLine, opts.totalLines)
		}
	case opts.moreRemain:
		nextOffset := rw.endLine + 1
		if opts.totalLines > 0 {
			if stoppedByBytes {
				fmt.Fprintf(&out, "\n[Showing lines %d-%d of %d total (%d-byte output cap reached). Continue with offset=%d.]",
					rw.startLine, rw.endLine, opts.totalLines, DefaultReadMaxBytes, nextOffset)
			} else {
				fmt.Fprintf(&out, "\n[Showing lines %d-%d of %d total. Continue with offset=%d.]",
					rw.startLine, rw.endLine, opts.totalLines, nextOffset)
			}
		} else {
			if stoppedByBytes {
				fmt.Fprintf(&out, "\n[Showing lines %d-%d (%d-byte output cap reached; file >2 MiB not scanned to EOF). Continue with offset=%d.]",
					rw.startLine, rw.endLine, DefaultReadMaxBytes, nextOffset)
			} else {
				fmt.Fprintf(&out, "\n[Showing lines %d-%d (more lines remain; file >2 MiB not scanned to EOF). Continue with offset=%d.]",
					rw.startLine, rw.endLine, nextOffset)
			}
		}
	case p.requestedWindow && opts.totalLines > 0:
		fmt.Fprintf(&out, "\n[Showing lines %d-%d of %d total (end of file).]", rw.startLine, rw.endLine, opts.totalLines)
	}

	if rw.clampedLines > 0 && !rw.singleLineOverflow {
		if opts.unnumbered {
			fmt.Fprintf(&out, "\n[%d line(s) exceeded %d chars and were truncated.]", rw.clampedLines, DefaultReadMaxLineChars)
		} else {
			fmt.Fprintf(&out, "\n[%d line(s) exceeded %d chars and were truncated; pass offset=N limit=1 to read one line uncut.]", rw.clampedLines, DefaultReadMaxLineChars)
		}
	}

	return out.String(), meta
}

func trimTrailingNewlineBytes(b []byte) []byte {
	if bytes.HasSuffix(b, []byte("\r\n")) {
		return b[:len(b)-2]
	}
	return bytes.TrimSuffix(b, []byte{'\n'})
}

func trimTrailingNewlineString(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return s[:len(s)-2]
	}
	return strings.TrimSuffix(s, "\n")
}

// formatNumberLinesBytes is the pure numbered-row formatter behind NumberLinesBytes
// (no file-path footers, no empty-file or unnumbered-tail headers).
func formatNumberLinesBytes(text []byte, offsetLine, limitLines *int) string {
	p := resolveReadWindowParams(offsetLine, limitLines)
	body := trimTrailingNewlineBytes(text)

	if p.tailMode {
		startOffset := backwardTailOffsetBytes(body, p.tailScanNeedLines())
		startLine := 1
		if startOffset > 0 {
			startLine = 1 + bytes.Count(body[:startOffset], []byte{'\n'})
		}
		rows, _, _, _ := collectBytesRows(body[startOffset:], startLine, p.effLimit, p.explicitLimit, p.singleLineEscape, false, DefaultReadMaxBytes)
		rw := renderPreparedRows(rows, false, DefaultReadMaxBytes, !p.explicitLimit, "", 0)
		return rw.body
	}

	offset := 0
	for i := 1; i < p.start; i++ {
		idx := bytes.IndexByte(body[offset:], '\n')
		if idx < 0 {
			return ""
		}
		offset += idx + 1
	}
	rows, _, _, _ := collectBytesRows(body[offset:], p.start, p.effLimit, true, p.singleLineEscape, false, DefaultReadMaxBytes)
	rw := renderPreparedRows(rows, false, DefaultReadMaxBytes, false, "", 0)
	return rw.body
}

func backwardTailOffsetBytes(body []byte, needLines int64) int {
	newlinesFound := int64(0)
	pos := len(body)
	for newlinesFound < needLines {
		idx := bytes.LastIndexByte(body[:pos], '\n')
		if idx < 0 {
			return 0
		}
		newlinesFound++
		if newlinesFound == needLines {
			return idx + 1
		}
		pos = idx
	}
	return 0
}

// formatNumberLinesString is the pure numbered-row formatter behind NumberLines.
// It scans only the window substring without copying the whole string, keeping
// allocations bounded on multi-megabyte inputs.
func formatNumberLinesString(text string, offsetLine, limitLines *int) string {
	p := resolveReadWindowParams(offsetLine, limitLines)
	body := trimTrailingNewlineString(text)

	if p.tailMode {
		needLines := p.tailScanNeedLines()
		newlinesFound := int64(0)
		startOffset := 0
		pos := len(body)
		for newlinesFound < needLines {
			idx := strings.LastIndexByte(body[:pos], '\n')
			if idx < 0 {
				startOffset = 0
				break
			}
			newlinesFound++
			if newlinesFound == needLines {
				startOffset = idx + 1
				break
			}
			pos = idx
		}
		startLine := 1
		if startOffset > 0 {
			startLine = 1 + strings.Count(body[:startOffset], "\n")
		}
		rows := collectStringRows(body[startOffset:], startLine, p.effLimit, p.explicitLimit, p.singleLineEscape, DefaultReadMaxBytes)
		rw := renderPreparedRows(rows, false, DefaultReadMaxBytes, !p.explicitLimit, "", 0)
		return rw.body
	}

	offset := 0
	for i := 1; i < p.start; i++ {
		idx := strings.IndexByte(body[offset:], '\n')
		if idx < 0 {
			return ""
		}
		offset += idx + 1
	}
	rows := collectStringRows(body[offset:], p.start, p.effLimit, true, p.singleLineEscape, DefaultReadMaxBytes)
	rw := renderPreparedRows(rows, false, DefaultReadMaxBytes, false, "", 0)
	return rw.body
}

func collectBytesRows(window []byte, startLine, effLimit int, stopAtMaxBytes, singleLineEscape, unnumbered bool, maxBodyBytes int) (rows []preparedRow, consumed int, moreRemain, stoppedByBytes bool) {
	remaining := effLimit
	lineNumber := startLine
	bodyBytes := 0
	for remaining > 0 {
		idx := bytes.IndexByte(window[consumed:], '\n')
		var line []byte
		var nextConsumed int
		if idx < 0 {
			line = window[consumed:]
			nextConsumed = len(window)
		} else {
			line = window[consumed : consumed+idx]
			nextConsumed = consumed + idx + 1
			line = bytes.TrimSuffix(line, []byte{'\r'})
		}
		row := prepareWindowRow(lineNumber, unnumbered, line, singleLineEscape, DefaultReadMaxLineChars)
		rowLen := row.renderedLen(unnumbered)
		if stopAtMaxBytes && len(rows) > 0 && bodyBytes+rowLen > maxBodyBytes {
			return rows, consumed, true, true
		}
		consumed = nextConsumed
		rows = append(rows, row)
		bodyBytes += rowLen
		if !unnumbered {
			lineNumber++
		}
		remaining--
		if idx < 0 {
			return rows, consumed, false, false
		}
	}
	// remaining == 0 after a newline (idx >= 0): at least one more segment follows,
	// even when consumed == len(window) (a trailing empty line after that newline).
	return rows, consumed, true, false
}

func collectStringRows(window string, startLine, effLimit int, stopAtMaxBytes, singleLineEscape bool, maxBodyBytes int) []preparedRow {
	var rows []preparedRow
	remaining := effLimit
	lineNumber := startLine
	consumed := 0
	bodyBytes := 0
	for remaining > 0 {
		idx := strings.IndexByte(window[consumed:], '\n')
		var lineStr string
		var nextConsumed int
		if idx < 0 {
			lineStr = window[consumed:]
			nextConsumed = len(window)
		} else {
			lineStr = window[consumed : consumed+idx]
			nextConsumed = consumed + idx + 1
			lineStr = strings.TrimSuffix(lineStr, "\r")
		}
		var row preparedRow
		if singleLineEscape {
			rawLen := len(lineStr)
			sliceStr := lineStr
			if len(rows) == 0 && len(sliceStr) > maxBodyBytes {
				sliceStr = truncateLineUTF8String(lineStr, maxBodyBytes)
			}
			row = preparedRow{
				num:        lineNumber,
				content:    []byte(sliceStr),
				rawLineLen: rawLen,
				origRaw:    []byte(sliceStr),
			}
		} else {
			prefix, wasClamped, totalRunes := clampLineString(lineStr, DefaultReadMaxLineChars)
			if !wasClamped {
				b := []byte(prefix)
				row = preparedRow{
					num:        lineNumber,
					content:    b,
					rawLineLen: len(prefix),
					origRaw:    b,
				}
			} else {
				suffix := fmt.Sprintf("... [line truncated: %d chars total; read with offset=%d limit=1 for full line]", totalRunes, lineNumber)
				out := make([]byte, len(prefix)+len(suffix))
				copy(out, prefix)
				copy(out[len(prefix):], suffix)
				var orig []byte
				if len(rows) == 0 {
					orig = []byte(truncateLineUTF8String(lineStr, maxBodyBytes))
				}
				row = preparedRow{
					num:        lineNumber,
					content:    out,
					clamped:    true,
					rawLineLen: len(lineStr),
					origRaw:    orig,
				}
			}
		}
		rowLen := row.renderedLen(false)
		if stopAtMaxBytes && len(rows) > 0 && bodyBytes+rowLen > maxBodyBytes {
			return rows
		}
		consumed = nextConsumed
		rows = append(rows, row)
		bodyBytes += rowLen
		lineNumber++
		remaining--
		if idx < 0 {
			break
		}
	}
	return rows
}

// renderTextReadRich turns the bytes a read loaded into read_file's result plus
// structured ReadFileMeta. Both whole-file reads and small/tail windowed reads
// share this path and finalizeReadOutput, keeping ReadFileCtx and
// ReadFileAndBytesCtx byte-identical at every file size.
func renderTextReadRich(displayPath string, b []byte, offsetLine, limitLines *int) (string, ReadFileMeta, error) {
	// Image files: return base64-encoded data instead of erroring on binary.
	if format := detectImageFormat(displayPath, b); format != "" {
		return fmt.Sprintf("[image: %s, %d bytes, base64 data follows]\n%s", format, len(b), base64.StdEncoding.EncodeToString(b)), ReadFileMeta{}, nil
	}
	// Document files (PDF): return base64-encoded data for vision/content pipeline.
	if format := detectDocumentFormat(displayPath, b); format != "" {
		return fmt.Sprintf("[document: %s, %d bytes, base64 data follows]\n%s", format, len(b), base64.StdEncoding.EncodeToString(b)), ReadFileMeta{}, nil
	}
	// Basic binary detection.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", ReadFileMeta{}, fmt.Errorf("%w: %s", errReadBinaryFile, displayPath)
	}
	if len(b) == 0 {
		return emptyFileNotice(displayPath), ReadFileMeta{}, nil
	}

	p := resolveReadWindowParams(offsetLine, limitLines)
	body := trimTrailingNewlineBytes(b)
	isLarge := int64(len(b)) > ReadTotalLinesScanCap

	if p.tailMode {
		startOffset := backwardTailOffsetBytes(body, p.tailScanNeedLines())
		unnumbered := isLarge && startOffset > 0
		startLine := 0
		totalLines := 0
		if !unnumbered {
			if startOffset == 0 {
				startLine = 1
			} else {
				startLine = 1 + bytes.Count(body[:startOffset], []byte{'\n'})
			}
		}

		window := body[startOffset:]
		bodyMax, headerReserve := readBodyBudget(displayPath, unnumbered, p)
		rows, _, moreRemain, stoppedByBytes := collectBytesRows(window, startLine, p.effLimit, p.explicitLimit, p.singleLineEscape, unnumbered, bodyMax)
		stoppedByLines := false

		if p.explicitLimit {
			if !moreRemain && !unnumbered {
				totalLines = startLine + len(rows) - 1
			} else if !isLarge || startOffset == 0 {
				totalLines = 1 + bytes.Count(body, []byte{'\n'})
			}
		} else {
			if !unnumbered {
				totalLines = startLine + len(rows) - 1
			}
			if startOffset > 0 && p.tailN > int64(DefaultReadLines) {
				stoppedByLines = true
			}
		}

		rw := renderPreparedRows(rows, unnumbered, bodyMax, !p.explicitLimit, displayPath, headerReserve)
		if !p.explicitLimit && rw.stoppedByBytes {
			stoppedByBytes = true
		}
		out, meta := finalizeReadOutput(rw, readFooterOpts{
			displayPath:    displayPath,
			params:         p,
			totalLines:     totalLines,
			unnumbered:     unnumbered,
			moreRemain:     moreRemain,
			stoppedByBytes: stoppedByBytes,
			stoppedByLines: stoppedByLines,
			headerReserve:  headerReserve,
		})
		return out, meta, nil
	}

	// Forward read.
	offset := 0
	for i := 1; i < p.start; i++ {
		idx := bytes.IndexByte(body[offset:], '\n')
		if idx < 0 {
			totalLines := i
			return pastEOFNotice(displayPath, totalLines, p.start), ReadFileMeta{
				TotalLines: totalLines,
				PastEOF:    true,
			}, nil
		}
		offset += idx + 1
	}

	window := body[offset:]
	bodyMax, headerReserve := readBodyBudget(displayPath, false, p)
	rows, consumed, moreRemain, stoppedByBytes := collectBytesRows(window, p.start, p.effLimit, true, p.singleLineEscape, false, bodyMax)
	stoppedByLines := false
	if moreRemain && !stoppedByBytes && !p.explicitLimit {
		stoppedByLines = true
	}

	totalLines := 0
	if !moreRemain {
		totalLines = p.start + len(rows) - 1
	} else if !isLarge {
		remLines := 1 + bytes.Count(window[consumed:], []byte{'\n'})
		totalLines = (p.start + len(rows) - 1) + remLines
	}

	rw := renderPreparedRows(rows, false, bodyMax, false, displayPath, headerReserve)
	out, meta := finalizeReadOutput(rw, readFooterOpts{
		displayPath:    displayPath,
		params:         p,
		totalLines:     totalLines,
		moreRemain:     moreRemain,
		stoppedByBytes: stoppedByBytes,
		stoppedByLines: stoppedByLines,
		headerReserve:  headerReserve,
	})
	return out, meta, nil
}

// readOffArmWindowCtx is read_file's bounded read on the off (not fd-anchored)
// arm: the same open and admission readFileCappedCtx does, handing the admitted
// handle to the window renderer instead of loading the file.
func readOffArmWindowCtx(ctx context.Context, fsys afero.Fs, displayPath, name string, offsetLine, limitLines *int) (string, ReadFileMeta, error) {
	if err := ctx.Err(); err != nil {
		return "", ReadFileMeta{}, err
	}
	f, err := fsys.OpenFile(name, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return "", ReadFileMeta{}, err
	}
	defer func() { _ = f.Close() }()
	info, serr := f.Stat()
	if serr != nil {
		return "", ReadFileMeta{}, serr
	}
	if !info.Mode().IsRegular() {
		return "", ReadFileMeta{}, fmt.Errorf("read %q: %w", name, errNotRegularFile)
	}
	return readNumberedWindowCtx(ctx, displayPath, name, f, info.Size(), offsetLine, limitLines)
}

// readNumberedWindowCtx renders read_file's result for a bounded request from r,
// a handle an arm has already opened and admitted, with the size that handle
// reported.
func readNumberedWindowCtx(ctx context.Context, displayPath, name string, r io.Reader, size int64, offsetLine, limitLines *int) (string, ReadFileMeta, error) {
	// One deadline for the head, the scan and the fallback: a handle that stats
	// regular but blocks in read(2) is bounded here exactly as the whole-file
	// reads bound it.
	deadline := readDeadlineOf(r)
	if deadline != nil {
		defer armStreamDeadline(ctx, deadline)()
	}
	head := make([]byte, readWindowHeadBytes)
	n, herr := io.ReadFull(r, head)
	head = head[:n]
	if herr != nil && !errors.Is(herr, io.EOF) && !errors.Is(herr, io.ErrUnexpectedEOF) {
		return "", ReadFileMeta{}, readWindowErr(ctx, name, herr)
	}
	p := resolveReadWindowParams(offsetLine, limitLines)
	// Small files, images, documents, and negative offsets on <=ReadTotalLinesScanCap
	// files take the whole-file path so NUL checks, exact total lines, and media
	// responses share renderTextReadRich.
	if size <= readWindowWholeBelow || detectImageFormat(displayPath, head) != "" || detectDocumentFormat(displayPath, head) != "" || (p.tailMode && size <= ReadTotalLinesScanCap) {
		if size > MaxWholeFileReadBytes {
			return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		b, over, err := readBounded(ctx, io.MultiReader(bytes.NewReader(head), r), nil, MaxWholeFileReadBytes)
		if err != nil {
			return "", ReadFileMeta{}, readWindowErr(ctx, name, err)
		}
		if over {
			return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		return renderTextReadRich(displayPath, b, offsetLine, limitLines)
	}

	if p.tailMode {
		if rs, ok := r.(io.ReadSeeker); ok {
			out, meta, err := scanTailWindowSeekerRich(ctx, rs, size, displayPath, offsetLine, limitLines)
			if err != nil {
				if errors.Is(err, errReadBinaryWindow) {
					return "", ReadFileMeta{}, fmt.Errorf("%w: %s", errReadBinaryFile, displayPath)
				}
				return "", ReadFileMeta{}, readWindowErr(ctx, name, err)
			}
			return out, meta, nil
		}
		if size > MaxWholeFileReadBytes {
			return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		b, over, err := readBounded(ctx, io.MultiReader(bytes.NewReader(head), r), nil, MaxWholeFileReadBytes)
		if err != nil {
			return "", ReadFileMeta{}, readWindowErr(ctx, name, err)
		}
		if over {
			return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		return renderTextReadRich(displayPath, b, offsetLine, limitLines)
	}

	out, meta, err := scanLineWindowRich(ctx, r, size, displayPath, head, offsetLine, limitLines)
	if err != nil {
		if errors.Is(err, errReadBinaryWindow) {
			return "", ReadFileMeta{}, fmt.Errorf("%w: %s", errReadBinaryFile, displayPath)
		}
		return "", ReadFileMeta{}, readWindowErr(ctx, name, err)
	}
	return out, meta, nil
}

// scanTailWindowSeekerRich executes negative-offset tail reads on >ReadTotalLinesScanCap
// seekable files without loading the whole file or fabricating 1-based line numbers
// when byte 0 is not reached.
func scanTailWindowSeekerRich(ctx context.Context, rs io.ReadSeeker, size int64, displayPath string, offsetLine, limitLines *int) (string, ReadFileMeta, error) {
	if err := ctx.Err(); err != nil {
		return "", ReadFileMeta{}, err
	}
	if size == 0 {
		return emptyFileNotice(displayPath), ReadFileMeta{}, nil
	}
	p := resolveReadWindowParams(offsetLine, limitLines)
	needLines := p.tailScanNeedLines()

	chunkStart := max(int64(0), size-readWindowChunk)
	lastChunk := make([]byte, size-chunkStart)
	if _, err := rs.Seek(chunkStart, io.SeekStart); err != nil {
		return "", ReadFileMeta{}, err
	}
	if _, err := io.ReadFull(rs, lastChunk); err != nil {
		return "", ReadFileMeta{}, err
	}
	totalScanned := int64(len(lastChunk))
	if totalScanned > MaxWholeFileReadBytes {
		return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
	}
	if bytes.IndexByte(lastChunk, 0) >= 0 {
		return "", ReadFileMeta{}, errReadBinaryWindow
	}

	logicalEnd := size
	if len(lastChunk) >= 2 && lastChunk[len(lastChunk)-2] == '\r' && lastChunk[len(lastChunk)-1] == '\n' {
		logicalEnd -= 2
		lastChunk = lastChunk[:len(lastChunk)-2]
	} else if len(lastChunk) >= 1 && lastChunk[len(lastChunk)-1] == '\n' {
		logicalEnd -= 1
		lastChunk = lastChunk[:len(lastChunk)-1]
	}

	newlinesFound := int64(0)
	startOffset := int64(-1)
	scanBackward := func(cStart int64, buf []byte) bool {
		pos := len(buf)
		for pos > 0 {
			idx := bytes.LastIndexByte(buf[:pos], '\n')
			if idx < 0 {
				return false
			}
			newlinesFound++
			if newlinesFound == needLines {
				startOffset = cStart + int64(idx) + 1
				return true
			}
			pos = idx
		}
		return false
	}

	found := scanBackward(chunkStart, lastChunk)
	curStart := chunkStart
	var reusableBuf []byte
	if !found && curStart > 0 {
		reusableBuf = make([]byte, readWindowChunk)
	}
	for !found && curStart > 0 {
		if err := ctx.Err(); err != nil {
			return "", ReadFileMeta{}, err
		}
		nextStart := max(int64(0), curStart-readWindowChunk)
		readLen := int(curStart - nextStart)
		if totalScanned+int64(readLen) > MaxWholeFileReadBytes {
			return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		if _, err := rs.Seek(nextStart, io.SeekStart); err != nil {
			return "", ReadFileMeta{}, err
		}
		prevChunk := reusableBuf[:readLen]
		if _, err := io.ReadFull(rs, prevChunk); err != nil {
			return "", ReadFileMeta{}, err
		}
		totalScanned += int64(readLen)
		if bytes.IndexByte(prevChunk, 0) >= 0 {
			return "", ReadFileMeta{}, errReadBinaryWindow
		}
		found = scanBackward(nextStart, prevChunk)
		curStart = nextStart
	}
	if !found {
		startOffset = 0
	}

	unnumbered := startOffset > 0
	startLine := 0
	totalLines := 0
	if !unnumbered {
		startLine = 1
		totalLines = int(newlinesFound + 1)
	}

	bodyMax, headerReserve := readBodyBudget(displayPath, unnumbered, p)

	var tailReader io.Reader
	if startOffset >= chunkStart {
		tailReader = bytes.NewReader(lastChunk[startOffset-chunkStart:])
	} else {
		if _, err := rs.Seek(startOffset, io.SeekStart); err != nil {
			return "", ReadFileMeta{}, err
		}
		tailReader = io.LimitReader(rs, logicalEnd-startOffset)
	}

	// Stream lines forward from [startOffset, logicalEnd).
	var (
		rows           []preparedRow
		lineNum        = startLine
		keptBytes      = 0
		stoppedByBytes = false
		stoppedByLines = false
		moreRemain     = false
	)
	readBuf := make([]byte, readWindowChunk)
	var pending []byte
	eof := false
	empties := 0
	refill := func() (bool, error) {
		for {
			if eof {
				return false, nil
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			n, err := tailReader.Read(readBuf)
			if n > 0 {
				pending = readBuf[:n]
				empties = 0
				if errors.Is(err, io.EOF) {
					eof = true
				} else if err != nil {
					return false, err
				}
				return true, nil
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					eof = true
					return false, nil
				}
				return false, err
			}
			empties++
			if empties > 100 {
				return false, io.ErrNoProgress
			}
		}
	}

	var curLine []byte
	emitted := 0
	for {
		if p.explicitLimit && emitted >= p.effLimit {
			if len(pending) > 0 {
				moreRemain = true
			} else if !eof {
				ok, err := refill()
				if err != nil {
					return "", ReadFileMeta{}, err
				}
				moreRemain = ok
			}
			break
		}
		i := bytes.IndexByte(pending, '\n')
		var lineReady bool
		if i < 0 {
			curLine = append(curLine, pending...)
			if int64(keptBytes)+int64(len(curLine)) > MaxWholeFileReadBytes {
				return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
			}
			pending = nil
			ok, err := refill()
			if err != nil {
				return "", ReadFileMeta{}, err
			}
			if !ok {
				lineReady = true
				eof = true
			}
		} else {
			curLine = append(curLine, pending[:i]...)
			if int64(keptBytes)+int64(len(curLine)) > MaxWholeFileReadBytes {
				return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
			}
			pending = pending[i+1:]
			if n := len(curLine); n > 0 && curLine[n-1] == '\r' {
				curLine = curLine[:n-1]
			}
			lineReady = true
		}
		if !lineReady {
			continue
		}

		// Copy raw bytes before curLine is reused.
		rawCopy := make([]byte, len(curLine))
		copy(rawCopy, curLine)
		curLine = curLine[:0]

		row := prepareWindowRow(lineNum, unnumbered, rawCopy, p.singleLineEscape, DefaultReadMaxLineChars)
		rl := row.renderedLen(unnumbered)
		if p.explicitLimit {
			if len(rows) > 0 && keptBytes+rl > bodyMax {
				stoppedByBytes = true
				moreRemain = true
				break
			}
			rows = append(rows, row)
			keptBytes += rl
		} else {
			rows = append(rows, row)
			keptBytes += rl
			for len(rows) > 1 && keptBytes > bodyMax {
				dropped := rows[0].renderedLen(unnumbered)
				rows = rows[1:]
				keptBytes -= dropped
				stoppedByBytes = true
			}
			if cap(rows) > 2*len(rows)+64 && len(rows) > 0 {
				compacted := make([]preparedRow, len(rows))
				copy(compacted, rows)
				rows = compacted
			}
		}
		if !unnumbered {
			lineNum++
		}
		emitted++
		if eof && len(pending) == 0 {
			break
		}
	}

	if !p.explicitLimit && unnumbered && p.tailN > int64(DefaultReadLines) {
		stoppedByLines = true
	}

	rw := renderPreparedRows(rows, unnumbered, bodyMax, false, displayPath, headerReserve)
	out, meta := finalizeReadOutput(rw, readFooterOpts{
		displayPath:    displayPath,
		params:         p,
		totalLines:     totalLines,
		unnumbered:     unnumbered,
		moreRemain:     moreRemain,
		stoppedByBytes: stoppedByBytes,
		stoppedByLines: stoppedByLines,
		headerReserve:  headerReserve,
	})
	return out, meta, nil
}

// scanLineWindowRich renders the numbered line window (with notices/footers and
// ReadFileMeta) for the bytes r yields after head. On files <= ReadTotalLinesScanCap
// when stopping before EOF, it counts remaining newlines to EOF without checking
// post-window bytes for NUL.
func scanLineWindowRich(ctx context.Context, r io.Reader, size int64, displayPath string, head []byte, offsetLine, limitLines *int) (string, ReadFileMeta, error) {
	if err := ctx.Err(); err != nil {
		return "", ReadFileMeta{}, err
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", ReadFileMeta{}, errReadBinaryWindow
	}
	p := resolveReadWindowParams(offsetLine, limitLines)
	bodyMax, headerReserve := readBodyBudget(displayPath, false, p)
	pending := head
	buf := make([]byte, readWindowChunk)
	empties := 0
	eof := false
	// readErr is an error a Read returned alongside its final bytes. Surface it
	// on the next refill rather than dropping it and reporting a clean EOF.
	var readErr error
	refill := func() (bool, error) {
		for {
			if readErr != nil {
				err := readErr
				readErr = nil
				return false, err
			}
			if eof {
				return false, nil
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			n, err := r.Read(buf)
			if n > 0 {
				pending = buf[:n]
				empties = 0
				switch {
				case err == nil:
				case errors.Is(err, io.EOF):
					eof = true
				default:
					readErr = err
				}
				return true, nil
			}
			if err != nil {
				if err == io.EOF {
					eof = true
					return false, nil
				}
				return false, err
			}
			empties++
			if empties > 100 {
				return false, io.ErrNoProgress
			}
		}
	}

	if len(pending) == 0 {
		ok, err := refill()
		if err != nil {
			return "", ReadFileMeta{}, err
		}
		if !ok {
			return emptyFileNotice(displayPath), ReadFileMeta{}, nil
		}
	}

	// Skip to the window's first line.
	sawTrailingBytes := false
	for skipped := 0; skipped < p.start-1; {
		i := bytes.IndexByte(pending, '\n')
		if i < 0 {
			if bytes.IndexByte(pending, 0) >= 0 {
				return "", ReadFileMeta{}, errReadBinaryWindow
			}
			if len(pending) > 0 {
				sawTrailingBytes = true
			}
			pending = nil
			ok, err := refill()
			if err != nil {
				return "", ReadFileMeta{}, err
			}
			if !ok {
				totalLines := skipped
				if sawTrailingBytes {
					totalLines++
				}
				return pastEOFNotice(displayPath, totalLines, p.start), ReadFileMeta{
					TotalLines: totalLines,
					PastEOF:    true,
				}, nil
			}
			continue
		}
		if bytes.IndexByte(pending[:i+1], 0) >= 0 {
			return "", ReadFileMeta{}, errReadBinaryWindow
		}
		pending = pending[i+1:]
		skipped++
		sawTrailingBytes = false
	}

	if len(pending) == 0 {
		ok, err := refill()
		if err != nil {
			return "", ReadFileMeta{}, err
		}
		if !ok {
			totalLines := p.start - 1
			return pastEOFNotice(displayPath, totalLines, p.start), ReadFileMeta{
				TotalLines: totalLines,
				PastEOF:    true,
			}, nil
		}
	}

	var (
		rows                 []preparedRow
		line                 []byte
		bodyBytes            int
		moreRemain           bool
		stoppedByBytes       bool
		stoppedByLines       bool
		postWindowLineOffset int // 1 if overflow line was already consumed up to its terminator
		postWindowInLine     bool
	)
	overWholeCap := func() bool {
		return int64(bodyBytes)+int64(len(line)) > MaxWholeFileReadBytes
	}

	for emitted := 0; emitted < p.effLimit; emitted++ {
		i := bytes.IndexByte(pending, '\n')
		if i < 0 {
			if bytes.IndexByte(pending, 0) >= 0 {
				return "", ReadFileMeta{}, errReadBinaryWindow
			}
			line = append(line, pending...)
			if overWholeCap() {
				return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
			}
			pending = nil

			// Early abort if we already have >=1 line and the partial line alone
			// cannot possibly fit within DefaultReadMaxBytes even after clamping.
			if emitted > 0 {
				remainingBudget := bodyMax - bodyBytes - rowPrefixLen(p.start+emitted, false) - 1
				if p.singleLineEscape && len(line) > remainingBudget+1 {
					stoppedByBytes = true
					moreRemain = true
					postWindowInLine = true
					break
				}
				if !p.singleLineEscape && len(line) > max(remainingBudget+1, DefaultReadMaxLineChars*4+64) {
					probe := prepareWindowRow(p.start+emitted, false, line, false, DefaultReadMaxLineChars)
					if probe.clamped && probe.renderedLen(false) > bodyMax-bodyBytes {
						stoppedByBytes = true
						moreRemain = true
						postWindowInLine = true
						break
					}
					if !probe.clamped && len(line) > remainingBudget+1 {
						stoppedByBytes = true
						moreRemain = true
						postWindowInLine = true
						break
					}
				}
			}

			ok, err := refill()
			if err != nil {
				return "", ReadFileMeta{}, err
			}
			if !ok {
				if len(line) > 0 || (emitted == 0 && p.start == 1) {
					rawCopy := make([]byte, len(line))
					copy(rawCopy, line)
					row := prepareWindowRow(p.start+emitted, false, rawCopy, p.singleLineEscape, DefaultReadMaxLineChars)
					rl := row.renderedLen(false)
					if len(rows) > 0 && bodyBytes+rl > bodyMax {
						stoppedByBytes = true
						moreRemain = true
						postWindowLineOffset = 1
					} else {
						rows = append(rows, row)
						bodyBytes += rl
					}
				}
				eof = true
				pending = nil
				break
			}
			emitted--
			continue
		}

		if bytes.IndexByte(pending[:i+1], 0) >= 0 {
			return "", ReadFileMeta{}, errReadBinaryWindow
		}
		line = append(line, pending[:i]...)
		if overWholeCap() {
			return "", ReadFileMeta{}, &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		pending = pending[i+1:]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		rawCopy := make([]byte, len(line))
		copy(rawCopy, line)
		line = line[:0]

		row := prepareWindowRow(p.start+emitted, false, rawCopy, p.singleLineEscape, DefaultReadMaxLineChars)
		rl := row.renderedLen(false)
		if len(rows) > 0 && bodyBytes+rl > bodyMax {
			stoppedByBytes = true
			moreRemain = true
			postWindowLineOffset = 1
			break
		}
		rows = append(rows, row)
		bodyBytes += rl
	}

	if !moreRemain && len(rows) == p.effLimit {
		if len(pending) > 0 {
			moreRemain = true
		} else if !eof {
			ok, err := refill()
			if err != nil {
				return "", ReadFileMeta{}, err
			}
			moreRemain = ok
		}
		if moreRemain && !p.explicitLimit {
			stoppedByLines = true
		}
	}

	totalLines := 0
	if !moreRemain {
		totalLines = p.start + len(rows) - 1
	} else if size <= ReadTotalLinesScanCap {
		// Count remaining lines to EOF WITHOUT checking post-window bytes for NUL
		// (preserves TestReadFileWindowScopesTheBinaryRefusal).
		remLines := postWindowLineOffset
		if postWindowLineOffset > 0 && len(pending) == 0 && eof {
			// Overflow line was already at EOF.
		} else {
			rem, err := countRemainingLines(ctx, &pending, refill, postWindowInLine)
			if err != nil {
				return "", ReadFileMeta{}, err
			}
			remLines += rem
		}
		totalLines = (p.start + len(rows) - 1) + remLines
	}

	rw := renderPreparedRows(rows, false, bodyMax, false, displayPath, headerReserve)
	out, meta := finalizeReadOutput(rw, readFooterOpts{
		displayPath:    displayPath,
		params:         p,
		totalLines:     totalLines,
		moreRemain:     moreRemain,
		stoppedByBytes: stoppedByBytes,
		stoppedByLines: stoppedByLines,
		headerReserve:  headerReserve,
	})
	return out, meta, nil
}

// countRemainingLines counts lines in pending + remaining refills without
// inspecting bytes for NUL (so post-window binary bytes do not retroactively
// fail a bounded read on files <= ReadTotalLinesScanCap).
func countRemainingLines(ctx context.Context, pending *[]byte, refill func() (bool, error), inLine bool) (int, error) {
	count := 0
	sawBytes := inLine
	for {
		cur := *pending
		if len(cur) > 0 {
			sawBytes = true
			pos := 0
			for {
				i := bytes.IndexByte(cur[pos:], '\n')
				if i < 0 {
					break
				}
				count++
				pos += i + 1
				sawBytes = false
			}
			if pos < len(cur) {
				sawBytes = true
			}
			*pending = nil
		}
		ok, err := refill()
		if err != nil {
			return 0, err
		}
		if !ok {
			if sawBytes {
				count++
			}
			return count, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
}

// readDeadlineOf is the handle-level read-deadline seam of r, if it has one: an
// OS-backed handle on a pollable mount does, an in-memory test handle does not.
func readDeadlineOf(r io.Reader) readDeadlineSetter {
	if d, ok := r.(readDeadlineSetter); ok {
		return d
	}
	return nil
}

// readWindowErr maps a windowed read's failure to read_file's error shapes: a
// cancelled call reports its cancellation, a read deadline that fired with a
// live context is the shared stall error the whole-file reads report, and
// everything else passes through.
func readWindowErr(ctx context.Context, name string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("read %q: %w", name, errGrepStreamStalled)
	}
	return err
}

// readFileWindowedCtx is LocalExecutionEnvironment's bounded read_file entry:
// the target resolution and missing-file hint ReadFileAndBytesCtx applies, over
// an arm that renders the window instead of loading the file.
func (e *LocalExecutionEnvironment) readFileWindowedCtx(ctx context.Context, path string, offsetLine, limitLines *int) (string, ReadFileMeta, error) {
	t, err := e.fileTarget("read_file", path, targetRead)
	if err != nil {
		return "", ReadFileMeta{}, err
	}
	defer t.release()
	var out string
	var meta ReadFileMeta
	if t.sfs != nil {
		out, meta, err = t.sfs.readNumberedWindowCtx(ctx, "read_file", t.abs, path, offsetLine, limitLines)
	} else {
		out, meta, err = readOffArmWindowCtx(ctx, e.filesystem(), path, t.abs, offsetLine, limitLines)
	}
	if err != nil {
		return "", ReadFileMeta{}, e.mapReadFileErr(t.sfs, t.abs, err)
	}
	return out, meta, nil
}

// mapReadFileErr adds read_file's "did you mean" hint to a genuine missing-file
// miss (never to a permission error or a directory-not-a-file error). It is
// skipped when sandboxed: the diagnostic listing walks plain os.Stat/os.ReadDir
// outside the confined resolver, and could name a masked or denied directory's
// contents that the sandbox would otherwise keep the model from seeing.
func (e *LocalExecutionEnvironment) mapReadFileErr(sfs *sandboxFS, abs string, err error) error {
	if sfs == nil && errors.Is(err, fs.ErrNotExist) {
		if suggestion := readFileNotFoundSuggestion(abs); suggestion != "" {
			return fmt.Errorf("%w. %s", err, suggestion)
		}
	}
	return err
}
