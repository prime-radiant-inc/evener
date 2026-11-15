package linecap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestJournalPageResumesLargeLineWithinByteBudget(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal")
	input := strings.Repeat("x", 71) + "\nlast\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	var cursor JournalCursor
	var lines []string
	for range 20 {
		before := cursor.Offset
		page, done, err := ReadJournalPage(context.Background(), path, &cursor, 17, 2, 100)
		if err != nil {
			t.Fatal(err)
		}
		if delta := cursor.Offset - before; delta > 17 {
			t.Fatalf("read %d bytes, budget17", delta)
		}
		for _, line := range page {
			lines = append(lines, string(line.Bytes))
		}
		if done {
			break
		}
		if cursor.Offset <= before {
			t.Fatal("budget continuation did not advance")
		}
	}
	if len(lines) != 2 || lines[0] != strings.Repeat("x", 71) || lines[1] != "last" {
		t.Fatalf("lines=%v", lines)
	}
}

func TestJournalPageCancellationKeepsPosition(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cursor := JournalCursor{Offset: 8}
	if _, _, err := ReadJournalPage(ctx, "unused", &cursor, 10, 2, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if cursor.Offset != 8 {
		t.Fatalf("position=%d", cursor.Offset)
	}
}

func TestReadJournalPageRecordsAbsoluteEnds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(path, []byte("one\ntwo\nlast"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cursor JournalCursor
	var ends []int64
	for {
		lines, complete, err := ReadJournalPage(t.Context(), path, &cursor, 3, 2, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			ends = append(ends, line.EndOffset)
		}
		if complete {
			break
		}
	}
	if !reflect.DeepEqual(ends, []int64{4, 8, 12}) {
		t.Fatalf("absolute record boundaries=%v", ends)
	}
}

func TestReadJournalPageAccountsForPrefetch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal")
	if err := os.WriteFile(path, []byte("a\n"+strings.Repeat("b", 6000)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cursor JournalCursor
	lines, _, err := ReadJournalPage(t.Context(), path, &cursor, 5000, 1, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || cursor.Offset != 2 || cursor.ReadBytes <= cursor.Offset || cursor.ReadBytes > 5000 {
		t.Fatalf("prefetch accounting offset=%d read=%d", cursor.Offset, cursor.ReadBytes)
	}
}

// TestReadJournalPageLargeBufferReadsFewTimes pins the page reader's read
// granularity: one underlying read per 64 KiB, not one per bufio's 4 KiB
// default.
// Not parallel: overrides the page open seam.
func TestReadJournalPageLargeBufferReadsFewTimes(t *testing.T) {
	origOpen := openJournalPageFile
	var reads int
	openJournalPageFile = func(path string) (pageFile, error) {
		file, err := origOpen(path)
		if err != nil {
			return nil, err
		}
		return &countingPageFile{pageFile: file, reads: &reads}, nil
	}
	t.Cleanup(func() { openJournalPageFile = origOpen })

	path := filepath.Join(t.TempDir(), "journal")
	const lineCount = 2000
	content := strings.Repeat(strings.Repeat("x", 79)+"\n", lineCount)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	var cursor JournalCursor
	page, done, err := ReadJournalPage(context.Background(), path, &cursor, 1<<20, lineCount, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != lineCount || !done {
		t.Fatalf("page lines = %d, done = %v — the page must read the whole journal", len(page), done)
	}
	if cursor.Offset != info.Size() {
		t.Fatalf("cursor offset = %d, want %d", cursor.Offset, info.Size())
	}
	wantReads := int((info.Size() + 64*1024 - 1) / (64 * 1024))
	// The slack covers the EOF probe that ends every page read plus at most
	// one fill stranded by a line boundary; a 4 KiB regression still pays
	// dozens of reads over this bound.
	if reads > wantReads+2 {
		t.Fatalf("page read the %d byte journal with %d underlying reads; a 64 KiB buffer pays about %d", info.Size(), reads, wantReads)
	}
}

// countingPageFile counts the underlying reads a page issues, so a test can
// pin a buffer's read granularity without timing anything.
type countingPageFile struct {
	pageFile
	reads *int
}

func (c *countingPageFile) Read(p []byte) (int, error) {
	*c.reads++
	return c.pageFile.Read(p)
}
