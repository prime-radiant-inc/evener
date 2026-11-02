package launchconfig

import (
	"maps"
	"strings"

	"primeradiant.com/evener/appwire"
)

// Runtime-default sources for provenance: a value the layered files did not
// set, supplied by the environment or by evener's built-in default. They sit
// below every file layer ("env"/"builtin" lose to any layer value) but above
// nothing — they are the floor of the fallback chain, mirroring the agent's
// own flag > env > builtin precedence for a session launched now.
const (
	LayerEnv     LayerName = "env"
	LayerBuiltin LayerName = "builtin"
	// LayerHub is the hub.toml floor source: the hub-wide default applied to
	// fields the hub owns (api_log today) when every file layer left them
	// unset. It beats the builtin floor for those fields and loses to any
	// layer value.
	LayerHub LayerName = "hub"
)

// ApplyEnvDefaults returns resolved with the effective layer's
// still-unset fields filled from the environment floor only: the schema's
// EnvFallback set (EVENER_MODEL, EVENER_REASONING_EFFORT,
// EVENER_OPENAI_RESPONSES_CONTINUATION). A field any file layer already set
// is left untouched; provenance records "env" for the fields filled.
//
// This is the floor spawn decisions need (a session started now would run
// with the env model), without pinning the agent's built-in defaults into
// the spawned argv — the agent applies its own, so the hub must not
// duplicate them here (version-skew hazard).
//
// The getenv function is injected by the caller so tests stay deterministic
// and never read the ambient environment; production callers pass os.Getenv.
func ApplyEnvDefaults(resolved Resolved, getenv func(string) string, schema []LaunchOption) Resolved {
	out := resolved
	// Only Provenance is deep-copied: this function writes new entries into it
	// (markDefault), so the caller's map must not be aliased. The Effective
	// layer is not mutated — only its scalar fields are assigned, which copies
	// the value, not the slice/map headers — so sharing the input's slice and
	// map headers (SkillsDirs, MCPs, Env, etc.) is safe as long as nothing
	// appends to or rekeys them. Callers do not mutate the returned Resolved's
	// Effective slices after this returns.
	if out.Provenance == nil {
		out.Provenance = map[string]LayerName{}
	} else {
		out.Provenance = cloneProvenance(resolved.Provenance)
	}

	for _, opt := range schema {
		if opt.EnvFallback == nil {
			continue
		}
		// All env-fallback fields are string scalars.
		defaultString(&out, opt, strings.TrimSpace(getenv(opt.EnvFallback.Name)), LayerEnv)
	}
	return out
}

// ApplyRuntimeDefaults returns a copy of resolved with the effective layer's
// still-unset fields filled from two floors of the fallback chain, in order:
//
//  1. env: the schema's EnvFallback set, and
//  2. builtin: the agent's own defaults declared on each LaunchOption
//     (BuiltinDefault / BuiltinDefaultInt / BuiltinDefaultBool) — the floor
//     the agent itself applies when flag, env, and layer are all absent.
//
// A field any file layer already set is left untouched (layer values win),
// and fields with no env or builtin default stay unset rather than inventing
// a value. Provenance records "env"/"builtin" for the fields this function
// filled. The schema is the single source of truth for the defaults — this
// function applies them, it does not duplicate them.
//
// This is the full stack the launch/resolve RPC answers with — "what would a
// session started now run with" — and is for display/preview surfaces; the
// spawn path uses ApplyEnvDefaults only (see its doc comment).
func ApplyRuntimeDefaults(resolved Resolved, getenv func(string) string, schema []LaunchOption) Resolved {
	out := ApplyEnvDefaults(resolved, getenv, schema)

	for _, opt := range schema {
		defaultString(&out, opt, opt.BuiltinDefault, LayerBuiltin)
		defaultIntPtr(&out, opt, opt.BuiltinDefaultInt)
		defaultBoolPtr(&out, opt, opt.BuiltinDefaultBool)
	}
	return out
}

// stringFields, intFields and boolFields map a schema row's WireField to the
// Layer field a runtime default fills. A row whose WireField is absent (a
// dynamic default such as fast_cheap_model) has no static value to apply.
var (
	stringFields = map[string]func(*Layer) *string{
		"model":                       func(l *Layer) *string { return &l.Model },
		"reasoningEffort":             func(l *Layer) *string { return &l.ReasoningEffort },
		"agent":                       func(l *Layer) *string { return &l.Agent },
		"providerIdleTimeout":         func(l *Layer) *string { return &l.ProviderIdleTimeout },
		"contextStrategy":             func(l *Layer) *string { return &l.ContextStrategy },
		"sandbox":                     func(l *Layer) *string { return &l.Sandbox },
		"openAIResponsesContinuation": func(l *Layer) *string { return &l.OpenAIResponsesContinuation },
		"exportATIFProviderHandles":   func(l *Layer) *string { return &l.ExportATIFProviderHandles },
	}
	intFields = map[string]func(*Layer) **int{
		"maxRounds":                  func(l *Layer) **int { return &l.MaxRounds },
		"maxSubagentDepth":           func(l *Layer) **int { return &l.MaxSubagentDepth },
		"maxConcurrentDelegateTurns": func(l *Layer) **int { return &l.MaxConcurrentDelegateTurns },
		"maxRetainedTerminal":        func(l *Layer) **int { return &l.MaxRetainedTerminal },
		"appReplaySize":              func(l *Layer) **int { return &l.AppReplaySize },
	}
	boolFields = map[string]func(*Layer) **bool{
		"sandboxNet": func(l *Layer) **bool { return &l.SandboxNet },
		"verbose":    func(l *Layer) **bool { return &l.Verbose },
		"apiLog":     func(l *Layer) **bool { return &l.APILog },
	}
)

// markDefault records source as the field's provenance unless a layer already
// owns it.
func markDefault(out *Resolved, field string, source LayerName) {
	if _, taken := out.Provenance[field]; !taken {
		out.Provenance[field] = source
	}
}

// defaultString fills a still-blank string field of the effective layer.
func defaultString(out *Resolved, opt LaunchOption, value string, source LayerName) {
	field, ok := stringFields[opt.WireField]
	if !ok || value == "" {
		return
	}
	if dst := field(&out.Effective); strings.TrimSpace(*dst) == "" {
		*dst = value
		markDefault(out, opt.Field, source)
	}
}

// defaultIntPtr fills a still-unset int field of the effective layer with a
// builtin default.
func defaultIntPtr(out *Resolved, opt LaunchOption, value *int) {
	field, ok := intFields[opt.WireField]
	if !ok || value == nil {
		return
	}
	if dst := field(&out.Effective); *dst == nil {
		*dst = appwire.ClonePointer(value)
		markDefault(out, opt.Field, LayerBuiltin)
	}
}

// defaultBoolPtr fills a still-unset bool field of the effective layer with a
// builtin default.
func defaultBoolPtr(out *Resolved, opt LaunchOption, value *bool) {
	field, ok := boolFields[opt.WireField]
	if !ok || value == nil {
		return
	}
	if dst := field(&out.Effective); *dst == nil {
		*dst = appwire.ClonePointer(value)
		markDefault(out, opt.Field, LayerBuiltin)
	}
}

func cloneProvenance(p map[string]LayerName) map[string]LayerName {
	out := make(map[string]LayerName, len(p))
	maps.Copy(out, p)
	return out
}
