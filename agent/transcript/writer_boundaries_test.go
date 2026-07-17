package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

type zeroProgressFS struct {
	afero.Fs
	file *zeroProgressFile
}

func (fs *zeroProgressFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	fs.file = &zeroProgressFile{File: file}
	return fs.file, nil
}

type zeroProgressFile struct {
	afero.File
	zeroNextWrite bool
}

func (f *zeroProgressFile) Write(p []byte) (int, error) {
	if f.zeroNextWrite {
		f.zeroNextWrite = false
		return 0, nil
	}
	return f.File.Write(p)
}

func TestWriterZeroProgressReturnsErrShortWrite(t *testing.T) {
	fs := &zeroProgressFS{Fs: afero.NewMemMapFs()}
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	fs.file.zeroNextWrite = true

	err = w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("not persisted")))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Append error = %v, want io.ErrShortWrite", err)
	}
	if w.seq != 0 {
		t.Fatalf("next sequence = %d, want 0 after failed append", w.seq)
	}

	data, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("persisted lines = %d, want only the v2 header", len(lines))
	}
	var header Header
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHeader(header); err != nil {
		t.Fatalf("persisted header: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterAppendRacingCloseStopsAfterLock(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })

	fs := afero.NewMemMapFs()
	w, err := newWriterFS(fs, faultTranscriptPath, faultTestHeader())
	if err != nil {
		t.Fatalf("newWriterFS: %v", err)
	}
	before, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatal(err)
	}

	w.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		started <- struct{}{}
		done <- w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("raced")))
	}()
	<-started
	runtime.Gosched()
	w.closed.Store(true)
	w.mu.Unlock()

	if err := <-done; err != nil {
		t.Fatalf("raced Append: %v", err)
	}
	after, err := afero.ReadFile(fs, faultTranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("close-raced append changed transcript:\nbefore=%q\nafter=%q", before, after)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
