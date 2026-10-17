import {
  type LaunchConfigLayer,
  type LaunchConfigResolved,
  type LaunchOption,
  PROMPT_COMPOSITE_SPECS,
  PROMPT_DEPENDENT_WIRE_FIELDS,
} from "@evener/appwire-client";

/** Display-only defaults below the project layer. The resolver includes `repo`
 * only after its reviewed hash is trusted. Never use the effective local value
 * as a fallback, and never send this projection back as a saved layer. */
export function projectInheritedDefaults(resolved: LaunchConfigResolved, options: LaunchOption[]): LaunchConfigLayer {
  const inherited: Record<string, unknown> = {};
  // Runtime floors are known only when they won resolution. A local override
  // may hide an environment or hub value that the response does not expose.
  for (const option of options) {
    if (["builtin", "env", "hub"].includes(resolved.provenance[option.field] ?? "")) {
      inherited[option.wireField] = (resolved.effective as Record<string, unknown>)[option.wireField];
    }
  }
  for (const layer of [resolved.layers.global, resolved.layers.repo]) {
    if (!layer) continue;
    for (const [field, value] of Object.entries(layer)) {
      if (value === undefined || value === null || value === "") continue;
      if (PROMPT_DEPENDENT_WIRE_FIELDS.has(field)) continue;
      if (field === "env") {
        // The resolver removes blocked credential keys. Keep its accepted key
        // inventory, but read values from the lower layer, not the local one.
        const env = Object.fromEntries(
          Object.entries(layer.env ?? {}).filter(([key]) => key in (resolved.effective.env ?? {})),
        );
        inherited.env = { ...(inherited.env as Record<string, string> | undefined), ...env };
      } else if (["skillsDirs", "pluginDirs", "mcpConfigs", "mcps"].includes(field)) {
        inherited[field] = [...((inherited[field] as unknown[] | undefined) ?? []), ...(value as unknown[])];
      } else {
        // Scalars replace, including false/zero. Replacement lists preserve []:
        // modelFallbacks and enabledPlugins deliberately support explicit empty.
        inherited[field] = value;
      }
    }
    for (const [mode, spec] of Object.entries(PROMPT_COMPOSITE_SPECS)) {
      const selection = (layer as Record<string, unknown>)[mode];
      if (!selection) continue;
      delete inherited[spec.fileWire];
      delete inherited[spec.textWire];
      if (selection === "file") inherited[spec.fileWire] = (layer as Record<string, unknown>)[spec.fileWire];
      if (selection === "inline") inherited[spec.textWire] = (layer as Record<string, unknown>)[spec.textWire];
    }
    // Legacy append paths are normalized by the same rule as the resolver.
    if (!layer.systemPromptAppendMode && layer.systemPromptAppend?.length) {
      inherited.systemPromptAppendMode = "file";
      inherited.systemPromptAppendFile = layer.systemPromptAppend[0];
      delete inherited.systemPromptAppendText;
    }
    delete inherited.systemPromptAppend;
  }
  return inherited as LaunchConfigLayer;
}
