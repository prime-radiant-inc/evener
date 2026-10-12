// Explicit host interoperability check; never joins a tailnet or uses live credentials.
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import nacl from "tweetnacl";
import { buildNativePairingCheck } from "./playdateNativeCheck.mjs";
import { loadPairingModules } from "./pairingModules.mjs";

const { binary, cleanup } = buildNativePairingCheck();
const server = await loadPairingModules();
try {
  const protocol = await server.ssrLoadModule("/src/dev/pairingEnvelope.ts");
  const pairing = await server.ssrLoadModule(
    fileURLToPath(new URL("../../../../appwire-client/typescript/pairing.ts", import.meta.url)),
  );
  const audio = await server.ssrLoadModule("/src/panes/settings/sections/devicePairing/audio.ts");
  const secret = "3a".repeat(32),
    session = "73".repeat(8);
  const qr = execFileSync(binary, ["qr", secret, session], { encoding: "utf8" }).trim();
  const recipient = protocol.readRecipient(qr);
  const settings = protocol.hubSettings("http://magic-kingdom:9180", "synthetic-pairing-token", true);
  const transfer = protocol.createTransfer(recipient, settings);
  const expected = `magic-kingdom 9180 /rpc 1 0 ${transfer.comparison} ${protocol.crc32(new TextEncoder().encode(settings.token)).toString(16).padStart(8, "0")}`;
  let passed = 0;
  function run(label, mode, input, success = true, key = secret, id = session) {
    const result = spawnSync(binary, [mode, key, id], { input, maxBuffer: 1024 * 1024 });
    if (success) {
      assert.equal(result.status, 0, `${label}: ${result.stderr}`);
      assert.equal(result.stdout.toString().trim(), expected, label);
    } else assert.notEqual(result.status, 0, label);
    passed++;
    console.log(`PASS ${label}`);
  }
  function pcm(samples, { prefix = 0, gain = 1, noise = 0, rate = 1, echo = 0 } = {}) {
    const length = Math.floor(samples.length / rate);
    const output = Buffer.alloc((prefix + length) * 2);
    let random = 89231;
    for (let i = 0; i < length; i++) {
      const at = i * rate,
        low = Math.floor(at),
        fraction = at - low;
      let value = (samples[low] ?? 0) * (1 - fraction) + (samples[low + 1] ?? 0) * fraction;
      value += echo * (samples[low - 137] ?? 0);
      random = (Math.imul(random, 1664525) + 1013904223) >>> 0;
      value = value * gain + (random / 0xffffffff - 0.5) * noise;
      output.writeInt16LE(Math.round(Math.max(-1, Math.min(1, value)) * 32767), (i + prefix) * 2);
    }
    return output;
  }
  const invitation = { invitation: "01".repeat(24), origin: "http://magic-kingdom:9180", tailnet: true, expiresAt: 0 };
  const claim = pairing.createPairingClaim(invitation, new Uint8Array(32).fill(0x3a));
  const decodedInvite = execFileSync(binary, ["decode-invitation", secret, session], {
    input: pcm(audio.encodeAudio(pairing.encodeInvitation(invitation))),
    encoding: "utf8",
  })
    .trim()
    .split(" ");
  assert.equal(decodedInvite[5], claim.comparison);
  assert.equal(decodedInvite[6], claim.publicKey);
  assert.equal(decodedInvite[7], invitation.invitation);
  console.log("PASS shared invitation PCM and fingerprint in C");
  run("JS encryption opens in C", "open", transfer.bytes);
  run("wrong recipient key", "open", transfer.bytes, false, "4b".repeat(32));
  run("old setup session", "open", transfer.bytes, false, secret, "74".repeat(8));
  const tampered = transfer.bytes.slice();
  tampered[tampered.length - 1] ^= 1;
  run("tampered ciphertext", "open", tampered, false);
  run("truncated envelope", "open", transfer.bytes.slice(0, 90), false);
  const plain = nacl.box.open(
    transfer.bytes.slice(68),
    transfer.bytes.slice(44, 68),
    transfer.bytes.slice(12, 44),
    new Uint8Array(32).fill(0x3a),
  );
  assert.ok(plain);
  const weak = transfer.bytes.slice();
  weak.fill(0, 12, 44);
  weak.set(
    nacl.secretbox(plain, weak.slice(44, 68), nacl.box.before(new Uint8Array(32), new Uint8Array(32).fill(0x3a))),
    68,
  );
  run("low-order sender cannot forge an envelope", "open", weak, false);
  assert.throws(() =>
    protocol.createTransfer(
      { ...recipient, publicKey: Uint8Array.from({ length: 32 }, (_, i) => (i === 0 ? 1 : 0)) },
      settings,
    ),
  );
  const wave = audio.encodeAudio(transfer.bytes);
  run("browser PCM through C receiver", "decode", pcm(wave));
  for (const prefix of [1, 24, 48, 73, 98, 122, 146]) run(`sample offset ${prefix}`, "decode", pcm(wave, { prefix }));
  run("quiet speaker and background noise", "decode", pcm(wave, { gain: 0.2, noise: 0.025 }));
  run("room reflection", "decode", pcm(wave, { echo: 0.25, noise: 0.04 }));
  run("sample clock +200ppm", "decode", pcm(wave, { rate: 1.0002 }));
  run("sample clock -200ppm", "decode", pcm(wave, { rate: 0.9998 }));
  const frames = audio.transferFrames(transfer.bytes);
  run("missing fragment", "decode", pcm(audio.encodeFrames(frames.slice(1))), false);
  run(
    "reordered repeated fragments",
    "decode",
    pcm(audio.encodeFrames([...frames.slice(1).reverse(), ...frames])),
    true,
  );
  const corrupted = frames.map((frame) => frame.slice());
  corrupted[0][10] ^= 255;
  run("bad fragment checksum", "decode", pcm(audio.encodeFrames(corrupted)), false);
  run("replay repairs bad fragment", "decode", pcm(audio.encodeFrames([...corrupted, ...frames])));
  const oversized = frames[0].slice();
  oversized[1] = oversized[2] = 255;
  new DataView(oversized.buffer).setUint32(55, protocol.crc32(oversized.subarray(0, 55)), true);
  run("oversized fragment declaration", "decode", pcm(audio.encodeFrames([oversized])), false);
  const maximum = {
    host: "a".repeat(255),
    path: "/" + "b".repeat(254),
    token: "t".repeat(511),
    port: 65535,
    tailnet: false,
    tls: true,
  };
  const largest = protocol.createTransfer(recipient, maximum);
  const largestResult = spawnSync(binary, ["decode", secret, session], {
    input: pcm(audio.encodeAudio(largest.bytes)),
  });
  assert.equal(largestResult.status, 0, largestResult.stderr.toString());
  assert.equal(
    largestResult.stdout.toString().trim(),
    `${maximum.host} 65535 ${maximum.path} 0 1 ${largest.comparison} ${protocol.crc32(new TextEncoder().encode(maximum.token)).toString(16).padStart(8, "0")}`,
  );
  passed++;
  console.log("PASS largest supported configuration through audio");
  assert.throws(() => protocol.readRecipient("unrelated QR"));
  assert.throws(() => protocol.hubSettings("https://magic-kingdom:9180", "token", true));
  assert.throws(() => protocol.hubSettings("http://magic-kingdom:9180", "bad\ntoken", true));
  assert.deepEqual(protocol.pairingDestination("http://magic-kingdom:9180/auth/synthetic-pairing-token"), {
    address: "http://magic-kingdom:9180",
    token: "synthetic-pairing-token",
  });
  console.log(
    `${passed} native interoperability cases passed; audio duration ${(wave.length / 44100).toFixed(1)} seconds.`,
  );
} finally {
  await server.close();
  cleanup();
}
