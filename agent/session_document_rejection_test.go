package agent

// Regression test for the #4465 review's medium finding: a provider that
// rejects a tool-result document used to fail the whole turn and resend the
// same bytes on every later request. The rejection now suppresses those bytes
// for the session, the round retries once, and the transcript keeps them.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func docRejectionHasDocument(req llm.Request) bool {
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && len(p.ToolResult.ImageData) > 0 && llm.IsDocumentMediaType(p.ToolResult.ImageMediaType) {
				return true
			}
		}
	}
	return false
}

func docRejectionToolText(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
				if s, ok := p.ToolResult.Content.(string); ok {
					b.WriteString(s)
				}
			}
		}
	}
	return b.String()
}

func TestProviderDocumentRejectionDropsThePayloadAndContinues(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	adapter := &agenttest.ScriptedAdapter{
		Provider: "anthropic",
		FaultResponder: func(llm.Request) error {
			if calls.Add(1) == 1 {
				return llm.ErrorFromHTTPStatus("anthropic", 400, "invalid document", nil, nil)
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "ok"))
		},
	}
	profile := newAnthropicProfile("claude-opus-4-6")
	if !profile.SeesToolResultMedia("application/pdf") {
		t.Fatal("test premise: the fixture Anthropic row must see tool-result documents")
	}
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "call_pdf", Name: "read_file", Content: "[document: a.pdf]",
			ImageData: []byte("%PDF-1.4"), ImageMediaType: "application/pdf",
		},
	}}})

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if !docRejectionHasDocument(reqs[0]) {
		t.Fatal("first request did not carry the document")
	}
	// The session's own retry accounting is the 400 attempt; find the first
	// request after it that carries no document.
	if len(reqs) < 2 || docRejectionHasDocument(reqs[1]) {
		t.Fatalf("retry still carried the rejected document: %d requests", len(reqs))
	}
	if !strings.Contains(docRejectionToolText(reqs[1]), "[document: a.pdf]") {
		t.Fatal("the retry lost the tool result's text")
	}

	// A later turn must keep omitting the rejected bytes.
	if _, err := sess.ProcessInput(ctx, "again", nil); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	reqs = adapter.Requests()
	if docRejectionHasDocument(reqs[len(reqs)-1]) {
		t.Fatal("a later request resent the provider-rejected document")
	}
}
