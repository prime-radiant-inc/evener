package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// Only the LLM boundary is scripted. The reopened session dispatches native
// memory_read through the real independent confined-file environments.
type memoryPreservationAdapter struct {
	mu    sync.Mutex
	stage int
	reads int
}

func (*memoryPreservationAdapter) Name() string { return "openai" }
func (*memoryPreservationAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *memoryPreservationAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(req.Tools) == 0 {
		return llm.Response{Message: llm.Assistant("fixture title")}, nil
	}
	if a.stage > 0 {
		var result *llm.ToolResultData
		for _, msg := range req.Messages {
			for _, part := range msg.Content {
				if part.ToolResult != nil {
					result = part.ToolResult
				}
			}
		}
		if result == nil || result.IsError || !strings.Contains(fmt.Sprint(result.Content), "opaque-surviving-hub-301") {
			return llm.Response{}, fmt.Errorf("surviving wiki read failed: %+v", result)
		}
		a.reads++
	}
	if a.stage == 2 {
		raw, _ := json.Marshal(map[string]any{"message": "surviving wiki read", "end_turn": true, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}})
		call := llm.ToolCallData{ID: "memory-finished", Name: "communicate", Type: "function", Arguments: raw}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}, nil
	}
	scope := []string{"personal", "project"}[a.stage]
	a.stage++
	raw, _ := json.Marshal(map[string]any{"scope": scope, "file_path": "fact.md", "intent": "Reading surviving fixture bytes"})
	call := llm.ToolCallData{ID: "memory-" + scope, Name: "memory_read", Type: "function", Arguments: raw}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}, nil
}

func TestMemoryPreservationHistoryCleanup(t *testing.T) {
	t.Parallel()
	root, workspace := t.TempDir(), t.TempDir()
	project, err := identifier.ResolveProject(workspace)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	target, survivor := hubtest.SessionID(t), hubtest.SessionID(t)
	writeSession(t, stateDir, target, project.CanonicalPath)
	writeSession(t, stateDir, survivor, project.CanonicalPath)
	// Personal and project memory are pages. Session memory is a directory no
	// session reads any more; its old index must survive untouched.
	paths := []string{filepath.Join(root, "memory", "personal", "fact.md"), filepath.Join(root, "memory", "projects", project.ID, "fact.md"), filepath.Join(root, "memory", "sessions", target, "MEMORY.md")}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("opaque-surviving-hub-301"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: root, StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries()})
	resp := mustDeleteSession(t, web, target)
	if len(resp.Deleted) != 1 || resp.Deleted[0] != target || len(resp.Skipped) != 0 {
		t.Fatalf("session deletion=%+v", resp)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", target+".meta.json")); !os.IsNotExist(err) {
		t.Fatalf("target history remains err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", survivor+".meta.json")); err != nil {
		t.Fatalf("unrelated history lost err=%v", err)
	}
	for _, path := range paths {
		if got, err := os.ReadFile(path); err != nil || string(got) != "opaque-surviving-hub-301" {
			t.Fatalf("session cleanup wiki=%q err=%v", got, err)
		}
	}
	deleted, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: project.CanonicalPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.Deleted) != 1 || deleted.Deleted[0] != survivor || len(deleted.Skipped) != 0 {
		t.Fatalf("project deletion=%+v", deleted)
	}
	for _, id := range []string{target, survivor} {
		for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".log.jsonl", ".api.jsonl", ".future-artifact", ""} {
			if _, err := os.Stat(filepath.Join(stateDir, "sessions", id+suffix)); !os.IsNotExist(err) {
				t.Fatalf("project history %s%s remains err=%v", id, suffix, err)
			}
		}
		if _, ok := past.Find(id); ok {
			t.Fatalf("deleted history %s remains indexed", id)
		}
	}
	for _, path := range paths {
		if got, err := os.ReadFile(path); err != nil || string(got) != "opaque-surviving-hub-301" {
			t.Fatalf("project cleanup wiki=%q err=%v", got, err)
		}
	}
	reopened, err := identifier.ResolveProject(workspace)
	if err != nil || reopened.ID != project.ID {
		t.Fatalf("canonical reopen=%+v err=%v", reopened, err)
	}
	adapter := &memoryPreservationAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	s, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workspace), agent.SessionConfig{StateDir: stateDir, MemoryStateRoot: root, MemoryProjectID: reopened.ID, Project: reopened, AgentsDocPath: filepath.Join(t.TempDir(), "absent-AGENTS.md")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ProcessInput(context.Background(), "read surviving fixture wiki", nil); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	reads := adapter.reads
	adapter.mu.Unlock()
	if reads != 2 {
		t.Fatalf("reopened scoped reads=%d", reads)
	}
	for _, path := range paths {
		if got, err := os.ReadFile(path); err != nil || string(got) != "opaque-surviving-hub-301" {
			t.Fatalf("reopened session changed %s=%q err=%v", path, got, err)
		}
	}
}
