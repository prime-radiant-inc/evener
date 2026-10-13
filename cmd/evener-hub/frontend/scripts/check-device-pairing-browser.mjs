import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { homedir } from "node:os";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import WebSocket from "ws";
import jsQR from "jsqr";
import { applyViewport, connectOnlyPage, evaluate, navigateTo } from "./browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "./browserGuardProcess.mjs";
import { buildNativePairingCheck } from "./playdateNativeCheck.mjs";
import { loadPairingModules } from "./pairingModules.mjs";

const frontend = fileURLToPath(new URL("..", import.meta.url));
const endpoint = process.env.EVENER_PAIRING_E2E_URL;
if (!endpoint || new URL(endpoint).hostname !== "127.0.0.1") throw new Error("Run the opt-in Hub pairing E2E test.");
const native = buildNativePairingCheck();
const clientDirectory = native.client;
const output = path.join(clientDirectory, "build/pairing-browser");
mkdirSync(output, { recursive: true });
execFileSync("make", ["-f", "check/Makefile", "build/check/receiver"], {
  cwd: clientDirectory,
  env: {
    ...process.env,
    PLAYDATE_SDK_PATH: process.env.PLAYDATE_SDK_PATH ?? path.join(homedir(), "Developer/PlaydateSDK"),
  },
});
const runFile = promisify(execFile);
const modules = await loadPairingModules();
const { AppwireClient } = await modules.ssrLoadModule(
  fileURLToPath(new URL("../../../../appwire-client/typescript/client.ts", import.meta.url)),
);
const pairing = await modules.ssrLoadModule(
  fileURLToPath(new URL("../../../../appwire-client/typescript/pairing.ts", import.meta.url)),
);
const guard = await startBrowserGuard({
  frontend,
  profilePrefix: "devicepairingguard-",
  chromeArgs: ["--mute-audio"],
  viteConfigFile: "scripts/device-pairing.vite.config.mjs",
});
const secret = "3a".repeat(32);
let page;
try {
  page = await connectOnlyPage(await waitForBrowserReady(guard));
  const { send } = page;
  const wait = async (text) => evaluate(send, `window.devicePairingGuard.waitFor(${JSON.stringify(text)})`);
  const click = async (label) => {
    const result = await send("Runtime.evaluate", {
      expression: `window.devicePairingGuard.click(${JSON.stringify(label)})`,
      userGesture: true,
      returnByValue: true,
    });
    assert.equal(result.result.exceptionDetails, undefined);
  };
  const decodeSound = async () => {
    const samples = await evaluate(send, "window.devicePairingGuard.samples[0]");
    const pcm = Buffer.alloc(samples.length * 2);
    samples.forEach((v, i) => pcm.writeInt16LE(Math.round(v * 32767), i * 2));
    const fields = execFileSync(native.binary, ["decode-invitation", secret, "00".repeat(8)], {
      input: pcm,
      encoding: "utf8",
    })
      .trim()
      .split(" ");
    return { pcm, fields };
  };
  const captureScreenshot = async (name) => {
    const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true });
    writeFileSync(path.join(output, `${name}.png`), Buffer.from(shot.result.data, "base64"));
  };
  for (const width of [1280, 390, 320]) {
    const tailnet = width === 1280;
    await applyViewport(send, { width, height: 960 });
    await navigateTo(page, `http://127.0.0.1:${guard.vitePort}/devicepairingguard.html`, {
      bootExpression: "Boolean(window.devicePairingGuard)",
      bootLabel: "Device pairing fixture",
    });
    await wait("Pair using sound");
    await wait("Copy pairing link");
    await wait("Mobile app pairing QR code");
    await captureScreenshot(`device-pairing-entry-${width}`);
    await click("Pair using sound");
    await wait("Create invitation");
    assert.equal(await evaluate(send, "document.querySelector('input').value"), "http://100.64.1.2:9180");
    if (!tailnet)
      await evaluate(
        send,
        `(() => {const select=document.querySelector('select');select.value='direct';select.dispatchEvent(new Event('change',{bubbles:true}));})()`,
      );
    await click("Create invitation");
    await wait("Play setup sound");
    await click("Play setup sound");
    await evaluate(send, "window.devicePairingGuard.firstAudioStarted");
    await click("Stop sound");
    await wait("Play setup sound");
    assert.equal(await evaluate(send, "window.devicePairingGuard.nodes.every(n=>n.context.state==='closed')"), true);
    await click("Play setup sound");
    await wait("Play setup sound");
    assert.equal(await evaluate(send, "window.devicePairingGuard.samples.length"), 2);
    assert.equal(
      await evaluate(
        send,
        "JSON.stringify(window.devicePairingGuard.samples[0])===JSON.stringify(window.devicePairingGuard.samples[1])",
      ),
      true,
    );
    const { pcm, fields } = await decodeSound();
    writeFileSync(path.join(output, `invitation-${width}.pcm`), pcm);
    assert.deepEqual(fields.slice(0, 5), ["100.64.1.2", "9180", "/rpc", tailnet ? "1" : "0", "0"]);
    const [, , , , , comparison, publicKey, invitation] = fields;
    const invite = { origin: "http://100.64.1.2:9180", tailnet, invitation, expiresAt: 0 };
    assert.equal(await evaluate(send, "document.querySelector('[role=img]')"), null);
    const phone = pairing.createPairingClaim(invite, new Uint8Array(32).fill(0x3a));
    assert.equal(phone.publicKey, publicKey);
    assert.equal(phone.comparison, comparison);
    const claim = (claimKey = publicKey, client = "Test device") =>
      fetch(`${endpoint}/api/device-pairing`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ invitation, publicKey: claimKey, client }),
      }).then((r) => r.json());
    // The receiver runs real C microphone decoding, HTTP parsing and settings
    // persistence. Its test boundary maps either device transport to this local Hub.
    const receiver = runFile(
      path.join(clientDirectory, "build/check/receiver"),
      [path.join(output, `invitation-${width}.pcm`), endpoint],
      { timeout: 60000 },
    );
    receiver.catch(() => {});
    const pending = await claim();
    assert.equal(pending.state, "pending");
    assert.equal(pending.envelope, undefined);
    await wait(`Codes match — approve ${comparison}`);
    const comparisonLines = await evaluate(send, "document.querySelector('pre').textContent.trim().split('\\n')");
    assert.equal(comparisonLines.length, 4);
    assert.ok(comparisonLines.every((line) => line.split(/\s+/).length === 2));
    assert.equal(comparisonLines.flatMap((line) => line.split(/\s+/)).join("-"), comparison);
    if (width <= 390) {
      const longClientName = "M".repeat(48);
      const rival = pairing.createPairingClaim(invite, new Uint8Array(32).fill(0x3b));
      assert.equal((await claim(rival.publicKey, longClientName)).state, "pending");
      await wait(`Codes match — approve ${rival.comparison}`);
      const claimFits = await evaluate(send, `(() => {
        const claim = Array.from(document.querySelectorAll('strong')).find(node => node.textContent === ${JSON.stringify(longClientName)});
        if (!claim) throw new Error('Missing long device label');
        const parent = claim.parentElement.getBoundingClientRect();
        const range = document.createRange();
        range.selectNodeContents(claim);
        return Array.from(range.getClientRects()).every(rect => rect.left >= parent.left && rect.right <= parent.right);
      })()`);
      assert.equal(claimFits, true, "long device label and fingerprint stay visible within the pane");
      // Exercise the rendered destination's layout with a supported long DNS
      // name, then restore the actual invitation before testing approval.
      const destinationFits = await evaluate(send, `(() => {
        const destination = Array.from(document.querySelectorAll('strong')).find(node => node.textContent === 'http://100.64.1.2:9180');
        if (!destination) throw new Error('Missing pairing destination');
        const original = destination.textContent;
        destination.textContent = 'http://' + ['a'.repeat(63), 'b'.repeat(63), 'example'].join('.') + ':9180';
        const parent = destination.parentElement.getBoundingClientRect();
        const fits = Array.from(destination.getClientRects()).every(rect => rect.left >= parent.left && rect.right <= parent.right);
        destination.textContent = original;
        return fits;
      })()`);
      assert.equal(destinationFits, true, "long pairing destination wraps within its pane");
    }
    assert.equal(await evaluate(send, "document.documentElement.scrollWidth<=innerWidth"), true);
    assert.equal(
      await evaluate(send, `Array.from(document.querySelectorAll('button')).every(button => {
        const rect = button.getBoundingClientRect();
        return rect.left >= 0 && rect.right <= innerWidth;
      })`),
      true,
      "pairing controls fit the viewport",
    );
    await captureScreenshot(`device-pairing-pending-${width}`);
    await click(`Codes match — approve ${comparison}`);
    await wait("Approved. Keep your device connected");
    const approved = await claim();
    assert.equal(approved.state, "approved");
    assert.deepEqual(await claim(), approved);
    const received = await receiver;
    assert.ok(received.stdout.includes("PASS C receiver"));
    console.log(received.stdout.trim());
    const target = pairing.openPairingEnvelope(phone, approved.envelope);
    assert.equal(target.token, "synthetic-pairing-token");
    const opened = execFileSync(native.binary, ["open", secret, invitation.slice(0, 16)], {
      input: Buffer.from(approved.envelope, "hex"),
      encoding: "utf8",
    });
    assert.ok(opened.startsWith(`100.64.1.2 9180 /rpc ${tailnet ? 1 : 0} 0 `));
    const authenticated = new AppwireClient({
      url: endpoint.replace(/^http/, "ws") + "/rpc",
      socketFactory: (url) => new WebSocket(url, { headers: { Authorization: `Bearer ${target.token}` } }),
    });
    try {
      await authenticated.connect();
      const state = await authenticated.request("evener/devicePairing/read", { invitation });
      assert.equal(state.state, "approved");
    } finally {
      authenticated.close();
    }
    assert.equal(await evaluate(send, "document.documentElement.scrollWidth<=innerWidth"), true);
    await captureScreenshot(`device-pairing-${width}`);
    await click("Close setup");
    assert.equal(await evaluate(send, "window.devicePairingGuard.nodes.every(n=>n.context.state==='closed')"), true);
    assert.equal(
      await evaluate(send, "JSON.stringify([localStorage,sessionStorage]).includes('synthetic-pairing-token')"),
      false,
    );
    console.log(
      `PASS ${width}px HTTP browser -> invitation PCM -> C/mobile protocol -> real Hub claim/approval -> both decrypt -> authenticated request`,
    );
  }
  // A phone can need a different destination from the browser's valid default.
  // Its QR invitation and committed approval must survive a lost browser reply.
  await navigateTo(page, `http://127.0.0.1:${guard.vitePort}/devicepairingguard.html`, {
    bootExpression: "Boolean(window.devicePairingGuard)",
    bootLabel: "Device pairing fixture",
  });
  await wait("Use another Hub address");
  await click("Use another Hub address");
  await wait("Create invitation");
  const phoneOrigin = "https://phone-hub.example.test:9443";
  assert.equal(await evaluate(send, "document.querySelector('input').value"), "http://100.64.1.2:9180");
  await evaluate(send, `(() => {
    const address = document.querySelector('input');
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(address, ${JSON.stringify(phoneOrigin)});
    address.dispatchEvent(new Event('input', {bubbles: true}));
    const transport = document.querySelector('select');
    transport.value = 'direct';
    transport.dispatchEvent(new Event('change', {bubbles: true}));
  })()`);
  await click("Create invitation");
  await wait("Device pairing invitation QR code");
  const pixels = await evaluate(send, "window.devicePairingGuard.invitationPixels()");
  const qr = jsQR(new Uint8ClampedArray(pixels), 768, 768);
  assert.ok(qr, "manual phone invitation QR decodes");
  const invitation = pairing.parseInvitation(qr.data);
  assert.ok(invitation);
  assert.equal(invitation.origin, phoneOrigin);
  assert.equal(invitation.tailnet, false);
  assert.equal(await evaluate(send, "window.devicePairingGuard.samples.length"), 0);
  assert.equal(await evaluate(send, "document.documentElement.scrollWidth<=innerWidth"), true);
  await captureScreenshot("device-pairing-manual-qr-320");
  const phone = pairing.createPairingClaim(invitation, new Uint8Array(32).fill(0x3a));
  const claim = () =>
    fetch(`${endpoint}/api/device-pairing`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        invitation: invitation.invitation,
        publicKey: phone.publicKey,
        client: "Mobile recovery",
      }),
    }).then((response) => response.json());
  assert.equal((await claim()).state, "pending");
  await wait(`Codes match — approve ${phone.comparison}`);
  await evaluate(send, "window.devicePairingGuard.holdApproval()");
  await click(`Codes match — approve ${phone.comparison}`);
  await evaluate(send, "window.devicePairingGuard.approvalReceived");
  await click("Close setup");
  await evaluate(send, "window.devicePairingGuard.drain()");
  const approved = await claim();
  assert.equal(approved.state, "approved", "closing setup must preserve an approval with a lost acknowledgement");
  assert.deepEqual(pairing.openPairingEnvelope(phone, approved.envelope), {
    origin: phoneOrigin,
    token: "synthetic-pairing-token",
  });
  await wait("Use another Hub address");
  console.log("PASS edited phone destination -> displayed QR -> real Hub approval survives lost browser acknowledgement and immediate close");
} finally {
  page?.close();
  await guard.cleanup();
  await modules.close();
  native.cleanup();
}
