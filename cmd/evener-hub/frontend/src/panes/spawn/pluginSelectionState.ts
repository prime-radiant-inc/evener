import type { LaunchConfigLayer, PluginPreviewResponse } from "../../protocol/types.gen";

export type PluginSelectionState = { mode: "default" } | { mode: "explicit"; names: string[] };

function orderedNames(names: Iterable<string>, preview: PluginPreviewResponse): string[] {
  const wanted = new Set(names);
  const ordered: string[] = [];
  for (const plugin of preview.plugins) {
    if (wanted.delete(plugin.name)) ordered.push(plugin.name);
  }
  // Keep names the server could not currently enumerate when it reports a
  // selection error. This lets the user recover from a transient/stale
  // candidate without silently changing the explicit wire selection.
  for (const name of wanted) ordered.push(name);
  return ordered;
}

export function withPluginSelection(overrides: LaunchConfigLayer, selection: PluginSelectionState): LaunchConfigLayer {
  const { enabledPlugins: _ignored, ...withoutSelection } = overrides;
  return selection.mode === "explicit"
    ? { ...withoutSelection, enabledPlugins: [...selection.names] }
    : withoutSelection;
}

export function reconcilePluginSelection(
  selection: PluginSelectionState,
  preview: PluginPreviewResponse,
): PluginSelectionState {
  if (selection.mode === "default") return selection;
  const candidateNames = new Set(preview.plugins.map((plugin) => plugin.name));
  const erroredNames = new Set((preview.selectionErrors ?? []).map((error) => error.name));
  const retained = selection.names.filter((name) => candidateNames.has(name) || erroredNames.has(name));
  return { mode: "explicit", names: orderedNames(retained, preview) };
}

export function setPluginSelected(
  selection: PluginSelectionState,
  preview: PluginPreviewResponse,
  name: string,
  selected: boolean,
): PluginSelectionState {
  const names =
    selection.mode === "default"
      ? preview.plugins.filter((plugin) => plugin.selected).map((plugin) => plugin.name)
      : selection.names;
  const next = new Set(names);
  if (selected) next.add(name);
  else next.delete(name);
  return { mode: "explicit", names: orderedNames(next, preview) };
}

export function selectAllPlugins(preview: PluginPreviewResponse): PluginSelectionState {
  return { mode: "explicit", names: preview.plugins.map((plugin) => plugin.name) };
}

export function selectNoPlugins(): PluginSelectionState {
  return { mode: "explicit", names: [] };
}
