import nacl from "tweetnacl";
import { parseHubOrigin } from "./hubOrigin";
import type { DevicePairingInvitation } from "./types.gen";

export type { DevicePairingInvitation } from "./types.gen";
export type ApprovedHub = { origin: string; token: string };
export type PairingClaim = {
  invitation: DevicePairingInvitation;
  publicKey: string;
  publicKeyBytes: Uint8Array;
  secretKey: Uint8Array;
  comparison: string;
};
export type PairingReply = {
  state: "pending" | "approved" | "rejected" | "expired";
  comparison?: string;
  envelope?: string;
};
const QR_PREFIX = "EVENER-INVITE1:";
const ascii = (text: string) => Uint8Array.from(text, (char) => char.charCodeAt(0));
const text = (bytes: Uint8Array) => String.fromCharCode(...bytes);
export const toHex = (bytes: Uint8Array) => Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
function fromHex(value: string, maximum: number): Uint8Array {
  if (!value.length || value.length > maximum * 2 || value.length % 2 || !/^[a-f0-9]+$/.test(value))
    throw new Error("Invalid pairing bytes.");
  return Uint8Array.from({ length: value.length / 2 }, (_, i) => Number.parseInt(value.slice(i * 2, i * 2 + 2), 16));
}
function destination(invitation: DevicePairingInvitation) {
  const url = new URL(parseHubOrigin(invitation.origin));
  const tls = url.protocol === "https:";
  const host = url.hostname.replace(/^\[|\]$/g, "");
  if (!/^[a-z0-9.:-]{1,255}$/.test(host)) throw new Error("Invalid pairing Hub address.");
  const port = Number(url.port || (tls ? 443 : 80));
  return {
    host,
    port,
    flags: Number(invitation.tailnet) | (Number(tls) << 1),
    path: "/rpc",
    origin: url.origin,
  };
}
/** One byte representation for audible setup and the mobile invitation QR. */
export function encodeInvitation(invitation: DevicePairingInvitation): Uint8Array {
  const d = destination(invitation),
    secret = fromHex(invitation.invitation, 24);
  if (secret.length !== 24) throw new Error("Invalid pairing invitation.");
  return new Uint8Array([
    ...ascii("EVI1"),
    d.flags,
    d.port >> 8,
    d.port & 255,
    d.host.length,
    d.path.length,
    ...secret,
    ...ascii(d.host + d.path),
  ]);
}
export function decodeInvitation(bytes: Uint8Array): DevicePairingInvitation {
  if (bytes.length < 38 || bytes.length > 548 || text(bytes.slice(0, 4)) !== "EVI1")
    throw new Error("Invalid pairing invitation.");
  const flags = bytes[4] ?? 255,
    hostLength = bytes[7] ?? 0,
    pathLength = bytes[8] ?? 0;
  const port = (bytes[5] ?? 0) * 256 + (bytes[6] ?? 0);
  const host = text(bytes.slice(33, 33 + hostLength)),
    path = text(bytes.slice(33 + hostLength));
  if (
    flags > 3 ||
    !hostLength ||
    pathLength !== 4 ||
    bytes.length !== 33 + hostLength + pathLength ||
    path !== "/rpc" ||
    !port
  )
    throw new Error("Invalid pairing destination.");
  const result = {
    invitation: toHex(bytes.slice(9, 33)),
    origin: parseHubOrigin(`${flags & 2 ? "https" : "http"}://${host.includes(":") ? `[${host}]` : host}:${port}`),
    tailnet: Boolean(flags & 1),
    expiresAt: 0,
  };
  const d = destination(result);
  if (d.host !== host) throw new Error("Invalid pairing hostname.");
  return result;
}
export function invitationText(invitation: DevicePairingInvitation): string {
  return QR_PREFIX + toHex(encodeInvitation(invitation));
}
export function parseInvitation(value: string): DevicePairingInvitation | null {
  if (!value.trim().startsWith(QR_PREFIX)) return null;
  return decodeInvitation(fromHex(value.trim().slice(QR_PREFIX.length), 548));
}
/** The caller supplies platform cryptographic randomness; no secure-context Web API is required. */
export function createPairingClaim(invitation: DevicePairingInvitation, secret: Uint8Array): PairingClaim {
  encodeInvitation(invitation);
  if (secret.length !== 32) throw new Error("Invalid device key.");
  const keys = nacl.box.keyPair.fromSecretKey(secret.slice());
  const hash = nacl.hash(
    new Uint8Array([...ascii("evener-pairing-v1"), ...fromHex(invitation.invitation, 24), ...keys.publicKey]),
  );
  const comparison = Array.from({ length: 8 }, (_, index) => toHex(hash.slice(index * 2, index * 2 + 2))).join("-");
  return {
    invitation,
    publicKey: toHex(keys.publicKey),
    publicKeyBytes: keys.publicKey,
    secretKey: keys.secretKey,
    comparison,
  };
}
export function openPairingEnvelope(claim: PairingClaim, envelope: string): ApprovedHub {
  const bytes = fromHex(envelope, 1152);
  if (
    bytes.length < 92 ||
    text(bytes.slice(0, 4)) !== "EVP1" ||
    toHex(bytes.slice(4, 12)) !== claim.invitation.invitation.slice(0, 16)
  )
    throw new Error("Wrong pairing invitation.");
  const sender = bytes.slice(12, 44),
    shared = nacl.scalarMult(claim.secretKey, sender);
  const valid = shared.some(Boolean);
  shared.fill(0);
  if (!valid) throw new Error("Invalid sender key.");
  const plain = nacl.box.open(bytes.slice(68), bytes.slice(44, 68), sender, claim.secretKey);
  if (!plain) throw new Error("Could not verify pairing approval.");
  try {
    const d = destination(claim.invitation),
      hostLength = plain[4] ?? 0,
      pathLength = plain[5] ?? 0;
    const tokenLength = (plain[6] ?? 0) * 256 + (plain[7] ?? 0);
    if (
      plain[0] !== 1 ||
      plain[1] !== d.flags ||
      (plain[2] ?? 0) * 256 + (plain[3] ?? 0) !== d.port ||
      plain.length !== 8 + hostLength + pathLength + tokenLength ||
      tokenLength < 1 ||
      tokenLength > 511 ||
      text(plain.slice(8, 8 + hostLength)) !== d.host ||
      text(plain.slice(8 + hostLength, 8 + hostLength + pathLength)) !== d.path
    )
      throw new Error("Pairing approval changed the Hub address.");
    const token = text(plain.slice(8 + hostLength + pathLength));
    if (!/^[\x21-\x7e]+$/.test(token)) throw new Error("Invalid Hub credential.");
    return { origin: d.origin, token };
  } finally {
    plain.fill(0);
  }
}
export async function requestPairingClaim(
  claim: PairingClaim,
  client: string,
  signal: AbortSignal,
  fetcher: typeof fetch = fetch,
): Promise<PairingReply> {
  const request = new AbortController();
  const abort = () => request.abort(signal.reason);
  signal.addEventListener("abort", abort, { once: true });
  if (signal.aborted) abort();
  try {
    const response = await fetcher(`${claim.invitation.origin}/api/device-pairing`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      redirect: "error",
      credentials: "omit",
      signal: request.signal,
      body: JSON.stringify({ invitation: claim.invitation.invitation, publicKey: claim.publicKey, client }),
    });
    if (!response.ok) {
      void response.body?.cancel().catch(() => {});
      throw new Error(
        response.status === 429
          ? "Too many devices tried this invitation. Create a new one."
          : "Could not reach the pairing service. Retrying…",
      );
    }
    const reader = response.body?.getReader();
    if (!reader) throw new Error("Invalid pairing response.");
    const bytes = new Uint8Array(4096);
    let length = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        if (length + value.byteLength > bytes.length) throw new Error("Invalid pairing response.");
        bytes.set(value, length);
        length += value.byteLength;
      }
    } catch (error) {
      void reader.cancel().catch(() => {});
      throw error;
    } finally {
      reader.releaseLock();
    }
    const raw = new TextDecoder().decode(bytes.subarray(0, length));
    const result: unknown = JSON.parse(raw);
    if (
      !result ||
      typeof result !== "object" ||
      !("state" in result) ||
      !["pending", "approved", "rejected", "expired"].includes(String(result.state))
    )
      throw new Error("Invalid pairing response.");
    const reply = result as PairingReply;
    if ((reply.state === "approved" || reply.state === "pending") && reply.comparison !== claim.comparison)
      throw new Error("The pairing code did not match. Start pairing again.");
    if (reply.state === "approved" && typeof reply.envelope !== "string") throw new Error("Missing pairing approval.");
    return reply;
  } finally {
    // Cancel the native transport too: cancelling a stream may only stop its JS events.
    request.abort();
    signal.removeEventListener("abort", abort);
  }
}

export const MAX_TRANSFER_BYTES = 1152;
export function crc32(bytes: Uint8Array): number {
  let crc = 0xffffffff;
  for (const byte of bytes) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}
