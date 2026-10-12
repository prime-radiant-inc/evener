import { crc32 as pairingCRC32, MAX_TRANSFER_BYTES as pairingMaxTransferBytes } from "@evener/appwire-client/pairing";
import nacl from "tweetnacl";

export const MAX_TRANSFER_BYTES = pairingMaxTransferBytes;
export type HubSettings = { host: string; port: number; path: string; token: string; tailnet: boolean; tls: boolean };
export type Recipient = { session: Uint8Array; publicKey: Uint8Array };
export type Transfer = { bytes: Uint8Array; comparison: string; settings: HubSettings };

export function readRecipient(text: string): Recipient {
  const match = /^EVENER1:([a-f0-9]{80})$/i.exec(text.trim());
  if (!match?.[1]) throw new Error("Use the QR code from Receive setup on your Playdate.");
  const value = match[1];
  const bytes = Uint8Array.from({ length: 40 }, (_, i) => Number.parseInt(value.slice(i * 2, i * 2 + 2), 16));
  if (!bytes.slice(8).some(Boolean)) throw new Error("That setup code has no device key. Start Receive setup again.");
  return { session: bytes.slice(0, 8), publicKey: bytes.slice(8) };
}

export function pairingDestination(authURL: string): { address: string; token: string } {
  const url = new URL(authURL);
  const match = /^(.*)\/auth\/([^/]+)\/?$/.exec(url.pathname);
  if (!match?.[2]) throw new Error("The Hub returned an invalid pairing link. Reload Settings and try again.");
  return { address: `${url.origin}${match[1] || ""}`, token: decodeURIComponent(match[2]) };
}

export function hubSettings(address: string, token: string, tailnet: boolean): HubSettings {
  let url: URL;
  try {
    url = new URL(address);
  } catch {
    throw new Error("Enter the Hub address including http:// or https://.");
  }
  if (
    !["http:", "https:", "ws:", "wss:"].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  )
    throw new Error("Use an HTTP or WebSocket Hub address without a password, query or fragment.");
  const tls = url.protocol === "https:" || url.protocol === "wss:";
  if (tailnet && tls)
    throw new Error(
      "For Tailscale, use the Hub’s internal http:// address and port. The tailnet encrypts the connection.",
    );
  const host = url.hostname.toLowerCase();
  const path = url.pathname === "/" ? "/rpc" : `${url.pathname.replace(/\/$/, "")}/rpc`;
  if (!/^[a-z0-9.-]{1,255}$/.test(host)) throw new Error("The Playdate needs an IPv4 address or a hostname.");
  if (path.length > 255 || /[\s#\\]/.test(path))
    throw new Error("The Hub path is too long or contains unsupported characters.");
  if (!/^[\x21-\x7e]{1,511}$/.test(token))
    throw new Error("The Hub credential exceeds the Playdate’s supported format.");
  return { host, port: Number(url.port || (tls ? 443 : 80)), path, token, tailnet, tls };
}

export function createTransfer(recipient: Recipient, settings: HubSettings): Transfer {
  const encoder = new TextEncoder();
  const host = encoder.encode(settings.host),
    path = encoder.encode(settings.path),
    token = encoder.encode(settings.token);
  const plain = new Uint8Array(8 + host.length + path.length + token.length);
  plain.set([
    1,
    Number(settings.tailnet) | (Number(settings.tls) << 1),
    settings.port >> 8,
    settings.port & 255,
    host.length,
    path.length,
    token.length >> 8,
    token.length & 255,
  ]);
  plain.set(host, 8);
  plain.set(path, 8 + host.length);
  plain.set(token, 8 + host.length + path.length);
  const sender = nacl.box.keyPair();
  const shared = nacl.scalarMult(sender.secretKey, recipient.publicKey);
  const validKey = shared.some(Boolean);
  shared.fill(0);
  if (!validKey) {
    sender.secretKey.fill(0);
    plain.fill(0);
    throw new Error("That device key is invalid. Start Receive setup again on the Playdate.");
  }
  const nonce = nacl.randomBytes(24);
  const encrypted = nacl.box(plain, nonce, recipient.publicKey, sender.secretKey);
  sender.secretKey.fill(0);
  plain.fill(0);
  const bytes = new Uint8Array(68 + encrypted.length);
  bytes.set([69, 86, 80, 49]);
  bytes.set(recipient.session, 4);
  bytes.set(sender.publicKey, 12);
  bytes.set(nonce, 44);
  bytes.set(encrypted, 68);
  if (bytes.length > MAX_TRANSFER_BYTES) throw new Error("The configuration is too large for the Playdate.");
  const hash = nacl.hash(bytes);
  const comparison = (((hash[0] ?? 0) * 65536 + (hash[1] ?? 0) * 256 + (hash[2] ?? 0)) % 1000000)
    .toString()
    .padStart(6, "0");
  return { bytes, comparison, settings };
}

export function crc32(bytes: Uint8Array): number {
  return pairingCRC32(bytes);
}
