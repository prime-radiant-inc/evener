// Package cmdutil provides shared helpers for evener CLI binaries.
package cmdutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// GitOriginURLFromDir runs "git remote get-url origin" in dir and returns the
// URL, or "" if not a git repo or no origin remote is configured.
func GitOriginURLFromDir(dir string) string {
	cmd := exec.CommandContext(context.Background(), "git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ModelRef is a provider-qualified model identifier.
type ModelRef struct {
	Provider string
	Model    string
}

func (r ModelRef) Qualified() string {
	if r.Provider == "" || r.Model == "" {
		return strings.Trim(r.Provider+"/"+r.Model, "/")
	}
	return r.Provider + "/" + r.Model
}

// ParseModelRef parses "provider/model" into a ModelRef. Model names may
// contain additional slashes; the provider is the first path segment.
func ParseModelRef(raw string) (ModelRef, error) {
	raw = strings.TrimSpace(raw)
	provider, model, ok := strings.Cut(raw, "/")
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if raw == "" {
		return ModelRef{}, errors.New("model is required: use provider/model")
	}
	if !ok || provider == "" || model == "" {
		return ModelRef{}, fmt.Errorf("model %q must use provider/model", raw)
	}
	return ModelRef{Provider: provider, Model: model}, nil
}

// ResolveModelRef resolves a provider-qualified model from CLI, env, or resume
// metadata. New invocations require --model/EVENER_MODEL to be provider/model;
// resumed sessions keep their persisted provider and bare model.
func ResolveModelRef(modelValue, envModel, resumeProvider, resumeModel string) (ModelRef, error) {
	if strings.TrimSpace(modelValue) != "" {
		return ParseModelRef(modelValue)
	}
	if strings.TrimSpace(envModel) != "" {
		return ParseModelRef(envModel)
	}
	resumeProvider = strings.ToLower(strings.TrimSpace(resumeProvider))
	resumeModel = strings.TrimSpace(resumeModel)
	if resumeProvider != "" && resumeModel != "" {
		return ModelRef{Provider: resumeProvider, Model: resumeModel}, nil
	}
	return ModelRef{}, fmt.Errorf("no model: use --model provider/model or set %s=provider/model", envvars.EVENERModel.Name)
}

// ResolveResumeModelRef resolves the model for an explicit session resume.
// Unlike fresh startup, persisted resume metadata wins over EVENER_MODEL so an
// inherited environment variable cannot silently change the resumed session's
// model. An explicit CLI --model still overrides the persisted model.
func ResolveResumeModelRef(modelValue, envModel, resumeProvider, resumeModel string) (ModelRef, error) {
	if strings.TrimSpace(modelValue) != "" {
		return ParseModelRef(modelValue)
	}
	resumeProvider = strings.ToLower(strings.TrimSpace(resumeProvider))
	resumeModel = strings.TrimSpace(resumeModel)
	if resumeProvider != "" && resumeModel != "" {
		return ModelRef{Provider: resumeProvider, Model: resumeModel}, nil
	}
	if strings.TrimSpace(envModel) != "" {
		return ParseModelRef(envModel)
	}
	return ModelRef{}, fmt.Errorf("no model: use --model provider/model or set %s=provider/model", envvars.EVENERModel.Name)
}

// StringSliceFlag implements flag.Value for a repeatable string flag.
type StringSliceFlag []string

func (f *StringSliceFlag) String() string { return strings.Join(*f, ",") }
func (f *StringSliceFlag) Set(val string) error {
	*f = append(*f, val)
	return nil
}

// ReasoningEffortResolution holds the result of resolving reasoning effort from CLI/env.
type ReasoningEffortResolution struct {
	// Set indicates a CLI/env override was provided (even if it resolves to "").
	Set bool
	// Value is the normalized effort: "low"|"medium"|"high" or "" (meaning none/clear).
	Value string
}

// ResolveReasoningEffort resolves reasoning effort from a CLI flag value and env var.
func ResolveReasoningEffort(cliValue, envValue string) (ReasoningEffortResolution, error) {
	raw := strings.TrimSpace(cliValue)
	set := raw != ""
	if raw == "" {
		raw = strings.TrimSpace(envValue)
		set = raw != ""
	}
	if !set {
		return ReasoningEffortResolution{}, nil
	}

	v := llm.NormalizeReasoningEffort(raw)
	switch v {
	case llm.ReasoningEffortNone:
		// A disable alias (none/null/off/false/0): thinking explicitly off,
		// which the session keeps distinct from "nothing configured".
		return ReasoningEffortResolution{Set: true, Value: llm.ReasoningEffortNone}, nil
	case "minimal", "low", "medium", "high", "xhigh", "max":
		// "xhigh" and "max" are distinct ascending tiers (max above xhigh);
		// the per-model clamp maps either to the nearest level the chosen
		// model actually advertises.
		return ReasoningEffortResolution{Set: true, Value: v}, nil
	default:
		return ReasoningEffortResolution{}, fmt.Errorf("invalid reasoning effort %q (expected minimal|low|medium|high|xhigh|max|none)", raw)
	}
}

// MaxRoundsToConfig converts a --max-rounds CLI value to a SessionConfig value.
//
//	-1 (not specified) → 0 (applyDefaults sets to -1, unlimited)
//	 0 (unlimited)     → -1 (negative means no limit)
//	>0 (explicit)      → that value
func MaxRoundsToConfig(cliValue int) int {
	switch {
	case cliValue > 0:
		return cliValue
	case cliValue == 0:
		return -1
	default:
		return 0
	}
}

// ResolveSessionMeta loads a session meta by ID or finds the most recent one.
func ResolveSessionMeta(stateDir, sessionID string, resumeLast bool) (schema.SessionMeta, error) {
	if resumeLast {
		list, err := schema.ListSessionMetas(stateDir)
		if err != nil {
			return schema.SessionMeta{}, fmt.Errorf("list sessions: %w", err)
		}
		if len(list) == 0 {
			return schema.SessionMeta{}, fmt.Errorf("no saved sessions in %s", stateDir)
		}
		return list[0], nil
	}
	if err := identifier.ValidateSessionID(sessionID); err != nil {
		return schema.SessionMeta{}, fmt.Errorf("invalid local session ID %q: %w", sessionID, err)
	}
	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		return schema.SessionMeta{}, fmt.Errorf("load session %s: %w", sessionID, err)
	}
	return meta, nil
}

// ListModelsFunc returns a function suitable for server.SetListModelsFunc
// that lists one instance's models as wire descriptors.
func ListModelsFunc(client *llm.Client, instance string) func(context.Context) ([]appwire.ModelDescriptor, error) {
	return func(ctx context.Context) ([]appwire.ModelDescriptor, error) {
		listing, err := client.Models(ctx, instance)
		if err != nil {
			return nil, err
		}
		items := make([]appwire.ModelDescriptor, 0, len(listing.Models))
		for _, m := range listing.Models {
			items = append(items, ModelDescriptorFromResolved(m))
		}
		return items, nil
	}
}

// ModelDescriptorFromResolved is the wire view of a resolved row (spec §11.3).
func ModelDescriptorFromResolved(res registry.Resolved) appwire.ModelDescriptor {
	caps := res.Caps
	d := appwire.ModelDescriptor{Provider: res.Instance, Model: res.ModelID, ReasoningEffortLevels: append([]string(nil), caps.EffortValues...)}
	if caps.ContextWindow != nil {
		d.ContextWindow = new(*caps.ContextWindow)
	}
	if caps.MaxInputTokens != nil {
		d.MaxInputTokens = new(*caps.MaxInputTokens)
	}
	if caps.MaxOutputTokens != nil {
		d.MaxOutputTokens = new(*caps.MaxOutputTokens)
	}
	if caps.Tools != nil {
		d.SupportsTools = new(*caps.Tools)
	}
	if len(caps.InputModalities) > 0 {
		d.SupportsVision = new(slices.Contains(caps.InputModalities, "image"))
	}
	if caps.WebSearch != nil {
		d.SupportsWebSearch = new(*caps.WebSearch)
	}
	if caps.Reasoning != nil {
		d.SupportsReasoning = new(*caps.Reasoning)
	}
	if caps.Cost != nil {
		d.InputCostPerMillion = new(caps.Cost.Input)
		d.OutputCostPerMillion = new(caps.Cost.Output)
	}
	return d
}

// ParseAllowedDecisions parses the EVENER_ALLOWED_DECISIONS value into a slice
// of decision keys. It accepts JSON arrays and comma-separated values.
func ParseAllowedDecisions(raw string) []string { return parseAllowedDecisions(raw) }

func parseAllowedDecisions(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var keys []string
		if err := json.Unmarshal([]byte(raw), &keys); err == nil && len(keys) > 0 {
			return trimNonEmpty(keys)
		}
	}
	return trimNonEmpty(strings.Split(raw, ","))
}

// trimNonEmpty trims each element and drops the empties, so the JSON and CSV
// forms of an allowed-decisions list normalize identically.
func trimNonEmpty(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
