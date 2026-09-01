package hub

import (
	"context"
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
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		tb.Fatal(err)
	}
	// Input fixture, not a durability test: batch the writes so seeding a large
	// synthetic transcript does not pay one fsync per Append. Close still
	// flushes, so the transcript read back is byte-identical.
	writer.SyncInterval = time.Hour
	// seq numbers the synthetic turns for call IDs and timestamps; the entry's
	// own Seq is the writer's, incremented once per Append.
	seq := 0
	appendTurn := func(turn schema.Turn) {
		seq++
		turn.Timestamp = time.Unix(1_700_000_000+int64(seq), 0).UTC()
		if err := writer.Append(turn); err != nil {
			tb.Fatal(err)
		}
	}
	assistantText := strings.Repeat("a long assistant answer line that mirrors real transcript density ", 8)
	cacheRead := 800
	for i := range rounds {
		appendTurn(schema.Turn{
			Kind: schema.TurnUserInput, Message: llm.User(fmt.Sprintf("round %d request", i)),
		})
		callID := fmt.Sprintf("call_%d", seq)
		appendTurn(schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: assistantText},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: callID, Name: "shell", Arguments: json.RawMessage(`{"argv":["ls","-la"]}`)}},
			}},
			Usage: llm.Usage{InputTokens: 1200, OutputTokens: 340, CacheReadTokens: &cacheRead},
		})
		// Every sixteenth tool result is a shell result whose command failed
		// but whose result is not an error — the case the failure rule reads
		// from opaque tool state. Every other eighth fails via IsError, so the
		// failure scan has real work either way.
		toolResult := &llm.ToolResultData{ToolCallID: callID, Name: "shell"}
		if i%16 == 0 {
			toolResult.Content = strings.Repeat("command failed output ", 2)
			toolResult.ToolState = json.RawMessage(`{"exit_code":1}`)
		} else {
			toolResult.Content = strings.Repeat("tool output text mirroring a real result body ", 6)
			toolResult.IsError = i%8 == 0
		}
		appendTurn(schema.Turn{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult, ToolResult: toolResult,
			}}},
		})
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
				pastTranscriptCache = apptranscript.NewTurnCache()
				b.StartTimer()
				response, found, err := pastThreadReadResponse(context.Background(), cfg, params)
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
