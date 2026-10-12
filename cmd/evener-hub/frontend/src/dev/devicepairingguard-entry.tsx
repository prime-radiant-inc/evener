import { AppwireClient, type WebSocketLike } from "@evener/appwire-client";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import { MobileSection } from "../panes/settings/sections/mobile";
import { ClientProvider } from "../shell/clientContext";
import { connectionStore } from "../stores/connection";
import "../styles/tokens.css";
import "../styles/global.css";

declare global {
  interface Window {
    devicePairingGuard: {
      samples: number[][];
      nodes: AudioBufferSourceNode[];
      firstAudioStarted: Promise<void>;
      waitFor: (text: string) => Promise<void>;
      click: (label: string) => void;
      invitationPixels: () => Promise<number[]>;
      holdApproval: () => void;
      approvalReceived: Promise<void>;
      drain: () => Promise<void>;
    };
  }
}
let holdApproval = false;
let approvalReceived = () => {};
const receivedApproval = new Promise<void>((resolve) => {
  approvalReceived = resolve;
});
let resolveFirstAudioStart = () => {};
const firstAudioStarted = new Promise<void>((resolve) => {
  resolveFirstAudioStart = resolve;
});
const inFlight = new Map<unknown, { method: string; received: Promise<void>; resolve(): void }>();

// Delay only wire delivery to the browser. The real Hub still commits approval
// and answers every request, including teardown cancellation if the UI sends it.
function pairingSocket(url: string): WebSocketLike {
  const socket = new WebSocket(url);
  const transport: WebSocketLike = {
    onopen: null,
    onmessage: null,
    onclose: null,
    onerror: null,
    close: (code) => socket.close(code),
    send(data) {
      const frame = JSON.parse(data);
      if (frame.id !== undefined && frame.method) {
        let resolve = () => {};
        const received = new Promise<void>((done) => {
          resolve = done;
        });
        inFlight.set(frame.id, { method: frame.method, received, resolve });
      }
      socket.send(data);
    },
  };
  socket.onopen = () => transport.onopen?.();
  socket.onerror = () => transport.onerror?.();
  socket.onclose = (event) => transport.onclose?.(event);
  socket.onmessage = (event) => {
    const frame = JSON.parse(event.data);
    const request = inFlight.get(frame.id);
    request?.resolve();
    inFlight.delete(frame.id);
    if (holdApproval && request?.method === "evener/devicePairing/approve") {
      approvalReceived();
      return;
    }
    if (holdApproval && request?.method === "evener/devicePairing/read") return;
    transport.onmessage?.(event);
  };
  return transport;
}
window.devicePairingGuard = {
  samples: [],
  nodes: [],
  firstAudioStarted,
  holdApproval() {
    holdApproval = true;
  },
  approvalReceived: receivedApproval,
  async drain() {
    await Promise.all(Array.from(inFlight.values(), (request) => request.received));
  },
  click(label: string) {
    const button = Array.from(document.querySelectorAll("button")).find(
      (node) => (node.getAttribute("aria-label") ?? node.textContent).trim() === label,
    );
    if (!button || button.disabled) throw new Error("Button unavailable");
    flushSync(() => button.click());
  },
  async invitationPixels() {
    const svg = document.querySelector('[aria-label="Device pairing invitation QR code"] svg');
    if (!svg) throw new Error("Missing invitation QR");
    const image = new Image();
    image.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(svg))}`;
    await image.decode();
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = 768;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("Missing canvas context");
    context.fillStyle = "white";
    context.fillRect(0, 0, 768, 768);
    context.drawImage(image, 0, 0, 768, 768);
    return Array.from(context.getImageData(0, 0, 768, 768).data);
  },
  waitFor(text: string) {
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        observer.disconnect();
        reject(new Error(`Missing UI state: ${text}`));
      }, 10000);
      const check = () => {
        const labelled = Array.from(document.querySelectorAll("button")).some(
          (button) => button.getAttribute("aria-label") === text,
        );
        if (!labelled && !document.body.textContent?.includes(text)) return;
        clearTimeout(timeout);
        observer.disconnect();
        resolve();
      };
      const observer = new MutationObserver(check);
      observer.observe(document.body, { subtree: true, childList: true, characterData: true });
      check();
    });
  },
};
const start = AudioBufferSourceNode.prototype.start;
AudioBufferSourceNode.prototype.start = function (when?: number, offset?: number, duration?: number) {
  start.call(this, when, offset, duration);
  window.devicePairingGuard.samples.push(Array.from(this.buffer?.getChannelData(0) ?? []));
  window.devicePairingGuard.nodes.push(this);
  resolveFirstAudioStart();
};
const client = new AppwireClient({ url: `ws://${location.host}/rpc`, socketFactory: pairingSocket });
await client.connect();
connectionStore.getState().connect(client);
const root = document.getElementById("root");
if (!root) throw new Error("Missing fixture root");
createRoot(root).render(
  <ClientProvider client={client}>
    <main style={{ maxWidth: 720, margin: "24px auto", padding: 20 }}>
      <h1>Connect a device</h1>
      <MobileSection />
    </main>
  </ClientProvider>,
);
