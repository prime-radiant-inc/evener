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

func documentToolResultMessage(callID string) llm.Message {
	return llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: callID, Name: "read_file", Content: "[document: a.pdf]",
			ImageData: []byte("%PDF-1.4"), ImageMediaType: "application/pdf",
		},
	}}}
}

func docRejectionToolResult(req llm.Request) *llm.ToolResultData {
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == "call_pdf" {
				return p.ToolResult
			}
		}
	}
	return nil
}

func sessionHistoryHasDocument(sess *Session) bool {
	for _, t := range sess.history {
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && len(p.ToolResult.ImageData) > 0 &&
				llm.IsDocumentMediaType(p.ToolResult.ImageMediaType) {
				return true
			}
		}
	}
	return false
}

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

// TestProviderDocumentSuppressionRollsBackWhenRetryFailsForUnrelatedReason
// pins the review finding behind the session-wide suppression: a generic
// invalid request that happens to carry a previously accepted document must
// not permanently suppress it, and the speculative strip must roll back when
// the retry fails for the same unrelated reason.
func TestProviderDocumentSuppressionRollsBackWhenRetryFailsForUnrelatedReason(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	adapter := &agenttest.ScriptedAdapter{
		Provider: "anthropic",
		FaultResponder: func(llm.Request) error {
			switch calls.Add(1) {
			case 2, 3:
				return llm.ErrorFromHTTPStatus("anthropic", 400, "invalid parameter: temperature", nil, nil)
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "ok"))
		},
	}
	profile := newAnthropicProfile("claude-opus-4-6")
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnToolResults, documentToolResultMessage("call_pdf"))

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	// The second input's first request is rejected for an unrelated parameter;
	// the stripped retry is rejected the same way, so suppression must roll
	// back rather than outlive the retry.
	if _, err := sess.ProcessInput(ctx, "again", nil); err == nil {
		t.Fatal("the unrelated invalid request was expected to fail the turn")
	}
	if _, err := sess.ProcessInput(ctx, "third", nil); err != nil {
		t.Fatalf("third ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) < 4 {
		t.Fatalf("requests = %d, want 4", len(reqs))
	}
	if !docRejectionHasDocument(reqs[0]) {
		t.Fatal("test premise: the first request delivered the document")
	}
	if docRejectionHasDocument(reqs[2]) {
		t.Fatal("the speculative retry still carried the document")
	}
	if !docRejectionHasDocument(reqs[len(reqs)-1]) {
		t.Fatal("a generic invalid request permanently suppressed a previously accepted document")
	}
}

// TestProviderDocumentPayloadSizeRejectionRetriesWithoutDocuments pins the
// 413 half of the review: a request-size rejection is document recovery, not
// context pressure. The oversized payload is the newest tool result, which
// compaction preserves, so the retry must strip the documents instead.
func TestProviderDocumentPayloadSizeRejectionRetriesWithoutDocuments(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	adapter := &agenttest.ScriptedAdapter{
		Provider: "anthropic",
		FaultResponder: func(llm.Request) error {
			if calls.Add(1) == 1 {
				return llm.ErrorFromHTTPStatus("anthropic", 413, "request too large", nil, nil)
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "ok"))
		},
	}
	profile := newAnthropicProfile("claude-opus-4-6")
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnToolResults, documentToolResultMessage("call_pdf"))

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want a rejected request and its retry", len(reqs))
	}
	if !docRejectionHasDocument(reqs[0]) {
		t.Fatal("test premise: the rejected request carried the document")
	}
	if docRejectionHasDocument(reqs[1]) {
		t.Fatal("the 413 retry resent the oversized document payload")
	}
	if !sessionHistoryHasDocument(sess) {
		t.Fatal("the transcript lost the document payload")
	}
}

// TestProviderDocumentRejectionAnnotatesToolResultForTheModel pins the third
// review finding: dropping the bytes must leave the model a failure it can
// see, not a successful-looking placeholder, while the transcript keeps the
// payload.
func TestProviderDocumentRejectionAnnotatesToolResultForTheModel(t *testing.T) {
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
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnToolResults, documentToolResultMessage("call_pdf"))

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want a rejected request and its retry", len(reqs))
	}
	tr := docRejectionToolResult(reqs[1])
	if tr == nil {
		t.Fatal("the retry carried no tool result")
	}
	if len(tr.ImageData) != 0 {
		t.Fatal("the retry still carried the rejected bytes")
	}
	if !tr.IsError {
		t.Fatal("the model saw the stripped read as a successful result")
	}
	if !sessionHistoryHasDocument(sess) {
		t.Fatal("the transcript lost the document payload")
	}
}

// TestProviderDocumentSuppressionIgnoresHintProse pins the evidence rule: the
// classifier's configuration hint mentions the provider's documentation, which
// must not read as a document-specific rejection. A generic invalid request
// built through ClassifyHTTPError therefore strips speculatively and rolls
// back when its retry fails the same way.
func TestProviderDocumentSuppressionIgnoresHintProse(t *testing.T) {
	t.Parallel()
	profile := newAnthropicProfile("claude-opus-4-6")
	classified := llm.ClassifyHTTPError("messages", 400, nil,
		[]byte(`{"error":{"type":"invalid_request_error","code":"unknown_parameter","param":"foo","message":"unknown parameter: 'foo'"}}`),
		profile.Resolved())
	if !strings.Contains(classified.Error(), "documentation") {
		t.Fatalf("test premise: the classifier attached no documentation hint: %v", classified)
	}
	var calls atomic.Int32
	adapter := &agenttest.ScriptedAdapter{
		Provider: "anthropic",
		FaultResponder: func(llm.Request) error {
			switch calls.Add(1) {
			case 2, 3:
				return classified
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "ok"))
		},
	}
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnToolResults, documentToolResultMessage("call_pdf"))

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	if _, err := sess.ProcessInput(ctx, "again", nil); err == nil {
		t.Fatal("the unrelated invalid request was expected to fail the turn")
	}
	if _, err := sess.ProcessInput(ctx, "third", nil); err != nil {
		t.Fatalf("third ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) < 4 {
		t.Fatalf("requests = %d, want 4", len(reqs))
	}
	if !docRejectionHasDocument(reqs[len(reqs)-1]) {
		t.Fatal("a hint that mentions documentation permanently suppressed the document")
	}
}

// TestProviderDocumentSpeculationClearedWhenRoundCapAbandonsRetry pins the
// exit rule: a speculative strip whose retry never runs (the input's round cap
// is already spent) must not outlive the input.
func TestProviderDocumentSpeculationClearedWhenRoundCapAbandonsRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	adapter := &agenttest.ScriptedAdapter{
		Provider: "anthropic",
		FaultResponder: func(llm.Request) error {
			if calls.Add(1) == 1 {
				return llm.ErrorFromHTTPStatus("anthropic", 400, "invalid parameter: temperature", nil, nil)
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "ok"))
		},
	}
	profile := newAnthropicProfile("claude-opus-4-6")
	sess := newSession(t, withProfile(profile), withAdapter(adapter),
		withConfig(SessionConfig{MaxToolRoundsPerInput: 1, testOnly: testConfig{skipGitSnapshot: true}}))
	sess.appendTurn(schema.TurnToolResults, documentToolResultMessage("call_pdf"))

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The round cap abandons the retry, so this input ends without sending a
	// second request.
	_, _ = sess.ProcessInput(ctx, "continue", nil)
	if _, err := sess.ProcessInput(ctx, "again", nil); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want the abandoned input and the next", len(reqs))
	}
	if !docRejectionHasDocument(reqs[len(reqs)-1]) {
		t.Fatal("an abandoned speculative strip outlived its input")
	}
}
