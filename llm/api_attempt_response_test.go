package llm

import (
	"testing"
	"time"
)

func TestBuildAPIAttemptRecordStoresCompactResponseCounts(t *testing.T) {
	started := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	response := Response{
		Message: Message{
			Role: RoleAssistant,
			Content: []ContentPart{
				{Kind: ContentText, Text: "hello"},
				{Kind: ContentToolCall, ToolCall: &ToolCallData{ID: "call_1", Name: "shell"}},
				{Kind: ContentToolCall, ToolCall: &ToolCallData{ID: "call_2", Name: "read_file"}},
			},
		},
	}
	record := buildAPIAttemptRecord("ag_test", "att_01K0QWERTYUIOPASDFGHJKLZX", 1, APIAttemptMeta{
		ProviderInstance: "openai",
		RequestModel:     "gpt-test",
		Method:           "POST",
		Endpoint:         "https://provider.test/v1/responses",
		StartedAt:        started,
	}, APIAttemptResult{
		StatusCode: 200,
		Response:   &response,
		Outcome:    "success",
		FinishedAt: started.Add(time.Second),
	})
	if record.Response == nil {
		t.Fatal("Response is nil")
	}
	if record.Response.TextLength == nil || *record.Response.TextLength != len(response.Text()) ||
		record.Response.ToolCallCount == nil || *record.Response.ToolCallCount != len(response.ToolCalls()) {
		t.Fatalf("compact counts = text %v tools %v, want text %d tools %d", record.Response.TextLength, record.Response.ToolCallCount, len(response.Text()), len(response.ToolCalls()))
	}
}
