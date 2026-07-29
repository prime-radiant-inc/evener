package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

type subagentModelSelection struct {
	agent          *plugin.Agent
	profile        *provider.Profile
	requestedModel string
	warning        *events.WarningData
}

type pluginAgentModelResolution struct {
	profile *provider.Profile
	reason  string
}

func (s *Session) selectSubagentModel(
	ctx context.Context,
	explicitModel string,
	agentType string,
) (subagentModelSelection, error) {
	s.mu.Lock()
	allowance := s.delegationAllowance
	s.mu.Unlock()
	if allowance <= 0 {
		return subagentModelSelection{}, errors.New("delegation not permitted: your delegation_allowance is 0")
	}

	agentType = strings.TrimSpace(agentType)
	var agent *plugin.Agent
	if agentType != "" {
		a, ok := s.pluginAgents[agentType]
		if !ok {
			return subagentModelSelection{}, fmt.Errorf("unknown plugin agent type: %s", agentType)
		}
		agent = &a
	}

	base := s.currentProfile()
	explicitModel = strings.TrimSpace(explicitModel)
	pluginModel := ""
	if agent != nil {
		pluginModel = strings.TrimSpace(agent.Model)
	}
	if pluginModel != "" && pluginModel != "inherit" {
		resolution := s.resolvePluginAgentModel(ctx, base, pluginModel)
		if resolution.reason == "" {
			return subagentModelSelection{
				agent:          agent,
				profile:        resolution.profile,
				requestedModel: pluginModel,
			}, nil
		}

		fallbackProfile := base
		if explicitModel != "" {
			resolved, crossProvider, err := s.resolveProfileForRef(base, explicitModel)
			if err != nil {
				return subagentModelSelection{}, fmt.Errorf("model override %q: %w", explicitModel, err)
			}
			if crossProvider {
				resolved = resolved.WithCommunicateOverridesFrom(base)
			}
			fallbackProfile = resolved
		}
		selected := subagentModelSelection{
			agent:          agent,
			profile:        fallbackProfile,
			requestedModel: explicitModel,
		}
		warning := events.WarningData{
			Source:     "plugin",
			Title:      "plugin agent model unavailable",
			PluginName: agent.PluginName,
			Message: fmt.Sprintf(
				"plugin %q agent %q requested model %q, but it is %s on active provider %q; using %q",
				agent.PluginName,
				agentType,
				pluginModel,
				resolution.reason,
				base.ID(),
				fallbackProfile.ID()+"/"+fallbackProfile.Model(),
			),
		}
		selected.warning = &warning
		return selected, nil
	}

	selected := subagentModelSelection{
		agent:   agent,
		profile: base,
	}
	if explicitModel == "" {
		return selected, nil
	}
	resolved, crossProvider, err := s.resolveProfileForRef(base, explicitModel)
	if err != nil {
		return subagentModelSelection{}, fmt.Errorf("model override %q: %w", explicitModel, err)
	}
	if crossProvider {
		resolved = resolved.WithCommunicateOverridesFrom(base)
	}
	selected.profile = resolved
	selected.requestedModel = explicitModel
	return selected, nil
}

func (s *Session) resolvePluginAgentModel(
	ctx context.Context,
	base *provider.Profile,
	requested string,
) pluginAgentModelResolution {
	candidateRef, reason := resolvePluginAgentCatalogRef(
		base,
		llm.EmbeddedModelCatalog(),
		requested,
	)
	if reason != "" {
		return pluginAgentModelResolution{reason: reason}
	}

	candidate := base.WithModel(candidateRef)
	if candidate.ID() == base.ID() && candidate.Model() == base.Model() {
		return pluginAgentModelResolution{profile: base}
	}

	listCtx, cancel := context.WithTimeout(ctx, liveModelMetadataTimeout)
	defer cancel()
	models, err := s.client.ListModels(listCtx, candidate.ID())
	if err != nil {
		return pluginAgentModelResolution{reason: "unverified"}
	}
	advertisedInfo, ok := liveModelInfoFor(models, candidate.Model())
	if !ok {
		return pluginAgentModelResolution{reason: "unavailable"}
	}
	candidate = candidate.WithModel(advertisedInfo.ID)
	return pluginAgentModelResolution{profile: candidate.WithLiveModelInfo(advertisedInfo)}
}

func resolvePluginAgentCatalogRef(
	base *provider.Profile,
	catalog *llm.ModelCatalog,
	requested string,
) (candidate string, reason string) {
	requested = strings.TrimSpace(requested)
	if base.CrossProviderRef(requested) {
		return "", "cross-provider"
	}
	if catalog == nil {
		return requested, ""
	}
	for _, model := range catalog.Models {
		if model.ID == requested {
			return requested, ""
		}
	}

	target, ambiguous := catalog.ResolveAlias(requested)
	if ambiguous {
		return "", "ambiguous"
	}
	if target == nil {
		return requested, ""
	}
	if strings.EqualFold(target.Provider, base.BehaviorTag()) {
		return target.ID, ""
	}

	candidate = target.Provider + "/" + target.ID
	if base.CrossProviderRef(candidate) {
		return "", "cross-provider"
	}
	return candidate, ""
}
