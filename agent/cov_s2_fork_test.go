package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// s2cov_writeRawTranscript writes a header line followed by verbatim raw JSONL
// lines, so fork tests can plant blank, corrupt, and non-entry lines the normal
// transcript.Writer would never emit.
func s2cov_writeRawTranscript(t *testing.T, stateDir, id string, headerLine string, lines []string) {
	t.Helper()
	dir := filepath.Join(stateDir, sessionsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, id+".transcript.jsonl")
	body := headerLine + "\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func s2cov_entryLine(t *testing.T, kind schema.TurnKind, text string) string {
	t.Helper()
	var msg llm.Message
	if kind == schema.TurnUserInput {
		msg = llm.User(text)
	} else {
		msg = llm.Assistant(text)
	}
	e := transcript.Entry{Kind: "entry", Turn: schema.NewTurn(kind, msg)}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	return string(b)
}

func s2cov_headerLine(t *testing.T, id string) string {
	t.Helper()
	h := transcript.Header{Kind: "header", SessionID: id, CreatedAt: time.Now().UTC(), ProfileID: "openai", Model: "gpt-5.2", WorkingDir: "/tmp/test"}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	return string(b)
}

func s2cov_saveParentMeta(t *testing.T, stateDir, id string) {
	t.Helper()
	meta := schema.SessionMeta{
		ID:        id,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
}

// TestS2Cov_ForkSession_SkipsNoiseLinesAndForks proves the scanner tolerates
// blank, corrupt-peek, corrupt-entry, and non-entry (api_call) lines, forking
// off only the surviving entry lines.
func TestS2Cov_ForkSession_SkipsNoiseLinesAndForks(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	parentID := "01PARENTNOISE000000000001"
	lines := []string{
		"", // blank line, skipped
		s2cov_entryLine(t, schema.TurnUserInput, "first task"),
		`{not valid json`,                         // corrupt peek, skipped
		`{"kind":"api_call","seq":2}`,             // non-entry kind, skipped
		`{"kind":"entry","turn":"not-an-object"}`, // valid peek, corrupt entry body, skipped
		s2cov_entryLine(t, schema.TurnAssistant, "first reply"),
		s2cov_entryLine(t, schema.TurnUserInput, "second task"),
	}
	s2cov_writeRawTranscript(t, stateDir, parentID, s2cov_headerLine(t, parentID), lines)
	s2cov_saveParentMeta(t, stateDir, parentID)

	// Surviving entries: [U(first), A(first reply), U(second)]. Fork at turn 3.
	childID, err := ForkSession(stateDir, parentID, 3, "edited second", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if childID == "" {
		t.Fatal("empty child id")
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("load child meta: %v", err)
	}
	if childMeta.DivergenceTurn != 3 {
		t.Fatalf("DivergenceTurn = %d, want 3", childMeta.DivergenceTurn)
	}
}

func TestS2Cov_ForkSession_EmptyTranscript(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01PARENTEMPTY000000000001"
	dir := filepath.Join(stateDir, sessionsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".transcript.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := ForkSession(stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want empty-transcript error", err)
	}
}

func TestS2Cov_ForkSession_BadHeader(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01PARENTBADHDR00000000001"
	s2cov_writeRawTranscript(t, stateDir, id, `{not json`, []string{s2cov_entryLine(t, schema.TurnUserInput, "x")})
	_, err := ForkSession(stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "parsing parent transcript header") {
		t.Fatalf("err = %v, want header parse error", err)
	}
}

func TestS2Cov_ForkSession_DivergenceNotUserInput(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01PARENTDIVASST0000000001"
	lines := []string{
		s2cov_entryLine(t, schema.TurnUserInput, "task"),
		s2cov_entryLine(t, schema.TurnAssistant, "reply"),
	}
	s2cov_writeRawTranscript(t, stateDir, id, s2cov_headerLine(t, id), lines)
	s2cov_saveParentMeta(t, stateDir, id)
	// Turn 2 is the ASSISTANT entry — not a valid divergence point.
	_, err := ForkSession(stateDir, id, 2, "x", "")
	if err == nil || !strings.Contains(err.Error(), "not a USER_INPUT turn") {
		t.Fatalf("err = %v, want non-USER_INPUT error", err)
	}
}

func TestS2Cov_ForkSession_MissingParentMeta(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "01PARENTNOMETA00000000001"
	lines := []string{s2cov_entryLine(t, schema.TurnUserInput, "task")}
	s2cov_writeRawTranscript(t, stateDir, id, s2cov_headerLine(t, id), lines)
	// Intentionally no meta file saved.
	_, err := ForkSession(stateDir, id, 1, "x", "")
	if err == nil || !strings.Contains(err.Error(), "load parent session meta") {
		t.Fatalf("err = %v, want load-meta error", err)
	}
}
