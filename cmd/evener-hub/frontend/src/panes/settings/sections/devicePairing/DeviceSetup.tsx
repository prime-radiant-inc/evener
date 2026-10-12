import type { DevicePairingInvitation, DevicePairingStatus } from "@evener/appwire-client";
import { encodeInvitation, invitationText } from "@evener/appwire-client/pairing";
import { QRCodeSVG } from "qrcode.react";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { useClient } from "../../../../shell/clientContext";
import { Button, FormRow, InlineNotice, Input, Select } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { encodeAudio, SAMPLE_RATE } from "./audio";
import styles from "./devicePairing.module.css";

const rootClass = requireClass(styles.root, "devicePairing.module.css", "root");
const controlsClass = requireClass(styles.controls, "devicePairing.module.css", "controls");

export function DeviceSetup({ onClose }: { onClose(): void }) {
  const client = useClient(),
    id = useId();
  const [origin, setOrigin] = useState(window.location.origin);
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
      <h3 id={`${id}-title`}>Pair a device</h3>
      <p>On your device, choose Receive setup to listen, or Scan pairing code to use the camera.</p>
      {!invite || status?.state === "expired" ? (
        <>
          {status?.state === "expired" && (
            <InlineNotice>This invitation expired. Create a new one and listen or scan again.</InlineNotice>
          )}
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
            <InlineNotice>
              Approved. Your device can now save this Hub. Keep it connected until setup finishes.
            </InlineNotice>
          ) : (
            <>
              <div role="img" aria-label="Device pairing invitation QR code">
                <QRCodeSVG value={invitationText(invite)} includeMargin level="M" size={192} />
              </div>
              <SetupSound invitation={invite} />
              <p>This invitation lasts ten minutes. Compare the code on your device before approving it here.</p>
              {status?.claims.length ? (
                status.claims.map((claim) => (
                  <div key={claim.publicKey} className={controlsClass}>
                    <span>
                      {claim.client}: <strong>{claim.comparison}</strong>
                    </span>
                    <Button
                      size="sm"
                      aria-label={`Approve code ${claim.comparison}`}
                      disabled={busy}
                      onClick={() => void approve(claim.publicKey)}
                    >
                      Approve this device
                    </Button>
                  </div>
                ))
              ) : (
                <InlineNotice>Waiting for your device…</InlineNotice>
              )}
            </>
          )}
        </>
      )}
      {error && <InlineNotice tone="danger">{error}</InlineNotice>}
      <div className={controlsClass}>
        <Button size="sm" variant="secondary" onClick={onClose}>
          Close setup
        </Button>
      </div>
    </section>
  );
}

function SetupSound({ invitation }: { invitation: DevicePairingInvitation }) {
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
    ready: "Hold your device near your speaker.",
    playing: "Sending setup sound…",
    sent: "Sound sent. Play it again if your device missed part of it.",
    stopped: "Sound stopped. You can play it again.",
  };
  return (
    <>
      <div className={controlsClass}>
        <Button size="sm" disabled={state === "playing"} onClick={() => void play()}>
          {state === "ready" ? "Play setup sound" : "Play sound again"}
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
      <InlineNotice>{messages[state]}</InlineNotice>
      {error && <InlineNotice tone="danger">{error}</InlineNotice>}
    </>
  );
}
