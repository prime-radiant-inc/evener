// instanceDialogs.tsx: the per-instance secret-entry dialogs (spec
// 2026-09-07 §2): Set/Replace API key and credential JSON.
// Editing an existing instance lives in InstanceSheet. Each owns its own
// client validation, store call, inline error, and toast; the parent
// (CredentialsSection) only needs to close the single open editor via
// `onSuccess`/`onCancel` - it never has to distinguish success from failure
// itself.

import type { AuthStatusResponse, InstanceEntry } from "@evener/appwire-client";
import {
  CONNECTION_REPLACED_ERROR,
  errorText,
  FINGERPRINT_UNAVAILABLE_ERROR,
  isEndpointConflict,
} from "@evener/appwire-client";
import { type FormEvent, useLayoutEffect, useRef, useState } from "react";
import { credentialsStore, isStaleListingRefusal } from "../../../../stores/credentials";
import { Button, Dialog, FormRow, InlineNotice, Input, Textarea, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { FormError } from "./formError";
import styles from "./instanceDialogs.module.css";
import { refreshListingAfterMutation } from "./reconcileListing";
import { useEditorLifetime } from "./useEditorLifetime";

const CLASS = {
  body: requireClass(styles.body, "instanceDialogs.module.css", "body"),
  actions: requireClass(styles.actions, "instanceDialogs.module.css", "actions"),
};

// The endpoint a credential dialog was opened against is the one the typed
// value belongs to. A concurrent change can put a different instance under the
// same name while the field holds the secret - re-resolving the name at submit
// time and asserting whatever it now points at would send that value to an
// endpoint the user never reviewed. The dialog compares the row's current
// fingerprint against the one captured when it opened, refuses the write, and
// clears the field; the same refusal the guided flow and the instance sheet
// make when their destination moves. The refusal is actionable rather than
// terminal: it re-anchors the dialog's expectation to the row now on screen,
// so the re-entry the message asks for saves against the destination the user
// can see.
const ENDPOINT_CHANGED_ERROR =
  "This connection changed to a different endpoint. Check its destination and enter the value again.";

// The same refusal whose re-read finds no row for the name: the destination is
// not merely different, it is gone from the listing, so there is nothing on
// screen to re-anchor the assertion to. The value is dropped and said so. The
// assertion itself stays where the user last reviewed it rather than being
// emptied - an empty one would let the retype land on whatever the name
// resolves to next, unverified, which is the save the guard above refuses.
const ENDPOINT_VANISHED_ERROR =
  "This connection changed and is no longer in the provider list, so the value was not saved. Reopen this editor from the current list to continue.";

/** Why a credential value was not saved, as the dialog's alert's data-reason:
 * the row's endpoint changed, it has a destination the hub cannot fingerprint,
 * it left the listing, the connection was replaced, or the hub failed it. */
type CredentialValueErrorReason =
  | "endpoint-changed"
  | "fingerprint-unavailable"
  | "endpoint-vanished"
  | "connection-replaced"
  | "save-failed";
export interface ApiKeyDialogProps {
  instance: InstanceEntry;
  /** The endpoint fingerprint the dialog was opened against, captured from the
   * row the user acted on. A submit asserts this value, so a concurrent
   * endpoint change cannot re-target the already-entered secret.
   * When a change is detected the dialog re-anchors to the row now on screen,
   * the destination the user reviews before re-entering the value; see
   * CredentialValueDialog.handleSubmit.
   * Required (and explicitly undefined when the row showed no endpoint, which
   * is the legitimate "nothing was shown, nothing to assert" case) rather than
   * optional, so a caller that forgets to capture it is a build error instead
   * of a save that silently asserts nothing. Only a capture and a row that are
   * both undefined compare equal: a row that gained a fingerprint is a change
   * to refuse like any other, since an empty assertion carries no endpoint for
   * the hub to check and would land the value unverified. */
  expectedEndpointFingerprint: string | undefined;
  onCancel: () => void;
  onSuccess: () => void;
}

interface CredentialValueDialogProps {
  instance: InstanceEntry;
  expectedEndpointFingerprint: string | undefined;
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
  /** Writes the typed value, asserting the endpoint this submit belongs to.
   * Handed the dialog's effective expectation rather than closing over the
   * captured prop, so a value re-entered after a refusal asserts the row now
   * on screen. */
  submit: (name: string, value: string, expectedEndpointFingerprint: string | undefined) => Promise<AuthStatusResponse>;
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
  expectedEndpointFingerprint,
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
  const [error, setError] = useState<FormError<CredentialValueErrorReason> | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToasts();
  const active = useEditorLifetime();
  // The expectation the next submit asserts. Seeded from the caller's capture
  // - the endpoint the row showed when the editor opened - and re-anchored to
  // the row now on screen when the guard refuses (see handleSubmit). Resynced
  // when the prop changes: a caller that re-captures while this component
  // stays mounted starts from its new capture, not a refusal from before.
  const expected = useRef(expectedEndpointFingerprint);
  useLayoutEffect(() => {
    expected.current = expectedEndpointFingerprint;
  }, [expectedEndpointFingerprint]);

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) {
      onCancel(); // empty submit silently cancels, no RPC
      return;
    }
    // The typed value belongs to the endpoint this dialog opened against. If
    // the name now resolves to a different one, refuse before any RPC, drop
    // the value, and say why: a save here would assert an endpoint the user
    // never reviewed and could write the secret somewhere they did not choose.
    // The refusal re-anchors the expectation to the row now on screen, so the
    // re-entry the error asks for saves against the destination the user can
    // see instead of being refused forever against the endpoint that is gone.
    // Re-anchoring is safe: that row is the destination the dialog renders,
    // the value was cleared so proceeding takes a deliberate retype of the
    // secret, and the hub re-checks the assertion under its credential lock at
    // write time, so a second change refuses again.
    // An undefined capture is no exemption: a row that gained a fingerprint
    // while the dialog sat open is a change to refuse too. An empty assertion
    // carries no endpoint for the hub to check - it accepts one while it can
    // key a fingerprint of its own, so a row that cannot produce one would
    // otherwise save unverified - and the same local refusal re-anchors the
    // expectation to the row now on screen, so the retype saves against the
    // destination the user can see. A capture and a row that are both undefined
    // still compare equal, so the "nothing shown, nothing to assert" case is
    // unchanged.
    const expectedFingerprint = expected.current;
    if (instance.endpointFingerprint !== expectedFingerprint) {
      expected.current = instance.endpointFingerprint;
      setValue("");
      setError({ reason: "endpoint-changed", message: ENDPOINT_CHANGED_ERROR });
      return;
    }
    // The hub serves a fingerprint only while it can key one, so a row that
    // carries an endpoint and shows none is a destination the hub cannot
    // describe right now - the state a key outage leaves, and the state a form
    // opened during one keeps if the listing has not refreshed since. Submitting
    // here would assert nothing against an endpoint nobody checked, so it is
    // refused locally, with the same "review its destination" remedy: the
    // listing that carries the fingerprint again is what makes the save work.
    // The wire omits an unavailable fingerprint (omitempty), so "shows none"
    // arrives as undefined as well as ""; both spellings are checked, the
    // absent-is-empty convention InstanceSheet.fieldValue documents. baseUrl is
    // the row's "has a destination" field: the listing suppresses it for a
    // hidden or destinationless row, so an empty baseUrl means there is nothing
    // for the hub to check and the save may proceed with no assertion.
    if ((instance.baseUrl ?? "") !== "" && (instance.endpointFingerprint ?? "") === "") {
      setError({ reason: "fingerprint-unavailable", message: FINGERPRINT_UNAVAILABLE_ERROR });
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await submit(instance.name, trimmed, expectedFingerprint);
      if (!active.current) return;
      await refreshListingAfterMutation();
      if (!active.current) return;
      toast.push("success", successText);
      onSuccess();
    } catch (err) {
      if (!active.current) return;
      // A hub refusal of the asserted destination, or the store's own refusal
      // of a write issued from the previous connection's listing
      // (stores/credentials.ts's requireWritableClient), is the change the
      // guard above makes locally when the client's listing already reflects
      // it - the window the client cannot see is the one between that listing
      // and this write, or the connection that replaced it. Neither is a save
      // failure, and the value must not be sent again against an endpoint
      // nobody can vouch for: drop it, re-read the listing, and re-anchor to
      // the row now on screen, so the retype the message asks for saves against
      // the destination the user can review. ProviderConnection recovers a
      // refused assertion the same way.
      const replaced = isStaleListingRefusal(err);
      if (isEndpointConflict(err) || replaced) {
        await recoverChangedEndpoint(
          replaced
            ? { reason: "connection-replaced", message: CONNECTION_REPLACED_ERROR }
            : { reason: "endpoint-changed", message: ENDPOINT_CHANGED_ERROR },
        );
        return;
      }
      const message = errorText(err);
      setError({ reason: "save-failed", message });
      toast.push("error", `Save failed: ${message}`);
    } finally {
      if (active.current) setBusy(false);
    }
  }

  // The recovery a refused assertion gets, mirroring the guided flow's
  // recoverChangedEndpoint: re-read the listing, drop the value typed for the
  // endpoint that is gone, and re-anchor the expectation to the row the
  // listing now holds for this name. The read owns its own failure
  // (refreshListingAfterMutation), so a dropped connection leaves the last
  // listing this client saw in place - the only expectation it can honestly
  // re-anchor to. The re-anchor is safe for the same reason the local guard's
  // is: the row it names is the destination the dialog renders, the value was
  // cleared so proceeding takes a deliberate retype, and the hub re-checks the
  // assertion under its credential lock at write time, so a second change is
  // refused again instead of landing anywhere new.
  // changed names the change that was refused - the hub's endpoint
  // conflict, or the store's replacement of the connection - because the two
  // differ in what the user has to check, and the caller is the one that knows
  // which refused it.
  async function recoverChangedEndpoint(changed: FormError<CredentialValueErrorReason>): Promise<void> {
    await refreshListingAfterMutation();
    if (!active.current) return;
    setValue("");
    const row = credentialsStore.getState().instances.find((candidate) => candidate.name === instance.name);
    if (!row) {
      // No row to re-anchor to: the name is gone from the listing, so there is
      // no destination on screen to assert and nothing this dialog can retry
      // against. Report the change and the dropped value rather than keep a
      // stale assertion or dress the refusal up as a failed save.
      setError({ reason: "endpoint-vanished", message: ENDPOINT_VANISHED_ERROR });
      return;
    }
    expected.current = row.endpointFingerprint;
    setError(changed);
  }

  return (
    <Dialog open onClose={onCancel} title={title}>
      <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
        <FormRow label={label} htmlFor={inputId}>
          {input === "textarea" ? (
            <Textarea
              id={inputId}
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder={placeholder}
              disabled={busy}
              rows={8}
              monospace
              fill
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
          <InlineNotice tone="danger" data-reason={error.reason}>
            {error.message}
          </InlineNotice>
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
export function ApiKeyDialog({ instance, expectedEndpointFingerprint, onCancel, onSuccess }: ApiKeyDialogProps) {
  return (
    <CredentialValueDialog
      instance={instance}
      expectedEndpointFingerprint={expectedEndpointFingerprint}
      onCancel={onCancel}
      onSuccess={onSuccess}
      title={`Set API key for ${instance.name}`}
      label={`API key for ${instance.name}`}
      inputId="api-key-value"
      placeholder="paste key"
      successText={`API key saved for ${instance.name}`}
      input="password"
      submit={(name, value, expectedFingerprint) =>
        credentialsStore.getState().setApiKey(name, value, expectedFingerprint)
      }
    />
  );
}

/**
 * CredentialJsonDialog stores a Google credential JSON (a service-account
 * key or an application_default_credentials.json) for a gcp-adc instance via
 * evener/auth/credentialJson/set. The hub validates the paste before it is
 * stored, so a server error here is the parse failure, shown inline.
 */
export function CredentialJsonDialog({
  instance,
  expectedEndpointFingerprint,
  onCancel,
  onSuccess,
}: ApiKeyDialogProps) {
  return (
    <CredentialValueDialog
      instance={instance}
      expectedEndpointFingerprint={expectedEndpointFingerprint}
      onCancel={onCancel}
      onSuccess={onSuccess}
      title={`Set Google credential JSON for ${instance.name}`}
      label={`Credential JSON for ${instance.name}`}
      inputId="credential-json-value"
      placeholder="paste a service-account key or application_default_credentials.json"
      successText={`Credential JSON saved for ${instance.name}`}
      input="textarea"
      submit={(name, value, expectedFingerprint) =>
        credentialsStore.getState().setCredentialJson(name, value, expectedFingerprint)
      }
    />
  );
}
