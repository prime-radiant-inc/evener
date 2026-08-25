import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import {
  CANONICAL_CSP,
  extractHtmlCsp,
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
  assert.deepEqual(codes(violations), [
    "network-api",
    "production-mobile-import",
  ]);
});

test("production imports are resolved from nested files and source aliases", () => {
  assert.ok(
    codes(
      scanText("src/app/deep/a.ts", 'import x from "../../../../mobile/src/x"'),
    ).includes("production-mobile-import"),
  );
  assert.ok(
    codes(
      scanText("src/app/a.ts", 'import x from "@/../../mobile/src/x"'),
    ).includes("production-mobile-import"),
  );
});

test("syntax-aware source scanning ignores comments and inert strings but catches dynamic production imports", () => {
  const clean = `
    // fetch("https://comment.invalid"); navigator.vibrate(1); invoke("x")
    const examples = "fetch( navigator.vibrate( token: #[tauri::command]";
    function invoke(value) { return value; }
    invoke("local");
  `;
  assert.deepEqual(scanText("src/clean.ts", clean), []);
  assert.ok(
    codes(
      scanText(
        "src/app/deep.ts",
        'const module = import("../../../mobile/src/x")',
      ),
    ).includes("production-mobile-import"),
  );
  assert.ok(
    codes(
      scanText(
        "src/app/deep.ts",
        'const module = import("../../../" + "mobile/src/x")',
      ),
    ).includes("production-mobile-import"),
  );
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
    assert.ok(
      codes(scanText("src/network.ts", source)).includes("network-api"),
      source,
    );
  }
  assert.ok(
    codes(scanText("src/network.ts", "window.fetch('/x')")).includes(
      "network-api",
    ),
  );
});

test("scanner rejects runtime URLs, dynamic remote imports, invokes, plugins, and credentials", () => {
  const cases = [
    ["const endpoint = 'http://example.invalid'", "remote-url"],
    ["import('https://example.invalid/a.js')", "remote-import"],
    [
      "import { invoke } from '@tauri-apps/api/core'; invoke('run')",
      "native-invoke",
    ],
    ["import '@tauri-apps/plugin-shell'", "production-plugin"],
    ["const fixture = { apiKey: 'fake' }", "credential-fixture"],
    ["const fixture = { token: 'fake' }", "credential-fixture"],
    [
      "const fixture = { authorizationUrl: '/authorize' }",
      "credential-fixture",
    ],
    ["const fixture = { secret: 'fake' }", "credential-fixture"],
  ];
  for (const [source, expected] of cases) {
    assert.ok(
      codes(scanText("src/case.ts", source)).includes(expected),
      source,
    );
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
    ['<input type="file" />', "file-access"],
    ['<input capture="user" />', "file-access"],
    ["showOpenFilePicker()", "file-access"],
    ["showSaveFilePicker()", "file-access"],
    ["showDirectoryPicker()", "file-access"],
    ["navigator.geolocation.getCurrentPosition(done)", "geolocation"],
    ["new Notification('x')", "notification-push"],
    ["PushManager.prototype.subscribe", "notification-push"],
    ["registration.showNotification('x')", "notification-push"],
    [
      "navigator.serviceWorker.ready.then(r => r.showNotification('x'))",
      "notification-push",
    ],
    ["navigator.vibrate(20)", "haptics"],
    ['<input type="file">', "file-access", "index.html"],
  ];
  for (const [source, expected, file = "src/capability.tsx"] of cases) {
    assert.ok(codes(scanText(file, source)).includes(expected), source);
  }
});

test("asset scanners reject remote HTML, CSS, and SVG references", () => {
  const cases = [
    ['<script src="https://cdn.invalid/a.js"></script>', "index.html"],
    ['<link href="//cdn.invalid/a.css" rel="stylesheet">', "index.html"],
    ['<img srcset="local.png 1x, https://cdn.invalid/a.png 2x">', "index.html"],
    [
      '<meta http-equiv="refresh" content="0; url=https://cdn.invalid/">',
      "index.html",
    ],
    [
      '<script>const endpoint = "https://runtime.invalid"</script>',
      "index.html",
    ],
    ["@import 'https://cdn.invalid/theme.css';", "src/styles/a.css"],
    [
      "@font-face { src: url(https://cdn.invalid/font.woff2) }",
      "src/styles/a.css",
    ],
    [
      ".a { background-image: image-set(url('https://cdn.invalid/a.png') 1x) }",
      "src/styles/a.css",
    ],
    ['<use href="https://cdn.invalid/icons.svg#x" />', "src/assets/a.svg"],
    ['<image xlink:href="//cdn.invalid/a.png" />', "src/assets/a.svg"],
  ];
  for (const [source, file] of cases) {
    assert.ok(codes(scanText(file, source)).includes("remote-asset"), source);
  }
});

test("walker scans public production assets", async (t) => {
  const root = await fixture({
    "src/app.ts": "export const ok = true",
    "public/theme.css": "@import 'https://cdn.invalid/theme.css';",
    "public/icon.svg": '<image href="https://cdn.invalid/icon.png"/>',
  });
  t.after(() => rm(root, { recursive: true, force: true }));
  assert.deepEqual(codes(scanSource(root)), ["remote-asset", "remote-asset"]);
});

test("violation sorting uses deterministic code-point order", async (t) => {
  const root = await fixture({
    "public/Z.css": "a{background:url(https://z.invalid)}",
    "public/a.css": "a{background:url(https://a.invalid)}",
    "public/é.css": "a{background:url(https://e.invalid)}",
  });
  t.after(() => rm(root, { recursive: true, force: true }));
  assert.deepEqual(
    scanSource(root).map((item) => item.file),
    ["public/Z.css", "public/a.css", "public/é.css"],
  );
});

test("capability JSON fails closed and rejects recursive escape surfaces", async (t) => {
  const cases = {
    "src-tauri/capabilities/array.json": "[]",
    "src-tauri/capabilities/empty.json": "{}",
    "src-tauri/capabilities/string-permission.json": JSON.stringify({
      permissions: "core:default",
    }),
    "src-tauri/capabilities/remote.json": JSON.stringify({
      identifier: "default",
      description: "x",
      windows: ["main"],
      permissions: ["core:default"],
      nested: { remote: { urls: ["https://example.invalid"] } },
    }),
    "src-tauri/capabilities/command.json": JSON.stringify({
      identifier: "default",
      description: "x",
      windows: ["main"],
      permissions: ["core:default"],
      commands: ["dangerous"],
    }),
    "src-tauri/capabilities/product.json": JSON.stringify({
      identifier: "default",
      description: "x",
      windows: ["main"],
      permissions: ["core:default"],
      nested: { plugin: "shell" },
    }),
  };
  const root = await fixture(cases);
  t.after(() => rm(root, { recursive: true, force: true }));
  const violations = scanSource(root);
  for (const name of Object.keys(cases)) {
    assert.ok(
      violations.some((item) => item.file.endsWith(name.split("/").at(-1))),
      name,
    );
  }
  assert.ok(codes(violations).includes("remote-capability"));
  assert.ok(codes(violations).includes("command-capability"));
  assert.ok(codes(violations).includes("product-permission"));
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
    "src-tauri/src/clean.rs":
      '// #[tauri::command]\nconst NOTE: &str = "tauri::generate_handler![fake]";',
    "src-tauri/capabilities/default.json": JSON.stringify({
      identifier: "default",
      description: "offline",
      windows: ["main"],
      permissions: ["core:default", "shell:allow-open"],
    }),
    "index.html":
      '<meta http-equiv="Content-Security-Policy" content="default-src \'self\'">',
  });
  t.after(() => rm(root, { recursive: true, force: true }));
  const violations = scanSource(root);
  assert.deepEqual(codes(violations), [
    "product-permission",
    "invoke-handler",
    "remote-asset",
    "remote-asset",
  ]);
  assert.ok(
    violations.every(
      (item, index) => index === 0 || violations[index - 1].file <= item.file,
    ),
  );
});

test("config validator requires the concept identity and offline CSP", () => {
  assert.deepEqual(
    codes(
      validateTauriConfig({
        productName: "Evener",
        identifier: "com.primeradiant.evener",
      }),
    ),
    ["product-name", "identifier", "offline-csp", "frontend-dist"],
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
  assert.ok(
    codes(
      validateTauriConfig(config, "default-src 'self'; connect-src 'none'"),
    ).includes("html-csp-mismatch"),
  );
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
  for (const expected of [
    "frontend-dist",
    "asset-protocol",
    "remote-config-url",
    "updater",
    "production-plugin",
    "product-permission",
    "forbidden-entitlement",
  ]) {
    assert.ok(found.includes(expected), expected);
  }
});

test("frontendDist is required and must resolve inside the concept package", () => {
  const base = {
    productName: "Evener Concepts",
    identifier: "com.primeradiant.evener.concepts",
    app: { security: { csp: CANONICAL_CSP } },
  };
  for (const frontendDist of [
    undefined,
    "",
    "../../mobile/dist",
    "../../../outside",
    "/tmp/dist",
  ]) {
    const config = structuredClone(base);
    if (frontendDist !== undefined) config.build = { frontendDist };
    assert.ok(
      codes(validateTauriConfig(config, CANONICAL_CSP)).includes(
        "frontend-dist",
      ),
      String(frontendDist),
    );
  }
});

test("HTML CSP parser requires exactly one complete valid meta", () => {
  assert.equal(
    extractHtmlCsp(
      `<meta content="${CANONICAL_CSP}" http-equiv=Content-Security-Policy>`,
    ),
    CANONICAL_CSP,
  );
  assert.equal(
    extractHtmlCsp(
      `<META HTTP-EQUIV='content-security-policy' CONTENT="${CANONICAL_CSP}">`,
    ),
    CANONICAL_CSP,
  );
  assert.throws(() => extractHtmlCsp("<html></html>"), /exactly one/i);
  assert.throws(
    () =>
      extractHtmlCsp(
        `<meta http-equiv="Content-Security-Policy" content="${CANONICAL_CSP}"><meta http-equiv="Content-Security-Policy" content="${CANONICAL_CSP}">`,
      ),
    /exactly one/i,
  );
  assert.throws(
    () => extractHtmlCsp('<meta http-equiv="Content-Security-Policy">'),
    /content/i,
  );
  assert.throws(
    () =>
      extractHtmlCsp(
        `<meta http-equiv="Content-Security-Policy" content="${CANONICAL_CSP}" content="default-src 'self'">`,
      ),
    /parser rejected/i,
  );
});

test("Rust manifest validator allows only tauri and tauri-build", () => {
  const allowed = `[build-dependencies]\ntauri-build = "2"\n[dependencies]\ntauri = "2"\n`;
  assert.deepEqual(validateRustManifest(allowed), []);
  const forbidden = `${allowed}serde = "1"\n[build-dependencies.extra]\nfoo = "1"\n`;
  assert.ok(codes(validateRustManifest(forbidden)).includes("rust-dependency"));
  assert.ok(
    codes(validateRustManifest("not toml = [")).includes("malformed-manifest"),
  );
  assert.ok(
    codes(validateRustManifest("[dependencies]\ntauri = [")).includes(
      "malformed-manifest",
    ),
  );
  assert.ok(
    codes(validateRustManifest("[dependencies]]\ntauri = '2'")).includes(
      "malformed-manifest",
    ),
  );
  assert.ok(
    codes(
      validateRustManifest(
        "[target.'cfg(unix)'.dependencies]\nreqwest = '1'\n",
      ),
    ).includes("rust-dependency"),
  );
  assert.ok(
    codes(
      validateRustManifest("[workspace.dependencies]\nreqwest = '1'\n"),
    ).includes("rust-dependency"),
  );
  assert.ok(
    codes(validateRustManifest("[dependencies]\ntauri = 2\n")).includes(
      "malformed-manifest",
    ),
  );
});

test("CLI exits cleanly for a valid package and emits sorted failures for invalid packages", async (t) => {
  const baseFiles = {
    "src/app.ts": "export const ok = true",
    "src-tauri/Cargo.toml":
      '[build-dependencies]\ntauri-build = "2"\n[dependencies]\ntauri = "2"\n',
    "src-tauri/tauri.conf.json": JSON.stringify({
      productName: "Evener Concepts",
      identifier: "com.primeradiant.evener.concepts",
      build: { frontendDist: "../dist" },
      app: { security: { csp: CANONICAL_CSP } },
    }),
    "src-tauri/capabilities/default.json": JSON.stringify({
      identifier: "default",
      description: "offline",
      windows: ["main"],
      permissions: ["core:default"],
    }),
    "index.html": `<meta http-equiv="Content-Security-Policy" content="${CANONICAL_CSP}">`,
  };
  const clean = await fixture(baseFiles);
  const violating = await fixture({
    ...baseFiles,
    "src/z.ts": "fetch('/x')",
    "src/A.ts": "navigator.vibrate(1)",
  });
  const malformed = await fixture({
    ...baseFiles,
    "src-tauri/Cargo.toml": "[dependencies]\ntauri = [",
  });
  t.after(() =>
    Promise.all(
      [clean, violating, malformed].map((root) =>
        rm(root, { recursive: true, force: true }),
      ),
    ),
  );
  const cli = path.resolve("scripts/boundary.mjs");
  const cleanRun = spawnSync(process.execPath, [cli, clean], {
    encoding: "utf8",
  });
  assert.equal(cleanRun.status, 0, cleanRun.stderr);
  assert.equal(cleanRun.stderr, "");
  for (const [kind, root] of [
    ["violating", violating],
    ["malformed", malformed],
  ]) {
    const badRun = spawnSync(process.execPath, [cli, root], {
      encoding: "utf8",
    });
    assert.equal(badRun.status, 1, kind);
    const lines = badRun.stderr.trim().split("\n");
    assert.ok(lines.length >= (kind === "violating" ? 2 : 1), badRun.stderr);
    assert.deepEqual(
      lines,
      [...lines].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)),
    );
    assert.ok(lines.every((line) => /^\S+ \[[a-z-]+\] .+/.test(line)));
  }
});
