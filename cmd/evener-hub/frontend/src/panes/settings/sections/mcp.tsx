// mcp.tsx is the "MCP servers" settings section (#15 - parity-m7-
// settings.md §15): a read-only "Discovered servers" probed block (T4's
// evener/settings/overview interface, injected - see McpSectionProps below)
// above two editable lists on the global launch layer - "MCP config files"
// (mcpConfigs: string[], reusing dirListSetting.tsx's PathListEditor) and
// "Inline MCP servers" (mcps: MCPServerSpec[], a CollectionEditor taking
// "name command [args...]" entries, the same syntax the launch-config form's
// McpServerListField uses, with no picker assistance, matching the legacy).
//
// Config files browse as kind="file", so the picker lists the JSON files
// themselves and clicking one fills the field - beyond parity, where the
// legacy gave the directory lists (plugins/skills) a picker and left config
// files typeahead-only, an asymmetry with no principled reason.

import type { MCPServerSpec, SettingsOverviewResponse } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { extensionsStoreForHost, useExtensionsStoreForHost } from "../../../stores/extensions";
import { isLocalHost, LOCAL_HOST } from "../../../stores/hostRouting";
import {
  Chip,
  type CollectionAddResult,
  CollectionEditor,
  ConfirmDialog,
  EmptyState,
  InlineNotice,
  ListRow,
  Skeleton,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { PathListEditor } from "./dirListSetting";
import { HostScopedSurface } from "./hostScopedSurface";
import {
  MCP_SERVER_ENTRY_USAGE,
  mcpServerKey,
  mcpServerLabel,
  parseMcpServerEntry,
} from "./launchShared/collectionFields";
import styles from "./mcp.module.css";
import { useHostScopedLoad } from "./useConnectedEffect";

const CLASS = {
  page: requireClass(styles.page, "mcp.module.css", "page"),
  title: requireClass(styles.title, "mcp.module.css", "title"),
  help: requireClass(styles.help, "mcp.module.css", "help"),
  section: requireClass(styles.section, "mcp.module.css", "section"),
  sectionTitle: requireClass(styles.sectionTitle, "mcp.module.css", "sectionTitle"),
  sectionHelp: requireClass(styles.sectionHelp, "mcp.module.css", "sectionHelp"),
  list: requireClass(styles.list, "mcp.module.css", "list"),
};

/**
 * The pinned interface T4's stores/settingsOverview.ts implements
 * (useSettingsOverviewStore()) - defined locally rather than imported,
 * since that store lands at merge time, after this task. Injected via
 * McpSectionProps so this component compiles and is fully testable today
 * against a local double; the controller wires the real hook in at merge
 * (see this task's report).
 */
export interface SettingsOverviewLike {
  data: SettingsOverviewResponse | null;
  loading: boolean;
  error: string | null;
  fetch(): Promise<void>;
}

export interface McpSectionProps {
  useOverviewStore: () => SettingsOverviewLike;
  /** The host whose own global launch layer this section edits (component
   * 07b). Defaults to the local hub, so a direct render is today's local
   * section. */
  host?: string;
}

export function McpSection({ useOverviewStore, host = LOCAL_HOST }: McpSectionProps) {
  // The extensions instance for the selected host: the controller's own store
  // for the local hub, a per-host instance (over evener/host/request) for a
  // remote one, so the global launch layer this section edits - and the path
  // helpers it validates an entry with - are that host's own. Same seam as
  // dirListSetting.tsx, which edits two other fields of this same layer.
  const store = extensionsStoreForHost(host);
  const overview = useOverviewStore();
  const layer = useExtensionsStoreForHost(host, (s) => s.launchLayer);
  const launchLayerError = useExtensionsStoreForHost(host, (s) => s.launchLayerError);
  const toasts = useToasts();

  const [pendingRemoveServerIndex, setPendingRemoveServerIndex] = useState<number | null>(null);
  const [removeServerBusy, setRemoveServerBusy] = useState(false);

  // evener/settings/overview is this hub's own read (stores/settingsOverview.ts
  // is built on the controller's client) and is NOT on the proxy allow-list, so
  // there is nothing honest to fetch or show for a remote host: the discovered
  // block below renders only for the local hub, and so does this fetch. The
  // injected hook's own fetch is a stable store action (every useXStore() here
  // defines its actions once in the store creator), so it fires on mount and
  // re-fires only when the selected host changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: host-keyed fetch against an injected store hook - see comment above.
  useEffect(() => {
    if (isLocalHost(host)) void overview.fetch();
  }, [host]);

  // useHostScopedLoad, not a bare mount-once effect: a direct deep link to
  // /settings/mcp can mount this section before AppShell's connect() handshake
  // finishes, and it re-runs when the selected host changes - and when it comes
  // BACK after being away - so this section shows THAT host's layer rather than
  // leaving this one's on screen (or its own failure standing).
  useHostScopedLoad(host, () => store.getState().fetchLaunchLayer(), [store]);

  // The layer on screen may be stale after a failed refresh, and stays stale
  // until a recovery refresh actually lands (fetchLaunchLayer clears the error
  // the moment it starts). The editors stay mounted so typing survives, but
  // nothing saves unless a layer is loaded, the last read succeeded, and no
  // read is in flight: a write from a stale snapshot could overwrite edits
  // made to the file elsewhere. Returns the layer to write from, or null.
  function writableLayer() {
    const { launchLayer, launchLayerError, launchLayerLoading } = store.getState();
    return launchLayer !== null && launchLayerError === null && !launchLayerLoading ? launchLayer : null;
  }

  function refusal(): string {
    const { launchLayerError } = store.getState();
    return launchLayerError !== null
      ? `Can't save while the launch settings fail to load: ${launchLayerError}`
      : "Can't save while the launch settings are loading. Try again in a moment.";
  }

  async function handleAddConfig(path: string): Promise<CollectionAddResult> {
    const validated = await store.getState().validatePath(path, "file");
    if (!validated.valid) return { ok: false, error: validated.error || "path does not exist" };
    const current = writableLayer();
    if (current === null) return { ok: false, error: refusal() };
    const canonical = validated.path || path;
    const nextList = [...(current.mcpConfigs ?? []), canonical];
    try {
      await store.getState().setLaunchLayer({ ...current, mcpConfigs: nextList });
      return { ok: true };
    } catch (err) {
      return { ok: false, error: friendlyErrorMessage(err) };
    }
  }

  async function handleRemoveConfig(path: string): Promise<void> {
    const current = writableLayer();
    if (current === null) {
      toasts.push("error", refusal());
      return;
    }
    const nextList = (current.mcpConfigs ?? []).filter((p) => p !== path);
    try {
      await store.getState().setLaunchLayer({ ...current, mcpConfigs: nextList });
    } catch (err) {
      toasts.push("error", `Remove failed: ${friendlyErrorMessage(err)}`);
    }
  }

  async function handleAddServer(value: string): Promise<CollectionAddResult> {
    const entry = parseMcpServerEntry(value);
    if (entry === null) return { ok: false, error: MCP_SERVER_ENTRY_USAGE };
    try {
      const validated = await store.getState().validatePath(entry.command, "command");
      if (!validated.valid) return { ok: false, error: validated.error || "command not found" };
      const current = writableLayer();
      if (current === null) return { ok: false, error: refusal() };
      const nextList = [...(current.mcps ?? []), entry];
      await store.getState().setLaunchLayer({ ...current, mcps: nextList });
      return { ok: true };
    } catch (err) {
      return { ok: false, error: friendlyErrorMessage(err) };
    }
  }

  async function handleConfirmRemoveServer() {
    const index = pendingRemoveServerIndex;
    if (index === null) return;
    setRemoveServerBusy(true);
    try {
      const current = writableLayer();
      if (current === null) throw new Error(refusal());
      const nextList = (current.mcps ?? []).filter((_, i) => i !== index);
      await store.getState().setLaunchLayer({ ...current, mcps: nextList });
    } catch (err) {
      toasts.push("error", `Remove failed: ${friendlyErrorMessage(err)}`);
    } finally {
      setRemoveServerBusy(false);
      setPendingRemoveServerIndex(null);
    }
  }

  const mcps = layer?.mcps ?? [];
  const pendingServer = pendingRemoveServerIndex !== null ? mcps[pendingRemoveServerIndex] : undefined;
  const discovered = overview.data?.mcpDiscovered;

  return (
    <section className={CLASS.page}>
      <h2 className={CLASS.title}>MCP servers</h2>
      <p className={CLASS.help}>MCP servers evener spawns alongside each session. Stored in the global launch layer.</p>

      {/* Reachability probed from THIS hub: settings/overview is not on the
          proxy allow-list, so it can only ever describe the controller, never a
          remote host. Under a remote selection the block is omitted rather than
          presenting this hub's probe as the selected host's own. */}
      {isLocalHost(host) && (
        <section className={CLASS.section} data-section="mcp-discovered">
          <h3 className={CLASS.sectionTitle}>Discovered servers</h3>
          <p className={CLASS.sectionHelp}>Reachability, as probed from the hub.</p>
          {discovered?.error !== undefined ? (
            <InlineNotice tone="danger">{discovered.error}</InlineNotice>
          ) : overview.loading && overview.data === null ? (
            <Skeleton />
          ) : (
            <ul aria-label="Discovered MCP servers" className={CLASS.list}>
              {(discovered?.servers ?? []).length === 0 ? (
                <li data-testid="mcp-discovered-empty">
                  <EmptyState size="compact" title="No MCP servers configured." />
                </li>
              ) : (
                (discovered?.servers ?? []).map((s) => (
                  <li key={s.name}>
                    {/* The name is the label alone, so a long one truncates on
                        its own (in full on hover); the transport is the meta. */}
                    <ListRow
                      label={s.name}
                      title={s.name}
                      chips={<Chip tone={s.status === "available" ? "alive" : "danger"}>{s.status}</Chip>}
                      meta={s.transport}
                    />
                    {/* A probe error wraps in full under its row: the row's meta
                        is one truncated line, and this read-only row has no
                        detail view to reveal the rest. */}
                    {s.error !== undefined && (
                      <InlineNotice tone="danger" live="off" data-reason="probe-failed">
                        {s.error}
                      </InlineNotice>
                    )}
                  </li>
                ))
              )}
            </ul>
          )}
        </section>
      )}

      {/* The editors need a loaded layer: adding to one that never loaded
          would save a layer holding only the new entry. Once loaded, a failed
          refresh keeps them mounted, so an entry the reader is typing survives
          the error and the recovery after it. */}
      {launchLayerError !== null && <InlineNotice tone="danger">{launchLayerError}</InlineNotice>}
      {layer === null ? (
        launchLayerError === null && <Skeleton />
      ) : (
        <>
          <section className={CLASS.section}>
            <h3 className={CLASS.sectionTitle}>MCP config files</h3>
            <PathListEditor
              directory={{
                validatePath: (path, kind) => store.getState().validatePath(path, kind),
                createDirectory: (path) => store.getState().createDirectory(path),
              }}
              label="MCP config files"
              addLabel="New config file"
              kind="file"
              items={layer?.mcpConfigs ?? []}
              onAdd={handleAddConfig}
              onRemove={handleRemoveConfig}
              complete={(prefix, includeFiles) => store.getState().completePaths(prefix, includeFiles)}
              emptyMessage="No MCP config files. Add one below."
              removeConfirmTitle="Remove config file"
              removeConfirmBody={(path) => `Remove "${path}" from MCP config files?`}
              addPlaceholder="/absolute/path/to/mcp.json"
            />
          </section>

          <section className={CLASS.section}>
            <h3 className={CLASS.sectionTitle}>Inline MCP servers</h3>
            {/* Removal is index-keyed (MCPServerSpec has no id of its own; the
                legacy spliced by index too), so the confirm resolves the row's
                own position rather than filtering every identical spec. */}
            <CollectionEditor<MCPServerSpec>
              label="Inline MCP servers"
              items={mcps}
              getKey={mcpServerKey}
              renderItem={mcpServerLabel}
              removeLabel={(server) => `Remove ${server.name}`}
              onRemove={(server) => setPendingRemoveServerIndex(mcps.indexOf(server))}
              emptyMessage="No inline MCP servers. Add one below."
              addPlaceholder="name command args..."
              onAdd={handleAddServer}
            />
          </section>
        </>
      )}

      <ConfirmDialog
        open={pendingRemoveServerIndex !== null}
        title="Remove MCP server"
        confirmLabel="Remove"
        busy={removeServerBusy}
        onConfirm={() => void handleConfirmRemoveServer()}
        onCancel={() => setPendingRemoveServerIndex(null)}
      >
        {pendingServer !== undefined ? `Remove "${pendingServer.name}"?` : ""}
      </ConfirmDialog>
    </section>
  );
}

/** McpSectionHostScope is the MCP settings section scoped to the settings
 * route's selected host (component 07b): the one shared HostPicker plus
 * McpSection. Its two editable lists (mcpConfigs, mcps) are fields of the
 * GLOBAL launch layer, which the launch-evener, plugins-dirs and skills-dirs
 * sections already host-scope - so without this the same layer would be a
 * different host's depending on which pane the user opened. */
export function McpSectionHostScope({ useOverviewStore }: McpSectionProps) {
  return (
    <HostScopedSurface>{(host) => <McpSection useOverviewStore={useOverviewStore} host={host} />}</HostScopedSurface>
  );
}
