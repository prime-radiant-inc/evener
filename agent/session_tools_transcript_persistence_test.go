package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

func TestSessionExplicitAPILogReadStaysOutOfSemanticTranscript(t *testing.T) {
	const (
		bodySentinel   = "PRIVATE_API_BODY_SENTINEL"
		headerSentinel = "PRIVATE_API_HEADER_SENTINEL"
	)
	stateDir := newBucket(t)
	targetSessionID := identifier.MustNewSessionID()
	writeFindSession(t, stateDir, findMetaSpec{id: targetSessionID, updated: time.Now().UTC()}, "target semantic turn")
	requestBody := bodySentinel + strings.Repeat("x", defaultExpansionBytes)
	attempt := testAPIAttemptRecord("ag_private_read", 1, []byte(requestBody), []byte("response"))
	attempt.Request.Headers = apilog.EncodedHeader{"X-Trace": []string{headerSentinel}}
	writeTestAPILog(t, stateDir, targetSessionID, attempt)

	readArgs, err := json.Marshal(map[string]any{
		"transcript_ref": "local:" + targetSessionID,
		"source":         apiLogSource,
		"attempt_id":     attempt.AttemptID,
		"body":           "request",
	})
	if err != nil {
		t.Fatalf("marshal API-log read arguments: %v", err)
	}
	readCall := llm.ToolCallData{
		ID:        "call_private_api_log",
		Name:      "read_session_transcript",
		Arguments: readArgs,
		Type:      "function",
	}

	adapter := &agenttest.ScriptedAdapter{Provider: "openai"}
	adapter.Responder = func(req llm.Request) llm.Response {
		if messagesContainToolResultText(req.Messages, bodySentinel) && messagesContainToolResultText(req.Messages, headerSentinel) {
			return agenttest.FinalResponse("private API evidence was available in live context")
		}
		return agenttest.ToolCallResponse(readCall)
	}
	client := llm.NewClient()
	client.Register(adapter)
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

	if _, err := sess.ProcessInput(context.Background(), "inspect the private attempt", nil); err != nil {
		sess.Close()
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	liveEvidenceSeen := false
	for _, req := range requests {
		if messagesContainToolResultText(req.Messages, bodySentinel) && messagesContainToolResultText(req.Messages, headerSentinel) {
			liveEvidenceSeen = true
			break
		}
	}
	if !liveEvidenceSeen {
		sess.Close()
		t.Fatalf("scripted provider never received the explicit API-log evidence: requests=%d", len(requests))
	}

	reRead := sess.execTool(context.Background(), readCall)
	if reRead.IsError || !strings.Contains(reRead.Output, bodySentinel) || !strings.Contains(reRead.Output, headerSentinel) {
		sess.Close()
		t.Fatalf("explicit API-log re-read lost private evidence: error=%v output=%q", reRead.IsError, reRead.Output)
	}
	transcriptPath := sess.TranscriptPath()
	sess.Close()

	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read semantic transcript: %v", err)
	}
	transcriptText := string(transcriptBytes)
	for _, forbidden := range []string{bodySentinel, headerSentinel} {
		if strings.Contains(transcriptText, forbidden) {
			t.Fatalf("semantic transcript persisted private API evidence %q:\n%s", forbidden, transcriptText)
		}
	}
	_, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("decode semantic transcript: %v", err)
	}
	var placeholderText string
	for _, entry := range entries {
		for _, part := range entry.Turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == readCall.ID {
				placeholderText, _ = part.ToolResult.Content.(string)
			}
		}
	}
	if placeholderText == "" || len(placeholderText) > apiLogTranscriptPlaceholderMaxBytes {
		t.Fatalf("persisted API-log placeholder length = %d, want 1..%d", len(placeholderText), apiLogTranscriptPlaceholderMaxBytes)
	}
	var placeholder apiLogTranscriptPlaceholder
	if err := json.Unmarshal([]byte(placeholderText), &placeholder); err != nil {
		t.Fatalf("decode API-log placeholder: %v", err)
	}
	if placeholder.Source != apiLogSource || !placeholder.PrivateEvidenceOmitted {
		t.Fatalf("API-log placeholder identity = %+v", placeholder)
	}
	if placeholder.ReRead.Tool != readCall.Name ||
		placeholder.ReRead.TranscriptRef != "local:"+targetSessionID ||
		placeholder.ReRead.Source != apiLogSource ||
		placeholder.ReRead.AttemptID != attempt.AttemptID ||
		placeholder.ReRead.Body != "request" ||
		placeholder.ReRead.OffsetBytes != 0 {
		t.Fatalf("API-log re-read handle = %+v", placeholder.ReRead)
	}
	if placeholder.Continuation == nil ||
		placeholder.Continuation.Tool != readCall.Name ||
		placeholder.Continuation.TranscriptRef != "local:"+targetSessionID ||
		placeholder.Continuation.Source != apiLogSource ||
		placeholder.Continuation.AttemptID != attempt.AttemptID ||
		placeholder.Continuation.Body != "request" ||
		placeholder.Continuation.OffsetBytes != defaultExpansionBytes {
		t.Fatalf("API-log continuation handle = %+v", placeholder.Continuation)
	}
	ordinary, ok := findToolResultInEntries(entries, "communicate_test_call")
	if !ok || ordinary.Content != `{"accepted":true,"end_turn":true,"inbox":[]}` {
		t.Fatalf("ordinary tool result changed = %+v, found=%v", ordinary, ok)
	}
	if len(entries) == 0 || entries[len(entries)-1].Turn.Kind != schema.TurnToolResults {
		t.Fatalf("transcript tail = %+v, want final tool-results turn", entries)
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
