package agent

import (
	"strings"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

const testSessionNamerProvider = "test-session-namer"

// withTestSessionNamer gives a session test an explicit fast-cheap model on a
// dedicated scripted provider so the background naming call cannot consume the
// main provider's response script.
func withTestSessionNamer(client *llm.Client, profile *provider.Profile) *provider.Profile {
	registerTestSessionNamer(client)
	return WithCheapModel(profile, testSessionNamerProvider+"/namer")
}

// The configured cheap model also serves compaction summaries, so it answers a
// summary prompt with a summary: the summarizer rejects anything else and
// falls back to the session model, which would consume the main script.
func registerTestSessionNamer(client *llm.Client) {
	client.Register(&agenttest.ScriptedAdapter{Provider: testSessionNamerProvider, Responder: func(request llm.Request) llm.Response {
		reply := `{"name":"Test Session"}`
		if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Text(), "CONTEXT CHECKPOINT COMPACTION") {
			reply = "## Progress\nTest session summary."
		}
		return llm.Response{
			Provider: testSessionNamerProvider,
			Model:    request.Model,
			Message:  llm.Assistant(reply),
		}
	}})
}

// updateSessionTestConfig synchronizes post-construction test seam changes with
// metadata snapshots taken by the asynchronous session namer.
func updateSessionTestConfig(session *Session, update func(*testConfig)) {
	session.mu.Lock()
	defer session.mu.Unlock()
	update(&session.cfg.testOnly)
}
