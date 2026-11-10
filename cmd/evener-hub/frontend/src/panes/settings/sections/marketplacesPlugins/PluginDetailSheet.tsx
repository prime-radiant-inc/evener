// PluginDetailSheet: the plugin's own editor for the Segmented Workspace
// redesign. Opens from an InstalledSection row tap (installed mode) or a
// BrowseSection catalog row tap (catalog mode); owns every per-plugin
// ACTION (enable/disable, auto-upgrade, upgrade, remove; install in catalog
// mode) so the list rows stay single-target. A right side Sheet on desktop,
// a bottom Sheet on mobile (same content, geometry follows useIsMobile).
// The catalog description is lazily pulled from the browse cache
// (browseMarketplace no-ops when the marketplace is already cached, so
// re-opens are free); installed mode's Contents block is lazily pulled from
// the components cache the same way (fetchPluginComponents no-ops for keys
// the cache already holds).

import { errorText, marketplaceSourceLabel } from "@evener/appwire-client";
import { HUB_WRITE_BUSY, isHubWriteBusy, pluginComponentsKey } from "@evener/appwire-client/state/extensions";
import { useEffect, useRef, useState } from "react";
import { Button, ConfirmDialog, Switch, useFocusRehome, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { PluginContentsBlock } from "../../../plugins/PluginContents";
import { type PluginMetadataRow, PluginSheet } from "../../../plugins/PluginSheet";
import { pluginStatusChips } from "../../../plugins/pluginStatusChips";
import { useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  focusRoot: requireClass(styles.focusRoot, "marketplacesPlugins.module.css", "focusRoot"),
};

/** What the sheet is editing, decided by whoever opened it: the installed
 * entry's editor, or the not-yet-installed catalog plugin's sheet. The
 * plugin/marketplace pair is the wire address in both modes; `mode` is a
 * UI-only discriminator, so the pair alone reaches every RPC. */
export type PluginDetailTarget =
  | { mode: "installed"; plugin: string; marketplace: string }
  | { mode: "catalog"; plugin: string; marketplace: string };

export interface PluginDetailSheetProps {
  target: PluginDetailTarget | null;
  onClose: () => void;
}

export function PluginDetailSheet({ target, onClose }: PluginDetailSheetProps) {
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
  const [pendingInstall, setPendingInstall] = useState(false);
  // Any commit that takes away the control holding the keyboard - the
  // Upgrade button a successful upgrade unmounts, or the remove confirm's
  // buttons disabled while the remove runs - would drop focus to <body>; the
  // shared hook keeps it in the sheet or confirm (their first control), and
  // leaves alone focus the user had already moved.
  const focusRoot = useRef<HTMLDivElement>(null);
  useFocusRehome(focusRoot);

  const pair = target === null ? undefined : { plugin: target.plugin, marketplace: target.marketplace };

  const entry =
    pair === undefined || plugins === null
      ? undefined
      : plugins.find((p) => p.plugin === pair.plugin && p.marketplace === pair.marketplace);
  const chips = target !== null && target.mode === "installed" && entry !== undefined ? pluginStatusChips(entry) : [];

  // The entry can vanish under an open sheet - a remove completing (this
  // sheet's own or another client's), or a refetch after external change.
  // An editor for a thing that no longer exists closes itself rather than
  // rendering stale actions. A catalog sheet has no entry to vanish - its
  // plugin not being installed is what opened it - so it stays open.
  useEffect(() => {
    if (target !== null && target.mode === "installed" && plugins !== null && entry === undefined) onClose();
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

  // Lazy components read for the Contents block: installed mode only (a
  // catalog sheet shows no Contents - spec decision 2). fetchPluginComponents
  // no-ops for keys the cache already holds - loading keys are awaited, and
  // settled keys send nothing until the store retires their inventories.
  useEffect(() => {
    if (target === null || target.mode !== "installed") return;
    void store.getState().fetchPluginComponents([{ plugin: target.plugin, marketplace: target.marketplace }]);
  }, [target, store]);

  const componentsEntry = pair === undefined ? undefined : pluginComponents.get(pluginComponentsKey(pair));

  function handleRetryComponents(): void {
    if (pair === undefined) return;
    // The reload is the store's own drop-and-reread: fetchPluginComponents
    // never re-requests a key the cache already holds - a settled error
    // entry included - so only the store dropping the entry makes a fresh
    // read go out.
    void store.getState().reloadPluginComponents([pair]);
  }

  async function handleConfirmInstall(): Promise<void> {
    if (pair === undefined || pluginsUnavailable) return;
    // Installed since the sheet opened - here or from another client: the
    // confirm's work is already done, and a second install call is not.
    if (entry !== undefined) {
      setPendingInstall(false);
      return;
    }
    try {
      await store.getState().installPlugin(pair.plugin, pair.marketplace);
      toasts.push("success", `Installed ${pair.plugin}`);
      setPendingInstall(false);
      // The catalog sheet's whole purpose is fulfilled; the browse list it
      // returns to now shows the installed row.
      onClose();
    } catch (err) {
      toasts.push("error", isHubWriteBusy(err) ? HUB_WRITE_BUSY : `Install failed: ${errorText(err)}`);
    }
  }

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

  const open = target !== null && (target.mode === "catalog" || entry !== undefined);

  const cache = pair === undefined ? undefined : browseCatalogs.get(pair.marketplace);
  const catalogEntry =
    pair === undefined || cache?.status !== "loaded" ? undefined : cache.plugins.find((p) => p.name === pair.plugin);
  const description = catalogEntry?.description;
  const marketplaceEntry = pair === undefined ? undefined : marketplaces.find((m) => m.name === pair.marketplace);

  const metaRows: PluginMetadataRow[] = [
    ...(target !== null && target.mode === "installed" && entry !== undefined
      ? [{ kind: "version" as const, label: "Version", value: `v${entry.version || "unknown"}`, mono: true }]
      : []),
    { kind: "marketplace", label: "Marketplace", value: pair?.marketplace ?? "" },
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
    ...(target?.mode === "catalog" && catalogEntry?.author !== undefined
      ? [{ kind: "author" as const, label: "Author", value: catalogEntry.author }]
      : []),
  ];

  // Where the Contents block goes, between the description and the metadata.
  // Only a settled inventory renders - the block appears when the answer
  // lands, and a broken plugin's load failure or an unreadable inventory
  // takes its place, never "Manifest only" (that reading is reserved for a
  // loadable plugin that ships nothing).
  const contentsBlock =
    target?.mode === "installed" ? (
      <PluginContentsBlock entry={componentsEntry} onRetry={handleRetryComponents} />
    ) : null;

  return (
    <div ref={focusRoot} className={CLASS.focusRoot}>
      <PluginSheet
        open={open}
        onClose={onClose}
        title={pair?.plugin ?? ""}
        mode={target?.mode ?? "installed"}
        description={description}
        chips={chips.length > 0 ? chips : undefined}
        contents={contentsBlock}
        metadata={metaRows}
        footer={
          target !== null &&
          (target.mode === "catalog" ? (
            <Button variant="primary" disabled={mutationDisabled} onClick={() => setPendingInstall(true)}>
              Install
            </Button>
          ) : (
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
          ))
        }
      >
        {target?.mode === "installed" && entry !== undefined && (
          <>
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
          </>
        )}
      </PluginSheet>
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
      <ConfirmDialog
        open={pendingInstall && target !== null && target.mode === "catalog"}
        title="Install plugin"
        confirmLabel="Install"
        destructive={false}
        busy={mutationDisabled}
        onConfirm={() => void handleConfirmInstall()}
        onCancel={() => setPendingInstall(false)}
      >
        {target !== null
          ? `Install "${target.plugin}" from ${target.marketplace}? This makes it available on this host.`
          : ""}
      </ConfirmDialog>
    </div>
  );
}
