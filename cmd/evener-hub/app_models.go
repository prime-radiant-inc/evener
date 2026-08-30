package hub

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

var liveModelLoadClient = cmdutil.LoadClient

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
	resp = enrichModelListResponse(cfg, resp)
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
	available := make(map[string]appwire.ModelDescriptor, len(resp.Data))
	for _, d := range resp.Data {
		available[d.Provider+"/"+d.Model] = d
	}
	var recent []appwire.ModelDescriptor
	for _, ref := range refs {
		if descriptor, ok := available[ref.Provider+"/"+ref.Model]; ok {
			recent = append(recent, descriptor)
		}
	}
	resp.Recent = recent
	return resp
}

func hubModelListInner(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" && harness != "evener" && harness != "local" {
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
		return sanitizeModelListResponse(resp), nil
	}

	launchResp, err := evenerLaunchModelList(ctx, cfg, params.CWD)
	if hasEvenerLaunchModelLister(cfg) {
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		return launchResp, nil
	}
	source, ok := sources.Source("local")
	if ok {
		resp, err := source.ListModels(ctx, params)
		if err == nil && len(resp.Data) > 0 {
			return sanitizeModelListResponse(resp), nil
		}
	}
	if cfg.LiveModels != nil {
		models := sanitizeModelDescriptors(cfg.LiveModels(ctx))
		if len(models) > 0 {
			return appwire.ModelListResponse{Data: models}, nil
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

func validateEvenerLaunchModel(ctx context.Context, cfg hubcore.WebConfig, ref cmdutil.ModelRef, workingDir string) error {
	contract, err := evenerLaunchModelList(ctx, cfg, workingDir)
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
		return appwire.HubLaunchError("model provider is not reported by the Evener launch harness: " + ref.Provider)
	}
	return appwire.HubLaunchError("model is not configured for Evener launch: " + ref.Qualified())
}

// launchProviderAllowsUnreportedModels returns true when the provider's
// behavior tag is "openrouter-anthropic". The tag is resolved from cfg when
// available; when cfg is nil the provider name is used as-is (identity
// fallback for the env path where instance name == type == tag).
func launchProviderAllowsUnreportedModels(provider string, cfg *providercfg.Config) bool {
	tag := behaviorTagFor(cfg, provider)
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

func evenerLaunchModelList(ctx context.Context, cfg hubcore.WebConfig, workingDir string) (appwire.ModelListResponse, error) {
	listers, configured := selectEvenerLaunchModelListers(cfg.Spawner)
	if strings.TrimSpace(workingDir) != "" && listers.workingDir != nil {
		resp, err := listers.workingDir.ListLaunchModelContractForWorkingDir(ctx, workingDir)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		return sanitizeModelListResponse(resp), nil
	}
	if listers.contract != nil {
		resp, err := listers.contract.ListLaunchModelContract(ctx)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		return sanitizeModelListResponse(resp), nil
	}
	if !configured || listers.legacy == nil {
		return appwire.ModelListResponse{}, nil
	}
	models, err := listers.legacy.ListLaunchModels(ctx)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	return sanitizeModelListResponse(appwire.ModelListResponse{Data: models}), nil
}

func hasEvenerLaunchModelLister(cfg hubcore.WebConfig) bool {
	_, configured := selectEvenerLaunchModelListers(cfg.Spawner)
	return configured
}

type evenerLaunchModelListers struct {
	workingDir EvenerLaunchModelContractWorkingDirLister
	contract   EvenerLaunchModelContractLister
	legacy     EvenerLaunchModelLister
}

func selectEvenerLaunchModelListers(spawner any) (evenerLaunchModelListers, bool) {
	var listers evenerLaunchModelListers
	configured := false
	if lister, ok := spawner.(EvenerLaunchModelContractWorkingDirLister); ok && lister != nil {
		listers.workingDir = lister
		configured = true
	}
	if lister, ok := spawner.(EvenerLaunchModelContractLister); ok && lister != nil {
		listers.contract = lister
		configured = true
	}
	if lister, ok := spawner.(EvenerLaunchModelLister); ok && lister != nil {
		listers.legacy = lister
		configured = true
	}
	return listers, configured
}

func sanitizeModelDescriptors(models []appwire.ModelDescriptor) []appwire.ModelDescriptor {
	out := make([]appwire.ModelDescriptor, 0, len(models))
	for _, model := range models {
		if !normalizeModelDescriptor(&model) {
			continue
		}
		out = append(out, model)
	}
	return out
}

func sanitizeModelListResponse(resp appwire.ModelListResponse) appwire.ModelListResponse {
	resp.Data = sanitizeModelDescriptors(resp.Data)
	resp.Diagnostics = sanitizeModelDiagnostics(resp.Diagnostics)
	return resp
}

func normalizeModelDescriptor(model *appwire.ModelDescriptor) bool {
	model.Provider = strings.TrimSpace(model.Provider)
	model.Model = strings.TrimSpace(model.Model)
	return model.Provider != "" && model.Model != ""
}

// datedSnapshotSuffix matches a trailing dated-snapshot suffix on a bare
// model id (e.g. "-20251101"), plus an optional trailing LiteLLM version tag
// ("-v1"). It mirrors llm/model_catalog_embedded.go's datedModelSuffix while
// keeping this display-order rule local to the hub response.
var datedSnapshotSuffix = regexp.MustCompile(`-\d{8}(-v\d+)?$`)

func isDatedSnapshotModelID(ref string) bool {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return datedSnapshotSuffix.MatchString(ref)
}

func prettifyModelDisplayName(id string) string {
	base := datedSnapshotSuffix.ReplaceAllString(id, "")
	segments := strings.Split(base, "-")
	for idx, segment := range segments {
		if segment == "" {
			continue
		}
		runes := []rune(segment)
		runes[0] = unicode.ToUpper(runes[0])
		segments[idx] = string(runes)
	}
	return strings.Join(segments, " ")
}

func sortModelDescriptors(models []appwire.ModelDescriptor) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		datedI := isDatedSnapshotModelID(models[i].Model)
		datedJ := isDatedSnapshotModelID(models[j].Model)
		return datedI != datedJ && !datedI
	})
}

func enrichModelListResponse(cfg hubcore.WebConfig, resp appwire.ModelListResponse) appwire.ModelListResponse {
	resp.Data = enrichModelDescriptors(resp.Data, cfg.ProviderConfig)
	if resp.Data == nil {
		resp.Data = []appwire.ModelDescriptor{}
	}
	sortModelDescriptors(resp.Data)
	resp.Recent = enrichModelDescriptors(resp.Recent, cfg.ProviderConfig)
	return resp
}

func enrichModelDescriptors(models []appwire.ModelDescriptor, providerCfg *providercfg.Config) []appwire.ModelDescriptor {
	if len(models) == 0 {
		return nil
	}
	out := make([]appwire.ModelDescriptor, 0, len(models))
	cat := llm.EmbeddedModelCatalog()
	for _, model := range models {
		if !normalizeModelDescriptor(&model) {
			continue
		}
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		if model.DisplayName == "" {
			model.DisplayName = prettifyModelDisplayName(model.Model)
		}

		if info := catalogModelInfo(cat, behaviorTagFor(providerCfg, model.Provider), model.Model); info != nil {
			if model.ContextWindow == nil && info.ContextWindow > 0 {
				value := info.ContextWindow
				model.ContextWindow = &value
			}
			if model.SupportsTools == nil {
				value := info.SupportsTools
				model.SupportsTools = &value
			}
			if model.SupportsVision == nil {
				value := info.SupportsVision
				model.SupportsVision = &value
			}
			if model.MaxOutputTokens == nil && info.MaxOutputTokens != nil {
				value := *info.MaxOutputTokens
				model.MaxOutputTokens = &value
			}
			if model.SupportsWebSearch == nil && info.SupportsWebSearch != nil {
				value := *info.SupportsWebSearch
				model.SupportsWebSearch = &value
			}
			if model.InputCostPerMillion == nil && info.InputCostPerMillion != nil {
				value := *info.InputCostPerMillion
				model.InputCostPerMillion = &value
			}
			if model.OutputCostPerMillion == nil && info.OutputCostPerMillion != nil {
				value := *info.OutputCostPerMillion
				model.OutputCostPerMillion = &value
			}
			if model.ReasoningEffortLevels == nil &&
				(model.SupportsReasoning == nil || *model.SupportsReasoning) &&
				info.SupportsReasoning && len(info.ReasoningEffortLevels) > 0 {
				model.ReasoningEffortLevels = append([]string(nil), info.ReasoningEffortLevels...)
			}
			if model.SupportsReasoning == nil {
				value := info.SupportsReasoning
				model.SupportsReasoning = &value
			}
		}
		applyInstanceModelOverride(&model, providerCfg, model.Provider, model.Model)
		out = append(out, model)
	}
	return out
}

func applyInstanceModelOverride(entry *appwire.ModelDescriptor, providerCfg *providercfg.Config, provider, model string) {
	if providerCfg == nil {
		return
	}
	var instance *providercfg.InstanceConfig
	for i := range providerCfg.Instances {
		if providerCfg.Instances[i].Name == provider {
			instance = &providerCfg.Instances[i]
			break
		}
	}
	if instance == nil {
		return
	}
	modelConfig, ok := instance.Models[model]
	if !ok {
		return
	}
	switch {
	case modelConfig.Reasoning != nil && !*modelConfig.Reasoning:
		value := false
		entry.SupportsReasoning = &value
		entry.ReasoningEffortLevels = []string{}
	case len(modelConfig.ThinkingLevels) > 0:
		value := true
		entry.SupportsReasoning = &value
		entry.ReasoningEffortLevels = llm.OrderedEffortLevels(modelConfig.ThinkingLevels)
	case modelConfig.Reasoning != nil && *modelConfig.Reasoning:
		value := true
		entry.SupportsReasoning = &value
	}
	if modelConfig.ContextWindow > 0 {
		value := modelConfig.ContextWindow
		entry.ContextWindow = &value
	}
}

func (s *WebServer) fetchLiveModels(ctx context.Context) []appwire.ModelDescriptor {
	s.liveModels.mu.Lock()
	if time.Now().Before(s.liveModels.expires) && s.liveModels.models != nil {
		out := append([]appwire.ModelDescriptor(nil), s.liveModels.models...)
		s.liveModels.mu.Unlock()
		return enrichModelDescriptors(out, s.cfg.ProviderConfig)
	}
	s.liveModels.mu.Unlock()

	client, err := liveModelLoadClient("")
	if err != nil || client == nil {
		return nil
	}
	var out []appwire.ModelDescriptor
	for _, inst := range client.Registry().Instances() {
		if inst.Hidden {
			continue
		}
		listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		listing, listErr := client.Models(listCtx, inst.Name)
		cancel()
		if listErr != nil {
			continue
		}
		for _, m := range listing.Models {
			out = append(out, cmdutil.ModelDescriptorFromResolved(m))
		}
	}
	sortModelDescriptors(out)
	s.liveModels.mu.Lock()
	s.liveModels.models = append([]appwire.ModelDescriptor(nil), out...)
	s.liveModels.expires = time.Now().Add(liveModelsTTL)
	s.liveModels.mu.Unlock()
	return enrichModelDescriptors(out, s.cfg.ProviderConfig)
}

func catalogModelInfo(cat *llm.ModelCatalog, behaviorTag, modelID string) *llm.ModelInfo {
	return cat.ResolveLiveModelInfo(behaviorTag, modelID)
}

func behaviorTagFor(providerCfg *providercfg.Config, name string) string {
	if providerCfg != nil {
		for i := range providerCfg.Instances {
			if providerCfg.Instances[i].Name == name {
				return providercfg.BehaviorTag(string(providerCfg.Instances[i].Type), string(providerCfg.Instances[i].APIStyle))
			}
		}
	}
	return name
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
	out := []appwire.HarnessDescriptor{{ID: "evener", Label: "evener", Kind: "evener"}}
	seen := map[string]bool{"evener": true}
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
