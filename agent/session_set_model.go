package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// unrepresentableContentKinds is the spec's explicit per-tag policy table
// (docs/superpowers/specs/2026-07-12-model-switching-design.md, "Unrepresentable
// history") for content kinds a target's request builder cannot faithfully
// carry: hard errors for anthropic-family (anthropic/request.go:512-513) and
// google (google/request.go:280-281,328-329); silent drops for openai-compat
// (openaicompat/request.go:299-329 has no document/audio cases — silently
// dropping user content counts as unrepresentable by policy); audio only for
// openai Responses (responses.go:913-938 — documents are carried).
func unrepresentableContentKinds(behaviorTag string) map[llm.ContentKind]bool {
	switch {
	case behaviorTag == "anthropic" || behaviorTag == "openrouter-anthropic" || behaviorTag == "google":
		return map[llm.ContentKind]bool{llm.ContentDocument: true, llm.ContentAudio: true}
	case isOpenAICompatFamilyTag(behaviorTag):
		return map[llm.ContentKind]bool{llm.ContentDocument: true, llm.ContentAudio: true}
	case behaviorTag == "openai":
		return map[llm.ContentKind]bool{llm.ContentAudio: true}
	default:
		return nil
	}
}

// isOpenAICompatFamilyTag reports whether tag routes through the openai-compat
// adapter — the behavior-tag-level counterpart of llm/providercfg.CompatFamily.
func isOpenAICompatFamilyTag(tag string) bool {
	switch tag {
	case "openai-compatible", "kimi", "glm", "openrouter", "ollama":
		return true
	}
	return false
}

// unrepresentableHistoryKinds scans history for content kinds the target
// behavior tag's request builder cannot faithfully carry (per
// unrepresentableContentKinds), returning the distinct offending kinds in
// first-seen order. Returns nil when the target has no restricted kinds or
// none appear in history.
func unrepresentableHistoryKinds(history []schema.Turn, behaviorTag string) []llm.ContentKind {
	restricted := unrepresentableContentKinds(behaviorTag)
	if len(restricted) == 0 {
		return nil
	}
	seen := make(map[llm.ContentKind]bool, len(restricted))
	var out []llm.ContentKind
	for _, t := range history {
		for _, p := range t.Message.Content {
			if restricted[p.Kind] && !seen[p.Kind] {
				seen[p.Kind] = true
				out = append(out, p.Kind)
			}
		}
	}
	return out
}

// formatContentKinds renders content kinds for an error message.
func formatContentKinds(kinds []llm.ContentKind) string {
	strs := make([]string, len(kinds))
	for i, k := range kinds {
		strs[i] = string(k)
	}
	return strings.Join(strs, ", ")
}

// modelSwitchEnumerationTimeout mirrors launchcheck's live model enumeration
// budget (cmd/serf/internal/launchcheck/launchcheck.go's validateLaunchCheckModel)
// for the analogous membership check SetModel performs.
const modelSwitchEnumerationTimeout = 8 * time.Second

// resolveModelSwitchTarget fetches the target instance's live model list ONCE
// and reuses it for both metadata fill (WithLiveModelInfo) and membership
// validation (validateModelSwitchMembership), rather than issuing two
// independent ListModels calls to the same instance for the same switch. A
// second call would double the network latency/timeout budget per switch and
// open a TOCTOU window where the provider's model list could change between
// the metadata-fill call and the membership-check call, letting the two
// disagree about availability.
func resolveModelSwitchTarget(client *llm.Client, profile *provider.Profile) (*provider.Profile, error) {
	if client == nil || profile == nil {
		return profile, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelSwitchEnumerationTimeout)
	defer cancel()
	models, err := client.ListModels(ctx, profile.ID())
	if err != nil {
		// Fail open unconditionally: a non-enumerable instance (adapter
		// doesn't implement ModelLister) and any enumeration failure
		// (timeout, auth error, etc.) both accept the switch, with no live
		// metadata to fill.
		return profile, nil
	}
	if info, ok := liveModelInfoFor(models, profile.Model()); ok {
		profile = profile.WithLiveModelInfo(info)
	}
	if err := validateModelSwitchMembership(client, profile, models); err != nil {
		return profile, err
	}
	return profile, nil
}

// validateModelSwitchMembership mirrors the launch policy's live-enumeration +
// catalog-visibility model membership check
// (cmd/serf/internal/launchcheck/launchcheck.go's validateLaunchCheckModel,
// cmd/serf-hub/app_models.go's launchProviderAllowsUnreportedModels), with one
// amendment per spec: a switch fails open (accepts) on ANY enumeration error
// class — not just launchcheck's message allowlist — so dead credentials
// never block a switch (those two files are cmd-internal and not importable
// from agent, so the policy is mirrored here rather than shared). The model
// list is supplied by the caller (resolveModelSwitchTarget), which has
// already fetched it once for metadata fill; fail-open-on-enumeration-error
// is handled there, before this is called.
func validateModelSwitchMembership(client *llm.Client, profile *provider.Profile, models []llm.ModelInfo) error {
	if client == nil || profile == nil {
		return nil
	}
	tag := client.BehaviorTagOf(profile.ID())
	cat := llm.EmbeddedModelCatalog()
	for _, m := range models {
		if m.ID == profile.Model() && modelSwitchVisible(tag, m.ID, cat) {
			return nil
		}
	}
	if strings.EqualFold(strings.TrimSpace(tag), "openrouter-anthropic") {
		// Behavior-tag unreported-models allowance: openrouter-anthropic's
		// enumerated set is not the source of truth for what it can serve.
		return nil
	}
	return fmt.Errorf("model %s is not available from instance %s", profile.Model(), profile.ID())
}

// modelSwitchVisible mirrors launchcheck's launchCheckModelVisible: non-chat
// model IDs (embedding, media, etc.) are never valid switch targets, and the
// "openrouter" tag additionally requires catalog tool support.
func modelSwitchVisible(behaviorTag, modelID string, cat *llm.ModelCatalog) bool {
	lower := strings.ToLower(modelID)
	if strings.Contains(lower, "embedding") ||
		strings.Contains(lower, "whisper") ||
		strings.Contains(lower, "tts") ||
		strings.Contains(lower, "dall-e") ||
		strings.Contains(lower, "moderation") ||
		strings.Contains(lower, "audio") ||
		strings.Contains(lower, "transcribe") ||
		strings.Contains(lower, "image") {
		return false
	}
	if behaviorTag == "openrouter" {
		if cat == nil {
			return false
		}
		mi := cat.GetModelInfo(modelID)
		return mi != nil && mi.SupportsTools
	}
	return true
}
