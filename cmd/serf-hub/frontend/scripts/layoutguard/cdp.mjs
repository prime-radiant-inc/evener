// A headless-Chrome CDP driver with no npm dependencies (Node has a native
// WebSocket client). Launches its OWN Chrome instance with a fresh temp
// profile and a random debug port, navigates to a file:// URL, runs a JS
// expression in the page, returns the JSON-serializable result, and quits.
//
// Deliberately file:// only, and deliberately its own throwaway profile/port:
// the shared Chrome MCP server (`use_browser`) has ONE sticky profile, so
// concurrent agents land in each other's sessions (kata 8ecz) - not viable as
// a gate. This never touches that shared instance, and never touches the
// serf-hub dev server's port (9180) because it never navigates to http(s).
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

const CHROME_CANDIDATES = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];

function findChrome() {
  for (const p of CHROME_CANDIDATES) {
    if (existsSync(p)) return p;
  }
  throw new Error(
    `no Chrome/Chromium binary found (looked at: ${CHROME_CANDIDATES.join(", ")}); layoutguard needs one of these installed`,
  );
}

/**
 * @param {string} fileUrl - must start with file://
 * @param {string} expr - JS expression to evaluate in the page after load
 * @returns {Promise<unknown>} the JSON-serializable value the expression evaluates to
 */
export async function evalInFreshChrome(fileUrl, expr) {
  if (!fileUrl.startsWith("file://")) {
    throw new Error(
      "evalInFreshChrome only navigates file:// URLs - layoutguard cases are static files, no dev server",
    );
  }

  const chromeBin = findChrome();
  const profileDir = mkdtempSync(path.join(tmpdir(), "layoutguard-chrome-profile-"));
  // Randomized, and explicitly never the shared serf-hub dev port.
  let port;
  do {
    port = 20000 + Math.floor(Math.random() * 20000);
  } while (port === 9180);

  const chrome = spawn(
    chromeBin,
    [
      "--headless=new",
      "--disable-gpu",
      `--remote-debugging-port=${port}`,
      `--user-data-dir=${profileDir}`,
      "--no-first-run",
      "--disable-extensions",
      "about:blank",
    ],
    { stdio: "ignore" },
  );

  const cleanup = () => {
    try {
      chrome.kill();
    } catch {
      // already dead
    }
    try {
      rmSync(profileDir, { recursive: true, force: true });
    } catch {
      // best-effort
    }
  };

  try {
    await waitForCdp(port);
    return await withPage(port, fileUrl, expr);
  } finally {
    cleanup();
  }
}

async function waitForCdp(port) {
  for (let i = 0; i < 100; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${port}/json/version`);
      if (res.ok) return;
    } catch {
      // chrome not listening yet
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("chrome devtools endpoint never came up");
}

async function withPage(port, fileUrl, expr) {
  const listRes = await fetch(`http://127.0.0.1:${port}/json/list`);
  const targets = await listRes.json();
  const page = targets.find((t) => t.type === "page");
  if (!page) throw new Error("no page target");

  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", reject, { once: true });
  });

  try {
    let id = 0;
    const pending = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id !== undefined && pending.has(msg.id)) {
        pending.get(msg.id)(msg);
        pending.delete(msg.id);
      }
    });
    const send = (method, params = {}) => {
      const thisId = ++id;
      return new Promise((resolve) => {
        pending.set(thisId, resolve);
        ws.send(JSON.stringify({ id: thisId, method, params }));
      });
    };

    await send("Page.enable");
    const navDone = new Promise((resolve) => {
      const handler = (ev) => {
        const msg = JSON.parse(ev.data);
        if (msg.method === "Page.loadEventFired") {
          ws.removeEventListener("message", handler);
          resolve();
        }
      };
      ws.addEventListener("message", handler);
    });
    await send("Page.navigate", { url: fileUrl });
    await navDone;

    // Belt-and-suspenders guard rail: refuse to proceed if this ever lands
    // anywhere but the static file it was told to load.
    const guard = await send("Runtime.evaluate", {
      expression: "location.protocol + '//' + location.host",
      returnByValue: true,
    });
    const origin = guard.result.result.value;
    if (origin.includes("9180")) {
      throw new Error("refusing: this eval landed on port 9180 (the shared serf-hub dev server)");
    }
    if (!origin.startsWith("file://")) {
      throw new Error(`refusing: expected a file:// origin, got ${origin}`);
    }

    const evalRes = await send("Runtime.evaluate", {
      expression: expr,
      returnByValue: true,
      awaitPromise: true,
    });
    if (evalRes.result.exceptionDetails) {
      throw new Error(`page eval threw: ${JSON.stringify(evalRes.result.exceptionDetails)}`);
    }
    return evalRes.result.result.value;
  } finally {
    ws.close();
  }
}
