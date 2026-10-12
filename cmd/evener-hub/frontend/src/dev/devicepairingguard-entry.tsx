import { AppwireClient } from "@evener/appwire-client";
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
      waitFor: (text: string) => Promise<void>;
      click: (label: string) => void;
      invitationPixels: () => Promise<number[]>;
    };
  }
}
window.devicePairingGuard = {
  samples: [],
  nodes: [],
  click(label: string) {
    const button = Array.from(document.querySelectorAll("button")).find((node) => node.textContent.trim() === label);
    if (!button || button.disabled) throw new Error("Button unavailable");
    button.click();
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
        if (!document.body.textContent?.includes(text)) return;
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
  window.devicePairingGuard.samples.push(Array.from(this.buffer?.getChannelData(0) ?? []));
  window.devicePairingGuard.nodes.push(this);
  start.call(this, when, offset, duration);
};
const client = new AppwireClient({ url: `ws://${location.host}/rpc` });
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
