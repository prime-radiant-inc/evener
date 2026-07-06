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

// hubModelList is the single server-side entry point for every ModelList
// RPC — the appwire dispatch (app_rpc.go) routes every harness's call here,
// which is also the path the TUI's client.ModelList() hits. It always
// attaches Recent (the model picker's global-recency group), regardless of
// which harness was requested: Recent is harness-independent by design (the
// picker shows the same top-5 list no matter which harness tab is active).
func hubModelList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	resp, err := hubModelListInner(ctx, cfg, sources, params)
	if err != nil {
		return resp, err
	}
	return attachRecentModels(cfg, resp), nil
}

// recentModelsLimit is the model picker's Recent group size (Decision #8:
// "the last 5 distinct models").
const recentModelsLimit = 5

// attachRecentModels resolves cfg.Past's globally-recent model refs and
// filters them to ones actually present in resp.Data — a recent model the
// current config no longer offers (retired, provider reconfigured) is
// dropped rather than rendered as an unselectable entry.
func attachRecentModels(cfg hubcore.WebConfig, resp appwire.ModelListResponse) appwire.ModelListResponse {
	if cfg.Past == nil {
		return resp
	}
	refs := cfg.Past.RecentModels(recentModelsLimit)
	if len(refs) == 0 {
		return resp
	}
	available := make(map[string]bool, len(resp.Data))
	for _, d := range resp.Data {
		available[d.Provider+"/"+d.Model] = true
	}
	var recent []appwire.ModelDescriptor
	for _, ref := range refs {
		if available[ref.Provider+"/"+ref.Model] {
			recent = append(recent, ref)
		}
	}
	resp.Recent = recent
	return resp
}

func hubModelListInner(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
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
		return nil //nolint:nilerr // fail open: if the model list can't be enumerated, don't block launch
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
