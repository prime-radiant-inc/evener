package linecap

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
)

// JournalCursor is disposable reader state, never a journal write authority.
// Offset includes a partial line already read; Pending carries it until complete.
type JournalCursor struct {
	Offset    int64
	Pending   []byte
	ReadBytes int64
	ReadLines int
	Info      os.FileInfo
}

type JournalLine struct {
	Bytes      []byte
	Terminated bool
	EndOffset  int64
}

// ReadJournalPage reads at most byteLimit bytes and lineLimit records. A long
// record advances the cursor without emitting a line until its bytes arrive.
// Cancellation leaves the caller's cursor unchanged.
func ReadJournalPage(ctx context.Context, path string, cursor *JournalCursor, byteLimit int64, lineLimit, maxLineBytes int) ([]JournalLine, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if byteLimit <= 0 || lineLimit <= 0 || maxLineBytes <= 0 {
		return nil, false, errors.New("linecap: positive page limits required")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	next := JournalCursor{Offset: cursor.Offset, Pending: append([]byte(nil), cursor.Pending...)}
	if _, err := file.Seek(next.Offset, io.SeekStart); err != nil {
		return nil, false, err
	}
	input := &journalPageInput{Reader: io.LimitReader(file, byteLimit)}
	reader := bufio.NewReader(input)
	var lines []JournalLine
	var used int64
	for len(lines) < lineLimit && used < byteLimit {
		line, terminated, consumed, err := ReadLine(ctx, reader, maxLineBytes-len(next.Pending))
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		used += consumed
		next.Offset += consumed
		next.Pending = append(next.Pending, line...)
		if len(next.Pending) > maxLineBytes {
			return nil, false, ErrTooLong
		}
		atEnd := next.Offset >= info.Size()
		if !terminated && !atEnd {
			break
		}
		if len(next.Pending) > 0 || terminated {
			lines = append(lines, JournalLine{Bytes: next.Pending, Terminated: terminated, EndOffset: next.Offset})
			next.Pending = nil
		}
		if atEnd {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	next.ReadBytes = input.readBytes
	next.ReadLines = len(lines)
	next.Info = info
	*cursor = next
	return lines, next.Offset >= info.Size() && len(next.Pending) == 0, nil
}

// journalPageInput accounts for bufio prefetch as well as consumed record bytes.
type journalPageInput struct {
	io.Reader
	readBytes int64
}

func (input *journalPageInput) Read(buffer []byte) (int, error) {
	n, err := input.Reader.Read(buffer)
	input.readBytes += int64(n)
	return n, err
}
