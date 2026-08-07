package agent

import (
	"context"
	"strings"
	"time"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

const liveModelMetadataTimeout = 2 * time.Second

func resolveLiveModelProfileWithTimeout(client *llm.Client, profile *provider.Profile) *provider.Profile {
	ctx, cancel := context.WithTimeout(context.Background(), liveModelMetadataTimeout)
	defer cancel()
	return resolveLiveModelProfile(ctx, client, profile)
}

// resolveLiveModelProfile fills profile's live model metadata (context window,
// reasoning support, etc.) from client's live model list when available.
// Fails open (returns profile unchanged) on a nil client/profile or any
// enumeration error — used by Restore and by tests that don't need the
// enumerated list or the fetch's success flag; resolveLiveModelProfileValidated
// is the membership-checking counterpart used by NewSession.
func resolveLiveModelProfile(ctx context.Context, client *llm.Client, profile *provider.Profile) *provider.Profile {
	filled, _, _ := fillLiveModelMetadata(ctx, client, profile)
	return filled
}

// fillLiveModelMetadata is the shared fetch-and-fill core behind
// resolveLiveModelProfile, resolveLiveModelProfileValidated, and
// resolveModelSwitchTarget: it fetches profile's provider instance's live
// model list ONCE, fills live metadata onto profile when the requested model
// is found, and returns the fetched list plus whether enumeration succeeded so
// a caller can additionally run a membership check against the same list
// without a second ListModels call (avoiding both the extra network latency
// and a TOCTOU window where the provider's list could change between two
// calls). Fails open on a nil client/profile or any ListModels error: ok is
// false, profile is returned unchanged, and models is nil.
func fillLiveModelMetadata(ctx context.Context, client *llm.Client, profile *provider.Profile) (*provider.Profile, []llm.ModelInfo, bool) {
	if client == nil || profile == nil {
		return profile, nil, false
	}
	models, err := client.ListModels(ctx, profile.ID())
	if err != nil {
		return profile, nil, false
	}
	if info, ok := liveModelInfoFor(models, profile.Model()); ok {
		profile = profile.WithLiveModelInfo(info)
	}
	return profile, models, true
}

// resolveLiveModelProfileValidated is NewSession's live-model seam: it fills
// live model metadata (fillLiveModelMetadata, 2s budget) and, only when
// enumeration succeeded, runs validateModelSwitchMembership against the same
// fetched list — the same membership policy SetModel and delegate dispatch
// apply, so a session can no longer launch with a model the provider's live
// list definitively doesn't carry. Fails open (nil error, profile unchanged
// modulo any metadata fill) on any enumeration failure, matching NewSession's
// prior unvalidated behavior in that case.
func resolveLiveModelProfileValidated(client *llm.Client, profile *provider.Profile) (*provider.Profile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), liveModelMetadataTimeout)
	defer cancel()
	filled, models, ok := fillLiveModelMetadata(ctx, client, profile)
	if !ok {
		return filled, nil
	}
	if err := validateModelSwitchMembership(client, filled, models); err != nil {
		return filled, err
	}
	return filled, nil
}

func liveModelInfoFor(models []llm.ModelInfo, model string) (llm.ModelInfo, bool) {
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return llm.ModelInfo{}, false
	}
	for _, info := range models {
		if info.ID == model {
			return info, true
		}
	}
	for _, info := range models {
		if strings.TrimSpace(info.ID) == trimmedModel {
			return info, true
		}
	}
	for _, info := range models {
		if strings.EqualFold(strings.TrimSpace(info.ID), trimmedModel) {
			return info, true
		}
	}
	return llm.ModelInfo{}, false
}
