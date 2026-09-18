// Advanced launch-config options: schema filtering, override collection, and
// the model/reasoning precedence (floor §1.11). The schema comes from appwire
// "evener/launch/schema" (LaunchOptionSchemaResponse); the collected overrides
// go to thread/start as launchOverrides and to "evener/launch/resolve" for the
// "show resolved config" preview.

import { collectScalar } from "./launchSchema";
import type { LaunchConfigLayer, LaunchOption, LaunchOptionSchemaResponse, MCPServerSpec } from "./types.gen";

// The per-field working value the advanced UI holds, keyed by the option's
// wireField (the LaunchConfigLayer key). `value` is a string for scalar controls
// (boolean tri-state uses "true"/"false"/"(default)"), a string[] for path/model
// lists, a Record for envMap, an MCPServerSpec[] for mcpServerList. `invalid`
// marks a path-kind control whose live validation failed - collect drops it.
export interface AdvancedFieldValue {
  value?: string | string[] | Record<string, string> | MCPServerSpec[];
  invalid?: boolean;
}
export type AdvancedValues = Record<string, AdvancedFieldValue>;

// Filters the schema to the options the spawn advanced panel offers (floor
// §1.11): perLaunch options whose evener driver support is not explicitly false.
export function perLaunchEvenerOptions(schema: LaunchOptionSchemaResponse): LaunchOption[] {
  return schema.options.filter(
    (opt) => opt.kind !== "pluginSelection" && opt.perLaunch && opt.driverSupport?.evener !== false,
  );
}

// Builds the launch overrides from the advanced form state (floor §1.11,
// spawn.js:1077-1120): a field flagged invalid by path validation is dropped,
// list/env/mcp collections are included only when non-empty, and every scalar
// kind goes through launchSchema.collectScalar - the same rule the settings
// form's collectConfig uses - so a boolean left at "(default)", an unchecked
// radio, an empty-after-trim scalar, or an unparsable integer is dropped
// identically in both surfaces (#1444). The result is keyed by each option's
// wireField.
export function collectAdvancedOverrides(options: LaunchOption[], values: AdvancedValues): LaunchConfigLayer {
  const layer: Record<string, unknown> = {};
  for (const opt of options) {
    const field = values[opt.wireField];
    if (!field || field.invalid) continue;
    const v = field.value;
    switch (opt.kind) {
      case "pathList":
      case "modelList":
      case "mcpServerList":
        if (Array.isArray(v) && v.length > 0) layer[opt.wireField] = v;
        break;
      case "envMap":
        if (v && typeof v === "object" && !Array.isArray(v) && Object.keys(v).length > 0) layer[opt.wireField] = v;
        break;
      default: {
        // boolean / integer / select / radio / text / path / modelPicker: the
        // shared scalar collector decides whether the raw string is sent.
        if (typeof v === "string") {
          const collected = collectScalar(opt, v);
          if (collected) layer[opt.wireField] = collected.value;
        }
        break;
      }
    }
  }
  return layer as LaunchConfigLayer;
}

export interface ChipScalars {
  modelProvider?: string;
  model?: string;
  reasoningEffort?: string;
}

// Resolves the effective thread/start scalar fields with schema-wins precedence
// (floor §1.11). The daemon makes the top-level thread/start scalars win over
// launchOverrides ("Legacy scalar fields win over launchOverrides",
// app_threadlifecycle.go), so a schema-set model/reasoningEffort must be hoisted
// into the top-level request or the chip value would silently win instead. A
// schema model arrives already qualified ("provider/model"), so it needs no
// separate modelProvider.
export function resolveScalars(chip: ChipScalars, overrides: LaunchConfigLayer): ChipScalars {
  const result: ChipScalars = {};
  if (overrides.model && overrides.model.trim() !== "") {
    result.model = overrides.model;
  } else {
    result.model = chip.model;
    result.modelProvider = chip.modelProvider;
  }
  result.reasoningEffort =
    overrides.reasoningEffort && overrides.reasoningEffort.trim() !== ""
      ? overrides.reasoningEffort
      : chip.reasoningEffort;
  return result;
}
