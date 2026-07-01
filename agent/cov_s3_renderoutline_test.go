package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// s3cov_assistantCall builds an assistant turn issuing the named tool calls.
func s3cov_assistantCall(text string, calls ...*llm.ToolCallData) schema.Turn {
	msg := llm.Assistant(text)
	for _, c := range calls {
		msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: c})
	}
	return schema.NewTurn(schema.TurnAssistant, msg)
}

func TestS3Cov_RenderOutline_ToolCallsAndStatus(t *testing.T) {
	t.Parallel()

	entries := s3cov_entries(
		schema.NewTurn(schema.TurnUserInput, llm.User("do a thing")),
		s3cov_assistantCall("reading files",
			&llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"purpose":"inspect"}`)},
			&llm.ToolCallData{ID: "c2", Name: "grep"},
		),
		schema.NewTurn(schema.TurnToolResults, twoResults("c1", "ok content", false, "c2", "match", false)),
	)

	content, truncated, elided := renderOutline(entries, 0, len(entries)-1)
	if truncated || elided != 0 {
		t.Fatalf("unexpected truncation")
	}
	// Tool names appear in call order, with an aggregated ok status.
	if !strings.Contains(content, "read_file, grep") {
		t.Fatalf("outline missing tool names:\n%s", content)
	}
	if !strings.Contains(content, "· ok ·") && !strings.Contains(content, "· ok") {
		t.Fatalf("outline missing ok status:\n%s", content)
	}
	// The TOOL_RESULTS turn folds under the assistant — no standalone line for seq 2.
	if strings.Contains(content, "\n2 · ") {
		t.Fatalf("tool-results turn should fold, not get its own line:\n%s", content)
	}
}

func TestS3Cov_RenderOutline_JobLifecycleBracket(t *testing.T) {
	t.Parallel()

	// A delegate call whose result carries a jobResult body → audit-pivot bracket.
	jobBody := `{"job_id":"J9","status":"completed","transcript_ref":"local:child"}`
	entries := s3cov_entries(
		s3cov_assistantCall("delegating",
			&llm.ToolCallData{ID: "d1", Name: "delegate"},
		),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("d1", jobBody, false)),
	)

	content, _, _ := renderOutline(entries, 0, len(entries)-1)
	if !strings.Contains(content, "delegate[status=completed child=local:child]") {
		t.Fatalf("outline missing lifecycle bracket:\n%s", content)
	}
}

func TestS3Cov_RenderOutline_ErrorStatus(t *testing.T) {
	t.Parallel()
	entries := s3cov_entries(
		s3cov_assistantCall("try",
			&llm.ToolCallData{ID: "c1", Name: "shell"},
		),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("c1", "boom", true)),
	)
	content, _, _ := renderOutline(entries, 0, len(entries)-1)
	if !strings.Contains(content, "error") {
		t.Fatalf("expected error status in outline:\n%s", content)
	}
}

// twoResults builds a single TOOL_RESULTS message answering two calls.
func twoResults(id1, c1 string, e1 bool, id2, c2 string, e2 bool) llm.Message {
	m := llm.ToolResult(id1, c1, e1)
	r2 := llm.ToolResult(id2, c2, e2)
	m.Content = append(m.Content, r2.Content...)
	return m
}
