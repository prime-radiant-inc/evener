package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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
			&llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"intent":"inspect"}`)},
			&llm.ToolCallData{ID: "c2", Name: "grep"},
		),
		schema.NewTurn(schema.TurnToolResults, s3cov_twoResults("c1", "ok content", false, "c2", "match", false)),
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

// s3cov_twoResults builds a single TOOL_RESULTS message answering two calls.
func s3cov_twoResults(id1, c1 string, e1 bool, id2, c2 string, e2 bool) llm.Message {
	m := llm.ToolResult(id1, c1, e1)
	r2 := llm.ToolResult(id2, c2, e2)
	m.Content = append(m.Content, r2.Content...)
	return m
}

// A compaction fold's record is durable resume bookkeeping with no
// model-visible content; the outline (which promises to mirror the markdown
// renderer) must emit no line for it, and skipping it must not renumber the
// turns around it — line numbers are entry indices.
func TestS3Cov_RenderOutline_FoldRecordSkippedNumberingUnchanged(t *testing.T) {
	t.Parallel()
	entries := s3cov_entries(
		schema.NewTurn(schema.TurnUserInput, llm.User("do a thing")),
		schema.NewTurn(schema.TurnSummary, llm.User("summary of earlier work")),
		schema.Turn{Kind: schema.TurnFoldRecord, Fold: &schema.FoldRecord{FoldID: "fold-1", Layers: []int{1}, RetainedSeqs: []int{0}}},
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("after the fold")),
	)
	content, _, _ := renderOutline(entries, 0, len(entries)-1)
	if strings.Contains(content, "FOLD_RECORD") {
		t.Fatalf("outline emitted a FOLD_RECORD line:\n%s", content)
	}
	if strings.Contains(content, "2 · ") {
		t.Fatalf("outline emitted a line for the fold record at index 2:\n%s", content)
	}
	// Numbering is unchanged: the post-fold assistant turn keeps entry index 3,
	// it is not renumbered down to fill the skipped record's slot.
	if !strings.Contains(content, "3 · ") {
		t.Fatalf("outline renumbered turns after skipping the fold record:\n%s", content)
	}
}
