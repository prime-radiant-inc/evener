package jobstore

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestReadOutputSnapshotFromFilePinsOpenedOutput(t *testing.T) {
	for _, replacement := range []string{"regular", "symlink", "fifo"} {
		for _, api := range []string{"snapshot", "window"} {
			t.Run(replacement+"/"+api, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "job.log")
				const original = "opened output\n"
				mustWriteSnapshotFixture(t, afero.NewOsFs(), path, []byte(original), int64(len(original)), 0)

				f, err := os.Open(path)
				if err != nil {
					t.Fatalf("open original output: %v", err)
				}
				t.Cleanup(func() { _ = f.Close() })
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove output leaf: %v", err)
				}
				switch replacement {
				case "regular":
					if err := os.WriteFile(path, []byte("replacement output must not be read\n"), 0o600); err != nil {
						t.Fatalf("write replacement: %v", err)
					}
				case "symlink":
					target := filepath.Join(dir, "replacement.log")
					if err := os.WriteFile(target, []byte("symlink target must not be read\n"), 0o600); err != nil {
						t.Fatalf("write symlink target: %v", err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				case "fifo":
					if _, err := exec.LookPath("mkfifo"); err != nil {
						t.Skip("mkfifo unavailable")
					}
					if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
						t.Skipf("mkfifo unavailable: %v (%s)", err, out)
					}
				}

				type result struct {
					content []byte
					err     error
				}
				done := make(chan result, 1)
				go func() {
					if api == "snapshot" {
						got, err := ReadOutputSnapshotFromFile(path, f, 1024, false)
						done <- result{content: got.Content, err: err}
						return
					}
					got, err := ReadOutputWindowSnapshotFromFile(path, f, 0, 1024)
					done <- result{content: got.Content, err: err}
				}()

				select {
				case got := <-done:
					if got.err != nil {
						t.Fatalf("fd snapshot: %v", got.err)
					}
					if string(got.content) != original {
						t.Fatalf("content = %q, want pinned original %q", got.content, original)
					}
				case <-time.After(3 * time.Second):
					if replacement == "fifo" {
						unblock, err := os.OpenFile(path, os.O_RDWR, 0)
						if err == nil {
							_ = unblock.Close()
						}
					}
					t.Fatal("fd snapshot blocked after the output path was replaced")
				}
			})
		}
	}
}

func TestReadOutputSnapshotFromFileParity(t *testing.T) {
	t.Run("full tail", func(t *testing.T) {
		path, f := newOutputSnapshotFile(t, 1024, "full output\n")
		got, err := ReadOutputSnapshotFromFile(path, f, 1024, false)
		if err != nil {
			t.Fatalf("ReadOutputSnapshotFromFile: %v", err)
		}
		if string(got.Content) != "full output\n" || got.TotalBytes != 12 || got.RetainedStart != 0 || got.Truncated {
			t.Fatalf("snapshot = %+v, want complete 12-byte output", got)
		}
	})

	t.Run("truncated head", func(t *testing.T) {
		path, f := newOutputSnapshotFile(t, 1024, "abcdefgh")
		got, err := ReadOutputSnapshotFromFile(path, f, 3, true)
		if err != nil {
			t.Fatalf("ReadOutputSnapshotFromFile: %v", err)
		}
		if string(got.Content) != "abc" || got.TotalBytes != 8 || got.RetainedStart != 0 || !got.Truncated {
			t.Fatalf("snapshot = %+v, want truncated head abc", got)
		}
	})

	t.Run("rune window edges", func(t *testing.T) {
		path, f := newOutputSnapshotFile(t, 1024, "😀😀")
		for _, fromHead := range []bool{true, false} {
			got, err := ReadOutputSnapshotFromFile(path, f, 6, fromHead)
			if err != nil {
				t.Fatalf("ReadOutputSnapshotFromFile(fromHead=%v): %v", fromHead, err)
			}
			if string(got.Content) != "😀" || got.TotalBytes != 8 || !got.Truncated {
				t.Fatalf("snapshot = %+v, want one whole rune", got)
			}
		}
	})

	t.Run("invalid limit", func(t *testing.T) {
		path, f := newOutputSnapshotFile(t, 1024, "data")
		if _, err := ReadOutputSnapshotFromFile(path, f, -1, false); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("error = %v, want ErrInvalidLimit", err)
		}
	})
}

func TestReadOutputWindowSnapshotFromFileParity(t *testing.T) {
	path, f := newOutputSnapshotFile(t, 5, "abcdefgh") // retained "defgh" at lifetime offset 3

	got, err := ReadOutputWindowSnapshotFromFile(path, f, 4, 3)
	if err != nil {
		t.Fatalf("ReadOutputWindowSnapshotFromFile: %v", err)
	}
	if string(got.Content) != "efg" || got.Start != 4 || got.End != 7 || got.TotalBytes != 8 || got.RetainedStart != 3 || !got.Truncated {
		t.Fatalf("snapshot = %+v, want efg at lifetime [4,7) of retained [3,8)", got)
	}

	for _, tc := range []struct {
		name      string
		offset    int64
		maxBytes  int
		wantError error
	}{
		{name: "negative offset", offset: -1, maxBytes: 1, wantError: ErrInvalidOffset},
		{name: "pruned offset", offset: 2, maxBytes: 1, wantError: ErrOutputPruned},
		{name: "beyond EOF", offset: 9, maxBytes: 1, wantError: ErrInvalidOffset},
		{name: "negative limit", offset: 3, maxBytes: -1, wantError: ErrInvalidLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadOutputWindowSnapshotFromFile(path, f, tc.offset, tc.maxBytes); !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v, want %v", err, tc.wantError)
			}
		})
	}

	eof, err := ReadOutputWindowSnapshotFromFile(path, f, 8, 16)
	if err != nil {
		t.Fatalf("EOF window: %v", err)
	}
	if len(eof.Content) != 0 || eof.Start != 8 || eof.End != 8 || eof.TotalBytes != 8 || eof.RetainedStart != 3 {
		t.Fatalf("EOF snapshot = %+v", eof)
	}
}

func TestObserveOutputSnapshotFromFileDetectsAppend(t *testing.T) {
	path, f := newOutputSnapshotFile(t, 1024, "seed\n")
	before, err := observeOutputSnapshotFromFile(afero.NewOsFs(), path, f)
	if err != nil {
		t.Fatalf("observe before append: %v", err)
	}

	appendFile, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open append handle: %v", err)
	}
	if _, err := appendFile.WriteString("next\n"); err != nil {
		_ = appendFile.Close()
		t.Fatalf("append output: %v", err)
	}
	if err := appendFile.Close(); err != nil {
		t.Fatalf("close append handle: %v", err)
	}

	after, err := observeOutputSnapshotFromFile(afero.NewOsFs(), path, f)
	if err != nil {
		t.Fatalf("observe after append: %v", err)
	}
	if !after.changedFrom(before) {
		t.Fatalf("append was not detected: before=%+v after=%+v", before, after)
	}
}

func TestReadOutputWindowSnapshotFromFileConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 1<<20)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	appendOutput(t, o, "seed\n")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	var finished atomic.Bool
	done := make(chan error, 1)
	go func() {
		defer finished.Store(true)
		for range 150 {
			if _, err := o.Append([]byte("append-line\n")); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	successes := 0
	for !finished.Load() || successes == 0 {
		got, err := ReadOutputWindowSnapshotFromFile(path, f, 0, 31)
		if errors.Is(err, ErrOutputChangedDuringRead) {
			continue
		}
		if err != nil {
			t.Fatalf("ReadOutputWindowSnapshotFromFile: %v", err)
		}
		successes++
		if got.Start != 0 || got.End < got.Start || got.End > got.TotalBytes || int64(len(got.Content)) != got.End-got.Start {
			t.Fatalf("inconsistent fd snapshot = %+v", got)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("append: %v", err)
	}
}

func newOutputSnapshotFile(t *testing.T, maxRetained int64, content string) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, maxRetained)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, content)
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return path, f
}

func TestReadOutputWindowSnapshotFromFilePreservesRawBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	want := []byte{0xf0, 0x9f, 0x98}
	mustWriteSnapshotFixture(t, afero.NewOsFs(), path, want, 3, 0)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	got, err := ReadOutputWindowSnapshotFromFile(path, f, 0, len(want))
	if err != nil {
		t.Fatalf("ReadOutputWindowSnapshotFromFile: %v", err)
	}
	if !bytes.Equal(got.Content, want) || got.Start != 0 || got.End != 3 {
		t.Fatalf("snapshot = %+v content=%x, want exact bytes %x", got, got.Content, want)
	}
}
