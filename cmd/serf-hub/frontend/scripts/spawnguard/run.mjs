#!/usr/bin/env node
// spawnguard - checks the real Spawn React tree at the mobile breakpoint and
// scans the rendered page for horizontal overflow.
//
// This is intentionally a browser guard rather than a CSS/source assertion:
// it uses the production Spawn component and actual viewport metrics at 390px,
// 899px, and 900px. It is deterministic because the harness uses FakeClient,
// and it has no dependency on provider credentials or the shared dev server.
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const FRONTEND = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const WIDTHS = [390, 899, 900];
// The staging cap (attachments/limits.ts MAX_ATTACHMENTS), so the row is
// measured at the widest the product allows it to get.
const STAGED_ATTACHMENTS = 8;
const TILE_PX = 80;
const CHROME_CANDIDATES = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];

function findChrome() {
  for (const candidate of CHROME_CANDIDATES) if (existsSync(candidate)) return candidate;
  throw new Error(`no Chrome/Chromium found (looked at: ${CHROME_CANDIDATES.join(", ")})`);
}

function pickPort() {
  return 20000 + Math.floor(Math.random() * 20000);
}

async function waitForHttp(url, label) {
  for (let attempt = 0; attempt < 300; attempt++) {
    try {
      if ((await fetch(url)).ok) return;
    } catch {
      // The child process is still starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`${label} never came up at ${url}`);
}

function connect(wsUrl) {
  const ws = new WebSocket(wsUrl);
  let id = 0;
  const pending = new Map();
  ws.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (message.id !== undefined && pending.has(message.id)) {
      pending.get(message.id)(message);
      pending.delete(message.id);
    }
  });
  const ready = new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", reject, { once: true });
  });
  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const requestId = ++id;
      pending.set(requestId, (message) => {
        if (message.error) reject(new Error(`${method}: ${JSON.stringify(message.error)}`));
        else resolve(message);
      });
      ws.send(JSON.stringify({ id: requestId, method, params }));
    });
  return { ws, ready, send };
}

async function measureAt(cdpPort, vitePort, width) {
  const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
  const target = targets.find((entry) => entry.type === "page");
  if (!target) throw new Error("chrome exposed no page target");
  const { ws, ready, send } = connect(target.webSocketDebuggerUrl);
  await ready;
  try {
    await send("Page.enable");
    await send("Runtime.enable");
    await send("Emulation.setDeviceMetricsOverride", {
      width,
      height: 900,
      deviceScaleFactor: 1,
      mobile: false,
    });
    const loaded = new Promise((resolve) => {
      const handler = (event) => {
        if (JSON.parse(event.data).method === "Page.loadEventFired") {
          ws.removeEventListener("message", handler);
          resolve();
        }
      };
      ws.addEventListener("message", handler);
    });
    await send("Page.navigate", { url: `http://127.0.0.1:${vitePort}/spawnguard.html` });
    await loaded;
    await send("Runtime.evaluate", { expression: "window.settledSpawn", awaitPromise: true, returnByValue: true });
    // Stage before measuring, at every width: the page is navigated fresh per
    // width, and the staged-attachment row exists only once something is in
    // it. A staging failure has to name itself here rather than surfacing
    // later as an empty row that reads like a layout regression.
    const staged = await send("Runtime.evaluate", {
      expression: `window.stageSpawnAttachments(${STAGED_ATTACHMENTS})`,
      awaitPromise: true,
      returnByValue: true,
    });
    if (staged.result.exceptionDetails) {
      const detail = staged.result.exceptionDetails;
      throw new Error(`staging attachments at ${width}px failed: ${detail.exception?.description ?? detail.text}`);
    }
    const output = await send("Runtime.evaluate", {
      expression: "JSON.stringify(window.measureSpawn())",
      returnByValue: true,
    });
    return JSON.parse(output.result.result.value);
  } finally {
    await send("Emulation.clearDeviceMetricsOverride").catch(() => {});
    ws.close();
  }
}

function assertResult(result, expectedWidth) {
  const failures = [];
  const mobile = expectedWidth <= 899;
  const visible = (value) => !("error" in value) && value.display !== "none" && value.visibility !== "hidden";
  if (result.viewport.width !== expectedWidth) failures.push(`viewport is ${result.viewport.width}px, expected ${expectedWidth}px`);
  if (visible(result.mobileConfig) !== mobile) failures.push(`mobile config visibility is wrong at ${expectedWidth}px`);
  if (visible(result.desktopConfig) === mobile) failures.push(`desktop config visibility is wrong at ${expectedWidth}px`);
  if (visible(result.mobileTitle) !== mobile) failures.push(`mobile title visibility is wrong at ${expectedWidth}px`);
  if (visible(result.desktopTitle) === mobile) failures.push(`desktop title visibility is wrong at ${expectedWidth}px`);
  if (visible(result.mobileIntro) !== mobile) failures.push(`prompt orientation visibility is wrong at ${expectedWidth}px`);

  if (mobile) {
    const action = result.actionBand;
    if (action.position !== "fixed") failures.push(`action band position is ${action.position}, expected fixed`);
    if (Math.abs(action.left) > 1 || Math.abs(action.bottom) > 1 || Math.abs(action.right - expectedWidth) > 1) {
      failures.push(`action band is not viewport-pinned: ${JSON.stringify(action)}`);
    }
    if (action.width < expectedWidth - 1 || Number.parseFloat(action.minHeight) < 76 || action.height < 76) {
      failures.push(`action band geometry is too small: ${JSON.stringify(action)}`);
    }
    if (result.rows.length !== 6) failures.push(`expected 6 mobile setting rows, found ${result.rows.length}`);
    for (const row of result.rows) {
      if (row.minHeight !== "48px" || row.height < 48) failures.push(`row ${row.label} is below 48px: ${JSON.stringify(row)}`);
    }
    const prompt = result.accessiblePrompt;
    if (
      prompt.headingTag !== "h3" ||
      prompt.headingText !== "What should the agent do?" ||
      !prompt.headingVisible ||
      prompt.subtitleTag !== "p" ||
      prompt.subtitleText !== "Leave blank to start a dormant session." ||
      !prompt.subtitleVisible ||
      prompt.headingHiddenFromAT ||
      prompt.subtitleHiddenFromAT
    ) {
      failures.push(`prompt orientation is not persistently accessible: ${JSON.stringify(prompt)}`);
    }
  } else {
    if (result.actionBand.position === "fixed") failures.push("desktop action band unexpectedly remains fixed");
  }

  // Staged attachments (kata 289v). The harness stages them through the
  // pane's own file picker before this runs, so a zero count here means the
  // row never entered the measured tree - the whole point of the case.
  const staged = result.attachments;
  if (staged.tiles.length !== STAGED_ATTACHMENTS) {
    failures.push(`expected ${STAGED_ATTACHMENTS} staged attachment tiles in the tree, found ${staged.tiles.length}`);
  }
  if (staged.row === null) {
    failures.push("staged-attachment row is not in the measured tree");
  } else if (staged.row.right > expectedWidth + 1 || staged.row.left < -1) {
    failures.push(`staged-attachment row escapes the viewport: ${JSON.stringify(staged.row)}`);
  }
  for (const [index, tile] of staged.tiles.entries()) {
    if (Math.abs(tile.width - TILE_PX) > 0.5 || Math.abs(tile.height - TILE_PX) > 0.5) {
      failures.push(`attachment tile ${index} is ${tile.width}x${tile.height}, expected ${TILE_PX}x${TILE_PX}`);
    }
    if (tile.right > expectedWidth + 1 || tile.left < -1) {
      failures.push(`attachment tile ${index} escapes the viewport: ${JSON.stringify(tile)}`);
    }
    if (staged.row !== null && (tile.right > staged.row.right + 1 || tile.left < staged.row.left - 1)) {
      failures.push(
        `attachment tile ${index} escapes its own row: ${JSON.stringify(tile)} vs ${JSON.stringify(staged.row)}`,
      );
    }
    // Redundant with the staging wait, which already blocks on this exact
    // condition - it cannot fail while that wait is in place, and it is here
    // for the harness change that drops the wait. Not independent coverage.
    if (!tile.decoded) failures.push(`attachment tile ${index} never decoded its thumbnail`);
  }
  // Fixed-size boxes in a flex-wrap row: where the row is too narrow to hold
  // every tile side by side (ignoring gaps, which only make it narrower), a
  // single line means the row is overflowing rather than wrapping. Read off
  // the MEASURED row width rather than the viewport, so this says nothing at
  // a width where one line is the right answer.
  const tooNarrowForOneLine = staged.row !== null && staged.row.width < staged.tiles.length * TILE_PX;
  if (tooNarrowForOneLine && staged.rowCount < 2) {
    failures.push(`${staged.tiles.length} tiles sit on one line inside a ${staged.row.width}px row instead of wrapping`);
  }

  if (result.overflow.length > 0) failures.push(`horizontal overflow: ${result.overflow.join("; ")}`);
  return failures;
}

async function main() {
  const vitePort = pickPort();
  const cdpPort = pickPort();
  const profileDir = mkdtempSync(path.join(tmpdir(), "spawnguard-chrome-"));
  const vite = spawn(
    "./node_modules/.bin/vite",
    ["--port", String(vitePort), "--strictPort", "--host", "127.0.0.1", "--clearScreen", "false"],
    { cwd: FRONTEND, stdio: ["ignore", "ignore", "pipe"] },
  );
  let viteErr = "";
  vite.stderr.on("data", (chunk) => {
    viteErr += chunk;
  });
  const chrome = spawn(
    findChrome(),
    [
      "--headless=new",
      "--disable-gpu",
      `--remote-debugging-port=${cdpPort}`,
      `--user-data-dir=${profileDir}`,
      "--no-first-run",
      "--disable-extensions",
      "about:blank",
    ],
    { stdio: "ignore" },
  );
  const cleanup = () => {
    for (const process of [chrome, vite]) {
      try {
        process.kill();
      } catch {
        // The child already exited.
      }
    }
    try {
      rmSync(profileDir, { recursive: true, force: true });
    } catch {
      // Best effort cleanup of the private profile.
    }
  };

  let failed = 0;
  try {
    try {
      await waitForHttp(`http://127.0.0.1:${vitePort}/spawnguard.html`, "vite dev server");
    } catch (error) {
      throw new Error(`${error.message}\nvite stderr:\n${viteErr}`);
    }
    await waitForHttp(`http://127.0.0.1:${cdpPort}/json/version`, "chrome devtools endpoint");
    for (const width of WIDTHS) {
      const result = await measureAt(cdpPort, vitePort, width);
      const failures = assertResult(result, width);
      if (failures.length === 0) {
        console.log(
          `${width}px ... PASS - Spawn breakpoint, action band, rows, accessibility, ${STAGED_ATTACHMENTS} staged attachment tiles, and overflow`,
        );
      } else {
        failed++;
        console.log(`${width}px ... FAIL`);
        for (const failure of failures) console.log(`    ${failure}`);
      }
    }
  } finally {
    cleanup();
  }
  process.exit(failed > 0 ? 1 : 0);
}

main().catch((error) => {
  console.error(error.message);
  process.exit(2);
});
