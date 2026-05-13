package diagnostic

import "strings"

type Source string

const (
	SourceProvider Source = "provider"
	SourceSerf     Source = "serf"
	SourceHub      Source = "hub"
	SourceUI       Source = "ui"
)

type Info struct {
	Source Source
	Title  string
	Hint   string
}

func Classify(message string) Info {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case isSerfConfiguration(lower):
		return Info{
			Source: SourceSerf,
			Title:  "Serf configuration error",
			Hint:   "Hub launched Serf with provider configuration this Serf runtime does not recognize. Check the model/provider passed by Hub and the Serf binary Hub is using.",
		}
	case isHubFailure(lower):
		return Info{
			Source: SourceHub,
			Title:  "Hub error",
			Hint:   "Check the hub process, AppWire connection, spawn arguments, and rendezvous state.",
		}
	case isProviderFailure(lower):
		return Info{
			Source: SourceProvider,
			Title:  "Provider error",
			Hint:   "Check provider credentials, account access, rate limits, and the selected model.",
		}
	default:
		return Info{
			Source: SourceSerf,
			Title:  "Serf error",
			Hint:   "Check the Serf session log and daemon state.",
		}
	}
}

func isSerfConfiguration(message string) bool {
	return strings.Contains(message, "unknown provider") ||
		strings.Contains(message, "configuration error") ||
		strings.Contains(message, "must use provider/model") ||
		strings.Contains(message, "no model:")
}

func isHubFailure(message string) bool {
	return strings.Contains(message, "rendezvous") ||
		strings.Contains(message, "daemon spawn") ||
		strings.Contains(message, "resume timed out") ||
		strings.Contains(message, "process exited before rendezvous") ||
		strings.Contains(message, "appwire") ||
		strings.Contains(message, "websocket") ||
		strings.Contains(message, "stream failed") ||
		strings.Contains(message, "source not found")
}

func isProviderFailure(message string) bool {
	providers := []string{
		"openai",
		"anthropic",
		"google",
		"gemini",
		"openrouter",
		"ollama",
		"kimi",
		"glm",
		"minimax",
	}
	for _, provider := range providers {
		if strings.Contains(message, provider+" error") {
			return true
		}
	}
	return strings.Contains(message, "provider unavailable") ||
		strings.Contains(message, "api key") ||
		strings.Contains(message, "rate limit") ||
		strings.Contains(message, "quota") ||
		strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "invalid_grant") ||
		strings.Contains(message, "token endpoint")
}
