package llm

import (
	"strings"
	"testing"
)

const (
	signatureReasoningItem = `[{"type":"reasoning.text","text":"","signature":"sig"}]`
	encryptedReasoningItem = `[{"type":"reasoning.encrypted","data":"abc"}]`
)

func TestTrimThinkingForReplay(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("t", 400)

	t.Run("plain thinking drops", func(t *testing.T) {
		part := ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{Text: text}}
		if _, dropped := TrimThinkingForReplay(part, true); !dropped {
			t.Fatal("plain chat reasoning must drop")
		}
	})
	t.Run("redacted thinking drops", func(t *testing.T) {
		part := ContentPart{Kind: ContentRedThinking, Thinking: &ThinkingData{Text: text}}
		if _, dropped := TrimThinkingForReplay(part, true); !dropped {
			t.Fatal("redacted thinking must drop")
		}
	})
	t.Run("encrypted details keep the blob and clear the display text", func(t *testing.T) {
		part := ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: encryptedReasoningItem, Text: text}}
		trimmed, dropped := TrimThinkingForReplay(part, true)
		if dropped || trimmed.Thinking == nil || trimmed.Thinking.EncryptedContent != encryptedReasoningItem || trimmed.Thinking.Text != "" {
			t.Fatalf("trimmed part = %+v, want the blob with no text", trimmed.Thinking)
		}
	})
	t.Run("a signature item keeps the text it needs", func(t *testing.T) {
		part := ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: signatureReasoningItem, Text: text}}
		trimmed, dropped := TrimThinkingForReplay(part, true)
		if dropped || trimmed.Thinking == nil || trimmed.Thinking.Text != text {
			t.Fatalf("trimmed part = %+v, want the signed text kept", trimmed.Thinking)
		}
	})
	t.Run("a target that keeps thinking is untouched", func(t *testing.T) {
		part := ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{Text: text}}
		trimmed, dropped := TrimThinkingForReplay(part, false)
		if dropped || trimmed.Thinking != part.Thinking {
			t.Fatal("a replaying target must keep the part as is")
		}
	})
}

// TestChatReasoningCharsBillsSignatureText pins the estimate side of the same
// rule: a signature-bearing reasoning.text item's text is serialized by the
// builder, so it must be billed even for a row whose plain text is otherwise
// kept off the wire (and not at all when the details array is off the wire).
func TestChatReasoningCharsBillsSignatureText(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("t", 400)
	on := func(withText bool) int {
		thinking := &ThinkingData{EncryptedContent: signatureReasoningItem}
		if withText {
			thinking.Text = text
		}
		return chatReasoningChars(targetInfo{}, ContentPart{Kind: ContentThinking, Thinking: thinking})
	}
	if got, want := on(true), len(signatureReasoningItem)+len(text); got != want {
		t.Fatalf("signature text billing = %d, want %d", got, want)
	}
	if got, want := on(false), len(signatureReasoningItem); got != want {
		t.Fatalf("empty signature text billing = %d, want %d", got, want)
	}
	off := targetInfo{reasoningOff: true}
	part := ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: signatureReasoningItem, Text: text}}
	if got := chatReasoningChars(off, part); got != 0 {
		t.Fatalf("a declared non-reasoning row bills %d, want 0: the details array never rides", got)
	}
}
