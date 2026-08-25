import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import {
  CANONICAL_CSP,
  NETWORK_API_PATTERNS,
  scanSource,
  scanText,
  validateRustManifest,
  validateTauriConfig,
} from "./boundary.mjs";

const codes = (violations) => violations.map((item) => item.code);

async function fixture(files) {
  const root = await mkdtemp(path.join(tmpdir(), "concept-boundary-"));
  await Promise.all(
    Object.entries(files).map(async ([name, contents]) => {
      const target = path.join(root, name);
      await mkdir(path.dirname(target), { recursive: true });
      await writeFile(target, contents);
    }),
  );
  return root;
}

test("source scanner rejects network and production mobile imports", () => {
  const violations = [
    ...scanText("src/a.ts", 'fetch("https://example.invalid")'),
    ...scanText("src/b.ts", 'import x from "../../mobile/src/state/x"'),
  ];
  assert.deepEqual(codes(violations), ["network-api", "production-mobile-import"]);
});

test("production imports are resolved from nested files and source aliases", () => {
  assert.ok(codes(scanText("src/app/deep/a.ts", 'import x from "../../../../mobile/src/x"')).includes("production-mobile-import"));
  assert.ok(codes(scanText("src/app/a.ts", 'import x from "@/../../mobile/src/x"')).includes("production-mobile-import"));
});

test("network rule table catches every named API", () => {
  assert.ok(NETWORK_API_PATTERNS.length >= 5);
  for (const source of [
    "fetch('/x')",
    "new XMLHttpRequest()",
    "new WebSocket('/x')",
    "new EventSource('/x')",
    "navigator.sendBeacon('/x')",
  ]) {
    assert.ok(codes(scanText("src/network.ts", source)).includes("network-api"), source);
  }
});

test("scanner rejects runtime URLs, dynamic remote imports, invokes, plugins, and credentials", () => {
  const cases = [
    ["const endpoint = 'http://example.invalid'", "remote-url"],
    ["import('https://example.invalid/a.js')", "remote-import"],
    ["import { invoke } from '@tauri-apps/api/core'; invoke('run')", "native-invoke"],
    ["import '@tauri-apps/plugin-shell'", "production-plugin"],
    ["const fixture = { apiKey: 'fake' }", "credential-fixture"],
    ["const fixture = { token: 'fake' }", "credential-fixture"],
    ["const fixture = { authorizationUrl: '/authorize' }", "credential-fixture"],
    ["const fixture = { secret: 'fake' }", "credential-fixture"],
  ];
  for (const [source, expected] of cases) {
    assert.ok(codes(scanText("src/case.ts", source)).includes(expected), source);
  }
});

test("scanner rejects every forbidden browser capability", () => {
  const cases = [
    ["navigator.mediaDevices", "media-capture"],
    ["navigator.mediaDevices.getUserMedia({audio:true})", "media-capture"],
    ["new SpeechRecognition()", "speech-recognition"],
    ["new webkitSpeechRecognition()", "speech-recognition"],
    ["speechSynthesis.speak(message)", "speech-synthesis"],
    ["new SpeechSynthesisUtterance('x')", "speech-synthesis"],
    ["new AudioContext()", "audio-context"],
    ["new webkitAudioContext()", "audio-context"],
    ['<input type="file">', "file-access"],
    ['<input capture="user">', "file-access"],
    ["showOpenFilePicker()", "file-access"],
    ["showSaveFilePicker()", "file-access"],
    ["showDirectoryPicker()", "file-access"],
    ["navigator.geolocation.getCurrentPosition(done)", "geolocation"],
    ["new Notification('x')", "notification-push"],
    ["PushManager.prototype.subscribe", "notification-push"],
    ["registration.showNotification('x')", "notification-push"],
    ["navigator.serviceWorker.ready.then(r => r.showNotification('x'))", "notification-push"],
    ["navigator.vibrate(20)", "haptics"],
    ['<input type="file">', "file-access", "index.html"],
  ];
  for (const [source, expected, file = "src/capability.tsx"] of cases) {
    assert.ok(codes(scanText(file, source)).includes(expected), source);
  }
});

test("asset scanners reject remote HTML, CSS, and SVG references", () => {
  const cases = [
    ['<script src="https://cdn.invalid/a.js"></script>', "remote-asset"],
    ['<link href="//cdn.invalid/a.css" rel="stylesheet">', "remote-asset"],
    ['<img src="https://cdn.invalid/a.png">', "remote-asset"],
    ['<video poster="https://cdn.invalid/p.png"></video>', "remote-asset"],
    ["@font-face { src: url(https://cdn.invalid/font.woff2) }", "remote-asset"],
    [".a { background: url('https://cdn.invalid/a.png') }", "remote-asset"],
    ['<use href="https://cdn.invalid/icons.svg#x" />', "remote-asset"],
    ['<image xlink:href="//cdn.invalid/a.png" />', "remote-asset"],
  ];
  for (const [source, expected] of cases) {
    assert.ok(codes(scanText("src/asset.svg", source)).includes(expected), source);
  }
});

test("source walker skips tests but scans application assets and native files", async (t) => {
  const root = await fixture({
    "src/app.ts": "export const ok = true",
    "src/app.test.ts": "fetch('https://ignored.invalid')",
    "src/test/setup.ts": "navigator.vibrate(1)",
    "src/test/traps.ts": "new WebSocket('https://ignored.invalid')",
    "src/styles/app.css": ".x{background:url(https://bad.invalid/a.png)}",
    "src/assets/icon.svg": '<use href="https://bad.invalid/a.svg#x"/>',
    "src-tauri/src/lib.rs": "tauri::generate_handler![dangerous]",
    "src-tauri/capabilities/default.json": JSON.stringify({ permissions: ["core:default", "shell:allow-open"] }),
    "index.html": '<meta http-equiv="Content-Security-Policy" content="default-src \'self\'">',
  });
  t.after(() => rm(root, { recursive: true, force: true }));
  const violations = scanSource(root);
  assert.deepEqual(codes(violations), ["product-permission", "invoke-handler", "remote-asset", "remote-asset"]);
  assert.ok(violations.every((item, index) => index === 0 || violations[index - 1].file <= item.file));
});

test("config validator requires the concept identity and offline CSP", () => {
  assert.deepEqual(
    codes(validateTauriConfig({ productName: "Evener", identifier: "com.primeradiant.evener" })),
    ["product-name", "identifier", "offline-csp"],
  );
});

test("config validator accepts only the canonical offline configuration", () => {
  const config = {
    productName: "Evener Concepts",
    identifier: "com.primeradiant.evener.concepts",
    build: { frontendDist: "../dist" },
    app: { security: { csp: CANONICAL_CSP } },
    bundle: {},
  };
  assert.deepEqual(validateTauriConfig(config, CANONICAL_CSP), []);
  assert.ok(codes(validateTauriConfig(config, "default-src 'self'; connect-src 'none'" )).includes("html-csp-mismatch"));
});

test("config validator rejects malformed and privileged or remote settings", () => {
  assert.ok(codes(validateTauriConfig(null)).includes("malformed-config"));
  const config = {
    productName: "Evener Concepts",
    identifier: "com.primeradiant.evener.concepts",
    build: { frontendDist: "https://example.invalid/app" },
    app: {
      security: { csp: CANONICAL_CSP, assetProtocol: { enable: true } },
      permissions: ["shell:allow-open"],
    },
    bundle: { iOS: { entitlements: "Entitlements.plist" } },
    plugins: { updater: { endpoints: ["https://example.invalid/update"] } },
  };
  const found = codes(validateTauriConfig(config, CANONICAL_CSP));
  for (const expected of ["frontend-dist", "asset-protocol", "remote-config-url", "updater", "production-plugin", "product-permission", "forbidden-entitlement"]) {
    assert.ok(found.includes(expected), expected);
  }
});

test("Rust manifest validator allows only tauri and tauri-build", () => {
  const allowed = `[build-dependencies]\ntauri-build = "2"\n[dependencies]\ntauri = "2"\n`;
  assert.deepEqual(validateRustManifest(allowed), []);
  const forbidden = `${allowed}serde = "1"\n[build-dependencies.extra]\nfoo = "1"\n`;
  assert.ok(codes(validateRustManifest(forbidden)).includes("rust-dependency"));
  assert.ok(codes(validateRustManifest("not toml = [")).includes("malformed-manifest"));
});
