package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestS2Cov_PersistToolResults_VisionSteering exercises persistToolResults'
// image/document side-channel: each result carrying image bytes triggers a
// vision Complete call whose description is injected as steering, with the label
// varying by media type and the tool's file_path argument.
func TestS2Cov_PersistToolResults_VisionSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("a red square")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("a signed invoice")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("   ")} },
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, MaxSubagentDepth: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	calls := []llm.ToolCallData{
		{ID: "c1", Name: "read_file", Arguments: []byte(`{"file_path":"/tmp/a.png"}`)},
		{ID: "c2", Name: "read_file", Arguments: []byte(`{}`)},
		{ID: "c3", Name: "read_file", Arguments: []byte(`{"file_path":"/tmp/b.png"}`)},
	}
	results := []tool.ExecResult{
		{CallID: "c1", ToolName: "read_file", Output: "png", ImageData: []byte("png-bytes"), ImageMediaType: "image/png"},
		{CallID: "c2", ToolName: "read_file", Output: "pdf", ImageData: []byte("pdf-bytes"), ImageMediaType: "application/pdf"},
		{CallID: "c3", ToolName: "read_file", Output: "png2", ImageData: []byte("png-bytes-2"), ImageMediaType: "image/png"},
	}

	if err := sess.persistToolResults(context.Background(), calls, results); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	// A single aggregated tool-result turn was appended.
	if got := s2cov_lastTurnKind(sess); got != schema.TurnToolResults {
		t.Fatalf("last turn kind = %v, want TurnToolResults", got)
	}

	steered := sess.drainSteering()
	if len(steered) != 2 {
		t.Fatalf("steered messages = %d, want 2 (empty description suppressed)", len(steered))
	}
	if !strings.Contains(steered[0].Text, "Image description (from vision) for /tmp/a.png") || !strings.Contains(steered[0].Text, "a red square") {
		t.Fatalf("first steer = %q", steered[0].Text)
	}
	if !strings.Contains(steered[1].Text, "Document description (from content analysis)") || !strings.Contains(steered[1].Text, "a signed invoice") {
		t.Fatalf("second steer = %q", steered[1].Text)
	}
}

func s2cov_lastTurnKind(s *Session) schema.TurnKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) == 0 {
		return ""
	}
	return s.history[len(s.history)-1].Kind
}
