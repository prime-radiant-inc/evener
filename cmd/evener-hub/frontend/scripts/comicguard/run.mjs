#!/usr/bin/env node
// Production hub + real daemon fixture. Go supplies an independently recorded literal source turn.
import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { spawn } from "node:child_process";
import { pathToFileURL } from "node:url";
import {
  chromeProfileEnvironment, chromeProfileIsolationArgs, createBrowserProcessCleanup,
  createChromeProfileDir, findChrome, parseChromeDevToolsAnnouncement, requestBrowserClose,
} from "../browserGuardProcess.mjs";
import { connectOnlyPage, navigateTo, evaluate, waitForHttp, devtoolsHttpURL } from "../browserGuardCdp.mjs";

export async function run(fixturePath) {
  const fixture = JSON.parse(readFileSync(fixturePath, "utf8"));
  if (!fixture.baseURL || !fixture.refs?.live || !fixture.evidenceDir || !fixture.expectedSourceText) throw new Error("incomplete real-session fixture");
  mkdirSync(fixture.evidenceDir, { recursive: true });
  const profileDir = createChromeProfileDir("comicguard-");
  const lifecycle = createBrowserProcessCleanup({ profileDir });
  let page;
  let endpoint;
  const chrome = spawn(findChrome(), ["--headless=new", "--disable-gpu", ...chromeProfileIsolationArgs(), "--remote-debugging-port=0", `--user-data-dir=${profileDir}`, "--no-first-run", "--disable-extensions", "--window-size=1200,900", "about:blank"], { stdio: ["ignore", "ignore", "pipe"], env: chromeProfileEnvironment(profileDir), detached: process.platform !== "win32" });
  lifecycle.addChild(chrome, { processGroupId: process.platform === "win32" ? null : chrome.pid, gracefulClose: () => requestBrowserClose(endpoint) });
  const stop = async () => { try { page?.close(); } catch {} await lifecycle.cleanup(); };
  process.once("SIGTERM", () => { void stop().then(() => process.exit(143)); });
  process.once("SIGINT", () => { void stop().then(() => process.exit(130)); });
  try {
    endpoint = await new Promise((resolve, reject) => {
      let buffer = "";
      const timer = setTimeout(() => reject(new Error("Chrome DevTools endpoint unavailable")), 15000);
      const settle = (fn, value) => { clearTimeout(timer); fn(value); };
      chrome.once("error", (error) => settle(reject, error));
      chrome.once("exit", (code) => settle(reject, new Error(`Chrome exited ${code} before readiness`)));
      chrome.stderr.on("data", (chunk) => {
        buffer += String(chunk);
        const lines = buffer.split(/\r?\n/);
        buffer = lines.pop() ?? "";
        for (const line of lines) {
          if (!line.startsWith("DevTools listening on ")) continue;
          try { const parsed = parseChromeDevToolsAnnouncement(line); if (parsed) settle(resolve, parsed); }
          catch (error) { settle(reject, error); }
        }
      });
    });
    await waitForHttp(devtoolsHttpURL(endpoint, "/json/version"), "chrome devtools endpoint");
    page = await connectOnlyPage(endpoint);
    await page.send("Page.enable");
    await page.send("Runtime.enable");
    await navigateTo(page, fixture.baseURL);
    const base = new URL(fixture.baseURL).origin;
    const route = `/comic/s/${encodeURIComponent(fixture.refs.live)}`;
    await navigateTo(page, `${base}${route}`);
    const deadline = Date.now() + 30000;
    let observed;
    do {
      observed = await evaluate(page.send, `(() => ({ path: location.pathname, route: document.querySelector('main')?.getAttribute('data-comic-route'), ref: document.querySelector('main')?.getAttribute('data-session-ref'), text: document.querySelector('ol[aria-label="Conversation"]')?.textContent ?? '', alert: document.querySelector('[role="alert"]')?.textContent ?? '' }))()`);
      if (observed?.path === route && observed?.route === "session" && observed?.ref === fixture.refs.live && observed?.text?.includes(fixture.expectedSourceText)) break;
      await new Promise((resolve) => setTimeout(resolve, 100));
    } while (Date.now() < deadline);
    writeFileSync(`${fixture.evidenceDir}/reader.json`, JSON.stringify({ route, expectedSourceText: fixture.expectedSourceText, observed }, null, 2));
    const screenshot = await page.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(`${fixture.evidenceDir}/reader.png`, Buffer.from(screenshot.result.data, "base64"));
    if (observed?.path !== route || observed?.route !== "session" || observed?.ref !== fixture.refs.live || !observed?.text?.includes(fixture.expectedSourceText)) throw new Error(`literal source not shown at authenticated route: ${JSON.stringify(observed)}`);
    console.log(`Comic Chat real session read passed: ${fixture.evidenceDir}/reader.json and reader.png`);
  } finally { await stop(); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = process.argv.slice(2);
  if (args.length !== 2 || args[0] !== "--fixture") { console.error("usage: node scripts/comicguard/run.mjs --fixture <absolute-json-path>"); process.exitCode = 2; }
  else run(args[1]).catch((error) => { console.error(error); process.exitCode = 1; });
}
