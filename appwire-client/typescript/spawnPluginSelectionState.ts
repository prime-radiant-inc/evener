// The new-session form's plugin selection: which plugins a launch enables,
// held as either the server's default set or an explicit allow-list. The pure
// derivations both apps' plugin pickers share live here - the effective names
// for a preview, the launchOverrides merge, the stale-name issues a preview
// cannot enumerate, and the toggle/select-all/select-none transitions.
import type { LaunchConfigLayer, PluginPreviewResponse, PluginSelectionError } from "./types.gen";

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

// The one place the effective selection is derived: an explicit allow-list
// wins over the preview's own selected flags, so the UI reflects a toggle the
// moment it happens rather than after the re-preview it triggers settles.
export function selectedPluginNames(selection: PluginSelectionState, preview: PluginPreviewResponse): string[] {
  return preview.plugins
    .filter((plugin) => (selection.mode === "explicit" ? selection.names.includes(plugin.name) : plugin.selected))
    .map((plugin) => plugin.name);
}

export function withPluginSelection(overrides: LaunchConfigLayer, selection: PluginSelectionState): LaunchConfigLayer {
  const { enabledPlugins: _ignored, ...withoutSelection } = overrides;
  return selection.mode === "explicit"
    ? { ...withoutSelection, enabledPlugins: [...selection.names] }
    : withoutSelection;
}

// The inverse of withPluginSelection: the selection a LaunchConfigLayer
// carries. An omitted enabledPlugins is the server's default set; a present
// one (including an empty list) is an explicit allow-list.
export function pluginSelectionFromOverrides(overrides: LaunchConfigLayer): PluginSelectionState {
  return overrides.enabledPlugins === undefined
    ? { mode: "default" }
    : { mode: "explicit", names: [...overrides.enabledPlugins] };
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
