// CredentialsSection (#7 - the dominant piece of the Agents & models
// cluster), detail-sheet redesign: instance rows are single tappable
// targets that open an InstanceSheet; the sheet is the instance's editor
// (name, base URL, protocol, surface, vars, api-key env, credential
// header; spec 2026-09-07 §2) and every other per-instance action (test,
// set key, sign in, make default, clear, clear stored key, remove) lives
// in it too - the same row→detail-sheet collection-page idiom the
// marketplacesPlugins redesign introduced (a sibling workstream; the
// idiom's design-system writeup lands with it) - instead of a per-row
// button cluster. The secret-entry and multi-step flows
// (add/apiKey/credential JSON/OAuth) stay dialogs: opening one from the
// sheet replaces it (single-mutable-editor invariant below).
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
import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { friendlyErrorMessage } from "../../../../protocol/errors";
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";
import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
import { Button, ConfirmDialog, Dialog, EmptyState, Loader, Skeleton, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useConnectedEffect } from "../useConnectedEffect";
import { ConnectProviderDialogBoundary, useConnectProviderDialogChunk } from "./ConnectProviderDialogBoundary";
import styles from "./CredentialsSection.module.css";
import { groupByProvider, safeCredentialTestResult } from "./credentialLabels";
import { InstanceRow } from "./InstanceRow";
import { InstanceSheet } from "./InstanceSheet";
import { AddInstanceDialog, ApiKeyDialog, CredentialJsonDialog } from "./instanceDialogs";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";
import { type OAuthEditor, startOAuthFlow } from "./oauthFlow";
import { confirmListingState, refreshListingAfterMutation } from "./reconcileListing";

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
  | { kind: "credentialJson"; name: string }
  | OAuthEditor
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
  /** The connector's escape must retain the full editor, not reopen itself. */
  // fullEditor selects the raw add-instance entry (the form that authors
  // providers.toml) instead of the guided connector. Only
  // ConnectProviderDialog's own "Full provider settings" view passes it; the
  // Settings pane renders the section without it so its header action opens the
  // guided flow, which is the reachable path to the full editor from there.
  fullEditor?: boolean;
  /** Reports a successful rename from this section's own instance sheet, so a
   * caller that holds a name for the same instance - the guided connection kept
   * mounted behind the full settings view - can follow it instead of searching
   * for the old one. */
  onInstanceRenamed?: (from: string, to: string) => void;
  /** Reports a successful removal from this section's own instance sheet, so a
   * caller that holds a name for the same instance - the guided connection
   * kept mounted behind the full settings view - can drop its retained editing
   * state: an instance later recreated under the same name is a new entity. */
  onInstanceRemoved?: (name: string) => void;
}

export function CredentialsSection({
  fullEditor = false,
  onInstanceRenamed,
  onInstanceRemoved,
}: CredentialsSectionProps) {
  const { instances, availableProviders, diagnostics, writesRefused, loading, error, fetch } = useCredentialsStore();
  const [connecting, setConnecting] = useState(false);
  // The connector is a dynamic-only import (see connectDialogChunk.ts): loading
  // it through the shared hook keeps ConnectProviderDialog out of this module's
  // static graph, which is what keeps it in its own chunk and its failure
  // handling detectable. A static import here closed the cycle
  // CredentialsSection -> ConnectProviderDialog -> CredentialsSection, which
  // collapsed the two into one chunk (roborev round 5).
  const {
    Dialog: ConnectProviderDialog,
    version: connectDialogVersion,
    retry: retryConnectDialog,
    reloadAvailable: connectDialogReloadAvailable,
  } = useConnectProviderDialogChunk();
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
      setOpenEditor(await startOAuthFlow(name));
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
        await refreshListingAfterMutation();
        toast.push("success", `Credentials cleared for ${name}`);
      } else if (kind === "clearStoredKey") {
        const label = clearsCredentialJson(name) ? "Stored credential JSON" : "Stored key";
        await credentialsStore.getState().clearStoredKey(name);
        await refreshListingAfterMutation();
        toast.push("success", `${label} cleared for ${name}`);
      } else {
        const applied = await credentialsStore.getState().remove(name);
        // The store's own listing IS the applied response when the removal won
        // the race, so it can be checked directly; only a superseded removal
        // has to be re-read. Either way the row must be gone from the listing
        // before the removal is reported, or the guided owner's reset fires on
        // a listing that never lost it. What must be gone is the *authored*
        // row: a name the environment also supplies comes back as an implicit
        // instance the moment the authored entry is removed, and requiring the
        // name itself to vanish would report a removal that happened as
        // unconfirmed.
        const removed = (instances: InstanceEntry[]) =>
          !instances.some((instance) => instance.name === name && !instance.implicit);
        const confirmed = applied ? removed(credentialsStore.getState().instances) : await confirmListingState(removed);
        if (!confirmed) {
          // Close the confirm dialog with the failure: the row is gone on the
          // host, so re-issuing the remove can only fail on a missing instance.
          setPendingConfirm(null);
          toast.push("error", `Removed on the host, but the provider list could not be confirmed for ${name}`);
          // A superseded verdict is not a failure: the removal's RPC resolved
          // and only its response was discarded, so the entry left the host,
          // and the listing that still shows it is one this client read before
          // the removal landed. The guided owner has to hear about it anyway -
          // what it retains for this name (a typed credential draft, a saved or
          // configured verdict) describes an instance that no longer exists,
          // and a name recreated under it would inherit that state. The applied
          // path is the opposite case: the hub's own response still held an
          // authored row, so the removal did not happen and is not reported.
          if (!applied) onInstanceRemoved?.(name);
          return;
        }
        // The authored entry is gone, but the environment can still supply
        // access under this name, and the row that remains in the listing says
        // so. "Removed instance X" alone would read as "no access under this
        // name any more", which is not what the hub's own listing reports.
        const stillSupplied = credentialsStore
          .getState()
          .instances.some((instance) => instance.name === name && instance.implicit);
        toast.push(
          "success",
          stillSupplied
            ? `Removed instance ${name}; environment access for it is still active`
            : `Removed instance ${name}`,
        );
        onInstanceRemoved?.(name);
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

  // clearStoredKey's dialog/toast name what it clears: a gcp-adc instance's
  // stored credential is a JSON document (spec 2026-09-04
  // google-vertex-express §4), not an API key, so "stored key" reads wrong
  // for it - same scheme test InstanceSheet's own button uses.
  function clearsCredentialJson(name: string): boolean {
    return findInstance(name)?.authModes?.includes("credentialJson") ?? false;
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
        {/* Only the raw add-instance action authors providers.toml, so only it
            is gated by the write refusal. The guided connector stays reachable:
            its credential writes go to the credential store (a different file),
            and a registry whose user layer failed to load still serves the
            curated/implicit providers it did read (cmd/evener-hub/main_test.go's
            broken-layer case), so disabling the entry point would hide a path
            that works. The connector's own configuration step surfaces the
            refusal when it needs a config write. */}
        <Button
          onClick={() => (fullEditor ? setOpenEditor({ kind: "add" }) : setConnecting(true))}
          disabled={fullEditor && writesRefused}
        >
          {fullEditor ? "+ Add provider instance" : "Connect provider"}
        </Button>
      </div>

      {connecting && (
        <ConnectProviderDialogBoundary
          key={connectDialogVersion}
          onRetry={retryConnectDialog}
          reloadAvailable={connectDialogReloadAvailable}
          onClose={() => setConnecting(false)}
        >
          <Suspense
            fallback={
              <Dialog open onClose={() => setConnecting(false)} title="Connect provider">
                <Loader label="Loading…" />
              </Dialog>
            }
          >
            <ConnectProviderDialog onClose={() => setConnecting(false)} onConnected={() => setConnecting(false)} />
          </Suspense>
        </ConnectProviderDialogBoundary>
      )}
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

      <InstanceSheet
        name={selectedInstance}
        writesRefused={writesRefused}
        onClose={() => setSelectedInstance(null)}
        onSetApiKey={() => openEditorFromSheet((name) => setOpenEditor({ kind: "apiKey", name }))}
        onSetCredentialJson={() => openEditorFromSheet((name) => setOpenEditor({ kind: "credentialJson", name }))}
        onOAuthStart={() => openEditorFromSheet((name) => void handleOAuthStart(name))}
        onRenamed={(next) => {
          const previous = selectedInstance;
          setSelectedInstance(next);
          if (previous !== null && previous !== next) onInstanceRenamed?.(previous, next);
        }}
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
      {openEditor?.kind === "credentialJson" &&
        (() => {
          const target = findInstance(openEditor.name);
          return target ? (
            <CredentialJsonDialog instance={target} onCancel={closeEditor} onSuccess={closeEditor} />
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
              ? clearsCredentialJson(pendingConfirm.name)
                ? "Clear stored credential JSON"
                : "Clear stored key"
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
            ? clearsCredentialJson(pendingConfirm.name)
              ? `Clear the stored credential JSON for "${pendingConfirm.name}"? Its active sign-in is not affected.`
              : `Clear the stored API key for "${pendingConfirm.name}"? Its active sign-in is not affected.`
            : `Remove instance "${pendingConfirm?.name}"? This will also clear its stored credentials.`}
      </ConfirmDialog>
    </div>
  );
}
