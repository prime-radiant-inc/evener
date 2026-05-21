package agent

func testOpenAIProfileWithContextWindow(contextWindow int) ProviderProfile {
	return &baseProfile{
		id:            "openai",
		model:         "test",
		contextWindow: contextWindow,
	}
}
