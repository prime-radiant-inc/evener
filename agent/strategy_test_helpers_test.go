package agent

func testOpenAIProfileWithContextWindow(contextWindow int) *Profile {
	return &Profile{
		id:            "openai",
		model:         "test",
		contextWindow: contextWindow,
	}
}
