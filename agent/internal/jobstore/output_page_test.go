package jobstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestOutputPageSelectsLatestAndBackwardRanges(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "job.log")
	output, err := CreateOutputNoSync(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	appendOutput(t, output, strings.Repeat("p", 100)+strings.Repeat("s", 100))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	for _, reader := range []struct {
		name string
		read func(*int64, int) (OutputWindowSnapshot, error)
	}{
		{"store", output.ReadPage},
		{"path", func(before *int64, maxBytes int) (OutputWindowSnapshot, error) {
			return ReadOutputPageSnapshot(path, before, maxBytes)
		}},
		{"descriptor", func(before *int64, maxBytes int) (OutputWindowSnapshot, error) {
			return ReadOutputPageSnapshotFromFile(path, f, before, maxBytes)
		}},
	} {
		t.Run(reader.name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				before *int64
				max    int
				start  int64
				end    int64
				err    error
			}{
				{"latest", nil, 64, 136, 200, nil},
				{"backward", pageOffset(184), 64, 120, 184, nil},
				{"clip calculated start", pageOffset(120), 64, 100, 120, nil},
				{"at floor", pageOffset(100), 64, 100, 100, nil},
				{"at total", pageOffset(200), 64, 136, 200, nil},
				{"below floor", pageOffset(99), 64, 0, 0, ErrOutputPruned},
				{"explicit zero is pruned", pageOffset(0), 64, 0, 0, ErrOutputPruned},
				{"beyond total", pageOffset(201), 64, 0, 0, ErrInvalidOffset},
				{"negative before", pageOffset(-1), 64, 0, 0, ErrInvalidOffset},
				{"zero limit", nil, 0, 0, 0, ErrInvalidOffset},
				{"negative limit", nil, -1, 0, 0, ErrInvalidOffset},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, readErr := reader.read(tc.before, tc.max)
					if !errors.Is(readErr, tc.err) {
						t.Fatalf("error = %v, want %v", readErr, tc.err)
					}
					if got.TotalBytes != 200 || got.RetainedStart != 100 {
						t.Fatalf("bounds = total %d, floor %d, want 200, 100", got.TotalBytes, got.RetainedStart)
					}
					if tc.err != nil {
						if len(got.Content) != 0 {
							t.Fatalf("rejected selector returned bytes %x", got.Content)
						}
						return
					}
					if got.Start != tc.start || got.End != tc.end {
						t.Fatalf("range = [%d,%d), want [%d,%d)", got.Start, got.End, tc.start, tc.end)
					}
					want := bytes.Repeat([]byte("s"), int(tc.end-tc.start))
					if !bytes.Equal(got.Content, want) {
						t.Fatalf("bytes = %x, want %x", got.Content, want)
					}
				})
			}
		})
	}
}

func TestOutputPagePreservesRawUnicodeAndMalformedBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		input   []byte
		before  *int64
		max     int
		start   int64
		end     int64
		content []byte
	}{
		{"two byte suffix", []byte{0x61, 0x62, 0xc3, 0xa9, 0x63, 0x64}, nil, 3, 3, 6, []byte{0xa9, 0x63, 0x64}},
		{"two byte prefix", []byte{0x61, 0x62, 0xc3, 0xa9, 0x63, 0x64}, pageOffset(3), 3, 0, 3, []byte{0x61, 0x62, 0xc3}},
		{"four byte suffix", []byte{0x61, 0x62, 0x63, 0xf0, 0x9f, 0x98, 0x80, 0x78, 0x79, 0x7a}, nil, 5, 5, 10, []byte{0x98, 0x80, 0x78, 0x79, 0x7a}},
		{"four byte prefix", []byte{0x61, 0x62, 0x63, 0xf0, 0x9f, 0x98, 0x80, 0x78, 0x79, 0x7a}, pageOffset(5), 5, 0, 5, []byte{0x61, 0x62, 0x63, 0xf0, 0x9f}},
		{"malformed", []byte{0xff, 0xc3, 0x28}, nil, 3, 0, 3, []byte{0xff, 0xc3, 0x28}},
		{"explicit zero", []byte("abcd"), pageOffset(0), 4, 0, 0, nil},
		{"empty output", nil, nil, 4, 0, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "job.log")
			output, err := CreateOutputNoSync(path, 1024)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = output.Close() })
			if len(tc.input) > 0 {
				appendOutput(t, output, string(tc.input))
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			for _, read := range []func(*int64, int) (OutputWindowSnapshot, error){
				output.ReadPage,
				func(before *int64, maxBytes int) (OutputWindowSnapshot, error) {
					return ReadOutputPageSnapshot(path, before, maxBytes)
				},
				func(before *int64, maxBytes int) (OutputWindowSnapshot, error) {
					return ReadOutputPageSnapshotFromFile(path, f, before, maxBytes)
				},
			} {
				got, err := read(tc.before, tc.max)
				if err != nil {
					t.Fatal(err)
				}
				if got.Start != tc.start || got.End != tc.end || got.TotalBytes != int64(len(tc.input)) || got.RetainedStart != 0 || !bytes.Equal(got.Content, tc.content) {
					t.Fatalf("snapshot = %+v, want [%d,%d) bytes %x, total %d, floor 0", got, tc.start, tc.end, tc.content, len(tc.input))
				}
			}
		})
	}
}

func TestOutputPageEmptyRetainedInterval(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "job.log")
	mustWriteSnapshotFixture(t, afero.NewOsFs(), path, nil, 37, 37)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	for _, read := range []func(*int64, int) (OutputWindowSnapshot, error){
		func(before *int64, maxBytes int) (OutputWindowSnapshot, error) {
			return ReadOutputPageSnapshot(path, before, maxBytes)
		},
		func(before *int64, maxBytes int) (OutputWindowSnapshot, error) {
			return ReadOutputPageSnapshotFromFile(path, f, before, maxBytes)
		},
	} {
		got, err := read(nil, 64)
		if err != nil || got.Start != 37 || got.End != 37 || got.TotalBytes != 37 || got.RetainedStart != 37 || len(got.Content) != 0 {
			t.Fatalf("snapshot = %+v, error %v, want empty [37,37), total/floor 37", got, err)
		}
	}
}

func TestOutputPageSnapshotRetriesOnlyOnce(t *testing.T) {
	t.Parallel()
	fs := &snapshotChangingFS{
		Fs:   afero.NewMemMapFs(),
		path: "/job.log",
		replacements: []snapshotReplacement{
			{content: []byte("BBBB"), total: 8, retainedStart: 4},
			{content: []byte("CCCC"), total: 12, retainedStart: 8},
		},
	}
	mustWriteSnapshotFixture(t, fs.Fs, fs.path, []byte("AAAA"), 4, 0)
	got, err := readOutputPageSnapshotFs(fs, fs.path, nil, 4)
	if !errors.Is(err, ErrOutputChangedDuringRead) || len(got.Content) != 0 {
		t.Fatalf("twice changed page = %+v, error %v, want change without bytes", got, err)
	}
	if len(fs.replacements) != 0 || fs.outputOpens != 6 {
		t.Fatalf("attempt evidence = %d pending changes, %d output opens, want 0, 6", len(fs.replacements), fs.outputOpens)
	}
}

func TestOutputPageStoreHoldsMutexAcrossSelectionAndRead(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "job.log")
	output, err := CreateOutputNoSync(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	appendOutput(t, output, "AAAA")
	gate := &pageOutputGateFS{Fs: output.fs, path: path, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(gate.release) }) }
	t.Cleanup(unblock)
	output.fs = gate
	done := make(chan pageReadResult, 1)
	go func() {
		got, err := output.ReadPage(nil, 4)
		done <- pageReadResult{got, err}
	}()
	awaitPageReadGate(t, gate.entered)
	if output.mu.TryLock() {
		output.mu.Unlock()
		t.Fatal("store released the mutex between page selection and read")
	}
	unblock()
	got := awaitPageReadResult(t, done)
	if got.err != nil || got.snapshot.Start != 0 || got.snapshot.End != 4 || got.snapshot.TotalBytes != 4 || string(got.snapshot.Content) != "AAAA" {
		t.Fatalf("locked page = %+v, error %v, want AAAA [0,4), total 4", got.snapshot, got.err)
	}
	appendOutput(t, output, "BBBB")
	latest, err := output.ReadPage(nil, 4)
	if err != nil || latest.Start != 4 || latest.End != 8 || latest.TotalBytes != 8 || string(latest.Content) != "BBBB" {
		t.Fatalf("next page = %+v, error %v, want BBBB [4,8), total 8", latest, err)
	}
}

func TestOutputPageSnapshotConcurrentProductionAppendAndPrune(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cap   int64
		floor int64
	}{
		{"append", 1024, 0},
		{"prune", 4, 4},
	} {
		for _, api := range []string{"path", "descriptor"} {
			t.Run(tc.name+"/"+api, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "job.log")
				output, err := CreateOutputNoSync(path, tc.cap)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = output.Close() })
				appendOutput(t, output, "AAAA")
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close() })
				gate := newPageMetadataGate(t, path)
				done := make(chan pageReadResult, 1)
				go func() {
					var snapshot OutputWindowSnapshot
					var readErr error
					if api == "path" {
						snapshot, readErr = readOutputPageSnapshotFs(gate, path, nil, 4)
					} else {
						snapshot, readErr = readOutputPageSnapshotFromFileOnce(gate, path, f, nil, 4)
					}
					done <- pageReadResult{snapshot, readErr}
				}()
				awaitPageReadGate(t, gate.entered)
				appendOutput(t, output, "BBBB")
				gate.unblock()
				got := awaitPageReadResult(t, done)
				if api == "descriptor" {
					if !errors.Is(got.err, errOutputChanged) || len(got.snapshot.Content) != 0 {
						t.Fatalf("changed descriptor = %+v, error %v, want change without bytes", got.snapshot, got.err)
					}
					return
				}
				if got.err != nil || got.snapshot.Start != 4 || got.snapshot.End != 8 || got.snapshot.TotalBytes != 8 || got.snapshot.RetainedStart != tc.floor || string(got.snapshot.Content) != "BBBB" {
					t.Fatalf("retried latest page = %+v, error %v, want BBBB [4,8), total 8, floor %d", got.snapshot, got.err, tc.floor)
				}
			})
		}
	}
}

func TestOutputPageDescriptorFencesRejectedSelectorGeneration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		before int64
		gateAt int
	}{
		{"pruned selector", 3, 2},
		{"invalid selector", 9, 2},
		{"successful selection, trailing observation", 8, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "job.log")
			mustWriteSnapshotFixture(t, afero.NewOsFs(), path, []byte("BBBB"), 8, 4)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			gate := newPageMetadataGate(t, path)
			gate.blockOnMetaOpen = tc.gateAt
			done := make(chan pageReadResult, 1)
			go func() {
				snapshot, err := readOutputPageSnapshotFromFileOnce(gate, path, f, &tc.before, 4)
				done <- pageReadResult{snapshot, err}
			}()
			awaitPageReadGate(t, gate.entered)
			replacement := path + ".replacement"
			if err := os.WriteFile(replacement, []byte("BBBB"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
			gate.unblock()
			got := awaitPageReadResult(t, done)
			if !errors.Is(got.err, errOutputChanged) || len(got.snapshot.Content) != 0 || got.snapshot.TotalBytes != 0 || got.snapshot.RetainedStart != 0 {
				t.Fatalf("replaced descriptor = %+v, error %v, want change without published bounds", got.snapshot, got.err)
			}
		})
	}
}

type pageReadResult struct {
	snapshot OutputWindowSnapshot
	err      error
}

type pageOutputGateFS struct {
	afero.Fs
	path    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (fs *pageOutputGateFS) Open(name string) (afero.File, error) {
	if name == fs.path {
		fs.once.Do(func() {
			close(fs.entered)
			<-fs.release
		})
	}
	return fs.Fs.Open(name)
}

type pageMetadataGateFS struct {
	afero.Fs
	path            string
	metaOpens       int
	blockOnMetaOpen int
	entered         chan struct{}
	release         chan struct{}
	once            sync.Once
}

func newPageMetadataGate(t *testing.T, path string) *pageMetadataGateFS {
	t.Helper()
	gate := &pageMetadataGateFS{Fs: afero.NewOsFs(), path: path, blockOnMetaOpen: 2, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(gate.unblock)
	return gate
}

func (fs *pageMetadataGateFS) unblock() { fs.once.Do(func() { close(fs.release) }) }

func (fs *pageMetadataGateFS) Open(name string) (afero.File, error) {
	if name == outputMetaPath(fs.path) {
		fs.metaOpens++
		if fs.metaOpens == fs.blockOnMetaOpen {
			close(fs.entered)
			<-fs.release
		}
	}
	return fs.Fs.Open(name)
}

func awaitPageReadGate(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("page read did not reach metadata gate")
	}
}

func awaitPageReadResult(t *testing.T, done <-chan pageReadResult) pageReadResult {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("page read did not finish")
		return pageReadResult{}
	}
}

func pageOffset(offset int64) *int64 { return new(offset) }
