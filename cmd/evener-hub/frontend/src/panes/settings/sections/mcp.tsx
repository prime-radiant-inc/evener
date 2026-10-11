// mcp.tsx is the "MCP servers" settings section (#15 - parity-m7-
// settings.md §15): a read-only "Discovered servers" probed block (T4's
// evener/settings/overview interface, injected - see McpSectionProps below)
// above two editable lists on the global launch layer - "MCP config files"
// (mcpConfigs: string[], reusing dirListSetting.tsx's PathListEditor) and
// "Inline MCP servers" (mcps: MCPServerSpec[], hand-rolled: a 3-field name/
// command/args row the path-list PathListEditor genuinely can't represent,
// matching the legacy's own "no picker assistance" for this list).
//
// Config files browse as kind="file", so the picker lists the JSON files
// themselves and clicking one fills the field - beyond parity, where the
// legacy gave the directory lists (plugins/skills) a picker and left config
// files typeahead-only, an asymmetry with no principled reason.

import type { MCPServerSpec, SettingsOverviewResponse } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { type FormEvent, useEffect, useId, useState } from "react";
import { extensionsStoreForHost, useExtensionsStoreForHost } from "../../../stores/extensions";
import { isLocalHost, LOCAL_HOST } from "../../../stores/hostRouting";
import {
  Button,
  Chip,
  type CollectionAddResult,
  ConfirmDialog,
  EmptyState,
  FormRow,
  IconButton,
  Input,
  Skeleton,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { PathListEditor } from "./dirListSetting";
import { HostScopedSurface } from "./hostScopedSurface";
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
  row: requireClass(styles.row, "mcp.module.css", "row"),
  rowText: requireClass(styles.rowText, "mcp.module.css", "rowText"),
  rowMeta: requireClass(styles.rowMeta, "mcp.module.css", "rowMeta"),
  empty: requireClass(styles.empty, "mcp.module.css", "empty"),
  addRow: requireClass(styles.addRow, "mcp.module.css", "addRow"),
  addField: requireClass(styles.addField, "mcp.module.css", "addField"),
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

function inlineServerLabel(server: MCPServerSpec): string {
  const parts = [server.command, ...(server.args ?? [])].filter(Boolean);
  return `${server.name} → ${parts.join(" ")}`;
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
  const launchLayerLoading = useExtensionsStoreForHost(host, (s) => s.launchLayerLoading);
  const launchLayerError = useExtensionsStoreForHost(host, (s) => s.launchLayerError);
  const toasts = useToasts();

  const [serverName, setServerName] = useState("");
  const [serverCommand, setServerCommand] = useState("");
  const [serverArgs, setServerArgs] = useState("");
  const [serverAddError, setServerAddError] = useState<string | null>(null);
  const [serverAddBusy, setServerAddBusy] = useState(false);
  const [pendingRemoveServerIndex, setPendingRemoveServerIndex] = useState<number | null>(null);
  const [removeServerBusy, setRemoveServerBusy] = useState(false);
  const serverCommandId = useId();

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

  async function handleAddConfig(path: string): Promise<CollectionAddResult> {
    const validated = await store.getState().validatePath(path, "file");
    if (!validated.valid) return { ok: false, error: validated.error || "path does not exist" };
    const current = store.getState().launchLayer ?? {};
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
    const current = store.getState().launchLayer ?? {};
    const nextList = (current.mcpConfigs ?? []).filter((p) => p !== path);
    try {
      await store.getState().setLaunchLayer({ ...current, mcpConfigs: nextList });
    } catch (err) {
      toasts.push("error", `Remove failed: ${friendlyErrorMessage(err)}`);
    }
  }

  async function handleAddServer(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const name = serverName.trim();
    const command = serverCommand.trim();
    if (name === "" || command === "" || serverAddBusy) return;
    const args = serverArgs
      .trim()
      .split(/\s+/)
      .filter((a) => a !== "");
    setServerAddBusy(true);
    try {
      const validated = await store.getState().validatePath(command, "command");
      if (!validated.valid) {
        setServerAddError(validated.error || "command not found");
        return;
      }
      const current = store.getState().launchLayer ?? {};
      const nextList = [...(current.mcps ?? []), { name, command, args }];
      await store.getState().setLaunchLayer({ ...current, mcps: nextList });
      setServerAddError(null);
      setServerName("");
      setServerCommand("");
      setServerArgs("");
    } catch (err) {
      setServerAddError(friendlyErrorMessage(err));
    } finally {
      setServerAddBusy(false);
    }
  }

  async function handleConfirmRemoveServer() {
    const index = pendingRemoveServerIndex;
    if (index === null) return;
    setRemoveServerBusy(true);
    const current = store.getState().launchLayer ?? {};
    const nextList = (current.mcps ?? []).filter((_, i) => i !== index);
    try {
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
        <section className={CLASS.section}>
          <h3 className={CLASS.sectionTitle}>Discovered servers</h3>
          <p className={CLASS.sectionHelp}>Reachability, as probed from the hub.</p>
          {discovered?.error !== undefined ? (
            <EmptyState title="Failed to load" hint={discovered.error} />
          ) : overview.loading && overview.data === null ? (
            <Skeleton />
          ) : (
            <ul aria-label="Discovered MCP servers" className={CLASS.list}>
              {(discovered?.servers ?? []).length === 0 ? (
                <li className={CLASS.empty}>No MCP servers configured.</li>
              ) : (
                (discovered?.servers ?? []).map((s) => (
                  <li key={s.name} className={CLASS.row}>
                    <div>
                      <div className={CLASS.rowText}>
                        {s.name} — {s.transport}{" "}
                        <Chip tone={s.status === "available" ? "alive" : "danger"}>{s.status}</Chip>
                      </div>
                      {s.error !== undefined && <div className={CLASS.rowMeta}>{s.error}</div>}
                    </div>
                  </li>
                ))
              )}
            </ul>
          )}
        </section>
      )}

      {launchLayerError !== null ? (
        <EmptyState title="Failed to load" hint={launchLayerError} />
      ) : launchLayerLoading && layer === null ? (
        <Skeleton />
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
            <ul aria-label="Inline MCP servers" className={CLASS.list}>
              {mcps.length === 0 ? (
                <li className={CLASS.empty}>No inline MCP servers. Add one below.</li>
              ) : (
                mcps.map((m, index) => (
                  // biome-ignore lint/suspicious/noArrayIndexKey: MCPServerSpec has no stable id of its own; removal is index-keyed too (mirrors the legacy's own mcps.splice(i,1)), so index is the correct identity here, not just a fallback.
                  <li key={index} className={CLASS.row}>
                    <div className={CLASS.rowText}>{inlineServerLabel(m)}</div>
                    <IconButton
                      label={`Remove ${m.name}`}
                      icon={<RemoveIcon />}
                      variant="quiet"
                      size="sm"
                      onClick={() => setPendingRemoveServerIndex(index)}
                    />
                  </li>
                ))
              )}
            </ul>
            {/* FormRow (not a hand-rolled error paragraph) for the same
                reason dirListSetting.tsx's PathListEditor uses it - see
                that file's own comment. htmlFor targets the command field
                specifically since that's what evener/path/validate actually
                validates; name/args get placeholder-only hints, matching
                the legacy's own zero-real-labels baseline for this form
                (not a regression this task introduces). */}
            <FormRow label="New MCP server" htmlFor={serverCommandId} error={serverAddError ?? undefined}>
              <form className={CLASS.addRow} onSubmit={(event) => void handleAddServer(event)}>
                <span className={CLASS.addField}>
                  <Input
                    value={serverName}
                    onChange={(event) => setServerName(event.target.value)}
                    placeholder="name"
                  />
                </span>
                <span className={CLASS.addField}>
                  <Input
                    id={serverCommandId}
                    value={serverCommand}
                    onChange={(event) => setServerCommand(event.target.value)}
                    placeholder="command"
                  />
                </span>
                <span className={CLASS.addField}>
                  <Input
                    value={serverArgs}
                    onChange={(event) => setServerArgs(event.target.value)}
                    placeholder="args (space-separated)"
                  />
                </span>
                <Button
                  type="submit"
                  disabled={serverName.trim() === "" || serverCommand.trim() === "" || serverAddBusy}
                >
                  Add
                </Button>
              </form>
            </FormRow>
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

function RemoveIcon() {
  return (
    <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
      <path d="M2 2 L10 10 M10 2 L2 10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
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
