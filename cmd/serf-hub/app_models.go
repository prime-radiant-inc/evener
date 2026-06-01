package main

import (
	"context"
	"strings"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm/providercfg"
)

func hubModelList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" && harness != "serf" && harness != "local" {
		source, err := sourceForModelHarness(ctx, cfg, sources, harness)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		sourceParams := params
		sourceParams.Harness = ""
		resp, err := source.ListModels(ctx, sourceParams)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		resp.Data = sanitizeModelDescriptors(resp.Data)
		resp.Diagnostics = sanitizeModelDiagnostics(resp.Diagnostics)
		return resp, nil
	}

	launchResp, err := serfLaunchModelList(ctx, cfg, params.CWD)
	if hasSerfLaunchModelLister(cfg) {
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		return launchResp, nil
	}
	source, ok := sources.Source("local")
	if ok {
		resp, err := source.ListModels(ctx, params)
		if err == nil && len(resp.Data) > 0 {
			return resp, nil
		}
	}
	return appwire.ModelListResponse{}, nil
}

func sourceForModelHarness(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, harness string) (appsource.Source, error) {
	if cfg.CodexLauncher != nil && cfg.CodexLauncher.Manages(harness) {
		return cfg.CodexLauncher.EnsureSource(ctx, harness, sources)
	}
	source, ok := sources.Source(harness)
	if !ok {
		return nil, appwire.Unavailable("model list source is not available: " + harness)
	}
	return source, nil
}

func validateSerfLaunchModel(ctx context.Context, cfg hubcore.WebConfig, ref cmdutil.ModelRef, workingDir string) error {
	contract, err := serfLaunchModelList(ctx, cfg, workingDir)
	if err != nil || (len(contract.Data) == 0 && len(contract.Diagnostics) == 0) {
		return nil
	}
	providerEnumerated := false
	for _, model := range contract.Data {
		if strings.EqualFold(strings.TrimSpace(model.Provider), ref.Provider) {
			providerEnumerated = true
			if strings.TrimSpace(model.Model) == ref.Model {
				return nil
			}
		}
	}
	if !providerEnumerated {
		if providerHasLaunchDiagnostic(contract.Diagnostics, ref.Provider) || launchProviderAllowsUnreportedModels(ref.Provider, cfg.ProviderConfig) {
			return nil
		}
		return appwire.HubLaunchError("model provider is not reported by the Serf launch harness: " + ref.Provider)
	}
	return appwire.HubLaunchError("model is not configured for Serf launch: " + ref.Qualified())
}

// behaviorTagFromConfig resolves a provider instance name to its behavior tag
// using cfg. When cfg is nil or the name is not present in the config, the
// name itself is returned (identity fallback for the env path).
func behaviorTagFromConfig(name string, cfg *providercfg.Config) string {
	if cfg != nil {
		if tag, ok := providercfg.NameToTag(*cfg)[name]; ok {
			return tag
		}
	}
	return name
}

// launchProviderAllowsUnreportedModels returns true when the provider's
// behavior tag is "openrouter-anthropic". The tag is resolved from cfg when
// available; when cfg is nil the provider name is used as-is (identity
// fallback for the env path where instance name == type == tag).
func launchProviderAllowsUnreportedModels(provider string, cfg *providercfg.Config) bool {
	tag := behaviorTagFromConfig(provider, cfg)
	return strings.EqualFold(strings.TrimSpace(tag), "openrouter-anthropic")
}

func providerHasLaunchDiagnostic(diagnostics []appwire.ModelListDiagnostic, provider string) bool {
	for _, diag := range diagnostics {
		if strings.EqualFold(strings.TrimSpace(diag.Provider), provider) {
			return true
		}
	}
	return false
}

func serfLaunchModels(ctx context.Context, cfg hubcore.WebConfig) ([]appwire.ModelDescriptor, error) {
	resp, err := serfLaunchModelList(ctx, cfg, "")
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func serfLaunchModelList(ctx context.Context, cfg hubcore.WebConfig, workingDir string) (appwire.ModelListResponse, error) {
	if strings.TrimSpace(workingDir) != "" {
		if lister, ok := cfg.Spawner.(SerfLaunchModelContractWorkingDirLister); ok && lister != nil {
			resp, err := lister.ListLaunchModelContractForWorkingDir(ctx, workingDir)
			if err != nil {
				return appwire.ModelListResponse{}, err
			}
			resp.Data = sanitizeModelDescriptors(resp.Data)
			resp.Diagnostics = sanitizeModelDiagnostics(resp.Diagnostics)
			return resp, nil
		}
	}
	if lister, ok := cfg.Spawner.(SerfLaunchModelContractLister); ok && lister != nil {
		resp, err := lister.ListLaunchModelContract(ctx)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		resp.Data = sanitizeModelDescriptors(resp.Data)
		resp.Diagnostics = sanitizeModelDiagnostics(resp.Diagnostics)
		return resp, nil
	}
	lister, ok := cfg.Spawner.(SerfLaunchModelLister)
	if !ok || lister == nil {
		return appwire.ModelListResponse{}, nil
	}
	models, err := lister.ListLaunchModels(ctx)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	return appwire.ModelListResponse{Data: sanitizeModelDescriptors(models)}, nil
}

func hasSerfLaunchModelLister(cfg hubcore.WebConfig) bool {
	if lister, ok := cfg.Spawner.(SerfLaunchModelContractWorkingDirLister); ok && lister != nil {
		return true
	}
	if lister, ok := cfg.Spawner.(SerfLaunchModelContractLister); ok && lister != nil {
		return true
	}
	if lister, ok := cfg.Spawner.(SerfLaunchModelLister); ok && lister != nil {
		return true
	}
	return false
}

func sanitizeModelDescriptors(models []appwire.ModelDescriptor) []appwire.ModelDescriptor {
	out := make([]appwire.ModelDescriptor, 0, len(models))
	for _, model := range models {
		provider := strings.TrimSpace(model.Provider)
		name := strings.TrimSpace(model.Model)
		if provider == "" || name == "" {
			continue
		}
		out = append(out, appwire.ModelDescriptor{Provider: provider, Model: name})
	}
	return out
}

func sanitizeModelDiagnostics(diagnostics []appwire.ModelListDiagnostic) []appwire.ModelListDiagnostic {
	out := make([]appwire.ModelListDiagnostic, 0, len(diagnostics))
	for _, diag := range diagnostics {
		diag.Provider = strings.TrimSpace(diag.Provider)
		diag.Source = strings.TrimSpace(diag.Source)
		diag.Title = strings.TrimSpace(diag.Title)
		diag.Message = strings.TrimSpace(diag.Message)
		diag.Hint = strings.TrimSpace(diag.Hint)
		if diag.Message == "" {
			continue
		}
		out = append(out, diag)
	}
	return out
}

func launchHarnessDescriptors(cfg hubcore.WebConfig) []appwire.HarnessDescriptor {
	out := []appwire.HarnessDescriptor{{ID: "serf", Label: "serf", Kind: "serf"}}
	seen := map[string]bool{"serf": true}
	for _, source := range cfg.CodexSources {
		id := strings.TrimSpace(source.ID)
		if id == "" {
			id = "codex"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, appwire.HarnessDescriptor{ID: id, Label: id, Kind: "codex"})
	}
	for _, launch := range cfg.CodexLaunches {
		id := strings.TrimSpace(launch.ID)
		if id == "" {
			id = "codex"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, appwire.HarnessDescriptor{ID: id, Label: id, Kind: "codex"})
	}
	return out
}
