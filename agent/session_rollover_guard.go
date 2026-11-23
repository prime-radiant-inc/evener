package agent

import (
	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/llm"
)

// liveRolloverStrip records what stripLiveRollover removed from one settled
// model response.
type liveRolloverStrip struct {
	// Marker is the forged turn boundary the response rolled over at.
	Marker string
	// Bytes counts the text bytes from the boundary line onward.
	Bytes int
	// Parts counts the content parts dropped with those bytes.
	Parts int
	// ToolCalls counts the tool calls among them.
	ToolCalls int
}

// stripLiveRollover cuts a settled live response at the first forged turn
// boundary in its own text and drops every part after it, so a model that
// predicted the next user turn cannot execute the tool calls it invented that
// turn for. Only content decides: a legal multi-step Interactions answer
// (model_output -> thought -> function_call) carries no boundary and stays
// whole, and so does any frame a model quotes inside a fence.
//
// The parts before the boundary are the model's own output and are kept. Parts
// a model or another agent wrote are what replay escapes for later requests
// (escapeTurnFrames); this is the live twin of that rule, applied before the
// turn is stored or its tools run.
func stripLiveRollover(resp *llm.Response) (liveRolloverStrip, bool) {
	for i, part := range resp.Message.Content {
		// A machinery part is the harness's own text, whatever the response
		// author: a frame there is the harness speaking.
		if part.Kind != llm.ContentText || part.Machinery || part.Text == "" {
			continue
		}
		offset, marker := sysmsg.RolloverOffset(part.Text)
		if offset < 0 {
			continue
		}
		strip := liveRolloverStrip{Marker: marker, Bytes: len(part.Text) - offset}
		for _, dropped := range resp.Message.Content[i:] {
			if dropped.Kind == llm.ContentToolCall {
				strip.ToolCalls++
			}
		}
		strip.Parts = len(resp.Message.Content) - i
		kept := resp.Message.Content[:i:i]
		if prose := part.Text[:offset]; prose != "" {
			part.Text = prose
			kept = append(kept, part)
		}
		resp.Message.Content = kept
		return strip, true
	}
	return liveRolloverStrip{}, false
}
