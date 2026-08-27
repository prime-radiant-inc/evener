package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// unrepresentableContentKinds is the spec's per-tag policy table
// (docs/superpowers/specs/2026-07-12-model-switching-design.md, "Unrepresentable
// history") for content kinds a target's request builder cannot faithfully
// carry. It derives the family from builderFamily (session_model_call.go) so
// this preflight and the N4 replay classifier share a single source of truth
// and cannot drift: a tag misfiled here that the anthropic/google builder
// hard-errors on would brick every subsequent turn.
//
// Per family: hard errors for anthropic-family (anthropic/request.go:512-513)
// and google (google/request.go:280-281,328-329); silent drops for openai-compat
// (openaicompat/request.go:299-329 has no document/audio cases — silently
// dropping user content counts as unrepresentable by policy); audio only for
// openai Responses (responses.go:913-938 — documents are carried).
func unrepresentableContentKinds(behaviorTag string) map[llm.ContentKind]bool {
	switch builderFamily(behaviorTag) {
	case "anthropic", "google", "compat":
		return map[llm.ContentKind]bool{llm.ContentDocument: true, llm.ContentAudio: true}
	case "openai":
		return map[llm.ContentKind]bool{llm.ContentAudio: true}
	default:
		return nil
	}
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
// budget (cmd/evener/internal/launchcheck/launchcheck.go's validateLaunchCheckModel)
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
	ctx, cancel := context.WithTimeout(context.Background(), modelSwitchEnumerationTimeout)
	defer cancel()
	filled, enumeration := fillLiveModelMetadata(ctx, client, profile)
	if enumeration.err != nil {
		// Fail open unconditionally: a non-enumerable instance (adapter
		// doesn't implement ModelLister) and any enumeration failure
		// (timeout, auth error, etc.) both accept the switch, with no live
		// metadata to fill.
		return filled, nil
	}
	if err := validateModelSwitchMembership(client, filled, enumeration.models); err != nil {
		return filled, err
	}
	return filled, nil
}

// validateModelSwitchMembership mirrors the launch policy's live-enumeration +
// catalog-visibility model membership check
// (cmd/evener/internal/launchcheck/launchcheck.go's validateLaunchCheckModel,
// cmd/evener-hub/app_models.go's launchProviderAllowsUnreportedModels), with one
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
	if err := client.ValidateModelCompatibility(profile.ID(), profile.Model()); err != nil {
		return err
	}
	tag := client.BehaviorTagOf(profile.ID())
	cat := llm.EmbeddedModelCatalog()
	for _, m := range models {
		if m.ID == profile.Model() && modelSwitchVisible(tag, m, cat) {
			return nil
		}
	}
	if strings.EqualFold(strings.TrimSpace(tag), "openrouter-anthropic") {
		// Behavior-tag unreported-models allowance: openrouter-anthropic's
		// enumerated set is not the source of truth for what it can serve.
		return nil
	}
	return fmt.Errorf("model %s is not available from instance %s (available: %s)", profile.Model(), profile.ID(), formatModelAlternatives(models, tag, cat))
}

// maxModelAlternatives caps how many live-list alternatives
// formatModelAlternatives names in a not-a-member error, so a large instance
// catalog doesn't blow up an error message.
const maxModelAlternatives = 20

// formatModelAlternatives renders the visible (modelSwitchVisible), sorted,
// deduplicated model IDs from models as a comma-separated list for a
// not-a-member error, capped at maxModelAlternatives with a "+N more" suffix
// when there are more.
func formatModelAlternatives(models []llm.ModelInfo, tag string, cat *llm.ModelCatalog) string {
	seen := make(map[string]bool, len(models))
	var ids []string
	for _, m := range models {
		if !modelSwitchVisible(tag, m, cat) || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "none"
	}
	if len(ids) > maxModelAlternatives {
		extra := len(ids) - maxModelAlternatives
		return fmt.Sprintf("%s, +%d more", strings.Join(ids[:maxModelAlternatives], ", "), extra)
	}
	return strings.Join(ids, ", ")
}

// modelSwitchVisible delegates to the shared llm.ModelCatalog.VisibleLiveModel
// rule so the in-session model-switch path, the launch-check path, and the hub
// /api/models path share one visibility rule and cannot drift. See
// VisibleLiveModel for the live-API-first tool-support resolution this
// implements.
func modelSwitchVisible(behaviorTag string, live llm.ModelInfo, cat *llm.ModelCatalog) bool {
	return cat.VisibleLiveModel(behaviorTag, live)
}
