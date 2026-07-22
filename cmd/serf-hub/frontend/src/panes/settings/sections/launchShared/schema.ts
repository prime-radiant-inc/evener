// schema.ts is the pure-logic half of the LaunchConfigControls port
// (docs/web-ui/parity/parity-m7-settings.md Appendix B): grouping, layer
// filtering, prompt-composite plumbing, and the populate()/collect() pair,
// all operating on a plain in-memory LaunchFormState instead of the legacy's
// direct DOM reads/writes. LaunchConfigForm.tsx (the rendering half) and the
// field components under this directory are the only callers.
import type { LaunchConfigLayer, LaunchOption, MCPServerSpec } from "../../../../protocol/types.gen";
import type { LaunchConfigLayerName } from "../../../../stores/launchConfig";

export function optionSupportsLayer(opt: LaunchOption, layer: LaunchConfigLayerName): boolean {
  return (opt.defaultableLayers ?? []).includes(layer);
}

// schemaPathKind maps a LaunchOption.pathKind onto the `kind` string
// serf/path/validate expects - the one case that differs is outputFile,
// whose wire/schema spelling ("outputFile") is not what the RPC accepts
// ("output-file"). Mirrors assets/launchconfig.js's own function of the
// same name exactly.
export function schemaPathKind(pathKind: string | undefined): string {
  return pathKind === "outputFile" ? "output-file" : (pathKind ?? "");
}

const COLLECTION_KINDS = new Set(["pathList", "modelList", "envMap", "mcpServerList"]);

export function isCollectionKind(kind: string): boolean {
  return COLLECTION_KINDS.has(kind);
}

export interface PromptCompositeSpec {
  fileWire: string;
  textWire: string;
  fileLabel: string;
  textLabel: string;
}

// Mirrors assets/launchconfig.js's own promptCompositeByMode exactly (field
// labels included) - the 2 composite radios that fold 4 leaf wire fields
// (systemPrompt{,Append}{File,Text}) into one control each.
export const PROMPT_COMPOSITE_SPECS: Record<string, PromptCompositeSpec> = {
  systemPromptMode: {
    fileWire: "systemPromptFile",
    textWire: "systemPromptText",
    fileLabel: "System prompt from file",
    textLabel: "System prompt text",
  },
  systemPromptAppendMode: {
    fileWire: "systemPromptAppendFile",
    textWire: "systemPromptAppendText",
    fileLabel: "Append from file",
    textLabel: "Append text",
  },
};

export function isPromptCompositeWireField(wireField: string): boolean {
  return wireField in PROMPT_COMPOSITE_SPECS;
}

// The 4 leaf wire fields folded into the 2 composites above - excluded from
// the main per-option render loop (LaunchConfigForm renders them only nested
// inside their owning composite's radio options).
export const PROMPT_DEPENDENT_WIRE_FIELDS = new Set([
  "systemPromptFile",
  "systemPromptText",
  "systemPromptAppendFile",
  "systemPromptAppendText",
]);

// Only pathList/modelList kinds ever surface the "explicit empty" checkbox,
// and today only modelFallbacks actually uses it (assets/launchconfig.js's
// own listSupportsExplicitEmpty checks this one wire field by name) - kept
// as a set (not a hardcoded single check) so a future second list field
// could opt in without touching the collect/populate logic below.
const SUPPORTS_EXPLICIT_EMPTY = new Set(["modelFallbacks"]);

export function listSupportsExplicitEmpty(wireField: string): boolean {
  return SUPPORTS_EXPLICIT_EMPTY.has(wireField);
}

// inactivePromptDependent: a leaf prompt field is skipped by both validate()
// and collect() whenever its owning composite radio isn't set to the mode
// that activates it - the user may type into it regardless (buildFormState/
// the field component never disables it), but the value is silently never
// validated or sent. Mirrors assets/launchconfig.js's own function name.
export function inactivePromptDependent(
  wireField: string,
  modes: { systemPromptMode?: string; systemPromptAppendMode?: string },
): boolean {
  switch (wireField) {
    case "systemPromptFile":
      return modes.systemPromptMode !== "file";
    case "systemPromptText":
      return modes.systemPromptMode !== "inline";
    case "systemPromptAppendFile":
      return modes.systemPromptAppendMode !== "file";
    case "systemPromptAppendText":
      return modes.systemPromptAppendMode !== "inline";
    default:
      return false;
  }
}

export interface OptionGroup {
  group: string;
  options: LaunchOption[];
}

// groupOptions splits `options` into segments every time the .group of one
// option differs from the previous option's - NOT a dedup-by-name grouping.
// Matches assets/launchconfig.js's render(): "a section-header row inserted
// every time the group value changes" - the real schema never actually
// revisits a group non-contiguously, but this stays faithful to the legacy
// rule rather than silently reordering if it ever did.
export function groupOptions(options: LaunchOption[]): OptionGroup[] {
  const segments: OptionGroup[] = [];
  for (const option of options) {
    const last = segments[segments.length - 1];
    if (last && last.group === option.group) {
      last.options.push(option);
    } else {
      segments.push({ group: option.group, options: [option] });
    }
  }
  return segments;
}

const GLOBAL_DEFAULT_HINT_MAX_LEN = 80;

// globalDefaultHint: the inline "default: {value}" text next to a field on
// the project-layer page only, sourced from the (separately-fetched) global
// layer - never rendered on the global-layer page itself (no "default" to
// show there). Truncates at 80 chars with an ellipsis, matching
// assets/launchconfig.js's own appendGlobalDefault exactly.
export function globalDefaultHint(
  wireField: string,
  layer: LaunchConfigLayerName,
  globalDefaults: LaunchConfigLayer | undefined,
): string | undefined {
  if (layer !== "project" || !globalDefaults) return undefined;
  const raw = (globalDefaults as Record<string, unknown>)[wireField];
  if (raw === null || raw === undefined || raw === "") return undefined;
  const text = String(raw);
  const truncated = text.length > GLOBAL_DEFAULT_HINT_MAX_LEN ? `${text.slice(0, GLOBAL_DEFAULT_HINT_MAX_LEN)}…` : text;
  return `default: ${truncated}`;
}

// matchesEnvCredentialError: the one backend error shape the engine ever
// attaches inline (to the `env` envMap field) rather than leaving purely to
// the caller's own status-line/toast - mirrors assets/launchconfig.js's
// showBackendError case-insensitive double match exactly.
export function matchesEnvCredentialError(message: string): boolean {
  return /\benv key\b/i.test(message) && /credential/i.test(message);
}

export function emptyChoiceLabel(layer: LaunchConfigLayerName): string {
  return layer === "project" ? "(use global default)" : "(default)";
}

// resolvedEmptyChoice: every select/radio field in the real schema
// (cmd/serf-hub/internal/launchconfig/schema.go) already supplies its own
// custom-labeled value:"" choice (e.g. "(inherit)", "Serf default") - using
// that verbatim instead of ALSO synthesizing a second, generically-labeled
// empty option avoids a real duplicate-"unset"-entry defect the legacy
// engine has (it unconditionally prepends its own placeholder AND then
// renders the schema's own choices, which for every current field already
// includes one - two indistinguishable value:"" options.
// See this task's own report for the write-up of this deliberate fix. A
// hypothetical future option with no empty choice of its own still gets a
// sensible fallback (the generic per-layer placeholder).
export function resolvedEmptyChoice(opt: LaunchOption, layer: LaunchConfigLayerName): { value: ""; label: string } {
  const own = (opt.choices ?? []).find((c) => (c.value ?? "") === "");
  if (own) return { value: "", label: own.label || emptyChoiceLabel(layer) };
  return { value: "", label: emptyChoiceLabel(layer) };
}

// --- form state: the React-side stand-in for the legacy's direct DOM state ---

export interface LaunchFormState {
  // wireField -> string value for every scalar kind (text/select/radio/
  // modelPicker/integer/multilineText/path) - booleans use the same 3-value
  // string domain ("" | "true" | "false") as their <select> control, so
  // ScalarField.tsx has one uniform string in/out contract for every kind.
  scalars: Record<string, string>;
  // wireField -> string[] for pathList/modelList kinds.
  lists: Record<string, string[]>;
  // wireField -> {NAME: value} for envMap kind.
  envMaps: Record<string, Record<string, string>>;
  // wireField -> specs[] for mcpServerList kind.
  mcpLists: Record<string, MCPServerSpec[]>;
  // wireField -> whether the "explicit empty" toggle (modelFallbacks only)
  // is on - lets collectConfig distinguish "never set" (omit the key) from
  // "explicitly cleared" (send []), the one case the wire format itself
  // cannot otherwise represent (see LaunchConfigLayer.MarshalJSON, which
  // special-cases modelFallbacks for exactly this reason).
  explicitEmpty: Record<string, boolean>;
}

function scalarFromLayer(opt: LaunchOption, current: LaunchConfigLayer): string {
  const value = (current as Record<string, unknown>)[opt.wireField];
  if (opt.kind === "boolean") return value === true ? "true" : value === false ? "false" : "";
  if (value === undefined || value === null) return "";
  return String(value);
}

export function buildFormState(options: LaunchOption[], current: LaunchConfigLayer): LaunchFormState {
  const state: LaunchFormState = { scalars: {}, lists: {}, envMaps: {}, mcpLists: {}, explicitEmpty: {} };
  const currentRecord = current as Record<string, unknown>;
  for (const opt of options) {
    const wire = opt.wireField;
    if (isCollectionKind(opt.kind)) {
      if (opt.kind === "envMap") {
        state.envMaps[wire] = { ...((currentRecord[wire] as Record<string, string> | undefined) ?? {}) };
      } else if (opt.kind === "mcpServerList") {
        state.mcpLists[wire] = [...((currentRecord[wire] as MCPServerSpec[] | undefined) ?? [])];
      } else {
        const values = currentRecord[wire] as string[] | undefined;
        state.lists[wire] = [...(values ?? [])];
        // Explicit-empty is checked only when `current` carries the key AND
        // it's a real (already-saved), empty array - matching
        // assets/launchconfig.js's populate(): distinguishes "never set"
        // from "cleared" so an unchecked toggle never gets misread as "on".
        if (listSupportsExplicitEmpty(wire)) {
          state.explicitEmpty[wire] =
            Object.hasOwn(currentRecord, wire) && Array.isArray(values) && values.length === 0;
        }
      }
    } else {
      state.scalars[wire] = scalarFromLayer(opt, current);
    }
  }
  return state;
}

function collectScalar(opt: LaunchOption, raw: string): { value: unknown } | null {
  if (opt.kind === "boolean") {
    if (raw === "true") return { value: true };
    if (raw === "false") return { value: false };
    return null; // unset - omit the key entirely
  }
  const trimmed = raw.trim();
  if (trimmed === "") return null; // omit empty-after-trim scalars, never sent as ""
  return { value: opt.kind === "integer" ? Number(trimmed) : trimmed };
}

// collectConfig builds the save payload from `state` alone (never a cached
// copy of the layer this form was seeded from) - mirrors assets/
// launchconfig.js's collect(): skips inactive prompt-dependents, coerces
// integers, omits empty-after-trim scalars and empty lists/maps, and emits
// an explicit [] for modelFallbacks only when its own toggle is on.
export function collectConfig(options: LaunchOption[], state: LaunchFormState): LaunchConfigLayer {
  const out: Record<string, unknown> = {};
  const modes = {
    systemPromptMode: state.scalars.systemPromptMode,
    systemPromptAppendMode: state.scalars.systemPromptAppendMode,
  };
  for (const opt of options) {
    const wire = opt.wireField;
    if (isCollectionKind(opt.kind)) {
      if (opt.kind === "envMap") {
        const entries = Object.entries(state.envMaps[wire] ?? {});
        if (entries.length > 0) out[wire] = Object.fromEntries(entries);
      } else if (opt.kind === "mcpServerList") {
        const specs = state.mcpLists[wire] ?? [];
        if (specs.length > 0) out[wire] = specs;
      } else {
        const values = state.lists[wire] ?? [];
        if (values.length > 0) out[wire] = values;
        else if (listSupportsExplicitEmpty(wire) && state.explicitEmpty[wire]) out[wire] = [];
      }
      continue;
    }
    if (inactivePromptDependent(wire, modes)) continue;
    const collected = collectScalar(opt, state.scalars[wire] ?? "");
    if (collected) out[wire] = collected.value;
  }
  return out as LaunchConfigLayer;
}
