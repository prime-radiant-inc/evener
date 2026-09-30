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
