package transcript

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// discardFS drops every byte its transcript file receives. An in-memory file
// that kept them would reallocate and copy its whole contents on each append,
// so the fixture's growth — not the append — would dominate the measurement. A
// real file does not: it takes the line into the page cache. The write still
// copies the line, which is the cost a real append pays; only the stored bytes
// are dropped.
type discardFS struct {
	afero.Fs
}

func (fs *discardFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &discardFile{File: file}, nil
}

type discardFile struct {
	afero.File
	copied []byte
}

func (f *discardFile) Write(p []byte) (int, error) {
	f.copied = append(f.copied[:0], p...)
	return len(p), nil
}

// BenchmarkRecordTranscriptEntry measures one session append on an established
// writer: the JSON encode of a turn into the entry buffer, and its write. The
// writer, its placement state and the turn are built before the loop and
// excluded from it, and the SyncInterval gate keeps fsync out of the measured
// region — the steady state production runs at (agent/session_init.go sets
// 1s).
func BenchmarkRecordTranscriptEntry(b *testing.B) {
	path := filepath.Join(b.TempDir(), "transcript.jsonl")
	w, err := NewWriterWithFS(&discardFS{Fs: afero.NewMemMapFs()}, path, sharedFileHeader)
	if err != nil {
		b.Fatalf("NewWriterWithFS: %v", err)
	}
	defer w.Close() //nolint:errcheck // benchmark fixture
	w.SyncInterval = time.Hour
	w.BeginExecution("turn_bench", false)
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("the assistant reply body ", 40)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := w.Record(turn, RecordOptions{Door: DoorBuffered, Place: PlaceSession}); err != nil {
			b.Fatalf("Record: %v", err)
		}
	}
}
