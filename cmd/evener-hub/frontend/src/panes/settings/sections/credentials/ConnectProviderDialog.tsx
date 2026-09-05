import { useCallback, useEffect, useRef, useState } from "react";
import { friendlyErrorMessage } from "../../../../protocol/errors";
import type { AuthTestResponse } from "../../../../protocol/types.gen";
import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
import { Button, Dialog, Skeleton, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useConnectedEffect } from "../useConnectedEffect";
import styles from "./ConnectProviderDialog.module.css";
import { activeSourceLabel, safeCredentialTestResult } from "./credentialLabels";
import { AddInstanceDialog, ApiKeyDialog } from "./instanceDialogs";
import { DeviceCodeDialog, OAuthRedirectDialog } from "./oauthDialogs";
import { type OAuthEditor, startOAuthFlow } from "./oauthFlow";

const CLASS = {
  body: requireClass(styles.body, "ConnectProviderDialog.module.css", "body"),
  intro: requireClass(styles.intro, "ConnectProviderDialog.module.css", "intro"),
  status: requireClass(styles.status, "ConnectProviderDialog.module.css", "status"),
  error: requireClass(styles.error, "ConnectProviderDialog.module.css", "error"),
  empty: requireClass(styles.empty, "ConnectProviderDialog.module.css", "empty"),
  providerList: requireClass(styles.providerList, "ConnectProviderDialog.module.css", "providerList"),
  providerRow: requireClass(styles.providerRow, "ConnectProviderDialog.module.css", "providerRow"),
  providerIdentity: requireClass(styles.providerIdentity, "ConnectProviderDialog.module.css", "providerIdentity"),
  providerName: requireClass(styles.providerName, "ConnectProviderDialog.module.css", "providerName"),
  providerInstance: requireClass(styles.providerInstance, "ConnectProviderDialog.module.css", "providerInstance"),
  actions: requireClass(styles.actions, "ConnectProviderDialog.module.css", "actions"),
  diagnostics: requireClass(styles.diagnostics, "ConnectProviderDialog.module.css", "diagnostics"),
};

type OpenEditor = { kind: "add" } | { kind: "apiKey"; name: string } | OAuthEditor | null;

const TEST_INTERRUPTED_MESSAGE = "Provider configuration refreshed while testing. Test the connection again.";

export interface ConnectProviderDialogProps {
  onClose(): void;
  onConnected(): void;
}

export function ConnectProviderDialog({ onClose, onConnected }: ConnectProviderDialogProps) {
  const { instances, availableProviders, diagnostics, writesRefused, loading, error, fetch } = useCredentialsStore();
  const [openEditor, setOpenEditor] = useState<OpenEditor>(null);
  const [testState, setTestState] = useState<{
    name: string;
    version: number;
    pending: boolean;
    result?: AuthTestResponse;
    notice?: string;
  } | null>(null);
  const toast = useToasts();
  const mounted = useRef(true);
  const previousInstances = useRef(instances);
  const instanceVersion = useRef(0);
  const operationVersion = useRef(0);
  if (previousInstances.current !== instances) {
    previousInstances.current = instances;
    instanceVersion.current += 1;
  }

  useConnectedEffect(fetch, [fetch]);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // A refresh can represent a secret rotation even when every public row
  // field is unchanged, so an in-flight test cannot safely survive it. Keep
  // the conservative invalidation visible as a retry state instead of making
  // the test silently look as though it never ran.
  // biome-ignore lint/correctness/useExhaustiveDependencies: instances is a deliberate trigger-only dependency
  useEffect(() => {
    setTestState((current) =>
      current?.pending
        ? {
            name: current.name,
            version: instanceVersion.current,
            pending: false,
            notice: TEST_INTERRUPTED_MESSAGE,
          }
        : null,
    );
  }, [instances]);

  const closeEditor = useCallback(() => {
    operationVersion.current += 1;
    setTestState(null);
    setOpenEditor(null);
  }, []);

  function closeDialog(): void {
    operationVersion.current += 1;
    onClose();
  }

  function beginOperation(): number {
    const version = ++operationVersion.current;
    setTestState(null);
    return version;
  }

  function chooseEditor(editor: Exclude<OpenEditor, OAuthEditor | null>): void {
    beginOperation();
    setOpenEditor(editor);
  }

  async function testConnection(name: string): Promise<void> {
    const operation = beginOperation();
    const version = instanceVersion.current;
    setTestState({ name, version, pending: true });
    try {
      const result = safeCredentialTestResult(name, await credentialsStore.getState().testCredentials(name));
      if (!mounted.current || operationVersion.current !== operation || instanceVersion.current !== version) return;
      if (result.status === "success") {
        onConnected();
        return;
      }
      setTestState({ name, version, pending: false, result });
    } catch {
      if (!mounted.current || operationVersion.current !== operation || instanceVersion.current !== version) return;
      setTestState({
        name,
        version,
        pending: false,
        result: safeCredentialTestResult(name, { provider: name, status: "endpoint_failure", message: "" }),
      });
    }
  }

  async function startSignIn(name: string): Promise<void> {
    const operation = beginOperation();
    const version = instanceVersion.current;
    const isCurrent = () =>
      mounted.current && operationVersion.current === operation && instanceVersion.current === version;
    try {
      const editor = await startOAuthFlow(name, isCurrent);
      if (editor && isCurrent()) setOpenEditor(editor);
    } catch (err) {
      if (isCurrent()) toast.push("error", `Sign-in failed: ${friendlyErrorMessage(err)}`);
    }
  }

  if (openEditor?.kind === "apiKey") {
    const instance = instances.find(
      (candidate) => candidate.name === openEditor.name && (candidate.authModes ?? []).includes("apiKey"),
    );
    if (instance) return <ApiKeyDialog instance={instance} onCancel={closeEditor} onSuccess={closeEditor} />;
  }
  if (openEditor?.kind === "add") {
    return <AddInstanceDialog availableProviders={availableProviders} onCancel={closeEditor} onSuccess={closeEditor} />;
  }
  if (openEditor?.kind === "oauth-redirect") {
    return (
      <OAuthRedirectDialog
        name={openEditor.name}
        flowId={openEditor.flowId}
        authUrl={openEditor.authUrl}
        onCancel={closeEditor}
        onSuccess={closeEditor}
      />
    );
  }
  if (openEditor?.kind === "device") {
    return (
      <DeviceCodeDialog
        key={openEditor.flowId}
        name={openEditor.name}
        flowId={openEditor.flowId}
        userCode={openEditor.userCode}
        verificationUrl={openEditor.verificationUrl}
        intervalSeconds={openEditor.intervalSeconds}
        onCancel={closeEditor}
        onSuccess={closeEditor}
        onRestart={() => void startSignIn(openEditor.name)}
      />
    );
  }

  const visibleInstances = instances.filter((instance) => !instance.hidden);

  return (
    <Dialog open onClose={closeDialog} title="Connect provider">
      <div className={CLASS.body}>
        <p className={CLASS.intro}>Choose a provider instance, configure it if needed, then test the connection.</p>
        {loading && <Skeleton />}
        {error && (
          <div className={CLASS.actions}>
            <p className={CLASS.error} role="alert">
              Failed to load providers: {friendlyErrorMessage(error)}
            </p>
            <Button variant="secondary" onClick={() => void fetch()}>
              Retry
            </Button>
          </div>
        )}
        {diagnostics.length > 0 && (
          <ul className={CLASS.diagnostics} aria-label="Provider warnings">
            {diagnostics.map((diagnostic, index) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: registry diagnostics are an unordered list without stable identities
              <li key={index}>{diagnostic}</li>
            ))}
          </ul>
        )}
        {!loading && !error && visibleInstances.length === 0 && (
          <p className={CLASS.empty}>No provider instances are available.</p>
        )}
        {!loading && !error && visibleInstances.length > 0 && (
          <ul className={CLASS.providerList}>
            {visibleInstances.map((instance) => {
              const providerName =
                availableProviders.find((provider) => provider.id === instance.providerId)?.name ?? instance.providerId;
              const supportsApiKey = (instance.authModes ?? []).includes("apiKey");
              const supportsOAuth = (instance.authModes ?? []).includes("oauth");
              const pending =
                testState?.name === instance.name && testState.version === instanceVersion.current && testState.pending;
              const result =
                testState?.name === instance.name && testState.version === instanceVersion.current
                  ? testState.result
                  : undefined;
              const notice =
                testState?.name === instance.name && testState.version === instanceVersion.current
                  ? testState.notice
                  : undefined;
              return (
                <li key={instance.name} className={CLASS.providerRow}>
                  <div className={CLASS.providerIdentity}>
                    <span className={CLASS.providerName}>{providerName}</span>
                    <span className={CLASS.providerInstance}>{instance.name}</span>
                    <span className={CLASS.status}>{activeSourceLabel(instance)}</span>
                    {(result || notice) && (
                      <span className={CLASS.status} role="status">
                        {notice ?? result?.message}
                      </span>
                    )}
                  </div>
                  <div className={CLASS.actions}>
                    {supportsApiKey && (
                      <Button variant="secondary" onClick={() => chooseEditor({ kind: "apiKey", name: instance.name })}>
                        {instance.hasStoredFile ? "Replace API key" : "Set API key"}
                      </Button>
                    )}
                    {supportsOAuth && (
                      <Button variant="secondary" onClick={() => void startSignIn(instance.name)}>
                        {instance.hasStoredOAuth ? "Refresh sign-in" : "Sign in"}
                      </Button>
                    )}
                    <Button onClick={() => void testConnection(instance.name)} disabled={pending}>
                      {pending ? "Testing connection…" : result || notice ? "Retry test" : "Test connection"}
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
        <div className={CLASS.actions}>
          <Button
            variant="secondary"
            onClick={() => chooseEditor({ kind: "add" })}
            disabled={writesRefused || availableProviders.length === 0}
          >
            Add provider instance
          </Button>
          <Button variant="quiet" onClick={closeDialog}>
            Cancel
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
