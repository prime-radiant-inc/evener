import { useCallback, useEffect, useRef, useState } from "react";
import type { AuthTestResponse, InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
import { connectionStore, useConnectionStore } from "../../../../stores/connection";
import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
import { Button, Dialog, FormRow, Input, Skeleton } from "../../../../widgets";
import { useConnectedEffect } from "../useConnectedEffect";
import { activeSourceLabel, safeCredentialTestResult } from "./credentialLabels";
import { AddInstanceDialog } from "./instanceDialogs";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";
import { type OAuthEditor, startOAuthFlow } from "./oauthFlow";
import styles from "./ProviderConnection.module.css";
import { useEditorLifetime } from "./useEditorLifetime";

export interface ProviderConnectionProps {
  onClose(): void;
  onConnected(name?: string): void;
  onManage(): void;
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

export function ProviderConnection(props: ProviderConnectionProps) {
  const store = useCredentialsStore();
  const [selected, setSelected] = useState<ProviderDescriptor | null>(null);
  const [all, setAll] = useState(false);
  const [search, setSearch] = useState("");
  const errorRef = useRef<HTMLDivElement>(null);
  useConnectedEffect(store.fetch, [store.fetch]);
  useEffect(() => {
    if (store.error) errorRef.current?.focus();
  }, [store.error]);
  if (selected)
    return <SelectedConnection key={selected.id} provider={selected} {...props} onChange={() => setSelected(null)} />;
  return (
    <Dialog open onClose={props.onClose} title="Connect a provider">
      <div className={styles.body}>
        <p>Connect one provider to get started. Add others later.</p>
        {store.loading && <Skeleton />}
        {store.error && (
          <div role="alert" tabIndex={-1} ref={errorRef}>
            Providers could not be loaded.{" "}
            <Button variant="secondary" onClick={() => void store.fetch()}>
              Retry
            </Button>
          </div>
        )}
        <div className={styles.actions}>
          <Button variant="quiet" onClick={() => setAll(false)}>
            Popular providers
          </Button>
          <Button variant="quiet" onClick={() => setAll(true)}>
            All providers
          </Button>
        </div>
        {all && (
          <FormRow label="Search providers" htmlFor="provider-search">
            <Input id="provider-search" type="search" value={search} onChange={(e) => setSearch(e.target.value)} />
          </FormRow>
        )}
        <div className={styles.grid}>
          {store.availableProviders
            .filter((row) =>
              all
                ? `${row.id} ${row.name ?? ""} ${HELP[row.id]?.label ?? ""}`
                    .toLowerCase()
                    .includes(search.toLowerCase())
                : row.id in HELP,
            )
            .map((row) => (
              <Button
                key={row.id}
                variant="secondary"
                disabled={store.loading || !!store.error}
                onClick={() => setSelected(row)}
              >
                {HELP[row.id]?.label || row.name || row.id}
              </Button>
            ))}
        </div>
        <details>
          <summary>Already configured access on this host?</summary>
          <Button variant="quiet" onClick={props.onManage}>
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
  onManage,
  onChange,
}: ProviderConnectionProps & { provider: ProviderDescriptor; onChange(): void }) {
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
  const help = HELP[effectiveProvider.id];
  const unavailable = connection !== "ready" || store.loading;

  const setPhase = useCallback((next: Phase) => {
    phaseRef.current = next;
    setPhaseState(next);
  }, []);
  const invalidate = useCallback(
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
      if (
        (["saving", "checking", "result", "review"].includes(phaseRef.current) ||
          (oauth && destination(findSetup(name)) !== destination(baseline))) &&
        (current.instances !== previous.instances ||
          current.availableProviders !== previous.availableProviders ||
          current.loading !== previous.loading ||
          current.error !== previous.error)
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
    setPhase("checking");
    setError("");
    setReview(null);
    try {
      const response = safeCredentialTestResult(
        target.name,
        await credentialsStore.getState().testCredentials(target.name),
      );
      if (!current(token)) return;
      setResult(response);
      setPhase("result");
      if (response.status !== "success") setError(response.message);
    } catch {
      if (!current(token)) return;
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

  async function refreshAndCheck(token: number, expectedSource?: string) {
    setPhase("refreshing");
    const previous = credentialsStore.getState();
    await previous.fetch();
    if (!current(token)) return;
    const fresh = credentialsStore.getState();
    const target = findSetup(name);
    if (
      fresh.loading ||
      fresh.error ||
      fresh.instances === previous.instances ||
      fresh.availableProviders === previous.availableProviders
    ) {
      setPhase("idle");
      setError("Access could not be refreshed. Your saved credential is retained; retry the check.");
      return;
    }
    if (needsConfiguration(target)) {
      setPhase("idle");
      setError("This connection needs configuration before it can be checked. Open the full editor.");
      return;
    }
    if (!target) return;
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
      setPhase("saving");
      try {
        const state = credentialsStore.getState();
        if (json) await state.setCredentialJson(name, value.trim());
        else await state.setApiKey(name, value.trim());
        if (!current(token)) return;
        setSaved(true);
      } catch {
        if (current(token)) {
          setPhase("idle");
          setError("Credential could not be saved. Your draft is retained; retry saving.");
        }
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
    } catch {
      if (current(token)) {
        setPhase("idle");
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

  async function reloadCreated() {
    const token = ++operation.current;
    setPhase("refreshing");
    setError("");
    await credentialsStore.getState().fetch();
    if (!current(token)) return;
    setPhase("idle");
    const state = credentialsStore.getState();
    const created = findSetup(name);
    if (state.loading || state.error || !created) {
      setError("The saved connection could not be loaded. Reload it or open the full editor; do not create it again.");
    } else setBaseline(created);
  }

  if (configure)
    return (
      <AddInstanceDialog
        availableProviders={store.availableProviders}
        initialBase={effectiveProvider.id}
        onCancel={() => setConfigure(false)}
        onSuccess={(createdName) => {
          operation.current += 1;
          const created = findSetup(createdName);
          setName(createdName);
          setBaseline(created);
          setConfigured(true);
          setConfigure(false);
          setSaved(false);
          setMissingCredential(false);
          setError("");
          setReview(null);
          setResult(null);
          setPhase("idle");
        }}
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
        {help && modes.includes("apiKey") && (
          <>
            <a href={help.keyUrl} target="_blank" rel="noreferrer">
              Get an API key
            </a>
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
            <Button variant="secondary" onClick={() => leave(onManage)}>
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
            <Button variant="quiet" onClick={() => leave(onManage)}>
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
