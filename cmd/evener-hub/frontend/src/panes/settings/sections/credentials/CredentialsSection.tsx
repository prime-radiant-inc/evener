// CredentialsSection (#7 - the dominant piece of the Agents & models
// cluster), detail-sheet redesign: instance rows are single tappable
// targets that open an InstanceDetailSheet inspector; every per-instance
// action (test, set key, sign in, edit, make default, clear, clear stored
// key, remove) lives in that sheet - the same row→detail-sheet
// collection-page idiom the marketplacesPlugins redesign introduced (a
// sibling workstream; the idiom's design-system writeup lands with it) -
// instead of a per-row button cluster. The editors (add/apiKey/edit/OAuth
// flows) stay dialogs: opening one from the sheet replaces it
// (single-mutable-editor invariant below).
//
// Updated for the provider registry's instance wire shape (spec §11.3):
// instances group by providerId, the add form is fed availableProviders, and
// a providers.toml load error surfaces as a diagnostics banner that disables
// every instance-CRUD action until it clears (writesRefused) - Set key/Sign
// in/Clear/Clear stored key/Test credentials are unaffected, since none of
// them write providers.toml.
//
// Single-mutable-editor invariant: `openEditor` is ONE section-level state
// value (a discriminated union), so opening a second editor always replaces
// whatever was open, matching the legacy's own single module-level
// `openEditor` variable - no per-row state, no dirty-check on replace.
import { useCallback, useEffect, useRef, useState } from "react";
import { friendlyErrorMessage } from "../../../../protocol/errors";
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";
import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
import { Button, ConfirmDialog, EmptyState, Skeleton, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useConnectedEffect } from "../useConnectedEffect";
import styles from "./CredentialsSection.module.css";
import { groupByProvider, safeCredentialTestResult } from "./credentialLabels";
import { InstanceDetailSheet } from "./InstanceDetailSheet";
import { InstanceRow } from "./InstanceRow";
import { AddInstanceDialog, ApiKeyDialog, EditInstanceDialog } from "./instanceDialogs";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";

const CLASS = {
  root: requireClass(styles.root, "CredentialsSection.module.css", "root"),
  headerRow: requireClass(styles.headerRow, "CredentialsSection.module.css", "headerRow"),
  error: requireClass(styles.error, "CredentialsSection.module.css", "error"),
  groups: requireClass(styles.groups, "CredentialsSection.module.css", "groups"),
  group: requireClass(styles.group, "CredentialsSection.module.css", "group"),
  groupHeader: requireClass(styles.groupHeader, "CredentialsSection.module.css", "groupHeader"),
  list: requireClass(styles.list, "CredentialsSection.module.css", "list"),
  diagnostics: requireClass(styles.diagnostics, "CredentialsSection.module.css", "diagnostics"),
  diagnosticsHeading: requireClass(styles.diagnosticsHeading, "CredentialsSection.module.css", "diagnosticsHeading"),
  diagnosticsList: requireClass(styles.diagnosticsList, "CredentialsSection.module.css", "diagnosticsList"),
};

type OpenEditor =
  | { kind: "add" }
  | { kind: "apiKey"; name: string }
  | { kind: "edit"; name: string }
  | { kind: "oauth-redirect"; name: string; flowId: string; authUrl: string }
  | { kind: "device"; name: string; flowId: string; userCode: string; verificationUrl: string; intervalSeconds: number }
  | null;

type PendingConfirm = { kind: "clear" | "clearStoredKey" | "remove"; name: string } | null;
type CredentialTestState = { version: number; pending: boolean; result?: AuthTestResponse };

// Diagnostics: the providers.toml load-error pointer, the user-layer note,
// stray OAuth record notices, and registry warnings (InstanceListResponse.
// diagnostics, spec §11.3) - mirrors launchServer.tsx's own Diagnostics
// component (this pane's sibling settings section), a flat unordered list
// with no stable per-entry identity of its own.
function Diagnostics({ diagnostics }: { diagnostics: string[] }) {
  if (diagnostics.length === 0) return null;
  return (
    <div className={CLASS.diagnostics} role="status" aria-live="polite">
      <p className={CLASS.diagnosticsHeading}>Warnings</p>
      <ul className={CLASS.diagnosticsList}>
        {diagnostics.map((d, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: diagnostics are a flat, unordered warning list with no stable identity of their own
          <li key={index}>{d}</li>
        ))}
      </ul>
    </div>
  );
}

export interface CredentialsSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

export function CredentialsSection(_props: CredentialsSectionProps) {
  const { instances, availableProviders, diagnostics, writesRefused, loading, error, fetch } = useCredentialsStore();
  const [openEditor, setOpenEditor] = useState<OpenEditor>(null);
  const [selectedInstance, setSelectedInstance] = useState<string | null>(null);
  const [pendingConfirm, setPendingConfirm] = useState<PendingConfirm>(null);
  const [confirmBusy, setConfirmBusy] = useState(false);
  const [credentialTests, setCredentialTests] = useState<Record<string, CredentialTestState>>({});
  const previousInstances = useRef(instances);
  const instanceVersion = useRef(0);
  if (previousInstances.current !== instances) {
    previousInstances.current = instances;
    instanceVersion.current += 1;
  }
  const toast = useToasts();

  // biome-ignore lint/correctness/useExhaustiveDependencies: instances is a deliberate trigger-only dependency; each refreshed list invalidates results from the prior provider configuration
  useEffect(() => {
    setCredentialTests({});
  }, [instances]);

  // useConnectedEffect (not a bare useEffect): a direct deep link to
  // /credentials can mount this section before AppShell's own connect()
  // handshake finishes, and credentialsStore.fetch() requires a connected
  // client (throws otherwise) - see that hook's own doc comment.
  useConnectedEffect(fetch, [fetch]);

  // handleOAuthStart is shared by the sheet's "Sign in…"/"Refresh OAuth"
  // action and the device editor's "Start again" - always begins with
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
      toast.push("error", `Sign-in failed: ${friendlyErrorMessage(err)}`);
    }
  }

  // "★ make default" has no confirm and no success toast (silent success -
  // only a failure toast exists), matching the legacy exactly.
  async function handleSetDefault(name: string): Promise<void> {
    try {
      await credentialsStore.getState().setDefault(name);
    } catch (err) {
      toast.push("error", `Set default failed: ${friendlyErrorMessage(err)}`);
    }
  }

  async function handleTestCredentials(name: string): Promise<void> {
    const version = instanceVersion.current;
    if (credentialTests[name]?.version === version && credentialTests[name]?.pending) return;
    setCredentialTests((current) => ({ ...current, [name]: { version, pending: true } }));
    // A result lands only on the request that is still pending for the
    // instance list it was started against: a refreshed list bumps the
    // version, and a stale answer is dropped rather than shown.
    function settle(response: AuthTestResponse): void {
      setCredentialTests((current) => ({
        ...current,
        ...(current[name]?.version === version && current[name]?.pending
          ? { [name]: { version, pending: false, result: safeCredentialTestResult(name, response) } }
          : {}),
      }));
    }
    try {
      settle(await credentialsStore.getState().testCredentials(name));
    } catch {
      settle({ provider: name, status: "endpoint_failure", message: "" });
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
      } else if (kind === "clearStoredKey") {
        await credentialsStore.getState().clearStoredKey(name);
        await credentialsStore.getState().fetch();
        toast.push("success", `Stored key cleared for ${name}`);
      } else {
        await credentialsStore.getState().remove(name);
        toast.push("success", `Removed instance ${name}`);
      }
      setPendingConfirm(null);
    } catch (err) {
      const verb = kind === "clear" ? "Clear" : kind === "clearStoredKey" ? "Clear stored key" : "Remove";
      toast.push("error", `${verb} failed: ${friendlyErrorMessage(err)}`);
    } finally {
      setConfirmBusy(false);
    }
  }

  function findInstance(name: string): InstanceEntry | undefined {
    return instances.find((i) => i.name === name);
  }

  // Editor-opening sheet actions REPLACE the inspector with the flow's
  // dialog (one overlay owns the screen during a flow); confirm-gated and
  // self-contained actions (clear, remove, test, make default) leave it
  // open - the confirm nests over it, and the store keeps it live.
  function openEditorFromSheet(editor: (name: string) => void): void {
    const name = selectedInstance;
    setSelectedInstance(null);
    if (name !== null) editor(name);
  }

  const groups = groupByProvider(instances);
  // useCallback'd (not a plain inline arrow) so its identity stays stable
  // across CredentialsSection re-renders - DeviceCodeDialog's own poll
  // effect depends on the onSuccess it's given, and an unstable reference
  // here would restart that dialog's poll timer on every unrelated parent
  // re-render (see oauthDialogs.tsx's own comment on that effect).
  const closeEditor = useCallback(() => setOpenEditor(null), []);

  return (
    <div className={CLASS.root}>
      <div className={CLASS.headerRow}>
        <Button onClick={() => setOpenEditor({ kind: "add" })} disabled={writesRefused}>
          + Add provider instance
        </Button>
      </div>

      <Diagnostics diagnostics={diagnostics} />

      {loading && <Skeleton />}
      {error && <p className={CLASS.error}>Failed to load: {friendlyErrorMessage(error)}</p>}
      {!loading &&
        !error &&
        (instances.length === 0 ? (
          <EmptyState title="No provider instances configured." />
        ) : (
          <div className={CLASS.groups}>
            {groups.map((group) => (
              <div key={group.providerId} className={CLASS.group}>
                {/* `name || id`, the same label the Add dialog gives a
                    provider - one pane must not name a provider two ways. */}
                <div className={CLASS.groupHeader}>
                  {availableProviders.find((p) => p.id === group.providerId)?.name || group.providerId}
                </div>
                <ul className={CLASS.list}>
                  {group.instances.map((instance) => (
                    <InstanceRow
                      key={instance.name}
                      instance={instance}
                      onSelect={() => setSelectedInstance(instance.name)}
                    />
                  ))}
                </ul>
              </div>
            ))}
          </div>
        ))}

      <InstanceDetailSheet
        name={selectedInstance}
        writesRefused={writesRefused}
        onClose={() => setSelectedInstance(null)}
        onSetApiKey={() => openEditorFromSheet((name) => setOpenEditor({ kind: "apiKey", name }))}
        onOAuthStart={() => openEditorFromSheet((name) => void handleOAuthStart(name))}
        onEdit={() => openEditorFromSheet((name) => setOpenEditor({ kind: "edit", name }))}
        onClear={() => {
          if (selectedInstance !== null) setPendingConfirm({ kind: "clear", name: selectedInstance });
        }}
        onClearStoredKey={() => {
          if (selectedInstance !== null) setPendingConfirm({ kind: "clearStoredKey", name: selectedInstance });
        }}
        onRemove={() => {
          if (selectedInstance !== null) setPendingConfirm({ kind: "remove", name: selectedInstance });
        }}
        onSetDefault={() => {
          if (selectedInstance !== null) void handleSetDefault(selectedInstance);
        }}
        onTestCredentials={() => {
          if (selectedInstance !== null) void handleTestCredentials(selectedInstance);
        }}
        testCredentialsPending={
          selectedInstance !== null &&
          credentialTests[selectedInstance]?.version === instanceVersion.current &&
          (credentialTests[selectedInstance]?.pending ?? false)
        }
        testCredentialsResult={
          selectedInstance !== null && credentialTests[selectedInstance]?.version === instanceVersion.current
            ? credentialTests[selectedInstance]?.result
            : undefined
        }
      />

      {openEditor?.kind === "add" && (
        <AddInstanceDialog availableProviders={availableProviders} onCancel={closeEditor} onSuccess={closeEditor} />
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
        title={
          pendingConfirm?.kind === "clear"
            ? "Clear credentials"
            : pendingConfirm?.kind === "clearStoredKey"
              ? "Clear stored key"
              : "Remove instance"
        }
        confirmLabel={pendingConfirm?.kind === "remove" ? "Remove" : "Clear"}
        busy={confirmBusy}
        onConfirm={() => void handleConfirmedAction()}
        onCancel={() => setPendingConfirm(null)}
      >
        {pendingConfirm?.kind === "clear"
          ? `Clear stored credentials for "${pendingConfirm.name}"?`
          : pendingConfirm?.kind === "clearStoredKey"
            ? `Clear the stored API key for "${pendingConfirm.name}"? Its active sign-in is not affected.`
            : `Remove instance "${pendingConfirm?.name}"? This will also clear its stored credentials.`}
      </ConfirmDialog>
    </div>
  );
}
