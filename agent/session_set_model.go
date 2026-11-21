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
	"primeradiant.com/evener/llm/registry"
)

// unrepresentableContentKinds is the spec's per-protocol policy table
// (docs/superpowers/specs/2026-07-12-model-switching-design.md, "Unrepresentable
// history") for content kinds a target's request builder cannot faithfully
// carry in at least one role. It keys on the wire protocol because the
// restriction belongs to the request builder: a protocol misfiled here whose
// builder hard-errors on the kind would brick every subsequent turn.
//
// Google rejects audio and non-user documents. OpenAI Chat carries user
// documents as a file part but silently drops audio and non-user documents.
// Anthropic and OpenAI Responses carry documents but reject audio.
// User-document delivery is checked separately below.
func unrepresentableContentKinds(protocol string) map[llm.ContentKind]bool {
	switch protocol {
	case registry.ProtocolGoogle, registry.ProtocolOpenAIChat:
		return map[llm.ContentKind]bool{llm.ContentDocument: true, llm.ContentAudio: true}
	case registry.ProtocolAnthropic, registry.ProtocolOpenAIResponses:
		return map[llm.ContentKind]bool{llm.ContentAudio: true}
	default:
		return nil
	}
}

// unrepresentableHistoryKinds scans history for content kinds the target
// protocol's request builder cannot faithfully carry (per
// unrepresentableContentKinds), returning the distinct offending kinds in
// first-seen order. Returns nil when the target has no restricted kinds or
// none appear in history.
func unrepresentableHistoryKinds(history []schema.Turn, protocol string) []llm.ContentKind {
	restricted := unrepresentableContentKinds(protocol)
	if len(restricted) == 0 {
		return nil
	}
	seen := make(map[llm.ContentKind]bool, len(restricted))
	var out []llm.ContentKind
	for _, t := range history {
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentDocument && t.Message.Role == llm.RoleUser && llm.DeliversUserMessageDocuments(registry.Resolved{Protocol: protocol}, p.Document) {
				continue
			}
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

// resolveModelSwitchTarget lists the target instance ONCE and reuses the
// listing for both metadata fill (WithResolved) and membership validation
// (validateModelSwitchMembership), rather than issuing two independent listing
// calls to the same instance for the same switch. A second call would double
// the network latency/timeout budget per switch and open a TOCTOU window where
// the provider's model list could change between the metadata-fill call and
// the membership-check call, letting the two disagree about availability.
// The instance and listing the enumeration produced are returned alongside the
// resolved profile so a queueing caller (Session.SetModel's queue path) can
// carry them to the switch's later application instead of enumerating again.
// sessionID attributes the canonical API-log attempt this listing produces
// (llm.WithAPILogContext), matching resolveLiveModelProfileWithEnumerationTimeout's
// pre-session counterpart — every caller here (SetModel, subagent model
// selection) runs on an already-existing session, so sessionID is always that
// session's own id.
func resolveModelSwitchTarget(client *llm.Client, profile *provider.Profile, sessionID string, timeout llm.AdapterTimeout, primedInstance string, primed *llm.ModelListing) (resolved *provider.Profile, listedInstance string, listing *llm.ModelListing, err error) {
	if err := validateVertexInteractionsModel(profile); err != nil {
		return profile, "", nil, err
	}
	if primed != nil && primedInstance == profile.ID() {
		// SetModel already listed this instance before it took the session
		// lock. Reuse that listing: a second fetch could return empty and clear
		// the rows the resolve just applied, and it would cost another full
		// enumeration budget.
		if res, err := client.Resolve(profile.ID() + "/" + profile.Model()); err == nil {
			profile = profile.WithResolved(res)
		}
		if err := validateVertexInteractionsModel(profile); err != nil {
			return profile, primedInstance, primed, err
		}
		if err := validateModelSwitchMembership(client, profile, *primed); err != nil {
			return profile, primedInstance, primed, err
		}
		return profile, primedInstance, primed, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelSwitchEnumerationTimeout)
	defer cancel()
	ctx = llm.WithModelListingTimeout(ctx, timeout)
	if sessionID != "" {
		ctx = llm.WithAPILogContext(ctx, sessionID)
	}
	filled, enumeration := fillLiveModelMetadata(ctx, client, profile)
	if enumeration.err != nil {
		if err := validateVertexInteractionsModel(filled); err != nil {
			return filled, "", nil, err
		}
		// Fail open unconditionally: an instance that cannot be listed at all
		// and any listing failure (timeout, auth error, etc.) both accept the
		// switch, with no live metadata to fill and no listing to carry.
		return filled, "", nil, nil
	}
	if err := validateModelSwitchMembership(client, filled, enumeration.listing); err != nil {
		return filled, "", nil, err
	}
	return filled, profile.ID(), &enumeration.listing, nil
}

// validateModelSwitchMembership is the switch-time membership policy. The
// client settles servability first: a reference the registry refuses outright
// (spec §7.3 — an id off the Codex allowlist is the only such case) reports
// the resolver's own error. Everything else is checked against the listing the
// caller already fetched (resolveModelSwitchTarget), and only when that listing
// is usable: a static fallback is not evidence of absence. The listing's
// rows are already filtered by the §5 visibility rule, so no second visibility
// pass runs here; fail-open-on-listing-error is handled by the caller, before
// this is called.
func validateModelSwitchMembership(client *llm.Client, profile *provider.Profile, listing llm.ModelListing) error {
	if err := validateVertexInteractionsModel(profile); err != nil {
		return err
	}
	if client == nil || profile == nil {
		return nil
	}
	if !client.CanServe(profile.ID(), profile.Model()) {
		_, err := client.Resolve(profile.ID() + "/" + profile.Model())
		return err
	}
	if !listing.Usable {
		return nil // static fallback: every servable id is accepted (spec §7.3, §8.1)
	}
	if _, ok := liveModelFor(listing.Models, profile.Model()); ok {
		return nil
	}
	return fmt.Errorf("model %s is not available from instance %s (available: %s)", profile.Model(), profile.ID(), formatModelAlternatives(listing.Models))
}

func validateVertexInteractionsResolved(res registry.Resolved, model, instance string) error {
	if res.Transport.Preset == registry.PresetVertexGemini && !registry.VertexSupportsGeminiModel(res.WireID) {
		return fmt.Errorf("model %q on %s is not supported by Vertex AI Interactions API (use a Gemini 3.1+/4 model such as gemini-3.8-flash)", model, instance)
	}
	return nil
}

func validateVertexInteractionsModel(profile *provider.Profile) error {
	if profile == nil {
		return nil
	}
	return validateVertexInteractionsResolved(profile.Resolved(), profile.Model(), profile.ID())
}

// googleMigrationTargetError returns an error only when a persisted target
// names retired google-vertex-express (without a custom instance in reg) or a
// Vertex AI Gemini model unsupported by /interactions. Temporarily disabled
// models and missing credentials are NOT pruned here so user choices survive in
// meta.json until re-enabled.
func googleMigrationTargetError(reg *registry.Registry, targetProvider, targetModel string) error {
	if strings.EqualFold(targetProvider, "google-vertex-express") && (reg == nil || !reg.HasInstance("google-vertex-express")) {
		return fmt.Errorf("provider %q is retired", targetProvider)
	}
	if reg != nil {
		if res, err := reg.ResolveInstanceModelFacts(targetProvider, targetModel); err == nil {
			return validateVertexInteractionsResolved(res, targetModel, targetProvider)
		}
	}
	return nil
}

// googleMigrationCheapModelError checks a persisted SessionMeta.CheapModel
// candidate only for retired google-vertex-express or unsupported Vertex
// Gemini models, keeping temporarily disabled or uncredentialed choices intact.
func (s *Session) googleMigrationCheapModelError(candidate *provider.Profile) error {
	if candidate == nil {
		return nil
	}
	cheapProv, cheapModel := candidate.CheapModelRef()
	reg := candidate.Registry()
	if reg == nil && s.client != nil {
		reg = s.client.Registry()
	}
	if err := googleMigrationTargetError(reg, cheapProv, cheapModel); err != nil {
		return err
	}
	if reg == nil && cheapProv == candidate.ID() {
		return validateVertexInteractionsModel(candidate.WithModel(cheapModel))
	}
	return nil
}

// googleMigrationFallbackError checks a persisted ModelFallbacks entry only for
// retired google-vertex-express or unsupported Vertex Gemini models.
func (s *Session) googleMigrationFallbackError(fbModel string) error {
	var reg *registry.Registry
	if s.profile != nil {
		reg = s.profile.Registry()
	}
	if reg == nil && s.client != nil {
		reg = s.client.Registry()
	}
	fbProv, fbMod := "", fbModel
	parts := strings.SplitN(fbModel, "/", 2)
	if s.profile != nil {
		if len(parts) == 2 && s.profile.CrossProviderRef(fbModel) {
			fbProv, fbMod = parts[0], parts[1]
		} else {
			fbProv = s.profile.ID()
			if len(parts) == 2 && strings.EqualFold(parts[0], fbProv) {
				fbMod = parts[1]
			}
		}
	} else if len(parts) == 2 {
		fbProv, fbMod = parts[0], parts[1]
	}
	if err := googleMigrationTargetError(reg, fbProv, fbMod); err != nil {
		return err
	}
	if reg == nil && s.profile != nil && fbProv == s.profile.ID() {
		return validateVertexInteractionsModel(s.profile.WithModel(fbModel))
	}
	return nil
}

// maxModelAlternatives caps how many live-list alternatives
// formatModelAlternatives names in a not-a-member error, so a large instance
// catalog doesn't blow up an error message.
const maxModelAlternatives = 20

// formatModelAlternatives renders the sorted, deduplicated model IDs from a
// listing as a comma-separated list for a not-a-member error, capped at
// maxModelAlternatives with a "+N more" suffix when there are more.
func formatModelAlternatives(models []registry.Resolved) string {
	seen := make(map[string]bool, len(models))
	var ids []string
	for _, row := range models {
		if seen[row.ModelID] {
			continue
		}
		seen[row.ModelID] = true
		ids = append(ids, row.ModelID)
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
