package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const liveModelMetadataTimeout = 2 * time.Second

type liveModelEnumeration struct {
	listing llm.ModelListing
	err     error
}

func resolveLiveModelProfileWithEnumerationTimeout(client *llm.Client, profile *provider.Profile) (*provider.Profile, liveModelEnumeration) {
	ctx, cancel := context.WithTimeout(context.Background(), liveModelMetadataTimeout)
	defer cancel()
	return fillLiveModelMetadata(ctx, client, profile)
}

// resolveLiveModelProfile fills profile's live model metadata (context window,
// reasoning support, etc.) from client's model listing when available.
// Fails open (returns profile unchanged) on a nil client/profile or any
// enumeration error — used by callers that don't need the enumerated list or
// fetch result; resolveLiveModelProfileValidated is the membership-checking
// counterpart used by NewSession.
func resolveLiveModelProfile(ctx context.Context, client *llm.Client, profile *provider.Profile) *provider.Profile {
	filled, _ := fillLiveModelMetadata(ctx, client, profile)
	return filled
}

// fillLiveModelMetadata is the shared fetch-and-fill core behind
// resolveLiveModelProfile, resolveLiveModelProfileValidated, and
// resolveModelSwitchTarget: it lists the profile's instance ONCE, applying a
// live listing to the registry, then overlays the freshly resolved record onto
// the profile. It returns the listing so a caller can additionally run a
// membership check against the same listing or preserve the failure in a
// startup snapshot without a second listing call (avoiding both the extra
// network latency and a TOCTOU window where the provider's list could change
// between two calls). The profile is returned unchanged on nil inputs or any
// listing error; callers choose their own fail-open policy.
func fillLiveModelMetadata(ctx context.Context, client *llm.Client, profile *provider.Profile) (*provider.Profile, liveModelEnumeration) {
	if client == nil || profile == nil {
		return profile, liveModelEnumeration{err: errors.New("live model metadata inputs are nil")}
	}
	listing, err := client.Models(ctx, profile.ID())
	if err != nil {
		return profile, liveModelEnumeration{err: err}
	}
	if res, err := client.Resolve(profile.ID() + "/" + profile.Model()); err == nil {
		profile = profile.WithResolved(res)
	}
	return profile, liveModelEnumeration{listing: listing}
}

// resolveLiveModelProfileValidated is NewSession's live-model seam: it fills
// live model metadata (fillLiveModelMetadata, 2s budget) and, only when
// enumeration succeeded, runs validateModelSwitchMembership against the same
// listing — the same membership policy SetModel and delegate dispatch
// apply, so a session can no longer launch with a model the provider's live
// list definitively doesn't carry. Fails open (nil error, profile unchanged
// modulo any metadata fill) on any enumeration failure, matching NewSession's
// prior unvalidated behavior in that case.
func resolveLiveModelProfileValidated(client *llm.Client, profile *provider.Profile) (*provider.Profile, liveModelEnumeration, error) {
	filled, enumeration := resolveLiveModelProfileWithEnumerationTimeout(client, profile)
	if enumeration.err == nil {
		if err := validateModelSwitchMembership(client, filled, enumeration.listing); err != nil {
			return filled, enumeration, err
		}
	}
	return filled, enumeration, nil
}

// liveModelFor finds model among the listed rows: exact id first, then the
// same id with surrounding whitespace trimmed, then a case-insensitive match.
func liveModelFor(models []registry.Resolved, model string) (registry.Resolved, bool) {
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return registry.Resolved{}, false
	}
	for _, row := range models {
		if row.ModelID == model {
			return row, true
		}
	}
	for _, row := range models {
		if strings.TrimSpace(row.ModelID) == trimmedModel {
			return row, true
		}
	}
	for _, row := range models {
		if strings.EqualFold(strings.TrimSpace(row.ModelID), trimmedModel) {
			return row, true
		}
	}
	return registry.Resolved{}, false
}
