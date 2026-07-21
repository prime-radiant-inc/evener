// oauthDialogs.tsx: the 2 OAuth continuation editors (parity-m7-settings.md
// §7g/§7h), opened by CredentialsSection's shared "Sign in…"/"Refresh OAuth"
// handler depending on whether serf/auth/device/start signals `fallback`.
//
// DeviceCodeDialog owns its own poll loop entirely internally (a single
// effect keyed by flowId, cleaned up on unmount) rather than a module-level
// timer the legacy uses - CredentialsSection renders this dialog with
// `key={flowId}` so a brand-new flow always gets a fresh mount (and the
// previous flow's effect cleanup already ran, cancelling its timer) before
// this one starts - the same safety property the legacy's own per-tick
// "is openEditor still this exact flow" staleness check exists for, via
// React's own idiomatic mechanism instead of a hand-rolled flag.
import { type FormEvent, useEffect, useState } from "react";
import type { AuthDevicePollResponse } from "../../../../protocol/types.gen";
import { credentialsStore } from "../../../../stores/credentials";
import { Button, Dialog, FormRow, Input, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { copyText } from "./clipboard";
import styles from "./oauthDialogs.module.css";

const CLASS = {
  body: requireClass(styles.body, "oauthDialogs.module.css", "body"),
  help: requireClass(styles.help, "oauthDialogs.module.css", "help"),
  actions: requireClass(styles.actions, "oauthDialogs.module.css", "actions"),
  error: requireClass(styles.error, "oauthDialogs.module.css", "error"),
  code: requireClass(styles.code, "oauthDialogs.module.css", "code"),
  status: requireClass(styles.status, "oauthDialogs.module.css", "status"),
};

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

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
      await credentialsStore.getState().fetch();
      toast.push("success", `Signed in to ${name}`);
      onSuccess();
    } catch (err) {
      const message = errorMessage(err);
      setError(message);
      toast.push("error", `Sign-in failed: ${message}`);
    } finally {
      setBusy(false);
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
  onCancel,
  onSuccess,
  onRestart,
}: DeviceCodeDialogProps) {
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const [expired, setExpired] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const toast = useToasts();

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const delayMs = Math.max(1, intervalSeconds || 5) * 1000;

    async function tick(): Promise<void> {
      let resp: AuthDevicePollResponse;
      try {
        resp = await credentialsStore.getState().devicePoll(name, flowId);
      } catch (err) {
        // Mirrors the verified legacy behavior exactly (templates/partials/
        // credentials.html:83-89): a poll-request error attaches its message
        // and does NOT reschedule - polling stops here, same as "expired".
        if (!cancelled) setError(errorMessage(err));
        return;
      }
      if (cancelled) return;
      if (resp.state === "authorized") {
        await credentialsStore.getState().fetch();
        toast.push("success", `Signed in to ${name}`);
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
  }, [name, flowId, intervalSeconds, onSuccess, toast.push]);

  async function handleCopy(): Promise<void> {
    const ok = await copyText(userCode);
    setCopied(true);
    setCopyFailed(!ok);
  }

  const done = expired || error !== null;
  const statusText =
    error ??
    (copyFailed
      ? "Couldn't copy automatically — select the code above and copy it, then continue."
      : "Waiting for you to authorize…");

  return (
    <Dialog open onClose={onCancel} title={`Sign in to ${name}`}>
      <div className={CLASS.body}>
        <p className={CLASS.help}>Copy this code, then continue to OpenAI and paste it to authorize:</p>
        <p className={CLASS.code}>{userCode}</p>
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
              <Button
                type="button"
                disabled={!copied}
                onClick={() => window.open(verificationUrl, "_blank", "noopener")}
              >
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
