package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// A PDF tool result now reaches an Anthropic session model natively as a
// document block inside tool_result (#4465), so the default native-vision
// setting skips the describing side-channel: a describe call would duplicate
// content the model already sees. The row's own pdf input is the prerequisite;
// the chat-route tests in session_vision_chat_document_test.go cover the
// routes that still need the side-channel.
func TestDescribeImage_AnthropicPDFToolResultSkipsTheSideChannel(t *testing.T) {
	t.Parallel()
	anthropic := &fakeAdapter{name: "anthropic", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("a pdf about cats")} },
	}}
	profile := newAnthropicProfile("claude-opus-4-6")
	if !profile.AcceptsDocumentInput(&llm.DocumentData{Data: []byte("%PDF-1.4 fake"), MediaType: "application/pdf"}) {
		t.Fatal("test premise: the fixture Anthropic row must declare pdf input")
	}
	sess := newSession(t, withAdapter(anthropic), withProfile(profile))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: "application/pdf",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "" {
		t.Fatalf("result = %+v, want the native-delivery skip", result)
	}
	if reqs := anthropic.Requests(); len(reqs) != 0 {
		t.Fatalf("side-channel calls = %d, want 0: the document rides inline", len(reqs))
	}
}
