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

import type { AuthTestResponse, InstanceEntry } from "@evener/appwire-client";
import {
  CONNECTION_REPLACED_ERROR,
  ENDPOINT_CHANGED_TEST_MESSAGE,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  fingerprintUnavailable,
  friendlyErrorMessage,
  fromEnvironment,
  isEndpointConflict,
  isInstanceRemoveApplied,
  safeCredentialTestResult,
} from "@evener/appwire-client";
import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { credentialsStore, isStaleListingRefusal, useCredentialsStore } from "../../../../stores/credentials";
import {
  Button,
  ConfirmDialog,
  Dialog,
  EmptyState,
  Loader,
  Skeleton,
  useFocusRehome,
  useToasts,
} from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useConnectedEffect } from "../useConnectedEffect";
import { ConnectProviderDialogBoundary, useConnectProviderDialogChunk } from "./ConnectProviderDialogBoundary";
import styles from "./CredentialsSection.module.css";
import { InstanceSheet } from "./InstanceSheet";
import { AddInstanceDialog, ApiKeyDialog, CredentialJsonDialog } from "./instanceDialogs";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";
import { type OAuthEditor, startOAuthFlow } from "./oauthFlow";
import { ProviderInstanceGroups } from "./ProviderInstanceGroups";
import { confirmListingState, refreshListingAfterMutation } from "./reconcileListing";

const CLASS = {
  root: requireClass(styles.root, "CredentialsSection.module.css", "root"),
  headerRow: requireClass(styles.headerRow, "CredentialsSection.module.css", "headerRow"),
  error: requireClass(styles.error, "CredentialsSection.module.css", "error"),
  diagnostics: requireClass(styles.diagnostics, "CredentialsSection.module.css", "diagnostics"),
  diagnosticsHeading: requireClass(styles.diagnosticsHeading, "CredentialsSection.module.css", "diagnosticsHeading"),
  diagnosticsList: requireClass(styles.diagnosticsList, "CredentialsSection.module.css", "diagnosticsList"),
};

type OpenEditor =
  | { kind: "add" }
  | { kind: "apiKey"; name: string; expectedEndpointFingerprint?: string }
  | { kind: "credentialJson"; name: string; expectedEndpointFingerprint?: string }
  | OAuthEditor
  | null;

// expectedEndpointFingerprint is the endpoint the confirm was opened against,
// captured from the row the user acted on. A concurrent client can put a
// different instance under the name while the dialog is open, and the hub uses
// this to refuse clearing or removing the replacement. Undefined when the row
// showed no endpoint, so an old listing asserts nothing rather than an empty
// value.
type PendingConfirm = {
  kind: "clear" | "clearStoredKey" | "remove";
  name: string;
  expectedEndpointFingerprint?: string;
} | null;
type CredentialTestState = { version: number; pending: boolean; result?: AuthTestResponse };

// What a confirm-gated action (Remove, Clear, Clear stored key) says when the
// hub refuses the destination the confirmation asserted: the name moved since
// the row was read, so nothing was sent. The credential test's own sentence
// ends "and test again" and the sheet's save wording ends "so the change was
// not saved" - neither fits a confirmed removal or clear, so this is the
// actions' own wording, kept here beside them rather than in the client package
// where only cross-client messages live.
const ENDPOINT_CHANGED_CONFIRM_ERROR =
  "This instance changed to a different endpoint since this confirmation was opened, so nothing was changed. The provider list was refreshed; review its destination and try again.";

// Diagnostics: the providers.toml load-error pointer, the user-layer note,
// stray OAuth record notices, and registry warnings (InstanceListResponse.
// diagnostics, spec §11.3) - mirrors launchServer.tsx's own Diagnostics
// component (this pane's sibling settings section), a flat unordered list
// with no stable per-entry identity of its own.
export function Diagnostics({ diagnostics }: { diagnostics: string[] }) {
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

// NO_PENDING_KEYS is the shared empty set a render with no sheet open hands
// down, instead of allocating one per render.
const NO_PENDING_KEYS: ReadonlySet<string> = new Set();

// withoutKey removes one pending key, keeping the same set object when it is
// already absent so React can bail out of an unchanged update.
function withoutKey(current: ReadonlySet<string>, key: string): ReadonlySet<string> {
  if (!current.has(key)) return current;
  const next = new Set(current);
  next.delete(key);
  return next;
}

export function CredentialsSection({
  fullEditor = false,
  onInstanceRenamed,
  onInstanceRemoved,
}: CredentialsSectionProps) {
  const {
    instances,
    availableProviders,
    diagnostics,
    writesRefused,
    loading,
    error,
    fetch,
    listingFromPreviousConnection,
  } = useCredentialsStore();
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
  // Live-model refresh for the sheet is manual: the hub prefetches every
  // instance's listing at startup and every few minutes after, so the
  // Models toggles read cached inventory. The Refresh button below
  // re-fetches on demand; failures toast and keep the cached rows. The
  // pending set holds every in-flight instance, so concurrent refreshes
  // for A and B each disable only their own sheet.
  const [refreshingInstances, setRefreshingInstances] = useState<ReadonlySet<string>>(new Set());
  // Pending model toggles by `instance/model`: switches stay enabled
  // only when no write for their row is in flight, so rapid clicks
  // cannot submit duplicate or reordered writes against a stale
  // `checked` prop (a quick disable-then-enable settles in order).
  const [pendingToggles, setPendingToggles] = useState<ReadonlySet<string>>(new Set());
  // pendingToggleKeys is the synchronous half of the same guard: React may
  // defer the state updater below past the next click, so the check has to
  // read a ref that this handler has already written. The state copy is what
  // renders the switch disabled.
  const pendingToggleKeys = useRef(new Set<string>());

  async function handleRefreshModels(name: string): Promise<void> {
    setRefreshingInstances((current) => new Set(current).add(name));
    try {
      await credentialsStore.getState().refreshModels(name);
    } catch (err) {
      toast.push("error", `Live refresh failed: ${friendlyErrorMessage(err)}`);
    } finally {
      setRefreshingInstances((current) => withoutKey(current, name));
    }
  }
  const previousInstances = useRef(instances);
  const instanceVersion = useRef(0);
  if (previousInstances.current !== instances) {
    previousInstances.current = instances;
    instanceVersion.current += 1;
  }
  const toast = useToasts();

  // Any action this section issues can be refused by the store because the
  // rows it would act on were read by a replaced connection
  // (stores/credentials.ts's requireWritableClient). Every one of them answers
  // the same way: say what changed - never in the store's own words, and never
  // as a failure of the action the user asked for, which nothing was sent for -
  // and ask for this connection's listing, whose arrival is what makes the
  // action retryable. Answers true when it handled the refusal.
  function recoverStaleListing(err: unknown): boolean {
    if (!isStaleListingRefusal(err)) return false;
    toast.push("warning", CONNECTION_REPLACED_ERROR);
    void fetch().catch(() => {});
    return true;
  }

  // A pending test was issued against the listing a replacement took away, so
  // its answer describes rows of a connection that is gone. Until the new
  // listing lands nothing else clears it: the row would sit in "Testing
  // credentials…", and an answer arriving in that window would be shown as a
  // result for rows this connection never read. The listing read that lands next
  // is what makes a test runnable again.
  useEffect(() => {
    if (listingFromPreviousConnection) setCredentialTests({});
  }, [listingFromPreviousConnection]);

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
      if (recoverStaleListing(err)) return;
      toast.push("error", `Sign-in failed: ${friendlyErrorMessage(err)}`);
    }
  }

  // "★ make default" has no confirm and no success toast (silent success -
  // only a failure toast exists), matching the legacy exactly.
  async function handleSetDefault(name: string): Promise<void> {
    try {
      await credentialsStore.getState().setDefault(name);
    } catch (err) {
      if (recoverStaleListing(err)) return;
      toast.push("error", `Set default failed: ${friendlyErrorMessage(err)}`);
    }
  }

  // instanceFingerprint is where the listing the row was read from says the
  // name resolves. The probe asserts it, so the hub can refuse a check whose
  // name was re-pointed since that read.
  function instanceFingerprint(name: string): string | undefined {
    return instances.find((candidate) => candidate.name === name)?.endpointFingerprint;
  }

  // Model toggles are self-contained like "make default": no confirm, and
  // a toast only on failure — plus a success toast naming the change, since
  // unlike a default flag the switch needs visible confirmation it landed.
  async function handleToggleModel(name: string, model: string, disabled: boolean): Promise<void> {
    const key = `${name}/${model}`;
    // A second click on the same row while its write is in flight is a
    // no-op: the switch is disabled meanwhile, and this guards the
    // programmatic path too. The ref is what makes the guard synchronous —
    // two clicks in one tick both run before React re-renders the switch
    // disabled, so a state-only check would let both reach the wire.
    if (pendingToggleKeys.current.has(key)) return;
    pendingToggleKeys.current.add(key);
    setPendingToggles((current) => new Set(current).add(key));
    try {
      await credentialsStore.getState().setModelDisabled({ name, model, disabled });
      toast.push("success", `${disabled ? "Disabled" : "Enabled"} ${model}`);
    } catch (err) {
      if (recoverStaleListing(err)) return;
      toast.push("error", `Model toggle failed: ${friendlyErrorMessage(err)}`);
    } finally {
      pendingToggleKeys.current.delete(key);
      setPendingToggles((current) => withoutKey(current, key));
    }
  }

  async function handleTestCredentials(name: string): Promise<void> {
    const version = instanceVersion.current;
    if (credentialTests[name]?.version === version && credentialTests[name]?.pending) return;
    // A destination the hub cannot fingerprint has no assertion to send, and an
    // unasserted probe dials whatever the name resolves to now: refuse it here
    // rather than let the hub check a destination the user never reviewed.
    if (fingerprintUnavailable(instances.find((row) => row.name === name))) {
      toast.push("error", FINGERPRINT_UNAVAILABLE_TEST_MESSAGE);
      return;
    }
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
      settle(await credentialsStore.getState().testCredentials(name, instanceFingerprint(name)));
    } catch (err) {
      if (recoverStaleListing(err)) {
        // The refusal is not a probe result: clear the pending test so the
        // action returns to its idle label, ready for the retry once this
        // connection's listing lands.
        setCredentialTests((current) => ({ ...current, [name]: { version, pending: false } }));
        return;
      }
      if (isEndpointConflict(err)) {
        // The hub refused the asserted destination: the name moved since this
        // listing was read, so there is no honest test result to show. Clear
        // the pending test, say why, and re-read the listing so a retry asserts
        // the destination now on screen.
        setCredentialTests((current) => ({ ...current, [name]: { version, pending: false } }));
        toast.push("error", ENDPOINT_CHANGED_TEST_MESSAGE);
        void fetch().catch(() => {});
        return;
      }
      settle({ provider: name, status: "endpoint_failure", message: "" });
    }
  }

  async function handleConfirmedAction(): Promise<void> {
    if (!pendingConfirm) return;
    const { kind, name, expectedEndpointFingerprint } = pendingConfirm;
    setConfirmBusy(true);
    try {
      if (kind === "clear") {
        await credentialsStore.getState().logout(name, expectedEndpointFingerprint);
        await refreshListingAfterMutation();
        toast.push("success", `Credentials cleared for ${name}`);
      } else if (kind === "clearStoredKey") {
        const label = clearsCredentialJson(name) ? "Stored credential JSON" : "Stored key";
        await credentialsStore.getState().clearStoredKey(name, expectedEndpointFingerprint);
        await refreshListingAfterMutation();
        toast.push("success", `${label} cleared for ${name}`);
      } else {
        const applied = await credentialsStore.getState().remove(name, expectedEndpointFingerprint);
        // The store's own listing IS the applied response when the removal won
        // the race, so it can be checked directly; only a superseded removal
        // has to be re-read. Either way the row must be gone from the listing
        // before the removal is reported, or the guided owner's reset fires on
        // a listing that never lost it. What must be gone is the row the USER
        // owns: a name the environment also supplies comes back as a row the
        // host derives (env:<VAR>, ADC, a keyless local default), and
        // requiring it to vanish would report a removal that happened as
        // unconfirmed. `implicit` alone is not that test - a stored key and a
        // signed-in Codex record are implicit rows the removal does delete, so
        // a stale listing still holding one must not be confirmed (and a
        // surviving one is not "environment access"). fromEnvironment answers
        // it by source.
        const removed = (instances: InstanceEntry[]) =>
          !instances.some((instance) => instance.name === name && !fromEnvironment(instance));
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
        // The user's row is gone, but the environment can still supply access
        // under this name, and the row that remains in the listing says so.
        // "Removed instance X" alone would read as "no access under this name
        // any more", which is not what the hub's own listing reports.
        const stillSupplied = credentialsStore
          .getState()
          .instances.some((instance) => instance.name === name && fromEnvironment(instance));
        // The name can survive the removal as an environment-supplied implicit
        // row, and a sheet left open on it would keep the removed instance's
        // dirty draft attached to a row the user never edited - a save from it
        // would author a new override out of that draft. Close the selection:
        // the replacement row (if any) opens fresh.
        setSelectedInstance(null);
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
      // The removal applied before it failed: the hub deleted the instance's
      // credential (or its config entry) and could not put it back, so the
      // removal stands. Reconcile it - close the confirmation and the sheet,
      // re-read the listing, tell the owning editor the instance is gone -
      // rather than report a failed Remove whose retry targets a missing
      // instance. The discriminator is authoritative, so this does not wait on
      // the listing to confirm it, and the selection is cleared the way the
      // success path clears it: a sheet left open on the name keeps the removed
      // instance's dirty draft attached to a row the user never edited, and a
      // save from it would author a new override out of that draft.
      if (kind === "remove" && isInstanceRemoveApplied(err)) {
        setPendingConfirm(null);
        setSelectedInstance(null);
        await refreshListingAfterMutation();
        toast.push("warning", friendlyErrorMessage(err));
        onInstanceRemoved?.(name);
        return;
      }
      if (recoverStaleListing(err)) {
        // The confirmation holds the destination fingerprint the row showed when
        // it was opened, which is the connection that is gone: a retry against
        // the listing that lands next has to capture it again, so the dialog
        // closes rather than carrying a stale assertion into the retry.
        setPendingConfirm(null);
        return;
      }
      if (isEndpointConflict(err)) {
        // The hub refused the asserted destination: the name moved since this
        // row was read, so nothing was sent. The confirmation holds that stale
        // fingerprint, so leave the user able to retry - close the dialog, clear
        // the selection the way the action's own success path does (a sheet left
        // open would keep operating on the destination that moved), re-read the
        // listing, and warn in this client's own words. The next confirmation
        // captures the fingerprint now on screen; reported as a failed action,
        // the open dialog would re-send the same refused assertion. Mirrors the
        // mobile and TUI confirm paths.
        setPendingConfirm(null);
        setSelectedInstance(null);
        await refreshListingAfterMutation();
        toast.push("warning", ENDPOINT_CHANGED_CONFIRM_ERROR);
        return;
      }
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

  // useCallback'd (not a plain inline arrow) so its identity stays stable
  // across CredentialsSection re-renders - DeviceCodeDialog's own poll
  // effect depends on the onSuccess it's given, and an unstable reference
  // here would restart that dialog's poll timer on every unrelated parent
  // re-render (see oauthDialogs.tsx's own comment on that effect).
  const closeEditor = useCallback(() => setOpenEditor(null), []);
  const rootRef = useRef<HTMLDivElement>(null);
  // A listing that no longer carries the focused row can unmount the control
  // holding the keyboard (the rows themselves stay mounted through reads and
  // through failed ones, so those are no longer cases here). The hook re-homes
  // focus to the pane's first control; a commit that removes nothing leaves it
  // where it was.
  useFocusRehome(rootRef);

  return (
    <div className={CLASS.root} ref={rootRef}>
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
      {/* A warning describes the listing that produced it - a providers.toml
          load error, the user-layer note, a stray OAuth notice - so while the
          rows on screen belong to a connection that is gone they describe a
          listing this one never read. Suppressed until this connection's own
          read lands, the same way the management dialog suppresses them. */}
      {/* Gated on the raw marker rather than staleListingHeld: a warning
          describes the listing that produced it, and that is true even when the
          listing carried no rows to act on. */}
      {!listingFromPreviousConnection && <Diagnostics diagnostics={diagnostics} />}

      {/* The skeleton is for the state it was written for - nothing to show yet
          - never for a read that is merely in flight. The rows a refresh would
          have swapped out are the listing the user is reading: replacing them
          makes the pane flicker on every background read and unmounts the row
          the keyboard is on (the connection dialog keeps its own rows for the
          same reason). A failed read keeps the listing it already had
          (readListing), so those rows stay too, with the banner above them. */}
      {loading && instances.length === 0 && <Skeleton />}
      {error && <p className={CLASS.error}>Failed to load: {friendlyErrorMessage(error)}</p>}
      {!loading && !error && instances.length === 0 && <EmptyState title="No provider instances configured." />}
      {/* The provider-grouped listing is shared with the host-scoped view of a
          remote host's own listing (ProviderInstanceGroups); this surface's
          rows are the interactive variant. */}
      {instances.length > 0 && (
        <ProviderInstanceGroups
          instances={instances}
          availableProviders={availableProviders}
          onSelect={setSelectedInstance}
        />
      )}

      <InstanceSheet
        name={selectedInstance}
        writesRefused={writesRefused}
        onClose={() => setSelectedInstance(null)}
        onSetApiKey={() =>
          openEditorFromSheet((name) =>
            // Capture the row's endpoint identity now, beside the name: the
            // dialog submits this value, so a concurrent change cannot update
            // its destination out from under the already-entered secret.
            setOpenEditor({
              kind: "apiKey",
              name,
              expectedEndpointFingerprint: findInstance(name)?.endpointFingerprint,
            }),
          )
        }
        onSetCredentialJson={() =>
          openEditorFromSheet((name) =>
            setOpenEditor({
              kind: "credentialJson",
              name,
              expectedEndpointFingerprint: findInstance(name)?.endpointFingerprint,
            }),
          )
        }
        onOAuthStart={() => openEditorFromSheet((name) => void handleOAuthStart(name))}
        onRenamed={(next) => {
          const previous = selectedInstance;
          setSelectedInstance(next);
          if (previous !== null && previous !== next) onInstanceRenamed?.(previous, next);
        }}
        onClear={() => {
          if (selectedInstance !== null) {
            setPendingConfirm({
              kind: "clear",
              name: selectedInstance,
              expectedEndpointFingerprint: findInstance(selectedInstance)?.endpointFingerprint,
            });
          }
        }}
        onClearStoredKey={() => {
          if (selectedInstance !== null) {
            setPendingConfirm({
              kind: "clearStoredKey",
              name: selectedInstance,
              expectedEndpointFingerprint: findInstance(selectedInstance)?.endpointFingerprint,
            });
          }
        }}
        onRemove={() => {
          if (selectedInstance !== null) {
            setPendingConfirm({
              kind: "remove",
              name: selectedInstance,
              expectedEndpointFingerprint: findInstance(selectedInstance)?.endpointFingerprint,
            });
          }
        }}
        onSetDefault={() => {
          if (selectedInstance !== null) void handleSetDefault(selectedInstance);
        }}
        onToggleModel={(model, disabled) => {
          if (selectedInstance !== null) void handleToggleModel(selectedInstance, model, disabled);
        }}
        onRefreshModels={() => {
          if (selectedInstance !== null) void handleRefreshModels(selectedInstance);
        }}
        modelsRefreshing={selectedInstance !== null && refreshingInstances.has(selectedInstance)}
        pendingToggles={selectedInstance !== null ? pendingToggles : NO_PENDING_KEYS}
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
          return target ? (
            <ApiKeyDialog
              instance={target}
              expectedEndpointFingerprint={openEditor.expectedEndpointFingerprint}
              onCancel={closeEditor}
              onSuccess={closeEditor}
            />
          ) : null;
        })()}
      {openEditor?.kind === "credentialJson" &&
        (() => {
          const target = findInstance(openEditor.name);
          return target ? (
            <CredentialJsonDialog
              instance={target}
              expectedEndpointFingerprint={openEditor.expectedEndpointFingerprint}
              onCancel={closeEditor}
              onSuccess={closeEditor}
            />
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
