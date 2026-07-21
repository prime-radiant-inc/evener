import { Button } from "../widgets";
import { requireClass } from "../widgets/internal/requireClass";
import type { ConnectionState } from "../protocol/client";
import styles from "./ConnectionBanner.module.css";

export interface ConnectionBannerProps {
  state: ConnectionState;
}

const CLASS = {
  banner: requireClass(styles.banner, "ConnectionBanner.module.css", "banner"),
};

const MESSAGE: Partial<Record<ConnectionState, string>> = {
  reconnecting: "Reconnecting to the server…",
  closed: "Connection closed.",
};

// window.location.reload is the only mechanism that reliably re-establishes
// a connection here: AppwireClient.connect() (protocol/client.ts) caches a
// single connectPromise for the object's whole lifetime and never resets
// it, so calling connect() again on an already-"closed" (or still-
// "reconnecting") client does not dial a new socket - a full reload is the
// only thing guaranteed to hand the app a fresh client.
function reload(): void {
  window.location.reload();
}

/**
 * A quiet inline strip reporting the connection state when it needs a
 * human's attention (reconnecting/closed) - silent the rest of the time.
 * Toast/richer chrome (503 detection, auth flow) lands in a later wave's
 * shell/chrome/** work; this is the minimal Task 1 stopgap.
 */
export function ConnectionBanner({ state }: ConnectionBannerProps) {
  const message = MESSAGE[state];
  if (!message) return null;
  return (
    <div className={CLASS.banner}>
      <span>{message}</span>
      <Button variant="quiet" size="sm" onClick={reload}>
        Reload
      </Button>
    </div>
  );
}
