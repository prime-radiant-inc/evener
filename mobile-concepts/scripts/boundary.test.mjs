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

test("dynamic imports fail closed and classify Tauri targets", () => {
  const cases = [
    [
      'const target = condition ? "../../../mobile/x" : "https://remote.invalid/x"; import(target)',
      "dynamic-import",
    ],
    ['import("@tauri-apps/api/core")', "native-invoke"],
    ['import("@tauri-apps/plugin-shell")', "production-plugin"],
  ];
  for (const [source, expected] of cases) {
    assert.ok(
      codes(scanText("src/dynamic.ts", source)).includes(expected),
      source,
    );
  }
  assert.ok(
    codes(
      scanText(
        "src/dynamic.ts",
        'import("data:text/javascript,export default 1")',
      ),
    ).includes("dynamic-import"),
  );
  assert.deepEqual(scanText("src/dynamic.ts", 'import("./safe-local.js")'), []);
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

test("network globals remain forbidden through executable aliases", () => {
  const cases = [
    ["const request = fetch; request('/x')", "fetch"],
    ["const Request = XMLHttpRequest; new Request()", "XMLHttpRequest"],
    ["const Socket = WebSocket; new Socket('/x')", "WebSocket"],
    ["const Events = EventSource; new Events('/x')", "EventSource"],
    ["const beacon = navigator.sendBeacon; beacon('/x')", "sendBeacon"],
  ];
  for (const [source, label] of cases) {
    assert.ok(
      codes(scanText("src/network-alias.ts", source)).includes("network-api"),
      label,
    );
  }
  assert.deepEqual(
    scanText(
      "src/local-network.ts",
      `
        // const request = fetch; const Socket = WebSocket;
        const note = "fetch XMLHttpRequest WebSocket EventSource navigator.sendBeacon";
        const fetch = () => "local";
        class WebSocket {}
        fetch();
        new WebSocket();
      `,
    ),
    [],
  );
});

test("browser-global provenance follows lexical scope and shadowing", () => {
  const clean = `
    const window = localWindow;
    const { fetch: request } = window;
    request('/x');

    function run(fetch) {
      const request = fetch;
      return request('/x');
    }

    function nested(window, AudioContext) {
      const { WebSocket: LocalSocket, speechSynthesis: synth } = window;
      new LocalSocket('/x');
      new AudioContext();
      synth.speak(message);
    }

    {
      const navigator = localNavigator;
      const { sendBeacon: beacon, geolocation, vibrate } = navigator;
      beacon('/x');
      geolocation.getCurrentPosition(done);
      vibrate(20);
    }

    try {
      localWork();
    } catch (Notification) {
      const Notify = Notification;
      new Notify('local');
    }
  `;
  assert.deepEqual(scanText("src/local-scopes.ts", clean), []);

  const globalControls = `
    const { fetch: request } = window;
    function run() {
      const requestAgain = fetch;
      return requestAgain('/x');
    }
    {
      const { sendBeacon: beacon, geolocation } = navigator;
      beacon('/x');
      geolocation.getCurrentPosition(done);
    }
    const Notify = Notification;
    const Audio = AudioContext;
  `;
  const found = codes(scanText("src/global-scopes.ts", globalControls));
  for (const expected of [
    "network-api",
    "geolocation",
    "notification-push",
    "audio-context",
  ]) {
    assert.ok(found.includes(expected), expected);
  }

  for (const [source, label] of [
    ["const { fetch: request } = window", "fetch"],
    ["const { XMLHttpRequest: Request } = globalThis", "XMLHttpRequest"],
    ["const { WebSocket: Socket } = self", "WebSocket"],
    ["const { EventSource: Events } = window", "EventSource"],
    ["const { sendBeacon: beacon } = navigator", "sendBeacon"],
  ]) {
    assert.ok(
      codes(scanText("src/global-destructure.ts", source)).includes(
        "network-api",
      ),
      label,
    );
  }
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

test("browser-global references remain forbidden when assigned aliases", () => {
  const cases = [
    ["const synth = speechSynthesis; synth.speak(message)", "speech-synthesis"],
    ["const Notify = Notification; new Notify('x')", "notification-push"],
    [
      "const worker = navigator.serviceWorker; worker.ready",
      "notification-push",
    ],
    ["const Push = PushManager; Push.prototype.subscribe", "notification-push"],
    ["const buzz = navigator.vibrate; buzz(20)", "haptics"],
    ["const { vibrate: buzz } = navigator; buzz(20)", "haptics"],
    [
      "const { serviceWorker: worker } = navigator; worker.ready",
      "notification-push",
    ],
    [
      "const { speechSynthesis: synth } = window; synth.speak(message)",
      "speech-synthesis",
    ],
  ];
  for (const [source, expected] of cases) {
    assert.ok(
      codes(scanText("src/alias.ts", source)).includes(expected),
      source,
    );
  }
});

test("browser-global container aliases preserve every network and capability surface", () => {
  const cases = [
    ["const browser = window; browser.fetch('/x')", "network-api"],
    [
      "const browser = globalThis.window; browser.fetch('/x')",
      "network-api",
    ],
    [
      "const browser = globalThis; const Request = browser.XMLHttpRequest; new Request()",
      "network-api",
    ],
    ["const browser = self; new browser.WebSocket('/x')", "network-api"],
    ["const browser = window; new browser.EventSource('/x')", "network-api"],
    ["const nav = navigator; nav.sendBeacon('/x')", "network-api"],
    [
      "const nav = navigator; nav.mediaDevices.getUserMedia({ audio: true })",
      "media-capture",
    ],
    [
      "const browser = globalThis; new browser.SpeechRecognition()",
      "speech-recognition",
    ],
    [
      "const browser = window; new browser.webkitSpeechRecognition()",
      "speech-recognition",
    ],
    [
      "const browser = self; browser.speechSynthesis.speak(message)",
      "speech-synthesis",
    ],
    [
      "const browser = globalThis; new browser.SpeechSynthesisUtterance('x')",
      "speech-synthesis",
    ],
    ["const browser = window; new browser.AudioContext()", "audio-context"],
    [
      "const browser = self; new browser.webkitAudioContext()",
      "audio-context",
    ],
    ["const browser = window; browser.showOpenFilePicker()", "file-access"],
    [
      "const { self: browser } = globalThis; browser.showOpenFilePicker()",
      "file-access",
    ],
    [
      "const browser = globalThis; browser.showSaveFilePicker()",
      "file-access",
    ],
    [
      "const browser = self; browser.showDirectoryPicker()",
      "file-access",
    ],
    [
      "const browser = globalThis; const { navigator: nav } = browser; nav.geolocation.getCurrentPosition(done)",
      "geolocation",
    ],
    ["const browser = self; new browser.Notification('x')", "notification-push"],
    [
      "const browser = window; browser.PushManager.prototype.subscribe",
      "notification-push",
    ],
    [
      "const nav = navigator; const { serviceWorker: worker } = nav; worker.ready",
      "notification-push",
    ],
    ["const nav = navigator; nav.vibrate(20)", "haptics"],
  ];
  for (const [source, expected] of cases) {
    assert.ok(
      codes(scanText("src/container-alias.ts", source)).includes(expected),
      source,
    );
  }
});

test("local shadows remain clean through container and member aliases", () => {
  const clean = `
    function local(window, globalThis, self, navigator) {
      const browser = window;
      const root = globalThis;
      const localSelf = self;
      const nav = navigator;
      const { fetch: request, AudioContext: Audio } = browser;
      request('/x');
      new Audio();
      root.showOpenFilePicker();
      new localSelf.Notification('x');
      nav.sendBeacon('/x');
      nav.mediaDevices.getUserMedia({ audio: true });
      nav.geolocation.getCurrentPosition(done);
      nav.serviceWorker.ready;
      nav.vibrate(20);
    }
  `;
  assert.deepEqual(scanText("src/local-container-alias.ts", clean), []);
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

test("HTML XML and SVG parsing fails closed and scans processing instructions", () => {
  assert.ok(
    codes(
      scanText("public/bad.html", '<img src="local.png" src="duplicate.png">'),
    ).includes("malformed-asset"),
  );
  const processingInstruction = scanText(
    "public/icon.svg",
    '<?xml version="1.0"?><?xml-stylesheet href="https://cdn.invalid/x.css"?><svg xmlns="http://www.w3.org/2000/svg"/>',
  );
  assert.ok(
    processingInstruction.some(
      (item) =>
        item.code === "remote-asset" &&
        item.detail.includes("processing instruction"),
    ),
  );
  assert.deepEqual(
    scanText(
      "public/local.svg",
      '<svg xmlns="http://www.w3.org/2000/svg"><use href="local.svg#x"/></svg>',
    ),
    [],
  );
  for (const [file, source] of [
    ["public/bad.svg", "<svg><g></svg>"],
    ["public/bad.xml", "<root><child></root>"],
  ]) {
    assert.ok(codes(scanText(file, source)).includes("malformed-asset"), file);
  }
});

test("HTML namespace declarations are local while ordinary remote attributes fail", () => {
  assert.deepEqual(
    scanText(
      "public/xhtml.html",
      '<!doctype html><html xmlns="http://www.w3.org/1999/xhtml"><body></body></html>',
    ),
    [],
  );
  assert.ok(
    codes(
      scanText(
        "public/remote.html",
        '<!doctype html><html><body data-source="https://cdn.invalid/data"></body></html>',
      ),
    ).includes("remote-asset"),
  );
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

test("Rust scanner tracks imported Tauri aliases and every handler occurrence", async (t) => {
  const root = await fixture({
    "src-tauri/src/lib.rs": `
      use tauri::{command as mobile_command, generate_handler as handlers};
      use tauri as runtime;
      #[mobile_command]
      fn first() {}
      #[runtime::command]
      fn second() {}
      fn wire() {
        let _ = handlers![first];
        let _ = handlers![second];
        let _ = runtime::generate_handler![third];
      }
    `,
  });
  t.after(() => rm(root, { recursive: true, force: true }));
  const handlers = scanSource(root).filter(
    (item) => item.code === "invoke-handler",
  );
  assert.equal(handlers.length, 5);
});

test("Rust scanner follows chained use and extern-crate Tauri provenance", async (t) => {
  const root = await fixture({
    "src-tauri/src/lib.rs": `
      use tauri as runtime;
      use runtime as chained_runtime;
      use chained_runtime::command as mobile_command;
      use chained_runtime::generate_handler as handlers;
      extern crate tauri as external_runtime;
      use external_runtime::command as external_command;
      #[mobile_command]
      fn first() {}
      #[external_command]
      fn second() {}
      fn wire() {
        let _ = handlers![first];
        let _ = external_runtime::generate_handler![second];
      }
    `,
  });
  t.after(() => rm(root, { recursive: true, force: true }));
  const handlers = scanSource(root).filter(
    (item) => item.code === "invoke-handler",
  );
  assert.equal(handlers.length, 4);
});

test("Rust scanner follows self aliases and absolute Tauri use trees", async (t) => {
  const root = await fixture({
    "src-tauri/src/lib.rs": `
      use tauri::{self as runtime};
      use ::tauri::command as mobile_command;
      use ::tauri::generate_handler as handlers;
      #[runtime::command]
      fn first() {}
      #[mobile_command]
      fn second() {}
      fn wire() {
        let _ = runtime::generate_handler![first];
        let _ = handlers![second];
      }
      // #[runtime::command]
      const NOTE: &str = "handlers![fake] use ::tauri::command as fake";
    `,
  });
  t.after(() => rm(root, { recursive: true, force: true }));
  const handlers = scanSource(root).filter(
    (item) => item.code === "invoke-handler",
  );
  assert.equal(handlers.length, 4);
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

test("Rust dependency declarations reject renamed remote escaping and override sources", () => {
  const cases = [
    '[dependencies]\ntauri = { package = "reqwest", version = "1" }\n',
    '[dependencies]\ntauri = { git = "https://example.invalid/tauri", version = "2" }\n',
    '[dependencies]\ntauri = { path = "../../mobile/src-tauri" }\n',
    "[dependencies]\ntauri = { workspace = true }\n",
    '[patch.crates-io]\ntauri = { git = "https://example.invalid/fork" }\n',
    '[replace]\n"tauri:2.0.0" = { path = "../../mobile/src-tauri" }\n',
  ];
  for (const manifest of cases) {
    assert.notDeepEqual(validateRustManifest(manifest), [], manifest);
  }
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
