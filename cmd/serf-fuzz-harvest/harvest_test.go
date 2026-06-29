package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

const plantedHarvestSecret = "sk-proj-PLANT3Dabcdefghijklmnop0123456789ZZ"

// writeFixtureState builds a state dir with one of every recorded source,
// including a planted secret inside a tool-call argument.
func writeFixtureState(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	sessDir := filepath.Join(root, "serf", "projects", "abcdef0123456789", "sessions")
	if err := os.MkdirAll(filepath.Join(sessDir, "01SID"), 0o755); err != nil {
		t.Fatal(err)
	}

	// api-raw.jsonl — an OpenAI Responses incomplete-stream body.
	respStream := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}` + "\n\n"
	rawEntry, _ := json.Marshal(llm.APIRawLogEntry{Provider: "openai", Model: "gpt-5", Mode: "stream", ResponseBody: respStream})
	writeFile(t, filepath.Join(root, "api-raw.jsonl"), string(rawEntry)+"\n")

	// transcript with a tool call whose args embed a secret.
	header, _ := json.Marshal(transcript.Header{Kind: "header", FormatVersion: 1, SessionID: "01SID", CreatedAt: time.Now()})
	entry, _ := json.Marshal(transcript.Entry{
		Kind: "entry", Seq: 1,
		Turn: schema.Turn{Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"/etc/key","note":"my key ` + plantedHarvestSecret + `"}`),
				},
			}},
		}},
	})
	writeFile(t, filepath.Join(sessDir, "01SID.transcript.jsonl"), string(header)+"\n"+string(entry)+"\n")

	// appwire-frames.jsonl — a request frame with a known method + params.
	frame, _ := json.Marshal(recordedFrame{Dir: "recv", Frame: `{"id":1,"method":"thread/read","params":{"threadId":"abc","subscribe":true}}`})
	writeFile(t, filepath.Join(root, "appwire-frames.jsonl"), string(frame)+"\n")

	// hub-http.jsonl — a replayable GET, a /doc/file GET, and a non-GET to drop.
	var http strings.Builder
	for _, rec := range []recordedHTTPRequest{
		{Method: "GET", Path: "/api/health", Query: ""},
		{Method: "GET", Path: "/doc/file", Query: "session=01SID&path=notes.txt"},
		{Method: "POST", Path: "/api/spawn", Query: ""},
	} {
		b, _ := json.Marshal(rec)
		http.Write(b)
		http.WriteByte('\n')
	}
	writeFile(t, filepath.Join(root, "hub-http.jsonl"), http.String())

	// jobs.jsonl — raw NDJSON event lines.
	writeFile(t, filepath.Join(sessDir, "01SID", "jobs.jsonl"),
		`{"event":"job_started","job_id":"j1","ts":"2026-01-01T00:00:00Z"}`+"\n"+
			`{"event":"job_completed","job_id":"j1","result":"done"}`+"\n")

	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHarvestEndToEnd(t *testing.T) {
	state := writeFixtureState(t)
	out := t.TempDir()

	code := run([]string{"--state-dir", state, "--out-root", out, "--no-gitleaks"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("harvest exited %d, want 0", code)
	}

	// Each surface produced at least one seed in its target dir.
	for _, rel := range []string{
		dirParseSSE,
		dirOpenAIResponses,
		dirToolArgsValidate,
		dirMessageDecode,
		dirMethodParams,
		dirWebHandler,
		dirJobstoreEvent,
		dirJobstoreSequence,
	} {
		assertNonEmptyCorpusDir(t, filepath.Join(out, rel))
	}

	// The planted secret never reaches a committed seed.
	walkAssertNoSecret(t, out)

	// FuzzWebHandler dropped the POST and kept the two GETs (health + doc/file).
	if n := countFiles(t, filepath.Join(out, dirWebHandler)); n != 2 {
		t.Fatalf("FuzzWebHandler: got %d seeds, want 2 (POST must be dropped)", n)
	}

	// Re-running over unchanged state writes nothing new (content-hash dedup).
	before := snapshot(t, out)
	if code := run([]string{"--state-dir", state, "--out-root", out, "--no-gitleaks"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("second harvest exited %d", code)
	}
	if after := snapshot(t, out); !equalSnapshot(before, after) {
		t.Fatalf("re-harvest was not idempotent:\n before=%v\n after=%v", before, after)
	}
}

// A personal-source harvest with --keep-values must force shape-scrub and still
// strip the planted secret (decision 6).
func TestHarvestPersonalSourceForcesScrub(t *testing.T) {
	state := writeFixtureState(t)
	t.Setenv("SERF_STATE_DIR", state) // makes this an explicit (non-personal) source override
	t.Setenv("SERF_FUZZ_CAPTURE_ENV", "1")
	out := t.TempDir()

	// Even with capture-env + keep-values, an explicit override is allowed to keep
	// values; assert the run succeeds and the known secret is still redacted.
	code := run([]string{"--state-dir", state, "--out-root", out, "--surface", "toolargs", "--keep-values", "--no-gitleaks"},
		&bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("keep-values harvest exited %d", code)
	}
	walkAssertNoSecret(t, out)
}

func assertNonEmptyCorpusDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected seeds in %s (err=%v)", dir, err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		assertValidCorpusFile(t, dir, raw)
	}
}

// assertValidCorpusFile checks the Go fuzz corpus format: the header line plus
// one parseable Go literal per argument.
func assertValidCorpusFile(t *testing.T, dir string, raw []byte) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 2 || lines[0] != "go test fuzz v1" {
		t.Fatalf("%s: bad corpus header: %q", dir, raw)
	}
	for _, line := range lines[1:] {
		if !parseCorpusArg(line) {
			t.Fatalf("%s: unparseable corpus arg %q", dir, line)
		}
	}
}

func parseCorpusArg(line string) bool {
	switch {
	case strings.HasPrefix(line, "[]byte(") && strings.HasSuffix(line, ")"):
		_, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "[]byte("), ")"))
		return err == nil
	case strings.HasPrefix(line, "string(") && strings.HasSuffix(line, ")"):
		_, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "string("), ")"))
		return err == nil
	case strings.HasPrefix(line, "int(") && strings.HasSuffix(line, ")"):
		_, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "int("), ")"))
		return err == nil
	case strings.HasPrefix(line, "uint8(") && strings.HasSuffix(line, ")"):
		_, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(line, "uint8("), ")"))
		return err == nil
	default:
		return false
	}
}

func walkAssertNoSecret(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error { //nolint:errcheck
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable/dir entries during the scan
		}
		raw, _ := os.ReadFile(path)
		if bytes.Contains(raw, []byte("sk-")) || bytes.Contains(raw, []byte(plantedHarvestSecret)) {
			t.Fatalf("planted secret leaked into %s:\n%s", path, raw)
		}
		return nil
	})
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}

func snapshot(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error { //nolint:errcheck
		if err == nil && !d.IsDir() {
			out[path] = true
		}
		return nil
	})
	return out
}

func equalSnapshot(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
