import type { AppwireClientLike } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { Component, lazy, type ReactNode, Suspense, useState } from "react";
import { useClient } from "../../../shell/clientContext";
import { Button, EmptyState, InlineNotice, Skeleton } from "../../../widgets";
import { copyText } from "./credentials/clipboard";
import { useConnectedEffect } from "./useConnectedEffect";

// qrcode.react rides its own split chunk: MobileSection itself stays in the
// Settings chunk (Settings.tsx imports it statically), but the QR renderer
// downloads only once this section actually renders. The Suspense fallback
// is scoped to the QR slot so a slow chunk shows a placeholder in this
// section only, never the whole pane.
const QRCodeSVG = lazy(() => import("qrcode.react").then((m) => ({ default: m.QRCodeSVG })));
const DeviceSetup = lazy(() => import("./devicePairing/DeviceSetup").then((m) => ({ default: m.DeviceSetup })));

// A failed chunk load rejects the lazy promise, which Suspense does not
// catch: without a boundary React unmounts the whole tree. Scope the blast
// radius to the QR slot and keep the pairing link (already loaded) usable via
// the copy button below.
class QRChunkBoundary extends Component<{ children: ReactNode; fallback?: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true };
  }

  render(): ReactNode {
    if (this.state.failed) {
      return (
        this.props.fallback ?? (
          <InlineNotice tone="danger" live="status" data-reason="unavailable" title="Couldn't load the QR renderer">
            The pairing link below still works — copy it instead, or reload this page to try again.
          </InlineNotice>
        )
      );
    }
    return this.props.children;
  }
}

type PairingState =
  | { kind: "loading" }
  | { kind: "ready"; authURL: string; origin: string }
  | { kind: "unreachable" }
  | { kind: "error" };

/**
 * MobileSection presents a browser-only pairing QR for the dedicated native
 * app. The QR holds the Hub's long-lived capability URL, so it is requested
 * only while this authenticated section is mounted and never stored or logged
 * here.
 */
export function MobileSection() {
  const client = useClient();
  const [state, setState] = useState<PairingState>({ kind: "loading" });
  const [setup, setSetup] = useState(false);

  useConnectedEffect(
    async (isCancelled) => {
      const next = await loadPairingURL(client);
      if (!isCancelled()) setState(next);
    },
    [client],
  );

  if (state.kind === "loading") return <Skeleton lines={4} />;

  const origin = state.kind === "ready" ? state.origin : window.location.origin;
  return (
    <>
      {setup ? (
        <>
          <QRChunkBoundary
            fallback={
              <InlineNotice tone="danger" data-reason="unavailable" title="Couldn't load device setup">
                Return to pairing to use the existing link, or reload to try setup again.
              </InlineNotice>
            }
          >
            <Suspense fallback={<Skeleton lines={4} />}>
              <DeviceSetup initialOrigin={origin} />
            </Suspense>
          </QRChunkBoundary>
          <Button size="sm" variant="secondary" onClick={() => setSetup(false)}>
            Close setup
          </Button>
        </>
      ) : (
        <Button size="sm" onClick={() => setSetup(true)}>
          Pair a device
        </Button>
      )}
      {!setup && <DirectPairing state={state} />}
    </>
  );
}

function DirectPairing({ state }: { state: PairingState }) {
  if (state.kind === "loading") return <Skeleton lines={4} />;

  if (state.kind === "unreachable") {
    return (
      <EmptyState
        title="Choose an address your device can reach"
        reason="needs-configuration"
        hint="Open Pair a device to enter the Hub address. A Hub listening only on localhost needs a network listener or a reachable reverse proxy."
      />
    );
  }

  if (state.kind === "error") {
    return (
      <InlineNotice tone="danger" live="status" data-reason="unavailable" title="Couldn't create a mobile pairing link">
        Reload this page to try again.
      </InlineNotice>
    );
  }

  const isPlaintextHTTP = state.authURL.toLowerCase().startsWith("http://");

  return (
    <section aria-labelledby="mobile-app-pairing-heading">
      <h2 id="mobile-app-pairing-heading">Mobile app</h2>
      <p>Scan this code from the Evener mobile app to pair another device.</p>
      <QRChunkBoundary>
        <Suspense fallback={<Skeleton lines={1} />}>
          <div role="img" aria-label="Mobile app pairing QR code">
            <QRCodeSVG value={state.authURL} includeMargin level="M" />
          </div>
        </Suspense>
      </QRChunkBoundary>
      <Button size="sm" variant="secondary" onClick={() => void copyText(state.authURL)}>
        Copy pairing link
      </Button>
      <p data-testid="mobile-reuse-warning">
        {isPlaintextHTTP && (
          <span data-testid="mobile-http-warning">
            HTTP connection: anyone who can observe this network can observe the pairing capability.{" "}
          </span>
        )}
        This link remains valid and can be reused until the Hub auth token is rotated.
      </p>
    </section>
  );
}

async function loadPairingURL(client: AppwireClientLike): Promise<PairingState> {
  try {
    const response = await client.request("evener/mobile/pairing", { origin: window.location.origin });
    if (response.authUrl === "") return { kind: "error" };
    return { kind: "ready", authURL: response.authUrl, origin: new URL(response.authUrl).origin };
  } catch (error) {
    if (error instanceof WireError && error.evenerErrorInfo === "conflict") {
      return { kind: "unreachable" };
    }
    return { kind: "error" };
  }
}
