// The one plugin row family: the installed list, the catalog list, and the
// launcher all render from these two ListRow compositions, so a plugin row's
// shape changes in one place. The rows are pure compositions - the caller
// owns the data (the components cache entry arrives as a prop) and the
// actions; a row never fetches.

import type { MarketplaceCatalogPlugin, PluginComponents, PluginEntry } from "@evener/appwire-client";
import type { PluginComponentsEntry } from "@evener/appwire-client/state/extensions";
import type { ReactElement } from "react";
import { Button, type CadenceState, Chip, ListRow, StatusDot } from "../../widgets";
import { countsSummary } from "./pluginCounts";
import { pluginStatusChips } from "./pluginStatusChips";

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
    components.hooks?.length ?? 0,
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
  const counts = components?.status === "loaded" ? ` · ${componentsCounts(components.components)}` : "";
  return (
    <ListRow
      leading={<StatusDot state={pluginStatus(entry)} />}
      label={entry.plugin}
      chips={chips.length > 0 ? chips : undefined}
      meta={`@ ${entry.marketplace} · v${entry.version || "unknown"}${counts}`}
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
      label={plugin.name}
      chips={installed ? <Chip kind="installed">Installed</Chip> : undefined}
      meta={`${plugin.description ?? ""}${plugin.category ? ` · ${plugin.category}` : ""}`}
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
