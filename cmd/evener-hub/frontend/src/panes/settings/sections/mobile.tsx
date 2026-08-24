import { QRCodeSVG } from "qrcode.react";
import { useEffect, useState } from "react";
import { Button, EmptyState, Skeleton } from "../../../widgets";
import { copyText } from "./credentials/clipboard";

type PairingState =
  | { kind: "loading" }
  | { kind: "ready"; authURL: string }
  | { kind: "unreachable" }
  | { kind: "error" };

/**
 * MobileSection presents a browser-only pairing QR for the dedicated native
 * app. The QR holds the Hub's long-lived capability URL, so it is fetched only
 * on demand from the authenticated, no-store endpoint and never logged here.
 */
export function MobileSection() {
  const [state, setState] = useState<PairingState>({ kind: "loading" });

  useEffect(() => {
    let mounted = true;
    void loadPairingURL().then((next) => {
      if (mounted) setState(next);
    });
    return () => {
      mounted = false;
    };
  }, []);

  if (state.kind === "loading") return <Skeleton lines={4} />;

  if (state.kind === "unreachable") {
    return (
      <EmptyState
        title="Mobile pairing needs a reachable Hub origin"
        hint="Configure mobile_base_url in ~/.config/evener/hub.toml to an HTTP(S) origin your phone can reach, then reload this page."
      />
    );
  }

  if (state.kind === "error") {
    return <EmptyState title="Couldn't create a mobile pairing link" hint="Reload this page to try again." />;
  }

  const isPlaintextHTTP = state.authURL.toLowerCase().startsWith("http://");

  return (
    <section aria-labelledby="mobile-app-pairing-heading">
      <h2 id="mobile-app-pairing-heading">Mobile app</h2>
      <p>Scan this code from the Evener mobile app to pair another device.</p>
      <div role="img" aria-label="Mobile app pairing QR code">
        <QRCodeSVG value={state.authURL} includeMargin level="M" />
      </div>
      <Button size="sm" variant="secondary" onClick={() => void copyText(state.authURL)}>
        Copy pairing link
      </Button>
      <p>
        {isPlaintextHTTP && (
          <>Private-network HTTP connection: anyone who can observe this network can observe the pairing capability. </>
        )}
        This link remains valid and can be reused until the Hub auth token is rotated.
      </p>
    </section>
  );
}

async function loadPairingURL(): Promise<PairingState> {
  try {
    const response = await fetch("/api/mobile/pairing", { credentials: "same-origin" });
    if (response.status === 409) return { kind: "unreachable" };
    if (!response.ok) return { kind: "error" };
    const payload: unknown = await response.json();
    if (!isPairingResponse(payload)) return { kind: "error" };
    return { kind: "ready", authURL: payload.auth_url };
  } catch {
    return { kind: "error" };
  }
}

function isPairingResponse(value: unknown): value is { auth_url: string } {
  return (
    typeof value === "object" &&
    value !== null &&
    "auth_url" in value &&
    typeof (value as { auth_url: unknown }).auth_url === "string" &&
    (value as { auth_url: string }).auth_url !== ""
  );
}
