package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// apptranscriptNewTurnCacheForBench exists only to name the constructor this
// benchmark needs without importing the package solely for that symbol in the
// loop body; keep it trivial.
var apptranscriptNewTurnCacheForBench = apptranscript.NewTurnCache

// seedLargePastThread builds one past session with a large synthetic transcript
// (thousands of user/assistant/tool turns carrying usage, tool calls, and failed
// tool results — the shape the derived usage/failure scans walk) and returns the
// WebConfig plus the thread/read params a client's initial past-session click
// issues. It is the benchmark counterpart of seedBoundedPastThread.
func seedLargePastThread(tb testing.TB, rounds int) (hubcore.WebConfig, appwire.ThreadReadParams, string) {
	tb.Helper()
	root := tb.TempDir()
	stateDir := filepath.Join(root, "projects", "project-bench-0000000000")
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	sessions := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		tb.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5", TurnCount: rounds,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/project"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(sessions, sessionID+".transcript.jsonl")
	file, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	// Buffered writes: seeding is input fixture construction, not the measured
	// read, and the transcript read back is byte-identical either way.
	writer := newSynclessBufferedWriter(file)
	writeLine := func(v any) {
		line, err := json.Marshal(v)
		if err != nil {
			tb.Fatal(err)
		}
		writer.Write(append(line, '\n'))
	}
	writeLine(transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5"})
	seq := 0
	assistantText := strings.Repeat("a long assistant answer line that mirrors real transcript density ", 8)
	cacheRead := 800
	for i := range rounds {
		seq++
		writeLine(transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{
			Kind: schema.TurnUserInput, Message: llm.User(fmt.Sprintf("round %d request", i)),
			Timestamp: time.Unix(1_700_000_000+int64(seq), 0).UTC(),
		}})
		seq++
		writeLine(transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: assistantText},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: fmt.Sprintf("call_%d", seq), Name: "shell", Arguments: json.RawMessage(`{"argv":["ls","-la"]}`)}},
			}},
			Usage:     llm.Usage{InputTokens: 1200, OutputTokens: 340, CacheReadTokens: &cacheRead},
			Timestamp: time.Unix(1_700_000_000+int64(seq), 0).UTC(),
		}})
		seq++
		// Every eighth tool result fails (IsError), so the failure scan has real
		// work; the rest carry a nonzero exit code every sixteenth call.
		seq++
		failed := i%8 == 0
		if i%16 == 0 {
			// A shell result whose command failed but whose result is not an
			// error — the case the failure rule reads from opaque tool state.
			writeLine(transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{
				Kind: schema.TurnToolResults,
				Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
					Kind: llm.ContentToolResult,
					ToolResult: &llm.ToolResultData{
						ToolCallID: fmt.Sprintf("call_%d", seq-1), Name: "shell",
						Content:   strings.Repeat("command failed output ", 2),
						ToolState: json.RawMessage(`{"exit_code":1}`),
					},
				}}},
				Timestamp: time.Unix(1_700_000_000+int64(seq), 0).UTC(),
			}})
			continue
		}
		writeLine(transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID: fmt.Sprintf("call_%d", seq-1), Name: "shell",
					Content: strings.Repeat("tool output text mirroring a real result body ", 6),
					IsError: failed,
				},
			}}},
			Timestamp: time.Unix(1_700_000_000+int64(seq), 0).UTC(),
		}})
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		tb.Fatal(err)
	}
	return hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{
		Ref: "local:" + sessionID, IncludeTurns: true, TurnLimit: 40,
	}, path
}

// synclessBufferedWriter buffers transcript fixture writes so seeding a large
// synthetic transcript does not pay one write syscall per entry.
type synclessBufferedWriter struct {
	file *os.File
	buf  []byte
}

func newSynclessBufferedWriter(file *os.File) *synclessBufferedWriter {
	return &synclessBufferedWriter{file: file}
}

func (w *synclessBufferedWriter) Write(p []byte) {
	w.buf = append(w.buf, p...)
	if len(w.buf) >= 1<<20 {
		w.flush()
	}
}

func (w *synclessBufferedWriter) flush() {
	if len(w.buf) == 0 {
		return
	}
	if _, err := w.file.Write(w.buf); err != nil {
		panic(err)
	}
	w.buf = w.buf[:0]
}

func (w *synclessBufferedWriter) Close() error {
	w.flush()
	return w.file.Close()
}

// BenchmarkPastThreadReadResponseCold times a past session's initial
// thread/read with a cold transcript cache and no on-disk turn index — the
// first read after hub start or LRU eviction. Each iteration re-creates the
// in-memory cache and removes the sidecar files the bounded reader persists,
// so every pass pays the full cold path: turn-index build, derived usage scan,
// derived failure scan, and projection of the latest 40 turns.
func BenchmarkPastThreadReadResponseCold(b *testing.B) {
	for _, rounds := range []int{4_000, 25_000} {
		b.Run(fmt.Sprintf("rounds=%d", rounds), func(b *testing.B) {
			cfg, params, path := seedLargePastThread(b, rounds)
			if info, err := os.Stat(path); err == nil {
				b.Logf("transcript %s: %.1f MB", filepath.Base(path), float64(info.Size())/(1<<20))
			}
			sidecars := []string{path + ".appwire-index.json", path + ".appwire-index.json.journal"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				for _, sidecar := range sidecars {
					if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
						b.Fatal(err)
					}
				}
				pastTranscriptCache = apptranscriptNewTurnCacheForBench()
				b.StartTimer()
				response, found, err := pastThreadReadResponse(cfg, params)
				if err != nil || !found {
					b.Fatalf("pastThreadReadResponse = %v, %v", err, found)
				}
				if response.Thread.Evener.Usage == nil {
					b.Fatal("bench response missing derived usage")
				}
				if response.Thread.Evener.FailedToolCalls == nil {
					b.Fatal("bench response missing derived failure count")
				}
			}
		})
	}
}
