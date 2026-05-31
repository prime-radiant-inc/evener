package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/llm"
)

// webSearch performs a web search by making a separate Gemini API call with
// google_search grounding enabled. This works around the Gemini API limitation
// where google_search cannot be combined with functionDeclarations in the same
// request. OpenAI and Anthropic handle web search natively alongside function
// calling; only Gemini needs this tool-based approach.
func (s *Session) webSearch(ctx context.Context, query string) (any, error) {
	// The request deliberately omits Tools so the Gemini adapter sends only
	// google_search in the tools array, avoiding the API conflict.
	p := s.currentProfile()
	req := llm.Request{
		Model:     p.Model(),
		Provider:  p.ID(),
		Messages:  []llm.Message{llm.User(query)},
		WebSearch: true,
	}

	resp, err := s.client.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("web search failed: %w", err)
	}

	return resp.Text(), nil
}
