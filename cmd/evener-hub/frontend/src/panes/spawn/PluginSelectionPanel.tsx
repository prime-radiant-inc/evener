import { type ReactElement, useMemo, useState } from "react";
import type { PluginLaunchCandidate, PluginPreviewResponse } from "../../protocol/types.gen";
import { Button, Input, Switch } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./pluginSelection.module.css";
import {
  type PluginSelectionState,
  selectAllPlugins,
  selectNoPlugins,
  setPluginSelected,
} from "./pluginSelectionState";

export interface PluginSelectionPanelProps {
  preview: PluginPreviewResponse;
  selection: PluginSelectionState;
  onSelectionChange(next: PluginSelectionState): void;
  onRetry(): void;
}

const CLASS = {
  panel: requireClass(styles.panel, "pluginSelection.module.css", "panel"),
  tools: requireClass(styles.tools, "pluginSelection.module.css", "tools"),
  filter: requireClass(styles.filter, "pluginSelection.module.css", "filter"),
  actions: requireClass(styles.actions, "pluginSelection.module.css", "actions"),
  list: requireClass(styles.list, "pluginSelection.module.css", "list"),
  row: requireClass(styles.row, "pluginSelection.module.css", "row"),
  metadata: requireClass(styles.metadata, "pluginSelection.module.css", "metadata"),
  source: requireClass(styles.source, "pluginSelection.module.css", "source"),
  kind: requireClass(styles.kind, "pluginSelection.module.css", "kind"),
  state: requireClass(styles.state, "pluginSelection.module.css", "state"),
  counts: requireClass(styles.counts, "pluginSelection.module.css", "counts"),
  resultCount: requireClass(styles.resultCount, "pluginSelection.module.css", "resultCount"),
  diagnostics: requireClass(styles.diagnostics, "pluginSelection.module.css", "diagnostics"),
  errors: requireClass(styles.errors, "pluginSelection.module.css", "errors"),
  empty: requireClass(styles.empty, "pluginSelection.module.css", "empty"),
};

function sourceLabel(plugin: PluginLaunchCandidate): string {
  if (plugin.source === "installed" && plugin.marketplace) return `@ ${plugin.marketplace}`;
  return plugin.path || plugin.source;
}

function componentCounts(plugin: PluginLaunchCandidate): string {
  const counts: string[] = [];
  if (plugin.skillCount > 0) counts.push(`${plugin.skillCount} skill${plugin.skillCount === 1 ? "" : "s"}`);
  if (plugin.agentCount > 0) counts.push(`${plugin.agentCount} agent${plugin.agentCount === 1 ? "" : "s"}`);
  if (plugin.commandCount > 0) counts.push(`${plugin.commandCount} command${plugin.commandCount === 1 ? "" : "s"}`);
  if (plugin.hookCount > 0) counts.push(`${plugin.hookCount} hook${plugin.hookCount === 1 ? "" : "s"}`);
  if (plugin.mcpCount > 0) counts.push(`${plugin.mcpCount} MCP server${plugin.mcpCount === 1 ? "" : "s"}`);
  return counts.length > 0 ? counts.join(" · ") : "No components";
}

function matchesFilter(plugin: PluginLaunchCandidate, query: string): boolean {
  const haystack = [plugin.name, plugin.description, plugin.source, plugin.marketplace, plugin.path]
    .filter(Boolean)
    .join(" ")
    .toLocaleLowerCase();
  return haystack.includes(query);
}

export function PluginSelectionPanel({
  preview,
  selection,
  onSelectionChange,
  onRetry,
}: PluginSelectionPanelProps): ReactElement {
  const [filter, setFilter] = useState("");
  const query = filter.trim().toLocaleLowerCase();
  const visiblePlugins = useMemo(
    () => preview.plugins.filter((plugin) => matchesFilter(plugin, query)),
    [preview.plugins, query],
  );
  const selectedNames = selection.mode === "explicit" ? new Set(selection.names) : null;
  const selectedCount = preview.plugins.filter((plugin) => selectedNames?.has(plugin.name) ?? plugin.selected).length;
  const selectionErrors = preview.selectionErrors ?? [];
  const currentNames = new Set(preview.plugins.map((plugin) => plugin.name));
  const reportedErrorNames = new Set(selectionErrors.map((error) => error.name));
  const staleSelectionErrors =
    selection.mode === "explicit"
      ? selection.names
          .filter((name) => !currentNames.has(name) && !reportedErrorNames.has(name))
          .map((name) => ({ name, reason: "no longer available in the current Preview" }))
      : [];
  const selectionIssues = [...selectionErrors, ...staleSelectionErrors];
  const diagnostics = preview.diagnostics ?? [];

  return (
    <section className={CLASS.panel} data-testid="plugin-selection-panel" aria-label="Plugins for this session">
      <div className={CLASS.tools}>
        <div className={CLASS.filter}>
          <label htmlFor="plugin-selection-filter">Filter plugins</label>
          <Input
            id="plugin-selection-filter"
            type="search"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            placeholder="Filter plugins…"
          />
        </div>
        <div className={CLASS.actions}>
          <Button
            variant="quiet"
            size="xs"
            type="button"
            onClick={() => onSelectionChange(selectAllPlugins(preview))}
            aria-label="All"
          >
            All
          </Button>
          <Button
            variant="quiet"
            size="xs"
            type="button"
            onClick={() => onSelectionChange(selectNoPlugins())}
            aria-label="None"
          >
            None
          </Button>
        </div>
      </div>

      <div className={CLASS.resultCount} role="status">
        {selectedCount} of {preview.plugins.length} selected
      </div>
      <div className={CLASS.list}>
        {visiblePlugins.map((plugin) => {
          const selected = selectedNames?.has(plugin.name) ?? plugin.selected;
          return (
            <div className={CLASS.row} key={plugin.name}>
              <Switch
                checked={selected}
                label={plugin.name}
                disabled={false}
                onChange={(next) => onSelectionChange(setPluginSelected(selection, preview, plugin.name, next))}
              />
              <div className={CLASS.metadata}>
                <span className={CLASS.source}>{sourceLabel(plugin)}</span>
                <span className={CLASS.counts}>{componentCounts(plugin)}</span>
                <span className={CLASS.state}>{selected ? "selected for session" : "off for session"}</span>
              </div>
              <span className={CLASS.kind}>{plugin.source === "installed" ? "installed" : plugin.source}</span>
            </div>
          );
        })}
      </div>
      {preview.plugins.length > 0 && (
        <div className={CLASS.resultCount}>
          Showing {visiblePlugins.length} of {preview.plugins.length}
          {visiblePlugins.length < preview.plugins.length ? " matching plugins" : " plugins"}
        </div>
      )}
      {preview.plugins.length === 0 && <p className={CLASS.empty}>No plugins are available for this session.</p>}

      {selectionIssues.length > 0 && (
        <div className={CLASS.errors} role="alert">
          <strong>Selected plugins need attention</strong>
          <ul>
            {selectionIssues.map((error) => (
              <li key={`${error.name}:${error.reason}`}>
                <span>
                  <strong>{error.name}</strong>: {error.reason}
                </span>
                <Button
                  variant="quiet"
                  size="xs"
                  type="button"
                  aria-label={`Remove ${error.name}`}
                  onClick={() => onSelectionChange(setPluginSelected(selection, preview, error.name, false))}
                >
                  Remove
                </Button>
              </li>
            ))}
          </ul>
          <Button variant="secondary" size="xs" type="button" onClick={onRetry}>
            Retry
          </Button>
        </div>
      )}
      {diagnostics.length > 0 && (
        <details className={CLASS.diagnostics}>
          <summary>
            {diagnostics.length} preview diagnostic{diagnostics.length === 1 ? "" : "s"} · Show details
          </summary>
          <ul>
            {diagnostics.map((diagnostic) => (
              <li key={`${diagnostic.name ?? ""}:${diagnostic.path ?? ""}:${diagnostic.message}`}>
                {diagnostic.name && <strong>{diagnostic.name}: </strong>}
                {diagnostic.message}
                {diagnostic.path && <span> ({diagnostic.path})</span>}
              </li>
            ))}
          </ul>
        </details>
      )}
    </section>
  );
}
