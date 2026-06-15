package contextestimate

import (
	"encoding/json"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const mediaPartEstimatedChars = 1024

// EstimateTurnsTokens estimates token count for turns using the char/4 heuristic.
func EstimateTurnsTokens(turns []schema.Turn) int {
	chars := 0
	for _, t := range turns {
		chars += MessageCharCount(t.Message)
	}
	return chars / 4
}

// MessageCharCount returns the total estimated character count of the visible
// message payload. Inline media bytes are intentionally counted as bounded
// media placeholders; providers tokenize media by their own rules, and treating
// raw bytes or base64 JSON as text wildly overstates local context pressure.
func MessageCharCount(m llm.Message) int {
	n := 0
	n += len(m.Name)
	n += len(m.ToolCallID)
	for _, p := range m.Content {
		switch p.Kind {
		case llm.ContentText:
			n += len(p.Text)
		case llm.ContentImage:
			if p.Image != nil {
				n += mediaCharCount(p.Image.URL, p.Image.MediaType, p.Image.Detail, "")
			}
		case llm.ContentAudio:
			if p.Audio != nil {
				n += mediaCharCount(p.Audio.URL, p.Audio.MediaType, "", "")
			}
		case llm.ContentDocument:
			if p.Document != nil {
				n += mediaCharCount(p.Document.URL, p.Document.MediaType, "", p.Document.FileName)
			}
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				n += len(p.ToolCall.ID)
				n += len(p.ToolCall.Name)
				n += len(p.ToolCall.Arguments)
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				n += len(p.ToolResult.ToolCallID)
				n += len(p.ToolResult.Name)
				if len(p.ToolResult.ImageData) > 0 || p.ToolResult.ImageMediaType != "" {
					n += mediaCharCount("", p.ToolResult.ImageMediaType, "", "")
				}
				switch x := p.ToolResult.Content.(type) {
				case string:
					n += len(x)
				case []byte:
					n += len(x)
				default:
					b, _ := json.Marshal(x)
					n += len(b)
				}
			}
		case llm.ContentThinking, llm.ContentRedThinking:
			if p.Thinking != nil {
				n += len(p.Thinking.Text)
				n += len(p.Thinking.Signature)
			}
		default:
			// Fallback to a best-effort JSON encoding.
			b, _ := json.Marshal(p)
			n += len(b)
		}
	}
	return n
}

func mediaCharCount(url, mediaType, detail, fileName string) int {
	return mediaPartEstimatedChars + len(url) + len(mediaType) + len(detail) + len(fileName)
}
