package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/afero"
)

// A read_file call that names an offset or a limit renders only the lines it
// was asked for, so it scans the file instead of loading it: the bytes before
// the window are counted for newlines and then dropped, and the scan stops at
// the byte that ends the window's last line. Everything about the window —
// numbering, CRLF normalization, what a missing final newline means, bytes that
// are not UTF-8 — is NumberLinesBytes' contract; this file holds the same
// contract without holding the file.
//
// What it does not do is seek. read_file's offset is a LINE number and a file
// carries no index from a line number to a byte offset, so scanning the prefix
// for newlines is the price of answering "line N" exactly. That price is
// O(bytes before line N): nothing for the common offset=1 read, and the whole
// file for a read of its last lines.

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
// comes back as text. That narrowing is the issue's open question, decided here
// as window-scoped: the alternative is reading every byte of a large file to
// answer a 20-line question, which is the cost this change exists to remove.
// TestReadFileWindowScopesTheBinaryRefusal pins the decision.
//
// A variable, like MaxWholeFileReadBytes, so a test can force the scanning path
// onto a fixture small enough to compare byte for byte with the whole-file path.
var readWindowWholeBelow int64 = 2 * readWindowChunk

// errReadBinaryWindow reports a NUL byte among the bytes a windowed read
// scanned. The caller turns it into read_file's binary refusal, which names the
// path as the caller spelled it.
var errReadBinaryWindow = errors.New("binary content in the read window")

// lineWindowRequested reports whether a call asked for a bounded window, as
// NumberLines reads the arguments: a non-positive value means "unset", so
// offset=0 or limit=0 is a whole-file read.
func lineWindowRequested(offsetLine, limitLines *int) bool {
	return (offsetLine != nil && *offsetLine > 0) || (limitLines != nil && *limitLines > 0)
}

// lineWindowBounds resolves the line window exactly as NumberLines does: a
// non-positive offset starts at line 1, a non-positive limit means
// DefaultReadLines.
func lineWindowBounds(offsetLine, limitLines *int) (start, limit int) {
	start = 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit = DefaultReadLines
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	return start, limit
}

// renderTextRead turns the bytes a read loaded into read_file's result for
// them: base64 text for an image or a document, the binary refusal for a NUL
// byte, otherwise the numbered window over the whole of b. Both read entries
// render through here, so a windowed read and a whole-file read of the same
// file answer identically.
func renderTextRead(path string, b []byte, offsetLine, limitLines *int) (string, error) {
	// Image files: return base64-encoded data instead of erroring on binary.
	if format := detectImageFormat(path, b); format != "" {
		return fmt.Sprintf("[image: %s, %d bytes, base64 data follows]\n%s", format, len(b), base64.StdEncoding.EncodeToString(b)), nil
	}
	// Document files (PDF): return base64-encoded data for vision/content pipeline.
	if format := detectDocumentFormat(path, b); format != "" {
		return fmt.Sprintf("[document: %s, %d bytes, base64 data follows]\n%s", format, len(b), base64.StdEncoding.EncodeToString(b)), nil
	}
	// Basic binary detection.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("binary file (NUL byte): %s", path)
	}
	return NumberLinesBytes(b, offsetLine, limitLines), nil
}

// readOffArmWindowCtx is read_file's bounded read on the off (not fd-anchored)
// arm: the same open and admission readFileCappedCtx does, handing the admitted
// handle to the window renderer instead of loading the file.
func readOffArmWindowCtx(ctx context.Context, fsys afero.Fs, displayPath, name string, offsetLine, limitLines *int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := fsys.OpenFile(name, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, serr := f.Stat()
	if serr != nil {
		return "", serr
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("read %q: %w", name, errNotRegularFile)
	}
	return readNumberedWindowCtx(ctx, displayPath, name, f, info.Size(), offsetLine, limitLines)
}

// readNumberedWindowCtx renders read_file's result for a bounded request from r,
// a handle an arm has already opened and admitted, with the size that handle
// reported.
//
// A small file, an image and a document take the whole-file path unchanged: a
// partial image is not something a caller can use, and below readWindowWholeBelow
// there is nothing to save. Everything else is scanned only as far as the
// window's last line — including a file past MaxWholeFileReadBytes, which a
// windowed read can answer because it never holds the whole file. The cap
// applies to what the read does hold, the window itself, in scanLineWindow.
func readNumberedWindowCtx(ctx context.Context, displayPath, name string, r io.Reader, size int64, offsetLine, limitLines *int) (string, error) {
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
		return "", readWindowErr(name, herr)
	}
	// Only the head decides whether the whole file is needed. renderTextRead
	// decides again over the bytes a whole-file read returns, so a file that
	// changes underneath this call answers exactly as a whole-file read of the
	// new content would.
	if size <= readWindowWholeBelow || detectImageFormat(displayPath, head) != "" || detectDocumentFormat(displayPath, head) != "" {
		// An image or document past the cap refuses at its stat size, before a
		// byte is read, exactly as the whole-file entry refuses it.
		if size > MaxWholeFileReadBytes {
			return "", &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		// readBounded gets a nil deadline: the handle's was armed above, and one
		// handle takes one watcher per call.
		b, over, err := readBounded(ctx, io.MultiReader(bytes.NewReader(head), r), nil, MaxWholeFileReadBytes)
		if err != nil {
			return "", readWindowErr(name, err)
		}
		if over {
			return "", &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		return renderTextRead(displayPath, b, offsetLine, limitLines)
	}
	start, limit := lineWindowBounds(offsetLine, limitLines)
	text, err := scanLineWindow(ctx, r, head, start, limit)
	if err != nil {
		if errors.Is(err, errReadBinaryWindow) {
			return "", fmt.Errorf("binary file (NUL byte): %s", displayPath)
		}
		return "", readWindowErr(name, err)
	}
	return text, nil
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
// read deadline that fired is the shared stall error the whole-file reads
// report, everything else passes through.
func readWindowErr(name string, err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("read %q: %w", name, errGrepStreamStalled)
	}
	return err
}

// scanLineWindow renders the numbered line window NumberLinesBytes would produce
// for the bytes r yields after head, reading no further than the byte that ends
// the window's last line.
//
// It holds NumberLinesBytes' contract exactly: the same lines under the same
// numbers, CRLF normalized on every newline-terminated line, a final
// unterminated line kept as it stands (a CR there is content, not half a
// terminator), and a trailing newline read as the last line's terminator rather
// than as the start of an empty one. Bytes that are not UTF-8 pass through
// untouched; nothing here decodes text.
func scanLineWindow(ctx context.Context, r io.Reader, head []byte, start, limit int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", errReadBinaryWindow
	}
	pending := head
	buf := make([]byte, readWindowChunk)
	empties := 0
	eof := false
	// refill replaces pending with the next chunk of the file. It reports false
	// at EOF. It does NOT judge the chunk's bytes: a chunk can extend past the
	// window's last line, and those bytes are neither part of the answer nor
	// part of what the read claims to have looked at. The scan checks the bytes
	// it consumes, at the point it consumes them.
	refill := func() (bool, error) {
		for {
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
				// A read may hand back data and an error together. The data is
				// good, and the error's consequence is EOF from then on.
				eof = err != nil
				return true, nil
			}
			if err != nil {
				if err == io.EOF {
					eof = true
					return false, nil
				}
				return false, err
			}
			// A handle that yields nothing and no error would spin here.
			empties++
			if empties > 100 {
				return false, io.ErrNoProgress
			}
		}
	}

	// Skip to the window's first line. A line number is a count of newlines, so
	// these bytes are scanned and dropped, never accumulated.
	for skipped := 0; skipped < start-1; skipped++ {
		i := bytes.IndexByte(pending, '\n')
		if i < 0 {
			// Every byte of this chunk belongs to the skipped prefix.
			if bytes.IndexByte(pending, 0) >= 0 {
				return "", errReadBinaryWindow
			}
			ok, err := refill()
			if err != nil {
				return "", err
			}
			if !ok {
				return "", nil // the file ends before the first requested line
			}
			skipped--
			continue
		}
		if bytes.IndexByte(pending[:i+1], 0) >= 0 {
			return "", errReadBinaryWindow
		}
		pending = pending[i+1:]
	}

	var (
		out    strings.Builder
		line   []byte
		held   int64
		number = start
	)
	// The window is what a caller sees, so the whole-file cap bounds it as the
	// cap bounded a whole-file read: a handle whose stat size understates what it
	// yields (a procfs special, a stream) is refused rather than accumulated.
	for emitted := 0; emitted < limit; emitted++ {
		i := bytes.IndexByte(pending, '\n')
		if i < 0 {
			if bytes.IndexByte(pending, 0) >= 0 {
				return "", errReadBinaryWindow
			}
			line = append(line, pending...)
			held += int64(len(pending))
			if held > MaxWholeFileReadBytes {
				return "", &ReadLimitError{Limit: MaxWholeFileReadBytes}
			}
			pending = nil
			ok, err := refill()
			if err != nil {
				return "", err
			}
			if !ok {
				// EOF. An empty tail means the newline before it closed the
				// final line, so it is not a row of its own; an unterminated
				// tail is the file's last line, and an empty file is one empty
				// first line.
				if len(line) > 0 || (emitted == 0 && number == 1) {
					fmt.Fprintf(&out, "%4d\t%s\n", number+emitted, line)
				}
				return out.String(), nil
			}
			emitted-- // this chunk ended no line; rescan it with the next
			continue
		}
		if bytes.IndexByte(pending[:i+1], 0) >= 0 {
			return "", errReadBinaryWindow
		}
		line = append(line, pending[:i]...)
		held += int64(i)
		if held > MaxWholeFileReadBytes {
			return "", &ReadLimitError{Limit: MaxWholeFileReadBytes}
		}
		pending = pending[i+1:]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		fmt.Fprintf(&out, "%4d\t%s\n", number+emitted, line)
		line = line[:0]
	}
	return out.String(), nil
}

// readFileWindowedCtx is LocalExecutionEnvironment's bounded read_file entry:
// the target resolution and missing-file hint ReadFileAndBytesCtx applies, over
// an arm that renders the window instead of loading the file.
func (e *LocalExecutionEnvironment) readFileWindowedCtx(ctx context.Context, path string, offsetLine, limitLines *int) (string, error) {
	t, err := e.fileTarget("read_file", path, targetRead)
	if err != nil {
		return "", err
	}
	defer t.release()
	var out string
	if t.sfs != nil {
		out, err = t.sfs.readNumberedWindowCtx(ctx, "read_file", t.abs, path, offsetLine, limitLines)
	} else {
		out, err = readOffArmWindowCtx(ctx, e.filesystem(), path, t.abs, offsetLine, limitLines)
	}
	if err != nil {
		return "", e.mapReadFileErr(t.sfs, t.abs, err)
	}
	return out, nil
}

// mapReadFileErr adds read_file's "did you mean" hint to a genuine missing-file
// miss (never to a permission error or a directory-not-a-file error). It is
// skipped when sandboxed: the diagnostic listing walks plain os.Stat/os.ReadDir
// outside the confined resolver, and could name a masked or denied directory's
// contents that the sandbox would otherwise keep the model from seeing.
// Off/unsandboxed is the default mode, so this still covers the common case;
// sandboxed sessions just get the raw ENOENT.
func (e *LocalExecutionEnvironment) mapReadFileErr(sfs *sandboxFS, abs string, err error) error {
	if sfs == nil && errors.Is(err, fs.ErrNotExist) {
		if suggestion := readFileNotFoundSuggestion(abs); suggestion != "" {
			return fmt.Errorf("%w. %s", err, suggestion)
		}
	}
	return err
}
