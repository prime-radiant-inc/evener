package sessionlog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// FuzzSessionLogFaultLifecycle drives the complete in-memory JSONL lifecycle
// through successful persistence, tolerant reload, and each injectable I/O
// failure boundary. The program produces valid entries while the table below
// supplies deterministic filesystem faults, so the oracle checks observable
// persistence and error identity rather than merely proving that no path panics.
// No lane touches the host filesystem.
func FuzzSessionLogFaultLifecycle(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{255, 7, 0, 1, 9, 2, 3, 4})

	f.Fuzz(func(t *testing.T, program []byte) {
		const path = "/session-log/session.jsonl"
		entries := sessionlogFaultEntries(program)
		empty := newSessionLogFaultLog(t, afero.NewMemMapFs(), path)
		requireSessionlogPublicView(t, empty, []SessionLogEntry{})

		// A healthy append/reload is the reference lifecycle. Its persisted JSONL
		// is then deliberately padded with tolerated partial-write forms below.
		healthyFS := afero.NewMemMapFs()
		healthy := newSessionLogFaultLog(t, healthyFS, path)
		for _, entry := range entries {
			if err := healthy.Append(entry); err != nil {
				t.Fatalf("healthy append: %v", err)
			}
		}
		healthyBytes := sessionlogFaultReadFile(t, healthyFS, path)
		healthyReloaded := newSessionLogFaultLog(t, healthyFS, path)
		requireSessionlogEntriesEqual(t, healthyReloaded.Entries(), entries)
		requireSessionlogPublicView(t, healthy, entries)

		// Blank and malformed lines model an interrupted prior writer. They are
		// intentionally ignored, so the valid suffix must retain the exact
		// reference lifecycle entries.
		tolerantFS := afero.NewMemMapFs()
		tolerantBytes := append([]byte("\nnot valid json\n"), healthyBytes...)
		if err := afero.WriteFile(tolerantFS, path, tolerantBytes, 0o644); err != nil {
			t.Fatalf("write tolerant input: %v", err)
		}
		tolerant := newSessionLogFaultLog(t, tolerantFS, path)
		requireSessionlogEntriesEqual(t, tolerant.Entries(), entries)

		// Existing-file open and scanner failures must fail construction with the
		// original cause intact, rather than returning a partially initialized log.
		openFailure := errors.New("open session log")
		openFS := afero.NewMemMapFs()
		if err := afero.WriteFile(openFS, path, healthyBytes, 0o644); err != nil {
			t.Fatalf("write open-fault input: %v", err)
		}
		if log, err := newSessionLogFS(path, sessionlogFaultFS{Fs: openFS, openErr: openFailure}); log != nil || !errors.Is(err, openFailure) {
			t.Fatalf("open failure = (%v, %v), want (nil, wrapped %v)", log, err, openFailure)
		}

		readFailure := errors.New("read session log")
		readFS := afero.NewMemMapFs()
		if err := afero.WriteFile(readFS, path, healthyBytes, 0o644); err != nil {
			t.Fatalf("write read-fault input: %v", err)
		}
		if log, err := newSessionLogFS(path, sessionlogFaultFS{Fs: readFS, readErr: readFailure}); log != nil || !errors.Is(err, readFailure) {
			t.Fatalf("read failure = (%v, %v), want (nil, wrapped %v)", log, err, readFailure)
		}

		for _, fault := range []struct {
			name string
			fs   sessionlogFaultFS
			err  error
		}{
			{
				name: "mkdir",
				fs:   sessionlogFaultFS{Fs: afero.NewMemMapFs(), mkdirErr: errors.New("make log directory")},
			},
			{
				name: "open-file",
				fs:   sessionlogFaultFS{Fs: afero.NewMemMapFs(), openFileErr: errors.New("open append file")},
			},
			{
				name: "write",
				fs:   sessionlogFaultFS{Fs: afero.NewMemMapFs(), writeErr: errors.New("write append file")},
			},
		} {
			fault.err = fault.fs.fault()
			log := newSessionLogFaultLog(t, fault.fs, path)
			err := log.Append(entries[0])
			if !errors.Is(err, fault.err) {
				t.Fatalf("%s append error = %v, want wrapped %v", fault.name, err, fault.err)
			}
			// Append's documented ordering makes an accepted in-memory action
			// immediately observable even when best-effort persistence fails.
			requireSessionlogEntriesEqual(t, log.Entries(), entries[:1])
			if got := sessionlogFaultReadOptional(fault.fs, path); len(got) != 0 {
				t.Fatalf("%s append persisted unexpected bytes %q", fault.name, got)
			}
		}
	})
}

func newSessionLogFaultLog(t *testing.T, fs afero.Fs, path string) *SessionLog {
	t.Helper()
	log, err := newSessionLogFS(path, fs)
	if err != nil {
		t.Fatalf("new session log: %v", err)
	}
	return log
}

func requireSessionlogEntriesEqual(t *testing.T, got, want []SessionLogEntry) {
	t.Helper()
	if !bytes.Equal(mustMarshal(t, got), mustMarshal(t, want)) {
		t.Fatalf("entries differ:\n got: %s\nwant: %s", mustMarshal(t, got), mustMarshal(t, want))
	}
}

func requireSessionlogPublicView(t *testing.T, log *SessionLog, want []SessionLogEntry) {
	t.Helper()
	if got := log.Len(); got != len(want) {
		t.Fatalf("Len() = %d, want %d", got, len(want))
	}
	requireSessionlogEntriesEqual(t, log.EntriesRange(-1, len(want)+1), want)
	if got := log.EntriesRange(len(want), 0); len(got) != 0 {
		t.Fatalf("reversed range = %v, want empty", got)
	}

	lines := make([]string, 0, len(want))
	for _, entry := range want {
		if entry.Kind == "advisory" {
			continue
		}
		lines = append(lines, fmt.Sprintf("Turn %d [%s] %s: %s", entry.Turn, entry.Action, entry.Outcome, entry.Summary))
	}
	if got, wantRender := log.String(), strings.Join(lines, "\n"); got != wantRender {
		t.Fatalf("String() = %q, want %q", got, wantRender)
	}
}

func sessionlogFaultReadFile(t *testing.T, fs afero.Fs, path string) []byte {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read persisted log: %v", err)
	}
	return data
}

func sessionlogFaultReadOptional(fs afero.Fs, path string) []byte {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil
	}
	return data
}

func sessionlogFaultEntries(program []byte) []SessionLogEntry {
	actions := []string{"shell", "edit_file", "read_file", "assistant"}
	outcomes := []string{"success", "failure"}
	summaries := []string{"", "ran check", "line one\nline two", "changed path/file.go"}

	count := 1
	if len(program) > 0 {
		count += int(program[0] % 4)
	}
	entries := make([]SessionLogEntry, 0, count)
	for index := 0; index < count; index++ {
		shape := sessionlogFaultByte(program, 1+index*4)
		entry := SessionLogEntry{
			Turn:    int(sessionlogFaultByte(program, 2+index*4)),
			Action:  actions[int(sessionlogFaultByte(program, 3+index*4))%len(actions)],
			Summary: summaries[int(sessionlogFaultByte(program, 4+index*4))%len(summaries)],
			Outcome: outcomes[int(shape>>1)%len(outcomes)],
		}
		if shape&0x01 != 0 {
			entry.Kind = "advisory"
		}
		if shape&0x04 != 0 {
			entry.FilesTouched = []string{"path/file.go"}
		}
		if shape&0x08 != 0 {
			entry.Failures = []string{"operation failed"}
		}
		entries = append(entries, entry)
	}
	return entries
}

func sessionlogFaultByte(program []byte, index int) byte {
	if index >= len(program) {
		return 0
	}
	return program[index]
}

type sessionlogFaultFS struct {
	afero.Fs
	openErr     error
	openFileErr error
	mkdirErr    error
	readErr     error
	writeErr    error
}

func (fs sessionlogFaultFS) MkdirAll(path string, perm os.FileMode) error {
	if fs.mkdirErr != nil {
		return fs.mkdirErr
	}
	return fs.Fs.MkdirAll(path, perm)
}

func (fs sessionlogFaultFS) Open(path string) (afero.File, error) {
	if fs.openErr != nil {
		return nil, fs.openErr
	}
	file, err := fs.Fs.Open(path)
	if err != nil || fs.readErr == nil {
		return file, err
	}
	return &sessionlogReadFaultFile{File: file, err: fs.readErr}, nil
}

func (fs sessionlogFaultFS) OpenFile(path string, flag int, perm os.FileMode) (afero.File, error) {
	if fs.openFileErr != nil {
		return nil, fs.openFileErr
	}
	file, err := fs.Fs.OpenFile(path, flag, perm)
	if err != nil || fs.writeErr == nil {
		return file, err
	}
	return &sessionlogWriteFaultFile{File: file, err: fs.writeErr}, nil
}

func (fs sessionlogFaultFS) fault() error {
	for _, err := range []error{fs.mkdirErr, fs.openFileErr, fs.writeErr} {
		if err != nil {
			return err
		}
	}
	return nil
}

type sessionlogReadFaultFile struct {
	afero.File
	err      error
	readOnce bool
}

func (f *sessionlogReadFaultFile) Read(p []byte) (int, error) {
	if f.readOnce {
		return 0, f.err
	}
	f.readOnce = true
	return f.File.Read(p)
}

type sessionlogWriteFaultFile struct {
	afero.File
	err error
}

func (f *sessionlogWriteFaultFile) Write([]byte) (int, error) {
	return 0, f.err
}
