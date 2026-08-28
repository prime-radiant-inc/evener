import type { ReactElement } from "react";
import type { PluginLaunchCandidate, PluginPreviewResponse } from "../../protocol/types.gen";
import { Button, Switch } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./pluginSelection.module.css";
import {
  type PluginSelectionState,
  pluginSelectionIssues,
  selectAllPlugins,
  selectedPluginNames,
  selectNoPlugins,
  setPluginSelected,
} from "./pluginSelectionState";

export interface PluginSelectionPanelProps {
  preview: PluginPreviewResponse;
  selection: PluginSelectionState;
  removeOnly?: boolean;
  onSelectionChange(next: PluginSelectionState): void;
  onRetry(): void;
}

const CLASS = {
  panel: requireClass(styles.panel, "pluginSelection.module.css", "panel"),
  tools: requireClass(styles.tools, "pluginSelection.module.css", "tools"),
  actions: requireClass(styles.actions, "pluginSelection.module.css", "actions"),
  list: requireClass(styles.list, "pluginSelection.module.css", "list"),
  row: requireClass(styles.row, "pluginSelection.module.css", "row"),
  metadata: requireClass(styles.metadata, "pluginSelection.module.css", "metadata"),
  source: requireClass(styles.source, "pluginSelection.module.css", "source"),
  counts: requireClass(styles.counts, "pluginSelection.module.css", "counts"),
  description: requireClass(styles.description, "pluginSelection.module.css", "description"),
  resultCount: requireClass(styles.resultCount, "pluginSelection.module.css", "resultCount"),
  diagnostics: requireClass(styles.diagnostics, "pluginSelection.module.css", "diagnostics"),
  errors: requireClass(styles.errors, "pluginSelection.module.css", "errors"),
  empty: requireClass(styles.empty, "pluginSelection.module.css", "empty"),
};

function sourceLabel(plugin: PluginLaunchCandidate): string {
  if (plugin.source === "installed" && plugin.marketplace) return `installed from ${plugin.marketplace}`;
  if (plugin.path) return `${plugin.source}: ${plugin.path}`;
  return plugin.source;
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

export function PluginSelectionPanel({
  preview,
  selection,
  removeOnly = false,
  onSelectionChange,
  onRetry,
}: PluginSelectionPanelProps): ReactElement {
  const selectedNames = selection.mode === "explicit" ? new Set(selection.names) : null;
  const issueNames =
    selectedNames ?? new Set(preview.plugins.filter((plugin) => plugin.selected).map((plugin) => plugin.name));
  const selectedCount = selectedPluginNames(selection, preview).length;
  const selectionIssues = pluginSelectionIssues(selection, preview).filter(
    (issue) => !removeOnly || issueNames.has(issue.name),
  );
  const diagnostics = preview.diagnostics ?? [];

  return (
    <section className={CLASS.panel} data-testid="plugin-selection-panel" aria-label="Plugins for this session">
      <div className={CLASS.tools}>
        <div className={CLASS.resultCount} role="status">
          {selectedCount} of {preview.plugins.length} selected
        </div>
        <div className={CLASS.actions}>
          <Button
            variant="quiet"
            size="xs"
            type="button"
            onClick={() => onSelectionChange(selectAllPlugins(preview))}
            aria-label="All"
            disabled={removeOnly}
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

      <div className={CLASS.list} data-testid="plugin-selection-list">
        {preview.plugins.map((plugin) => {
          const selected = selectedNames?.has(plugin.name) ?? plugin.selected;
          return (
            <div className={CLASS.row} key={plugin.name}>
              <Switch
                checked={selected}
                label={plugin.name}
                disabled={removeOnly && !selected}
                onChange={(next) => {
                  if (!removeOnly || !next) onSelectionChange(setPluginSelected(selection, preview, plugin.name, next));
                }}
              />
              <div className={CLASS.metadata} data-testid="plugin-row-metadata">
                <span className={CLASS.source}>{sourceLabel(plugin)}</span>
                <span className={CLASS.counts}>{componentCounts(plugin)}</span>
                {plugin.description && <span className={CLASS.description}>{plugin.description}</span>}
              </div>
            </div>
          );
        })}
      </div>
      {preview.plugins.length === 0 && <p className={CLASS.empty}>No plugins are available for this session.</p>}

      {removeOnly && (
        <div className={CLASS.errors} role="status">
          <strong>Couldn't inspect plugins</strong>
          <span>Only removing plugins is available until the preview succeeds.</span>
          <Button variant="secondary" size="xs" type="button" onClick={onRetry}>
            Retry
          </Button>
        </div>
      )}

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
