#!/usr/bin/env node
// record.mjs — scene-driven motion recorder for the improvements movie.
//
// Drives its own headless Chrome over CDP against the vite dev server,
// draws a visible cursor (automation with an invisible pointer reads as a
// haunted UI), types at human pace, and captures deliberate frames per beat
// into shots/<scene-id>/frame-NNNN.png for assemble's `frames` kind.
//
// Usage: node record.mjs scenes-motion.yaml shots/ [--base http://127.0.0.1:5199]
//        [--only scene-id] [--chrome /usr/bin/google-chrome]
//
// Scene verbs (see scenes-motion.yaml): goto, wait_for, click, type, key,
// pause, eval, assert_text, assert_gone. assert_* verbs make a recording
// pass double as a live E2E: a failed assertion aborts the pass nonzero, so
// a shipped movie is also a passed test.

import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

const args = process.argv.slice(2);
const [scenesFile, shotsDir] = args;
if (!scenesFile || !shotsDir) {
  console.error("usage: record.mjs SCENES.yaml SHOTS_DIR [--base URL] [--only id] [--chrome BIN]");
  process.exit(2);
}
const opt = (name, dflt) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : dflt;
};
const BASE = opt("--base", "http://127.0.0.1:5199");
const ONLY = opt("--only", null);
const FROM = opt("--from", null);
const CHROME = opt("--chrome", "/usr/bin/google-chrome");
const WIDTH = 1920;
const HEIGHT = 1080;
const FPS = 8; // deliberate frame cadence; assemble plays frames dirs at `rate`

// --- tiny YAML loader (scene files use a strict subset; no deps) ---------
// Supported: top-level `scenes:` list of maps; scalar values; `actions:`
// list of single-key maps whose value is a scalar or inline map.
function parseScenes(text) {
  const scenes = [];
  let scene = null;
  let inActions = false;
  for (const raw of text.split("\n")) {
    // Full-line comments only: '#' appears inside CSS selectors ('#spawn-cwd'),
    // so inline stripping would eat real values.
    if (/^\s*#/.test(raw)) continue;
    const line = raw.trimEnd();
    if (!line.trim()) continue;
    let m;
    if ((m = /^  - id:\s*(.+)$/.exec(line))) {
      scene = { id: unq(m[1]), actions: [] };
      scenes.push(scene);
      inActions = false;
    } else if (/^\s+actions:\s*$/.test(line)) {
      inActions = true;
    } else if (inActions && (m = /^\s+- (\w+):\s*(.*)$/.exec(line))) {
      scene.actions.push({ verb: m[1], arg: parseArg(m[2]) });
    } else if (scene && (m = /^    (\w+):\s*(.+)$/.exec(line))) {
      scene[m[1]] = unq(m[2]);
    }
  }
  return scenes;
}
function unq(s) {
  s = s.trim();
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) return s.slice(1, -1);
  return s;
}
function parseArg(s) {
  s = s.trim();
  if (s.startsWith("{")) {
    const out = {};
    for (const kv of s.slice(1, -1).split(/,\s*(?=[a-z_]+:)/)) {
      const i = kv.indexOf(":");
      out[kv.slice(0, i).trim()] = unq(kv.slice(i + 1));
    }
    return out;
  }
  return unq(s);
}

// --- CDP plumbing --------------------------------------------------------
const profile = mkdtempSync(path.join(tmpdir(), "record-chrome-"));
const port = 9500 + Math.floor(Math.random() * 400);
const chrome = spawn(
  CHROME,
  [
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    "--headless=new",
    "--no-first-run",
    "--no-sandbox",
    "--disable-gpu",
    "--force-device-scale-factor=1",
    `--window-size=${WIDTH},${HEIGHT}`,
    "--autoplay-policy=no-user-gesture-required",
    "about:blank",
  ],
  { stdio: "ignore" },
);
process.on("exit", () => {
  chrome.kill("SIGKILL");
  try {
    rmSync(profile, { recursive: true, force: true });
  } catch {}
});

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function httpJson(url, tries = 60) {
  for (let i = 0; i < tries; i++) {
    try {
      const r = await fetch(url);
      if (r.ok) return await r.json();
    } catch {}
    await sleep(200);
  }
  throw new Error(`never came up: ${url}`);
}

await httpJson(`http://127.0.0.1:${port}/json/version`);
const page = (await httpJson(`http://127.0.0.1:${port}/json/list`)).find((t) => t.type === "page");
const ws = new WebSocket(page.webSocketDebuggerUrl);
let msgId = 0;
const pending = new Map();
const send = (method, params = {}) =>
  new Promise((resolve, reject) => {
    const id = ++msgId;
    pending.set(id, { resolve, reject, method });
    ws.send(JSON.stringify({ id, method, params }));
  });
ws.onmessage = (e) => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) {
    const { resolve, reject, method } = pending.get(m.id);
    pending.delete(m.id);
    m.error ? reject(new Error(`${method}: ${m.error.message}`)) : resolve(m.result);
  }
};
await new Promise((r) => (ws.onopen = r));
await send("Page.enable");
await send("Runtime.enable");
await send("Emulation.setDeviceMetricsOverride", {
  width: WIDTH,
  height: HEIGHT,
  deviceScaleFactor: 1,
  mobile: false,
});

// Visible cursor on every page (recording-motion.md: an invisible pointer
// reads as a haunted UI). Injected before each document loads.
const CURSOR_JS = `(() => {
  if (window.__recCursor) return;
  const ring = document.createElement("div");
  ring.id = "__rec_cursor";
  ring.style.cssText = "position:fixed;left:-40px;top:-40px;width:22px;height:22px;" +
    "border:3px solid rgba(255,64,129,.95);border-radius:50%;pointer-events:none;" +
    "z-index:2147483647;transform:translate(-50%,-50%);" +
    "transition:left .18s ease-out,top .18s ease-out,transform .08s;box-shadow:0 0 8px rgba(255,64,129,.5)";
  const attach = () => document.body && document.body.appendChild(ring);
  document.readyState === "loading" ? document.addEventListener("DOMContentLoaded", attach) : attach();
  window.__recCursor = ring;
  window.__recMove = (x, y) => { ring.style.left = x + "px"; ring.style.top = y + "px"; };
  window.__recPress = () => { ring.style.transform = "translate(-50%,-50%) scale(.55)"; };
  window.__recRelease = () => { ring.style.transform = "translate(-50%,-50%) scale(1)"; };
})();`;
await send("Page.addScriptToEvaluateOnNewDocument", { source: CURSOR_JS });

async function evalJs(expr) {
  const r = await send("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error(`eval failed: ${r.exceptionDetails.text} ${JSON.stringify(r.exceptionDetails.exception?.description ?? "")}`);
  return r.result?.value;
}

// --- frame capture (timeout-raced: a capture in-flight across a navigation
// never resolves; a dropped frame is fine, a hung loop is not) ------------
let frameDir = null;
let frameNo = 0;
let capturing = false;
let capturePump = null;
async function captureFrame() {
  // .catch: a capture orphaned by a navigation can settle as an ERROR long
  // after the race gave up on it; an unhandled rejection would kill the pass.
  const shot = await Promise.race([
    send("Page.captureScreenshot", { format: "png" }).catch(() => null),
    sleep(700).then(() => null),
  ]);
  if (shot && frameDir) {
    writeFileSync(path.join(frameDir, `frame-${String(frameNo++).padStart(4, "0")}.png`), Buffer.from(shot.data, "base64"));
  }
}
function startCapture(dir) {
  frameDir = dir;
  frameNo = 0;
  mkdirSync(dir, { recursive: true });
  capturing = true;
  capturePump = (async () => {
    while (capturing) {
      const t0 = Date.now();
      await captureFrame();
      const left = 1000 / FPS - (Date.now() - t0);
      if (left > 0) await sleep(left);
    }
  })();
}
async function stopCapture() {
  capturing = false;
  await capturePump;
  frameDir = null;
}

// --- input helpers -------------------------------------------------------
async function centerOf(selector) {
  const v = await evalJs(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`);
  if (!v) throw new Error(`no element matches ${selector}`);
  return v;
}
async function centerOfText(text) {
  const v = await evalJs(`(() => {
    const el = [...document.querySelectorAll('button, label, [role="option"], [role="menuitem"], [role="treeitem"]')]
      .find((b) => b.textContent.includes(${JSON.stringify(text)}));
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`);
  if (!v) throw new Error(`no clickable element with text ${JSON.stringify(text)}`);
  return v;
}
async function clickAt({ x, y }) {
  await moveCursor(x, y);
  await evalJs("window.__recPress && window.__recPress()");
  await send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await sleep(70);
  await send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
  await evalJs("window.__recRelease && window.__recRelease()");
  await sleep(120);
}
async function moveCursor(x, y) {
  await evalJs(`window.__recMove && window.__recMove(${x}, ${y})`);
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y });
  await sleep(260); // let the CSS transition glide there on camera
}
async function click(selector) {
  await clickAt(await centerOf(selector));
}
async function typeText(text) {
  for (const ch of text) {
    if (ch === "\n") {
      await key("Enter");
      continue;
    }
    await send("Input.dispatchKeyEvent", { type: "keyDown", text: ch, key: ch });
    await send("Input.dispatchKeyEvent", { type: "keyUp", key: ch });
    await sleep(/[.,!?;:]/.test(ch) ? 140 : 55);
  }
}
const KEYDEFS = {
  Enter: { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" },
  Escape: { key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 },
  Tab: { key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 },
  ArrowDown: { key: "ArrowDown", code: "ArrowDown", windowsVirtualKeyCode: 40 },
  ArrowUp: { key: "ArrowUp", code: "ArrowUp", windowsVirtualKeyCode: 38 },
  Backspace: { key: "Backspace", code: "Backspace", windowsVirtualKeyCode: 8 },
};
async function key(spec) {
  // "Ctrl+k" / "Meta+Enter" / bare "Escape"
  const parts = spec.split("+");
  const base = parts.pop();
  let modifiers = 0;
  for (const p of parts) {
    modifiers |= { Alt: 1, Ctrl: 2, Control: 2, Meta: 4, Shift: 8 }[p] ?? 0;
  }
  const def = KEYDEFS[base] ?? { key: base, code: `Key${base.toUpperCase()}`, text: base };
  await send("Input.dispatchKeyEvent", { type: "keyDown", modifiers, ...def, text: modifiers & ~8 ? undefined : def.text });
  await send("Input.dispatchKeyEvent", { type: "keyUp", modifiers, key: def.key, code: def.code });
  await sleep(160);
}
async function waitFor(selector, timeoutMs = 15000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    const found = await evalJs(`!!document.querySelector(${JSON.stringify(selector)})`);
    if (found) return;
    await sleep(150);
  }
  throw new Error(`wait_for timed out: ${selector}`);
}
async function waitText(text, timeoutMs = 20000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    const found = await evalJs(`document.body.innerText.includes(${JSON.stringify(text)})`);
    if (found) return;
    await sleep(200);
  }
  throw new Error(`assert_text timed out: ${JSON.stringify(text)}`);
}

// --- scene loop ----------------------------------------------------------
const scenes = parseScenes(readFileSync(scenesFile, "utf8"));
let authed = false;
let started = FROM === null;
for (const scene of scenes) {
  if (scene.id === FROM) started = true;
  if (!started) continue;
  if (ONLY && scene.id !== ONLY) continue;
  console.log(`scene ${scene.id}`);
  const dir = path.join(shotsDir, scene.id);
  rmSync(dir, { recursive: true, force: true });
  startCapture(dir);
  try {
    for (const { verb, arg } of scene.actions) {
      switch (verb) {
        case "goto": {
          let url = BASE + arg;
          if (!authed) {
            const token = readFileSync(path.join(process.env.HOME, ".evener/auth-token"), "utf8").trim();
            url = `${BASE}/auth?token=${token}&next=${encodeURIComponent(arg)}`;
            authed = true;
          }
          await send("Page.navigate", { url });
          await sleep(1200);
          break;
        }
        case "wait_for":
          await waitFor(typeof arg === "string" ? arg : arg.selector, Number(arg.timeout ?? 15000));
          break;
        case "click":
          await click(arg);
          break;
        case "click_text":
          await clickAt(await centerOfText(arg));
          break;
        case "type":
          await typeText(arg);
          break;
        case "key":
          await key(arg);
          break;
        case "pause":
          await sleep(Number(arg) * 1000);
          break;
        case "eval":
          await evalJs(arg);
          break;
        case "assert_text":
          await waitText(arg);
          break;
        case "assert_gone": {
          // Poll: a transient toast may legitimately still name the thing
          // (e.g. "Deleted session X") for a few seconds after the state
          // change this asserts.
          const t0 = Date.now();
          let gone = false;
          while (Date.now() - t0 < 20000) {
            gone = await evalJs(`!document.body.innerText.includes(${JSON.stringify(arg)})`);
            if (gone) break;
            await sleep(400);
          }
          if (!gone) throw new Error(`assert_gone failed: ${JSON.stringify(arg)} still present`);
          break;
        }
        default:
          throw new Error(`scene ${scene.id}: unknown verb ${verb}`);
      }
    }
    // Settle beat so the last state is unmistakably on camera (the checker
    // samples at 1 Hz; sub-second finales fall between samples).
    await sleep(1600);
  } finally {
    await stopCapture();
  }
  console.log(`  ${frameNo} frames -> ${dir}`);
}
process.exit(0);
