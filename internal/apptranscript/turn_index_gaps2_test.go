package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestLatestFromFileLimitZero covers the limit<=0 full-read path in
// LatestFromFile (lines 171-177).
func TestLatestFromFileLimitZero(t *testing.T) {
	path := writeNumberedTranscript(t, 4)
	cache := NewTurnCache()
	turns, cursor := requireLatestFromFile(t, cache, path, testMaxLineBytes, 0, boundedTestProjector)
	if cursor != "" {
		t.Fatalf("limit=0 cursor = %q, want empty", cursor)
	}
	if len(turns) == 0 {
		t.Fatalf("limit=0 should return all turns, got none")
	}
}

// TestLatestFromFileLimitNegative covers the negative-limit full-read path.
func TestLatestFromFileLimitNegative(t *testing.T) {
	path := writeNumberedTranscript(t, 3)
	cache := NewTurnCache()
	turns, cursor := requireLatestFromFile(t, cache, path, testMaxLineBytes, -1, boundedTestProjector)
	if cursor != "" {
		t.Fatalf("limit=-1 cursor = %q, want empty", cursor)
	}
	if len(turns) == 0 {
		t.Fatalf("limit=-1 should return all turns, got none")
	}
}

// TestLatestFromFileMissingFile covers the error path where the transcript
// file does not exist (loadTurnIndex returns an error).
func TestLatestFromFileMissingFile(t *testing.T) {
	cache := NewTurnCache()
	_, _, err := cache.LatestFromFile(filepath.Join(t.TempDir(), "missing.transcript.jsonl"), testMaxLineBytes, 5, boundedTestProjector)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestPageFromFileLimitZero covers the limit<=0 full-read path in PageFromFile
// (lines 202-208).
func TestPageFromFileLimitZero(t *testing.T) {
	path := writeNumberedTranscript(t, 4)
	cache := NewTurnCache()
	page := requirePageFromFile(t, cache, path, testMaxLineBytes, "", 0, boundedTestProjector)
	if len(page.Turns) == 0 {
		t.Fatalf("limit=0 should return all turns, got none")
	}
}

// TestPageFromFileLimitNegative covers the negative-limit full-read path.
func TestPageFromFileLimitNegative(t *testing.T) {
	path := writeNumberedTranscript(t, 3)
	cache := NewTurnCache()
	page := requirePageFromFile(t, cache, path, testMaxLineBytes, "", -1, boundedTestProjector)
	if len(page.Turns) == 0 {
		t.Fatalf("limit=-1 should return all turns, got none")
	}
}

// TestPageFromFileCursorExceedsCount covers the path where the parsed cursor
// exceeds the logical turn count (line 220-222).
func TestPageFromFileCursorExceedsCount(t *testing.T) {
	path := writeNumberedTranscript(t, 3)
	cache := NewTurnCache()
	// First load the index so it's cached, then request a cursor beyond count.
	_ = requirePageFromFile(t, cache, path, testMaxLineBytes, "", 2, boundedTestProjector)
	page := requirePageFromFile(t, cache, path, testMaxLineBytes, "999", 2, boundedTestProjector)
	// cursor clamped to count, so we should get the last 2 turns
	if len(page.Turns) != 2 {
		t.Fatalf("cursor=999 should clamp to count, got %d turns", len(page.Turns))
	}
}

// TestPageFromFileCursorNegative covers the hi<0 clamping path (lines 223-225).
func TestPageFromFileCursorNegative(t *testing.T) {
	path := writeNumberedTranscript(t, 3)
	cache := NewTurnCache()
	_ = requirePageFromFile(t, cache, path, testMaxLineBytes, "", 2, boundedTestProjector)
	// Negative cursor clamps to 0, so lo=max(0-2,0)=0, hi=0, range is empty.
	// This covers the hi<0 clamp path without error.
	page, err := cache.PageFromFile(path, testMaxLineBytes, "-5", 2, boundedTestProjector)
	if err != nil {
		t.Fatalf("negative cursor should not error, got %v", err)
	}
	if len(page.Turns) != 0 {
		t.Fatalf("negative cursor clamps to 0, should return 0 turns, got %d", len(page.Turns))
	}
}

// TestPageFromFileNonNumericCursor covers the path where the cursor is
// non-numeric and Atoi fails (cursor stays as logicalTurnCount).
func TestPageFromFileNonNumericCursor(t *testing.T) {
	path := writeNumberedTranscript(t, 5)
	cache := NewTurnCache()
	_ = requirePageFromFile(t, cache, path, testMaxLineBytes, "", 3, boundedTestProjector)
	page := requirePageFromFile(t, cache, path, testMaxLineBytes, "abc", 2, boundedTestProjector)
	// Non-numeric cursor keeps hi=count, so we get the last 2
	if len(page.Turns) != 2 {
		t.Fatalf("non-numeric cursor should keep hi=count, got %d turns", len(page.Turns))
	}
}

// TestPageFromFileMissingFile covers the error path for a missing file.
func TestPageFromFileMissingFile(t *testing.T) {
	cache := NewTurnCache()
	_, err := cache.PageFromFile(filepath.Join(t.TempDir(), "missing.transcript.jsonl"), testMaxLineBytes, "", 5, boundedTestProjector)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestPageFromFileNextCursorNonZero covers the path where lo>0 producing a
// non-empty NextCursor (line 228-229).
func TestPageFromFileNextCursorNonZero(t *testing.T) {
	path := writeNumberedTranscript(t, 10)
	cache := NewTurnCache()
	page := requirePageFromFile(t, cache, path, testMaxLineBytes, "", 3, boundedTestProjector)
	if page.NextCursor == "" {
		t.Fatalf("expected non-empty NextCursor for 10 turns with limit 3")
	}
}

// TestScanTurnIndexBlankLine covers the blank-line handling in scanTurnIndex
// (lines 661-665): a line that is all whitespace is skipped while advancing
// offset and updating prefix stamp.
func TestScanTurnIndexBlankLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank.transcript.jsonl")
	header := transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: "blank"}
	headerLine, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	entry := transcript.Entry{Kind: "entry", Seq: 1, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("hi"))}
	entryLine, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	// header + blank line + entry
	data := append(headerLine, '\n')
	data = append(data, '\n') // blank line
	data = append(data, entryLine...)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	turns := requireTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	if len(turns) == 0 {
		t.Fatalf("expected turns from transcript with blank line")
	}
}

// TestUsableTurnIndexBadRecordKind covers the validation failure where
// record.Kind != "entry" (line 599-600).
func TestUsableTurnIndexBadRecordKind(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		IntegrityStamp:          turnIndexIntegrityStamp(turnIndexDisk{}),
		TranscriptSize:          100,
		CompleteSize:            100,
		Records: []indexedTurn{
			{Offset: 0, Length: 10, Index: 1, Kind: "garbage", Visible: true, VisibleIndex: 1},
		},
		VisibleRecords: 1,
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	data := make([]byte, 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, start, _ := usableTurnIndex(f, 100, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for bad record kind, got %d", start)
	}
	if got.Version != 0 {
		t.Fatalf("expected zero disk for invalid index")
	}
}

// TestUsableTurnIndexBadVisibleCount covers the validation failure where
// visibleRecords != candidate.VisibleRecords (line 615-616).
func TestUsableTurnIndexBadVisibleCount(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          100,
		CompleteSize:            100,
		Records: []indexedTurn{
			{Offset: 0, Length: 10, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1},
		},
		VisibleRecords: 5, // mismatch
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	data := make([]byte, 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 100, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for bad visible count, got %d", start)
	}
}

// TestUsableTurnIndexBadVisibleIndex covers the validation failure where
// record.VisibleIndex != visibleRecords (line 608-609).
func TestUsableTurnIndexBadVisibleIndex(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          100,
		CompleteSize:            100,
		Records: []indexedTurn{
			{Offset: 0, Length: 10, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 99},
		},
		VisibleRecords: 1,
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	data := make([]byte, 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 100, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for bad visible index, got %d", start)
	}
}

// TestUsableTurnIndexBadOffset covers the validation failure where
// record.Offset < previousEnd (line 596-597).
func TestUsableTurnIndexBadOffset(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          100,
		CompleteSize:            100,
		Records: []indexedTurn{
			{Offset: 0, Length: 50, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1},
			{Offset: 10, Length: 20, Index: 2, Kind: "entry", Visible: false, VisibleIndex: 1}, // offset < previousEnd
		},
		VisibleRecords: 1,
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	data := make([]byte, 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 100, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for bad offset, got %d", start)
	}
}

// TestUsableTurnIndexBadIndex covers the validation failure where
// record.Index != previousIndex+1 (line 596-597).
func TestUsableTurnIndexBadIndex(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          100,
		CompleteSize:            100,
		Records: []indexedTurn{
			{Offset: 0, Length: 50, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1},
			{Offset: 50, Length: 20, Index: 5, Kind: "entry", Visible: false, VisibleIndex: 1}, // index != 2
		},
		VisibleRecords: 1,
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	data := make([]byte, 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 100, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for bad index, got %d", start)
	}
}

// TestUsableTurnIndexBadToolSeed covers the validation failure where
// validToolSeed returns false (line 602-603).
func TestUsableTurnIndexBadToolSeed(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          100,
		CompleteSize:            100,
		Records: []indexedTurn{
			{Offset: 0, Length: 50, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1,
				ToolSeed:    map[string]string{"badid": "wrong"},
				ToolChanges: []toolNameChange{{ID: "badid", Name: "wrong", Lookup: true}}},
		},
		VisibleRecords: 1,
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	data := make([]byte, 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 100, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for bad tool seed, got %d", start)
	}
}

// TestUsableTurnIndexZeroLength covers the validation failure where
// record.Length <= 0 (line 596).
func TestUsableTurnIndexZeroLength(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          100,
		CompleteSize:            100,
		Records: []indexedTurn{
			{Offset: 0, Length: 0, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1},
		},
		VisibleRecords: 1,
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	data := make([]byte, 100)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 100, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for zero length, got %d", start)
	}
}

// TestUsableTurnIndexTrustedMemory covers the trustedMemory early-return path
// (lines 587-589).
func TestUsableTurnIndexTrustedMemory(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          0,
		CompleteSize:            0,
		Records:                 []indexedTurn{},
		VisibleRecords:          0,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.transcript.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, start, _ := usableTurnIndex(f, 0, testMaxLineBytes, index.ProjectionID, &index, true, false, true, nil)
	if start != 0 {
		t.Fatalf("trusted memory should return start=CompleteSize=0, got %d", start)
	}
	if got.Version != turnIndexVersion {
		t.Fatalf("trusted memory should return the candidate, got version %d", got.Version)
	}
}

// TestUsableTurnIndexAppendOnlyAnchorsMismatch covers the path where
// appendOnly is true but anchors don't match (line 577-578).
func TestUsableTurnIndexAppendOnlyAnchorsMismatch(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          5,
		CompleteSize:            5,
		Records: []indexedTurn{
			{Offset: 0, Length: 5, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1},
		},
		VisibleRecords: 1,
		FirstAnchor:    turnIndexAnchor{Offset: 0, Length: 5, Stamp: "deadbeef"},
		TailAnchor:     turnIndexAnchor{Offset: 0, Length: 5, Stamp: "deadbeef"},
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "anchors.transcript.jsonl")
	// Write 10 bytes so appendOnly is true (sameFile check requires fileIdentity)
	data := []byte("0123456789")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// appendOnly=true, anchors won't match (wrong stamp)
	_, start, _ := usableTurnIndex(f, 10, testMaxLineBytes, index.ProjectionID, &index, false, true, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for anchor mismatch, got %d", start)
	}
}

// TestUsableTurnIndexTranscriptSizeTooLarge covers the path where
// candidate.TranscriptSize > size (line 573-574).
func TestUsableTurnIndexTranscriptSizeTooLarge(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          200, // larger than actual file
		CompleteSize:            200,
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "small.transcript.jsonl")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 5, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for transcript size too large, got %d", start)
	}
}

// TestUsableTurnIndexCompleteSizeNegative covers the path where
// candidate.CompleteSize < 0 (line 573-574).
func TestUsableTurnIndexCompleteSizeNegative(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          5,
		CompleteSize:            -1, // negative
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "small.transcript.jsonl")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 5, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for negative complete size, got %d", start)
	}
}

// TestUsableTurnIndexCompleteExceedsTranscript covers the path where
// candidate.CompleteSize > candidate.TranscriptSize (line 573-574).
func TestUsableTurnIndexCompleteExceedsTranscript(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		TranscriptSize:          5,
		CompleteSize:            10, // > TranscriptSize
	}
	index.IntegrityStamp = turnIndexIntegrityStamp(index)
	dir := t.TempDir()
	path := filepath.Join(dir, "small.transcript.jsonl")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 5, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for complete>transcript, got %d", start)
	}
}

// TestUsableTurnIndexBadIntegrityStamp covers the path where the integrity
// stamp doesn't match (line 570-571) for non-trusted-memory indices.
func TestUsableTurnIndexBadIntegrityStamp(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
		IntegrityStamp:          "wrong-stamp", // doesn't match
		TranscriptSize:          5,
		CompleteSize:            5,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 5, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for bad integrity stamp, got %d", start)
	}
}

// TestUsableTurnIndexVersionMismatch covers the path where candidate.Version
// doesn't match turnIndexVersion (line 567).
func TestUsableTurnIndexVersionMismatch(t *testing.T) {
	index := turnIndexDisk{
		Version:                 turnIndexVersion + 1, // wrong version
		TranscriptFormatVersion: transcript.FormatVersion,
		MaxLineBytes:            testMaxLineBytes,
		ProjectionID:            projectionIdentity(boundedTestProjector),
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.transcript.jsonl")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, start, _ := usableTurnIndex(f, 1, testMaxLineBytes, index.ProjectionID, &index, false, false, false, nil)
	if start != -1 {
		t.Fatalf("expected start=-1 for version mismatch, got %d", start)
	}
}

// TestFileIdentityWindowsStruct covers the Windows-style file identity path
// using a mock struct with VolumeSerialNumber/FileIndexHigh/FileIndexLow
// fields (lines 1283-1303).
func TestFileIdentityWindowsStruct(t *testing.T) {
	type winStat struct {
		VolumeSerialNumber uint64
		FileIndexHigh      uint64
		FileIndexLow       uint64
	}
	info := mockFileInfo{sys: winStat{VolumeSerialNumber: 42, FileIndexHigh: 7, FileIndexLow: 3}}
	got := fileIdentity(info)
	if !strings.HasPrefix(got, "volume:") {
		t.Fatalf("expected 'volume:' prefix for Windows file identity, got %q", got)
	}
}

// TestFileIdentityWindowsShortNames covers the alternative Windows field names
// vol/idxhi/idxlo (lines 1291-1299).
func TestFileIdentityWindowsShortNames(t *testing.T) {
	type winStat struct {
		vol   uint64
		idxhi uint64
		idxlo uint64
	}
	info := mockFileInfo{sys: winStat{vol: 99, idxhi: 1, idxlo: 2}}
	got := fileIdentity(info)
	if !strings.HasPrefix(got, "volume:99:") {
		t.Fatalf("expected 'volume:99:' prefix for Windows short names, got %q", got)
	}
}

// TestFileIdentityDevIno covers the Unix dev/ino path (lines 1283-1285).
func TestFileIdentityDevIno(t *testing.T) {
	type unixStat struct {
		Dev uint64
		Ino uint64
	}
	info := mockFileInfo{sys: unixStat{Dev: 10, Ino: 20}}
	got := fileIdentity(info)
	want := "dev:10:ino:20"
	if got != want {
		t.Fatalf("fileIdentity with Dev/Ino = %q, want %q", got, want)
	}
}

// TestFileIdentityNonIntField covers the default path in the field helper
// where the field exists but is not an int/uint kind (line 1280).
func TestFileIdentityNonIntField(t *testing.T) {
	type weirdStat struct {
		Dev string // wrong kind
		Ino uint64
	}
	info := mockFileInfo{sys: weirdStat{Dev: "not-a-number", Ino: 20}}
	got := fileIdentity(info)
	// Dev field is a string so field("Dev") returns false; should fall through
	if got != "" {
		t.Fatalf("fileIdentity with non-int Dev should return empty, got %q", got)
	}
}

// TestFileIdentityNoMatchingFields covers the case where the struct has no
// recognized fields at all (returns "").
func TestFileIdentityNoMatchingFields(t *testing.T) {
	type emptyStat struct {
		Foo string
	}
	info := mockFileInfo{sys: emptyStat{Foo: "bar"}}
	got := fileIdentity(info)
	if got != "" {
		t.Fatalf("fileIdentity with no matching fields should return empty, got %q", got)
	}
}

// TestFileChangeIdentityWindowsHighLow covers the Windows-style change
// identity path using ChangeTimeHigh/ChangeTimeLow fields (lines 1321-1326).
func TestFileChangeIdentityWindowsHighLow(t *testing.T) {
	type winStat struct {
		ChangeTimeHigh uint64
		ChangeTimeLow  uint64
	}
	info := mockFileInfo{sys: winStat{ChangeTimeHigh: 5, ChangeTimeLow: 10}}
	got := fileChangeIdentity(info)
	if !strings.HasPrefix(got, "ChangeTime:") {
		t.Fatalf("expected 'ChangeTime:' prefix for Windows change identity, got %q", got)
	}
}

// TestFileChangeIdentityHighDateTime covers the HighDateTime/LowDateTime
// struct path in reflectedTimeIdentity (line 1340).
func TestFileChangeIdentityHighDateTime(t *testing.T) {
	type winTimespec struct {
		HighDateTime int64
		LowDateTime  int64
	}
	// The Ctime/ChangeTime field name is "ChangeTime" on Windows, but the code
	// also checks "Ctime". Let's test HighDateTime/LowDateTime via a field
	// named "ChangeTime" that is a struct with HighDateTime/LowDateTime.
	type winStat struct {
		ChangeTime winTimespec
	}
	info := mockFileInfo{sys: winStat{ChangeTime: winTimespec{HighDateTime: 100, LowDateTime: 200}}}
	got := fileChangeIdentity(info)
	if !strings.HasPrefix(got, "ChangeTime:") {
		t.Fatalf("expected 'ChangeTime:' prefix for Windows HighDateTime, got %q", got)
	}
	if !strings.Contains(got, "100") || !strings.Contains(got, "200") {
		t.Fatalf("expected HighDateTime/LowDateTime values in %q", got)
	}
}

// TestFileChangeIdentityCTime covers the Ctime field name path.
func TestFileChangeIdentityCTime(t *testing.T) {
	type unixStat struct {
		Ctime struct {
			Sec  int64
			Nsec int64
		}
	}
	info := mockFileInfo{sys: unixStat{Ctime: struct {
		Sec  int64
		Nsec int64
	}{Sec: 42, Nsec: 99}}}
	got := fileChangeIdentity(info)
	if !strings.HasPrefix(got, "Ctime:") {
		t.Fatalf("expected 'Ctime:' prefix, got %q", got)
	}
}

// TestReflectedTimeIdentityHighDateTimeStruct covers the
// HighDateTime/LowDateTime struct field pair path (line 1340).
func TestReflectedTimeIdentityHighDateTimeStruct(t *testing.T) {
	type winTimespec struct {
		HighDateTime int64
		LowDateTime  int64
	}
	v := reflect.ValueOf(winTimespec{HighDateTime: 7, LowDateTime: 8})
	got := reflectedTimeIdentity(v)
	want := "7:8"
	if got != want {
		t.Fatalf("reflectedTimeIdentity on HighDateTime/LowDateTime = %q, want %q", got, want)
	}
}

// TestRecordNodeAtDeltaRoot covers the recordNodeAt function with a deltaRoot
// (lines 422-433).
func TestRecordNodeAtDeltaRoot(t *testing.T) {
	leaf := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}, {Index: 2, Visible: false}})
	node := joinRecordNodes(leaf, newRecordLeaf([]indexedTurn{{Index: 3, Visible: true}}))
	// recordNodeAt with nil node
	if got := recordNodeAt(nil, 0); got.Index != 0 {
		t.Fatalf("recordNodeAt(nil, 0) should return zero value")
	}
	// recordNodeAt out of bounds
	if got := recordNodeAt(node, 99); got.Index != 0 {
		t.Fatalf("recordNodeAt out of bounds should return zero value")
	}
	// recordNodeAt with deltaRoot
	got := recordNodeAt(node, 1)
	if got.Index != 2 {
		t.Fatalf("recordNodeAt(node, 1) = index %d, want 2", got.Index)
	}
}

// TestVisibleNodeAtLeafNotFound covers the leaf path where no visible record
// matches the rank (line 403).
func TestVisibleNodeAtLeafNotFound(t *testing.T) {
	leaf := newRecordLeaf([]indexedTurn{{Index: 1, Visible: false}, {Index: 2, Visible: false}})
	// No visible records in this leaf, so rank 0 should return false
	_, ok := visibleNodeAt(leaf, 0, nil)
	if ok {
		t.Fatalf("visibleNodeAt on leaf with no visible records should return false")
	}
}

// TestRecordNodeAtNegativeIndex covers the i<0 path.
func TestRecordNodeAtNegativeIndex(t *testing.T) {
	leaf := newRecordLeaf([]indexedTurn{{Index: 1, Visible: true}})
	if got := recordNodeAt(leaf, -1); got.Index != 0 {
		t.Fatalf("recordNodeAt with negative index should return zero value")
	}
}

// TestAppendTurnIndexJournalRegression covers the error path where
// previousCount > index.recordCount() (line 1085-1086).
func TestAppendTurnIndexJournalRegression(t *testing.T) {
	previous := turnIndexDisk{
		Records: []indexedTurn{{Index: 1}, {Index: 2}, {Index: 3}},
	}
	index := turnIndexDisk{
		Records: []indexedTurn{{Index: 1}}, // fewer records
	}
	err := appendTurnIndexJournal("unused", previous, &index, nil)
	if err == nil || !strings.Contains(err.Error(), "regressed") {
		t.Fatalf("expected regression error, got %v", err)
	}
}

// TestAppendTurnIndexJournalWriteError covers the path where opening the
// journal file fails (line 1131-1133).
func TestAppendTurnIndexJournalWriteError(t *testing.T) {
	dir := t.TempDir()
	// Create a directory where the journal file should be — OpenFile will fail
	journalPath := filepath.Join(dir, "mydir", "index.journal")
	previous := turnIndexDisk{}
	index := turnIndexDisk{
		Version:                 turnIndexVersion,
		TranscriptFormatVersion: transcript.FormatVersion,
		Records:                 []indexedTurn{{Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1}},
		VisibleRecords:          1,
	}
	err := appendTurnIndexJournal(journalPath, previous, &index, nil)
	if err == nil {
		t.Fatalf("expected error when journal directory doesn't exist")
	}
}

// TestWriteTurnIndexTempError covers the writeTurnIndex error path where
// creating a temp file fails (lines 1181-1183).
func TestWriteTurnIndexTempError(t *testing.T) {
	dir := t.TempDir()
	// Use a non-existent directory for the temp file
	badPath := filepath.Join(dir, "nonexistent-dir", "index.json")
	index := turnIndexDisk{Version: turnIndexVersion}
	err := writeTurnIndex(badPath, index, nil)
	if err == nil {
		t.Fatalf("expected error when temp dir doesn't exist")
	}
}

// TestTurnIndexJournalStampMarshalError covers the json.Marshal error path in
// turnIndexJournalStampObserved (line 1162-1163). This is hard to trigger
// directly since the struct is simple, so we verify the happy path instead.
func TestTurnIndexJournalStampObserved(t *testing.T) {
	frame := turnIndexJournalFrame{Version: turnIndexJournalVersion}
	stamp := turnIndexJournalStampObserved(frame, nil)
	if stamp == "" {
		t.Fatalf("expected non-empty stamp for valid frame")
	}
	// With stats
	var stats ReadStats
	_ = turnIndexJournalStampObserved(frame, &stats)
	if stats.indexBytesSerialized == 0 {
		t.Fatalf("expected non-zero serialized bytes with stats")
	}
}

// TestTurnIndexIntegrityStampObserved covers the observed path in
// turnIndexIntegrityStampObserved (lines 1251-1256).
func TestTurnIndexIntegrityStampObserved(t *testing.T) {
	index := turnIndexDisk{Version: turnIndexVersion}
	var stats ReadStats
	stamp := turnIndexIntegrityStampObserved(index, &stats)
	if stamp == "" {
		t.Fatalf("expected non-empty integrity stamp")
	}
	if stats.indexBytesSerialized == 0 {
		t.Fatalf("expected non-zero serialized bytes")
	}
}

// TestPrefixStampEmptyStamp covers the path in prefixStamp where the initial
// stamp computation succeeds for a zero-size file.
func TestPrefixStampZeroSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.transcript.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stamp, readBytes := prefixStamp(f, 0)
	if stamp == "" {
		t.Fatalf("expected non-empty initial stamp for zero-size file")
	}
	if readBytes != 0 {
		t.Fatalf("expected 0 read bytes, got %d", readBytes)
	}
}

// TestProjectIndexedRangeObservedOpenError covers the error path where opening
// the transcript file fails in projectIndexedRangeObserved (line 867-869).
func TestProjectIndexedRangeObservedOpenError(t *testing.T) {
	index := turnIndexDisk{
		VisibleRecords: 3,
		Records: []indexedTurn{
			{Offset: 0, Length: 10, Index: 1, Kind: "entry", Visible: true, VisibleIndex: 1},
		},
	}
	_, _, err := projectIndexedRangeObserved(filepath.Join(t.TempDir(), "missing.transcript.jsonl"), index, 0, 3, nil, nil)
	if err == nil {
		t.Fatalf("expected error for missing transcript in projectIndexedRangeObserved")
	}
}

// TestProjectIndexedRangeObservedEmptyRange covers the lo>=hi early return.
func TestProjectIndexedRangeObservedEmptyRange(t *testing.T) {
	turns, projected, err := projectIndexedRangeObserved("unused", turnIndexDisk{}, 5, 3, nil, nil)
	if err != nil || turns != nil || projected != 0 {
		t.Fatalf("lo>=hi should return nil/0/nil, got turns=%v projected=%d err=%v", turns, projected, err)
	}
}

// TestReadTurnIndexWithJournalNoBase covers the path where the base index file
// doesn't exist (readTurnIndexBounded returns error).
func TestReadTurnIndexWithJournalNoBase(t *testing.T) {
	_, err := readTurnIndexWithJournal(filepath.Join(t.TempDir(), "missing.appwire-index.json"), 100)
	if err == nil {
		t.Fatalf("expected error for missing base index")
	}
}

// TestReadTurnIndexMissingTranscriptStatError covers the os.Stat error path in
// readTurnIndex (lines 920-922).
func TestReadTurnIndexStatError(t *testing.T) {
	_, err := readTurnIndex(filepath.Join(t.TempDir(), "missing.appwire-index.json"))
	if err == nil {
		t.Fatalf("expected error for missing transcript in readTurnIndex")
	}
}

// TestScanTurnIndexStartAtEOF covers the path where start >= transcriptSize
// (lines 623-627).
func TestScanTurnIndexStartAtEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.transcript.jsonl")
	header := transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion}
	headerLine, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	data := append(headerLine, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	index := turnIndexDisk{Header: header}
	_, err = scanTurnIndex(f, int64(len(data)), int64(len(data)), testMaxLineBytes, &index, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("scanTurnIndex at EOF should succeed: %v", err)
	}
}

// TestScanTurnIndexStartAtEOFBadHeader covers the ValidateHeader error path
// when start >= transcriptSize (lines 623-625).
func TestScanTurnIndexStartAtEOFBadHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.transcript.jsonl")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// An empty header with Kind="" should fail validation
	index := turnIndexDisk{Header: transcript.Header{}}
	_, err = scanTurnIndex(f, 2, 2, testMaxLineBytes, &index, map[string]string{}, nil)
	if err == nil {
		t.Fatalf("expected validation error for empty header at EOF")
	}
}

// TestScanTurnIndexMissingHeader covers the path where the transcript has no
// header at all — the first line is an entry that gets parsed as a header
// and fails validation (line 710-711).
func TestScanTurnIndexMissingHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noheader.transcript.jsonl")
	// Just an entry line, no header
	entry := transcript.Entry{Kind: "entry", Seq: 1, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("hi"))}
	entryLine, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	data := append(entryLine, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	index := turnIndexDisk{}
	_, err = scanTurnIndex(f, int64(len(data)), 0, testMaxLineBytes, &index, map[string]string{}, nil)
	if err == nil {
		t.Fatalf("expected error for transcript with no header")
	}
	// The entry line gets parsed as a header but fails because Kind != "header"
	// The error is either "parse transcript header" or "missing transcript header"
}

// TestScanTurnIndexBadHeader covers the path where the header line can't be
// decoded (lines 669-671).
func TestScanTurnIndexBadHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "badheader.transcript.jsonl")
	// Invalid JSON for header
	data := []byte("{bad json}\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	index := turnIndexDisk{}
	_, err = scanTurnIndex(f, int64(len(data)), 0, testMaxLineBytes, &index, map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "parse transcript header") {
		t.Fatalf("expected parse header error, got %v", err)
	}
}

// TestScanTurnIndexBadEntry covers the path where an entry line can't be
// decoded (lines 677-678).
func TestScanTurnIndexBadEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "badentry.transcript.jsonl")
	header := transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion}
	headerLine, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	data := append(headerLine, '\n')
	// Invalid JSON for entry
	data = append(data, []byte("{bad entry}\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	index := turnIndexDisk{}
	_, err = scanTurnIndex(f, int64(len(data)), 0, testMaxLineBytes, &index, map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "parse transcript entry") {
		t.Fatalf("expected parse entry error, got %v", err)
	}
}

// TestLoadTurnIndexStatError covers the os.Stat error path in loadTurnIndex
// (lines 448-451). We can't easily test this directly since it's inside
// loadTurnIndex which opens the file first. Instead we test the full
// loadTurnIndex with a missing file which exercises the os.Open error path
// (lines 442-445).
func TestLoadTurnIndexOpenError(t *testing.T) {
	cache := NewTurnCache()
	_, _, err := cache.loadTurnIndex(filepath.Join(t.TempDir(), "missing.transcript.jsonl"), testMaxLineBytes, boundedTestProjector)
	if err == nil {
		t.Fatalf("expected error for missing file in loadTurnIndex")
	}
}

// TestCloneTurnIndexForAppend covers the trivial clone function.
func TestCloneTurnIndexForAppend(t *testing.T) {
	original := turnIndexDisk{Version: turnIndexVersion, VisibleRecords: 5}
	cloned := cloneTurnIndexForAppend(original)
	if cloned.Version != original.Version || cloned.VisibleRecords != original.VisibleRecords {
		t.Fatalf("clone mismatch")
	}
}

// TestTurnCountFromFileMissingFile covers the error path in TurnCountFromFile.
func TestTurnCountFromFileMissingFile(t *testing.T) {
	cache := NewTurnCache()
	_, err := cache.TurnCountFromFile(filepath.Join(t.TempDir(), "missing.transcript.jsonl"), testMaxLineBytes, boundedTestProjector)
	if err == nil {
		t.Fatalf("expected error for missing file in TurnCountFromFile")
	}
}

// TestToolProjectionStateDefaultKind covers the default case in
// toolProjectionState (line 736-737).
func TestToolProjectionStateDefaultKind(t *testing.T) {
	entry := transcript.Entry{Kind: "entry", Seq: 1, Turn: schema.NewTurn(schema.TurnSystem, llm.User("system"))}
	seed, changes := toolProjectionState(entry, map[string]string{})
	if seed != nil || changes != nil {
		t.Fatalf("default kind should return nil seed and changes, got seed=%v changes=%v", seed, changes)
	}
}

// TestToolProjectionStateToolResultWithName covers the path where a tool
// result already has a name (no lookup needed).
func TestToolProjectionStateToolResultWithName(t *testing.T) {
	entry := transcript.Entry{
		Kind: "entry",
		Seq:  1,
		Turn: schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Content: []llm.ContentPart{{
				Kind:       llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{ToolCallID: "call-1", Name: "read_file", Content: "data"},
			}}},
		},
	}
	seed, changes := toolProjectionState(entry, map[string]string{})
	// When name is non-empty, no seed/lookup changes are produced for that part
	if seed != nil {
		t.Fatalf("expected nil seed when name is provided, got %v", seed)
	}
	// The communicate-name check produces a Delete change since name != "communicate"
	// Actually only "communicate" name triggers delete; read_file doesn't
	if len(changes) != 0 {
		t.Fatalf("expected no changes for tool result with non-communicate name, got %v", changes)
	}
}

// TestToolProjectionStateCommunicateDelete covers the communicate-name delete
// path (lines 765-769).
func TestToolProjectionStateCommunicateDelete(t *testing.T) {
	entry := transcript.Entry{
		Kind: "entry",
		Seq:  1,
		Turn: schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Content: []llm.ContentPart{{
				Kind:       llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{ToolCallID: "call-1", Name: "communicate", Content: "data"},
			}}},
		},
	}
	_, changes := toolProjectionState(entry, map[string]string{})
	foundDelete := false
	for _, c := range changes {
		if c.Delete {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("expected a delete change for communicate tool result")
	}
}

// TestToolProjectionStateEmptyNameTouched covers the path where a tool result
// has an empty name and the ID has been touched (localNames lookup, lines
// 751-753).
func TestToolProjectionStateEmptyNameTouched(t *testing.T) {
	entry := transcript.Entry{
		Kind: "entry",
		Seq:  1,
		Turn: schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Content: []llm.ContentPart{
				{
					Kind:       llm.ContentToolResult,
					ToolResult: &llm.ToolResultData{ToolCallID: "call-1", Name: "communicate", Content: "data"},
				},
				{
					Kind:       llm.ContentToolResult,
					ToolResult: &llm.ToolResultData{ToolCallID: "call-1", Name: "", Content: "data"},
				},
			}},
		},
	}
	// The second result has empty name, and call-1 was touched (by the first
	// communicate result which deleted it). So it won't look up localNames
	// because localDeleted is true.
	seed, changes := toolProjectionState(entry, map[string]string{})
	if seed != nil {
		t.Fatalf("expected nil seed (touched path doesn't populate seed), got %v", seed)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (delete + lookup), got %d: %v", len(changes), changes)
	}
	if !changes[0].Delete {
		t.Fatalf("expected first change to be a delete for communicate, got %v", changes[0])
	}
	if changes[1].ID != "call-1" || changes[1].Name != "" || !changes[1].Lookup {
		t.Fatalf("expected second change to be a lookup with empty name, got %v", changes[1])
	}
}

// TestToolProjectionStateEmptyNameLookupLocal covers the path where a tool
// result has an empty name and the ID was touched but not deleted (localNames
// lookup, lines 751-753).
func TestToolProjectionStateEmptyNameLookupLocal(t *testing.T) {
	// First result with a name that's not communicate, setting localNames
	// But the code only tracks localNames via the communicate-delete path.
	// Actually, the localNames map is only populated/deleted via the communicate
	// path. Let's test with two results: first with name, then empty name
	// for same ID. But the first non-communicate result with a name doesn't
	// populate localNames. So the empty-name lookup for a touched (but not
	// deleted) ID requires the first result to have been "communicate" and
	// then... no, communicate deletes it. This path requires a tool result
	// with a non-empty, non-communicate name first, then an empty name.
	// But the first result with a name doesn't set touched=true. Let me
	// re-read the code...
	//
	// Looking at the code: touched is only set in the communicate branch.
	// localNames is only modified in the communicate branch. So the
	// "touched but not deleted" path (lines 752-753) requires communicate
	// to set touched=true without setting localDeleted=true. But communicate
	// always sets both touched and localDeleted. So this specific branch
	// (name = localNames[id] when touched && !localDeleted) appears to be
	// unreachable with the current code flow.
	//
	// The only way touched[id] is true is through the communicate branch,
	// which also sets localDeleted[id]=true. So !localDeleted[id] is always
	// false when touched[id] is true. This means lines 752-753 are
	// unreachable.

	// Documenting this as unreachable
	t.Skip("lines 752-753 in toolProjectionState are unreachable: touched is only set by the communicate branch which also sets localDeleted")
}

// TestValidToolSeedLookupMismatch covers the path where a lookup change's
// name doesn't match the local seed (line 807-808).
func TestValidToolSeedLookupMismatch(t *testing.T) {
	seed := map[string]string{"id-1": "tool-a"}
	changes := []toolNameChange{{ID: "id-1", Name: "tool-b", Lookup: true}}
	names := map[string]string{"id-1": "tool-a"}
	if validToolSeed(seed, changes, names) {
		t.Fatalf("validToolSeed should return false for lookup name mismatch")
	}
}

// TestValidToolSeedDeleteChange covers the delete-change path (line 810-811).
func TestValidToolSeedDeleteChange(t *testing.T) {
	seed := map[string]string{} // no seed entry
	changes := []toolNameChange{{ID: "id-1", Delete: true}}
	names := map[string]string{}
	if !validToolSeed(seed, changes, names) {
		t.Fatalf("validToolSeed with delete change should return true")
	}
}

// TestValidToolSeedDefaultChange covers the default (set name) path (lines
// 812-813).
func TestValidToolSeedDefaultChange(t *testing.T) {
	seed := map[string]string{}
	changes := []toolNameChange{{ID: "id-1", Name: "tool-a"}}
	names := map[string]string{}
	if !validToolSeed(seed, changes, names) {
		t.Fatalf("validToolSeed with default change should return true")
	}
}

// TestValidToolSeedSeedMismatch covers the final equalToolNames check (line
// 816).
func TestValidToolSeedSeedMismatch(t *testing.T) {
	// The required map is built from lookups; if the seed doesn't match
	// required, return false.
	seed := map[string]string{"id-1": "tool-a", "id-2": "tool-b"}
	changes := []toolNameChange{{ID: "id-1", Name: "tool-a", Lookup: true}}
	names := map[string]string{"id-1": "tool-a"}
	// required = {"id-1": "tool-a"}, but seed has extra id-2
	if validToolSeed(seed, changes, names) {
		t.Fatalf("validToolSeed with seed/required mismatch should return false")
	}
}

// TestApplyToolNameChangesLookup covers the lookup skip path.
func TestApplyToolNameChangesLookup(t *testing.T) {
	names := map[string]string{"id-1": "tool-a"}
	changes := []toolNameChange{{ID: "id-1", Name: "tool-b", Lookup: true}}
	applyToolNameChanges(names, changes)
	if names["id-1"] != "tool-a" {
		t.Fatalf("lookup change should not modify names, got %q", names["id-1"])
	}
}

// TestApplyToolNameChangesDelete covers the delete path.
func TestApplyToolNameChangesDelete(t *testing.T) {
	names := map[string]string{"id-1": "tool-a", "id-2": "tool-b"}
	changes := []toolNameChange{{ID: "id-1", Delete: true}}
	applyToolNameChanges(names, changes)
	if _, ok := names["id-1"]; ok {
		t.Fatalf("delete change should remove entry")
	}
	if names["id-2"] != "tool-b" {
		t.Fatalf("delete should not affect other entries")
	}
}

// TestApplyToolNameChangesSet covers the default set path.
func TestApplyToolNameChangesSet(t *testing.T) {
	names := map[string]string{}
	changes := []toolNameChange{{ID: "id-1", Name: "tool-a"}}
	applyToolNameChanges(names, changes)
	if names["id-1"] != "tool-a" {
		t.Fatalf("set change should set name, got %q", names["id-1"])
	}
}

// TestCloneToolNamesObserved covers the observed clone path.
func TestCloneToolNamesObserved(t *testing.T) {
	names := map[string]string{"a": "x", "b": "y"}
	var stats ReadStats
	clone := cloneToolNamesObserved(names, &stats)
	if clone["a"] != "x" || clone["b"] != "y" {
		t.Fatalf("clone mismatch")
	}
	if stats.resolverEntriesCopied != 2 {
		t.Fatalf("expected 2 entries copied, got %d", stats.resolverEntriesCopied)
	}
}

// TestReplayToolResolverWithStats covers the observed path in replayToolResolver.
func TestReplayToolResolverWithStats(t *testing.T) {
	index := turnIndexDisk{
		Records: []indexedTurn{
			{Index: 1, ToolChanges: []toolNameChange{{ID: "id-1", Name: "tool-a"}}},
			{Index: 2, ToolChanges: []toolNameChange{{ID: "id-2", Name: "tool-b"}}},
		},
	}
	var stats ReadStats
	names := replayToolResolver(index, &stats)
	if names["id-1"] != "tool-a" || names["id-2"] != "tool-b" {
		t.Fatalf("replayToolResolver mismatch: %v", names)
	}
	if stats.resolverHistoryVisits != 2 {
		t.Fatalf("expected 2 history visits, got %d", stats.resolverHistoryVisits)
	}
}

// TestReadFileBoundedOpenError covers the os.Open error path in readFileBounded.
func TestReadFileBoundedOpenError(t *testing.T) {
	_, err := readFileBounded(filepath.Join(t.TempDir(), "missing.json"), 1024)
	if err == nil {
		t.Fatalf("expected error for missing file in readFileBounded")
	}
}

// TestProjectIndexedRangeCoversNoProjector covers projectIndexedRange without
// stats.
func TestProjectIndexedRangeNoProjector(t *testing.T) {
	path := writeNumberedTranscript(t, 2)
	cache := NewTurnCache()
	// Load the index first
	index, _, err := cache.loadTurnIndex(path, testMaxLineBytes, boundedTestProjector)
	if err != nil {
		t.Fatal(err)
	}
	turns, projected := projectIndexedRange(path, index, 0, index.logicalTurnCount(), nil)
	// With nil projector, no items are produced from records, so turns
	// should be empty (no prelude without a system prompt) and projected
	// counts only the records read.
	if len(turns) != 0 {
		t.Fatalf("expected 0 turns with nil projector and no prelude, got %d", len(turns))
	}
	if projected != 2 {
		t.Fatalf("expected projected=2 (2 records read, no prelude), got %d", projected)
	}
}

// TestPersistedTurnID covers the persistedTurnID helper.
func TestPersistedTurnID(t *testing.T) {
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("hi"))
	id := persistedTurnID(turn, 5)
	if id == "" {
		t.Fatalf("persistedTurnID should return non-empty ID")
	}
}

// TestAnchorAt covers the anchorAt function.
func TestAnchorAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	data := []byte("hello world this is test data")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	anchor := anchorAt(f, 0, 5)
	if anchor.Length != 5 {
		t.Fatalf("expected length 5, got %d", anchor.Length)
	}
	if anchor.Stamp == "" {
		t.Fatalf("expected non-empty stamp")
	}
}

// TestTranscriptAnchorsSmallFile covers a file smaller than anchorBytes.
func TestTranscriptAnchorsSmallFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.transcript.jsonl")
	data := []byte("tiny\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	first, tail := transcriptAnchors(f, int64(len(data)))
	if first.Length != len(data) {
		t.Fatalf("first anchor length = %d, want %d", first.Length, len(data))
	}
	if tail.Length != len(data) {
		t.Fatalf("tail anchor length = %d, want %d", tail.Length, len(data))
	}
}

// TestTimeImport covers the time import usage in mockFileInfo.
func TestMockFileInfoMethods(t *testing.T) {
	m := mockFileInfo{sys: nil}
	if m.Name() != "test" {
		t.Fatalf("expected name 'test'")
	}
	if m.Size() != 0 {
		t.Fatalf("expected size 0")
	}
	if m.Mode() != 0o644 {
		t.Fatalf("expected mode 0o644")
	}
	if !m.ModTime().IsZero() {
		t.Fatalf("expected zero mod time")
	}
	if m.IsDir() {
		t.Fatalf("expected not dir")
	}
	if m.Sys() != nil {
		t.Fatalf("expected nil sys")
	}
	_ = time.Time{}
}
