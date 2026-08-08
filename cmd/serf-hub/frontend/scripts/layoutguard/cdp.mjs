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
import { diagnoseRealizedViewport } from "./viewport.mjs";

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
 * Probes whether Chrome can start and CDP is reachable. Fails fast if not, with
 * diagnostic info. Call this ONCE before iterating cases to avoid cascading
 * identical environment-failure errors.
 * @param {Function} [spawnProcess=spawn] - injectable spawn function for testing
 * @returns {Promise<{chromeBin: string, args: string[], launchStderr: string}>}
 *   diagnostic info to include if Chrome startup subsequently fails in a case
 */
export async function probeBrowserCapability(spawnProcess = spawn) {
  const chromeBin = findChrome();
  const profileDir = mkdtempSync(path.join(tmpdir(), "layoutguard-chrome-probe-"));
  let port;
  do {
    port = 20000 + Math.floor(Math.random() * 20000);
  } while (port === 9180);

  const args = [
    "--headless=new",
    "--disable-gpu",
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profileDir}`,
    "--no-first-run",
    "--disable-extensions",
    "about:blank",
  ];

  let launchStderr = "";
  const chrome = spawnProcess(chromeBin, args, {
    stdio: ["ignore", "ignore", "pipe"],
  });

  const stderrChunks = [];
  chrome.stderr.on("data", (chunk) => {
    stderrChunks.push(chunk);
  });

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
    return {
      chromeBin,
      args,
      launchStderr: Buffer.concat(stderrChunks).toString("utf8"),
    };
  } catch (err) {
    launchStderr = Buffer.concat(stderrChunks).toString("utf8");
    const stderrHint = launchStderr
      ? `\n\nChrome stderr:\n${launchStderr}`
      : "\n(no stderr captured; check Chrome binary permissions and system resources)";
    throw new Error(
      `Chrome startup failed (environment problem, not a test case failure).\n` +
        `Chrome binary: ${chromeBin}\n` +
        `Launch args: ${args.join(" ")}\n` +
        `\nTo remediate:\n` +
        `1. Verify Chrome/Chromium is installed and the binary is executable.\n` +
        `2. Check system resources (memory, temp directory).\n` +
        `3. Ensure no port conflict on ${port}.\n` +
        `Diagnostic: ${err.message}${stderrHint}`,
    );
  } finally {
    cleanup();
  }
}

/**
 * @param {string} fileUrl - must start with file://
 * @param {string} expr - JS expression to evaluate in the page after load
 * @param {{selector: string, pseudoClasses: string[]}[]} [forcePseudoStates] - pseudo-classes
 *   to pin ON before evaluating, via CSS.forcePseudoState (the same mechanism DevTools' own
 *   ":hov" toggle uses). Needed because some states cannot be reached from a page script at
 *   all: there is no way to synthesize a trusted hover, and a programmatic .focus() does NOT
 *   match :focus-visible (measured in this Chrome - it stayed unmatched with opacity 0). This
 *   pins the SELECTOR match, so it proves the cascade applies the rule and nothing overrides
 *   it; whether Chrome's own heuristic decides a given focus is "visible" is Chrome's contract,
 *   not ours. A selector that matches no element is an error, never a silent no-op.
 * @param {{width: number, height: number, deviceScaleFactor?: number, mobile?: boolean} | null} [viewport]
 *   explicit viewport metrics for a case that must run at fixed browser dimensions rather than
 *   inheriting Chrome's ambient default window size.
 * @returns {Promise<unknown>} the JSON-serializable value the expression evaluates to
 */
export async function evalInFreshChrome(fileUrl, expr, forcePseudoStates = [], viewport = null) {
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
    return await withPage(port, fileUrl, expr, forcePseudoStates, viewport);
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

async function withPage(port, fileUrl, expr, forcePseudoStates, viewport) {
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

    if (viewport) {
      const realizedRes = await send("Runtime.evaluate", {
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
      const realized = JSON.parse(realizedRes.result.result.value);
      const viewportDiagnostic = diagnoseRealizedViewport(viewport, realized);
      if (viewportDiagnostic) throw new Error(viewportDiagnostic);
    }

    if (forcePseudoStates.length > 0) {
      await send("DOM.enable");
      await send("CSS.enable");
      const doc = await send("DOM.getDocument", { depth: -1 });
      const rootId = doc.result.root.nodeId;
      for (const { selector, pseudoClasses } of forcePseudoStates) {
        const found = await send("DOM.querySelector", { nodeId: rootId, selector });
        // DOM.querySelector answers with nodeId 0 for "no match" rather than
        // failing - forcing nothing would leave the case measuring the resting
        // state while reporting the forced one, so it stops here instead.
        if (!found.result?.nodeId) throw new Error(`forcePseudoStates: no element matches ${selector}`);
        await send("CSS.forcePseudoState", { nodeId: found.result.nodeId, forcedPseudoClasses: pseudoClasses });
      }
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
