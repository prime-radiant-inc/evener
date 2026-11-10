// InstalledSection: the installed-plugins row list for the Segmented
// Workspace redesign. Every row is the shared InstalledPluginRow - status
// (dot + chips), identity, and the plugin's component counts once the
// components cache holds them - and every ACTION (enable/disable,
// auto-upgrade, upgrade, remove) lives in the PluginDetailBody a row tap
// expands under that row, so the list stays a single-tap-target-per-row on
// both desktop and mobile. The client-side filter matches plugin name OR
// marketplace, case-insensitively.
//
// The section reads the components cache and never fetches: the store's
// fetchPlugins chain owns filling it, keyed by pluginComponentsKey.

import { pluginComponentsKey } from "@evener/appwire-client/state/extensions";
import { useState } from "react";
import { Button, EmptyState, Input } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { InstalledPluginRow } from "../../../plugins/PluginRow";
import { useExtensionsHostState } from "./hostStore";
import styles from "./marketplacesPlugins.module.css";
import { PluginDetailBody, type PluginDetailTarget } from "./PluginDetailBody";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  filterRow: requireClass(styles.filterRow, "marketplacesPlugins.module.css", "filterRow"),
  list: requireClass(styles.list, "marketplacesPlugins.module.css", "list"),
};

export interface InstalledSectionProps {
  /** The one row whose details are expanded, owned by the section's parent. */
  expanded: PluginDetailTarget | null;
  onToggle: (target: { plugin: string; marketplace: string }) => void;
  onBrowse?: () => void;
}

export function InstalledSection({ expanded, onToggle, onBrowse }: InstalledSectionProps) {
  const plugins = useExtensionsHostState((s) => s.plugins) ?? [];
  const pluginComponents = useExtensionsHostState((s) => s.pluginComponents);
  const [filterQuery, setFilterQuery] = useState("");

  const trimmedQuery = filterQuery.trim().toLowerCase();
  const visible =
    trimmedQuery === ""
      ? plugins
      : plugins.filter(
          (p) => p.plugin.toLowerCase().includes(trimmedQuery) || p.marketplace.toLowerCase().includes(trimmedQuery),
        );

  return (
    <section className={CLASS.section}>
      <div className={CLASS.filterRow}>
        <Input
          aria-label="Filter installed"
          value={filterQuery}
          onChange={(event) => setFilterQuery(event.target.value)}
          placeholder="Filter installed…"
        />
      </div>
      <ul aria-label="Installed plugins" className={CLASS.list}>
        {plugins.length === 0 ? (
          <li>
            <EmptyState
              size="compact"
              reason="empty"
              title="No plugins installed yet."
              action={onBrowse && <Button onClick={onBrowse}>Browse plugins</Button>}
            />
          </li>
        ) : visible.length === 0 ? (
          <li>
            <EmptyState size="compact" title={`No plugins match "${filterQuery.trim()}".`} />
          </li>
        ) : (
          visible.map((p) => {
            const key = pluginComponentsKey({ plugin: p.plugin, marketplace: p.marketplace });
            const pair = { plugin: p.plugin, marketplace: p.marketplace };
            const isExpanded =
              expanded !== null && expanded.plugin === p.plugin && expanded.marketplace === p.marketplace;
            return (
              <li key={key}>
                <InstalledPluginRow entry={p} components={pluginComponents.get(key)} onOpen={() => onToggle(pair)} />
                {isExpanded && <PluginDetailBody target={pair} onCollapse={() => onToggle(pair)} />}
              </li>
            );
          })
        )}
      </ul>
    </section>
  );
}
