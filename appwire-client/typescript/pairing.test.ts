// @vitest-environment node
import nacl from "tweetnacl";
import { describe, expect, it } from "vitest";
import {
  createPairingClaim,
  decodeInvitation,
  encodeInvitation,
  invitationText,
  openPairingEnvelope,
  parseInvitation,
  requestPairingClaim,
  toHex,
} from "./pairing";

const invitation = { invitation: "01".repeat(24), origin: "http://magic-kingdom:9180", tailnet: true, expiresAt: 0 };
describe("device pairing invitations", () => {
  it("distinguishes device keys with the same six-digit hash reduction", () => {
    const key = (hex: string) => Uint8Array.from(hex.match(/../g) ?? [], (byte) => Number.parseInt(byte, 16));
    const first = createPairingClaim(
      invitation,
      key("e771865d3c730bf94edcaa4c6fad4dda31f7fa1ad462af0990bdea23c283a80f"),
    );
    const second = createPairingClaim(
      invitation,
      key("a0d8cc836eb3fd38b0c862586293bc8b5f42570d115bbc0b94ff54a8e63d742c"),
    );
    expect(first.comparison).toBe("2be9-271d-6222-d455-d2d1-c217-62b2-3b82");
    expect(second.comparison).toBe("1ca6-e724-b024-83d3-5bba-fbcb-6c57-d342");
  });
  it("carries the identical locator and secret through sound bytes and QR", () => {
    const ipv6 = { ...invitation, origin: "https://[fd7a:115c:a1e0::1]:9180" };
    expect(decodeInvitation(encodeInvitation(ipv6))).toEqual(ipv6);
    const wire = encodeInvitation(invitation);
    expect(decodeInvitation(wire)).toEqual(invitation);
    expect(parseInvitation(invitationText(invitation))).toEqual(decodeInvitation(wire));
    expect(() => decodeInvitation(wire.slice(0, -1))).toThrow();
    const bad = wire.slice();
    bad[4] = 4;
    expect(() => decodeInvitation(bad)).toThrow();
  });
  it("binds encrypted approval to this key, invitation and destination", () => {
    const claim = createPairingClaim(invitation, new Uint8Array(32).fill(7));
    const sender = nacl.box.keyPair.fromSecretKey(new Uint8Array(32).fill(9));
    const nonce = new Uint8Array(24).fill(2);
    const host = "magic-kingdom",
      path = "/rpc",
      token = "private-credential";
    const plain = new Uint8Array([
      1,
      1,
      35,
      220,
      host.length,
      path.length,
      0,
      token.length,
      ...new TextEncoder().encode(host + path + token),
    ]);
    const envelope = new Uint8Array([
      69,
      86,
      80,
      49,
      ...new Uint8Array(8).fill(1),
      ...sender.publicKey,
      ...nonce,
      ...nacl.box(plain, nonce, claim.publicKeyBytes, sender.secretKey),
    ]);
    expect(openPairingEnvelope(claim, toHex(envelope)).token).toBe(token);
    expect(() =>
      openPairingEnvelope(createPairingClaim(invitation, new Uint8Array(32).fill(8)), toHex(envelope)),
    ).toThrow();
    expect(() =>
      openPairingEnvelope(
        { ...claim, invitation: { ...invitation, origin: "http://another-hub:9180" } },
        toHex(envelope),
      ),
    ).toThrow();
    envelope[envelope.length - 1] = (envelope[envelope.length - 1] ?? 0) ^ 1;
    expect(() => openPairingEnvelope(claim, toHex(envelope))).toThrow();
    expect(claim.comparison).toMatch(/^[0-9a-f]{4}(?:-[0-9a-f]{4}){7}$/);
  });
});

describe("pairing claim responses", () => {
  const claim = createPairingClaim(invitation, new Uint8Array(32).fill(7));
  it("reads a reply split across network chunks", async () => {
    const reply = { state: "pending", comparison: claim.comparison };
    const bytes = new TextEncoder().encode(JSON.stringify(reply));
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(bytes.slice(0, 11));
        controller.enqueue(bytes.slice(11));
        controller.close();
      },
    });
    expect(
      await requestPairingClaim(claim, "mobile", new AbortController().signal, async () => new Response(stream)),
    ).toEqual(reply);
  });
  it("cancels an oversized stream before consuming the rest", async () => {
    let reads = 0,
      cancelled = false;
    let transportSignal: AbortSignal | null | undefined;
    const stream = new ReadableStream<Uint8Array>(
      {
        pull(controller) {
          reads++;
          controller.enqueue(new Uint8Array(1024).fill(32));
          if (reads === 100) controller.close();
        },
        cancel() {
          cancelled = true;
        },
      },
      { highWaterMark: 0 },
    );
    await expect(
      requestPairingClaim(claim, "mobile", new AbortController().signal, async (_url, init) => {
        transportSignal = init?.signal;
        return new Response(stream);
      }),
    ).rejects.toThrow();
    expect(cancelled).toBe(true);
    expect(reads).toBe(5);
    expect(transportSignal?.aborted).toBe(true);
  });
  it("cancels an unsuccessful response without reading its body", async () => {
    let reads = 0,
      cancelled = false;
    const stream = new ReadableStream<Uint8Array>(
      {
        pull(controller) {
          reads++;
          controller.enqueue(new Uint8Array(1024));
        },
        cancel() {
          cancelled = true;
        },
      },
      { highWaterMark: 0 },
    );
    await expect(
      requestPairingClaim(
        claim,
        "mobile",
        new AbortController().signal,
        async () => new Response(stream, { status: 429 }),
      ),
    ).rejects.toThrow();
    expect(cancelled).toBe(true);
    expect(reads).toBe(0);
  });
});
