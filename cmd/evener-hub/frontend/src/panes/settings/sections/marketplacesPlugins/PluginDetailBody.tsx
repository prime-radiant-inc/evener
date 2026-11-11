// PluginDetailBody: the mode-aware plugin detail that a row's expansion
// mounts inline under that row. The pair alone is the address; the mode is
// derived from the installed list - an entry makes it installed mode, none
// makes it catalog mode - so a plugin that installs or is removed while its
// details are open re-modes in place. The list owns the accordion: one row
// expanded at a time, and `onCollapse` is how a body that lost its entry
// (its own remove completing, or another client's) takes itself down.

import { errorText, marketplaceSourceLabel } from "@evener/appwire-client";
import { HUB_WRITE_BUSY, isHubWriteBusy, pluginComponentsKey } from "@evener/appwire-client/state/extensions";
import { useEffect, useRef, useState } from "react";
import { Button, ConfirmDialog, Switch, useFocusRehome, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { PluginContentsBlock } from "../../../plugins/PluginContents";
import { PluginDetail, type PluginMetadataRow } from "../../../plugins/PluginDetail";
import { pluginDetailBodyId } from "../../../plugins/pluginRowFocus";
import { pluginStatusChips } from "../../../plugins/pluginStatusChips";
import { useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  detail: requireClass(styles.detail, "marketplacesPlugins.module.css", "detail"),
  switchRows: requireClass(styles.switchRows, "marketplacesPlugins.module.css", "switchRows"),
  detailActions: requireClass(styles.detailActions, "marketplacesPlugins.module.css", "detailActions"),
};

/** The row whose details are expanded: the wire address of the plugin. */
export interface PluginDetailTarget {
  plugin: string;
  marketplace: string;
}

export interface PluginDetailBodyProps {
  target: PluginDetailTarget;
  onCollapse: () => void;
  /** Where the catalog-mode Install action routes: the list owner's single
   * install confirm, so the row's trailing Install and the details' Install
   * share one confirmation and one focus recovery. */
  onInstall?: (target: PluginDetailTarget) => void;
}

export function PluginDetailBody({ target, onCollapse, onInstall }: PluginDetailBodyProps) {
  const store = useExtensionsHostStore();
  const plugins = useExtensionsHostState((s) => s.plugins);
  const marketplaces = useExtensionsHostState((s) => s.marketplaces) ?? [];
  const browseCatalogs = useExtensionsHostState((s) => s.browseCatalogs);
  const pluginComponents = useExtensionsHostState((s) => s.pluginComponents);
  const hubWriteBusy = useExtensionsHostState((s) => s.hubWriteBusy);
  const pluginsUnavailable = useExtensionsHostState(
    (s) => s.plugins === null || s.pluginsError !== null || s.pluginsLoading,
  );
  const mutationDisabled = hubWriteBusy || pluginsUnavailable;
  const toasts = useToasts();

  const [pendingRemove, setPendingRemove] = useState(false);

  const entry =
    plugins === null
      ? undefined
      : plugins.find((p) => p.plugin === target.plugin && p.marketplace === target.marketplace);
  const mode = entry !== undefined ? "installed" : "catalog";
  // The mode this body opened in decides what vanishing means: a body that
  // opened installed-mode collapses when its entry disappears, while a
  // catalog body has no entry to lose - not being installed is what it
  // opened as - and re-modes in place when the list lands one.
  const openedInstalled = useRef(entry !== undefined);
  useEffect(() => {
    if (openedInstalled.current && plugins !== null && entry === undefined) onCollapse();
  }, [plugins, entry, onCollapse]);

  // Lazy catalog browse for the description. browseMarketplace no-ops when
  // the marketplace already has a cache entry (loaded, errored, or in
  // flight - see the store's own comment), so this fires once per
  // marketplace, not once per expansion.
  useEffect(() => {
    if (!store.getState().browseCatalogs.has(target.marketplace)) {
      void store.getState().browseMarketplace(target.marketplace);
    }
  }, [target, store]);

  // Lazy components read for the Contents block: installed mode only (a
  // catalog body shows no Contents - spec decision 2). fetchPluginComponents
  // no-ops for keys the cache already holds.
  useEffect(() => {
    if (mode === "installed") {
      void store.getState().fetchPluginComponents([target]);
    }
  }, [mode, target, store]);

  const componentsEntry = pluginComponents.get(pluginComponentsKey(target));

  // Any commit that takes away the control holding the keyboard - the
  // Upgrade button a successful upgrade unmounts, or the remove confirm's
  // buttons disabled while the remove runs - would drop focus to <body>; the
  // shared hook keeps it in the body (its first control), and leaves alone
  // focus the user had already moved.
  const focusRoot = useRef<HTMLDivElement>(null);
  useFocusRehome(focusRoot);

  function handleRetryComponents(): void {
    // The reload is the store's own drop-and-reread: fetchPluginComponents
    // never re-requests a key the cache already holds - a settled error
    // entry included - so only the store dropping the entry makes a fresh
    // read go out.
    void store.getState().reloadPluginComponents([target]);
  }

  async function handleToggleEnable(currentlyEnabled: boolean): Promise<void> {
    if (mutationDisabled) return;
    try {
      if (currentlyEnabled) await store.getState().disablePlugin(target.plugin, target.marketplace);
      else await store.getState().enablePlugin(target.plugin, target.marketplace);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Toggle enable failed: ${errorText(err)}`);
    }
  }

  async function handleToggleAutoUpgrade(currentlyAutoUpgrade: boolean): Promise<void> {
    if (mutationDisabled) return;
    try {
      await store.getState().setPluginAutoUpgrade(target.plugin, target.marketplace, !currentlyAutoUpgrade);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Toggle auto-upgrade failed: ${errorText(err)}`);
    }
  }

  async function handleUpgrade(): Promise<void> {
    if (mutationDisabled) return;
    try {
      await store.getState().upgradePlugin(target.plugin, target.marketplace);
      toasts.push("success", `Upgraded ${target.plugin}`);
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Upgrade failed: ${errorText(err)}`);
    }
  }

  async function handleConfirmRemove(): Promise<void> {
    if (mutationDisabled) return;
    try {
      await store.getState().removePlugin(target.plugin, target.marketplace);
      toasts.push("success", `Removed ${target.plugin}`);
      setPendingRemove(false);
      // onCollapse fires via the entry-vanished effect above once the
      // store's updated plugin list lands - no explicit collapse here.
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Remove failed: ${errorText(err)}`);
    }
  }

  const chips = mode === "installed" && entry !== undefined ? pluginStatusChips(entry) : [];

  const cache = browseCatalogs.get(target.marketplace);
  const catalogEntry = cache?.status === "loaded" ? cache.plugins.find((p) => p.name === target.plugin) : undefined;
  const description = catalogEntry?.description;
  const marketplaceEntry = marketplaces.find((m) => m.name === target.marketplace);

  const metaRows: PluginMetadataRow[] = [
    ...(mode === "installed" && entry !== undefined
      ? [{ kind: "version" as const, label: "Version", value: `v${entry.version || "unknown"}`, mono: true }]
      : []),
    { kind: "marketplace", label: "Marketplace", value: target.marketplace },
    ...(marketplaceEntry !== undefined
      ? [
          {
            kind: "source" as const,
            label: "Source",
            value: marketplaceSourceLabel(marketplaceEntry.source),
            mono: true,
          },
        ]
      : []),
    // Catalog metadata carries the author the browse catalog knows; an
    // installed entry's metadata stays today's rows.
    ...(mode === "catalog" && catalogEntry?.author !== undefined
      ? [{ kind: "author" as const, label: "Author", value: catalogEntry.author }]
      : []),
  ];

  // Where the Contents block goes, between the description and the metadata.
  // Only a settled inventory renders - the block appears when the answer
  // lands, and a broken plugin's load failure or an unreadable inventory
  // takes its place, never "Manifest only" (that reading is reserved for a
  // loadable plugin that ships nothing).
  const contentsBlock =
    mode === "installed" ? <PluginContentsBlock entry={componentsEntry} onRetry={handleRetryComponents} /> : null;

  return (
    <div ref={focusRoot} className={CLASS.detail}>
      <PluginDetail
        id={pluginDetailBodyId(target.plugin, target.marketplace)}
        mode={mode}
        description={description}
        chips={chips.length > 0 ? chips : undefined}
        contents={contentsBlock}
        metadata={metaRows}
        onCollapse={onCollapse}
      >
        {mode === "installed" && entry !== undefined ? (
          <>
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
            <div className={CLASS.detailActions}>
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
            </div>
          </>
        ) : (
          <Button variant="primary" disabled={mutationDisabled} onClick={() => onInstall?.(target)}>
            Install
          </Button>
        )}
      </PluginDetail>
      <ConfirmDialog
        open={pendingRemove}
        title="Remove plugin"
        confirmLabel="Remove"
        busy={mutationDisabled}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(false)}
      >
        {`Remove plugin "${target.plugin}"?`}
      </ConfirmDialog>
    </div>
  );
}
