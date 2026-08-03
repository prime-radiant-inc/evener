package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestSessionCanceledAPILogReadStaysOutOfSemanticTranscript(t *testing.T) {
	const bodySentinel = "PRIVATE_CANCELED_API_BODY_SENTINEL"
	stateDir := newBucket(t)
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         stateDir,
		NoProjectPrompts: true,
		clock:            agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainSessionEvents(sess)

	args := json.RawMessage(`{"source":"api_log","attempt_id":"att_private"}`)
	calls := []llm.ToolCallData{{ID: "call_private_canceled", Name: "read_session_transcript", Arguments: args}}
	results := []tool.ExecResult{{
		ToolName: "read_session_transcript",
		CallID:   calls[0].ID,
		Output:   `{"source":"api_log","transcript_ref":"local:target","attempt":{"attempt_id":"att_private"},"private":"` + bodySentinel + `"}`,
	}}
	sess.appendCanceledToolResults(calls, results, context.Canceled)

	sess.mu.Lock()
	liveHistory := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if len(liveHistory) != 1 || !messagesContainToolResultText([]llm.Message{liveHistory[0].Message}, bodySentinel) {
		t.Fatalf("live history lost explicit API evidence: %+v", liveHistory)
	}
	transcriptPath := sess.TranscriptPath()
	sess.Close()

	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read semantic transcript: %v", err)
	}
	if strings.Contains(string(transcriptBytes), bodySentinel) {
		t.Fatalf("semantic transcript persisted canceled private API evidence:\n%s", transcriptBytes)
	}
	_, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("decode semantic transcript: %v", err)
	}
	persisted, ok := findToolResultInEntries(entries, calls[0].ID)
	if !ok {
		t.Fatal("semantic transcript omitted canceled tool result")
	}
	content, ok := persisted.Content.(string)
	if !ok || !strings.Contains(content, `"private_evidence_omitted":true`) {
		t.Fatalf("persisted canceled API-log result = %#v, want private placeholder", persisted.Content)
	}
}

func findToolResultInEntries(entries []transcript.Entry, callID string) (llm.ToolResultData, bool) {
	for _, entry := range entries {
		for _, part := range entry.Turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return *part.ToolResult, true
			}
		}
	}
	return llm.ToolResultData{}, false
}

func messagesContainToolResultText(messages []llm.Message, want string) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
				continue
			}
			if text, ok := part.ToolResult.Content.(string); ok && strings.Contains(text, want) {
				return true
			}
		}
	}
	return false
}
