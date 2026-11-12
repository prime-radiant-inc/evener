package hub

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm/registry"
)

var effectiveModelProvenanceFields = []string{
	"ContextWindow",
	"MaxInputTokens",
	"CompactionThreshold",
	"MaxOutputTokens",
	"Reasoning",
	"DefaultEffort",
	"EffortValues",
	"InputModalities",
	"Tools",
	"Protocol",
	"Surface",
	"ThinkingFormat",
	"ThinkingShape",
	"WireID",
	"AliasOf",
}

// GetModel returns one model row's effective capabilities, normalized
// provenance, and exact authored overrides on an instance. It reads the held
// in-memory registry under c.mu.RLock() with zero disk I/O and stays readable
// while WritesRefused is true.
func (c *hubInstancesController) GetModel(params appwire.InstanceModelGetParams) (appwire.InstanceModelDetailResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.reg == nil || c.reg.Get() == nil {
		return appwire.InstanceModelDetailResponse{}, errors.New("providers.toml cannot be read: the provider registry has not loaded")
	}
	return c.modelDetailLocked(c.reg.Get(), params.Name, params.Model)
}

func (c *hubInstancesController) modelDetailLocked(r *registry.Registry, name, model string) (appwire.InstanceModelDetailResponse, error) {
	name = strings.TrimSpace(name)
	model = strings.TrimSpace(model)
	if _, ok := r.Instance(name); !ok {
		return appwire.InstanceModelDetailResponse{}, instanceNotFound(name)
	}
	if model == "" || strings.Contains(model, "*") {
		return appwire.InstanceModelDetailResponse{}, appwire.InvalidParams(fmt.Sprintf("model %q is not an exact model id", model))
	}
	target, err := r.AliasTarget(name, model)
	if err != nil {
		return appwire.InstanceModelDetailResponse{}, appwire.InvalidParams(err.Error())
	}
	models, err := r.InstanceModels(name)
	if err != nil {
		return appwire.InstanceModelDetailResponse{}, appwire.InvalidParams(err.Error())
	}
	idx := slices.IndexFunc(models, func(m registry.InstanceModel) bool { return m.ID == model })
	if idx < 0 {
		return appwire.InstanceModelDetailResponse{}, appwire.InvalidParams(fmt.Sprintf("model %q is not a known model of instance %q", model, name))
	}
	summary := models[idx]
	disabledLockstepTarget := ""
	if target.Instance == name && target.Model != model {
		disabledLockstepTarget = target.Model
	}

	res, err := r.ResolveInstanceModelFacts(name, model)
	if err != nil {
		return appwire.InstanceModelDetailResponse{}, appwire.InvalidParams(err.Error())
	}

	var authored appwire.InstanceModelAuthored
	if m, ok := r.AuthoredModel(name, model); ok {
		authored = instanceModelAuthoredFrom(m)
	}

	return appwire.InstanceModelDetailResponse{
		Name:                   name,
		Model:                  model,
		Disabled:               summary.Disabled,
		DisabledLockstepTarget: disabledLockstepTarget,
		AuthoredOnly:           r.IsAuthoredOnlyModel(name, model),
		Effective:              instanceModelEffectiveFrom(res),
		Provenance:             normalizeModelProvenance(res.Provenance),
		Authored:               authored,
		ThinkingFormats:        registry.ThinkingFormats(),
		ThinkingShapes:         registry.ThinkingShapes(),
		Warnings:               slices.Clone(res.Warnings),
	}, nil
}

func instanceModelEffectiveFrom(res registry.Resolved) appwire.InstanceModelEffective {
	return appwire.InstanceModelEffective{
		ContextWindow:       cloneIntPtr(res.Caps.ContextWindow),
		MaxInputTokens:      cloneIntPtr(res.Caps.MaxInputTokens),
		CompactionThreshold: cloneIntPtr(res.Caps.CompactionThreshold),
		MaxOutputTokens:     cloneIntPtr(res.Caps.MaxOutputTokens),
		Reasoning:           cloneBoolPtr(res.Caps.Reasoning),
		DefaultEffort:       registry.StringValue(res.Caps.DefaultEffort),
		EffortValues:        slices.Clone(res.Caps.EffortValues),
		InputModalities:     slices.Clone(res.Caps.InputModalities),
		Tools:               cloneBoolPtr(res.Caps.Tools),
		Protocol:            res.Protocol,
		Surface:             res.Surface,
		ThinkingFormat:      registry.StringValue(res.Caps.ThinkingFormat),
		ThinkingShape:       registry.StringValue(res.Caps.ThinkingShape),
		WireID:              res.WireID,
		AliasOf:             res.Model.AliasOf,
	}
}

func instanceModelAuthoredFrom(m registry.Model) appwire.InstanceModelAuthored {
	return appwire.InstanceModelAuthored{
		CompactionThreshold: cloneIntPtr(m.Caps.CompactionThreshold),
		ContextWindow:       cloneIntPtr(m.Caps.ContextWindow),
		MaxOutputTokens:     cloneIntPtr(m.Caps.MaxOutputTokens),
		Reasoning:           cloneBoolPtr(m.Caps.Reasoning),
		DefaultEffort:       registry.StringValue(m.Caps.DefaultEffort),
		EffortValues:        slices.Clone(m.Caps.EffortValues),
		InputModalities:     slices.Clone(m.Caps.InputModalities),
		Tools:               cloneBoolPtr(m.Caps.Tools),
		Protocol:            m.Protocol,
		Surface:             m.Surface,
		ThinkingFormat:      registry.StringValue(m.Caps.ThinkingFormat),
		ThinkingShape:       registry.StringValue(m.Caps.ThinkingShape),
		WireID:              m.WireID,
		AliasOf:             m.AliasOf,
	}
}

func normalizeModelProvenance(raw map[string]string) map[string]string {
	out := make(map[string]string, len(effectiveModelProvenanceFields))
	for _, field := range effectiveModelProvenanceFields {
		if norm := normalizeModelProvenanceValue(raw[field]); norm != "" {
			out[field] = norm
		}
	}
	return out
}

func normalizeModelProvenanceValue(v string) string {
	switch {
	case v == "":
		return ""
	case v == registry.LayerSnapshot || strings.HasPrefix(v, registry.LayerSnapshot+"/") ||
		v == registry.LayerCache || strings.HasPrefix(v, registry.LayerCache+"/"):
		return "catalog"
	case v == registry.LayerOverlay || strings.HasPrefix(v, registry.LayerOverlay+"/"):
		return "overlay"
	case v == registry.LayerLive || strings.HasPrefix(v, registry.LayerLive+"/"):
		return "live"
	case v == registry.LayerConfig+"/provider":
		return "provider"
	case strings.HasPrefix(v, registry.LayerConfig+"/glob:") || v == registry.LayerConfig+"/glob":
		return "glob"
	case v == registry.LayerConfig+"/row" || v == registry.LayerConfig:
		return "config"
	case v == "alias":
		return "alias"
	case v == "derived":
		return "derived"
	default:
		return v
	}
}

// EditModel creates, edits, resets, or removes per-model capability and
// routing overrides under [providers.<name>.models."<model>"] in
// providers.toml.
func (c *hubInstancesController) EditModel(params appwire.InstanceModelEditParams, out *appwire.InstanceModelEditResponse) (err error) {
	if err := c.refuseWhenBroken(); err != nil {
		return err
	}
	if err := c.requireAuth(); err != nil {
		return err
	}
	if err := refuseModelEditSetClearPair(params); err != nil {
		return err
	}
	name := strings.TrimSpace(params.Name)
	model := strings.TrimSpace(params.Model)
	if name == "" {
		return instanceNotFound(params.Name)
	}
	if model == "" || strings.Contains(model, "*") {
		return appwire.InvalidParams(fmt.Sprintf("model %q is not an exact model id", params.Model))
	}

	key, keyErr := resolveEndpointFingerprintKey(c.authStateDir())
	defer c.lockMutation(&err)()

	r := c.reg.Get()
	if _, ok := r.Instance(name); !ok {
		return instanceNotFound(name)
	}
	if params.ExpectedEndpointFingerprint != "" {
		if err := c.auth.verifyEndpointFingerprint(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
			return err
		}
	}

	l, _, err := c.read()
	if err != nil {
		return err
	}
	before := cloneLayer(l)
	beforeProvider, beforeAuthored := before.Providers[name]
	beforeAccess := effectiveAccessIdentity(key, r, name, beforeProvider, beforeAuthored)

	p, wasAuthoredProvider := l.Providers[name]
	if !wasAuthoredProvider {
		p = registry.Provider{ID: name}
	}

	if params.RemoveCustom {
		if !r.IsAuthoredOnlyModel(name, model) {
			return appwire.InvalidParams(fmt.Sprintf("model %q on %q is not a custom-only model", model, name))
		}
		delete(p.Models, model)
		if len(p.Models) == 0 {
			p.Models = nil
		}
		if providerIsBare(p) {
			delete(l.Providers, name)
		} else {
			l.Providers[name] = p
		}
		if err := c.commitModelMutation(key, before, l, name, beforeProvider, beforeAuthored, beforeAccess, nil); err != nil {
			return err
		}
		if out != nil {
			*out = appwire.InstanceModelEditResponse{
				List:   c.listLocked(key, keyErr),
				Detail: nil,
			}
		}
		return nil
	}

	existingModels, err := r.InstanceModels(name)
	if err != nil {
		return appwire.InvalidParams(err.Error())
	}
	inInventory := slices.ContainsFunc(existingModels, func(m registry.InstanceModel) bool { return m.ID == model })
	if params.Create {
		if inInventory {
			return appwire.InvalidParams(fmt.Sprintf("model %q already exists on instance %q", model, name))
		}
	} else {
		_, hasAuthoredRow := p.Models[model]
		if !inInventory && !hasAuthoredRow {
			return appwire.InvalidParams(fmt.Sprintf("model %q is not a known model of instance %q", model, name))
		}
	}

	if !params.ClearAliasOf {
		if aliasOf := strings.TrimSpace(params.AliasOf); aliasOf != "" {
			if err := r.CheckAliasTarget(name, model, aliasOf); err != nil {
				return appwire.InvalidParams(err.Error())
			}
		}
	}

	resBefore, resBeforeErr := r.ResolveInstanceModelFacts(name, model)
	hadThresholdBefore := resBeforeErr == nil && resBefore.Caps.CompactionThreshold != nil
	wasAuthoredOnly := params.Create || r.IsAuthoredOnlyModel(name, model)

	row := p.Models[model]
	row.ID = model
	applyModelEditFields(&row, params)

	if err := checkModelCeilingPreWrite(row, params, resBefore, resBeforeErr == nil); err != nil {
		return err
	}

	if !wasAuthoredOnly && modelRowIsBare(row) {
		delete(p.Models, model)
	} else {
		if p.Models == nil {
			p.Models = map[string]registry.Model{}
		}
		p.Models[model] = row
	}
	if len(p.Models) == 0 {
		p.Models = nil
	}

	if !wasAuthoredProvider && providerIsBare(p) {
		if out != nil {
			detail, err := c.modelDetailLocked(r, name, model)
			if err != nil {
				return err
			}
			*out = appwire.InstanceModelEditResponse{
				List:   c.listLocked(key, keyErr),
				Detail: &detail,
			}
		}
		return nil
	}

	if providerIsBare(p) {
		delete(l.Providers, name)
	} else {
		l.Providers[name] = p
	}

	validate := func(reloaded *registry.Registry) error {
		resAfter, err := reloaded.ResolveInstanceModelFacts(name, model)
		if err != nil {
			return err
		}
		if resAfter.Provenance["CompactionThreshold"] == "derived" &&
			(row.Caps.CompactionThreshold != nil || hadThresholdBefore || params.ContextWindow != nil || params.ClearContextWindow) {
			return fmt.Errorf("compaction_threshold exceeds model input ceiling (%d)", resAfter.Caps.InputCeiling())
		}
		return nil
	}

	if err := c.commitModelMutation(key, before, l, name, beforeProvider, beforeAuthored, beforeAccess, validate); err != nil {
		return err
	}

	if out != nil {
		detail, err := c.modelDetailLocked(c.reg.Get(), name, model)
		if err != nil {
			return err
		}
		*out = appwire.InstanceModelEditResponse{
			List:   c.listLocked(key, keyErr),
			Detail: &detail,
		}
	}
	return nil
}

func (c *hubInstancesController) commitModelMutation(
	key []byte,
	before, l *registry.Layer,
	name string,
	beforeProvider registry.Provider,
	beforeAuthored bool,
	beforeAccess string,
	validate func(*registry.Registry) error,
) error {
	afterProvider, afterAuthored := l.Providers[name]
	if !afterAuthored {
		afterProvider = registry.Provider{ID: name}
	}
	if err := c.writeAndReload(before, l, name, "edit model", validate); err != nil {
		if c.applied.peekApplied() && authoredAccessDiffers(beforeProvider, beforeAuthored, afterProvider) {
			c.auth.forgetCredentialRejection(name)
		}
		return err
	}
	afterAccess := effectiveAccessIdentity(key, c.reg.Get(), name, afterProvider, afterAuthored)
	if effectiveAccessChanged(beforeAccess, afterAccess, beforeProvider, beforeAuthored, afterProvider) {
		if c.afterAccessReload != nil {
			c.afterAccessReload()
		}
		c.auth.forgetCredentialRejection(name)
	}
	return nil
}

func refuseModelEditSetClearPair(params appwire.InstanceModelEditParams) error {
	if params.RemoveCustom && (params.Create || params.ResetAll || hasAnyModelFieldEdit(params)) {
		return appwire.InvalidParams("removeCustom cannot be combined with create, resetAll, or field edits")
	}
	if params.Create && params.ResetAll {
		return appwire.InvalidParams("create and resetAll are mutually exclusive")
	}
	type pair struct {
		field string
		both  bool
	}
	pairs := []pair{
		{"compactionThreshold", params.CompactionThreshold != nil && params.ClearCompactionThreshold},
		{"contextWindow", params.ContextWindow != nil && params.ClearContextWindow},
		{"maxOutputTokens", params.MaxOutputTokens != nil && params.ClearMaxOutputTokens},
		{"reasoning", params.Reasoning != nil && params.ClearReasoning},
		{"defaultEffort", strings.TrimSpace(params.DefaultEffort) != "" && params.ClearDefaultEffort},
		{"effortValues", params.EffortValues != nil && params.ClearEffortValues},
		{"inputModalities", params.InputModalities != nil && params.ClearInputModalities},
		{"tools", params.Tools != nil && params.ClearTools},
		{"protocol", strings.TrimSpace(params.Protocol) != "" && params.ClearProtocol},
		{"surface", strings.TrimSpace(params.Surface) != "" && params.ClearSurface},
		{"thinkingFormat", strings.TrimSpace(params.ThinkingFormat) != "" && params.ClearThinkingFormat},
		{"thinkingShape", strings.TrimSpace(params.ThinkingShape) != "" && params.ClearThinkingShape},
		{"wireId", strings.TrimSpace(params.WireID) != "" && params.ClearWireID},
		{"aliasOf", strings.TrimSpace(params.AliasOf) != "" && params.ClearAliasOf},
	}
	for _, p := range pairs {
		if p.both {
			return appwire.InvalidParams(p.field + " and its clear flag are never both meaningful in one request")
		}
	}
	if !params.ClearEffortValues && params.EffortValues != nil && len(params.EffortValues) == 0 {
		return appwire.InvalidParams("effortValues cannot be empty")
	}
	if !params.ClearInputModalities && params.InputModalities != nil && len(params.InputModalities) == 0 {
		return appwire.InvalidParams("inputModalities cannot be empty")
	}
	return nil
}

func hasAnyModelFieldEdit(params appwire.InstanceModelEditParams) bool {
	return params.CompactionThreshold != nil || params.ClearCompactionThreshold ||
		params.ContextWindow != nil || params.ClearContextWindow ||
		params.MaxOutputTokens != nil || params.ClearMaxOutputTokens ||
		params.Reasoning != nil || params.ClearReasoning ||
		strings.TrimSpace(params.DefaultEffort) != "" || params.ClearDefaultEffort ||
		params.EffortValues != nil || params.ClearEffortValues ||
		params.InputModalities != nil || params.ClearInputModalities ||
		params.Tools != nil || params.ClearTools ||
		strings.TrimSpace(params.Protocol) != "" || params.ClearProtocol ||
		strings.TrimSpace(params.Surface) != "" || params.ClearSurface ||
		strings.TrimSpace(params.ThinkingFormat) != "" || params.ClearThinkingFormat ||
		strings.TrimSpace(params.ThinkingShape) != "" || params.ClearThinkingShape ||
		strings.TrimSpace(params.WireID) != "" || params.ClearWireID ||
		strings.TrimSpace(params.AliasOf) != "" || params.ClearAliasOf
}

func applyModelEditFields(row *registry.Model, params appwire.InstanceModelEditParams) {
	if params.ResetAll {
		row.Caps.CompactionThreshold = nil
		row.Caps.ContextWindow = nil
		row.Caps.MaxOutputTokens = nil
		row.Caps.Reasoning = nil
		row.Caps.DefaultEffort = nil
		row.Caps.EffortValues = nil
		row.Caps.InputModalities = nil
		row.Caps.Tools = nil
		row.Protocol = ""
		row.Surface = ""
		row.Caps.ThinkingFormat = nil
		row.Caps.ThinkingShape = nil
		row.WireID = ""
		row.AliasOf = ""
	}
	if params.ClearCompactionThreshold {
		row.Caps.CompactionThreshold = nil
	} else if params.CompactionThreshold != nil {
		row.Caps.CompactionThreshold = new(*params.CompactionThreshold)
	}
	if params.ClearContextWindow {
		row.Caps.ContextWindow = nil
	} else if params.ContextWindow != nil {
		row.Caps.ContextWindow = new(*params.ContextWindow)
	}
	if params.ClearMaxOutputTokens {
		row.Caps.MaxOutputTokens = nil
	} else if params.MaxOutputTokens != nil {
		row.Caps.MaxOutputTokens = new(*params.MaxOutputTokens)
	}
	if params.ClearReasoning {
		row.Caps.Reasoning = nil
	} else if params.Reasoning != nil {
		row.Caps.Reasoning = new(*params.Reasoning)
	}
	if params.ClearDefaultEffort {
		row.Caps.DefaultEffort = nil
	} else if v := strings.TrimSpace(params.DefaultEffort); v != "" {
		row.Caps.DefaultEffort = new(v)
	}
	if params.ClearEffortValues {
		row.Caps.EffortValues = nil
	} else if len(params.EffortValues) > 0 {
		vals := make([]string, 0, len(params.EffortValues))
		for _, v := range params.EffortValues {
			vals = append(vals, strings.TrimSpace(v))
		}
		row.Caps.EffortValues = vals
	}
	if params.ClearInputModalities {
		row.Caps.InputModalities = nil
	} else if len(params.InputModalities) > 0 {
		mods := make([]string, 0, len(params.InputModalities))
		for _, m := range params.InputModalities {
			mods = append(mods, strings.TrimSpace(m))
		}
		row.Caps.InputModalities = mods
	}
	if params.ClearTools {
		row.Caps.Tools = nil
	} else if params.Tools != nil {
		row.Caps.Tools = new(*params.Tools)
	}
	if params.ClearProtocol {
		row.Protocol = ""
	} else if v := strings.TrimSpace(params.Protocol); v != "" {
		row.Protocol = v
	}
	if params.ClearSurface {
		row.Surface = ""
	} else if v := strings.TrimSpace(params.Surface); v != "" {
		row.Surface = v
	}
	if params.ClearThinkingFormat {
		row.Caps.ThinkingFormat = nil
	} else if v := strings.TrimSpace(params.ThinkingFormat); v != "" {
		row.Caps.ThinkingFormat = new(v)
	}
	if params.ClearThinkingShape {
		row.Caps.ThinkingShape = nil
	} else if v := strings.TrimSpace(params.ThinkingShape); v != "" {
		row.Caps.ThinkingShape = new(v)
	}
	if params.ClearWireID {
		row.WireID = ""
	} else if v := strings.TrimSpace(params.WireID); v != "" {
		row.WireID = v
	}
	if params.ClearAliasOf {
		row.AliasOf = ""
	} else if v := strings.TrimSpace(params.AliasOf); v != "" {
		row.AliasOf = v
	}
}

func checkModelCeilingPreWrite(row registry.Model, params appwire.InstanceModelEditParams, resBefore registry.Resolved, resBeforeOK bool) error {
	var cw, maxIn *int
	sameTarget := resBeforeOK && !params.Create && !params.ClearAliasOf && strings.TrimSpace(params.AliasOf) == ""
	if sameTarget {
		maxIn = resBefore.Caps.MaxInputTokens
		if !params.ClearContextWindow {
			cw = resBefore.Caps.ContextWindow
		}
	}
	if row.Caps.ContextWindow != nil {
		cw = row.Caps.ContextWindow
	}
	ceiling := (registry.Caps{ContextWindow: cw, MaxInputTokens: maxIn}).InputCeiling()
	ct := row.Caps.CompactionThreshold
	if ct == nil && sameTarget && !params.ResetAll && !params.ClearCompactionThreshold {
		ct = resBefore.Caps.CompactionThreshold
	}
	if ct != nil && ceiling > 0 && *ct >= ceiling &&
		(params.CompactionThreshold != nil || params.ContextWindow != nil || row.Caps.CompactionThreshold != nil) {
		return appwire.InvalidParams(fmt.Sprintf("compaction_threshold (%d) must be less than input ceiling (%d)", *ct, ceiling))
	}
	return nil
}

func modelRowIsBare(row registry.Model) bool {
	return reflect.DeepEqual(row, registry.Model{ID: row.ID})
}

func cloneLayer(l *registry.Layer) *registry.Layer {
	if l == nil {
		return nil
	}
	out := &registry.Layer{
		Tag:          l.Tag,
		Default:      l.Default,
		DefaultOrder: slices.Clone(l.DefaultOrder),
	}
	if l.Transports != nil {
		out.Transports = make(map[string]registry.Transport, len(l.Transports))
		for k, t := range l.Transports {
			out.Transports[k] = cloneRegistryTransport(t)
		}
	}
	if l.TopGlobs != nil {
		out.TopGlobs = make(map[string]registry.Model, len(l.TopGlobs))
		for k, m := range l.TopGlobs {
			out.TopGlobs[k] = cloneRegistryModel(m)
		}
	}
	if l.Providers != nil {
		out.Providers = make(map[string]registry.Provider, len(l.Providers))
		for k, p := range l.Providers {
			out.Providers[k] = cloneRegistryProvider(p)
		}
	}
	return out
}

func cloneRegistryProvider(p registry.Provider) registry.Provider {
	out := p
	out.InheritModels = cloneBoolPtr(p.InheritModels)
	out.InheritModelsMatching = slices.Clone(p.InheritModelsMatching)
	out.Implicit = cloneBoolPtr(p.Implicit)
	out.Transport = cloneRegistryTransport(p.Transport)
	out.APIKeyEnv = slices.Clone(p.APIKeyEnv)
	out.Headers = maps.Clone(p.Headers)
	out.CredentialHeaders = maps.Clone(p.CredentialHeaders)
	out.Caps = cloneRegistryCaps(p.Caps)
	if p.Models != nil {
		out.Models = make(map[string]registry.Model, len(p.Models))
		for k, m := range p.Models {
			out.Models[k] = cloneRegistryModel(m)
		}
	}
	return out
}

func cloneRegistryModel(m registry.Model) registry.Model {
	out := m
	out.Headers = maps.Clone(m.Headers)
	out.Caps = cloneRegistryCaps(m.Caps)
	out.Disabled = cloneBoolPtr(m.Disabled)
	if m.Transport != nil {
		tr := cloneRegistryTransport(*m.Transport)
		out.Transport = &tr
	}
	return out
}

func cloneRegistryTransport(t registry.Transport) registry.Transport {
	out := t
	out.Vars = maps.Clone(t.Vars)
	out.VarsEnv = maps.Clone(t.VarsEnv)
	out.Body = maps.Clone(t.Body)
	return out
}

func cloneRegistryCaps(c registry.Caps) registry.Caps {
	out := c
	out.ContextWindow = cloneIntPtr(c.ContextWindow)
	out.MaxInputTokens = cloneIntPtr(c.MaxInputTokens)
	out.CompactionThreshold = cloneIntPtr(c.CompactionThreshold)
	out.MaxOutputTokens = cloneIntPtr(c.MaxOutputTokens)
	out.Tools = cloneBoolPtr(c.Tools)
	out.StructuredOutput = cloneBoolPtr(c.StructuredOutput)
	out.Sampling = cloneBoolPtr(c.Sampling)
	out.Reasoning = cloneBoolPtr(c.Reasoning)
	out.ReasoningControls = slices.Clone(c.ReasoningControls)
	out.EffortValues = slices.Clone(c.EffortValues)
	out.EffortParameterUnsupported = cloneBoolPtr(c.EffortParameterUnsupported)
	out.DefaultEffort = cloneStringPtr(c.DefaultEffort)
	out.InputModalities = slices.Clone(c.InputModalities)
	out.KnowledgeCutoff = cloneStringPtr(c.KnowledgeCutoff)
	if c.Cost != nil {
		cost := *c.Cost
		cost.Tiers = slices.Clone(c.Cost.Tiers)
		out.Cost = &cost
	}
	out.Fields = maps.Clone(c.Fields)
	out.MaxTokensField = cloneStringPtr(c.MaxTokensField)
	out.ThinkingFormat = cloneStringPtr(c.ThinkingFormat)
	out.ThinkingShape = cloneStringPtr(c.ThinkingShape)
	out.ThinkingDisplay = cloneStringPtr(c.ThinkingDisplay)
	out.ThinkingAlwaysOn = cloneBoolPtr(c.ThinkingAlwaysOn)
	out.ReasoningField = cloneStringPtr(c.ReasoningField)
	out.ReasoningSummary = cloneStringPtr(c.ReasoningSummary)
	out.ChatTemplateKwargs = maps.Clone(c.ChatTemplateKwargs)
	out.FinishReasonMap = maps.Clone(c.FinishReasonMap)
	out.CacheControl = cloneStringPtr(c.CacheControl)
	out.CacheTTL = cloneStringPtr(c.CacheTTL)
	out.StrictTools = cloneBoolPtr(c.StrictTools)
	out.ToolChoiceForcing = cloneBoolPtr(c.ToolChoiceForcing)
	out.MaxStopSequences = cloneIntPtr(c.MaxStopSequences)
	out.ImageDetail = cloneStringPtr(c.ImageDetail)
	out.ResponsesLite = cloneBoolPtr(c.ResponsesLite)
	out.AssistantAfterToolResult = cloneBoolPtr(c.AssistantAfterToolResult)
	out.ThinkingAsText = cloneBoolPtr(c.ThinkingAsText)
	out.EmptyReasoningContent = cloneBoolPtr(c.EmptyReasoningContent)
	out.StripEmptyContent = cloneBoolPtr(c.StripEmptyContent)
	out.ToolResultName = cloneBoolPtr(c.ToolResultName)
	out.ToolStream = cloneBoolPtr(c.ToolStream)
	out.SessionAffinityHeaders = cloneBoolPtr(c.SessionAffinityHeaders)
	out.MultimodalToolResults = cloneBoolPtr(c.MultimodalToolResults)
	out.WebSearch = cloneBoolPtr(c.WebSearch)
	return out
}

func cloneIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	return new(*p)
}

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	return new(*p)
}

func cloneStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	return new(*p)
}
