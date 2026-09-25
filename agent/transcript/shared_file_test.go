package transcript

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Several writers can be open on one transcript file in one process: a
// session's own writer plus the short-lived writers the delegate-attention
// paths open to append one record and close. These tests pin that every
// append to the file goes through one sequence and one append position,
// whichever writer makes it. A real transcript carried seq 9390, 9391, 9390,
// 9391 after a resumed session's writer appended behind two such cold writes.

func newSharedFileTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	w, err := NewWriterNoSync(path, Header{SessionID: "shared-file", CreatedAt: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatalf("NewWriterNoSync: %v", err)
	}
	if err := w.Append(steeringTurn("before resume")); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close seed writer: %v", err)
	}
	return path
}

func steeringTurn(text string) schema.Turn {
	return schema.NewTurn(schema.TurnSteering, llm.User(text))
}

func openSharedFileWriter(t *testing.T, path string) *Writer {
	t.Helper()
	w, _, err := OpenWriterForSession(path, "shared-file")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	return w
}

// requireOneSequenceInFileOrder decodes every entry in file order and fails
// unless each seq is exactly one more than the last and every wanted text is
// present once: a duplicate, a gap, or an overwritten record all fail it.
func requireOneSequenceInFileOrder(t *testing.T, path string, wantTexts []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if _, err := DecodeHeader(lines[0]); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	seen := make(map[string]int)
	for i, line := range lines[1:] {
		entry, err := DecodeEntry(line)
		if err != nil {
			t.Fatalf("entry %d is not a record (%v): %q", i, err, line)
		}
		if entry.Seq != i {
			t.Fatalf("entry %d in file order has seq %d, want %d", i, entry.Seq, i)
		}
		seen[entry.Turn.Message.Text()]++
	}
	for _, text := range wantTexts {
		if seen[text] != 1 {
			t.Fatalf("record %q appears %d times, want once", text, seen[text])
		}
	}
}

// The recorded incident: a resumed session's writer is open, two cold writers
// each append one durable record and close, then the session writer appends —
// once durably, once through the buffered door.
func TestWritersOnOneFileShareOneSequenceAndAppendPosition(t *testing.T) {
	path := newSharedFileTranscript(t)
	session := openSharedFileWriter(t, path)
	defer session.Close() //nolint:errcheck // assertion fixture

	for _, text := range []string{"cold one", "cold two"} {
		cold := openSharedFileWriter(t, path)
		if err := cold.AppendSynced(steeringTurn(text)); err != nil {
			t.Fatalf("cold AppendSynced: %v", err)
		}
		if err := cold.Close(); err != nil {
			t.Fatalf("close cold writer: %v", err)
		}
	}
	if err := session.AppendDurable(steeringTurn("session durable")); err != nil {
		t.Fatalf("session AppendDurable: %v", err)
	}
	if err := session.Append(steeringTurn("session buffered")); err != nil {
		t.Fatalf("session Append: %v", err)
	}
	requireOneSequenceInFileOrder(t, path, []string{"before resume", "cold one", "cold two", "session durable", "session buffered"})
}

// The buffered door writes wherever its handle's position is. A writer whose
// first append after a cold write is buffered must not write at the position
// it held before that cold write, over the cold record.
func TestBufferedAppendAfterAnotherWriterDoesNotOverwriteIt(t *testing.T) {
	path := newSharedFileTranscript(t)
	session := openSharedFileWriter(t, path)
	defer session.Close() //nolint:errcheck // assertion fixture

	cold := openSharedFileWriter(t, path)
	if err := cold.AppendSynced(steeringTurn("cold record that is longer than the next one")); err != nil {
		t.Fatalf("cold AppendSynced: %v", err)
	}
	if err := cold.Close(); err != nil {
		t.Fatalf("close cold writer: %v", err)
	}
	if err := session.Append(steeringTurn("short")); err != nil {
		t.Fatalf("session Append: %v", err)
	}
	requireOneSequenceInFileOrder(t, path, []string{"before resume", "cold record that is longer than the next one", "short"})
}

// A writer reopened while another is still open on the file (the session's
// mid-session durability recovery) continues above every sequence any writer
// on the file has used, and so does the old writer until it closes.
func TestReopenWhileAnotherWriterIsOpenContinuesTheSequence(t *testing.T) {
	path := newSharedFileTranscript(t)
	old := openSharedFileWriter(t, path)
	if err := old.Append(steeringTurn("old one")); err != nil {
		t.Fatalf("old Append: %v", err)
	}
	reopened := openSharedFileWriter(t, path)
	defer reopened.Close() //nolint:errcheck // assertion fixture
	if err := old.Append(steeringTurn("old two")); err != nil {
		t.Fatalf("old Append after reopen: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close old writer: %v", err)
	}
	if err := reopened.AppendDurable(steeringTurn("reopened")); err != nil {
		t.Fatalf("reopened AppendDurable: %v", err)
	}
	requireOneSequenceInFileOrder(t, path, []string{"before resume", "old one", "old two", "reopened"})
}

// Writers appending concurrently through every door interleave only whole
// records, in one sequence. Run under -race.
func TestConcurrentWritersOnOneFileKeepOneSequence(t *testing.T) {
	path := newSharedFileTranscript(t)
	const writers, appendsEach = 3, 20
	var texts []string
	var wg sync.WaitGroup
	for i := range writers {
		w := openSharedFileWriter(t, path)
		t.Cleanup(func() { _ = w.Close() })
		for j := range appendsEach {
			texts = append(texts, fmt.Sprintf("writer %d append %d", i, j))
		}
		wg.Go(func() {
			for j := range appendsEach {
				turn := steeringTurn(fmt.Sprintf("writer %d append %d", i, j))
				var err error
				switch j % 3 {
				case 0:
					err = w.Append(turn)
				case 1:
					err = w.AppendDurable(turn)
				default:
					err = w.AppendSynced(turn)
				}
				if err != nil {
					t.Errorf("writer %d append %d: %v", i, j, err)
					return
				}
			}
		})
	}
	wg.Wait()
	requireOneSequenceInFileOrder(t, path, texts)
}
