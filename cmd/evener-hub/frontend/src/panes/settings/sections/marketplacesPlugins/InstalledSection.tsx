// InstalledSection: the installed-plugins list (parity-m7-settings.md
// §12e) - enable/disable, auto-upgrade toggle, upgrade, and a
// ConfirmDialog-gated remove (this wave's binding "every destructive
// action confirms" constraint; the legacy had none for plugin removal
// either - `confirm(...)` there, ConfirmDialog here).
import { type Dispatch, type SetStateAction, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { PluginEntry } from "../../../../protocol/types.gen";
import { extensionsStore, useExtensionsStore } from "../../../../stores/extensions";
import { Button, type CadenceState, Chip, ConfirmDialog, StatusDot, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  header: requireClass(styles.header, "marketplacesPlugins.module.css", "header"),
  title: requireClass(styles.title, "marketplacesPlugins.module.css", "title"),
  count: requireClass(styles.count, "marketplacesPlugins.module.css", "count"),
  list: requireClass(styles.list, "marketplacesPlugins.module.css", "list"),
  row: requireClass(styles.row, "marketplacesPlugins.module.css", "row"),
  rowMain: requireClass(styles.rowMain, "marketplacesPlugins.module.css", "rowMain"),
  rowText: requireClass(styles.rowText, "marketplacesPlugins.module.css", "rowText"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
  rowActions: requireClass(styles.rowActions, "marketplacesPlugins.module.css", "rowActions"),
  empty: requireClass(styles.empty, "marketplacesPlugins.module.css", "empty"),
};

function pluginKey(plugin: string, marketplace: string): string {
  return `${plugin}@${marketplace}`;
}

// §12e: "Status dot: warning if broken, else ended if !enabled, else idle".
// StatusDot (widgets/statusdot) is built on the shared CadenceState enum,
// which has no "warning" state - broken maps onto "failed" (the only
// failure-family state, giving the danger/red treatment a broken plugin
// warrants) rather than inventing a state the shared widget doesn't have.
function pluginStatus(plugin: PluginEntry): CadenceState {
  if (plugin.broken) return "failed";
  if (!plugin.enabled) return "ended";
  return "idle";
}

// Runs fn() with `key` added to the given busy-key Set for its duration,
// always removing it again in a finally - the "withBusy" shape (§12f:
// "disables the triggering button... re-enabling in a finally") for a
// per-row action, without a shared boolean that would also disable every
// OTHER row's (or this row's OTHER action's) button.
async function runBusy(setBusy: Dispatch<SetStateAction<Set<string>>>, key: string, fn: () => Promise<void>) {
  setBusy((prev) => new Set(prev).add(key));
  try {
    await fn();
  } finally {
    setBusy((prev) => {
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
  }
}

export function InstalledSection() {
  const plugins = useExtensionsStore((s) => s.plugins) ?? [];
  const toasts = useToasts();
  const [pendingRemove, setPendingRemove] = useState<{ plugin: string; marketplace: string } | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [toggleBusy, setToggleBusy] = useState<Set<string>>(new Set());
  const [autoUpgradeBusy, setAutoUpgradeBusy] = useState<Set<string>>(new Set());
  const [upgradeBusy, setUpgradeBusy] = useState<Set<string>>(new Set());

  async function handleToggleEnable(plugin: string, marketplace: string, currentlyEnabled: boolean) {
    await runBusy(setToggleBusy, pluginKey(plugin, marketplace), async () => {
      try {
        if (currentlyEnabled) await extensionsStore.getState().disablePlugin(plugin, marketplace);
        else await extensionsStore.getState().enablePlugin(plugin, marketplace);
      } catch (err) {
        toasts.push("error", `Toggle enable failed: ${errorText(err)}`);
      }
    });
  }

  async function handleToggleAutoUpgrade(plugin: string, marketplace: string, currentlyAutoUpgrade: boolean) {
    await runBusy(setAutoUpgradeBusy, pluginKey(plugin, marketplace), async () => {
      try {
        await extensionsStore.getState().setPluginAutoUpgrade(plugin, marketplace, !currentlyAutoUpgrade);
      } catch (err) {
        toasts.push("error", `Toggle auto-upgrade failed: ${errorText(err)}`);
      }
    });
  }

  async function handleUpgrade(plugin: string, marketplace: string) {
    await runBusy(setUpgradeBusy, pluginKey(plugin, marketplace), async () => {
      try {
        await extensionsStore.getState().upgradePlugin(plugin, marketplace);
        toasts.push("success", `Checked ${plugin} for upgrades`);
      } catch (err) {
        toasts.push("error", `Upgrade failed: ${errorText(err)}`);
      }
    });
  }

  async function handleConfirmRemove() {
    const target = pendingRemove;
    if (target === null) return;
    setRemoveBusy(true);
    try {
      await extensionsStore.getState().removePlugin(target.plugin, target.marketplace);
      toasts.push("success", `Removed ${target.plugin}`);
      setPendingRemove(null);
    } catch (err) {
      toasts.push("error", `Remove failed: ${errorText(err)}`);
    } finally {
      setRemoveBusy(false);
    }
  }

  return (
    <section className={CLASS.section}>
      <header className={CLASS.header}>
        <h3 className={CLASS.title}>Installed</h3>
        <span className={CLASS.count}>
          {plugins.length} {plugins.length === 1 ? "entry" : "entries"}
        </span>
      </header>
      <ul aria-label="Installed plugins" className={CLASS.list}>
        {plugins.length === 0 ? (
          <li className={CLASS.empty}>No plugins installed yet. Install one from Browse above.</li>
        ) : (
          plugins.map((p) => (
            <li key={`${p.plugin}@${p.marketplace}`} className={CLASS.row}>
              <div className={CLASS.rowMain}>
                <div className={CLASS.rowText}>
                  <StatusDot state={pluginStatus(p)} />
                  {p.plugin} <span className={CLASS.rowMeta}>@ {p.marketplace}</span>
                  {p.broken && <Chip tone="danger">broken</Chip>}
                  {!p.enabled && <Chip tone="neutral">disabled</Chip>}
                  {p.autoUpgrade && <Chip tone="neutral">auto-upgrade</Chip>}
                </div>
                <div className={CLASS.rowMeta}>v{p.version || "unknown"}</div>
              </div>
              <div className={CLASS.rowActions}>
                <Button
                  variant="quiet"
                  size="sm"
                  onClick={() => void handleToggleEnable(p.plugin, p.marketplace, p.enabled)}
                  disabled={toggleBusy.has(pluginKey(p.plugin, p.marketplace))}
                >
                  {p.enabled ? "Disable" : "Enable"}
                </Button>
                <Button
                  variant="quiet"
                  size="sm"
                  onClick={() => void handleToggleAutoUpgrade(p.plugin, p.marketplace, p.autoUpgrade)}
                  disabled={autoUpgradeBusy.has(pluginKey(p.plugin, p.marketplace))}
                >
                  {p.autoUpgrade ? "Auto-upgrade: on" : "Auto-upgrade: off"}
                </Button>
                <Button
                  variant="quiet"
                  size="sm"
                  onClick={() => void handleUpgrade(p.plugin, p.marketplace)}
                  disabled={upgradeBusy.has(pluginKey(p.plugin, p.marketplace))}
                >
                  Upgrade
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  onClick={() => setPendingRemove({ plugin: p.plugin, marketplace: p.marketplace })}
                >
                  Remove
                </Button>
              </div>
            </li>
          ))
        )}
      </ul>
      <ConfirmDialog
        open={pendingRemove !== null}
        title="Remove plugin"
        confirmLabel="Remove"
        busy={removeBusy}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(null)}
      >
        {pendingRemove !== null ? `Remove plugin "${pendingRemove.plugin}"?` : ""}
      </ConfirmDialog>
    </section>
  );
}
