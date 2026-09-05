// instanceDialogs.tsx: the 3 instance-CRUD editors (parity-m7-settings.md
// §7d-§7f) - Add, Edit, and Set/Replace API key. Each owns its own client
// validation, store call, inline error, and toast; the parent
// (CredentialsSection) only needs to close the single open editor via
// `onSuccess`/`onCancel` - it never has to distinguish success from failure
// itself.
//
// Updated for the provider registry's instance shape (spec §11.3): Type
// becomes Base provider over availableProviders, the openai-only API-style
// radio is gone (Protocol is no longer openai-specific data the form
// special-cases), and the Add form gains a dynamic Input per the selected
// provider's VarsEnv entry plus api-key-env/credential-header fields
// mirroring the CLI's --api-key-env/--credential-header flags (§11.2).
// VarsEnv maps template placeholder name -> environment variable name
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
  onCancel: () => void;
  onSuccess: () => void;
}

/** The global "+ Add provider instance" form (parity-m7-settings.md §7f). */
export function AddInstanceDialog({ availableProviders, onCancel, onSuccess }: AddInstanceDialogProps) {
  const [base, setBase] = useState("");
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [vars, setVars] = useState<Record<string, string>>({});
  const [apiKeyEnv, setApiKeyEnv] = useState("");
  const [credentialHeader, setCredentialHeader] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToasts();

  const baseOptions: SelectOption[] = [
    { value: "", label: "" },
    ...availableProviders.map((p) => ({ value: p.id, label: p.name || p.id })),
  ];
  const varsEnv = availableProviders.find((p) => p.id === base)?.varsEnv ?? {};

  function handleBaseChange(nextBase: string): void {
    setBase(nextBase);
    setVars({}); // a var input from the previous base must not leak into the new one
  }

  function updateVar(template: string, value: string): void {
    setVars((current) => ({ ...current, [template]: value }));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
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
      await credentialsStore.getState().create({
        name: trimmedName,
        base,
        baseUrl: baseUrl.trim(),
        vars: nonEmptyVars(vars),
        apiKeyEnv: apiKeyEnv.trim() || undefined,
        credentialHeader: trimmedCredentialHeader || undefined,
      });
      toast.push("success", `Created instance ${trimmedName}`);
      onSuccess();
    } catch (err) {
      const message = errorText(err);
      setError(message);
      toast.push("error", `Create failed: ${message}`);
    } finally {
      setBusy(false);
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
        {Object.entries(varsEnv)
          .sort(([a], [b]) => a.localeCompare(b))
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
          <Button type="submit" disabled={busy}>
            Create
          </Button>
          <Button type="button" variant="quiet" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export interface EditInstanceDialogProps {
  instance: InstanceEntry;
  onCancel: () => void;
  onSuccess: () => void;
}

/** The Edit form (parity-m7-settings.md §7e, updated for the registry's
 * instance shape): Base URL only, sent only when it actually changed.
 * InstanceEditParams also carries protocol/surface/vars overrides, but the
 * pane's only spec-mandated way to set those is the Add form's
 * provider-driven fields (spec §11.3 only calls out VarsEnv driving the add
 * form) - Edit's job is nudging an existing instance's endpoint, not
 * re-deriving its whole shape, and editing an implicit instance already
 * writes a shadow that carries only what changed here. */
export function EditInstanceDialog({ instance, onCancel, onSuccess }: EditInstanceDialogProps) {
  const [baseUrl, setBaseUrl] = useState(instance.baseUrl || "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToasts();

  // InstanceEditParams keeps baseUrl's old "empty means unchanged" meaning
  // (v3, unchanged by #711) and adds clearBaseUrl as an additive clear
  // signal: an old hub ignores the unknown field and treats the request as
  // an ordinary no-op, never a silent wrong clear. Emptying a field that
  // had a value takes that clear path below, so note what saving will do.
  const trimmedBaseUrl = baseUrl.trim();
  const displayedBaseUrl = instance.baseUrl || "";
  const clearingBaseUrl = Boolean(instance.baseUrl) && trimmedBaseUrl === "";

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await credentialsStore.getState().edit({
        name: instance.name,
        baseUrl: trimmedBaseUrl !== displayedBaseUrl && trimmedBaseUrl !== "" ? trimmedBaseUrl : undefined,
        clearBaseUrl: clearingBaseUrl ? true : undefined,
      });
      toast.push("success", `Saved ${instance.name}`);
      onSuccess();
    } catch (err) {
      const message = errorText(err);
      setError(message);
      toast.push("error", `Edit failed: ${message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onClose={onCancel} title={`Edit ${instance.name}`}>
      <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
        <FormRow label="Base URL (optional)" htmlFor="edit-instance-baseurl">
          <Input
            id="edit-instance-baseurl"
            value={baseUrl}
            onChange={(event) => setBaseUrl(event.target.value)}
            placeholder="https://…"
            disabled={busy}
          />
        </FormRow>
        {clearingBaseUrl && (
          <p className={CLASS.error} role="status">
            Resets the endpoint to the provider's default.
          </p>
        )}
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
      await credentialsStore.getState().fetch();
      toast.push("success", successText);
      onSuccess();
    } catch (err) {
      const message = errorText(err);
      setError(message);
      toast.push("error", `Save failed: ${message}`);
    } finally {
      setBusy(false);
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
