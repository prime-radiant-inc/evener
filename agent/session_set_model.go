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
// carry. It keys on the wire protocol because the restriction belongs to the
// request builder: a protocol misfiled here whose builder hard-errors on the
// kind would brick every subsequent turn.
//
// Per protocol: hard errors for anthropic and google (their request builders
// reject the kind outright); silent drops for openai-chat (llm/providers/
// chatcompletions has no document/audio case — silently dropping user content
// counts as unrepresentable by policy); audio only for openai-responses
// (llm/providers/responses carries documents).
func unrepresentableContentKinds(protocol string) map[llm.ContentKind]bool {
	switch protocol {
	case registry.ProtocolAnthropic, registry.ProtocolGoogle, registry.ProtocolOpenAIChat:
		return map[llm.ContentKind]bool{llm.ContentDocument: true, llm.ContentAudio: true}
	case registry.ProtocolOpenAIResponses:
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
// sessionID attributes the canonical API-log attempt this listing produces
// (llm.WithAPILogContext), matching resolveLiveModelProfileWithEnumerationTimeout's
// pre-session counterpart — every caller here (SetModel, subagent model
// selection) runs on an already-existing session, so sessionID is always that
// session's own id.
func resolveModelSwitchTarget(client *llm.Client, profile *provider.Profile, sessionID string, timeout llm.AdapterTimeout) (*provider.Profile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelSwitchEnumerationTimeout)
	defer cancel()
	ctx = llm.WithModelListingTimeout(ctx, timeout)
	if sessionID != "" {
		ctx = llm.WithAPILogContext(ctx, sessionID)
	}
	filled, enumeration := fillLiveModelMetadata(ctx, client, profile)
	if enumeration.err != nil {
		// Fail open unconditionally: an instance that cannot be listed at all
		// and any listing failure (timeout, auth error, etc.) both accept the
		// switch, with no live metadata to fill.
		return filled, nil
	}
	if err := validateModelSwitchMembership(client, filled, enumeration.listing); err != nil {
		return filled, err
	}
	return filled, nil
}

// validateModelSwitchMembership is the switch-time membership policy. The
// client settles servability first: a reference the registry refuses outright
// (spec §7.3 — an id off the Codex allowlist is the only such case) reports
// the resolver's own error. Everything else is checked against the listing the
// caller already fetched (resolveModelSwitchTarget), and only when that listing
// is live: a registry-only listing is not evidence of absence. The listing's
// rows are already filtered by the §5 visibility rule, so no second visibility
// pass runs here; fail-open-on-listing-error is handled by the caller, before
// this is called.
func validateModelSwitchMembership(client *llm.Client, profile *provider.Profile, listing llm.ModelListing) error {
	if client == nil || profile == nil {
		return nil
	}
	if !client.CanServe(profile.ID(), profile.Model()) {
		_, err := client.Resolve(profile.ID() + "/" + profile.Model())
		return err
	}
	if !listing.Live {
		return nil // registry-only listing: every id resolves (spec §7.3, §8.1)
	}
	if _, ok := liveModelFor(listing.Models, profile.Model()); ok {
		return nil
	}
	return fmt.Errorf("model %s is not available from instance %s (available: %s)", profile.Model(), profile.ID(), formatModelAlternatives(listing.Models))
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
