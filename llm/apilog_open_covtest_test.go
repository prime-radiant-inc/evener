//go:build darwin || linux

package llm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCovOpenPrivateAPILogFileNotRegular(t *testing.T) {
	file, err := openPrivateAPILogFile("/dev/null")
	if file != nil {
		_ = file.Close()
		t.Fatal("openPrivateAPILogFile(/dev/null) returned a file")
	}
	want := `API-log target "/dev/null" is not a regular file`
	if err == nil || err.Error() != want {
		t.Fatalf("openPrivateAPILogFile(/dev/null) error = %v, want %q", err, want)
	}
}

func TestCovOpenPrivateAPILogFileTargetLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	owner, err := openPrivateAPILogFile(path)
	if err != nil {
		t.Fatalf("first openPrivateAPILogFile: %v", err)
	}
	defer owner.Close()

	contender, err := openPrivateAPILogFile(path)
	if contender != nil {
		_ = contender.Close()
		t.Fatal("second openPrivateAPILogFile returned a file")
	}
	if !errors.Is(err, ErrAPILogTargetLocked) {
		t.Fatalf("second open error = %v, want ErrAPILogTargetLocked identity", err)
	}
	want := fmt.Sprintf("%s: %s", ErrAPILogTargetLocked, path)
	if err.Error() != want {
		t.Fatalf("second open error = %q, want %q", err, want)
	}
}

func TestCovOpenPrivateAPILogFileOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent-dir", "api.jsonl")
	file, err := openPrivateAPILogFile(path)
	if file != nil {
		_ = file.Close()
		t.Fatal("openPrivateAPILogFile under a missing directory returned a file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("openPrivateAPILogFile under a missing directory error = %v, want fs.ErrNotExist", err)
	}
}

func TestCovOpenPrivateAPILogFileSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.jsonl")
	file, err := openPrivateAPILogFile(path)
	if err != nil {
		t.Fatalf("openPrivateAPILogFile: %v", err)
	}
	defer file.Close()

	if file.Name() != path {
		t.Fatalf("opened file name = %q, want %q", file.Name(), path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat opened API log: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("opened API log mode = %v, want regular 0600", info.Mode())
	}
}
