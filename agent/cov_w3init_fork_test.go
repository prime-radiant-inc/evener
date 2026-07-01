package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
)

// w3init_writeOversizeLine writes a transcript file whose bytes exceed the fork
// scanner's 10 MiB per-token buffer, so scanner.Scan reports bufio.ErrTooLong.
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

// TestW3Init_ForkSession_HeaderScanError covers the header-scan error arm: the
// first transcript line exceeds the scanner's buffer.
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

// TestW3Init_ForkSession_EntryScanError covers the entry-scan error arm: an entry
// line after a valid header exceeds the scanner's buffer.
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

// TestW3Init_ForkSession_OpenError covers the non-IsNotExist open error arm: the
// parent transcript exists but is unreadable.
func TestW3Init_ForkSession_OpenError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3FORKOPENDENIED0000001"
	lines := []string{s2cov_entryLine(t, schema.TurnUserInput, "task")}
	s2cov_writeRawTranscript(t, stateDir, id, s2cov_headerLine(t, id), lines)

	tpath := filepath.Join(stateDir, sessionsSubdir, id+".transcript.jsonl")
	if err := os.Chmod(tpath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(tpath, 0o644) })

	_, err := ForkSession(stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "open parent transcript") {
		t.Fatalf("err = %v, want open parent transcript error", err)
	}
}

// TestW3Init_ForkSession_NewWriterError covers the child-transcript creation
// failure arm: the sessions dir is read-only, so the child file cannot be
// created while the parent transcript and meta remain readable.
func TestW3Init_ForkSession_NewWriterError(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01W3FORKNEWWRITER00000001"
	lines := []string{s2cov_entryLine(t, schema.TurnUserInput, "task")}
	s2cov_writeRawTranscript(t, stateDir, id, s2cov_headerLine(t, id), lines)
	s2cov_saveParentMeta(t, stateDir, id)

	sessDir := filepath.Join(stateDir, sessionsSubdir)
	if err := os.Chmod(sessDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessDir, 0o755) })

	_, err := ForkSession(stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "create child transcript") {
		t.Fatalf("err = %v, want create child transcript error", err)
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
