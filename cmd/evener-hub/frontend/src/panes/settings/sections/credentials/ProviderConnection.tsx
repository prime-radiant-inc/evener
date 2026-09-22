import type { AuthTestResponse, InstanceEntry, ProviderDescriptor } from "@evener/appwire-client";
import {
  activeSourceLabel,
  CONNECTION_REPLACED_ERROR,
  FINGERPRINT_UNAVAILABLE_ERROR,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  fingerprintUnavailable,
  isEndpointConflict,
  safeCredentialTestResult,
} from "@evener/appwire-client";
import { useCallback, useEffect, useRef, useState } from "react";
import { connectionStore, useConnectionStore } from "../../../../stores/connection";
import {
  credentialsStore,
  foreignListingChange,
  isStaleListingRefusal,
  useCredentialsStore,
} from "../../../../stores/credentials";
import { Button, Chevron, Dialog, Disclosure, FormRow, Input, Skeleton } from "../../../../widgets";
import { useConnectedEffect } from "../useConnectedEffect";
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

function findSetup(name: string): InstanceEntry | undefined {
  const store = credentialsStore.getState();
  return (
    store.instances.find((row) => row.name === name) ??
    store.availableProviders.find((row) => row.setup?.name === name)?.setup
  );
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
function needsConfiguration(row: InstanceEntry | undefined): boolean {
  return !row || !!row.hidden || !row.baseUrl || /[{}]/.test(row.baseUrl);
}

/**
 * One tappable row per provider, drawn with the app's list-of-selectable-
 * things row (panes/settings/sections/marketplacesPlugins/InstalledSection.tsx
 * and MarketplacesSection.tsx: a full-width button carrying a surface, an
 * edge and a trailing chevron) rather than a box per provider. The whole row
 * is the target and a real <button>, so it is a tab stop whose accessible
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
          <button type="button" className={styles.provider} disabled={disabled} onClick={() => onSelect(row)}>
            <span className={styles.providerLabel}>{HELP[row.id]?.label || row.name || row.id}</span>
            <span className={styles.providerChevron} aria-hidden="true">
              <Chevron direction="right" />
            </span>
          </button>
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
          <div role="alert" tabIndex={-1} ref={errorRef}>
            Providers could not be loaded.{" "}
            {/* fetch() rejects when there is no client; the error region this
                button lives in is already the recovery affordance. */}
            <Button variant="secondary" onClick={() => void store.fetch().catch(() => {})}>
              Retry
            </Button>
          </div>
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
        <details>
          <summary>Already configured access on this host?</summary>
          <Button variant="quiet" onClick={props.onOpenSettings}>
            Manage existing connections
          </Button>
        </details>
        <Button variant="quiet" onClick={props.onClose}>
          Cancel
        </Button>
      </div>
    </Dialog>
  );
}

type Phase = "idle" | "saving" | "refreshing" | "checking" | "review" | "result";
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
  const [saved, setSaved] = useState(false);
  const [configured, setConfigured] = useState(false);
  const [configure, setConfigure] = useState(false);
  const [oauth, setOAuth] = useState<OAuthEditor | null>(null);
  const [phase, setPhaseState] = useState<Phase>("idle");
  const phaseRef = useRef<Phase>("idle");
  const [error, setError] = useState("");
  const [result, setResult] = useState<AuthTestResponse | null>(null);
  const [review, setReview] = useState<InstanceEntry | null>(null);
  const errorRef = useRef<HTMLDivElement>(null);
  const operation = useRef(0);
  const active = useEditorLifetime();
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
    (message = "Connection or configuration changed. Review access and check again.") => {
      operation.current += 1;
      setPhase("idle");
      setResult(null);
      setReview(null);
      setOAuth(null);
      setError(message);
    },
    [setPhase],
  );
  useEffect(() => {
    const unsubscribeConnection = connectionStore.subscribe((current, previous) => {
      if (current.client !== previous.client || current.state !== previous.state) {
        invalidate();
        setConfigure(false);
        if (current.client !== previous.client) {
          setSaved(false);
          setConfigured(false);
        }
      }
    });
    const unsubscribeStore = credentialsStore.subscribe((current, previous) => {
      // The store schedules its own listing refresh the moment this client's
      // auth mutation succeeds; that refresh is this flow's own change, not an
      // unrelated one, and it lands while the flow sits in checking/result.
      // foreignListingChange excludes it and names every other change
      // (another client's edit, a reconnect's restore, a failed read's error
      // field); which phases of this flow it invalidates stays the flow's rule.
      if (!foreignListingChange(current, previous)) return;
      if (
        ["saving", "checking", "result", "review"].includes(phaseRef.current) ||
        (oauth && destination(findSetup(name)) !== destination(baseline))
      )
        invalidate();
    });
    return () => {
      unsubscribeConnection();
      unsubscribeStore();
    };
  }, [invalidate, oauth, name, baseline]);
  useEffect(() => {
    if (error) errorRef.current?.focus();
  }, [error]);

  function leave(callback: () => void) {
    operation.current += 1;
    callback();
  }
  function current(token: number) {
    return active.current && operation.current === token && connectionStore.getState().state === "ready";
  }
  function changeValue(next: string) {
    operation.current += 1;
    setDraft({ providerId: resolvedProviderId ?? effectiveProvider.id, value: next });
    setMissingCredential(false);
    setSaved(false);
    setResult(null);
    setReview(null);
    setPhase("idle");
    setError("");
  }

  async function check(token: number, target: InstanceEntry) {
    if (!current(token)) return;
    // A row whose destination the listing could not fingerprint has no
    // assertion to carry, and an unasserted probe dials whatever the name
    // resolves to now: refuse it before the call.
    if (fingerprintUnavailable(target)) {
      setPhase("idle");
      setError(FINGERPRINT_UNAVAILABLE_TEST_MESSAGE);
      return;
    }
    setPhase("checking");
    setError("");
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
      setResult(response);
      setPhase("result");
      if (response.status !== "success") setError(response.message);
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
        await recoverChangedEndpoint(replaced ? CONNECTION_REPLACED_ERROR : ENDPOINT_MOVED_ERROR);
        return;
      }
      const response = safeCredentialTestResult(target.name, {
        provider: target.name,
        status: "endpoint_failure",
        message: "",
      });
      setResult(response);
      setPhase("result");
      setError(response.message);
    }
  }

  // A refused endpoint assertion means the name moved between this flow's check
  // and the write - or, when the store refused the write, that the listing this
  // flow anchored on was read by a connection that is gone - so the draft was
  // typed for a destination nobody can vouch for: drop it, re-anchor to what
  // the listing now holds, and say why - rather than leave the user with a save
  // that silently went somewhere else. changedMessage names the change, because
  // the two differ in what the user has to check and the caller knows which one
  // refused it. The re-read is the flow's own, so it carries the flow's own
  // marker (fetchSelf): this refusal is the outcome the user gets, not a reason
  // for the invalidation watch to reset the flow underneath it. changeValue
  // below settles the flow either way.
  async function recoverChangedEndpoint(changedMessage: string) {
    try {
      await credentialsStore.getState().fetchSelf();
    } catch {
      // Best-effort: the alert below is the answer either way.
    }
    changeValue("");
    setBaseline(findSetup(name));
    setError(changedMessage);
  }

  async function refreshAndCheck(token: number, expectedSource?: string) {
    setPhase("refreshing");
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
      setError("Access could not be refreshed. Your saved credential is retained; retry the check.");
      return;
    }
    const fresh = credentialsStore.getState();
    const target = findSetup(name);
    if (!applied || fresh.loading || fresh.error) {
      setPhase("idle");
      setError("Access could not be refreshed. Your saved credential is retained; retry the check.");
      return;
    }
    if (needsConfiguration(target)) {
      setPhase("idle");
      setError("This connection needs configuration before it can be checked. Open the full editor.");
      return;
    }
    // needsConfiguration already covers a missing target, so this is the type
    // narrowing. If it is ever reached, fail like any other failed refresh
    // rather than returning with the phase still "refreshing".
    if (!target) {
      setPhase("idle");
      setError("Access could not be refreshed. Your saved credential is retained; retry the check.");
      return;
    }
    if (
      destination(baseline) !== destination(target) ||
      target.activeSource !== (expectedSource ?? baseline?.activeSource)
    ) {
      setReview(target);
      setPhase("review");
      return;
    }
    await check(token, target);
  }

  async function submit() {
    if (busy || unavailable || needsConfiguration(row)) return;
    if (!saved && storedMode && required && !value.trim()) {
      setMissingCredential(true);
      document.getElementById("provider-credential")?.focus();
      return;
    }
    const token = ++operation.current;
    setError("");
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
        setError(FINGERPRINT_UNAVAILABLE_ERROR);
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
        setError(ENDPOINT_MOVED_ERROR);
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
      } catch (err) {
        if (!current(token)) return;
        // The same two changes, and the same recovery: the hub refused the
        // assertion this flow carried, or the store refused the write because
        // the listing it was anchored on belongs to a connection that is gone.
        const replaced = isStaleListingRefusal(err);
        if (isEndpointConflict(err) || replaced) {
          await recoverChangedEndpoint(replaced ? CONNECTION_REPLACED_ERROR : ENDPOINT_MOVED_ERROR);
          return;
        }
        setPhase("idle");
        setError("Credential could not be saved. Your draft is retained; retry saving.");
        return;
      }
      await refreshAndCheck(token, "store");
    } else await refreshAndCheck(token, saved ? "store" : undefined);
  }

  async function signIn() {
    const token = ++operation.current;
    setPhase("saving");
    setError("");
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
          setError(CONNECTION_REPLACED_ERROR);
          void credentialsStore
            .getState()
            .fetch()
            .catch(() => {});
          return;
        }
        setError("Sign-in could not be started. Try again.");
      }
    }
  }
  function closeOAuth() {
    operation.current += 1;
    setOAuth(null);
    setPhase("idle");
  }
  function signedIn() {
    setOAuth(null);
    const token = ++operation.current;
    void refreshAndCheck(token, "oauth");
  }
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
    operation.current += 1;
    const created = findSetup(createdName);
    setName(createdName);
    setBaseline(created);
    setConfigured(true);
    setConfigure(false);
    setSaved(false);
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
    setError("");
    setReview(null);
    setResult(null);
    setPhase("idle");
  }

  async function reloadCreated() {
    const token = ++operation.current;
    setPhase("refreshing");
    setError("");
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
      setError("The saved connection could not be loaded. Reload it or open the full editor; do not create it again.");
      return;
    }
    if (!current(token)) return;
    setPhase("idle");
    const state = credentialsStore.getState();
    const created = findSetup(name);
    if (!applied || state.loading || state.error || !created) {
      setError("The saved connection could not be loaded. Reload it or open the full editor; do not create it again.");
    } else setBaseline(created);
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
        {configured && <p>Connection settings saved as {name}. Add access below; retrying will use this connection.</p>}
        {saved && (
          <p>
            Credential saved for {name}. {row && activeSourceLabel(row)}
          </p>
        )}
        {help && (
          <>
            {modes.includes("apiKey") && (
              <a href={help.keyUrl} target="_blank" rel="noreferrer">
                Get an API key
              </a>
            )}
            <p>{help.billing}</p>
          </>
        )}
        {row && (
          <div className={styles.review}>
            <p>{row.baseUrl}</p>
            <p>{activeSourceLabel(row)}</p>
          </div>
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
            <Button disabled={store.writesRefused || unavailable} onClick={() => setConfigure(true)}>
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
            {modes.includes("oauth") && (
              <Button disabled={busy || unavailable} onClick={() => void signIn()}>
                Sign in
              </Button>
            )}
            <p>The check requests only the model list. It does not send a prompt or generate a response.</p>
            {review ? (
              <div className={styles.review}>
                <p>Review changed access before contacting the provider.</p>
                <p>Previous destination: {baseline?.baseUrl ?? "Not configured"}</p>
                <p>Current destination: {review.baseUrl}</p>
                <p>{activeSourceLabel(review)}</p>
                <Button
                  disabled={unavailable}
                  onClick={() => {
                    setBaseline(review);
                    void check(++operation.current, review);
                  }}
                >
                  Use reviewed access and check
                </Button>
              </div>
            ) : result?.status === "success" ? (
              <>
                <p role="status">Model list access confirmed. This does not verify generation access.</p>
                <Button onClick={() => leave(() => onConnected(name))}>Continue</Button>
              </>
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
          <div className={styles.review} role="alert" tabIndex={-1} ref={errorRef}>
            {error}
          </div>
        )}
        {connection !== "ready" && <p role="status">Waiting for the Evener host to reconnect.</p>}
        <details>
          <summary>Advanced settings</summary>
          <div className={styles.actions}>
            {storedMode && (
              <Button
                variant="quiet"
                disabled={busy}
                onClick={() => {
                  setHost(!host);
                  setResult(null);
                  setReview(null);
                  setPhase("idle");
                }}
              >
                {host ? "Use a new credential" : "Use existing host access"}
              </Button>
            )}
            <Button variant="secondary" disabled={busy || store.writesRefused} onClick={() => setConfigure(true)}>
              Configure another instance
            </Button>
            <Button variant="quiet" onClick={() => leave(onOpenSettings)}>
              Open full connection editor
            </Button>
          </div>
          <p>
            Existing host access keeps whatever authentication the host resolves; it does not disable authentication.
          </p>
        </details>
        <div className={styles.actions}>
          <Button variant="quiet" onClick={() => leave(onChange)}>
            Change provider
          </Button>
          <Button variant="quiet" onClick={() => leave(onClose)}>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
