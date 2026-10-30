package apptranscript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/transcript"
)

func scanCount(t *testing.T, path string, maxLineBytes int) (int, error) {
	t.Helper()
	n := 0
	_, err := scanSemanticTranscript(path, maxLineBytes, func(json.RawMessage) error {
		n++
		return nil
	})
	return n, err
}

func appendToFile(t *testing.T, path string, tail []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(tail); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestScanSemanticTranscriptSkipsAnUnterminatedTail(t *testing.T) {
	path := writeEntries(t, userEntry(1, "a"), assistantTextEntry(2, "b"))
	appendToFile(t, path, []byte(`{"kind":"entry","seq":3`))
	n, err := scanCount(t, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("visited %d entries, want the 2 complete ones", n)
	}
}

func TestScanSemanticTranscriptRefusesAnOverLimitLine(t *testing.T) {
	path := writeEntries(t, userEntry(1, "a"))
	appendToFile(t, path, marshalEntryLine(t, userEntry(2, string(make([]byte, 4096)))))
	_, err := scanCount(t, path, 1024)
	if !errors.Is(err, transcript.ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
}

func TestScanSemanticTranscriptRefusesACorruptCompleteLine(t *testing.T) {
	path := writeEntries(t, userEntry(1, "a"))
	appendToFile(t, path, []byte("{not json\n"))
	if _, err := scanCount(t, path, 0); err == nil {
		t.Fatal("corrupt complete line accepted")
	}
}

func TestScanSemanticTranscriptRefusesAMissingHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.transcript.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := scanCount(t, path, 0)
	if !errors.Is(err, transcript.ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}
