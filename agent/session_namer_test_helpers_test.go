package agent

import (
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

func registerTestSessionNamer(client *llm.Client) {
	client.Register(&agenttest.ScriptedAdapter{Provider: testSessionNamerProvider, Responder: func(request llm.Request) llm.Response {
		return llm.Response{
			Provider: testSessionNamerProvider,
			Model:    request.Model,
			Message:  llm.Assistant(`{"name":"Test Session"}`),
		}
	}})
}
