// InstalledSection: the installed-plugins row list for the Segmented
// Workspace redesign. Rows carry status (dot + chips) and identity only -
// every ACTION (enable/disable, auto-upgrade, upgrade, remove) moved into
// the PluginDetailSheet that opens when a row is selected, so the list
// stays a single-tap-target-per-row on both desktop and mobile. The
// client-side filter matches plugin name OR marketplace, case-insensitively.

import type { PluginEntry } from "@evener/appwire-client";
import { useState } from "react";
import { Button, type CadenceState, EmptyState, Input, ListRow, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useExtensionsHostState } from "./hostStore";
import styles from "./marketplacesPlugins.module.css";
import { pluginStatusChips } from "./pluginStatusChips";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  filterRow: requireClass(styles.filterRow, "marketplacesPlugins.module.css", "filterRow"),
  list: requireClass(styles.list, "marketplacesPlugins.module.css", "list"),
};

export interface InstalledSectionProps {
  onSelect: (target: { plugin: string; marketplace: string }) => void;
  onBrowse?: () => void;
}

// See the pre-redesign comment that lived here: StatusDot's CadenceState has
// no "warning" state, so broken maps onto "failed" (the only failure-family
// state) rather than inventing a state the shared widget doesn't have.
function pluginStatus(plugin: PluginEntry): CadenceState {
  if (plugin.broken) return "failed";
  if (!plugin.enabled) return "ended";
  return "idle";
}

export function InstalledSection({ onSelect, onBrowse }: InstalledSectionProps) {
  const plugins = useExtensionsHostState((s) => s.plugins) ?? [];
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
          visible.map((p) => (
            <li key={`${p.plugin}@${p.marketplace}`}>
              <ListRow
                leading={<StatusDot state={pluginStatus(p)} />}
                label={p.plugin}
                chips={pluginStatusChips(p).length > 0 ? pluginStatusChips(p) : undefined}
                meta={`@ ${p.marketplace} · v${p.version || "unknown"}`}
                onSelect={() => onSelect({ plugin: p.plugin, marketplace: p.marketplace })}
              />
            </li>
          ))
        )}
      </ul>
    </section>
  );
}
