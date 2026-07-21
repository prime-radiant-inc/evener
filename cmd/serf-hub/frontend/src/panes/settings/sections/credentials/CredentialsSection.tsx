// CredentialsSection (#7 - the dominant piece of the Agents & models
// cluster): instance list/CRUD, API-key set, OAuth browser+device dual
// flow, default-instance switch, remove - parity-m7-settings.md §7.
//
// Single-mutable-editor invariant: `openEditor` is ONE section-level state
// value (a discriminated union), so opening a second editor always replaces
// whatever was open, matching the legacy's own single module-level
// `openEditor` variable - no per-row state, no dirty-check on replace.
import { useCallback, useState } from "react";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
import { Button, ConfirmDialog, EmptyState, Skeleton, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useConnectedEffect } from "../useConnectedEffect";
import styles from "./CredentialsSection.module.css";
import { groupByType } from "./credentialLabels";
import { InstanceRow } from "./InstanceRow";
import { AddInstanceDialog, ApiKeyDialog, EditInstanceDialog } from "./instanceDialogs";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";

const CLASS = {
  root: requireClass(styles.root, "CredentialsSection.module.css", "root"),
  headerRow: requireClass(styles.headerRow, "CredentialsSection.module.css", "headerRow"),
  help: requireClass(styles.help, "CredentialsSection.module.css", "help"),
  error: requireClass(styles.error, "CredentialsSection.module.css", "error"),
  groups: requireClass(styles.groups, "CredentialsSection.module.css", "groups"),
  group: requireClass(styles.group, "CredentialsSection.module.css", "group"),
  groupHeader: requireClass(styles.groupHeader, "CredentialsSection.module.css", "groupHeader"),
  list: requireClass(styles.list, "CredentialsSection.module.css", "list"),
};

type OpenEditor =
  | { kind: "add" }
  | { kind: "apiKey"; name: string }
  | { kind: "edit"; name: string }
  | { kind: "oauth-redirect"; name: string; flowId: string; authUrl: string }
  | { kind: "device"; name: string; flowId: string; userCode: string; verificationUrl: string; intervalSeconds: number }
  | null;

type PendingConfirm = { kind: "clear" | "remove"; name: string } | null;

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export interface CredentialsSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

export function CredentialsSection(_props: CredentialsSectionProps) {
  const { instances, availableTypes, loading, error, fetch } = useCredentialsStore();
  const [openEditor, setOpenEditor] = useState<OpenEditor>(null);
  const [pendingConfirm, setPendingConfirm] = useState<PendingConfirm>(null);
  const [confirmBusy, setConfirmBusy] = useState(false);
  const toast = useToasts();

  // useConnectedEffect (not a bare useEffect): a direct deep link to
  // /credentials can mount this section before AppShell's own connect()
  // handshake finishes, and credentialsStore.fetch() requires a connected
  // client (throws otherwise) - see that hook's own doc comment.
  useConnectedEffect(fetch, [fetch]);

  // handleOAuthStart is shared by a row's own "Sign in…"/"Refresh OAuth"
  // button and the device editor's "Start again" - always begins with
  // authDeviceStart, then branches on `fallback` exactly like the legacy's
  // startDeviceLogin (templates/partials/credentials.html:58-75).
  async function handleOAuthStart(name: string): Promise<void> {
    try {
      const resp = await credentialsStore.getState().deviceStart(name);
      if (resp.fallback) {
        const login = await credentialsStore.getState().loginStart(name);
        window.open(login.url, "_blank", "noopener");
        setOpenEditor({ kind: "oauth-redirect", name, flowId: login.flowId, authUrl: login.url });
      } else {
        setOpenEditor({
          kind: "device",
          name,
          flowId: resp.flowId,
          userCode: resp.userCode,
          verificationUrl: resp.verificationUrl,
          intervalSeconds: resp.intervalSeconds,
        });
      }
    } catch (err) {
      toast.push("error", `Sign-in failed: ${errorMessage(err)}`);
    }
  }

  // "★ make default" has no confirm and no success toast (silent success -
  // only a failure toast exists), matching the legacy exactly.
  async function handleSetDefault(name: string): Promise<void> {
    try {
      await credentialsStore.getState().setDefault(name);
    } catch (err) {
      toast.push("error", `Set default failed: ${errorMessage(err)}`);
    }
  }

  async function handleConfirmedAction(): Promise<void> {
    if (!pendingConfirm) return;
    const { kind, name } = pendingConfirm;
    setConfirmBusy(true);
    try {
      if (kind === "clear") {
        await credentialsStore.getState().logout(name);
        await credentialsStore.getState().fetch();
        toast.push("success", `Credentials cleared for ${name}`);
      } else {
        await credentialsStore.getState().remove(name);
        toast.push("success", `Removed instance ${name}`);
      }
      setPendingConfirm(null);
    } catch (err) {
      const verb = kind === "clear" ? "Clear" : "Remove";
      toast.push("error", `${verb} failed: ${errorMessage(err)}`);
    } finally {
      setConfirmBusy(false);
    }
  }

  function findInstance(name: string): InstanceEntry | undefined {
    return instances.find((i) => i.name === name);
  }

  const groups = groupByType(instances);
  // useCallback'd (not a plain inline arrow) so its identity stays stable
  // across CredentialsSection re-renders - DeviceCodeDialog's own poll
  // effect depends on the onSuccess it's given, and an unstable reference
  // here would restart that dialog's poll timer on every unrelated parent
  // re-render (see oauthDialogs.tsx's own comment on that effect).
  const closeEditor = useCallback(() => setOpenEditor(null), []);

  return (
    <div className={CLASS.root}>
      <div className={CLASS.headerRow}>
        <p className={CLASS.help}>
          Provider instances and their credentials. Keys are stored in <code>~/.serf/credentials.toml</code> (chmod
          600). Env vars in the hub process take precedence only when no file entry exists. The UI never displays stored
          values.
        </p>
        <Button onClick={() => setOpenEditor({ kind: "add" })}>+ Add provider instance</Button>
      </div>

      {loading && <Skeleton />}
      {error && <p className={CLASS.error}>Failed to load: {error}</p>}
      {!loading &&
        !error &&
        (instances.length === 0 ? (
          <EmptyState title="No provider instances configured." />
        ) : (
          <div className={CLASS.groups}>
            {groups.map((group) => (
              <div key={group.type} className={CLASS.group}>
                <div className={CLASS.groupHeader}>{group.type}</div>
                <ul className={CLASS.list}>
                  {group.instances.map((instance) => (
                    <InstanceRow
                      key={instance.name}
                      instance={instance}
                      onSetApiKey={() => setOpenEditor({ kind: "apiKey", name: instance.name })}
                      onOAuthStart={() => void handleOAuthStart(instance.name)}
                      onEdit={() => setOpenEditor({ kind: "edit", name: instance.name })}
                      onClear={() => setPendingConfirm({ kind: "clear", name: instance.name })}
                      onRemove={() => setPendingConfirm({ kind: "remove", name: instance.name })}
                      onSetDefault={() => void handleSetDefault(instance.name)}
                    />
                  ))}
                </ul>
              </div>
            ))}
          </div>
        ))}

      {openEditor?.kind === "add" && (
        <AddInstanceDialog availableTypes={availableTypes} onCancel={closeEditor} onSuccess={closeEditor} />
      )}
      {openEditor?.kind === "apiKey" &&
        (() => {
          const target = findInstance(openEditor.name);
          return target ? <ApiKeyDialog instance={target} onCancel={closeEditor} onSuccess={closeEditor} /> : null;
        })()}
      {openEditor?.kind === "edit" &&
        (() => {
          const target = findInstance(openEditor.name);
          return target ? (
            <EditInstanceDialog instance={target} onCancel={closeEditor} onSuccess={closeEditor} />
          ) : null;
        })()}
      {openEditor?.kind === "oauth-redirect" && (
        <OAuthRedirectDialog
          name={openEditor.name}
          flowId={openEditor.flowId}
          authUrl={openEditor.authUrl}
          onCancel={closeEditor}
          onSuccess={closeEditor}
        />
      )}
      {openEditor?.kind === "device" && (
        <DeviceCodeDialog
          key={openEditor.flowId}
          name={openEditor.name}
          flowId={openEditor.flowId}
          userCode={openEditor.userCode}
          verificationUrl={openEditor.verificationUrl}
          intervalSeconds={openEditor.intervalSeconds}
          onCancel={closeEditor}
          onSuccess={closeEditor}
          onRestart={() => void handleOAuthStart(openEditor.name)}
        />
      )}

      <ConfirmDialog
        open={pendingConfirm !== null}
        title={pendingConfirm?.kind === "clear" ? "Clear credentials" : "Remove instance"}
        confirmLabel={pendingConfirm?.kind === "clear" ? "Clear" : "Remove"}
        busy={confirmBusy}
        onConfirm={() => void handleConfirmedAction()}
        onCancel={() => setPendingConfirm(null)}
      >
        {pendingConfirm?.kind === "clear"
          ? `Clear stored credentials for "${pendingConfirm.name}"?`
          : `Remove instance "${pendingConfirm?.name}"? This will also clear its stored credentials.`}
      </ConfirmDialog>
    </div>
  );
}
