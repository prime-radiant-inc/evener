// instanceDialogs.tsx: the instance-CRUD editors that stay dialogs (spec
// 2026-09-07 §2): Add, plus Set/Replace API key and credential JSON.
// Editing an existing instance lives in InstanceSheet. Each owns its own
// client validation, store call, inline error, and toast; the parent
// (CredentialsSection) only needs to close the single open editor via
// `onSuccess`/`onCancel` - it never has to distinguish success from failure
// itself.
//
// Updated for the provider registry's instance shape (spec §11.3): Type
// becomes Base provider over availableProviders, Protocol and Surface are
// plain selects over the registry's vocabularies (instanceEdit.ts),
// defaulting to inherit, and the Add form gains a dynamic Input per the
// selected provider's Vars entry plus api-key-env/credential-header fields
// mirroring the CLI's --api-key-env/--credential-header flags (§11.2).
// Vars maps template placeholder name -> environment variable name
// (roborev round 1, F3): the input is labeled by the env name (what the
// docs tell users to set) but keyed by the template name, since that is
// what the registry actually substitutes.
import { type FormEvent, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { AuthStatusResponse, InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
import { credentialsStore } from "../../../../stores/credentials";
import { Button, Dialog, FormRow, Input, Select, type SelectOption, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./instanceDialogs.module.css";
import { byCodePoint, PROTOCOL_OPTIONS, SURFACE_OPTIONS } from "./instanceEdit";
import { confirmListingState } from "./reconcileListing";

import { useEditorLifetime } from "./useEditorLifetime";

const CLASS = {
  body: requireClass(styles.body, "instanceDialogs.module.css", "body"),
  actions: requireClass(styles.actions, "instanceDialogs.module.css", "actions"),
  error: requireClass(styles.error, "instanceDialogs.module.css", "error"),
  textarea: requireClass(styles.textarea, "instanceDialogs.module.css", "textarea"),
};

// nonEmptyVars trims and drops blank entries before they reach the wire -
// InstanceCreateParams.Vars only carries variables the user actually set
// (spec §11.3); a blank templated field means "leave it to the
// environment," not "set it to the empty string."
function nonEmptyVars(vars: Record<string, string>): Record<string, string> | undefined {
  const entries = Object.entries(vars)
    .map(([key, value]) => [key, value.trim()] as const)
    .filter(([, value]) => value !== "");
  return entries.length > 0 ? Object.fromEntries(entries) : undefined;
}

export interface AddInstanceDialogProps {
  availableProviders: ProviderDescriptor[];
  initialBase?: string;
  onCancel: () => void;
  onSuccess: (name: string) => void;
  // A create whose reconciled listing did not show the instance is NOT a
  // success. A consumer that has its own recovery for a missing row (the
  // guided flow's not-ready/reload state) takes it through this callback,
  // without a success toast; a consumer without one gets the dialog's own
  // error and re-confirm action.
  onUnconfirmedCreate?: (name: string) => void;
}

/** The global "+ Add provider instance" form (parity-m7-settings.md §7f). */
export function AddInstanceDialog({
  availableProviders,
  initialBase = "",
  onCancel,
  onSuccess,
  onUnconfirmedCreate,
}: AddInstanceDialogProps) {
  const [base, setBase] = useState(initialBase);
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [protocol, setProtocol] = useState("");
  const [surface, setSurface] = useState("");
  const [vars, setVars] = useState<Record<string, string>>({});
  const [apiKeyEnv, setApiKeyEnv] = useState("");
  const [credentialHeader, setCredentialHeader] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Set when the create succeeded but the listing could not be confirmed to
  // contain it: the dialog stays open and offers a re-confirm rather than
  // re-issuing a create that already landed on the host.
  const [unconfirmedName, setUnconfirmedName] = useState<string | null>(null);
  const toast = useToasts();
  const active = useEditorLifetime();

  function confirmCreate(instanceName: string): Promise<boolean> {
    return confirmListingState((instances) => instances.some((instance) => instance.name === instanceName));
  }

  const baseOptions: SelectOption[] = [
    { value: "", label: "" },
    ...availableProviders.map((p) => ({ value: p.id, label: p.name || p.id })),
  ];
  const templateVars = availableProviders.find((p) => p.id === base)?.vars ?? {};

  function handleBaseChange(nextBase: string): void {
    setBase(nextBase);
    setVars({}); // a var input from the previous base must not leak into the new one
  }

  function updateVar(template: string, value: string): void {
    setVars((current) => ({ ...current, [template]: value }));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    // While a create still needs confirming, a submit (the button or Enter)
    // re-confirms instead of re-issuing a create the host already accepted.
    if (unconfirmedName) {
      await handleCheckAgain();
      return;
    }
    if (!base) {
      setError("Base provider is required.");
      return;
    }
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Name is required.");
      return;
    }
    const trimmedCredentialHeader = credentialHeader.trim();
    if (trimmedCredentialHeader && !trimmedCredentialHeader.includes("$")) {
      setError("Credential header must reference a $VARIABLE, never a literal secret.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      const applied = await credentialsStore.getState().create({
        name: trimmedName,
        base,
        baseUrl: baseUrl.trim(),
        protocol: protocol || undefined,
        surface: surface || undefined,
        vars: nonEmptyVars(vars),
        apiKeyEnv: apiKeyEnv.trim() || undefined,
        credentialHeader: trimmedCredentialHeader || undefined,
      });
      // The listing a superseded create answered with was discarded by the
      // store's generation guard - reconcile before steering on the create,
      // and require the listing that applied to actually contain the new
      // instance. A resolved fetch is not confirmation (a newer read
      // supersedes it, a failed read lands its error in the store), and
      // neither is a listing that never reflected the create: reporting
      // success there would close the editor on an instance the host may not
      // have. A consumer with its own missing-row recovery (the guided flow's
      // not-ready/reload state) takes over without a success claim; otherwise
      // the dialog stays open with a re-confirm path rather than re-issuing
      // the create. Data refresh deliberately survives an unmount: the dialog
      // is gone, but the store still owes the caller a current listing.
      if (!applied && !(await confirmCreate(trimmedName))) {
        if (!active.current) return;
        if (onUnconfirmedCreate) {
          onUnconfirmedCreate(trimmedName);
          return;
        }
        setUnconfirmedName(trimmedName);
        setError(
          `The connection was saved on the host, but the provider list could not confirm ${trimmedName}. Check again.`,
        );
        return;
      }
      if (!active.current) return;
      toast.push("success", `Created instance ${trimmedName}`);
      onSuccess(trimmedName);
    } catch (err) {
      if (!active.current) return;
      const message = errorText(err);
      setError(message);
      toast.push("error", `Create failed: ${message}`);
    } finally {
      if (active.current) setBusy(false);
    }
  }

  // Re-confirms a create whose first reconcile could not see the instance.
  // Only the listing read is retried: the create itself already succeeded on
  // the host, so re-issuing it could fail on an instance that exists.
  async function handleCheckAgain(): Promise<void> {
    const instanceName = unconfirmedName;
    if (!instanceName) return;
    setBusy(true);
    try {
      const confirmed = await confirmCreate(instanceName);
      if (!active.current) return;
      if (!confirmed) {
        setError(`The provider list still does not show ${instanceName}. Check again.`);
        return;
      }
      setError(null);
      setUnconfirmedName(null);
      toast.push("success", `Created instance ${instanceName}`);
      onSuccess(instanceName);
    } catch (err) {
      // A dropped connection rejects the read (requireClient's contract). Keep
      // the unconfirmed name so the user can retry once it is back, and show
      // the failure the way the initial submit does.
      if (!active.current) return;
      setError(errorText(err));
    } finally {
      if (active.current) setBusy(false);
    }
  }

  return (
    <Dialog open onClose={onCancel} title="Add provider instance">
      <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
        <FormRow label="Base provider" htmlFor="add-instance-base">
          <Select
            id="add-instance-base"
            value={base}
            onChange={(event) => handleBaseChange(event.target.value)}
            options={baseOptions}
          />
        </FormRow>
        <FormRow label="Name" htmlFor="add-instance-name">
          <Input
            id="add-instance-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="e.g. work"
            disabled={busy}
          />
        </FormRow>
        <FormRow label="Base URL (optional)" htmlFor="add-instance-baseurl">
          <Input
            id="add-instance-baseurl"
            value={baseUrl}
            onChange={(event) => setBaseUrl(event.target.value)}
            placeholder="https://…"
            disabled={busy}
          />
        </FormRow>
        <FormRow
          label="Protocol"
          htmlFor="add-instance-protocol"
          help="Leave on inherit unless the endpoint speaks a different wire protocol than its base."
        >
          <Select
            id="add-instance-protocol"
            value={protocol}
            onChange={(event) => setProtocol(event.target.value)}
            options={PROTOCOL_OPTIONS}
            disabled={busy}
          />
        </FormRow>
        <FormRow label="Surface" htmlFor="add-instance-surface">
          <Select
            id="add-instance-surface"
            value={surface}
            onChange={(event) => setSurface(event.target.value)}
            options={SURFACE_OPTIONS}
            disabled={busy}
          />
        </FormRow>
        {Object.entries(templateVars)
          .sort(([a], [b]) => byCodePoint(a, b))
          .map(([template, envName]) => (
            <FormRow key={template} label={envName} htmlFor={`add-instance-var-${template}`}>
              <Input
                id={`add-instance-var-${template}`}
                value={vars[template] ?? ""}
                onChange={(event) => updateVar(template, event.target.value)}
                disabled={busy}
              />
            </FormRow>
          ))}
        <FormRow label="API key environment variable (optional)" htmlFor="add-instance-apikeyenv">
          <Input
            id="add-instance-apikeyenv"
            value={apiKeyEnv}
            onChange={(event) => setApiKeyEnv(event.target.value)}
            placeholder="e.g. PORTKEY_KEY"
            disabled={busy}
          />
        </FormRow>
        <FormRow label="Credential header (optional)" htmlFor="add-instance-credentialheader">
          <Input
            id="add-instance-credentialheader"
            value={credentialHeader}
            onChange={(event) => setCredentialHeader(event.target.value)}
            placeholder="Authorization=Bearer $VAR"
            disabled={busy}
          />
        </FormRow>
        {error && (
          <p className={CLASS.error} role="alert">
            {error}
          </p>
        )}
        <div className={CLASS.actions}>
          {unconfirmedName ? (
            <Button type="button" disabled={busy} onClick={() => void handleCheckAgain()}>
              Check again
            </Button>
          ) : (
            <Button type="submit" disabled={busy}>
              Create
            </Button>
          )}
          <Button type="button" variant="quiet" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export interface ApiKeyDialogProps {
  instance: InstanceEntry;
  onCancel: () => void;
  onSuccess: () => void;
}

interface CredentialValueDialogProps {
  instance: InstanceEntry;
  onCancel: () => void;
  onSuccess: () => void;
  title: string;
  label: string;
  inputId: string;
  placeholder: string;
  successText: string;
  /** "password" for a single-line secret (ApiKeyDialog); "textarea" for a
   * multi-line paste (CredentialJsonDialog). */
  input: "password" | "textarea";
  submit: (name: string, value: string) => Promise<AuthStatusResponse>;
}

// CredentialValueDialog is the submit/refresh/toast/error flow shared by
// ApiKeyDialog and CredentialJsonDialog - a trimmed-empty value silently
// cancels (no RPC), otherwise it calls `submit`, refetches the instance
// list, toasts, and calls onSuccess, or shows the server's rejection inline
// and as a "Save failed" toast. ApiKeyDialog/CredentialJsonDialog are thin
// wrappers that supply this component's copy, field id/kind, and which
// store method `submit` calls - never a second copy of this flow.
function CredentialValueDialog({
  instance,
  onCancel,
  onSuccess,
  title,
  label,
  inputId,
  placeholder,
  successText,
  input,
  submit,
}: CredentialValueDialogProps) {
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToasts();
  const active = useEditorLifetime();

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) {
      onCancel(); // empty submit silently cancels, no RPC
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await submit(instance.name, trimmed);
      if (!active.current) return;
      await credentialsStore.getState().fetch();
      if (!active.current) return;
      toast.push("success", successText);
      onSuccess();
    } catch (err) {
      if (!active.current) return;
      const message = errorText(err);
      setError(message);
      toast.push("error", `Save failed: ${message}`);
    } finally {
      if (active.current) setBusy(false);
    }
  }

  return (
    <Dialog open onClose={onCancel} title={title}>
      <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
        <FormRow label={label} htmlFor={inputId}>
          {input === "textarea" ? (
            <textarea
              id={inputId}
              className={CLASS.textarea}
              rows={8}
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder={placeholder}
              disabled={busy}
              spellCheck={false}
            />
          ) : (
            <Input
              id={inputId}
              type="password"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder={placeholder}
              disabled={busy}
            />
          )}
        </FormRow>
        {error && (
          <p className={CLASS.error} role="alert">
            {error}
          </p>
        )}
        <div className={CLASS.actions}>
          <Button type="submit" disabled={busy}>
            Save
          </Button>
          <Button type="button" variant="quiet" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Set/Replace API key (parity-m7-settings.md §7d) - never echoes any
 * stored value; the field is write-only. Unaffected by the registry
 * cut-over: it only ever reads instance.name. */
export function ApiKeyDialog({ instance, onCancel, onSuccess }: ApiKeyDialogProps) {
  return (
    <CredentialValueDialog
      instance={instance}
      onCancel={onCancel}
      onSuccess={onSuccess}
      title={`Set API key for ${instance.name}`}
      label={`API key for ${instance.name}`}
      inputId="api-key-value"
      placeholder="paste key"
      successText={`API key saved for ${instance.name}`}
      input="password"
      submit={(name, value) => credentialsStore.getState().setApiKey(name, value)}
    />
  );
}

/**
 * CredentialJsonDialog stores a Google credential JSON (a service-account
 * key or an application_default_credentials.json) for a gcp-adc instance via
 * evener/auth/credentialJson/set. The hub validates the paste before it is
 * stored, so a server error here is the parse failure, shown inline.
 */
export function CredentialJsonDialog({ instance, onCancel, onSuccess }: ApiKeyDialogProps) {
  return (
    <CredentialValueDialog
      instance={instance}
      onCancel={onCancel}
      onSuccess={onSuccess}
      title={`Set Google credential JSON for ${instance.name}`}
      label={`Credential JSON for ${instance.name}`}
      inputId="credential-json-value"
      placeholder="paste a service-account key or application_default_credentials.json"
      successText={`Credential JSON saved for ${instance.name}`}
      input="textarea"
      submit={(name, value) => credentialsStore.getState().setCredentialJson(name, value)}
    />
  );
}
