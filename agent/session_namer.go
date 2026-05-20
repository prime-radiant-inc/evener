package agent

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/serf/llm"
)

const (
	sessionNameSourcePrompt     = "prompt"
	sessionNameSourceCompaction = "compaction"
	sessionNameMaxRunes         = 60
)

// SessionNameResult is the advisory output of the cheap-model session namer.
type SessionNameResult struct {
	Name   string
	Source string
	Usage  llm.Usage
}

// NameSession derives a short human-readable session title using a single cheap-model LLM call.
func NameSession(ctx context.Context, client *llm.Client, profile ProviderProfile, source, text string) (SessionNameResult, error) {
	if client == nil {
		return SessionNameResult{}, fmt.Errorf("session namer: llm client is nil")
	}
	if profile == nil {
		return SessionNameResult{}, fmt.Errorf("session namer: profile is nil")
	}
	source = normalizeSessionNameSource(source)
	text = strings.TrimSpace(text)
	if text == "" {
		return SessionNameResult{}, fmt.Errorf("session namer: source text is empty")
	}

	cheapModel := strings.TrimSpace(profile.CheapModel())
	if cheapModel == "" {
		cheapModel = profile.Model()
	}
	maxTokens := 80
	temp := 0.0
	res, err := llm.GenerateObject(ctx, llm.GenerateObjectOptions{
		GenerateOptions: llm.GenerateOptions{
			Client:      client,
			Provider:    profile.ID(),
			Model:       cheapModel,
			System:      ptrString(sessionNamerSystemPrompt),
			Prompt:      ptrString(sessionNamerUserPrompt(source, text)),
			Temperature: &temp,
			MaxTokens:   &maxTokens,
		},
		Schema: sessionNameSchema(),
	})
	if err != nil {
		return SessionNameResult{}, fmt.Errorf("session namer: %w", err)
	}

	obj, _ := res.Output.(map[string]any)
	name, _ := obj["name"].(string)
	name = sanitizeSessionName(name)
	if name == "" {
		return SessionNameResult{}, fmt.Errorf("session namer: generated name is empty")
	}
	return SessionNameResult{Name: name, Source: source, Usage: res.TotalUsage}, nil
}

func sessionNamerEnabled(profile ProviderProfile) bool {
	if profile == nil {
		return false
	}
	return strings.TrimSpace(configuredSessionNamerModel(profile)) != ""
}

func configuredSessionNamerModel(profile ProviderProfile) string {
	if profile == nil {
		return ""
	}
	switch p := profile.(type) {
	case *baseProfile:
		return p.configuredCheapModel()
	case *anthropicProfile:
		return p.baseProfile.configuredCheapModel()
	default:
		return ""
	}
}

func (p *baseProfile) configuredCheapModel() string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.cheapModel)
}

const sessionNamerSystemPrompt = `You generate concise session titles for a developer assistant.
Return only JSON matching the requested schema.
Rules:
- 2 to 6 words.
- Maximum 60 characters.
- Use Title Case.
- No ending punctuation.
- No quotes or markdown.
- Prefer the concrete task or outcome over generic wording.`

func sessionNamerUserPrompt(source, text string) string {
	var label string
	switch normalizeSessionNameSource(source) {
	case sessionNameSourceCompaction:
		label = "Use this compaction summary/checkpoint to refresh the session title"
	default:
		label = "Use this initial user prompt to name the session"
	}
	return label + ":\n\n" + trimForSessionNamer(text)
}

func sessionNameSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "A short human-readable session title.",
				"minLength":   1,
				"maxLength":   sessionNameMaxRunes,
			},
		},
		"required": []string{"name"},
	}
}

func normalizeSessionNameSource(source string) string {
	switch strings.TrimSpace(source) {
	case sessionNameSourceCompaction:
		return sessionNameSourceCompaction
	default:
		return sessionNameSourcePrompt
	}
}

func trimForSessionNamer(text string) string {
	text = strings.TrimSpace(text)
	const maxRunes = 4000
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "\n...[truncated]"
}

func sanitizeSessionName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "\"'`“”‘’")
	name = strings.Join(strings.Fields(name), " ")
	name = strings.TrimRightFunc(name, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	if utf8.RuneCountInString(name) > sessionNameMaxRunes {
		runes := []rune(name)
		name = strings.TrimSpace(string(runes[:sessionNameMaxRunes]))
		name = strings.TrimRightFunc(name, func(r rune) bool {
			return unicode.IsPunct(r) || unicode.IsSpace(r)
		})
	}
	return name
}

func ptrString(s string) *string { return &s }
