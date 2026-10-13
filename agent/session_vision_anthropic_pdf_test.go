package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// A PDF tool result still takes the side-channel on an Anthropic session model
// that sees tool-result images inline: documents never count as natively seen
// (llm.DeliversToolResultMedia is image-only), so the description must still be
// generated, and the describe request must carry the document as a user-message
// document part.
func TestDescribeImage_AnthropicSessionStillDescribesPDFs(t *testing.T) {
	t.Parallel()
	anthropic := &fakeAdapter{name: "anthropic", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("a pdf about cats")} },
	}}
	profile := newAnthropicProfile("claude-opus-4-6")
	if !profile.AcceptsDocumentInput() {
		t.Fatal("test premise: the fixture Anthropic row must declare pdf input")
	}
	sess := newSession(t, withAdapter(anthropic), withProfile(profile))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData: []byte("%PDF-1.4 fake"), ImageMediaType: "application/pdf",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "a pdf about cats" {
		t.Fatalf("result = %+v, want the side-channel document description", result)
	}
	reqs := anthropic.Requests()
	if len(reqs) != 1 {
		t.Fatalf("side-channel calls = %d, want 1", len(reqs))
	}
	media := reqs[0].Messages[0].Content[1]
	if media.Kind != llm.ContentDocument {
		t.Fatalf("describe media part = %v, want ContentDocument", media.Kind)
	}
}
