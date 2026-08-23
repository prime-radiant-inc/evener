/**
 * Full-screen onboarding / add-server flow.
 *
 * With no profiles (or when adding another server) the user sees a clear
 * Evener identity, a primary Scan QR action, and a secondary Paste
 * authorization URL action. The paste field is transient: it holds the URL
 * only until native preview is invoked, then clears immediately. The raw URL
 * or token is never persisted or echoed in errors. After a successful add the
 * screen fires a success haptic and calls `onConnected`.
 *
 * QR scanning uses the native `scanAndPreviewPairing` result directly: the
 * opaque previewId and redacted origin are fed to the store's `previewScan`
 * without fabricating a paste URL or touching a token. On unmount or cancel,
 * the active preview is cancelled so no native preview leaks.
 */
import {
  type ChangeEvent,
  type JSX,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createConnectionStore } from "../state/connection";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import type { ConnectionStore, ShellServices } from "./root-types";

export interface OnboardingScreenProps {
  readonly services: ShellServices;
  /** Optional shared connection store; otherwise this screen owns one. */
  readonly connection?: ConnectionStore;
  readonly onConnected?: () => void;
  readonly onCancel?: () => void;
}

type Phase = "actions" | "previewing" | "confirming" | "error";

export function OnboardingScreen({
  services,
  connection,
  onConnected,
  onCancel,
}: OnboardingScreenProps): JSX.Element {
  const [ownedStore] = useState(() => createConnectionStore(services.profile));
  const store = connection ?? ownedStore;
  const [phase, setPhase] = useState<Phase>("actions");
  const [pasteUrl, setPasteUrl] = useState("");
  const [serverName, setServerName] = useState("");
  const [consent, setConsent] = useState(false);
  const [confirmError, setConfirmError] = useState<string | null>(null);
  const [pendingSave, setPendingSave] = useState(false);
  const mounted = useRef(true);
  const refreshPromise = useRef<Promise<void> | null>(null);

  const preview = store((s) => s.preview);
  const previewError = store((s) => s.previewError);
  const profiles = store((s) => s.profiles);

  // Cancel the active preview on unmount so no native preview leaks.
  useEffect(() => {
    mounted.current = true;
    // Load profiles on mount so the name uniqueness check has data. The
    // promise is captured so the scan callback can await it before calling
    // the native scan — ensuring the scan doesn't fire before profiles load
    // and, critically, doesn't fire after unmount if refresh is still pending.
    refreshPromise.current = store.getState().refresh();
    return () => {
      mounted.current = false;
      void store.getState().cancelPreview();
    };
  }, [store]);

  const nameError = useMemo(() => {
    const trimmed = serverName.trim();
    if (trimmed === "") return null;
    const target = trimmed.toLowerCase();
    for (const p of profiles) {
      if (p.name.toLowerCase() === target) {
        return "server name must be unique";
      }
    }
    return null;
  }, [serverName, profiles]);

  const nameValid = serverName.trim() !== "" && nameError === null;

  const duplicateOrigin = useMemo(() => {
    if (preview === null) return false;
    return profiles.some((p) => p.origin === preview.origin);
  }, [preview, profiles]);

  const canSave =
    preview !== null &&
    nameValid &&
    (!duplicateOrigin || consent) &&
    !pendingSave;

  const handlePasteFromClipboard = useCallback(async () => {
    try {
      const text = await services.native.clipboardPaste();
      if (text) {
        setPasteUrl(text);
      }
    } catch {
      // best-effort: ignore errors
    }
  }, [services]);

  // Clear paste input synchronously after dispatching preview.
  const handlePaste = useCallback(() => {
    const raw = pasteUrl;
    setPasteUrl("");
    setPhase("previewing");
    setConfirmError(null);
    // Start previewPaste immediately — it increments the store generation
    // synchronously at user intent. No refresh is needed for paste: the
    // name uniqueness check runs at confirmPairing time (loadProfiles). If
    // the user cancels, cancelPreview invalidates the generation and the
    // paste result is never published.
    void store
      .getState()
      .previewPaste(raw)
      .then(() => {
        if (mounted.current && store.getState().preview !== null) {
          setPhase("confirming");
        }
      });
  }, [pasteUrl, store]);

  // QR scan: use the native result directly — no fabricated paste URL.
  const handleScan = useCallback(async () => {
    setPhase("previewing");
    setConfirmError(null);
    // Establish the store generation synchronously at user intent, before
    // awaiting refresh. previewScan increments the generation immediately;
    // a late refresh cannot reverse click order or launch after cancel.
    const scanP = store.getState().previewScan(async (context) => {
      // Await the mount refresh BEFORE calling the native scan. If unmount
      // happens during refresh, mounted.current is false and the native
      // scan is NOT called. The store generation guard also rejects any
      // late result if cancelled.
      await (refreshPromise.current ?? Promise.resolve());
      // If unmounted/cancelled during refresh, don't call the native scan.
      // The store's generation guard will best-effort cancel any late result.
      if (!mounted.current || !context.isCurrent()) return null;
      const scanResult = await services.native.scanAndPreviewPairing();
      return { previewId: scanResult.previewId, origin: scanResult.origin };
    });
    try {
      await scanP;
      if (mounted.current && store.getState().preview !== null) {
        setPhase("confirming");
      }
    } catch {
      if (mounted.current) setPhase("error");
    }
  }, [services, store]);

  const handleSave = useCallback(async () => {
    if (!canSave || preview === null) return;
    setPendingSave(true);
    setConfirmError(null);
    try {
      await store
        .getState()
        .confirmPairing(preview.previewId, serverName.trim(), consent);
      // Haptic is best-effort — pairing success commits and navigates
      // even if the haptic call rejects.
      void services.native.hapticPerform("notificationSuccess").catch(() => {});
      onConnected?.();
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause);
      setConfirmError(message);
      setPhase("error");
    } finally {
      setPendingSave(false);
    }
  }, [canSave, preview, serverName, consent, store, services, onConnected]);

  const handleNameChange = (e: ChangeEvent<HTMLInputElement>) => {
    setServerName(e.target.value);
    setConsent(false);
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" && canSave) {
      handleSave();
    }
  };

  // Cancel invalidates the preview before calling the parent, so a late
  // preview result never publishes after the user navigates away.
  const handleCancel = useCallback(() => {
    void store.getState().cancelPreview();
    onCancel?.();
  }, [store, onCancel]);

  const showConfirm = preview !== null && phase !== "previewing";
  const showActions = !showConfirm;

  return (
    <main className="evener-onboarding">
      <h1 className="evener-onboarding__identity">Evener</h1>
      <p>Connect to a Hub to manage sessions from your phone.</p>

      {showActions ? (
        <>
          <Button variant="primary" onClick={handleScan}>
            Scan QR Code
          </Button>
          <p className="evener-onboarding__divider">or</p>
          <Input
            label="Authorization URL"
            name="auth-url"
            placeholder="https://hub.example.com/authorization"
            value={pasteUrl}
            onChange={(e) => setPasteUrl(e.target.value)}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
          />
          <Button variant="tertiary" onClick={handlePasteFromClipboard}>
            Paste from Clipboard
          </Button>
          <Button
            variant="secondary"
            onClick={handlePaste}
            disabled={pasteUrl.trim() === ""}
          >
            Connect
          </Button>
          <p className="evener-onboarding__warning">
            Plain HTTP is only safe on a trusted local network — anyone on that
            network can observe the bearer capability.
          </p>
          {previewError !== null ? (
            <p role="alert">
              {previewError} — unable to preview the authorization URL.
            </p>
          ) : null}
          {onCancel ? (
            <Button variant="tertiary" onClick={handleCancel}>
              Cancel
            </Button>
          ) : null}
        </>
      ) : null}

      {showConfirm && preview !== null ? (
        <>
          <p>
            Confirm connection to <strong>{preview.origin}</strong>
          </p>
          {preview.isPrivateNetwork ? (
            <p role="alert">
              This is an HTTP origin on a private network. The bearer capability
              is visible to anyone on that network.
            </p>
          ) : null}
          <Input
            label="Server name"
            name="server-name"
            placeholder="my hub"
            value={serverName}
            onChange={handleNameChange}
            onKeyDown={handleKeyDown}
          />
          {nameError !== null ? <p role="alert">{nameError}</p> : null}
          {duplicateOrigin ? (
            <label>
              <p>
                You already have a server at this origin. Confirm to add a
                second credential for this server.
              </p>
              <input
                type="checkbox"
                aria-label="confirm duplicate origin second credential"
                checked={consent}
                onChange={(e) => setConsent(e.target.checked)}
              />
            </label>
          ) : null}
          <Button variant="primary" disabled={!canSave} onClick={handleSave}>
            Connect
          </Button>
          {onCancel ? (
            <Button variant="tertiary" onClick={handleCancel}>
              Cancel
            </Button>
          ) : null}
          {confirmError !== null ? (
            <p role="alert">{confirmError} — unable to complete pairing.</p>
          ) : null}
        </>
      ) : null}
    </main>
  );
}
