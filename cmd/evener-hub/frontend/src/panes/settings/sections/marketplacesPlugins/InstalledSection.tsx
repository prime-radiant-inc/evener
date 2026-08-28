// InstalledSection: the installed-plugins row list for the Segmented
// Workspace redesign. Rows carry status (dot + chips) and identity only -
// every ACTION (enable/disable, auto-upgrade, upgrade, remove) moved into
// the PluginDetailSheet that opens when a row is selected, so the list
// stays a single-tap-target-per-row on both desktop and mobile. The
// client-side filter matches plugin name OR marketplace, case-insensitively.
import { useId, useState } from "react";
import type { PluginEntry } from "../../../../protocol/types.gen";
import { useExtensionsStore } from "../../../../stores/extensions";
import { type CadenceState, Chevron, Chip, Input, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { VisuallyHidden } from "../../../../widgets/internal/VisuallyHidden";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  filterRow: requireClass(styles.filterRow, "marketplacesPlugins.module.css", "filterRow"),
  list: requireClass(styles.list, "marketplacesPlugins.module.css", "list"),
  rowButton: requireClass(styles.rowButton, "marketplacesPlugins.module.css", "rowButton"),
  rowMain: requireClass(styles.rowMain, "marketplacesPlugins.module.css", "rowMain"),
  rowText: requireClass(styles.rowText, "marketplacesPlugins.module.css", "rowText"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
  rowChevron: requireClass(styles.rowChevron, "marketplacesPlugins.module.css", "rowChevron"),
  empty: requireClass(styles.empty, "marketplacesPlugins.module.css", "empty"),
};

export interface InstalledSectionProps {
  onSelect: (target: { plugin: string; marketplace: string }) => void;
}

// See the pre-redesign comment that lived here: StatusDot's CadenceState has
// no "warning" state, so broken maps onto "failed" (the only failure-family
// state) rather than inventing a state the shared widget doesn't have.
function pluginStatus(plugin: PluginEntry): CadenceState {
  if (plugin.broken) return "failed";
  if (!plugin.enabled) return "ended";
  return "idle";
}

export function InstalledSection({ onSelect }: InstalledSectionProps) {
  const plugins = useExtensionsStore((s) => s.plugins) ?? [];
  const [filterQuery, setFilterQuery] = useState("");
  const filterId = useId();

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
        <VisuallyHidden>
          <label htmlFor={filterId}>Filter installed</label>
        </VisuallyHidden>
        <Input
          id={filterId}
          value={filterQuery}
          onChange={(event) => setFilterQuery(event.target.value)}
          placeholder="Filter installed…"
        />
      </div>
      <ul aria-label="Installed plugins" className={CLASS.list}>
        {plugins.length === 0 ? (
          <li className={CLASS.empty}>No plugins installed yet. Install one from Browse.</li>
        ) : visible.length === 0 ? (
          <li className={CLASS.empty}>{`No plugins match "${filterQuery.trim()}".`}</li>
        ) : (
          visible.map((p) => (
            <li key={`${p.plugin}@${p.marketplace}`}>
              <button
                type="button"
                className={CLASS.rowButton}
                onClick={() => onSelect({ plugin: p.plugin, marketplace: p.marketplace })}
              >
                <div className={CLASS.rowMain}>
                  <div className={CLASS.rowText}>
                    <StatusDot state={pluginStatus(p)} />
                    {p.plugin}
                    {p.broken && <Chip tone="danger">broken</Chip>}
                    {!p.enabled && <Chip tone="neutral">disabled</Chip>}
                    {p.autoUpgrade && <Chip tone="neutral">auto-upgrade</Chip>}
                  </div>
                  <div className={CLASS.rowMeta}>{`@ ${p.marketplace} · v${p.version || "unknown"}`}</div>
                </div>
                <span className={CLASS.rowChevron} aria-hidden="true">
                  <Chevron direction="right" />
                </span>
              </button>
            </li>
          ))
        )}
      </ul>
    </section>
  );
}
