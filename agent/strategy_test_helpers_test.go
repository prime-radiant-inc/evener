package agent

import "primeradiant.com/serf/agent/provider"

func testOpenAIProfileWithContextWindow(contextWindow int) *provider.Profile {
	return testProfile("openai", "test", contextWindow)
}
