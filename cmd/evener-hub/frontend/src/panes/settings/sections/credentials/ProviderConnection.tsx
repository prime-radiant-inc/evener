import type { AuthTestResponse, InstanceEntry, ProviderDescriptor } from "@evener/appwire-client";
import {
  activeSourceLabel,
  CONNECTION_REPLACED_ERROR,
  FINGERPRINT_UNAVAILABLE_ERROR,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  fingerprintUnavailable,
  isEndpointConflict,
  safeCredentialTestResult,
  WireError,
} from "@evener/appwire-client";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { connectionStore, useConnectionStore } from "../../../../stores/connection";
import {
  credentialsStore,
  foreignListingChange,
  isStaleListingRefusal,
  useCredentialsStore,
} from "../../../../stores/credentials";
import {
  Button,
  Card,
  Dialog,
  Disclosure,
  FormRow,
  InlineNotice,
  Input,
  ListRow,
  OpenButton,
  Skeleton,
} from "../../../../widgets";
import { useConnectedEffect } from "../useConnectedEffect";
import type { FormError } from "./formError";
import { AddInstanceDialog } from "./instanceDialogs";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";
import { type OAuthEditor, startOAuthFlow } from "./oauthFlow";
import styles from "./ProviderConnection.module.css";
import { useEditorLifetime } from "./useEditorLifetime";

// ENDPOINT_MOVED_ERROR is what this flow says when the destination the user
// reviewed is no longer the one the name resolves to - the hub refused the
// assertion this flow carried, or the client's own listing moved under it.
const ENDPOINT_MOVED_ERROR =
  "This connection changed to a different endpoint. Check its destination and enter the key again.";
const ACCESS_CHANGED_ERROR = "Access changed during the check. Checking again.";
const CHANGED_ERROR = "Connection or configuration changed. Review access and check again.";
const REFRESH_FAILED_ERROR = "Access could not be refreshed. Your saved credential is retained; retry the check.";
const RELOAD_FAILED_ERROR =
  "The saved connection could not be loaded. Reload it or open the full editor; do not create it again.";

// isAccessChangedConflict recognizes the hub refusing a check result that a
// hub-observed credential write superseded while the probe ran
// (appwire.Conflict: code -32013 with data.evenerErrorInfo "conflict"). The
// discriminant, never the code: endpointConflict shares the code and keeps its
// own destination-review handling above. This refusal is not evidence about
// the endpoint, so it must not be presented as an endpoint failure.
function isAccessChangedConflict(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === "conflict";
}

/** Why the flow stopped, as its alert's data-reason: the connection or its
 * configuration changed under it, the check or the save was refused for a
 * destination the hub cannot fingerprint, the endpoint moved, the connection
 * was replaced, a credential write superseded the check, the check failed, the
 * refresh failed, the connection needs configuration, the save or the sign-in
 * start failed, or a created connection could not be reloaded. */
type ConnectionErrorReason =
  | "changed"
  | "check-fingerprint-unavailable"
  | "fingerprint-unavailable"
  | "endpoint-changed"
  | "connection-replaced"
  | "access-changed"
  | "check-failed"
  | "refresh-failed"
  | "needs-configuration"
  | "save-failed"
  | "sign-in-failed"
  | "reload-failed";

export interface ProviderConnectionProps {
  onClose(): void;
  onConnected(name?: string): void;
  /** Hands the user to the settings pane's credentials section, where the
   * instances this flow connects are listed, edited, tested and removed. The
   * hosting dialog owns the handoff - it closes itself and navigates there
   * (ConnectProviderDialog), so this flow never renders that editor itself. */
  onOpenSettings(): void;
}

// Presentation only. Authentication capabilities always come from the catalogue.
const HELP: Record<string, { label: string; keyUrl: string; billing: string }> = {
  anthropic: {
    label: "Anthropic",
    keyUrl: "https://console.anthropic.com/settings/keys",
    billing: "Claude subscriptions do not include API billing. API usage is billed separately.",
  },
  openai: {
    label: "OpenAI",
    keyUrl: "https://platform.openai.com/api-keys",
    billing: "ChatGPT subscriptions do not include API billing. API usage is billed separately.",
  },
  // Codex is the other way into OpenAI: a subscription account added by
  // signing in, not a key. It gets a card of its own because the platform
  // key above cannot reach it - `api_key_env` is empty for this provider and
  // its transport reads only the OAuth record (registry spec §5.1) - so a
  // user whose access is a ChatGPT/Codex subscription has nothing to paste.
  // keyUrl is here for completeness only: it renders on the API-key help
  // link, which this provider's auth modes never offer.
  "openai-codex": {
    label: "OpenAI Codex",
    keyUrl: "https://developers.openai.com/codex",
    billing: "Codex access comes from your ChatGPT/Codex subscription. Sign in instead of pasting a key.",
  },
  google: {
    label: "Gemini",
    keyUrl: "https://aistudio.google.com/apikey",
    billing: "Gemini API billing and limits are separate from your chat subscription.",
  },
  openrouter: {
    label: "OpenRouter",
    keyUrl: "https://openrouter.ai/settings/keys",
    billing: "API billing uses your OpenRouter credits.",
  },
};

function findSetupIn(
  listing: { instances: InstanceEntry[]; availableProviders: ProviderDescriptor[] },
  name: string,
): InstanceEntry | undefined {
  return (
    listing.instances.find((row) => row.name === name) ??
    listing.availableProviders.find((row) => row.setup?.name === name)?.setup
  );
}
function findSetup(name: string): InstanceEntry | undefined {
  return findSetupIn(credentialsStore.getState(), name);
}
function destination(row: InstanceEntry | undefined): string {
  if (!row) return "";
  return JSON.stringify([
    row.name,
    row.providerId,
    row.base,
    // The complete endpoint identity, query parameters included: baseUrl is the
    // sanitized copy the user reads, and a query-only change (an API version, a
    // deployment, a token) leaves it identical. Without this the flow would
    // treat a different endpoint as the one it anchored on and skip the review.
    row.endpointFingerprint,
    row.baseUrl,
    row.protocol,
    row.surface,
    row.auth,
    row.authModes,
    row.vars,
    row.apiKeyEnv,
    row.credentialHeader,
    row.hidden,
  ]);
}
/** accessIdentity is the selected row's effective access, separate from its
 * destination: which credential source resolves, the revision of that
 * configuration, and the stored/account facts that say what access is there.
 * A listing refresh that replaces the row without moving any of these - new
 * models, warnings, or another provider's entirely separate row - has not
 * changed the evidence a check is about. */
function accessIdentity(row: InstanceEntry | undefined): string {
  if (!row) return "";
  return JSON.stringify([
    row.activeSource,
    row.configRevision,
    row.hasStoredFile,
    row.hasStoredOAuth,
    row.storedEmail,
    row.envVar,
    row.shadowedEnvVar,
  ]);
}
function selectionIdentity(row: InstanceEntry | undefined): string {
  return `${destination(row)}\n${accessIdentity(row)}`;
}
function needsConfiguration(row: InstanceEntry | undefined): boolean {
  return !row || !!row.hidden || !row.baseUrl || /[{}]/.test(row.baseUrl);
}
function hasSavedAccess(row: InstanceEntry | undefined): boolean {
  return row?.activeSource === "store" || row?.activeSource === "oauth";
}

function resolvedAuthModes(row: ProviderDescriptor): string[] {
  return row.setup?.authModes ?? row.authModes ?? [];
}

function authCapabilities(row: ProviderDescriptor): string {
  const modes = resolvedAuthModes(row);
  const labels = modes.flatMap((mode) => {
    if (mode === "apiKey") return ["API key"];
    if (mode === "credentialJson") return ["Credential JSON"];
    if (mode === "oauth") return ["Account sign-in"];
    if (mode === "none") return ["No credential"];
    return [];
  });
  return labels.length > 0 ? labels.join(" or ") : "Host or endpoint access";
}

function ProviderChoice({
  row,
  disabled,
  onSelect,
}: {
  row: ProviderDescriptor;
  disabled: boolean;
  onSelect(row: ProviderDescriptor): void;
}) {
  const descriptionId = useId();
  const label = HELP[row.id]?.label || row.name || row.id;
  return (
    <ListRow
      label={label}
      meta={<span id={descriptionId}>{authCapabilities(row)}</span>}
      aria-label={label}
      aria-describedby={descriptionId}
      data-auth-modes={resolvedAuthModes(row).join(" ")}
      disabled={disabled}
      onSelect={() => onSelect(row)}
    />
  );
}

/**
 * One tappable ListRow per provider rather than a box per provider. The whole
 * row is the target and a real <button>, so it is a tab stop whose accessible
 * name is exactly the provider's label - the same label the list has always
 * printed (help metadata first, then the registry's name, then the id).
 */
function ProviderChoices({
  rows,
  disabled,
  onSelect,
}: {
  rows: ProviderDescriptor[];
  disabled: boolean;
  onSelect(row: ProviderDescriptor): void;
}) {
  return (
    <ul aria-label="Providers" className={styles.providers}>
      {rows.map((row) => (
        <li key={row.id}>
          <ProviderChoice row={row} disabled={disabled} onSelect={onSelect} />
        </li>
      ))}
    </ul>
  );
}

export function ProviderConnection(props: ProviderConnectionProps) {
  const store = useCredentialsStore();
  const [selected, setSelected] = useState<ProviderDescriptor | null>(null);
  const selectProvider = useCallback((row: ProviderDescriptor) => setSelected(row), []);
  const clearSelection = useCallback(() => setSelected(null), []);
  // The popular set is what a new account needs first; the rest of the
  // catalogue is one disclosure away. Both lists are the same widget, so the
  // reveal swaps one for the other rather than stacking them - the popular
  // providers are in the full catalogue already, and rendering them twice
  // would put two same-named buttons (and two same-named tab stops) on
  // screen.
  const [showAll, setShowAll] = useState(false);
  const [existingAccessOpen, setExistingAccessOpen] = useState(false);
  const [search, setSearch] = useState("");
  const errorRef = useRef<HTMLDivElement>(null);
  useConnectedEffect(store.fetch, [store.fetch]);
  useEffect(() => {
    if (store.error) errorRef.current?.focus();
  }, [store.error]);
  const popular = store.availableProviders.filter((row) => Object.hasOwn(HELP, row.id));
  // The search matches the same three fields it always has: the id, the
  // registry's display name, and the help label the row prints.
  const matching = store.availableProviders.filter((row) =>
    `${row.id} ${row.name ?? ""} ${HELP[row.id]?.label ?? ""}`.toLowerCase().includes(search.toLowerCase()),
  );
  const listingUnavailable = store.loading || !!store.error;
  if (selected)
    return <SelectedConnection key={selected.id} provider={selected} {...props} onChange={clearSelection} />;
  return (
    <Dialog open onClose={props.onClose} title="Connect a provider">
      <div className={styles.body}>
        <p>Connect one provider to get started. Add others later.</p>
        {store.loading && <Skeleton />}
        {store.error && (
          <InlineNotice
            tone="danger"
            tabIndex={-1}
            ref={errorRef}
            action={
              // fetch() rejects when there is no client; the error region this
              // button lives in is already the recovery affordance.
              <Button variant="secondary" onClick={() => void store.fetch().catch(() => {})}>
                Retry
              </Button>
            }
          >
            Providers could not be loaded.
          </InlineNotice>
        )}
        {!showAll && <ProviderChoices rows={popular} disabled={listingUnavailable} onSelect={selectProvider} />}
        {/* A disclosure, not a second mode: one focusable control opens the
            catalogue and closes it again, so there is no "back to popular"
            control to keep in sync with it. */}
        <Disclosure
          open={showAll}
          onOpenChange={setShowAll}
          summary="Show all providers"
          data-testid="show-all-providers"
        >
          <div className={styles.catalogue}>
            <FormRow label="Search providers" htmlFor="provider-search">
              <Input id="provider-search" type="search" value={search} onChange={(e) => setSearch(e.target.value)} />
            </FormRow>
            <ProviderChoices rows={matching} disabled={listingUnavailable} onSelect={selectProvider} />
          </div>
        </Disclosure>
        <Disclosure
          open={existingAccessOpen}
          onOpenChange={setExistingAccessOpen}
          summary="Already configured access on this host?"
        >
          <Button variant="quiet" onClick={props.onOpenSettings}>
            Manage existing connections
          </Button>
        </Disclosure>
        <Button variant="quiet" onClick={props.onClose}>
          Cancel
        </Button>
      </div>
    </Dialog>
  );
}

type Phase = "idle" | "saving" | "refreshing" | "checking" | "review" | "result";
// Transient failures are retried while the dialog is open: one second, then
// doubling to fifteen, and fifteen thereafter. The timer is the flow's own;
// there is no separate retry store.
const RETRY_DELAYS_MS = [1000, 2000, 4000, 8000, 15000];
function SelectedConnection({
  provider,
  onClose,
  onConnected,
  onOpenSettings,
  onChange,
}: ProviderConnectionProps & {
  provider: ProviderDescriptor;
  onChange(): void;
}) {
  const store = useCredentialsStore();
  const connection = useConnectionStore((state) => state.state);
  const [name, setName] = useState(provider.setup?.name ?? provider.id);
  const row = findSetup(name);
  const effectiveProvider = store.availableProviders.find((candidate) => candidate.id === row?.providerId) ?? provider;
  const [baseline, setBaseline] = useState(row);
  const resolvedProviderId = row?.providerId;
  const [draft, setDraft] = useState({ providerId: resolvedProviderId ?? provider.id, value: "" });
  // Keep the origin while create metadata is unresolved. Never expose or
  // submit its value for a different provider, even before the effect runs.
  const value = resolvedProviderId && resolvedProviderId !== draft.providerId ? "" : draft.value;
  useEffect(() => {
    if (!resolvedProviderId) return;
    setDraft((current) =>
      current.providerId === resolvedProviderId ? current : { providerId: resolvedProviderId, value: "" },
    );
  }, [resolvedProviderId]);
  const [missingCredential, setMissingCredential] = useState(false);
  const [host, setHost] = useState(false);
  const [saved, setSaved] = useState(hasSavedAccess(row));
  const [storedAccess, setStoredAccess] = useState(hasSavedAccess(row));
  const [configured, setConfigured] = useState(false);
  const [configure, setConfigure] = useState(false);
  const [oauth, setOAuth] = useState<OAuthEditor | null>(null);
  const [phase, setPhaseState] = useState<Phase>("idle");
  const phaseRef = useRef<Phase>("idle");
  const [error, setError] = useState<FormError<ConnectionErrorReason> | null>(null);
  const [result, setResult] = useState<AuthTestResponse | null>(null);
  const [review, setReview] = useState<InstanceEntry | null>(null);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const errorRef = useRef<HTMLDivElement>(null);
  const operation = useRef(0);
  // One retry timer owned by this flow. The attempt count paces it; both are
  // cleared whenever the flow leaves, its target moves, or the outcome is
  // terminal.
  const retryTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const retryAttempt = useRef(0);
  // Mirrors whether retryTimer is armed, for presentation only: a ref change
  // does not re-render. Cleared whenever the timer is cleared or fires.
  const [retryScheduled, setRetryScheduled] = useState(false);
  const retryRunRef = useRef<((token: number, expectedSource?: string) => Promise<void>) | null>(null);
  // What automatic work still owes the user, independent of the request token
  // that a reconnect makes obsolete: a read/check reconciliation anchored on
  // this flow's destination and expected credential source.
  const pendingRecovery = useRef<{ expectedSource?: string } | null>(null);
  // The source the acquisition actually produced ("store" for a saved key or
  // JSON, "oauth" for a sign-in, a resolved host source, or undefined). Manual
  // and automatic recovery compare against this, never against a generic
  // "saved" flag.
  const acquisitionSource = useRef<string | undefined>(
    row?.activeSource && row.activeSource !== "none" ? row.activeSource : undefined,
  );
  // The callback the check completes through. A ref keeps a check that began
  // under an earlier render from calling a stale owner callback.
  const onConnectedRef = useRef(onConnected);
  onConnectedRef.current = onConnected;
  // What the in-flight (or completed) check was taken against: the selected
  // row's destination and access identity at the moment check() began. An
  // acquisition's own confirmed revision becomes that anchor; a later listing
  // transition that moves it retires the check, whichever mark its read
  // carries.
  const checkAnchor = useRef<string | null>(null);
  const active = useEditorLifetime();
  const clearRetry = useCallback(() => {
    if (retryTimer.current !== null) {
      clearTimeout(retryTimer.current);
      retryTimer.current = null;
    }
    setRetryScheduled(false);
  }, []);
  useEffect(() => () => clearRetry(), [clearRetry]);
  const scheduleRetryRef = useRef<(expectedSource?: string) => void>(() => {});
  const scheduleRetry = useCallback(
    (expectedSource?: string) => {
      if (!active.current) return;
      clearRetry();
      const delay = RETRY_DELAYS_MS[Math.min(retryAttempt.current, RETRY_DELAYS_MS.length - 1)] ?? 15000;
      retryAttempt.current += 1;
      setRetryScheduled(true);
      retryTimer.current = setTimeout(() => {
        retryTimer.current = null;
        setRetryScheduled(false);
        if (!active.current) return;
        // A retry that comes due during a reconnect waits for the recovered
        // connection instead of spending its attempt on a request that cannot
        // land; transport recovery is what re-anchors the next read.
        if (connectionStore.getState().state !== "ready") {
          scheduleRetryRef.current(expectedSource);
          return;
        }
        // Every earlier attempt is obsolete, so the retry starts a fresh
        // operation and re-reads the listing before it contacts the provider.
        const next = ++operation.current;
        void retryRunRef.current?.(next, expectedSource);
      }, delay);
    },
    [active, clearRetry],
  );
  scheduleRetryRef.current = scheduleRetry;
  const modes = row?.authModes ?? effectiveProvider.authModes ?? [];
  const json = modes.includes("credentialJson");
  const storedMode = json || modes.includes("apiKey");
  const required = !!row?.credentialRequired && !host;
  const busy = phase === "saving" || phase === "refreshing" || phase === "checking";
  // Own-key: a schema-legal id like "constructor" would otherwise resolve to
  // Object.prototype.constructor here and render a destinationless
  // "Get an API key" anchor for a provider with no help metadata.
  const help = Object.hasOwn(HELP, effectiveProvider.id) ? HELP[effectiveProvider.id] : undefined;
  const unavailable = connection !== "ready" || store.loading;

  const setPhase = useCallback((next: Phase) => {
    phaseRef.current = next;
    setPhaseState(next);
  }, []);
  const invalidate = useCallback(
    // The change invalidates this draft's verdict - phase, result, review and
    // any open OAuth flow - and is said out loud: it is never this draft's own
    // save or check, so the user has to check again rather than trust a result
    // that was taken against the connection that is gone.
    (clearScheduled = true) => {
      if (clearScheduled) {
        clearRetry();
        retryAttempt.current = 0;
        pendingRecovery.current = null;
      }
      operation.current += 1;
      setPhase("idle");
      setResult(null);
      setReview(null);
      setOAuth(null);
      setError({ reason: "changed", message: CHANGED_ERROR });
    },
    [clearRetry, setPhase],
  );
  useEffect(() => {
    const unsubscribeConnection = connectionStore.subscribe((current, previous) => {
      if (current.client !== previous.client || current.state !== previous.state) {
        // A reconnect or replacement is not a different target: a scheduled
        // retry still re-anchors on the destination it named and reads through
        // whichever client is current when it fires. Pending read intent is not
        // a request token: the old request is obsolete, but the reconciliation
        // it owed the user is scheduled again over the recovered client.
        const replacement = current.client !== previous.client;
        const recovered = current.state === "ready" && previous.state !== "ready";
        invalidate(false);
        setConfigure(false);
        // A same-controller replacement is transport, not credential removal:
        // saved access and its source survive so manual recovery cannot write
        // the credential a second time. The destination comparison still
        // refuses a replacement controller's different listing.
        if (replacement) setConfigured(false);
        if (
          current.state === "ready" &&
          (replacement || recovered) &&
          pendingRecovery.current !== null &&
          retryTimer.current === null
        ) {
          retryAttempt.current = 0;
          scheduleRetry(pendingRecovery.current.expectedSource);
        }
      }
    });
    const unsubscribeStore = credentialsStore.subscribe((current, previous) => {
      const selected = findSetupIn(current, name);
      // A check is evidence about the one row it anchored on, not about the
      // listing as a whole. Another provider's update - or any read that only
      // replaces arrays - must not retract it. The comparison deliberately
      // ignores list loading/error flags and row fields that do not describe
      // this target's access. It runs on self-marked transitions too: a
      // delayed acquisition refresh that carries a different revision is stale
      // evidence even though foreignListingChange would not report it.
      if (phaseRef.current === "checking" || phaseRef.current === "result") {
        if (selectionIdentity(selected) !== checkAnchor.current) invalidate();
        return;
      }
      // The review names the row it showed. It survives the same unrelated
      // replacements and retires when that row itself moves.
      if (phaseRef.current === "review") {
        if (selectionIdentity(selected) !== selectionIdentity(review ?? undefined)) invalidate();
        return;
      }
      if (!foreignListingChange(current, previous)) return;
      // A mutation or sign-in start has no check to anchor on yet, so it is
      // anchored on the destination it showed: an unrelated provider's update,
      // another row's replacement or a read's loading transition does not
      // retire an acquisition the user already confirmed, while an actual move
      // of the selected destination still does. The acquisition's own expected
      // source/revision change is not a destination change. An open OAuth
      // editor follows the same rule.
      if ((phaseRef.current === "saving" || oauth !== null) && destination(selected) !== destination(baseline)) {
        invalidate();
      }
    });
    return () => {
      unsubscribeConnection();
      unsubscribeStore();
    };
  }, [invalidate, oauth, name, baseline, review, scheduleRetry]);
  const errorMessage = error?.message;
  useEffect(() => {
    if (errorMessage) errorRef.current?.focus();
  }, [errorMessage]);

  function leave(callback: () => void) {
    retireForIntentChange();
    callback();
  }
  function current(token: number) {
    return active.current && operation.current === token && connectionStore.getState().state === "ready";
  }
  function changeValue(next: string) {
    clearRetry();
    retryAttempt.current = 0;
    pendingRecovery.current = null;
    acquisitionSource.current = undefined;
    operation.current += 1;
    setDraft({ providerId: resolvedProviderId ?? effectiveProvider.id, value: next });
    setMissingCredential(false);
    setSaved(false);
    setResult(null);
    setReview(null);
    setPhase("idle");
    setError(null);
  }

  // A user-intent change away from the checked connection retires every
  // automatic read for it; the replacement editor or access mode owns what
  // happens next.
  function retireForIntentChange() {
    clearRetry();
    retryAttempt.current = 0;
    pendingRecovery.current = null;
    operation.current += 1;
  }

  async function check(token: number, target: InstanceEntry) {
    if (!current(token)) return;
    clearRetry();
    pendingRecovery.current = { expectedSource: target.activeSource };
    // A row whose destination the listing could not fingerprint has no
    // assertion to carry, and an unasserted probe dials whatever the name
    // resolves to now: refuse it before the call.
    if (fingerprintUnavailable(target)) {
      pendingRecovery.current = null;
      setPhase("idle");
      setError({ reason: "check-fingerprint-unavailable", message: FINGERPRINT_UNAVAILABLE_TEST_MESSAGE });
      return;
    }
    checkAnchor.current = selectionIdentity(target);
    setPhase("checking");
    setError(null);
    setReview(null);
    try {
      // The probe carries the destination this flow reviewed. The hub validates
      // it against the configuration the probe will dial, so a name re-pointed
      // between the refresh above and this call cannot receive the stored
      // credential.
      const response = safeCredentialTestResult(
        target.name,
        await credentialsStore.getState().testCredentials(target.name, target.endpointFingerprint),
      );
      if (!current(token)) return;
      if (response.status === "success") {
        // Retire the operation before telling the owner: whatever the owner
        // does next - including a store notification - cannot complete twice.
        pendingRecovery.current = null;
        operation.current += 1;
        onConnectedRef.current(target.name);
        return;
      }
      setResult(response);
      setPhase("result");
      setError({ reason: "check-failed", message: response.message });
      // Only an unreachable endpoint is transient evidence. A rejected
      // credential, a missing credential and broken configuration are answers
      // about the connection, and retrying them cannot change that.
      if (response.status === "endpoint_failure") scheduleRetry(target.activeSource);
      else pendingRecovery.current = null;
    } catch (err) {
      if (!current(token)) return;
      // A refused assertion means the destination moved under the check, and a
      // refusal from a listing the store no longer trusts (the connection was
      // replaced) means the destination this check anchored on was read by a
      // connection that is gone - the same change submit() reports. Reset the
      // flow and re-anchor rather than dress the refusal up as an endpoint
      // failure, which no retry against this listing would fix.
      const replaced = isStaleListingRefusal(err);
      if (isEndpointConflict(err) || replaced) {
        await recoverChangedEndpoint(
          replaced
            ? { reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR }
            : { reason: "endpoint-changed", message: ENDPOINT_MOVED_ERROR },
        );
        return;
      }
      if (isAccessChangedConflict(err)) {
        // The credential this check sent was replaced by a hub-observed write
        // while the provider was answering. The replacement is already saved;
        // the paced retry rereads the listing and checks it without saving
        // again. Clear any endpoint verdict: this endpoint did not move.
        setResult(null);
        setPhase("result");
        setError({ reason: "access-changed", message: ACCESS_CHANGED_ERROR });
        scheduleRetry(target.activeSource);
        return;
      }
      const response = safeCredentialTestResult(target.name, {
        provider: target.name,
        status: "endpoint_failure",
        message: "",
      });
      setResult(response);
      setPhase("result");
      setError({ reason: "check-failed", message: response.message });
      scheduleRetry(target.activeSource);
    }
  }

  // A refused endpoint assertion means the name moved between this flow's check
  // and the write - or, when the store refused the write, that the listing this
  // flow anchored on was read by a connection that is gone - so the draft was
  // typed for a destination nobody can vouch for: drop it, re-anchor to what
  // the listing now holds, and say why - rather than leave the user with a save
  // that silently went somewhere else. changed names the change, because
  // the two differ in what the user has to check and the caller knows which one
  // refused it. The re-read is the flow's own, so it carries the flow's own
  // marker (fetchSelf): this refusal is the outcome the user gets, not a reason
  // for the invalidation watch to reset the flow underneath it. changeValue
  // below settles the flow either way.
  async function recoverChangedEndpoint(changed: FormError<ConnectionErrorReason>) {
    try {
      await credentialsStore.getState().fetchSelf();
    } catch {
      // Best-effort: the alert below is the answer either way.
    }
    changeValue("");
    setBaseline(findSetup(name));
    setError(changed);
  }

  async function refreshAndCheck(token: number, expectedSource?: string) {
    setPhase("refreshing");
    setError(null);
    pendingRecovery.current = { expectedSource };
    // The listing this refresh waits for can be superseded by the debounced
    // refetch evener/auth/updated schedules (250ms after the save), and the
    // store drops a superseded response without applying it. The read's own
    // applied verdict separates "the listing really did not move" from "my
    // response lost the race": a read that did not apply gets one more ask,
    // and the flow reports a failure only when that one did not apply either.
    // (This replaces the old listing-identity heuristic, which called the
    // listing unchanged when EITHER array kept its identity.)
    let applied = false;
    try {
      applied = await credentialsStore.getState().fetchSelf();
      if (!current(token)) return;
      if (!applied) {
        applied = await credentialsStore.getState().fetchSelf();
        if (!current(token)) return;
      }
    } catch {
      // fetch() rejects when the client is gone (credentials.ts's
      // requireClient contract), so a dropped connection is reported like any
      // other refresh failure instead of escaping as an unhandled rejection.
      if (!current(token)) return;
      setPhase("idle");
      setError({ reason: "refresh-failed", message: REFRESH_FAILED_ERROR });
      scheduleRetry(expectedSource);
      return;
    }
    const fresh = credentialsStore.getState();
    const target = findSetup(name);
    if (!applied || fresh.loading || fresh.error) {
      setPhase("idle");
      setError({ reason: "refresh-failed", message: REFRESH_FAILED_ERROR });
      scheduleRetry(expectedSource);
      return;
    }
    if (needsConfiguration(target)) {
      clearRetry();
      retryAttempt.current = 0;
      pendingRecovery.current = null;
      setPhase("idle");
      setError({
        reason: "needs-configuration",
        message: "This connection needs configuration before it can be checked. Open the full editor.",
      });
      return;
    }
    // needsConfiguration already covers a missing target, so this is the type
    // narrowing. If it is ever reached, fail like any other failed refresh
    // rather than returning with the phase still "refreshing".
    if (!target) {
      setPhase("idle");
      setError({ reason: "refresh-failed", message: REFRESH_FAILED_ERROR });
      scheduleRetry(expectedSource);
      return;
    }
    if (
      destination(baseline) !== destination(target) ||
      target.activeSource !== (expectedSource ?? baseline?.activeSource)
    ) {
      pendingRecovery.current = null;
      setReview(target);
      setPhase("review");
      return;
    }
    await check(token, target);
  }
  // A retry re-runs the flow's own refresh-and-check, never the credential
  // mutation: the value on screen was already saved by the attempt that failed.
  retryRunRef.current = refreshAndCheck;

  async function submit() {
    if (busy || unavailable || needsConfiguration(row)) return;
    if (!saved && storedMode && required && !value.trim()) {
      setMissingCredential(true);
      document.getElementById("provider-credential")?.focus();
      return;
    }
    clearRetry();
    retryAttempt.current = 0;
    pendingRecovery.current = null;
    const token = ++operation.current;
    setError(null);
    setResult(null);
    setReview(null);
    if (!saved && storedMode && !host && value.trim()) {
      // A row with a destination but no fingerprint is one the hub cannot key
      // right now, and an empty assertion is accepted rather than validated: the
      // write would land on whatever the name resolves to when it arrives, and
      // the check that follows would refuse, leaving a secret saved that was
      // never tested. Refuse the write here, as the instance dialogs do.
      if (fingerprintUnavailable(row)) {
        setPhase("idle");
        setError({ reason: "fingerprint-unavailable", message: FINGERPRINT_UNAVAILABLE_ERROR });
        return;
      }
      // The draft was typed against the connection this flow anchored on, and a
      // credential only ever goes to an endpoint the user was shown. If the
      // name resolves to a different destination now - another client edited
      // it, or removed and recreated it under the same name - saving would send
      // this key somewhere the user never reviewed. Report the change, drop the
      // draft, and re-anchor so a re-entered key lands on the destination now
      // on screen; the check path's own review step covers the rest.
      if (row && destination(row) !== destination(baseline)) {
        changeValue("");
        setBaseline(row);
        setError({ reason: "endpoint-changed", message: ENDPOINT_MOVED_ERROR });
        return;
      }
      setPhase("saving");
      try {
        const state = credentialsStore.getState();
        // The endpoint this flow showed the user is asserted on the write: the
        // hub compares it where the write lands, which is what closes the gap
        // between the check above and the RPC.
        const asserted = row?.endpointFingerprint;
        if (json) await state.setCredentialJson(name, value.trim(), asserted);
        else await state.setApiKey(name, value.trim(), asserted);
        if (!current(token)) return;
        setSaved(true);
        setStoredAccess(true);
        acquisitionSource.current = "store";
      } catch (err) {
        if (!current(token)) return;
        // The same two changes, and the same recovery: the hub refused the
        // assertion this flow carried, or the store refused the write because
        // the listing it was anchored on belongs to a connection that is gone.
        const replaced = isStaleListingRefusal(err);
        if (isEndpointConflict(err) || replaced) {
          await recoverChangedEndpoint(
            replaced
              ? { reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR }
              : { reason: "endpoint-changed", message: ENDPOINT_MOVED_ERROR },
          );
          return;
        }
        setPhase("idle");
        pendingRecovery.current = null;
        setError({
          reason: "save-failed",
          message: "Credential could not be saved. Your draft is retained; retry saving.",
        });
        return;
      }
    }
    await refreshAndCheck(token, acquisitionSource.current);
  }

  async function signIn() {
    clearRetry();
    retryAttempt.current = 0;
    pendingRecovery.current = null;
    const token = ++operation.current;
    setPhase("saving");
    setError(null);
    setResult(null);
    try {
      const editor = await startOAuthFlow(name, () => current(token));
      if (current(token)) {
        setOAuth(editor);
        setPhase("idle");
      }
    } catch (err) {
      if (current(token)) {
        setPhase("idle");
        // A start the store refused because the rows on screen belong to a
        // replaced connection is not a failed start - nothing was sent. Say
        // what changed and ask for this connection's listing, whose arrival is
        // what makes the retry land.
        if (isStaleListingRefusal(err)) {
          pendingRecovery.current = null;
          setError({ reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR });
          void credentialsStore
            .getState()
            .fetch()
            .catch(() => {});
          return;
        }
        pendingRecovery.current = null;
        setError({ reason: "sign-in-failed", message: "Sign-in could not be started. Try again." });
      }
    }
  }
  function closeOAuth() {
    pendingRecovery.current = null;
    operation.current += 1;
    setOAuth(null);
    setPhase("idle");
  }
  function signedIn() {
    setOAuth(null);
    // A completed sign-in has stored access just as a saved key does; its
    // dismissal wording and the retained access follow that fact even before
    // the check returns.
    setSaved(true);
    setStoredAccess(true);
    acquisitionSource.current = "oauth";
    const token = ++operation.current;
    void refreshAndCheck(token, "oauth");
  }
  // The OAuth child confirms authorization before its first listing read. That
  // read is interruptible: a transport reconnect unmounts the child, so the
  // parent records the acquired source and its automatic-recovery ownership
  // here, before the read, rather than waiting for the child's success callback
  // that the unmount would drop.
  const oauthAuthorized = useCallback(() => {
    setSaved(true);
    setStoredAccess(true);
    acquisitionSource.current = "oauth";
    pendingRecovery.current = { expectedSource: "oauth" };
  }, []);
  // DeviceCodeDialog's polling effect owns an in-flight refresh and depends
  // on this callback. Keep its identity stable while using current setup.
  const signedInRef = useRef(signedIn);
  signedInRef.current = signedIn;
  const completeOAuth = useCallback(() => signedInRef.current(), []);

  // A created instance becomes the flow's connection. An unconfirmed create
  // (the listing never showed the row) reaches the same state without a
  // success claim: findSetup finds nothing, so the flow shows its not-ready
  // reload recovery instead of a working connection.
  function adoptCreated(createdName: string): void {
    clearRetry();
    retryAttempt.current = 0;
    pendingRecovery.current = null;
    acquisitionSource.current = undefined;
    operation.current += 1;
    const created = findSetup(createdName);
    setName(createdName);
    setBaseline(created);
    setConfigured(true);
    setConfigure(false);
    setSaved(false);
    setStoredAccess(hasSavedAccess(created));
    // A value typed for a different destination must not be submitted to the
    // adopted connection, the reason the removal effect makes this same reset.
    // Keying that on providerId alone is not enough: one provider can host two
    // endpoints, and a recovered row can carry the provider's own setup entry
    // while the flow was on another. The listing's digest is what says whether
    // the destination is the one the value was typed against, including the
    // parts the displayed URL cannot show. Two missing fingerprints are not
    // such evidence: an unkeyable hub or a row the listing has not resolved
    // yet leaves nothing to compare, so the draft is cleared unless both sides
    // carry a non-empty, matching fingerprint.
    const createdFingerprint = created?.endpointFingerprint;
    const baselineFingerprint = baseline?.endpointFingerprint;
    if (!createdFingerprint || !baselineFingerprint || createdFingerprint !== baselineFingerprint) {
      setDraft({ providerId: provider.id, value: "" });
    }
    // The adopted instance is a different connection: a host-access choice
    // made for the previous one does not describe it, and inheriting it would
    // skip this instance's credential submission entirely.
    setHost(false);
    setMissingCredential(false);
    setError(null);
    setReview(null);
    setResult(null);
    setPhase("idle");
  }

  async function reloadCreated() {
    clearRetry();
    retryAttempt.current = 0;
    pendingRecovery.current = null;
    const token = ++operation.current;
    setPhase("refreshing");
    setError(null);
    // The listing this reload waits for can be superseded by a concurrent
    // request (a write or another view's read), and the store drops a
    // superseded response without applying it. The read's own applied verdict
    // is what separates "the listing does not carry the connection yet" from
    // "my response lost the race": a read that did not apply says nothing
    // about the listing, so baselining from it can anchor the flow on whatever
    // same-named row another connection left in the store. One retry, then the
    // recovery message.
    let applied = false;
    try {
      applied = await credentialsStore.getState().fetch();
      if (!current(token)) return;
      if (!applied) {
        applied = await credentialsStore.getState().fetch();
        if (!current(token)) return;
      }
    } catch {
      // Same dropped-connection case as refreshAndCheck: reusing the reload's
      // own recovery message keeps the user on an actionable path.
      if (!current(token)) return;
      setPhase("idle");
      setError({ reason: "reload-failed", message: RELOAD_FAILED_ERROR });
      return;
    }
    if (!current(token)) return;
    setPhase("idle");
    const state = credentialsStore.getState();
    const created = findSetup(name);
    if (!applied || state.loading || state.error || !created) {
      setError({ reason: "reload-failed", message: RELOAD_FAILED_ERROR });
    } else setBaseline(created);
  }

  function enterConfiguration() {
    retireForIntentChange();
    setResult(null);
    setReview(null);
    setError(null);
    setPhase("idle");
    setConfigure(true);
  }

  // A step of the flow replaces this dialog with the editor it opens - the
  // configuration form, or the OAuth flow's own dialog - so the flow never puts
  // two dialogs, two focus traps or a hidden poller on screen at once.
  if (configure)
    return (
      <AddInstanceDialog
        availableProviders={store.availableProviders}
        initialBase={effectiveProvider.id}
        onCancel={() => setConfigure(false)}
        onSuccess={adoptCreated}
        onUnconfirmedCreate={adoptCreated}
      />
    );
  if (oauth?.kind === "device")
    return (
      <DeviceCodeDialog
        key={oauth.flowId}
        name={oauth.name}
        flowId={oauth.flowId}
        userCode={oauth.userCode}
        verificationUrl={oauth.verificationUrl}
        intervalSeconds={oauth.intervalSeconds}
        onCancel={closeOAuth}
        onAuthorized={oauthAuthorized}
        onSuccess={completeOAuth}
        onRestart={() => void signIn()}
      />
    );
  if (oauth?.kind === "oauth-redirect")
    return (
      <OAuthRedirectDialog
        name={oauth.name}
        flowId={oauth.flowId}
        authUrl={oauth.authUrl}
        onCancel={closeOAuth}
        onAuthorized={oauthAuthorized}
        onSuccess={completeOAuth}
      />
    );

  return (
    <Dialog
      open
      onClose={() => leave(onClose)}
      title={`Connect ${help?.label || effectiveProvider.name || effectiveProvider.id}`}
    >
      <form
        className={styles.body}
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        {configured && (
          <p data-testid="connection-configured">
            Connection settings saved as {name}. Add access below; retrying will use this connection.
          </p>
        )}
        {saved && (
          <p data-testid="credential-saved">
            Credential saved for {name}. {row && activeSourceLabel(row)}
          </p>
        )}
        {help && (
          <>
            {modes.includes("apiKey") && <OpenButton href={help.keyUrl} word="Get an API key" />}
            <p data-testid="billing-note">{help.billing}</p>
          </>
        )}
        {row && (
          <Card variant="outlined">
            <div className={styles.review}>
              <p>{row.baseUrl}</p>
              <p>{activeSourceLabel(row)}</p>
            </div>
          </Card>
        )}
        {configured && needsConfiguration(row) ? (
          <>
            <p>Settings were saved, but this connection is not ready. Reload it or repair it in the full editor.</p>
            <Button disabled={unavailable || busy} onClick={() => void reloadCreated()}>
              Reload connection
            </Button>
            <Button variant="secondary" onClick={() => leave(onOpenSettings)}>
              Open full connection editor
            </Button>
          </>
        ) : needsConfiguration(row) ? (
          <>
            <p>
              Configure the endpoint and required template variables before adding access.{" "}
              {Object.values(effectiveProvider.vars ?? {}).join(", ")}
            </p>
            <Button disabled={store.writesRefused || unavailable} onClick={() => enterConfiguration()}>
              Configure provider
            </Button>
            {store.writesRefused && (
              <p>Provider settings are read-only. Repair the host configuration to add a connection.</p>
            )}
          </>
        ) : (
          <>
            {storedMode && !host && (
              <FormRow
                label={json ? "Credential JSON" : "API key"}
                htmlFor="provider-credential"
                error={
                  missingCredential
                    ? "Enter a credential, or choose existing host access in Advanced settings."
                    : undefined
                }
              >
                <Input
                  id="provider-credential"
                  type="password"
                  autoComplete="off"
                  aria-describedby={missingCredential ? "provider-credential-error" : undefined}
                  required={required}
                  disabled={busy}
                  value={value}
                  onChange={(event) => changeValue(event.target.value)}
                />
              </FormRow>
            )}
            {json && (
              <p>
                Paste a service-account key or application_default_credentials.json. Existing Application Default
                Credentials can also be checked without replacing them.
              </p>
            )}
            {!row?.credentialRequired && (
              <p>
                This endpoint accepts optional or no credentials. Leaving the key empty retains the access resolved on
                the Evener host.
              </p>
            )}
            {/* While an automatic retry for saved access is pending, another
                acquisition is not what the flow needs: the stored access is
                already there and recovery is already scheduled. */}
            {modes.includes("oauth") && !busy && !retryScheduled && (
              <Button disabled={busy || unavailable} onClick={() => void signIn()}>
                Sign in
              </Button>
            )}
            <p>The check requests only the model list. It does not send a prompt or generate a response.</p>
            {review ? (
              <Card variant="outlined" data-testid="access-review">
                <div className={styles.review}>
                  <p>Review changed access before contacting the provider.</p>
                  <p data-destination="previous">Previous destination: {baseline?.baseUrl ?? "Not configured"}</p>
                  <p data-destination="current">Current destination: {review.baseUrl}</p>
                  <p>{activeSourceLabel(review)}</p>
                  <Button
                    disabled={unavailable}
                    onClick={() => {
                      acquisitionSource.current = review.activeSource;
                      setBaseline(review);
                      void check(++operation.current, review);
                    }}
                  >
                    Use reviewed access and check
                  </Button>
                </div>
              </Card>
            ) : (
              <Button type="submit" disabled={busy || unavailable}>
                {busy
                  ? phase === "saving"
                    ? "Saving…"
                    : phase === "refreshing"
                      ? "Refreshing access…"
                      : "Checking model list…"
                  : saved || result
                    ? "Retry check"
                    : storedMode && !host && (required || value.trim())
                      ? "Save and check"
                      : "Check connection"}
              </Button>
            )}
            {result?.status === "unsupported" && (
              <Button variant="secondary" onClick={() => leave(() => onConnected(name))}>
                Continue without verification
              </Button>
            )}
          </>
        )}
        {error && (
          <InlineNotice tone="danger" tabIndex={-1} ref={errorRef} data-reason={error.reason}>
            {error.message}
          </InlineNotice>
        )}
        {retryScheduled && storedAccess && (
          <InlineNotice live="status" data-retry="scheduled">
            Access is saved. An automatic retry is scheduled; you can also retry now.
          </InlineNotice>
        )}
        {connection !== "ready" && (
          <p role="status" data-state="reconnecting">
            Waiting for the Evener host to reconnect.
          </p>
        )}
        <Disclosure open={advancedOpen} onOpenChange={setAdvancedOpen} summary="Advanced settings">
          <div className={styles.actions}>
            {storedMode && (
              <Button
                variant="quiet"
                disabled={busy}
                onClick={() => {
                  retireForIntentChange();
                  acquisitionSource.current = undefined;
                  setHost(!host);
                  setResult(null);
                  setReview(null);
                  setPhase("idle");
                  setError(null);
                }}
              >
                {host ? "Use a new credential" : "Use existing host access"}
              </Button>
            )}
            <Button variant="secondary" disabled={busy || store.writesRefused} onClick={() => enterConfiguration()}>
              Configure another instance
            </Button>
            <Button variant="quiet" onClick={() => leave(onOpenSettings)}>
              Open full connection editor
            </Button>
          </div>
          <p>
            Existing host access keeps whatever authentication the host resolves; it does not disable authentication.
          </p>
        </Disclosure>
        <div className={styles.actions}>
          <Button variant="quiet" onClick={() => leave(onChange)}>
            Change provider
          </Button>
          <Button variant="quiet" onClick={() => leave(onClose)}>
            {storedAccess ? "Close, access stays saved" : "Cancel"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
