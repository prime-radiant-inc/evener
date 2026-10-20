// PluginDetailSheet: the plugin's own editor for the Segmented Workspace
// redesign. Opens from an InstalledSection row tap; owns every per-plugin
// ACTION (enable/disable, auto-upgrade, upgrade, remove) so the list rows
// stay single-target. A right side Sheet on desktop, a bottom Sheet on
// mobile (same content, geometry follows useIsMobile). The catalog
// description is lazily pulled from the browse cache (browseMarketplace
// no-ops when the marketplace is already cached, so re-opens are free).

import { errorText, marketplaceSourceLabel } from "@evener/appwire-client";
import { HUB_WRITE_BUSY, isHubWriteBusy } from "@evener/appwire-client/state/extensions";
import { useEffect, useRef, useState } from "react";
import { useIsMobile } from "../../../../shell/useIsMobile";
import {
  Button,
  ConfirmDialog,
  PropertyList,
  type PropertyListRow,
  Sheet,
  Switch,
  useFocusRehome,
  useToasts,
} from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import styles from "./marketplacesPlugins.module.css";
import { pluginStatusChips } from "./pluginStatusChips";

const CLASS = {
  sheetDesc: requireClass(styles.sheetDesc, "marketplacesPlugins.module.css", "sheetDesc"),
  chipRow: requireClass(styles.chipRow, "marketplacesPlugins.module.css", "chipRow"),
  metaList: requireClass(styles.metaList, "marketplacesPlugins.module.css", "metaList"),
  switchRows: requireClass(styles.switchRows, "marketplacesPlugins.module.css", "switchRows"),
  focusRoot: requireClass(styles.focusRoot, "marketplacesPlugins.module.css", "focusRoot"),
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
  const hubWriteBusy = useExtensionsHostState((s) => s.hubWriteBusy);
  const pluginsUnavailable = useExtensionsHostState(
    (s) => s.plugins === null || s.pluginsError !== null || s.pluginsLoading,
  );
  const mutationDisabled = hubWriteBusy || pluginsUnavailable;
  const isMobile = useIsMobile();
  const toasts = useToasts();

  const [pendingRemove, setPendingRemove] = useState(false);
  // Any commit that takes away the control holding the keyboard - the
  // Upgrade button a successful upgrade unmounts, or the remove confirm's
  // buttons disabled while the remove runs - would drop focus to <body>; the
  // shared hook keeps it in the sheet or confirm (their first control), and
  // leaves alone focus the user had already moved.
  const focusRoot = useRef<HTMLDivElement>(null);
  useFocusRehome(focusRoot);

  const entry =
    target === null || plugins === null
      ? undefined
      : plugins.find((p) => p.plugin === target.plugin && p.marketplace === target.marketplace);
  const chips = entry === undefined ? [] : pluginStatusChips(entry);

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
    if (target === null || mutationDisabled) return;
    try {
      if (currentlyEnabled) await store.getState().disablePlugin(target.plugin, target.marketplace);
      else await store.getState().enablePlugin(target.plugin, target.marketplace);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Toggle enable failed: ${errorText(err)}`);
    }
  }

  async function handleToggleAutoUpgrade(currentlyAutoUpgrade: boolean) {
    if (target === null || mutationDisabled) return;
    try {
      await store.getState().setPluginAutoUpgrade(target.plugin, target.marketplace, !currentlyAutoUpgrade);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Toggle auto-upgrade failed: ${errorText(err)}`);
    }
  }

  async function handleUpgrade() {
    if (target === null || mutationDisabled) return;
    try {
      await store.getState().upgradePlugin(target.plugin, target.marketplace);
      toasts.push("success", `Upgraded ${target.plugin}`);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Upgrade failed: ${errorText(err)}`);
    }
  }

  async function handleConfirmRemove() {
    if (target === null || mutationDisabled) return;
    try {
      await store.getState().removePlugin(target.plugin, target.marketplace);
      toasts.push("success", `Removed ${target.plugin}`);
      setPendingRemove(false);
      // onClose fires via the entry-vanished effect above once the store's
      // updated plugin list lands - no explicit close here.
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Remove failed: ${errorText(err)}`);
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
    <div ref={focusRoot} className={CLASS.focusRoot}>
      <Sheet
        open={open}
        onClose={onClose}
        title={entry?.plugin ?? ""}
        side={isMobile ? "bottom" : "right"}
        footer={
          entry !== undefined && (
            <>
              {/* Offered only when the hub's update check found one; an older hub
                  without the check offers none. */}
              {entry.updateAvailable && (
                <Button variant="primary" onClick={() => void handleUpgrade()} aria-disabled={mutationDisabled}>
                  Upgrade
                </Button>
              )}
              <Button variant="danger" disabled={mutationDisabled} onClick={() => setPendingRemove(true)}>
                Remove
              </Button>
            </>
          )
        }
      >
        {entry !== undefined && (
          <>
            {chips.length > 0 && <div className={CLASS.chipRow}>{chips}</div>}
            {description !== undefined && <p className={CLASS.sheetDesc}>{description}</p>}
            <div className={CLASS.metaList}>
              <PropertyList
                rows={
                  [
                    { label: "Version", value: `v${entry.version || "unknown"}`, mono: true },
                    { label: "Marketplace", value: entry.marketplace },
                    ...(marketplaceEntry !== undefined
                      ? [{ label: "Source", value: marketplaceSourceLabel(marketplaceEntry.source), mono: true }]
                      : []),
                  ] satisfies PropertyListRow[]
                }
              />
            </div>
            <div className={CLASS.switchRows}>
              <Switch
                checked={entry.enabled}
                onChange={() => void handleToggleEnable(entry.enabled)}
                pending={mutationDisabled}
                label="Enabled by default"
                description="Used by new sessions with the default plugin selection. Custom launches and existing sessions keep their own selections."
              />
              <Switch
                checked={entry.autoUpgrade}
                onChange={() => void handleToggleAutoUpgrade(entry.autoUpgrade)}
                pending={mutationDisabled}
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
        busy={mutationDisabled}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(false)}
      >
        {target !== null ? `Remove plugin "${target.plugin}"?` : ""}
      </ConfirmDialog>
    </div>
  );
}
