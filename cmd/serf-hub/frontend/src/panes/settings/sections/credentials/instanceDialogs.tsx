// instanceDialogs.tsx: the 3 instance-CRUD editors (parity-m7-settings.md
// §7d-§7f) - Add, Edit, and Set/Replace API key. Each owns its own client
// validation, store call, inline error, and toast; the parent
// (CredentialsSection) only needs to close the single open editor via
// `onSuccess`/`onCancel` - it never has to distinguish success from failure
// itself.
import { type FormEvent, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { credentialsStore } from "../../../../stores/credentials";
import { Button, Dialog, FormRow, Input, RadioGroup, Select, type SelectOption, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./instanceDialogs.module.css";

const CLASS = {
  body: requireClass(styles.body, "instanceDialogs.module.css", "body"),
  actions: requireClass(styles.actions, "instanceDialogs.module.css", "actions"),
  error: requireClass(styles.error, "instanceDialogs.module.css", "error"),
};

const API_STYLE_OPTIONS = [
  { value: "responses", label: "responses" },
  { value: "chat-completions", label: "chat-completions" },
];

export interface AddInstanceDialogProps {
  availableTypes: string[];
  onCancel: () => void;
  onSuccess: () => void;
}

/** The global "+ Add provider instance" form (parity-m7-settings.md §7f). */
export function AddInstanceDialog({ availableTypes, onCancel, onSuccess }: AddInstanceDialogProps) {
  const [type, setType] = useState("");
  const [name, setName] = useState("");
  const [apiStyle, setApiStyle] = useState("responses");
  const [baseUrl, setBaseUrl] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToasts();

  const typeOptions: SelectOption[] = [
    { value: "", label: "" },
    ...availableTypes.map((t) => ({ value: t, label: t })),
  ];

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (!type) {
      setError("Type is required.");
      return;
    }
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("Name is required.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      // apiStyle only applies to type openai; forced to "" for every other
      // type even if a stale radio selection exists from a previous Type
      // choice - the backend rejects apiStyle on non-openai types.
      await credentialsStore.getState().create({
        type,
        name: trimmedName,
        apiStyle: type === "openai" ? apiStyle : "",
        baseUrl: baseUrl.trim(),
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
        <FormRow label="Type" htmlFor="add-instance-type">
          <Select
            id="add-instance-type"
            value={type}
            onChange={(event) => setType(event.target.value)}
            options={typeOptions}
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
        {type === "openai" && (
          <RadioGroup label="API style" value={apiStyle} onChange={setApiStyle} options={API_STYLE_OPTIONS} />
        )}
        <FormRow label="Base URL (optional)" htmlFor="add-instance-baseurl">
          <Input
            id="add-instance-baseurl"
            value={baseUrl}
            onChange={(event) => setBaseUrl(event.target.value)}
            placeholder="https://…"
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

/** The per-row Edit form (parity-m7-settings.md §7e): API-style only for
 * openai instances, Base URL always. */
export function EditInstanceDialog({ instance, onCancel, onSuccess }: EditInstanceDialogProps) {
  const showApiStyle = instance.type === "openai";
  const [apiStyle, setApiStyle] = useState(instance.apiStyle || "");
  const [baseUrl, setBaseUrl] = useState(instance.baseUrl || "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToasts();

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setError(null);
    setBusy(true);
    try {
      await credentialsStore.getState().edit({
        name: instance.name,
        apiStyle: showApiStyle ? apiStyle : "",
        baseUrl: baseUrl.trim(),
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
        {showApiStyle && (
          <RadioGroup label="API style" value={apiStyle} onChange={setApiStyle} options={API_STYLE_OPTIONS} />
        )}
        <FormRow label="Base URL (optional)" htmlFor="edit-instance-baseurl">
          <Input
            id="edit-instance-baseurl"
            value={baseUrl}
            onChange={(event) => setBaseUrl(event.target.value)}
            placeholder="https://…"
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

/** Set/Replace API key (parity-m7-settings.md §7d) - never echoes any
 * stored value; the field is write-only. */
export function ApiKeyDialog({ instance, onCancel, onSuccess }: ApiKeyDialogProps) {
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
      await credentialsStore.getState().setApiKey(instance.name, trimmed);
      await credentialsStore.getState().fetch();
      toast.push("success", `API key saved for ${instance.name}`);
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
    <Dialog open onClose={onCancel} title={`Set API key for ${instance.name}`}>
      <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
        <FormRow label={`API key for ${instance.name}`} htmlFor="api-key-value">
          <Input
            id="api-key-value"
            type="password"
            value={value}
            onChange={(event) => setValue(event.target.value)}
            placeholder="paste key"
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
