// Advanced launch-config options: schema filtering, override collection, and
// the model/reasoning precedence (floor §1.11). The schema comes from appwire
// "serf/launch/schema" (LaunchOptionSchemaResponse); the collected overrides
// go to thread/start as launchOverrides and to "serf/launch/resolve" for the
// "show resolved config" preview.
import type {
  LaunchConfigLayer,
  LaunchOption,
  LaunchOptionSchemaResponse,
  MCPServerSpec,
} from "../../protocol/types.gen";

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
// §1.11): perLaunch options whose serf driver support is not explicitly false.
export function perLaunchSerfOptions(schema: LaunchOptionSchemaResponse): LaunchOption[] {
  return schema.options.filter((opt) => opt.perLaunch && opt.driverSupport?.serf !== false);
}

// Builds the launch overrides from the advanced form state (floor §1.11,
// spawn.js:1077-1120): a boolean left at "(default)" is dropped (tri-state), an
// unchecked radio / empty scalar is dropped, a field flagged invalid by path
// validation is dropped, and list/env/mcp collections are included only when
// non-empty. The result is keyed by each option's wireField.
export function collectAdvancedOverrides(options: LaunchOption[], values: AdvancedValues): LaunchConfigLayer {
  const layer: Record<string, unknown> = {};
  for (const opt of options) {
    const field = values[opt.wireField];
    if (!field || field.invalid) continue;
    const v = field.value;
    switch (opt.kind) {
      case "boolean":
        if (v === "true") layer[opt.wireField] = true;
        else if (v === "false") layer[opt.wireField] = false;
        break;
      case "integer":
        if (typeof v === "string" && v.trim() !== "") {
          const n = Number.parseInt(v, 10);
          if (!Number.isNaN(n)) layer[opt.wireField] = n;
        }
        break;
      case "pathList":
      case "modelList":
      case "mcpServerList":
        if (Array.isArray(v) && v.length > 0) layer[opt.wireField] = v;
        break;
      case "envMap":
        if (v && typeof v === "object" && !Array.isArray(v) && Object.keys(v).length > 0) layer[opt.wireField] = v;
        break;
      default:
        // select / radio / text / modelPicker: a non-empty string scalar.
        if (typeof v === "string" && v !== "") layer[opt.wireField] = v;
        break;
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
