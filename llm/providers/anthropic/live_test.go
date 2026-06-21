package anthropic

import (
	"os"
	"testing"
)

func requireLiveAnthropic(t *testing.T) {
	t.Helper()
	if os.Getenv("SERF_LIVE_TESTS") != "1" {
		t.Skip("set SERF_LIVE_TESTS=1 to run live Anthropic integration tests")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
}
