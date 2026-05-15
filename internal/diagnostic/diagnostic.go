package diagnostic

import (
	"errors"
	"strings"

	"primeradiant.com/serf/llm"
)

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
		return serfConfiguration()
	case isHubFailure(lower):
		return hubFailure()
	case isProviderFailure(lower):
		return providerFailure()
	default:
		return serfFailure()
	}
}

func FromError(err error) Info {
	if err == nil {
		return serfFailure()
	}
	var cfg *llm.ConfigurationError
	if errors.As(err, &cfg) {
		return serfConfiguration()
	}
	var llmErr llm.Error
	if errors.As(err, &llmErr) && (strings.TrimSpace(llmErr.Provider()) != "" || llmErr.StatusCode() != 0 || strings.TrimSpace(llmErr.ErrorCode()) != "") {
		return providerFailure()
	}
	return Classify(err.Error())
}

func FromFields(source, title, hint, message string) Info {
	info := Classify(message)
	if src := normalizeSource(source); src != "" {
		info = defaultForSource(src, message)
	}
	if strings.TrimSpace(title) != "" {
		info.Title = strings.TrimSpace(title)
	}
	if strings.TrimSpace(hint) != "" {
		info.Hint = strings.TrimSpace(hint)
	}
	return info
}

func normalizeSource(source string) Source {
	switch Source(strings.ToLower(strings.TrimSpace(source))) {
	case SourceProvider:
		return SourceProvider
	case SourceSerf:
		return SourceSerf
	case SourceHub:
		return SourceHub
	case SourceUI:
		return SourceUI
	default:
		return ""
	}
}

func defaultForSource(source Source, message string) Info {
	switch source {
	case SourceProvider:
		return providerFailure()
	case SourceHub:
		return hubFailure()
	case SourceUI:
		return Info{
			Source: SourceUI,
			Title:  "UI error",
			Hint:   "Check the browser console and UI state.",
		}
	case SourceSerf:
		if isSerfConfiguration(strings.ToLower(strings.TrimSpace(message))) {
			return serfConfiguration()
		}
		return serfFailure()
	default:
		return Classify(message)
	}
}

func serfConfiguration() Info {
	return Info{
		Source: SourceSerf,
		Title:  "Serf configuration error",
		Hint:   "Hub launched Serf with provider configuration this Serf runtime does not recognize. Check the model/provider passed by Hub and the Serf binary Hub is using.",
	}
}

func providerFailure() Info {
	return Info{
		Source: SourceProvider,
		Title:  "Provider error",
		Hint:   "Check provider credentials, account access, rate limits, and the selected model.",
	}
}

func hubFailure() Info {
	return Info{
		Source: SourceHub,
		Title:  "Hub error",
		Hint:   "Check the hub process, AppWire connection, spawn arguments, and rendezvous state.",
	}
}

func serfFailure() Info {
	return Info{
		Source: SourceSerf,
		Title:  "Serf error",
		Hint:   "Check the Serf session log and daemon state.",
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
