// Exact sandbox-only CDP pipe driver retained for Task 4 review.
// This file is copied over scripts/layoutguard/cdp.mjs only during layoutguard,
// then the repository driver is restored byte-for-byte.
import { spawn } from "node:child_process";
import { appendFileSync, existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { diagnoseRealizedViewport } from "./viewport.mjs";

const CHROME_CANDIDATES = [
  process.env.SERF_HEADLESS_CHROME,
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
].filter(Boolean);

function findChrome() {
  for (const candidate of CHROME_CANDIDATES) if (existsSync(candidate)) return candidate;
  throw new Error(`no Chrome binary found (looked at: ${CHROME_CANDIDATES.join(", ")})`);
}

function appendLog(file, value) {
  if (file) appendFileSync(file, value);
}

function launchPipeChrome(profileDir) {
  const chromeBin = findChrome();
  appendLog(
    process.env.SERF_CHROME_STDERR_LOG,
    `\n=== launch ${chromeBin} profile=${profileDir} ===\n`,
  );
  const chrome = spawn(
    chromeBin,
    [
      "--headless",
      "--no-sandbox",
      "--single-process",
      "--disable-gpu",
      "--allow-file-access-from-files",
      "--remote-debugging-pipe",
      `--user-data-dir=${profileDir}`,
      "--no-first-run",
      "--disable-extensions",
      "about:blank",
    ],
    {
      stdio: ["ignore", "ignore", "pipe", "pipe", "pipe"],
      env: { ...process.env, DYLD_INSERT_LIBRARIES: process.env.SERF_IOKIT_SHIM },
    },
  );
  let id = 0;
  let buffer = "";
  let stderr = "";
  const pending = new Map();
  const listeners = new Set();
  chrome.stderr.on("data", (chunk) => {
    const text = chunk.toString();
    stderr += text;
    appendLog(process.env.SERF_CHROME_STDERR_LOG, text);
  });
  chrome.stdio[4].on("data", (chunk) => {
    buffer += chunk.toString();
    for (;;) {
      const end = buffer.indexOf("\0");
      if (end < 0) break;
      const raw = buffer.slice(0, end);
      buffer = buffer.slice(end + 1);
      if (!raw) continue;
      const message = JSON.parse(raw);
      if (message.id !== undefined && pending.has(message.id)) {
        const { resolve, reject } = pending.get(message.id);
        pending.delete(message.id);
        if (message.error) reject(new Error(`CDP ${message.error.code}: ${message.error.message}`));
        else resolve(message);
      }
      for (const listener of listeners) listener(message);
    }
  });
  chrome.on("exit", (code, signal) => {
    if (pending.size === 0) return;
    const error = new Error(`headless Chrome exited (${code ?? signal})\n${stderr}`);
    for (const { reject } of pending.values()) reject(error);
    pending.clear();
  });
  const send = (method, params = {}, sessionId = undefined) => {
    const thisId = ++id;
    const message = { id: thisId, method, params };
    if (sessionId) message.sessionId = sessionId;
    return new Promise((resolve, reject) => {
      pending.set(thisId, { resolve, reject });
      chrome.stdio[3].write(`${JSON.stringify(message)}\0`);
    });
  };
  return { chrome, listeners, send };
}

export async function evalInFreshChrome(fileUrl, expr, forcePseudoStates = [], viewport = null) {
  if (!fileUrl.startsWith("file://")) throw new Error("evalInFreshChrome only navigates file:// URLs");
  const profileRoot = process.env.SERF_CHROME_PROFILE_ROOT || tmpdir();
  const profileDir = mkdtempSync(path.join(profileRoot, "layoutguard-chrome-profile-"));
  const cdp = launchPipeChrome(profileDir);
  const cleanup = () => {
    try {
      cdp.chrome.kill();
    } catch {}
    try {
      rmSync(profileDir, { recursive: true, force: true });
    } catch {}
  };
  try {
    const browserVersion = await cdp.send("Browser.getVersion");
    appendLog(process.env.SERF_CDP_MEASUREMENTS, `${JSON.stringify({ browserVersion: browserVersion.result })}\n`);
    const targets = await cdp.send("Target.getTargets");
    const page = targets.result.targetInfos.find((target) => target.type === "page");
    if (!page) throw new Error("no page target");
    const attached = await cdp.send("Target.attachToTarget", { targetId: page.targetId, flatten: true });
    const sessionId = attached.result.sessionId;
    const send = (method, params = {}) => cdp.send(method, params, sessionId);
    await send("Page.enable");
    if (viewport) {
      await send("Emulation.setDeviceMetricsOverride", {
        width: viewport.width,
        height: viewport.height,
        deviceScaleFactor: viewport.deviceScaleFactor ?? 1,
        mobile: viewport.mobile ?? false,
        screenWidth: viewport.width,
        screenHeight: viewport.height,
      });
    }
    const navDone = new Promise((resolve) => {
      const listener = (message) => {
        if (message.sessionId === sessionId && message.method === "Page.loadEventFired") {
          cdp.listeners.delete(listener);
          resolve();
        }
      };
      cdp.listeners.add(listener);
    });
    await send("Page.navigate", { url: fileUrl });
    await navDone;
    const guard = await send("Runtime.evaluate", {
      expression: "location.protocol + '//' + location.host",
      returnByValue: true,
    });
    const origin = guard.result.result.value;
    if (!origin.startsWith("file://")) throw new Error(`refusing: expected file:// origin, got ${origin}`);
    if (viewport) {
      const realizedResponse = await send("Runtime.evaluate", {
        expression: `JSON.stringify({
          windowInnerWidth: window.innerWidth,
          windowInnerHeight: window.innerHeight,
          documentClientWidth: document.documentElement.clientWidth,
          documentClientHeight: document.documentElement.clientHeight,
          visualViewportWidth: window.visualViewport ? window.visualViewport.width : null,
          visualViewportHeight: window.visualViewport ? window.visualViewport.height : null
        })`,
        returnByValue: true,
      });
      const diagnostic = diagnoseRealizedViewport(viewport, JSON.parse(realizedResponse.result.result.value));
      if (diagnostic) throw new Error(diagnostic);
    }
    if (forcePseudoStates.length > 0) {
      await send("DOM.enable");
      await send("CSS.enable");
      const document = await send("DOM.getDocument", { depth: -1 });
      for (const { selector, pseudoClasses } of forcePseudoStates) {
        const found = await send("DOM.querySelector", { nodeId: document.result.root.nodeId, selector });
        if (!found.result?.nodeId) throw new Error(`forcePseudoStates: no element matches ${selector}`);
        await send("CSS.forcePseudoState", {
          nodeId: found.result.nodeId,
          forcedPseudoClasses: pseudoClasses,
        });
      }
    }
    const evaluated = await send("Runtime.evaluate", {
      expression: expr,
      returnByValue: true,
      awaitPromise: true,
    });
    if (evaluated.result.exceptionDetails) {
      throw new Error(`page eval threw: ${JSON.stringify(evaluated.result.exceptionDetails)}`);
    }
    const value = evaluated.result.result.value;
    appendLog(process.env.SERF_CDP_MEASUREMENTS, `${JSON.stringify({ fileUrl, value })}\n`);
    return value;
  } finally {
    cleanup();
  }
}
