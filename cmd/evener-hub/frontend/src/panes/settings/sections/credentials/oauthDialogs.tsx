// oauthDialogs.tsx: the 2 OAuth continuation editors (parity-m7-settings.md
// §7g/§7h), opened by CredentialsSection's shared "Sign in…"/"Refresh OAuth"
// handler depending on whether evener/auth/device/start signals `fallback`.
//
// DeviceCodeDialog owns its own poll loop entirely internally (a single
// effect keyed by flowId, cleaned up on unmount) rather than a module-level
// timer the legacy uses - CredentialsSection renders this dialog with
// `key={flowId}` so a brand-new flow always gets a fresh mount (and the
// previous flow's effect cleanup already ran, cancelling its timer) before
// this one starts - the same safety property the legacy's own per-tick
// "is openEditor still this exact flow" staleness check exists for, via
// React's own idiomatic mechanism instead of a hand-rolled flag.

import type { AuthDevicePollResponse } from "@evener/appwire-client";
import { CONNECTION_REPLACED_ERROR, errorText } from "@evener/appwire-client";
import { type FormEvent, useEffect, useState } from "react";
import { openInNewTab } from "../../../../shell/openInNewTab";
import { credentialsStore, devicePollOnHost, fetchHost, isStaleListingRefusal } from "../../../../stores/credentials";
import { Button, Dialog, FormRow, Input, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { copyText } from "./clipboard";
import styles from "./oauthDialogs.module.css";
import { refreshListingAfterMutation } from "./reconcileListing";
import { useEditorLifetime } from "./useEditorLifetime";

const CLASS = {
  body: requireClass(styles.body, "oauthDialogs.module.css", "body"),
  help: requireClass(styles.help, "oauthDialogs.module.css", "help"),
  actions: requireClass(styles.actions, "oauthDialogs.module.css", "actions"),
  error: requireClass(styles.error, "oauthDialogs.module.css", "error"),
  code: requireClass(styles.code, "oauthDialogs.module.css", "code"),
  status: requireClass(styles.status, "oauthDialogs.module.css", "status"),
  url: requireClass(styles.url, "oauthDialogs.module.css", "url"),
};

export interface OAuthRedirectDialogProps {
  name: string;
  flowId: string;
  authUrl: string;
  onCancel: () => void;
  onSuccess: () => void;
}

/** The browser-redirect fallback flow's paste-back editor. */
export function OAuthRedirectDialog({ name, flowId, authUrl, onCancel, onSuccess }: OAuthRedirectDialogProps) {
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToasts();
  const active = useEditorLifetime();

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) {
      onCancel(); // empty submit silently cancels, no RPC - matches Set-key's identical rule
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await credentialsStore.getState().loginComplete(name, flowId, trimmed);
      if (!active.current) return;
      await refreshListingAfterMutation();
      if (!active.current) return;
      toast.push("success", `Signed in to ${name}`);
      onSuccess();
    } catch (err) {
      if (!active.current) return;
      // A completion the store refused because the listing on screen belongs to
      // a replaced connection (requireWritableClient): the flow this editor is
      // completing was started on the connection that is gone, so nothing was
      // sent. The pasted URL is not a secret and is kept, so Finish is the
      // retry once this connection's listing lands; what it must not do is show
      // the store's own words or report a failed sign-in.
      if (isStaleListingRefusal(err)) {
        setError(CONNECTION_REPLACED_ERROR);
        toast.push("warning", CONNECTION_REPLACED_ERROR);
        void credentialsStore
          .getState()
          .fetch()
          .catch(() => {});
        return;
      }
      const message = errorText(err);
      setError(message);
      toast.push("error", `Sign-in failed: ${message}`);
    } finally {
      if (active.current) setBusy(false);
    }
  }

  return (
    <Dialog open onClose={onCancel} title={`Sign in to ${name}`}>
      <form className={CLASS.body} onSubmit={(event) => void handleSubmit(event)}>
        <p className={CLASS.help}>
          Authorize in browser, then paste the full redirect URL back here.{" "}
          <a href={authUrl} target="_blank" rel="noopener">
            Re-open authorize URL
          </a>
        </p>
        <FormRow label="Redirect URL" htmlFor="oauth-redirect-url" error={error ?? undefined}>
          <Input
            id="oauth-redirect-url"
            value={value}
            onChange={(event) => setValue(event.target.value)}
            placeholder="https://…"
            disabled={busy}
          />
        </FormRow>
        <div className={CLASS.actions}>
          <Button type="submit" disabled={busy}>
            Finish
          </Button>
          <Button type="button" variant="quiet" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export interface DeviceCodeDialogProps {
  name: string;
  flowId: string;
  userCode: string;
  verificationUrl: string;
  intervalSeconds: number;
  /** When set, this dialog signs in an instance ON that remote host: it polls
   * through evener/host/request (component 07d), refreshes THAT host's own
   * listing, and shows the verification URL as text so the user can complete it
   * from any device (there is no browser on the host). Absent - the controller's
   * own listing - this is exactly today's dialog, using the credential store's
   * plain devicePoll. */
  host?: string;
  onCancel: () => void;
  onSuccess: () => void;
  /** "Start again" - re-runs the same start-flow handler CredentialsSection
   * uses for the row's own "Sign in…" button. */
  onRestart: () => void;
}

/** The device-code flow's editor: shows the code to copy, polls for
 * authorization, and offers Start again once expired/errored. */
export function DeviceCodeDialog({
  name,
  flowId,
  userCode,
  verificationUrl,
  intervalSeconds,
  host,
  onCancel,
  onSuccess,
  onRestart,
}: DeviceCodeDialogProps) {
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const [expired, setExpired] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // True while this connection's listing is what the store is waiting for: the
  // poll is being refused, not answered, and the status line says so.
  const [waitingForConnection, setWaitingForConnection] = useState(false);
  const toast = useToasts();
  const active = useEditorLifetime();

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    // The refusal clears when a listing this connection read is applied, and the
    // usual one is the reconnect's own restore read. One ask is not enough: that
    // read can fail too (the connection dropped again while it was in flight),
    // and a latch that never re-asks would leave every later tick refused with
    // nothing on screen to say so. The in-flight flag keeps a slow read from
    // being asked for twice; the next refused tick asks again.
    let askInFlight = false;
    const delayMs = Math.max(1, intervalSeconds || 5) * 1000;
    // Which hub this flow is driven against: the selected remote host through
    // evener/host/request (component 07d), or this hub's own credential store.
    // Both are the same two RPCs - only the address differs - so the poll loop,
    // its cadence, and its stop conditions stay one implementation.
    const pollOnce = (id: string): Promise<AuthDevicePollResponse> =>
      host === undefined ? credentialsStore.getState().devicePoll(name, id) : devicePollOnHost(host, name, id);

    async function tick(): Promise<void> {
      let resp: AuthDevicePollResponse;
      try {
        resp = await pollOnce(flowId);
      } catch (err) {
        // A poll the store refused because the rows it read on screen belong to
        // a replaced connection (requireWritableClient) is not a poll outcome:
        // the flow lives on the hub, not in this client, and the refusal is the
        // store holding the client back until the replacement's own listing
        // lands. Retry on the normal cadence - the auth state the hub reports
        // once the listing is back is the authority on whether the flow
        // survived - rather than ending a flow that may still be authorizable
        // with "Start again".
        if (isStaleListingRefusal(err)) {
          if (!askInFlight) {
            askInFlight = true;
            void credentialsStore
              .getState()
              .fetch()
              .catch(() => {})
              .finally(() => {
                askInFlight = false;
              });
          }
          // The wait is named rather than silent: the poll keeps retrying, but
          // "Waiting for you to authorize…" would claim the hub is being polled
          // when this client cannot reach it yet.
          if (!cancelled) setWaitingForConnection(true);
          if (!cancelled && active.current) timer = setTimeout(() => void tick(), delayMs);
          return;
        }
        // Mirrors the verified legacy behavior exactly (templates/partials/
        // credentials.html:83-89): a poll-request error attaches its message
        // and does NOT reschedule - polling stops here, same as "expired".
        if (!cancelled) setError(errorText(err));
        return;
      }
      if (cancelled || !active.current) return;
      // The hub answered, so this client is polling the connection again and
      // the status line goes back to what the flow itself is doing.
      setWaitingForConnection(false);
      if (resp.state === "authorized") {
        // The listing that proves the sign-in is the one belonging to the hub
        // that ran the flow: the remote host's own partition, never this hub's.
        if (host === undefined) {
          await refreshListingAfterMutation();
        } else {
          await fetchHost(host);
        }
        if (cancelled || !active.current) return;
        toast.push("success", host === undefined ? `Signed in to ${name}` : `Signed in to ${name} on ${host}`);
        onSuccess();
        return;
      }
      if (resp.state === "expired") {
        setExpired(true);
        setError("Code expired — start again.");
        return;
      }
      timer = setTimeout(() => void tick(), delayMs);
    }

    timer = setTimeout(() => void tick(), delayMs);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
    // onSuccess/toast.push depended on honestly (not suppressed): both are
    // genuinely stable references in this app (onSuccess is a useCallback'd
    // closeEditor at the one real call site, CredentialsSection.tsx; toast.push
    // is useToasts()'s own re-export of a module-level function) - an unstable
    // caller-supplied onSuccess WOULD restart this poll's timer on every
    // parent re-render, which is the actual bug this dependency list guards
    // against, not just a lint nicety.
  }, [name, flowId, intervalSeconds, host, onSuccess, toast.push, active]);

  async function handleCopy(): Promise<void> {
    const ok = await copyText(userCode);
    if (!active.current) return;
    setCopied(true);
    setCopyFailed(!ok);
  }

  function handleOpenVerificationUrl(): void {
    try {
      openInNewTab(verificationUrl);
    } catch (err) {
      // openInNewTab refuses a verification URL that does not parse or is not
      // http(s) by throwing - the loud refusal is what keeps a hostile
      // `javascript:` URL from running in this origin (shell/openInNewTab.ts).
      // React never hands an event-handler throw to an error boundary, so
      // without this catch the refusal would leave a button that silently
      // does nothing; the toast is what makes it visible to the user.
      toast.push("error", `Couldn't open the verification page: ${errorText(err)}`);
    }
  }

  const done = expired || error !== null;
  const statusText =
    error ??
    (waitingForConnection
      ? "Waiting for the connection to be restored…"
      : copyFailed
        ? "Couldn't copy automatically — select the code above and copy it, then continue."
        : "Waiting for you to authorize…");

  return (
    <Dialog open onClose={onCancel} title={host === undefined ? `Sign in to ${name}` : `Sign in to ${name} on ${host}`}>
      <div className={CLASS.body}>
        <p className={CLASS.help}>
          {host === undefined
            ? "Copy this code, then continue to OpenAI and paste it to authorize:"
            : `Copy this code, then open the verification URL below on any device and paste it to authorize the sign-in to ${name} on ${host}:`}
        </p>
        <p className={CLASS.code}>{userCode}</p>
        {/* The URL as text, not only behind the button: the sign-in happens on a
            machine that is not this one, and the user has to be able to read or
            copy the address to reach it from wherever they are. */}
        {host !== undefined && <p className={CLASS.url}>{verificationUrl}</p>}
        <p className={CLASS.status} role="status" aria-live="polite">
          {statusText}
        </p>
        <div className={CLASS.actions}>
          {done ? (
            <Button type="button" onClick={onRestart}>
              Start again
            </Button>
          ) : (
            <>
              <Button type="button" variant="quiet" onClick={() => void handleCopy()}>
                {copied ? "Copied ✓" : "Copy code"}
              </Button>
              <Button type="button" disabled={!copied} onClick={handleOpenVerificationUrl}>
                Send me to OpenAI
              </Button>
            </>
          )}
          <Button type="button" variant="quiet" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
