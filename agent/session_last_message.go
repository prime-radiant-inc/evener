package agent

import (
	"slices"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// lastAgentText is the text of the agent message an assistant response records
// last: its last text part that says something, the one the transcript shows
// last (internal/apptranscript projects each text part as its own agentMessage
// item). Reasoning, redacted reasoning and tool calls are not agent messages,
// so none of them reaches a row.
func lastAgentText(message llm.Message) string {
	for _, part := range slices.Backward(message.Content) {
		if part.Kind == llm.ContentText && strings.TrimSpace(part.Text) != "" {
			return part.Text
		}
	}
	return ""
}

// noteAgentMessageLocked records the opening of an agent message the session
// just wrote to its transcript, which its Finished row shows (S1d): one line
// cut to the wire's bound. A message with no text leaves the last one
// standing. The caller holds s.mu.
func (s *Session) noteAgentMessageLocked(text string) {
	if excerpt := appwire.Excerpt(text, appwire.MaxMessageExcerptRunes); excerpt != "" {
		s.lastMessage = excerpt
	}
}
