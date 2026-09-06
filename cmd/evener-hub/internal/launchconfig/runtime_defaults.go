package launchconfig

import (
	"maps"
	"strings"
)

// Runtime-default sources for provenance: a value the layered files did not
// set, supplied by the environment or by evener's built-in default. They sit
// below every file layer ("env"/"builtin" lose to any layer value) but above
// nothing — they are the floor of the fallback chain, mirroring the agent's
// own flag > env > builtin precedence for a session launched now.
const (
	LayerEnv     LayerName = "env"
	LayerBuiltin LayerName = "builtin"
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
	// (set/LayerEnv), so the caller's map must not be aliased. The Effective
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
	set := func(field string, name LayerName) {
		if _, taken := out.Provenance[field]; !taken {
			out.Provenance[field] = name
		}
	}

	for _, opt := range schema {
		if opt.EnvFallback == nil {
			continue
		}
		// All env-fallback fields are string scalars, so a single read of the
		// env var (keyed by opt.EnvFallback.Name) covers every case. The switch
		// only selects which effective field to check-and-set.
		if v := strings.TrimSpace(getenv(opt.EnvFallback.Name)); v != "" {
			switch opt.WireField {
			case "model":
				if strings.TrimSpace(out.Effective.Model) == "" {
					out.Effective.Model = v
					set(opt.Field, LayerEnv)
				}
			case "reasoningEffort":
				if strings.TrimSpace(out.Effective.ReasoningEffort) == "" {
					out.Effective.ReasoningEffort = v
					set(opt.Field, LayerEnv)
				}
			case "openAIResponsesContinuation":
				if strings.TrimSpace(out.Effective.OpenAIResponsesContinuation) == "" {
					out.Effective.OpenAIResponsesContinuation = v
					set(opt.Field, LayerEnv)
				}
			}
		}
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
	set := func(field string) {
		if _, taken := out.Provenance[field]; !taken {
			out.Provenance[field] = LayerBuiltin
		}
	}

	for _, opt := range schema {
		switch opt.WireField {
		case "agent":
			if strings.TrimSpace(out.Effective.Agent) == "" && opt.BuiltinDefault != "" {
				out.Effective.Agent = opt.BuiltinDefault
				set(opt.Field)
			}
		case "providerIdleTimeout":
			if strings.TrimSpace(out.Effective.ProviderIdleTimeout) == "" && opt.BuiltinDefault != "" {
				out.Effective.ProviderIdleTimeout = opt.BuiltinDefault
				set(opt.Field)
			}
		case "contextStrategy":
			if strings.TrimSpace(out.Effective.ContextStrategy) == "" && opt.BuiltinDefault != "" {
				out.Effective.ContextStrategy = opt.BuiltinDefault
				set(opt.Field)
			}
		case "sandbox":
			if strings.TrimSpace(out.Effective.Sandbox) == "" && opt.BuiltinDefault != "" {
				out.Effective.Sandbox = opt.BuiltinDefault
				set(opt.Field)
			}
		case "sandboxNet":
			if out.Effective.SandboxNet == nil && opt.BuiltinDefaultBool != nil {
				v := *opt.BuiltinDefaultBool
				out.Effective.SandboxNet = &v
				set(opt.Field)
			}
		case "openAIResponsesContinuation":
			if strings.TrimSpace(out.Effective.OpenAIResponsesContinuation) == "" && opt.BuiltinDefault != "" {
				out.Effective.OpenAIResponsesContinuation = opt.BuiltinDefault
				set(opt.Field)
			}
		case "maxRounds":
			if out.Effective.MaxRounds == nil && opt.BuiltinDefaultInt != nil {
				v := *opt.BuiltinDefaultInt
				out.Effective.MaxRounds = &v
				set(opt.Field)
			}
		case "maxSubagentDepth":
			if out.Effective.MaxSubagentDepth == nil && opt.BuiltinDefaultInt != nil {
				v := *opt.BuiltinDefaultInt
				out.Effective.MaxSubagentDepth = &v
				set(opt.Field)
			}
		case "maxConcurrentDelegateTurns":
			if out.Effective.MaxConcurrentDelegateTurns == nil && opt.BuiltinDefaultInt != nil {
				v := *opt.BuiltinDefaultInt
				out.Effective.MaxConcurrentDelegateTurns = &v
				set(opt.Field)
			}
		case "maxRetainedTerminal":
			if out.Effective.MaxRetainedTerminal == nil && opt.BuiltinDefaultInt != nil {
				v := *opt.BuiltinDefaultInt
				out.Effective.MaxRetainedTerminal = &v
				set(opt.Field)
			}
		case "noProjectPrompts":
			if out.Effective.NoProjectPrompts == nil && opt.BuiltinDefaultBool != nil {
				v := *opt.BuiltinDefaultBool
				out.Effective.NoProjectPrompts = &v
				set(opt.Field)
			}
		case "appReplaySize":
			if out.Effective.AppReplaySize == nil && opt.BuiltinDefaultInt != nil {
				v := *opt.BuiltinDefaultInt
				out.Effective.AppReplaySize = &v
				set(opt.Field)
			}
		case "verbose":
			if out.Effective.Verbose == nil && opt.BuiltinDefaultBool != nil {
				v := *opt.BuiltinDefaultBool
				out.Effective.Verbose = &v
				set(opt.Field)
			}
		case "exportATIFProviderHandles":
			if strings.TrimSpace(out.Effective.ExportATIFProviderHandles) == "" && opt.BuiltinDefault != "" {
				out.Effective.ExportATIFProviderHandles = opt.BuiltinDefault
				set(opt.Field)
			}
		}
	}
	return out
}

func cloneProvenance(p map[string]LayerName) map[string]LayerName {
	out := make(map[string]LayerName, len(p))
	maps.Copy(out, p)
	return out
}
