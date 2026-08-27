import type { LaunchConfigLayer, PluginPreviewResponse, PluginSelectionError } from "../../protocol/types.gen";

export type PluginSelectionState = { mode: "default" } | { mode: "explicit"; names: string[] };

function orderedNames(names: Iterable<string>, preview: PluginPreviewResponse): string[] {
  const wanted = new Set(names);
  const ordered: string[] = [];
  for (const plugin of preview.plugins) {
    if (wanted.delete(plugin.name)) ordered.push(plugin.name);
  }
  // Keep names the server could not currently enumerate. The next Preview or
  // Start must carry them so the server can report a blocking selection error;
  // the user can then remove the stale name explicitly.
  for (const name of wanted) ordered.push(name);
  return ordered;
}

export function withPluginSelection(overrides: LaunchConfigLayer, selection: PluginSelectionState): LaunchConfigLayer {
  const { enabledPlugins: _ignored, ...withoutSelection } = overrides;
  return selection.mode === "explicit"
    ? { ...withoutSelection, enabledPlugins: [...selection.names] }
    : withoutSelection;
}

export function pluginSelectionIssues(
  selection: PluginSelectionState,
  preview: PluginPreviewResponse,
): PluginSelectionError[] {
  const selectionErrors = preview.selectionErrors ?? [];
  if (selection.mode === "default") return selectionErrors;

  const previewNames = new Set(preview.plugins.map((plugin) => plugin.name));
  const reportedErrorNames = new Set(selectionErrors.map((error) => error.name));
  const staleSelectionErrors = selection.names
    .filter((name) => !previewNames.has(name) && !reportedErrorNames.has(name))
    .map((name) => ({ name, reason: "not present in current preview" }));
  return [...selectionErrors, ...staleSelectionErrors];
}

export function reconcilePluginSelection(
  selection: PluginSelectionState,
  preview: PluginPreviewResponse,
): PluginSelectionState {
  if (selection.mode === "default") return selection;
  return { mode: "explicit", names: orderedNames(selection.names, preview) };
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
