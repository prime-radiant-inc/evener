package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/fuzz/fault"
)

// w3init_writeOversizeLine writes a transcript line beyond the fork reader's
// 10 MiB bound, so readStrictTranscriptLine rejects it without allocating an
// unbounded record.
// When headerLine is empty the oversize content is the first (header) line;
// otherwise a small header precedes the oversize entry line.
func w3init_writeOversizeLine(t *testing.T, stateDir, id, headerLine string) {
	t.Helper()
	dir := filepath.Join(stateDir, sessionsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oversize := bytes.Repeat([]byte("A"), 11*1024*1024)
	var body []byte
	if headerLine != "" {
		body = append(body, headerLine...)
		body = append(body, '\n')
	}
	body = append(body, oversize...)
	body = append(body, '\n')
	if err := os.WriteFile(filepath.Join(dir, id+".transcript.jsonl"), body, 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// TestW3Init_ForkSession_HeaderScanError covers the bounded header-read error
// arm: the first transcript line exceeds the configured record limit.
func TestW3Init_ForkSession_HeaderScanError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3FORKHDRSCANERR0000001"
	w3init_writeOversizeLine(t, stateDir, id, "")
	_, err := ForkSession(stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "reading parent transcript header") {
		t.Fatalf("err = %v, want header scan error", err)
	}
}

// TestW3Init_ForkSession_EntryScanError covers the bounded entry-read error arm:
// an entry line after a valid header exceeds the configured record limit.
func TestW3Init_ForkSession_EntryScanError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3FORKENTRYSCANERR00001"
	w3init_writeOversizeLine(t, stateDir, id, s2cov_headerLine(t, id))
	_, err := ForkSession(stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "reading parent transcript entries") {
		t.Fatalf("err = %v, want entry scan error", err)
	}
}

// TestW3Init_ForkSession_OpenError covers the non-IsNotExist open error arm with
// a faulting filesystem. Permission bits are not reliable here: privileged test
// processes can read a mode-000 file.
func TestW3Init_ForkSession_OpenError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3FORKOPENDENIED0000001"
	fs := fault.FS(afero.NewMemMapFs(), fault.FromBytes([]byte{0}))

	_, err := forkSessionFS(fs, stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "open parent transcript") {
		t.Fatalf("err = %v, want open parent transcript error", err)
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("err = %v, want wrapped injected open error", err)
	}
}

// w3init_childTranscriptCreateFailFS only faults creation of a newly minted
// transcript. Parent transcript/meta reads and the writer's MkdirAll proceed
// through the base filesystem so the fault reaches the intended Create call.
type w3init_childTranscriptCreateFailFS struct {
	afero.Fs
	parentTranscriptPath string
}

func (fs w3init_childTranscriptCreateFailFS) Create(name string) (afero.File, error) {
	if filepath.Dir(name) == filepath.Dir(fs.parentTranscriptPath) &&
		strings.HasSuffix(name, ".transcript.jsonl") && name != fs.parentTranscriptPath {
		return nil, &os.PathError{Op: "create", Path: name, Err: fault.ErrInjected}
	}
	return fs.Fs.Create(name)
}

// TestW3Init_ForkSession_NewWriterError covers the child-transcript creation
// failure arm with a filesystem boundary that fails only the child Create call.
func TestW3Init_ForkSession_NewWriterError(t *testing.T) {
	t.Parallel()
	const stateDir = "/state"
	id := "01W3FORKNEWWRITER00000001"
	lines := []string{s2cov_entryLine(t, schema.TurnUserInput, "task")}
	base := afero.NewMemMapFs()
	parentPath := filepath.Join(stateDir, sessionsSubdir, id+".transcript.jsonl")
	parentBody := s2cov_headerLine(t, id) + "\n" + strings.Join(lines, "\n") + "\n"
	if err := afero.WriteFile(base, parentPath, []byte(parentBody), 0o644); err != nil {
		t.Fatalf("write parent transcript: %v", err)
	}
	parentMeta := schema.SessionMeta{ID: id, ProfileID: "openai", Model: "gpt-5.2"}
	if err := schema.SaveSessionMetaWithFS(base, stateDir, parentMeta); err != nil {
		t.Fatalf("save parent meta: %v", err)
	}

	fs := w3init_childTranscriptCreateFailFS{
		Fs:                   base,
		parentTranscriptPath: parentPath,
	}
	_, err := forkSessionFS(fs, stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "create child transcript") {
		t.Fatalf("err = %v, want create child transcript error", err)
	}
	if !errors.Is(err, fault.ErrInjected) {
		t.Fatalf("err = %v, want wrapped injected child transcript create error", err)
	}
}

// TestW3Init_ForkSession_ParentMetaSaveError covers the parent-fork-label save
// failure arm: the child is written and saved successfully, but the parent
// meta's atomic temp write collides with a pre-planted directory at its .tmp
// path.
func TestW3Init_ForkSession_ParentMetaSaveError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3FORKPARENTMETA0000001"
	lines := []string{s2cov_entryLine(t, schema.TurnUserInput, "task")}
	s2cov_writeRawTranscript(t, stateDir, id, s2cov_headerLine(t, id), lines)
	s2cov_saveParentMeta(t, stateDir, id)

	// SaveSessionMeta writes <id>.meta.json.tmp then renames. Planting a directory
	// there makes the parent's temp write fail while the child save (a different
	// id) succeeds.
	tmp := filepath.Join(stateDir, sessionsSubdir, id+".meta.json.tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatalf("mkdir tmp block: %v", err)
	}

	_, err := ForkSession(stateDir, id, 1, "x", "branch-label")
	if err == nil || !strings.Contains(err.Error(), "update parent session meta with fork label") {
		t.Fatalf("err = %v, want parent meta save error", err)
	}
}
