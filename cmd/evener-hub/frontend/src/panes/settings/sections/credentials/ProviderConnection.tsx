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
  Chip,
  Dialog,
  Disclosure,
  InlineNotice,
  Input,
  ListRow,
  OpenButton,
  Select,
  Skeleton,
} from "../../../../widgets";
import { useConnectedEffect } from "../useConnectedEffect";
import type { FormError } from "./formError";
import { byCodePoint, PROTOCOL_OPTIONS, SURFACE_OPTIONS } from "./instanceEdit";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";
import { type OAuthEditor, startOAuthFlow } from "./oauthFlow";
import styles from "./ProviderConnection.module.css";
import { confirmListingState, refreshListingAfterMutation } from "./reconcileListing";
import { useEditorLifetime } from "./useEditorLifetime";

// A connection is one card in every state: the same rows, the same footer.
// Nothing here is a wizard, and nothing navigates away to edit - the full
// editor is the card's own last disclosure.
const ENDPOINT_MOVED_ERROR =
  "This connection changed to a different endpoint. Check its destination and enter the key again.";
const CHANGED_ERROR = "Connection or configuration changed. Review access and check again.";
const REFRESH_FAILED_ERROR = "Access could not be refreshed. Your saved credential is retained; retry the check.";
const RELOAD_FAILED_ERROR =
  "The saved connection could not be loaded. Reload it or open the full editor; do not create it again.";
const NAME_REQUIRED_ERROR = "Enter a name for this connection.";
const ENDPOINT_REQUIRED_ERROR = "Enter the endpoint's base URL.";

/** Why the flow stopped, as its alert's data-reason: the connection or its
 * configuration changed under it, the check or the save was refused for a
 * destination the hub cannot fingerprint, the endpoint moved, the connection
 * was replaced, the check failed, the refresh failed, the connection needs
 * configuration, the save or the sign-in start failed, a create was refused,
 * or a created connection could not be reloaded. */
type ConnectionErrorReason =
  | "changed"
  | "check-fingerprint-unavailable"
  | "fingerprint-unavailable"
  | "endpoint-changed"
  | "connection-replaced"
  | "check-failed"
  | "refresh-failed"
  | "needs-configuration"
  | "save-failed"
  | "sign-in-failed"
  | "reload-failed"
  | "name-required"
  | "endpoint-required"
  | "create-failed";

export interface ProviderConnectionProps {
  onClose(): void;
  onConnected(name?: string): void;
  completionLabel: string;
  /** Hands the user to the settings pane's credentials section, where every
   * connection is listed. The card edits its own connection in place; this is
   * only the listing. The hosting dialog owns the handoff. */
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
/** The refusal every write and check reports the same way: either the
 * connection was replaced under the flow, or its endpoint moved. */
function destinationChangeError(err: unknown): FormError<ConnectionErrorReason> | null {
  const replaced = isStaleListingRefusal(err);
  if (!isEndpointConflict(err) && !replaced) return null;
  return replaced
    ? { reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR }
    : { reason: "endpoint-changed", message: ENDPOINT_MOVED_ERROR };
}

/** The key row's value in the card's own vocabulary: where the credential
 * comes from, short enough to sit in a row. activeSourceLabel is the full
 * sentence a detail sheet prints, and reading it here would put a sentence in
 * a cell. */
function credentialValue(row: InstanceEntry | undefined): string {
  const source = row?.activeSource ?? "none";
  if (source.startsWith("env:")) return `Environment (${source.slice(4)})`;
  switch (source) {
    case "api_key":
      return "providers.toml";
    case "credential_headers":
      return "Credential header";
    case "store":
      return "Saved";
    case "oauth":
      return row?.storedEmail ? `Signed in as ${row.storedEmail}` : "Signed in";
    case "adc":
      return "Application Default Credentials";
    case "none":
      return row?.credentialRequired ? "Not set" : "Not required";
    default:
      return activeSourceLabel(row as InstanceEntry);
  }
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
  const [showAll, setShowAll] = useState(false);
  const [existingAccessOpen, setExistingAccessOpen] = useState(false);
  const [search, setSearch] = useState("");
  const errorRef = useRef<HTMLDivElement>(null);
  useConnectedEffect(store.fetch, [store.fetch]);
  useEffect(() => {
    if (store.error) errorRef.current?.focus();
  }, [store.error]);
  const popular = store.availableProviders.filter((row) => Object.hasOwn(HELP, row.id));
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
              <Button variant="secondary" onClick={() => void store.fetch().catch(() => {})}>
                Retry
              </Button>
            }
          >
            Providers could not be loaded.
          </InlineNotice>
        )}
        {!showAll && <ProviderChoices rows={popular} disabled={listingUnavailable} onSelect={selectProvider} />}
        <Disclosure
          open={showAll}
          onOpenChange={setShowAll}
          summary="Show all providers"
          data-testid="show-all-providers"
        >
          <div className={styles.catalogue}>
            <label className={styles.searchLabel} htmlFor="provider-search">
              Search providers
              <Input id="provider-search" type="search" value={search} onChange={(e) => setSearch(e.target.value)} />
            </label>
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

/** Which row of the card is open for editing. Only one at a time, because a
 * draft belongs to one destination and the check that follows it. */
type EditingRow = "endpoint" | "key" | null;

interface SettingsDraft {
  protocol: string;
  surface: string;
  apiKeyEnv: string;
  credentialHeader: string;
}

function settingsOf(row: InstanceEntry | undefined): SettingsDraft {
  return {
    protocol: row?.protocol ?? "",
    surface: row?.surface ?? "",
    apiKeyEnv: row?.apiKeyEnv ?? "",
    credentialHeader: row?.credentialHeader ?? "",
  };
}

function SelectedConnection({
  provider,
  onClose,
  onConnected,
  completionLabel,
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
  const value = resolvedProviderId && resolvedProviderId !== draft.providerId ? "" : draft.value;
  useEffect(() => {
    if (!resolvedProviderId) return;
    setDraft((current) =>
      current.providerId === resolvedProviderId ? current : { providerId: resolvedProviderId, value: "" },
    );
  }, [resolvedProviderId]);
  const [missingCredential, setMissingCredential] = useState(false);
  const [host, setHost] = useState(false);
  const [saved, setSaved] = useState(row?.activeSource === "store");
  const [storedAccess, setStoredAccess] = useState(row?.activeSource === "store");
  const [configured, setConfigured] = useState(false);
  const [oauth, setOAuth] = useState<OAuthEditor | null>(null);
  const [phase, setPhaseState] = useState<Phase>("idle");
  const phaseRef = useRef<Phase>("idle");
  const [error, setError] = useState<FormError<ConnectionErrorReason> | null>(null);
  const [result, setResult] = useState<AuthTestResponse | null>(null);
  const [review, setReview] = useState<InstanceEntry | null>(null);
  const errorRef = useRef<HTMLDivElement>(null);
  const operation = useRef(0);
  // A listing the card's own create/edit installed is not a foreign change.
  // The write's asserted endpoint is what protects it from a destination that
  // moved meanwhile, so the watch stands aside until the write's continuation
  // has read the listing.
  const selfMutation = useRef(false);
  const active = useEditorLifetime();
  // The add state: a connection this card has not created yet. Its three rows
  // are inputs; every other state shows values.
  const hostConfigured = !!row?.hidden;
  const adding = !row?.hidden && needsConfiguration(row) && !configured;
  const [newName, setNewName] = useState(provider.setup?.name ?? provider.id);
  const [newBaseUrl, setNewBaseUrl] = useState("");
  const [endpointDraft, setEndpointDraft] = useState("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsDraft, setSettingsDraft] = useState<SettingsDraft>(() => settingsOf(row));
  const [settingsBaseline, setSettingsBaseline] = useState<SettingsDraft>(() => settingsOf(row));
  const modes = row?.authModes ?? effectiveProvider.authModes ?? [];
  const json = modes.includes("credentialJson");
  const storedMode = json || modes.includes("apiKey");
  const required = !!row?.credentialRequired && !host;
  // A connection with nothing saved yet opens its key row: the card's first
  // job is to get one, and an "Add key" step would hide the only field the
  // user can act on.
  const needsKey = storedMode && !adding && !row?.hidden && row?.activeSource !== "store";
  const [editing, setEditing] = useState<EditingRow>(() => (needsKey ? "key" : null));
  const busy = phase === "saving" || phase === "refreshing" || phase === "checking";
  const help = Object.hasOwn(HELP, effectiveProvider.id) ? HELP[effectiveProvider.id] : undefined;
  const unavailable = connection !== "ready" || store.loading;
  const providerLabel = help?.label || effectiveProvider.name || effectiveProvider.id;
  const status = statusOf();

  const setPhase = useCallback((next: Phase) => {
    phaseRef.current = next;
    setPhaseState(next);
  }, []);
  const invalidate = useCallback(() => {
    operation.current += 1;
    setPhase("idle");
    setResult(null);
    setReview(null);
    setOAuth(null);
    setError({ reason: "changed", message: CHANGED_ERROR });
  }, [setPhase]);
  useEffect(() => {
    const unsubscribeConnection = connectionStore.subscribe((current, previous) => {
      if (current.client !== previous.client || current.state !== previous.state) {
        invalidate();
        if (current.client !== previous.client) {
          setEditing(null);
          setSaved(false);
          setStoredAccess(false);
          setConfigured(false);
        }
      }
    });
    const unsubscribeStore = credentialsStore.subscribe((current, previous) => {
      if (selfMutation.current) return;
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
  const errorMessage = error?.message;
  useEffect(() => {
    if (errorMessage) errorRef.current?.focus();
  }, [errorMessage]);

  /** The chip and the Status row: one place decides what the card says. */
  function statusOf(): { label: string; tone: "neutral" | "attention" | "alive" | "danger"; row: string } {
    if (adding) {
      return phase === "saving"
        ? { label: "Creating", tone: "neutral", row: "Runs when you save" }
        : { label: "New", tone: "neutral", row: "Runs when you save" };
    }
    if (phase === "saving") return { label: "Saving", tone: "neutral", row: "Saving…" };
    if (phase === "refreshing") return { label: "Checking", tone: "neutral", row: "Reading the provider list…" };
    if (phase === "checking") return { label: "Testing", tone: "neutral", row: "Asking for the model list…" };
    if (phase === "review") return { label: "Review", tone: "attention", row: "Waiting for your review" };
    if (error?.reason === "check-failed") return { label: "Failed", tone: "danger", row: "Failed just now" };
    if (error && (error.reason === "endpoint-changed" || error.reason === "changed"))
      return { label: "Review", tone: "attention", row: "Recheck before use" };
    if (result?.status === "success")
      return { label: "Connected", tone: "alive", row: "Model list reachable, just now" };
    if (result?.status === "unsupported") return { label: "Unverified", tone: "attention", row: "Not testable here" };
    if (saved || host) return { label: "Saved", tone: "neutral", row: "Not tested yet" };
    return { label: "Needs a key", tone: "attention", row: "Not tested yet" };
  }

  function leave(callback: () => void) {
    operation.current += 1;
    callback();
  }
  function current(token: number) {
    return active.current && operation.current === token && connectionStore.getState().state === "ready";
  }
  function changeValue(next: string) {
    operation.current += 1;
    // A typed credential replaces host access: the flag applies only while the
    // field is empty and the card is about to test whatever the host resolves.
    if (next.trim()) setHost(false);
    setDraft({ providerId: resolvedProviderId ?? effectiveProvider.id, value: next });
    setMissingCredential(false);
    setSaved(false);
    setResult(null);
    setReview(null);
    setPhase("idle");
    setError(null);
  }

  async function check(token: number, target: InstanceEntry) {
    if (!current(token)) return;
    if (fingerprintUnavailable(target)) {
      setPhase("idle");
      setError({ reason: "check-fingerprint-unavailable", message: FINGERPRINT_UNAVAILABLE_TEST_MESSAGE });
      return;
    }
    setPhase("checking");
    setError(null);
    setReview(null);
    try {
      const response = safeCredentialTestResult(
        target.name,
        await credentialsStore.getState().testCredentials(target.name, target.endpointFingerprint),
      );
      if (!current(token)) return;
      setResult(response);
      setPhase("result");
      if (response.status !== "success") setError({ reason: "check-failed", message: response.message });
    } catch (err) {
      if (!current(token)) return;
      const changed = destinationChangeError(err);
      if (changed) {
        await recoverChangedEndpoint(changed);
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
    }
  }

  async function recoverChangedEndpoint(changed: FormError<ConnectionErrorReason>, targetName = name) {
    try {
      await credentialsStore.getState().fetchSelf();
    } catch {
      // Best-effort: the alert below is the answer either way.
    }
    changeValue("");
    setBaseline(findSetup(targetName));
    setError(changed);
  }

  async function refreshAndCheck(
    token: number,
    expectedSource?: string,
    targetName = name,
    contextBaseline: InstanceEntry | undefined = baseline,
  ) {
    setPhase("refreshing");
    let applied = false;
    try {
      applied = await credentialsStore.getState().fetchSelf();
      if (!current(token)) return;
      if (!applied) {
        applied = await credentialsStore.getState().fetchSelf();
        if (!current(token)) return;
      }
    } catch {
      if (!current(token)) return;
      setPhase("idle");
      setError({ reason: "refresh-failed", message: REFRESH_FAILED_ERROR });
      return;
    }
    const fresh = credentialsStore.getState();
    const target = findSetup(targetName);
    if (!applied || fresh.loading || fresh.error) {
      setPhase("idle");
      setError({ reason: "refresh-failed", message: REFRESH_FAILED_ERROR });
      return;
    }
    if (needsConfiguration(target)) {
      setPhase("idle");
      setError({
        reason: "needs-configuration",
        message: "This connection needs configuration before it can be checked. Open the full editor.",
      });
      return;
    }
    if (!target) {
      setPhase("idle");
      setError({ reason: "refresh-failed", message: REFRESH_FAILED_ERROR });
      return;
    }
    if (
      destination(contextBaseline) !== destination(target) ||
      target.activeSource !== (expectedSource ?? contextBaseline?.activeSource)
    ) {
      setReview(target);
      setPhase("review");
      return;
    }
    await check(token, target);
  }

  // The card's one primary action. In the add state it creates the connection
  // first; otherwise it writes the typed credential (if any) and tests.
  async function submit() {
    if (busy || unavailable) return;
    if (adding) {
      await createConnection();
      return;
    }
    if (needsConfiguration(row)) return;
    if (!saved && storedMode && required && !value.trim()) {
      setMissingCredential(true);
      document.getElementById("provider-credential")?.focus();
      return;
    }
    const token = ++operation.current;
    setError(null);
    setResult(null);
    setReview(null);
    if (!saved && storedMode && !host && value.trim()) {
      if (fingerprintUnavailable(row)) {
        setPhase("idle");
        setError({ reason: "fingerprint-unavailable", message: FINGERPRINT_UNAVAILABLE_ERROR });
        return;
      }
      if (row && destination(row) !== destination(baseline)) {
        changeValue("");
        setBaseline(row);
        setError({ reason: "endpoint-changed", message: ENDPOINT_MOVED_ERROR });
        return;
      }
      setPhase("saving");
      try {
        const state = credentialsStore.getState();
        const asserted = row?.endpointFingerprint;
        if (json) await state.setCredentialJson(name, value.trim(), asserted);
        else await state.setApiKey(name, value.trim(), asserted);
        if (!current(token)) return;
        setSaved(true);
        setStoredAccess(true);
        setEditing(null);
      } catch (err) {
        if (!current(token)) return;
        const changed = destinationChangeError(err);
        if (changed) {
          await recoverChangedEndpoint(changed);
          return;
        }
        setPhase("idle");
        setError({
          reason: "save-failed",
          message: "Credential could not be saved. Your draft is retained; retry saving.",
        });
        return;
      }
      await refreshAndCheck(token, "store");
    } else await refreshAndCheck(token, saved ? "store" : undefined);
  }

  // Creating the connection is the add state's save: the row the card shows
  // afterwards is the one the listing holds, never an optimistic copy.
  async function createConnection() {
    const trimmedName = newName.trim();
    const trimmedBaseUrl = newBaseUrl.trim();
    if (!trimmedName) {
      setError({ reason: "name-required", message: NAME_REQUIRED_ERROR });
      document.getElementById("connection-name")?.focus();
      return;
    }
    if (!trimmedBaseUrl) {
      setError({ reason: "endpoint-required", message: ENDPOINT_REQUIRED_ERROR });
      document.getElementById("connection-endpoint")?.focus();
      return;
    }
    // The read is part of this operation: a dismissal or a replaced connection
    // while it runs must invalidate the continuation, so the token is taken
    // before the await and the card is busy for its duration.
    const token = ++operation.current;
    setPhase("saving");
    if (!(await nameIsFree(trimmedName))) {
      if (!current(token)) return;
      setPhase("idle");
      setError({ reason: "create-failed", message: `A connection named ${trimmedName} already exists.` });
      document.getElementById("connection-name")?.focus();
      return;
    }
    if (!current(token)) return;
    setError(null);
    setResult(null);
    selfMutation.current = true;
    let applied = false;
    // The fingerprint the create itself authored. It is read from the create's
    // own applied listing before any further await, because a later listing is
    // a different destination's word and must never carry this key.
    let authoredFingerprint: string | undefined;
    try {
      applied = await credentialsStore.getState().create({
        name: trimmedName,
        base: effectiveProvider.id,
        baseUrl: trimmedBaseUrl,
      });
      if (!current(token)) {
        selfMutation.current = false;
        return;
      }
      const authored = applied
        ? credentialsStore
            .getState()
            .instances.find((candidate) => candidate.name === trimmedName && !candidate.implicit)
        : undefined;
      authoredFingerprint = authored?.endpointFingerprint;
      const confirmed = applied
        ? authored !== undefined
        : await confirmListingState((instances) =>
            instances.some((candidate) => candidate.name === trimmedName && !candidate.implicit),
          );
      selfMutation.current = false;
      if (!confirmed) {
        // The create itself landed. Keep its name and move to the card's
        // reload recovery rather than leaving add mode without a way back.
        setName(trimmedName);
        setConfigured(true);
        setPhase("idle");
        setError({
          reason: "reload-failed",
          message: `The connection was saved on the host, but the provider list could not confirm ${trimmedName}. Try again.`,
        });
        return;
      }
    } catch (err) {
      selfMutation.current = false;
      if (!current(token)) return;
      if (isStaleListingRefusal(err)) {
        setPhase("idle");
        setError({ reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR });
        void refreshListingAfterMutation();
        return;
      }
      setPhase("idle");
      setError({ reason: "create-failed", message: "The connection could not be created. Your values are kept." });
      return;
    }
    if (!current(token)) return;
    const created = findSetup(trimmedName);
    setName(trimmedName);
    setConfigured(true);
    setStoredAccess(created?.activeSource === "store");
    setHost(false);
    setMissingCredential(false);
    if (!created) {
      setPhase("idle");
      setError({ reason: "reload-failed", message: RELOAD_FAILED_ERROR });
      return;
    }
    const typedKey = value.trim();
    if (!needsConfiguration(created) && created.baseUrl !== trimmedBaseUrl) {
      // The name resolved to a different destination than this card authored.
      // The draft belongs to the authored endpoint, so it is dropped; the
      // reviewed row is contacted only after the user accepts the move.
      changeValue("");
      setBaseline({ ...created, baseUrl: trimmedBaseUrl });
      setReview(created);
      setPhase("review");
      return;
    }
    setBaseline(created);
    // The key typed in the add state belongs to the endpoint just created, so
    // it is written against that row and tested with it.
    if (storedMode && typedKey) {
      if (!applied || authoredFingerprint === undefined || created.endpointFingerprint !== authoredFingerprint) {
        // The create's own listing was superseded, or the reconciled row is a
        // different destination. The draft belongs to the authored endpoint:
        // keep it, and let the user save it from the key row, whose write
        // asserts the row actually on screen.
        setPhase("idle");
        return;
      }
      try {
        if (json) await credentialsStore.getState().setCredentialJson(trimmedName, typedKey, authoredFingerprint);
        else await credentialsStore.getState().setApiKey(trimmedName, typedKey, authoredFingerprint);
        if (!current(token)) return;
        setSaved(true);
        setStoredAccess(true);
      } catch (err) {
        if (!current(token)) return;
        const changed = destinationChangeError(err);
        if (changed) {
          await recoverChangedEndpoint(changed, trimmedName);
          return;
        }
        setPhase("idle");
        setError({
          reason: "save-failed",
          message: "Credential could not be saved. Your draft is retained; retry saving.",
        });
        return;
      }
    }
    // The created row is this flow's baseline now, and the state update above
    // has not rendered yet, so pass it rather than let refreshAndCheck read the
    // pre-create baseline from this closure.
    await refreshAndCheck(token, storedMode && typedKey ? "store" : undefined, trimmedName, created);
  }

  async function nameIsFree(candidate: string): Promise<boolean> {
    const taken = (state: { instances: InstanceEntry[] }) =>
      state.instances.some((instance) => instance.name === candidate);
    if (taken(credentialsStore.getState())) return false;
    try {
      await credentialsStore.getState().fetchSelf();
    } catch {
      return true; // the create itself is the authority; a failed read is not a refusal
    }
    return !taken(credentialsStore.getState());
  }

  // Saving the endpoint row rewrites the destination the card shows and drops
  // the test result, which was taken against the old one.
  async function saveEndpoint() {
    const next = endpointDraft.trim();
    if (!row) return;
    if (!next) {
      setError({ reason: "endpoint-required", message: ENDPOINT_REQUIRED_ERROR });
      return;
    }
    const token = ++operation.current;
    setPhase("saving");
    setError(null);
    setResult(null);
    selfMutation.current = true;
    try {
      await credentialsStore
        .getState()
        .edit({ name: row.name, baseUrl: next, expectedEndpointFingerprint: row.endpointFingerprint });
      selfMutation.current = false;
      if (!current(token)) return;
      await credentialsStore.getState().fetchSelf();
      if (!current(token)) return;
      const updated = findSetup(name);
      setBaseline(updated);
      setEndpointDraft("");
      setEditing(null);
      setResult(null);
      setPhase("idle");
    } catch (err) {
      selfMutation.current = false;
      if (!current(token)) return;
      const changed = destinationChangeError(err);
      if (changed) {
        await recoverChangedEndpoint(changed);
        return;
      }
      setPhase("idle");
      setError({ reason: "save-failed", message: "The endpoint could not be saved. Your value is kept." });
    }
  }

  // The full editor is this card's own disclosure. Saving it is one edit with
  // every field, so the row the listing returns is the only source of truth.
  async function saveSettings() {
    if (!row) return;
    const token = ++operation.current;
    setPhase("saving");
    setError(null);
    setResult(null);
    selfMutation.current = true;
    const changed = (field: keyof SettingsDraft) => settingsDraft[field] !== settingsBaseline[field];
    try {
      await credentialsStore.getState().edit({
        name: row.name,
        ...(changed("protocol")
          ? { protocol: settingsDraft.protocol || undefined, clearProtocol: !settingsDraft.protocol }
          : {}),
        ...(changed("surface")
          ? { surface: settingsDraft.surface || undefined, clearSurface: !settingsDraft.surface }
          : {}),
        ...(changed("apiKeyEnv")
          ? { apiKeyEnv: settingsDraft.apiKeyEnv || undefined, clearApiKeyEnv: !settingsDraft.apiKeyEnv }
          : {}),
        ...(changed("credentialHeader")
          ? {
              credentialHeader: settingsDraft.credentialHeader || undefined,
              clearCredentialHeader: !settingsDraft.credentialHeader,
            }
          : {}),
        expectedEndpointFingerprint: row.endpointFingerprint,
      });
      selfMutation.current = false;
      if (!current(token)) return;
      await credentialsStore.getState().fetchSelf();
      if (!current(token)) return;
      setBaseline(findSetup(name));
      setSettingsOpen(false);
      setResult(null);
      setPhase("idle");
    } catch (err) {
      selfMutation.current = false;
      if (!current(token)) return;
      if (isStaleListingRefusal(err)) {
        await recoverChangedEndpoint({ reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR });
        return;
      }
      setPhase("idle");
      setError({ reason: "save-failed", message: "The settings could not be saved. Your values are kept." });
    }
  }

  async function signIn() {
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
        if (isStaleListingRefusal(err)) {
          setError({ reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR });
          void credentialsStore
            .getState()
            .fetch()
            .catch(() => {});
          return;
        }
        setError({ reason: "sign-in-failed", message: "Sign-in could not be started. Try again." });
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
  const signedInRef = useRef(signedIn);
  signedInRef.current = signedIn;
  const completeOAuth = useCallback(() => signedInRef.current(), []);

  async function reloadCreated() {
    const token = ++operation.current;
    setPhase("refreshing");
    setError(null);
    let applied = false;
    try {
      applied = await credentialsStore.getState().fetch();
      if (!current(token)) return;
      if (!applied) {
        applied = await credentialsStore.getState().fetch();
        if (!current(token)) return;
      }
    } catch {
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

  function primaryActionLabel(): string {
    if (editing === "endpoint") return "Save endpoint";
    if (busy) {
      if (phase === "saving") return adding ? "Creating…" : "Saving…";
      return phase === "refreshing" ? "Reading providers…" : "Testing…";
    }
    if (adding) return "Save and test";
    if (result || saved) return "Test again";
    if (storedMode && !host && (required || value.trim())) return "Save and test";
    return "Test connection";
  }
  const primaryLabel = primaryActionLabel();

  return (
    <Dialog
      open
      onClose={() => leave(onClose)}
      title={adding ? "New connection" : row?.implicit === false ? name : providerLabel}
      footer={
        <>
          {!adding && (
            <Button variant="quiet" onClick={() => leave(onChange)}>
              Change provider
            </Button>
          )}
          <Button variant="quiet" onClick={() => leave(onClose)}>
            {storedAccess ? "Close, access stays saved" : "Cancel"}
          </Button>
          {result?.status === "unsupported" ? (
            <Button variant="secondary" disabled={busy} onClick={() => leave(() => onConnected(name))}>
              Continue without verification
            </Button>
          ) : result?.status === "success" ? (
            <Button disabled={busy} onClick={() => leave(() => onConnected(name))}>
              {completionLabel}
            </Button>
          ) : (
            <Button type="submit" form="provider-connection-form" disabled={busy || unavailable || hostConfigured}>
              {primaryLabel}
            </Button>
          )}
        </>
      }
    >
      <form
        id="provider-connection-form"
        className={styles.body}
        noValidate
        onSubmit={(event) => {
          event.preventDefault();
          if (editing === "endpoint") void saveEndpoint();
          else void submit();
        }}
        onKeyDown={(event) => {
          if (event.key !== "Enter" || event.target instanceof HTMLButtonElement) return;
          event.preventDefault();
          if (editing === "endpoint") void saveEndpoint();
          else void submit();
        }}
      >
        <div className={styles.cardHead}>
          <span className={styles.providerName}>{providerLabel}</span>
          <Chip tone={status.tone} kind="connection-status">
            {status.label}
          </Chip>
        </div>

        <div className={styles.rows}>
          {adding ? (
            <>
              <div className={styles.row}>
                <label className={styles.rowLabel} htmlFor="connection-name">
                  Name
                </label>
                <div className={styles.rowValue}>
                  <Input
                    id="connection-name"
                    value={newName}
                    disabled={busy}
                    onChange={(event) => {
                      setNewName(event.target.value);
                      setError(null);
                    }}
                  />
                </div>
              </div>
              <div className={styles.row}>
                <label className={styles.rowLabel} htmlFor="connection-endpoint">
                  Endpoint URL
                </label>
                <div className={styles.rowValue}>
                  <div className={styles.mono}>
                    <Input
                      id="connection-endpoint"
                      placeholder="https://…/v1"
                      value={newBaseUrl}
                      disabled={busy}
                      onChange={(event) => {
                        setNewBaseUrl(event.target.value);
                        setError(null);
                      }}
                    />
                  </div>
                </div>
              </div>
              <div className={styles.row}>
                <label className={styles.rowLabel} htmlFor="provider-credential">
                  {json ? "Credential JSON" : "API key"}
                </label>
                <div className={styles.rowValue}>
                  <Input
                    id="provider-credential"
                    type="password"
                    autoComplete="off"
                    required={required}
                    disabled={busy}
                    aria-describedby={missingCredential ? "provider-credential-error" : undefined}
                    value={value}
                    onChange={(event) => changeValue(event.target.value)}
                  />
                  <span className={styles.rowHelp}>Optional. Leave it empty to use the key this host resolves.</span>
                  {missingCredential && (
                    <span className={styles.rowError} id="provider-credential-error" role="alert">
                      Enter a credential, or use this host's access.
                    </span>
                  )}
                </div>
              </div>
            </>
          ) : (
            <>
              <div className={styles.row}>
                <span className={styles.rowLabel}>Endpoint</span>
                <div className={styles.rowValue}>
                  {editing === "endpoint" ? (
                    <div className={styles.mono}>
                      <Input
                        id="connection-endpoint"
                        aria-label="Endpoint URL"
                        value={endpointDraft}
                        disabled={busy}
                        onChange={(event) => setEndpointDraft(event.target.value)}
                      />
                    </div>
                  ) : (
                    <span className={styles.monoValue}>{row?.baseUrl}</span>
                  )}
                </div>
                <div className={styles.rowActions}>
                  {editing === "endpoint" ? (
                    <>
                      <Button variant="secondary" size="xs" disabled={busy} onClick={() => void saveEndpoint()}>
                        Save
                      </Button>
                      <Button variant="quiet" size="xs" disabled={busy} onClick={() => setEditing(null)}>
                        Cancel
                      </Button>
                    </>
                  ) : (
                    <Button
                      variant="quiet"
                      size="xs"
                      disabled={busy}
                      onClick={() => {
                        setEndpointDraft(row?.baseUrl ?? "");
                        setEditing("endpoint");
                        setError(null);
                      }}
                    >
                      Edit
                    </Button>
                  )}
                </div>
              </div>

              {storedMode && (
                <div className={styles.row}>
                  {editing === "key" ? (
                    <label className={styles.rowLabel} htmlFor="provider-credential">
                      {json ? "Credential JSON" : "API key"}
                    </label>
                  ) : (
                    <span className={styles.rowLabel}>{json ? "Credential JSON" : "API key"}</span>
                  )}
                  <div className={styles.rowValue}>
                    {editing === "key" ? (
                      <Input
                        id="provider-credential"
                        type="password"
                        autoComplete="off"
                        required={required}
                        disabled={busy}
                        aria-describedby={missingCredential ? "provider-credential-error" : undefined}
                        value={value}
                        onChange={(event) => changeValue(event.target.value)}
                      />
                    ) : (
                      <span data-testid={saved || host ? "credential-saved" : undefined}>
                        {saved || host ? (host ? "Host access" : credentialValue(row)) : "Not set"}
                      </span>
                    )}
                    {editing === "key" && (
                      <span className={styles.rowHelp}>
                        {result || saved ? "The old key stops working when this saves. " : ""}
                        Or{" "}
                        <Button
                          variant="link"
                          disabled={busy}
                          onClick={() => {
                            setHost(true);
                            changeValue("");
                            setResult(null);
                            setPhase("idle");
                          }}
                        >
                          use this host's access instead
                        </Button>
                        .
                      </span>
                    )}
                    {editing === "key" && host && (
                      <span className={styles.rowHelp}>
                        This connection will use whatever the host resolves. Authentication stays on.
                      </span>
                    )}
                    {missingCredential && (
                      <span className={styles.rowError} id="provider-credential-error" role="alert">
                        Enter a credential, or use this host's access.
                      </span>
                    )}
                  </div>
                  <div className={styles.rowActions}>
                    {editing === "key" ? (
                      <>
                        <Button variant="secondary" size="xs" disabled={busy} onClick={() => void submit()}>
                          Save
                        </Button>
                        <Button
                          variant="quiet"
                          size="xs"
                          disabled={busy}
                          onClick={() => {
                            setEditing(null);
                            setHost(false);
                            changeValue("");
                            setSaved(row?.activeSource === "store");
                          }}
                        >
                          Cancel
                        </Button>
                      </>
                    ) : (
                      <Button
                        variant="quiet"
                        size="xs"
                        disabled={busy}
                        onClick={() => {
                          setEditing("key");
                          setError(null);
                          document.getElementById("provider-credential")?.focus();
                        }}
                      >
                        {saved || host ? "Replace" : "Add key"}
                      </Button>
                    )}
                  </div>
                </div>
              )}

              <div className={styles.row}>
                <span className={styles.rowLabel}>Status</span>
                <div className={styles.rowValue}>
                  <span role="status" data-state={result?.status === "success" ? "success" : undefined}>
                    {status.row}
                  </span>
                </div>
                <div className={styles.rowActions} />
              </div>
            </>
          )}
        </div>

        {hostConfigured && (
          <p className={styles.note} data-state="host-configured">
            This connection is configured on the host, so its settings are read-only here.
          </p>
        )}

        {json && editing !== "key" && (
          <p className={styles.note}>
            Paste a service-account key or application_default_credentials.json. Existing Application Default
            Credentials can also be checked without replacing them.
          </p>
        )}

        {modes.includes("oauth") && (
          <Button disabled={busy || unavailable} onClick={() => void signIn()}>
            Sign in
          </Button>
        )}
        {help && modes.includes("apiKey") && <OpenButton href={help.keyUrl} word="Get an API key" />}
        {help && (
          <p className={styles.note} data-testid="billing-note">
            {help.billing}
          </p>
        )}

        {review && (
          <div className={styles.review} data-testid="access-review">
            <p>Review changed access before contacting the provider.</p>
            <div className={styles.rows}>
              <div className={styles.row}>
                <span className={styles.rowLabel}>Was</span>
                <div className={styles.rowValue}>
                  <span className={styles.monoValue} data-destination="previous">
                    {baseline?.baseUrl ?? "Not configured"}
                  </span>
                </div>
              </div>
              <div className={styles.row}>
                <span className={styles.rowLabel}>Now</span>
                <div className={styles.rowValue}>
                  <span className={styles.monoValue} data-destination="current">
                    {review.baseUrl}
                  </span>
                </div>
              </div>
            </div>
            <Button
              disabled={unavailable}
              onClick={() => {
                setBaseline(review);
                void check(++operation.current, review);
              }}
            >
              Use reviewed access and test
            </Button>
          </div>
        )}

        {configured && needsConfiguration(row) && (
          <div className={styles.noticeRow} data-testid="connection-configured">
            <InlineNotice
              tone="neutral"
              action={
                <Button variant="secondary" size="sm" disabled={busy} onClick={() => void reloadCreated()}>
                  Reload
                </Button>
              }
            >
              Settings were saved, but this connection is not ready.
            </InlineNotice>
          </div>
        )}

        {error && (
          <InlineNotice tone="danger" tabIndex={-1} ref={errorRef} data-reason={error.reason}>
            {error.message}
          </InlineNotice>
        )}
        {connection !== "ready" && (
          <p role="status" data-state="reconnecting">
            Waiting for the Evener host to reconnect.
          </p>
        )}

        {!adding && !hostConfigured && (
          <Disclosure
            open={settingsOpen}
            onOpenChange={(open) => {
              if (open) {
                setSettingsDraft(settingsOf(row));
                setSettingsBaseline(settingsOf(row));
              }
              setSettingsOpen(open);
            }}
            summary="All connection settings"
          >
            <div className={styles.settings}>
              {row?.vars && Object.keys(row.vars).length > 0 && (
                <p className={styles.note}>{Object.keys(row.vars).sort(byCodePoint).join(", ")}</p>
              )}
              <label className={styles.settingsRow} htmlFor="connection-protocol">
                <span className={styles.rowLabel}>Protocol</span>
                <Select
                  id="connection-protocol"
                  value={settingsDraft.protocol}
                  onChange={(event) => setSettingsDraft((current) => ({ ...current, protocol: event.target.value }))}
                  options={PROTOCOL_OPTIONS}
                  disabled={busy}
                />
              </label>
              <label className={styles.settingsRow} htmlFor="connection-surface">
                <span className={styles.rowLabel}>Surface</span>
                <Select
                  id="connection-surface"
                  value={settingsDraft.surface}
                  onChange={(event) => setSettingsDraft((current) => ({ ...current, surface: event.target.value }))}
                  options={SURFACE_OPTIONS}
                  disabled={busy}
                />
              </label>
              <label className={styles.settingsRow} htmlFor="connection-apikeyenv">
                <span className={styles.rowLabel}>API key env</span>
                <Input
                  id="connection-apikeyenv"
                  placeholder="e.g. PORTKEY_KEY"
                  value={settingsDraft.apiKeyEnv}
                  disabled={busy}
                  onChange={(event) => setSettingsDraft((current) => ({ ...current, apiKeyEnv: event.target.value }))}
                />
              </label>
              <label className={styles.settingsRow} htmlFor="connection-credentialheader">
                <span className={styles.rowLabel}>Credential header</span>
                <Input
                  id="connection-credentialheader"
                  placeholder="Authorization=Bearer $VAR or $(get-token)"
                  value={settingsDraft.credentialHeader}
                  disabled={busy}
                  onChange={(event) =>
                    setSettingsDraft((current) => ({ ...current, credentialHeader: event.target.value }))
                  }
                />
              </label>
              <div className={styles.settingsActions}>
                <Button variant="quiet" size="sm" disabled={busy} onClick={() => leave(onOpenSettings)}>
                  Manage all connections
                </Button>
                <Button variant="secondary" size="sm" disabled={busy} onClick={() => void saveSettings()}>
                  Save settings
                </Button>
              </div>
            </div>
          </Disclosure>
        )}
      </form>
    </Dialog>
  );
}
