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

func resolveLiveModelProfile(ctx context.Context, client *llm.Client, profile *provider.Profile) *provider.Profile {
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
	return profile.WithLiveModelInfo(info)
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
