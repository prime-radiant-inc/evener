import {
  type AppwireClientLike,
  type PluginPreviewResponse,
  type PluginSelectionState,
  pluginSelectionIssues,
  selectAllPlugins,
  selectedPluginNames,
  selectNoPlugins,
  setPluginSelected,
} from "@evener/appwire-client";
import { type ReactElement, useEffect, useState } from "react";
import { Button, Card, Disclosure, EmptyState, InlineNotice } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { LauncherPluginRow } from "../plugins/PluginRow";
import { LauncherPluginSheet } from "./LauncherPluginSheet";
import styles from "./pluginSelection.module.css";

export interface PluginSelectionPanelProps {
  preview: PluginPreviewResponse;
  selection: PluginSelectionState;
  removeOnly?: boolean;
  /** The client a Details-opened sheet reads component inventories through. */
  client: Pick<AppwireClientLike, "request">;
  /** The launch source the preview resolved against; the sheet's read follows
   * it so its Contents describe the plugins that host would load. */
  host?: string;
  onSelectionChange(next: PluginSelectionState): void;
  onRetry(): void;
}

const CLASS = {
  content: requireClass(styles.content, "pluginSelection.module.css", "content"),
  tools: requireClass(styles.tools, "pluginSelection.module.css", "tools"),
  actions: requireClass(styles.actions, "pluginSelection.module.css", "actions"),
  list: requireClass(styles.list, "pluginSelection.module.css", "list"),
  resultCount: requireClass(styles.resultCount, "pluginSelection.module.css", "resultCount"),
  diagnostics: requireClass(styles.diagnostics, "pluginSelection.module.css", "diagnostics"),
  issueList: requireClass(styles.issueList, "pluginSelection.module.css", "issueList"),
};

export function PluginSelectionPanel({
  preview,
  selection,
  removeOnly = false,
  client,
  host,
  onSelectionChange,
  onRetry,
}: PluginSelectionPanelProps): ReactElement {
  const [diagnosticsOpen, setDiagnosticsOpen] = useState(false);
  // The panel owns the one open sheet: a row's Details opens its candidate,
  // and only one candidate is open at a time. The sheet mounts only while it
  // is open, so its read starts when the sheet does.
  const [sheetName, setSheetName] = useState<string | null>(null);
  const sheetCandidate = preview.plugins.find((plugin) => plugin.name === sheetName);
  useEffect(() => {
    if (sheetName !== null && sheetCandidate === undefined) setSheetName(null);
  }, [sheetName, sheetCandidate]);
  const selectedNames = selection.mode === "explicit" ? new Set(selection.names) : null;
  const issueNames =
    selectedNames ?? new Set(preview.plugins.filter((plugin) => plugin.selected).map((plugin) => plugin.name));
  const selectedCount = selectedPluginNames(selection, preview).length;
  const selectionIssues = pluginSelectionIssues(selection, preview).filter(
    (issue) => !removeOnly || issueNames.has(issue.name),
  );
  const diagnostics = preview.diagnostics ?? [];

  return (
    <Card variant="outlined" role="region" data-testid="plugin-selection-panel" aria-label="Plugins for this session">
      <div className={CLASS.content}>
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

        {diagnostics.length > 0 && (
          <div className={CLASS.diagnostics}>
            <Disclosure
              open={diagnosticsOpen}
              onOpenChange={setDiagnosticsOpen}
              summary={`${diagnostics.length} preview diagnostic${diagnostics.length === 1 ? "" : "s"} · Show details`}
            >
              <ul>
                {diagnostics.map((diagnostic) => (
                  <li key={`${diagnostic.name ?? ""}:${diagnostic.path ?? ""}:${diagnostic.message}`}>
                    {diagnostic.name && <strong>{diagnostic.name}: </strong>}
                    {diagnostic.message}
                    {diagnostic.path && !diagnostic.message.includes(diagnostic.path) && (
                      <span> ({diagnostic.path})</span>
                    )}
                  </li>
                ))}
              </ul>
            </Disclosure>
          </div>
        )}

        <div className={CLASS.list} data-testid="plugin-selection-list">
          {preview.plugins.map((plugin) => {
            const selected = selectedNames?.has(plugin.name) ?? plugin.selected;
            return (
              <LauncherPluginRow
                key={plugin.name}
                candidate={plugin}
                selected={selected}
                removeOnly={removeOnly}
                onToggleSelected={(next) => onSelectionChange(setPluginSelected(selection, preview, plugin.name, next))}
                onOpen={() => setSheetName(plugin.name)}
              />
            );
          })}
        </div>
        {sheetCandidate !== undefined && (
          <LauncherPluginSheet
            open
            onClose={() => setSheetName(null)}
            candidate={sheetCandidate}
            client={client}
            host={host}
            selected={selectedNames?.has(sheetCandidate.name) ?? sheetCandidate.selected}
            selectionDisabled={removeOnly && !(selectedNames?.has(sheetCandidate.name) ?? sheetCandidate.selected)}
            onToggleSelected={(next) => {
              // The same remove-only guard the row's own switch applies: an
              // unreadable preview still allows taking plugins out.
              if (!removeOnly || !next)
                onSelectionChange(setPluginSelected(selection, preview, sheetCandidate.name, next));
            }}
          />
        )}
        {preview.plugins.length === 0 && (
          <EmptyState size="compact" title="No plugins are available for this session." />
        )}

        {removeOnly && (
          <InlineNotice
            tone="danger"
            live="status"
            title="Couldn't inspect plugins"
            action={
              <Button variant="secondary" size="xs" type="button" onClick={onRetry}>
                Retry
              </Button>
            }
          >
            Only removing plugins is available until the preview succeeds.
          </InlineNotice>
        )}

        {selectionIssues.length > 0 && (
          <InlineNotice
            tone="danger"
            title="Selected plugins need attention"
            action={
              <Button variant="secondary" size="xs" type="button" onClick={onRetry}>
                Retry
              </Button>
            }
          >
            <ul className={CLASS.issueList}>
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
          </InlineNotice>
        )}
      </div>
    </Card>
  );
}
