package openai

import (
	"os"
	"strings"
	"testing"
)

func requireLiveOpenAIKey(t *testing.T) string {
	t.Helper()
	if os.Getenv("SERF_LIVE_TESTS") != "1" {
		t.Skip("set SERF_LIVE_TESTS=1 to run live OpenAI integration tests")
	}
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	return key
}
