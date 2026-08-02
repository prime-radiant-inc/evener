package jobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestReadOutputSnapshotFullTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 1024)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "full output\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	got, err := ReadOutputSnapshot(path, 1024, false)
	if err != nil {
		t.Fatalf("ReadOutputSnapshot: %v", err)
	}
	if string(got.Content) != "full output\n" || got.TotalBytes != 12 || got.RetainedStart != 0 || got.Truncated {
		t.Fatalf("snapshot = %+v, want complete 12-byte output", got)
	}
}

func TestReadOutputSnapshotTruncatedHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 1024)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "abcdefgh")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	got, err := ReadOutputSnapshot(path, 3, true)
	if err != nil {
		t.Fatalf("ReadOutputSnapshot: %v", err)
	}
	if string(got.Content) != "abc" || got.TotalBytes != 8 || got.RetainedStart != 0 || !got.Truncated {
		t.Fatalf("snapshot = %+v, want truncated head abc", got)
	}
}

func TestReadOutputSnapshotRetentionPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 5)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "abcdefgh")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	got, err := ReadOutputSnapshot(path, 1024, false)
	if err != nil {
		t.Fatalf("ReadOutputSnapshot: %v", err)
	}
	if string(got.Content) != "defgh" || got.TotalBytes != 8 || got.RetainedStart != 3 || !got.Truncated {
		t.Fatalf("snapshot = %+v, want retained tail defgh at lifetime offset 3", got)
	}
}

func TestReadOutputSnapshotAlignsRuneWindowEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 1024)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "😀😀")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	for _, tc := range []struct {
		name     string
		fromHead bool
	}{
		{name: "head", fromHead: true},
		{name: "tail", fromHead: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadOutputSnapshot(path, 6, tc.fromHead)
			if err != nil {
				t.Fatalf("ReadOutputSnapshot: %v", err)
			}
			if string(got.Content) != "😀" || got.TotalBytes != 8 || !got.Truncated {
				t.Fatalf("snapshot = %+v, want one whole rune from an 8-byte output", got)
			}
		})
	}
}

func TestReadOutputSnapshotRejectsInvalidLimit(t *testing.T) {
	_, err := ReadOutputSnapshot(filepath.Join(t.TempDir(), "unused.log"), -1, false)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("ReadOutputSnapshot error = %v, want ErrInvalidLimit", err)
	}
}

func TestReadOutputSnapshotDistinguishesMalformedMetadataAndMissingArtifact(t *testing.T) {
	t.Run("malformed metadata", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "job.log")
		if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
			t.Fatalf("write output: %v", err)
		}
		if err := os.WriteFile(outputMetaPath(path), []byte("not-json\n"), 0o644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		_, err := ReadOutputSnapshot(path, 3, false)
		if err == nil || !strings.Contains(err.Error(), "parse output metadata") || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ReadOutputSnapshot error = %v, want malformed-metadata error", err)
		}
	})

	t.Run("missing artifact", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "missing.log")
		_, err := ReadOutputSnapshot(path, 3, false)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ReadOutputSnapshot error = %v, want missing-artifact error", err)
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatalf("read temp directory: %v", readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("missing read created artifacts: %v", entries)
		}
	})
}

func TestReadOutputSnapshotUsesOnlyReadOnlyFilesystemOperations(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("stable\n"), 7, 0)
	fs := &snapshotReadOnlyAuditFS{Fs: base}

	got, err := readOutputSnapshotFs(fs, path, 1024, false)
	if err != nil {
		t.Fatalf("readOutputSnapshotFs: %v", err)
	}
	if string(got.Content) != "stable\n" {
		t.Fatalf("content = %q, want stable newline", got.Content)
	}
	if fs.mutations != 0 {
		t.Fatalf("snapshot attempted %d mutating filesystem operations", fs.mutations)
	}
}

func TestReadOutputSnapshotRetriesOneChangedAttempt(t *testing.T) {
	attempts := 0
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		if attempts == 1 {
			return OutputSnapshot{}, errOutputChanged
		}
		return OutputSnapshot{Content: []byte("stable\n"), TotalBytes: 7}, nil
	})
	if err != nil || attempts != 2 || string(got.Content) != "stable\n" {
		t.Fatalf("snapshot=%+v attempts=%d err=%v", got, attempts, err)
	}
}

func TestReadOutputSnapshotDetectsPostReadMetadataChange(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("first\n"), 6, 0)
	fs := &snapshotChangingFS{
		Fs:   base,
		path: path,
		replacements: []snapshotReplacement{
			{content: []byte("later\n"), total: 12, retainedStart: 6},
		},
	}

	got, err := readOutputSnapshotFs(fs, path, 1024, false)
	if err != nil {
		t.Fatalf("readOutputSnapshotFs: %v", err)
	}
	if string(got.Content) != "later\n" || got.TotalBytes != 12 || got.RetainedStart != 6 {
		t.Fatalf("snapshot = %+v, want stable second attempt after retention advanced", got)
	}
}

func TestReadOutputSnapshotReturnsChangedErrorAfterTwoRaces(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("first\n"), 6, 0)
	fs := &snapshotChangingFS{
		Fs:   base,
		path: path,
		replacements: []snapshotReplacement{
			{content: []byte("later\n"), total: 12, retainedStart: 6},
			{content: []byte("third\n"), total: 18, retainedStart: 12},
		},
	}

	_, err := readOutputSnapshotFs(fs, path, 1024, false)
	if !errors.Is(err, ErrOutputChangedDuringRead) {
		t.Fatalf("readOutputSnapshotFs error = %v, want ErrOutputChangedDuringRead", err)
	}
}

func mustWriteSnapshotFixture(t *testing.T, fs afero.Fs, path string, content []byte, total, retainedStart int64) {
	t.Helper()
	if err := writeSnapshotFixture(fs, path, content, total, retainedStart); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}
}

func writeSnapshotFixture(fs afero.Fs, path string, content []byte, total, retainedStart int64) error {
	if err := afero.WriteFile(fs, path, content, 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	meta, err := json.Marshal(outputMeta{
		TotalBytes:     total,
		RetainedStart:  retainedStart,
		RetainedSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return err
	}
	return afero.WriteFile(fs, outputMetaPath(path), append(meta, '\n'), 0o644)
}

var errSnapshotMutation = errors.New("snapshot test: mutating filesystem operation")

type snapshotReadOnlyAuditFS struct {
	afero.Fs
	mutations int
}

func (fs *snapshotReadOnlyAuditFS) rejectMutation() error {
	fs.mutations++
	return errSnapshotMutation
}

func (fs *snapshotReadOnlyAuditFS) Create(string) (afero.File, error) {
	return nil, fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Mkdir(string, os.FileMode) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) MkdirAll(string, os.FileMode) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	const mutating = os.O_WRONLY | os.O_RDWR | os.O_APPEND | os.O_CREATE | os.O_EXCL | os.O_TRUNC
	if flag&mutating != 0 {
		return nil, fs.rejectMutation()
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

func (fs *snapshotReadOnlyAuditFS) Remove(string) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) RemoveAll(string) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Rename(string, string) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Chmod(string, os.FileMode) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Chtimes(string, time.Time, time.Time) error {
	return fs.rejectMutation()
}

type snapshotChangingFS struct {
	afero.Fs
	path         string
	outputStats  int
	replacements []snapshotReplacement
}

type snapshotReplacement struct {
	content       []byte
	total         int64
	retainedStart int64
}

func (fs *snapshotChangingFS) Stat(name string) (os.FileInfo, error) {
	if name == fs.path {
		fs.outputStats++
		if fs.outputStats%2 == 0 && len(fs.replacements) > 0 {
			replacement := fs.replacements[0]
			fs.replacements = fs.replacements[1:]
			if err := writeSnapshotFixture(fs.Fs, fs.path, replacement.content, replacement.total, replacement.retainedStart); err != nil {
				return nil, err
			}
		}
	}
	return fs.Fs.Stat(name)
}
