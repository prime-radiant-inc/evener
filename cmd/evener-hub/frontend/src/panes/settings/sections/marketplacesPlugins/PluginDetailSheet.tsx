// PluginDetailSheet: the plugin's own editor for the Segmented Workspace
// redesign. Opens from an InstalledSection row tap; owns every per-plugin
// ACTION (enable/disable, auto-upgrade, upgrade, remove) so the list rows
// stay single-target. A right side Sheet on desktop, a bottom Sheet on
// mobile (same content, geometry follows useIsMobile). The catalog
// description is lazily pulled from the browse cache (browseMarketplace
// no-ops when the marketplace is already cached, so re-opens are free).

import { errorText, marketplaceSourceLabel } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { Button, Chip, ConfirmDialog, Sheet, Switch, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  sheetDesc: requireClass(styles.sheetDesc, "marketplacesPlugins.module.css", "sheetDesc"),
  chipRow: requireClass(styles.chipRow, "marketplacesPlugins.module.css", "chipRow"),
  metaList: requireClass(styles.metaList, "marketplacesPlugins.module.css", "metaList"),
  metaRow: requireClass(styles.metaRow, "marketplacesPlugins.module.css", "metaRow"),
  metaLabel: requireClass(styles.metaLabel, "marketplacesPlugins.module.css", "metaLabel"),
  metaValue: requireClass(styles.metaValue, "marketplacesPlugins.module.css", "metaValue"),
  switchRows: requireClass(styles.switchRows, "marketplacesPlugins.module.css", "switchRows"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
};

export interface PluginDetailSheetProps {
  target: { plugin: string; marketplace: string } | null;
  onClose: () => void;
}

export function PluginDetailSheet({ target, onClose }: PluginDetailSheetProps) {
  const store = useExtensionsHostStore();
  const plugins = useExtensionsHostState((s) => s.plugins);
  const marketplaces = useExtensionsHostState((s) => s.marketplaces) ?? [];
  const browseCatalogs = useExtensionsHostState((s) => s.browseCatalogs);
  const isMobile = useIsMobile();
  const toasts = useToasts();

  const [toggleBusy, setToggleBusy] = useState(false);
  const [autoUpgradeBusy, setAutoUpgradeBusy] = useState(false);
  const [upgradeBusy, setUpgradeBusy] = useState(false);
  const [pendingRemove, setPendingRemove] = useState(false);
  const [removeBusy, setRemoveBusy] = useState(false);

  const entry =
    target === null || plugins === null
      ? undefined
      : plugins.find((p) => p.plugin === target.plugin && p.marketplace === target.marketplace);

  // The entry can vanish under an open sheet - a remove completing (this
  // sheet's own or another client's), or a refetch after external change.
  // An editor for a thing that no longer exists closes itself rather than
  // rendering stale actions.
  useEffect(() => {
    if (target !== null && plugins !== null && entry === undefined) onClose();
  }, [target, plugins, entry, onClose]);

  // Lazy catalog browse for the description. browseMarketplace no-ops when
  // the marketplace already has a cache entry (loaded, errored, or in
  // flight - see the store's own comment), so this fires once per
  // marketplace, not once per open.
  useEffect(() => {
    if (target === null) return;
    if (!store.getState().browseCatalogs.has(target.marketplace)) {
      void store.getState().browseMarketplace(target.marketplace);
    }
  }, [target, store]);

  async function handleToggleEnable(currentlyEnabled: boolean) {
    if (target === null) return;
    setToggleBusy(true);
    try {
      if (currentlyEnabled) await store.getState().disablePlugin(target.plugin, target.marketplace);
      else await store.getState().enablePlugin(target.plugin, target.marketplace);
    } catch (err) {
      toasts.push("error", `Toggle enable failed: ${errorText(err)}`);
    } finally {
      setToggleBusy(false);
    }
  }

  async function handleToggleAutoUpgrade(currentlyAutoUpgrade: boolean) {
    if (target === null) return;
    setAutoUpgradeBusy(true);
    try {
      await store.getState().setPluginAutoUpgrade(target.plugin, target.marketplace, !currentlyAutoUpgrade);
    } catch (err) {
      toasts.push("error", `Toggle auto-upgrade failed: ${errorText(err)}`);
    } finally {
      setAutoUpgradeBusy(false);
    }
  }

  async function handleUpgrade() {
    if (target === null) return;
    setUpgradeBusy(true);
    try {
      await store.getState().upgradePlugin(target.plugin, target.marketplace);
      toasts.push("success", `Checked ${target.plugin} for upgrades`);
    } catch (err) {
      toasts.push("error", `Upgrade failed: ${errorText(err)}`);
    } finally {
      setUpgradeBusy(false);
    }
  }

  async function handleConfirmRemove() {
    if (target === null) return;
    setRemoveBusy(true);
    try {
      await store.getState().removePlugin(target.plugin, target.marketplace);
      toasts.push("success", `Removed ${target.plugin}`);
      setPendingRemove(false);
      // onClose fires via the entry-vanished effect above once the store's
      // updated plugin list lands - no explicit close here.
    } catch (err) {
      toasts.push("error", `Remove failed: ${errorText(err)}`);
    } finally {
      setRemoveBusy(false);
    }
  }

  const open = target !== null && entry !== undefined;

  const cache = target === null ? undefined : browseCatalogs.get(target.marketplace);
  const description =
    cache?.status === "loaded" && entry !== undefined
      ? cache.plugins.find((p) => p.name === entry.plugin)?.description
      : undefined;
  const marketplaceEntry = entry === undefined ? undefined : marketplaces.find((m) => m.name === entry.marketplace);

  return (
    <>
      <Sheet
        open={open}
        onClose={onClose}
        title={entry?.plugin ?? ""}
        side={isMobile ? "bottom" : "right"}
        footer={
          entry !== undefined && (
            <>
              <Button variant="primary" onClick={() => void handleUpgrade()} aria-disabled={upgradeBusy}>
                Upgrade
              </Button>
              <Button variant="danger" onClick={() => setPendingRemove(true)}>
                Remove
              </Button>
            </>
          )
        }
      >
        {entry !== undefined && (
          <>
            {(entry.broken || !entry.enabled || entry.autoUpgrade) && (
              <div className={CLASS.chipRow}>
                {entry.broken && <Chip tone="danger">broken</Chip>}
                {!entry.enabled && <Chip tone="neutral">off by default</Chip>}
                {entry.autoUpgrade && <Chip tone="neutral">auto-upgrade</Chip>}
              </div>
            )}
            {description !== undefined && <p className={CLASS.sheetDesc}>{description}</p>}
            <div className={CLASS.metaList}>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Version</span>
                <span className={`${CLASS.metaValue} ${CLASS.rowMeta}`}>{`v${entry.version || "unknown"}`}</span>
              </div>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Marketplace</span>
                <span className={CLASS.metaValue}>{entry.marketplace}</span>
              </div>
              {marketplaceEntry !== undefined && (
                <div className={CLASS.metaRow}>
                  <span className={CLASS.metaLabel}>Source</span>
                  <span className={`${CLASS.metaValue} ${CLASS.rowMeta}`}>
                    {marketplaceSourceLabel(marketplaceEntry.source)}
                  </span>
                </div>
              )}
            </div>
            <div className={CLASS.switchRows}>
              <Switch
                checked={entry.enabled}
                onChange={() => void handleToggleEnable(entry.enabled)}
                pending={toggleBusy}
                label="Enabled by default"
              />
              <Switch
                checked={entry.autoUpgrade}
                onChange={() => void handleToggleAutoUpgrade(entry.autoUpgrade)}
                pending={autoUpgradeBusy}
                label="Auto-upgrade"
              />
            </div>
          </>
        )}
      </Sheet>
      <ConfirmDialog
        open={pendingRemove && target !== null}
        title="Remove plugin"
        confirmLabel="Remove"
        busy={removeBusy}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(false)}
      >
        {target !== null ? `Remove plugin "${target.plugin}"?` : ""}
      </ConfirmDialog>
    </>
  );
}
