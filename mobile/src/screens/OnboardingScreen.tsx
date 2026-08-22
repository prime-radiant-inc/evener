/**
 * Full-screen onboarding / add-server flow.
 *
 * With no profiles (or when adding another server) the user sees a clear
 * Evener identity, a primary Scan QR action, and a secondary Paste
 * authorization URL action. The paste field is transient: it holds the URL
 * only until native preview is invoked, then clears immediately. The raw URL
 * or token is never persisted or echoed in errors. After a successful add the
 * screen fires a success haptic and calls `onConnected`.
 */
import {
  type ChangeEvent,
  type JSX,
  type KeyboardEvent,
  useCallback,
  useMemo,
  useState,
} from "react";
import { createConnectionStore } from "../state/connection";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import type { OnboardingServiceBundle } from "./fixture-services";

export interface OnboardingScreenProps {
  readonly services: OnboardingServiceBundle;
  readonly onConnected?: () => void;
}

type Phase = "actions" | "previewing" | "confirming" | "error";

export function OnboardingScreen({
  services,
  onConnected,
}: OnboardingScreenProps): JSX.Element {
  const [store] = useState(() => createConnectionStore(services.profile));
  const [phase, setPhase] = useState<Phase>("actions");
  const [pasteUrl, setPasteUrl] = useState("");
  const [serverName, setServerName] = useState("");
  const [consent, setConsent] = useState(false);
  const [confirmError, setConfirmError] = useState<string | null>(null);
  const [pendingSave, setPendingSave] = useState(false);

  const preview = store((s) => s.preview);
  const previewError = store((s) => s.previewError);
  const profiles = store((s) => s.profiles);

  // Validate the server name against existing profiles (derived, not stored).
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

  // Duplicate origin detection for the consent prompt.
  const duplicateOrigin = useMemo(() => {
    if (preview === null) return false;
    return profiles.some((p) => p.origin === preview.origin);
  }, [preview, profiles]);

  const canSave =
    preview !== null &&
    nameValid &&
    (!duplicateOrigin || consent) &&
    !pendingSave;

  // Clear paste input synchronously after dispatching preview.
  const handlePaste = useCallback(() => {
    const raw = pasteUrl;
    setPasteUrl("");
    setPhase("previewing");
    setConfirmError(null);
    void store
      .getState()
      .refresh()
      .then(() =>
        store
          .getState()
          .previewPaste(raw)
          .then(() => {
            setPhase("confirming");
          }),
      );
  }, [pasteUrl, store]);

  const handleScan = useCallback(async () => {
    setPhase("previewing");
    setConfirmError(null);
    try {
      await store.getState().refresh();
      const scanPreview = await services.native.scanAndPreviewPairing();
      await store
        .getState()
        .previewPaste(`${scanPreview.origin}/auth?token=redacted-scan-token`);
      setPhase("confirming");
    } catch {
      setPhase("error");
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
      await services.native.hapticPerform("notificationSuccess");
      onConnected?.();
    } catch {
      setConfirmError("pairing failed");
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

  const showConfirm = preview !== null && phase !== "previewing";
  const showActions = !showConfirm;

  return (
    <main className="evener-onboarding">
      <h1 className="evener-onboarding__identity">Evener</h1>
      <p>
        Connect to a Hub to manage sessions from your phone. Plain HTTP belongs
        only on a trusted private network — anyone on that network can observe
        the bearer capability.
      </p>

      {showActions ? (
        <>
          <Button variant="primary" onClick={handleScan}>
            Scan QR
          </Button>
          <Input
            label="Paste authorization URL"
            name="auth-url"
            placeholder="https://hub.example.com/auth?token=…"
            value={pasteUrl}
            onChange={(e) => setPasteUrl(e.target.value)}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
          />
          <Button
            variant="secondary"
            onClick={handlePaste}
            disabled={pasteUrl.trim() === ""}
          >
            Paste
          </Button>
          {previewError !== null ? (
            <p role="alert">
              {previewError} — unable to preview the authorization URL.
            </p>
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
            Add
          </Button>
          {confirmError !== null ? (
            <p role="alert">{confirmError} — unable to complete pairing.</p>
          ) : null}
        </>
      ) : null}
    </main>
  );
}
