package agent

import (
	"context"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
)

const liveModelMetadataTimeout = 2 * time.Second

func resolveLiveModelProfileWithTimeout(client *llm.Client, profile *Profile) *Profile {
	ctx, cancel := context.WithTimeout(context.Background(), liveModelMetadataTimeout)
	defer cancel()
	return resolveLiveModelProfile(ctx, client, profile)
}

func resolveLiveModelProfile(ctx context.Context, client *llm.Client, profile *Profile) *Profile {
	if client == nil || profile == nil {
		return profile
	}
	models, err := client.ListModels(ctx, profile.ID())
	if err != nil {
		return profile
	}
	info, ok := liveModelInfoFor(models, profile.Model())
	if !ok {
		return profile
	}
	return profileWithLiveModelInfo(profile, info)
}

func liveModelInfoFor(models []llm.ModelInfo, model string) (llm.ModelInfo, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return llm.ModelInfo{}, false
	}
	for _, info := range models {
		if strings.TrimSpace(info.ID) == model {
			return info, true
		}
	}
	for _, info := range models {
		if strings.EqualFold(strings.TrimSpace(info.ID), model) {
			return info, true
		}
	}
	return llm.ModelInfo{}, false
}

func profileWithLiveModelInfo(profile *Profile, info llm.ModelInfo) *Profile {
	if profile == nil {
		return nil
	}
	clone := *profile
	applyLiveModelInfo(&clone, info)
	return &clone
}

func applyLiveModelInfo(profile *Profile, info llm.ModelInfo) {
	if profile == nil {
		return
	}
	if info.ContextWindow > 0 {
		profile.contextWindow = info.ContextWindow
	}
	if len(info.ReasoningEffortLevels) > 0 {
		profile.effortLevels = append([]string(nil), info.ReasoningEffortLevels...)
	}
	if info.SupportsReasoning {
		profile.reasoning = true
	}
	if info.SupportsWebSearch != nil {
		profile.webSearch = *info.SupportsWebSearch
	}
}
