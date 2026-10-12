import type { DevicePairingInvitation, DevicePairingStatus } from "@evener/appwire-client";
import { encodeInvitation, invitationText } from "@evener/appwire-client/pairing";
import { lazy, Suspense, useCallback, useEffect, useId, useRef, useState } from "react";
import { useClient } from "../../../../shell/clientContext";
import { Button, FormRow, InlineNotice, Input, Select, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { encodeAudio, SAMPLE_RATE } from "./audio";
import styles from "./devicePairing.module.css";

const rootClass = requireClass(styles.root, "devicePairing.module.css", "root");
const controlsClass = requireClass(styles.controls, "devicePairing.module.css", "controls");
const claimClass = requireClass(styles.claim, "devicePairing.module.css", "claim");
const comparisonClass = requireClass(styles.comparison, "devicePairing.module.css", "comparison");
const helpClass = requireClass(styles.help, "devicePairing.module.css", "help");
const QRCodeSVG = lazy(() => import("qrcode.react").then((m) => ({ default: m.QRCodeSVG })));

export function DeviceSetup({ initialOrigin, method }: { initialOrigin: string; method: "sound" | "qr" }) {
  const client = useClient(),
    id = useId();
  const [origin, setOrigin] = useState(initialOrigin);
  const [tailnet, setTailnet] = useState(window.location.protocol === "http:");
  const [invite, setInvite] = useState<DevicePairingInvitation | null>(null);
  const [status, setStatus] = useState<DevicePairingStatus | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const mounted = useRef(true),
    pending = useRef<DevicePairingInvitation | null>(null),
    approved = useRef(false),
    approvalSubmitted = useRef(false);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      // An approval may have committed even if its reply never reached us.
      // Preserve that invitation until expiry so the device can retrieve it.
      if (pending.current && !approved.current && !approvalSubmitted.current)
        void client.request("evener/devicePairing/cancel", { invitation: pending.current.invitation }).catch(() => {});
    };
  }, [client]);
  useEffect(() => {
    if (!invite) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const next = await client.request("evener/devicePairing/read", { invitation: invite.invitation });
        if (cancelled) return;
        if (!(approved.current && next.state === "pending")) {
          approved.current = next.state === "approved";
          setStatus(next);
        }
        setError("");
        if (next.state === "expired") return;
      } catch {
        if (!cancelled) setError("Reconnecting to the Hub. This invitation is still pending.");
      }
      if (!cancelled) timer = setTimeout(() => void poll(), 1200);
    };
    void poll();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [client, invite]);

  async function create() {
    setBusy(true);
    setError("");
    try {
      if (pending.current)
        await client.request("evener/devicePairing/cancel", { invitation: pending.current.invitation });
      const next = await client.request("evener/devicePairing/create", { origin, tailnet });
      if (!mounted.current) {
        void client.request("evener/devicePairing/cancel", { invitation: next.invitation }).catch(() => {});
        return;
      }
      pending.current = next;
      approved.current = false;
      approvalSubmitted.current = false;
      setInvite(next);
      setStatus(null);
    } catch (problem) {
      if (mounted.current) setError(problem instanceof Error ? problem.message : "Could not create an invitation.");
    } finally {
      if (mounted.current) setBusy(false);
    }
  }
  async function approve(publicKey: string) {
    if (!invite) return;
    approvalSubmitted.current = true;
    setBusy(true);
    setError("");
    try {
      const next = await client.request("evener/devicePairing/approve", { invitation: invite.invitation, publicKey });
      if (!mounted.current) return;
      approved.current = next.state === "approved";
      setStatus(next);
    } catch {
      if (mounted.current) setError("Could not confirm approval. Keep this screen open while the Hub reconnects.");
    } finally {
      if (mounted.current) setBusy(false);
    }
  }
  return (
    <section className={rootClass} aria-labelledby={`${id}-title`}>
      <h3 id={`${id}-title`}>{method === "sound" ? "Pair using sound" : "Scan with your phone"}</h3>
      {!invite || status?.state === "expired" ? (
        <>
          {status?.state === "expired" && (
            <InlineNotice>This invitation expired. Create a new one to try again.</InlineNotice>
          )}
          <p>
            {method === "sound"
              ? "Choose Receive setup on your device, then create an invitation for this Hub."
              : "Enter the Hub address your phone can reach, then create an invitation."}
          </p>
          <FormRow label="Hub address" htmlFor={`${id}-origin`}>
            <Input
              id={`${id}-origin`}
              value={origin}
              onChange={(event) => setOrigin(event.target.value)}
              disabled={busy}
              autoComplete="off"
            />
          </FormRow>
          <FormRow label="Connection" htmlFor={`${id}-transport`}>
            <Select
              id={`${id}-transport`}
              value={tailnet ? "tailnet" : "direct"}
              onChange={(event) => setTailnet(event.target.value === "tailnet")}
              disabled={busy}
              options={[
                { value: "tailnet", label: "Tailscale" },
                { value: "direct", label: "Direct Wi-Fi" },
              ]}
            />
          </FormRow>
          <Button size="sm" disabled={busy} onClick={() => void create()}>
            {busy ? "Creating invitation…" : "Create invitation"}
          </Button>
        </>
      ) : (
        <>
          <p>
            <strong>{invite.origin}</strong>
          </p>
          {status?.state === "approved" ? (
            <InlineNotice>Approved. Keep your device connected until setup finishes.</InlineNotice>
          ) : (
            <>
              {method === "sound" ? (
                <SetupSound invitation={invite} hasClaims={Boolean(status?.claims.length)} />
              ) : (
                <>
                  <p>Scan this invitation in the Evener mobile app.</p>
                  <Suspense fallback={<Skeleton lines={1} />}>
                    <div role="img" aria-label="Device pairing invitation QR code">
                      <QRCodeSVG value={invitationText(invite)} includeMargin level="M" size={192} />
                    </div>
                  </Suspense>
                </>
              )}
              {status?.claims.length ? (
                <>
                  <p>
                    {method === "sound"
                      ? "Compare all four lines with your device."
                      : "Compare the whole code with your phone."}
                  </p>
                  {status.claims.map((claim) => (
                    <div key={claim.publicKey} className={claimClass}>
                      <strong>{claim.client}</strong>
                      <pre className={comparisonClass}>
                        {claim.comparison.replace(/([^-]+)-([^-]+)(?:-|$)/g, "$1  $2\n").trim()}
                      </pre>
                      <Button
                        size="sm"
                        aria-label={`Codes match — approve ${claim.comparison}`}
                        disabled={busy}
                        onClick={() => void approve(claim.publicKey)}
                      >
                        Codes match — approve
                      </Button>
                    </div>
                  ))}
                </>
              ) : null}
              <p className={helpClass}>The invitation expires after ten minutes.</p>
            </>
          )}
        </>
      )}
      {error && <InlineNotice tone="danger">{error}</InlineNotice>}
    </section>
  );
}

function SetupSound({ invitation, hasClaims }: { invitation: DevicePairingInvitation; hasClaims: boolean }) {
  const [state, setState] = useState<"ready" | "playing" | "sent" | "stopped">("ready");
  const [error, setError] = useState("");
  const audio = useRef<AudioContext | null>(null),
    source = useRef<AudioBufferSourceNode | null>(null),
    generation = useRef(0);
  const dispose = useCallback(() => {
    generation.current++;
    if (source.current) {
      source.current.onended = null;
      source.current.stop();
      source.current.disconnect();
    }
    source.current = null;
    if (audio.current) void audio.current.close().catch(() => {});
    audio.current = null;
  }, []);
  useEffect(() => () => dispose(), [dispose]);
  async function play() {
    dispose();
    setError("");
    const current = generation.current;
    try {
      // Resume inside the click, before yielding, so HTTP browser playback also
      // satisfies user-gesture audio policy.
      const context = new AudioContext();
      audio.current = context;
      const resumed = context.resume();
      const samples = encodeAudio(encodeInvitation(invitation));
      const buffer = context.createBuffer(1, samples.length, SAMPLE_RATE);
      buffer.getChannelData(0).set(samples);
      setState("playing");
      await resumed;
      if (current !== generation.current) return;
      const node = context.createBufferSource();
      source.current = node;
      node.buffer = buffer;
      node.connect(context.destination);
      node.onended = () => {
        if (current === generation.current) {
          dispose();
          setState("sent");
        }
      };
      node.start();
    } catch {
      if (current === generation.current) {
        dispose();
        setState("ready");
        setError("Could not play sound. Check speaker access and try again.");
      }
    }
  }
  const messages: Record<typeof state, string> = {
    ready: "Hold your listening device near this computer’s speaker, then play the sound.",
    playing: "Keep your device near the speaker while the sound plays.",
    sent: "Waiting for your device. Play the sound again if it did not receive setup.",
    stopped: "Sound stopped. Play it again when your device is ready.",
  };
  return (
    <>
      <div className={controlsClass}>
        <Button size="sm" disabled={state === "playing"} onClick={() => void play()}>
          {state === "playing" ? "Playing setup sound…" : "Play setup sound"}
        </Button>
        {state === "playing" && (
          <Button
            size="sm"
            variant="secondary"
            onClick={() => {
              dispose();
              setState("stopped");
            }}
          >
            Stop sound
          </Button>
        )}
      </div>
      {(!hasClaims || state === "playing") && <InlineNotice>{messages[state]}</InlineNotice>}
      {error && <InlineNotice tone="danger">{error}</InlineNotice>}
    </>
  );
}
