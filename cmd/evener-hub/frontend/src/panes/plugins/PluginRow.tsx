// The one plugin row family: the installed list, the catalog list, and the
// launcher all render from these contextual compositions, so a plugin row's
// shape changes in one place. The rows are pure compositions - the caller
// owns the data (the components cache entry arrives as a prop) and the
// actions; a row never fetches.

import type {
  MarketplaceCatalogPlugin,
  PluginComponents,
  PluginEntry,
  PluginLaunchCandidate,
} from "@evener/appwire-client";
import type { PluginComponentsEntry } from "@evener/appwire-client/state/extensions";
import type { KeyboardEvent, ReactElement } from "react";
import { Button, type CadenceState, Chip, ListRow, StatusDot, Switch } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { countsSummary, hookTotal } from "./pluginCounts";
import styles from "./pluginRow.module.css";
import { pluginStatusChips } from "./pluginStatusChips";

const CLASS = {
  row: requireClass(styles.row, "pluginRow.module.css", "row"),
  metadata: requireClass(styles.metadata, "pluginRow.module.css", "metadata"),
  source: requireClass(styles.source, "pluginRow.module.css", "source"),
  counts: requireClass(styles.counts, "pluginRow.module.css", "counts"),
  description: requireClass(styles.description, "pluginRow.module.css", "description"),
  details: requireClass(styles.details, "pluginRow.module.css", "details"),
};

// StatusDot's CadenceState has no "warning" state, so broken maps onto
// "failed" (the only failure-family state) rather than inventing a state the
// shared widget doesn't have.
function pluginStatus(plugin: PluginEntry): CadenceState {
  if (plugin.broken) return "failed";
  if (!plugin.enabled) return "ended";
  return "idle";
}

// The counts segment of an installed row's meta: the loaded inventory's
// per-kind totals in the shared vocabulary.
function componentsCounts(components: PluginComponents): string {
  return countsSummary(
    components.skills?.length ?? 0,
    components.agents?.length ?? 0,
    components.commands?.length ?? 0,
    hookTotal(components),
    components.mcpServers?.length ?? 0,
  );
}

export function InstalledPluginRow({
  entry,
  components,
  onOpen,
}: {
  entry: PluginEntry;
  components?: PluginComponentsEntry;
  onOpen: () => void;
}): ReactElement {
  const chips = pluginStatusChips(entry);
  // Only a loaded cache entry widens the meta: absent, loading and error
  // entries keep the identity-only line - the list-triggered batch fills the
  // cache in, and the row does not fetch.
  const counts = components?.status === "loaded" ? componentsCounts(components.components) : undefined;
  return (
    <ListRow
      data-kind="plugin-row"
      data-mode="installed"
      leading={<StatusDot state={pluginStatus(entry)} />}
      label={entry.plugin}
      chips={chips.length > 0 ? chips : undefined}
      meta={
        <>
          {`@ ${entry.marketplace} · v${entry.version || "unknown"}`}
          {counts !== undefined && (
            <>
              {" "}
              · <span data-kind="plugin-counts">{counts}</span>
            </>
          )}
        </>
      }
      onSelect={onOpen}
    />
  );
}

export function CatalogPluginRow({
  plugin,
  installed,
  onOpen,
  onInstall,
  installDisabled,
}: {
  plugin: MarketplaceCatalogPlugin;
  installed: boolean;
  onOpen: () => void;
  onInstall: () => void;
  installDisabled: boolean;
}): ReactElement {
  return (
    <ListRow
      data-kind="plugin-row"
      data-mode="catalog"
      label={plugin.name}
      chips={installed ? <Chip kind="installed">Installed</Chip> : undefined}
      meta={<span data-kind="plugin-meta">{[plugin.description, plugin.category].filter(Boolean).join(" · ")}</span>}
      onSelect={onOpen}
      // An installed row has no trailing control, so it stays one whole-row
      // button; a not-installed row splits around the Install control.
      trailingAction={
        installed ? undefined : (
          <Button size="sm" disabled={installDisabled} onClick={onInstall}>
            Install
          </Button>
        )
      }
    />
  );
}

function launcherSource(candidate: PluginLaunchCandidate): string {
  if (candidate.source === "installed" && candidate.marketplace) return `installed from ${candidate.marketplace}`;
  if (candidate.path) return `${candidate.source}: ${candidate.path}`;
  return candidate.source;
}

/** Launcher mode keeps selection on the Switch and inspection on Details. */
export function LauncherPluginRow({
  candidate,
  selected,
  removeOnly,
  expanded = false,
  onToggleSelected,
  onOpen,
}: {
  candidate: PluginLaunchCandidate;
  selected: boolean;
  removeOnly: boolean;
  /** Whether this row's detail body is expanded; the Details action says
   * which way it goes. */
  expanded?: boolean;
  onToggleSelected: (next: boolean) => void;
  onOpen: () => void;
}): ReactElement {
  const source = launcherSource(candidate);
  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    // Opening the details leaves focus on the row - the old nested sheet took
    // focus on mount, so its Escape never had to reach from here. The row
    // owns what it opened: while expanded, Escape on the row dismisses the
    // details (preventDefault per OverlayPanel's contract) instead of bubbling
    // to an ancestor overlay such as the mobile selection sheet.
    if (expanded && event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      onOpen();
    }
  }

  return (
    // data-plugin names the row to the focus rehome (pluginRowFocus.ts): the
    // row's switch survives the details collapsing, so the keyboard has a
    // control to come back to.
    // biome-ignore lint/a11y/noStaticElementInteractions: Escape is the keyboard dismissal path for the details this row opened, the same contract PluginDetail and OverlayPanel give their own surfaces; the row's real controls keep their own roles
    <div
      className={CLASS.row}
      data-kind="plugin-row"
      data-mode="launcher"
      data-plugin={candidate.name}
      onKeyDown={handleKeyDown}
    >
      <Switch
        checked={selected}
        label={candidate.name}
        disabled={removeOnly && !selected}
        onChange={(next) => {
          if (!removeOnly || !next) onToggleSelected(next);
        }}
        description={
          <span className={CLASS.metadata} data-testid="plugin-row-metadata">
            <span className={CLASS.source}>{source}</span>
            <span className={CLASS.counts} data-kind="plugin-counts">
              {countsSummary(
                candidate.skillCount,
                candidate.agentCount,
                candidate.commandCount,
                candidate.hookCount,
                candidate.mcpCount,
              )}
            </span>
            {candidate.description && <span className={CLASS.description}>{candidate.description}</span>}
            <span className={CLASS.details}>
              <Button variant="link" size="xs" type="button" onClick={onOpen}>
                {expanded ? "Hide details" : "Details"}
              </Button>
            </span>
          </span>
        }
      />
    </div>
  );
}
