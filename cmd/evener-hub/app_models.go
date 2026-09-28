package hub

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

var liveModelLoadClient = func(string) (*llm.Client, error) {
	r, _, err := cmdutil.LoadRegistry()
	if err != nil {
		return nil, err
	}
	return LiveRegistryClient(r), nil
}

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
	resp = enrichModelListResponse(resp)
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
		source, err := sourceForModelHarness(sources, harness)
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

	// The evener launch contract is the picker's source whenever a spawner is
	// configured. It is served through cfg.LaunchModels when the hub wired its
	// cache (production): the underlying `evener launch-check --models` re-lists
	// every provider live on each call, so the picker must not re-run it on
	// every open. A server with no such loader (a bare embedder or a test that
	// hands a spawner directly) falls back to the spawner itself, unchanged.
	if hasEvenerLaunchModelLister(cfg) {
		load := cfg.LaunchModels
		if load == nil {
			load = func(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
				return evenerLaunchModelList(ctx, cfg, workingDir)
			}
		}
		resp, err := load(ctx, params.CWD)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		return resp, nil
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

func sourceForModelHarness(sources *appsource.Registry, harness string) (appsource.Source, error) {
	source, ok := sources.Source(harness)
	if !ok {
		return nil, appwire.Unavailable("model list source is not available: " + harness)
	}
	return source, nil
}

// fetchLaunchModels serves the evener launch model list from a per-working-dir
// cache, generation-gated like fetchLiveModels: a registry Reload or live
// re-apply bumps the holder generation and retires the entry. A cold key blocks
// (the launch check is the only way to answer it); a stale entry is served
// immediately and refreshed behind the request, so a picker open only pays the
// live listing once per key rather than on every open.
func (s *WebServer) fetchLaunchModels(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	gen, ok := liveModelsGeneration(s)
	if !ok {
		// No holder generation to gate on (a bare test/embedder server): a
		// cached entry could never be invalidated, so don't cache.
		return evenerLaunchModelList(ctx, s.cfg, workingDir)
	}
	s.launchModels.mu.Lock()
	entry := s.launchModels.entries[workingDir]
	if entry != nil && entry.gen == gen && time.Since(entry.filledAt) < launchModelsTTL {
		resp := cloneModelListResponse(entry.resp)
		s.launchModels.mu.Unlock()
		return resp, nil
	}
	if entry != nil {
		resp := cloneModelListResponse(entry.resp)
		refresh := !s.launchModels.refreshing[workingDir]
		if refresh {
			s.launchModels.refreshing[workingDir] = true
		}
		s.launchModels.mu.Unlock()
		if refresh {
			go s.refreshLaunchModels(workingDir)
		}
		return resp, nil
	}
	s.launchModels.mu.Unlock()
	return s.loadLaunchModels(ctx, workingDir, gen)
}

// loadLaunchModels runs the launch check for one working dir and caches the
// answer. A failed load caches nothing, so the next request retries instead of
// serving an empty list for the TTL.
func (s *WebServer) loadLaunchModels(ctx context.Context, workingDir string, gen uint64) (appwire.ModelListResponse, error) {
	resp, err := evenerLaunchModelList(ctx, s.cfg, workingDir)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	s.launchModels.mu.Lock()
	if _, exists := s.launchModels.entries[workingDir]; !exists && len(s.launchModels.entries) >= launchModelsMaxEntries {
		evictOldestLaunchModelsEntry(s.launchModels.entries)
	}
	s.launchModels.entries[workingDir] = &launchModelsEntry{
		resp:     cloneModelListResponse(resp),
		gen:      gen,
		filledAt: time.Now(),
	}
	s.launchModels.mu.Unlock()
	return resp, nil
}

// evictOldestLaunchModelsEntry drops the least-recently-filled entry so a
// user-driven key space (the spawn pane's typed working directories) cannot
// grow without bound. Caller holds the cache lock.
func evictOldestLaunchModelsEntry(entries map[string]*launchModelsEntry) {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range entries {
		if oldestKey == "" || entry.filledAt.Before(oldest) {
			oldestKey, oldest = key, entry.filledAt
		}
	}
	if oldestKey != "" {
		delete(entries, oldestKey)
	}
}

// refreshLaunchModels re-fetches a stale entry in the background. It runs on a
// detached context so the request that noticed the staleness returning does not
// cancel the refresh, and clears its refreshing guard on the way out. A failed
// refresh leaves the stale entry in place for the next request to retry.
func (s *WebServer) refreshLaunchModels(workingDir string) {
	defer func() {
		s.launchModels.mu.Lock()
		delete(s.launchModels.refreshing, workingDir)
		s.launchModels.mu.Unlock()
	}()
	gen, ok := liveModelsGeneration(s)
	if !ok {
		return
	}
	// The launch check carries its own evenerLaunchCheckTimeout budget; this
	// outer deadline only bounds a refresh that outlives it (a stray pipe).
	ctx, cancel := context.WithTimeout(context.Background(), evenerLaunchCheckTimeout+time.Minute)
	defer cancel()
	_, _ = s.loadLaunchModels(ctx, workingDir, gen)
}

// warmLaunchModels loads the unscoped launch model list into the cache, so the
// first picker open after hub start is served instantly instead of blocking on
// the live provider listing. Best-effort: a failure leaves the cache cold and
// the next picker open (or prefetch tick) retries.
func (s *WebServer) warmLaunchModels(ctx context.Context) {
	if !hasEvenerLaunchModelLister(s.cfg) {
		return
	}
	gen, ok := liveModelsGeneration(s)
	if !ok {
		return
	}
	_, _ = s.loadLaunchModels(ctx, "", gen)
}

// startLaunchModelsPrefetch warms the unscoped launch model list once at
// startup and refreshes it on interval, mirroring startLiveModelsPrefetch for
// the picker's cached list. It runs through the caller's background runner so
// hub shutdown cancels it; a failed pass is silent and the next tick retries.
func startLaunchModelsPrefetch(ctx context.Context, web *WebServer, interval time.Duration, startBackground func(func())) {
	startBackground(func() {
		warm := func() {
			// The launch check carries its own evenerLaunchCheckTimeout budget;
			// this outer deadline only bounds a pass that outlives it.
			warmCtx, cancel := context.WithTimeout(ctx, evenerLaunchCheckTimeout+time.Minute)
			defer cancel()
			web.warmLaunchModels(warmCtx)
		}
		warm()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				warm()
			}
		}
	})
}

func cloneModelListResponse(resp appwire.ModelListResponse) appwire.ModelListResponse {
	out := resp
	out.Data = cloneModelDescriptors(resp.Data)
	out.Recent = cloneModelDescriptors(resp.Recent)
	out.Diagnostics = append([]appwire.ModelListDiagnostic(nil), resp.Diagnostics...)
	return out
}

// cloneModelDescriptors copies a descriptor slice deeply enough that a caller
// enriching or sorting it (enrichModelListResponse) cannot mutate the cached
// copy through a shared backing array or its nested string slices.
func cloneModelDescriptors(models []appwire.ModelDescriptor) []appwire.ModelDescriptor {
	if models == nil {
		return nil
	}
	out := make([]appwire.ModelDescriptor, len(models))
	for i, model := range models {
		model.Warnings = append([]string(nil), model.Warnings...)
		model.ReasoningEffortLevels = append([]string(nil), model.ReasoningEffortLevels...)
		out[i] = model
	}
	return out
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
		if providerHasLaunchDiagnostic(contract.Diagnostics, ref.Provider) || launchInstanceExists(cfg, ref.Provider) {
			return nil
		}
		return appwire.HubLaunchError("model provider is not reported by the Evener launch harness: " + ref.Provider)
	}
	return appwire.HubLaunchError("model is not configured for Evener launch: " + ref.Qualified())
}

// launchInstanceExists reports whether the registry has the named instance.
// A provider the launch contract never enumerated is still launchable when
// the registry knows it: an instance whose endpoint lists no models (or lists
// them only on the first request) is configured, and refusing it here would
// make the registry's own instance set unusable (spec §11.3).
func launchInstanceExists(cfg hubcore.WebConfig, provider string) bool {
	if cfg.Registry == nil || cfg.Registry.Get() == nil {
		return false
	}
	_, ok := cfg.Registry.Get().Instance(strings.ToLower(strings.TrimSpace(provider)))
	return ok
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

// isDatedSnapshotModelID reports whether a model id names a dated snapshot
// rather than a family. The rule is the registry's own — one implementation
// covering "-YYYYMMDD", Bedrock's "-vN:N" revision and Vertex's "@YYYYMMDD"
// — so the picker cannot disagree with resolution about what is dated.
func isDatedSnapshotModelID(ref string) bool {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return registry.StripDatedSuffix(ref) != ref
}

func prettifyModelDisplayName(id string) string {
	base := registry.StripDatedSuffix(id)
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

// enrichModelListResponse fills in the one display-only field the registry
// does not carry — a human-readable name for a bare model id — and puts the
// rows in the picker's order. Every capability on a descriptor comes from
// the registry's Resolved record (spec §11.3), so there is nothing else to
// merge in here.
func enrichModelListResponse(resp appwire.ModelListResponse) appwire.ModelListResponse {
	resp.Data = withDisplayNames(resp.Data)
	if resp.Data == nil {
		resp.Data = []appwire.ModelDescriptor{}
	}
	sortModelDescriptors(resp.Data)
	resp.Recent = withDisplayNames(resp.Recent)
	return resp
}

// withDisplayNames drops descriptors with no provider or model and gives each
// survivor a display name, prettified from the model id when the source left
// it blank.
func withDisplayNames(models []appwire.ModelDescriptor) []appwire.ModelDescriptor {
	if len(models) == 0 {
		return nil
	}
	out := make([]appwire.ModelDescriptor, 0, len(models))
	for _, model := range models {
		if !normalizeModelDescriptor(&model) {
			continue
		}
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		if model.DisplayName == "" {
			model.DisplayName = prettifyModelDisplayName(model.Model)
		}
		out = append(out, model)
	}
	return out
}

func (s *WebServer) fetchLiveModels(ctx context.Context) []appwire.ModelDescriptor {
	// Generation-gated: every holder Reload or live re-apply bumps the
	// generation, so a mutation or refresh between fill and read misses
	// the cache automatically — no explicit invalidation call sites to
	// keep in sync with every mutation path.
	gen, ok := liveModelsGeneration(s)
	s.liveModels.mu.Lock()
	if ok && s.liveModels.gen == gen && time.Now().Before(s.liveModels.expires) && s.liveModels.models != nil {
		out := append([]appwire.ModelDescriptor(nil), s.liveModels.models...)
		s.liveModels.mu.Unlock()
		return out
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
		if client.Registry().LaunchMintsCredentialCommand(inst.Name) {
			// The hub never executes a credential command (spec §10.1):
			// a command-credentialed instance's live listing is the
			// child's to make; the picker serves its registry rows,
			// resolved at facts depth — every advertised fact, no
			// credential materialized.
			rows, err := client.Registry().InstanceModels(inst.Name)
			if err != nil {
				continue
			}
			for _, row := range rows {
				if row.Disabled {
					continue
				}
				res, err := client.Registry().ResolveInstanceModelFacts(inst.Name, row.ID)
				if err != nil || res.Model.Hidden || llm.LiveSaysNoTools(res) {
					// The same §5 visibility filter the child's own
					// listing applies (resolveListing): a hidden row
					// never reaches the picker either.
					continue
				}
				out = append(out, cmdutil.ModelDescriptorFromResolved(res))
			}
			continue
		}
		// The listing is an authenticated request like every other hub
		// fetch: bind this client's own registry root (see
		// withScopedCodexAuth) so a custom root reads its own Codex
		// record instead of the process default's.
		listCtx, cancel := context.WithTimeout(withScopedCodexAuth(ctx, client.Registry()), instanceLiveListTimeout)
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
	// A nil holder (bare test servers) skips the cache: with no
	// generation clock the entry could never be invalidated.
	if !ok {
		return out
	}
	s.liveModels.mu.Lock()
	s.liveModels.models = append([]appwire.ModelDescriptor(nil), out...)
	s.liveModels.expires = time.Now().Add(liveModelsTTL)
	s.liveModels.gen = gen
	s.liveModels.mu.Unlock()
	return out
}

// liveModelsGeneration reports the holder generation the model cache is
// gated on. False when the server has no holder: caching is disabled.
func liveModelsGeneration(s *WebServer) (uint64, bool) {
	if s == nil || s.cfg.Registry == nil {
		return 0, false
	}
	return s.cfg.Registry.Generation(), true
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

func launchHarnessDescriptors() []appwire.HarnessDescriptor {
	return []appwire.HarnessDescriptor{{ID: "evener", Label: "evener", Kind: "evener"}}
}
