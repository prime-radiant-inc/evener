import { QRCodeSVG } from "qrcode.react";
import { useState } from "react";
import { WireError } from "../../../protocol/errors";
import type { AppwireClientLike } from "../../../protocol/testing/fakeClient";
import { useClient } from "../../../shell/clientContext";
import { Button, EmptyState, Skeleton } from "../../../widgets";
import { copyText } from "./credentials/clipboard";
import { useConnectedEffect } from "./useConnectedEffect";

type PairingState =
  | { kind: "loading" }
  | { kind: "ready"; authURL: string }
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

  useConnectedEffect(
    async (isCancelled) => {
      const next = await loadPairingURL(client);
      if (!isCancelled()) setState(next);
    },
    [client],
  );

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

async function loadPairingURL(client: AppwireClientLike): Promise<PairingState> {
  try {
    const response = await client.request("evener/mobile/pairing", { origin: window.location.origin });
    if (response.authUrl === "") return { kind: "error" };
    return { kind: "ready", authURL: response.authUrl };
  } catch (error) {
    if (error instanceof WireError && error.evenerErrorInfo === "conflict") {
      return { kind: "unreachable" };
    }
    return { kind: "error" };
  }
}
