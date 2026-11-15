package hub

import (
	"errors"
	"fmt"
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
	if _, ok := r.Instance(name); !ok {
		return appwire.InstanceModelDetailResponse{}, instanceNotFound(name)
	}
	return c.modelDetailFromResolvable(r, name, model)
}

func (c *hubInstancesController) modelDetailFromResolvable(r *registry.Registry, name, model string) (appwire.InstanceModelDetailResponse, error) {
	name = strings.TrimSpace(name)
	model = strings.TrimSpace(model)
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
		AuthoredOnly:           summary.AuthoredOnly,
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
		ContextWindow:       clonePtr(res.Caps.ContextWindow),
		MaxInputTokens:      clonePtr(res.Caps.MaxInputTokens),
		CompactionThreshold: clonePtr(res.Caps.CompactionThreshold),
		MaxOutputTokens:     clonePtr(res.Caps.MaxOutputTokens),
		Reasoning:           clonePtr(res.Caps.Reasoning),
		DefaultEffort:       registry.StringValue(res.Caps.DefaultEffort),
		EffortValues:        slices.Clone(res.Caps.EffortValues),
		InputModalities:     slices.Clone(res.Caps.InputModalities),
		Tools:               clonePtr(res.Caps.Tools),
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
		CompactionThreshold: clonePtr(m.Caps.CompactionThreshold),
		ContextWindow:       clonePtr(m.Caps.ContextWindow),
		MaxOutputTokens:     clonePtr(m.Caps.MaxOutputTokens),
		Reasoning:           clonePtr(m.Caps.Reasoning),
		DefaultEffort:       registry.StringValue(m.Caps.DefaultEffort),
		EffortValues:        slices.Clone(m.Caps.EffortValues),
		InputModalities:     slices.Clone(m.Caps.InputModalities),
		Tools:               clonePtr(m.Caps.Tools),
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
	return registry.ProvenanceSource(v)
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
	defer c.lockMutation(params.OriginClientId, &err)()

	r := c.reg.Get()
	if _, ok := r.Instance(name); !ok {
		return instanceNotFound(name)
	}
	if params.ExpectedEndpointFingerprint != "" {
		if err := c.auth.verifyEndpointFingerprint(name, params.ExpectedEndpointFingerprint, key, keyErr); err != nil {
			return err
		}
	}

	before, _, err := c.read()
	if err != nil {
		return err
	}
	l, _, err := c.read()
	if err != nil {
		return err
	}
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
		detail, err := c.modelDetailFromResolvable(r, name, model)
		if err != nil {
			return err
		}
		if out != nil {
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

	var committedDetail appwire.InstanceModelDetailResponse
	validate := func(reloaded *registry.Registry) error {
		resAfter, err := reloaded.ResolveInstanceModelFacts(name, model)
		if err != nil {
			return err
		}
		if row.Caps.CompactionThreshold != nil && resAfter.Caps.CompactionThreshold == nil {
			return fmt.Errorf("compaction_threshold exceeds model input ceiling (%d)", resAfter.Caps.InputCeiling())
		}
		detail, err := c.modelDetailFromResolvable(reloaded, name, model)
		if err != nil {
			return err
		}
		committedDetail = detail
		return nil
	}

	if err := c.commitModelMutation(key, before, l, name, beforeProvider, beforeAuthored, beforeAccess, validate); err != nil {
		return err
	}

	if out != nil {
		*out = appwire.InstanceModelEditResponse{
			List:   c.listLocked(key, keyErr),
			Detail: &committedDetail,
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

func checkModelEditFieldPairs(params appwire.InstanceModelEditParams) (hasAny bool, err error) {
	type pair struct {
		field string
		set   bool
		clear bool
	}
	pairs := []pair{
		{"compactionThreshold", params.CompactionThreshold != nil, params.ClearCompactionThreshold},
		{"contextWindow", params.ContextWindow != nil, params.ClearContextWindow},
		{"maxOutputTokens", params.MaxOutputTokens != nil, params.ClearMaxOutputTokens},
		{"reasoning", params.Reasoning != nil, params.ClearReasoning},
		{"defaultEffort", strings.TrimSpace(params.DefaultEffort) != "", params.ClearDefaultEffort},
		{"effortValues", params.EffortValues != nil, params.ClearEffortValues},
		{"inputModalities", params.InputModalities != nil, params.ClearInputModalities},
		{"tools", params.Tools != nil, params.ClearTools},
		{"protocol", strings.TrimSpace(params.Protocol) != "", params.ClearProtocol},
		{"surface", strings.TrimSpace(params.Surface) != "", params.ClearSurface},
		{"thinkingFormat", strings.TrimSpace(params.ThinkingFormat) != "", params.ClearThinkingFormat},
		{"thinkingShape", strings.TrimSpace(params.ThinkingShape) != "", params.ClearThinkingShape},
		{"wireId", strings.TrimSpace(params.WireID) != "", params.ClearWireID},
		{"aliasOf", strings.TrimSpace(params.AliasOf) != "", params.ClearAliasOf},
	}
	for _, p := range pairs {
		if p.set || p.clear {
			hasAny = true
		}
		if p.set && p.clear {
			return hasAny, appwire.InvalidParams(p.field + " and its clear flag are never both meaningful in one request")
		}
	}
	return hasAny, nil
}

func refuseModelEditSetClearPair(params appwire.InstanceModelEditParams) error {
	hasAny, pairErr := checkModelEditFieldPairs(params)
	if params.RemoveCustom && (params.Create || params.ResetAll || hasAny) {
		return appwire.InvalidParams("removeCustom cannot be combined with create, resetAll, or field edits")
	}
	if params.Create && params.ResetAll {
		return appwire.InvalidParams("create and resetAll are mutually exclusive")
	}
	if pairErr != nil {
		return pairErr
	}
	if !params.ClearEffortValues && params.EffortValues != nil && len(params.EffortValues) == 0 {
		return appwire.InvalidParams("effortValues cannot be empty")
	}
	if !params.ClearInputModalities && params.InputModalities != nil && len(params.InputModalities) == 0 {
		return appwire.InvalidParams("inputModalities cannot be empty")
	}
	return nil
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
		row.Caps.EffortValues = trimStrings(params.EffortValues)
	}
	if params.ClearInputModalities {
		row.Caps.InputModalities = nil
	} else if len(params.InputModalities) > 0 {
		row.Caps.InputModalities = trimStrings(params.InputModalities)
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
	if row.Caps.CompactionThreshold == nil {
		return nil
	}
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
	if ceiling > 0 && *row.Caps.CompactionThreshold >= ceiling {
		return appwire.InvalidParams(fmt.Sprintf("compaction_threshold (%d) must be less than input ceiling (%d)", *row.Caps.CompactionThreshold, ceiling))
	}
	return nil
}

func modelRowIsBare(row registry.Model) bool {
	return reflect.DeepEqual(row, registry.Model{ID: row.ID})
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return new(*p)
}

func trimStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}
