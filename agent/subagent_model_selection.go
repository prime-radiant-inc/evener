package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm/registry"
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
			resolved, err = resolveModelSwitchTarget(s.client, resolved)
			if err != nil {
				return subagentModelSelection{}, fmt.Errorf("model override %q: %w", explicitModel, err)
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
	resolved, err = resolveModelSwitchTarget(s.client, resolved)
	if err != nil {
		return subagentModelSelection{}, fmt.Errorf("model override %q: %w", explicitModel, err)
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
	ref, reason := resolvePluginAgentRef(s.client.Registry(), base, requested)
	if reason != "" {
		return pluginAgentModelResolution{reason: reason}
	}
	candidate, crossInstance, err := s.resolveProfileForRef(base, ref)
	if err != nil {
		return pluginAgentModelResolution{reason: "unresolvable"}
	}
	if crossInstance {
		candidate = candidate.WithCommunicateOverridesFrom(base)
	}
	if candidate.ID() == base.ID() && candidate.Model() == base.Model() {
		return pluginAgentModelResolution{profile: base}
	}

	listCtx, cancel := context.WithTimeout(ctx, liveModelMetadataTimeout)
	defer cancel()
	listing, err := s.client.Models(listCtx, candidate.ID())
	if err != nil {
		return pluginAgentModelResolution{reason: "unverified"}
	}
	if listing.Live {
		if _, ok := liveModelFor(listing.Models, candidate.Model()); !ok {
			return pluginAgentModelResolution{reason: "unavailable"}
		}
	}
	if res, err := s.client.Resolve(candidate.ID() + "/" + candidate.Model()); err == nil {
		candidate = candidate.WithResolved(res)
	}
	return pluginAgentModelResolution{profile: candidate}
}

// resolvePluginAgentRef is spec §7.5's plugin-agent rule: instance/model
// resolves directly; a bare id resolves to the session's instance when it
// serves the id, else to the highest-ranked serving instance (Registry.FindModel),
// else it is unavailable.
func resolvePluginAgentRef(r *registry.Registry, base *provider.Profile, requested string) (ref, reason string) {
	requested = strings.TrimSpace(requested)
	if inst, _, ok := strings.Cut(requested, "/"); ok {
		if _, known := r.Instance(inst); known {
			return requested, ""
		}
	}
	refs := r.FindModel(requested)
	for _, candidate := range refs {
		if candidate.Instance == base.ID() {
			return requested, ""
		}
	}
	if len(refs) > 0 {
		return refs[0].String(), ""
	}
	return "", "unavailable"
}
