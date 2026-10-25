#!/usr/bin/env node
// Production hub + real daemon fixture. Go supplies an independently recorded literal source turn.
import { readFileSync, mkdirSync, writeFileSync, appendFileSync } from "node:fs";
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
    await journey(page, fixture);
  } finally { await stop(); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = process.argv.slice(2);
  if (args.length !== 2 || args[0] !== "--fixture") { console.error("usage: node scripts/comicguard/run.mjs --fixture <absolute-json-path>"); process.exitCode = 2; }
  else run(args[1]).catch((error) => { console.error(error); process.exitCode = 1; });
}

async function until(label, read, accept = Boolean) {
  const deadline = Date.now() + 20000;
  let value;
  do {
    value = await read();
    if (accept(value)) return value;
    await new Promise(resolve => setTimeout(resolve, 40));
  } while (Date.now() < deadline);
  throw new Error(`${label}: ${JSON.stringify(value)}`);
}

async function journey(page, fixture) {
  const command = (command, phase = "") => appendFileSync(fixture.controlPath, `${JSON.stringify({ command, phase })}\n`);
  const complete = async (text, failed = false) => {
    command("background-start-turn", text);
    return until("real turn completion", () => {
      const records = readFileSync(fixture.requestLog, "utf8").trim().split("\n").flatMap(line => { try { return [JSON.parse(line)]; } catch { return []; } });
      const record = records.find(record => record.event === "turn-complete" && record.prompt === text);
      if (record && (failed ? record.ok || record.status !== "failed" : !record.ok)) throw new Error(`unexpected real producer outcome: ${JSON.stringify(record)}`);
      return record;
    });
  };
  command("background-mode");
  command("background-role", "history");
  for (const text of fixture.historyTexts) await complete(text);
  await complete(fixture.expectedSourceText);
  command("background-role", "precontent-error");
  const failedTurn = await complete("COMIC_PROVIDER_FAILURE", true);
  command("background-role", "history");
  writeFileSync(`${fixture.evidenceDir}/failed-turn.json`, JSON.stringify(failedTurn, null, 2));

  const metrics = [];
  const requests = new Map();
  const pageReads = [];
  let activePages = 0;
  let maxPages = 0;
  let socketsClosed = 0;
  let subscriptions = 0;
  const incoming = [];
  let canonicalFailure;
  const paused = [];
  let holdFonts = fixture.requirePrivateFonts;
  await page.send("Network.enable");
  await page.send("DOM.enable");
  await page.send("CSS.enable");
  page.ws.addEventListener("message", event => {
    const message = JSON.parse(event.data);
    if (message.method === "Network.webSocketClosed") socketsClosed++;
    if (message.method === "Fetch.requestPaused") {
      if (holdFonts) paused.push(message.params.requestId);
      else void page.send("Fetch.continueRequest", { requestId: message.params.requestId });
    }
    if (message.method === "Network.webSocketFrameSent") {
      try {
        const payload = JSON.parse(message.params.response.payloadData);
        if (payload.method === "thread/read" && payload.params?.subscribe) subscriptions++;
        if (payload.method === "thread/turns/list") {
          requests.set(payload.id, payload);
          activePages++;
          maxPages = Math.max(maxPages, activePages);
        }
      } catch {}
    }
    if (message.method === "Network.webSocketFrameReceived") {
      incoming.push(message.params.response.payloadData);
      try {
        const payload = JSON.parse(message.params.response.payloadData);
        if (requests.has(payload.id)) {
          pageReads.push({ request: requests.get(payload.id).params, turns: payload.result?.turns?.length, error: !!payload.error });
          requests.delete(payload.id);
          activePages--;
        }
      } catch {}
    }
  });
  // Observe actual native sockets. A chosen outgoing history frame is dropped by
  // closing that transport, never by injecting a model or fabricated RPC reply.
  await page.send("Page.addScriptToEvaluateOnNewDocument", { source: `(() => {
    const Native = window.WebSocket;
    window.__comicSockets = [];
    window.WebSocket = class extends Native {
      constructor(...args) { super(...args); window.__comicSockets.push(this); }
      send(data) {
        if (window.__comicFailHistory && JSON.parse(data).method === 'thread/turns/list') {
          window.__comicFailHistory = false;
          this.close(4000, 'comic history transport failure');
          return;
        }
        super.send(data);
      }
    };
  })()` });
  if (fixture.requirePrivateFonts) await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/webassets/comic-local/*" }] });
  else await page.send("Network.setBlockedURLs", { urls: ["*/webassets/comic-local/*"] });
  await page.send("Emulation.setDeviceMetricsOverride", { width: 1360, height: 900, deviceScaleFactor: 1, mobile: false });
  await navigateTo(page, fixture.baseURL);
  const base = new URL(fixture.baseURL).origin;
  const route = `${base}/comic/s/${encodeURIComponent(fixture.refs.live)}`;
  // Page.load waits for paused fonts. The actual reader, not that event, is the readiness contract.
  await page.send("Page.navigate", { url: route });
  const evaluateHere = expression => evaluate(page.send, expression);
  const read = (point = null) => evaluateHere(`(${readGeometry.toString()})(${JSON.stringify(point)}, ${JSON.stringify(fixture.refs.live)})`);
  const click = name => evaluateHere(`(() => { const button = [...document.querySelectorAll('button')].find(node => node.textContent.trim() === ${JSON.stringify(name)}); if (!button || button.disabled) return false; button.click(); return true; })()`);
  const select = (name, value) => evaluateHere(`(() => { const label = [...document.querySelectorAll('label')].find(node => node.textContent.startsWith(${JSON.stringify(name)})); const control = label?.querySelector('select'); if (!control) throw Error('missing choice'); control.value = ${JSON.stringify(value)}; control.dispatchEvent(new Event('change', { bubbles: true })); })()`);
  const screenshot = async name => {
    const shot = await page.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(`${fixture.evidenceDir}/${name}.png`, Buffer.from(shot.result.data, "base64"));
  };
  const record = async (name, point, end = false) => {
    const value = await until(name, () => read(point), value => value && value.mounted > 0 && !value.missingArt.length && (end ? value.endGap <= 2 : point ? value.point && Math.abs(value.point.y - point.viewportOffset) <= 2 : true));
    if (value.overflow > 1 || value.pageOverflow > 1 || value.clipped.length || value.mounted > 35 || value.missingArt.length) throw new Error(`${name}: bad geometry ${JSON.stringify(value)}`);
    if (value.path !== new URL(route).pathname || value.route !== "session" || value.ref !== fixture.refs.live) throw new Error(`${name}: wrong authenticated conversation route ${JSON.stringify(value)}`);
    if (name === "away-before-font" && value.parts.filter(part => part.entryId === point.entryId).map(part => part.text).join("") !== fixture.expectedSourceText) throw new Error("complete independent literal source was not rendered exactly once");
    if (point && (value.saved?.entryId !== point.entryId || value.saved?.offset !== point.offset)) throw new Error(`${name}: anchor identity changed ${JSON.stringify(value.saved)}`);
    if (end && name !== "initial-end" && value.saved?.mode !== "end") throw new Error(`${name}: lost explicit end-following ${JSON.stringify(value.saved)}`);
    if (canonicalFailure && point?.text === fixture.expectedSourceText) {
      const failureItem = canonicalFailure.items.find(item => item.eventKind === "error");
      // Narrow widths split the long source into more rows, legitimately moving
      // the later error outside the virtual window. At desktop/plain both fit.
      const sameWindow = value.width === 1360 || value.mode === "plain";
      if ((sameWindow && !value.parts.some(part => part.entryId === `${failureItem.id}:turn-error`)) || value.errorGroups.length) throw new Error(`${name}: canonical item-backed error lost or misgrouped ${JSON.stringify(value)}`);
    }
    if (value.mode === "comic" && name.startsWith("away-width") && !value.panels.some(panel => Math.abs(panel.panel.width - value.probe.panel.width) < 1 && Math.abs(panel.text.width - value.probe.text.width) < 1 && panel.text.font === value.probe.text.font)) throw new Error(`${name}: probe does not match a rendered panel ${JSON.stringify(value)}`);
    for (const part of value.parts) {
      if (point?.entryId === part.entryId && point.text && part.text !== point.text.slice(part.start, part.end)) throw new Error(`${name}: literal source range mismatch ${JSON.stringify(part)}`);
    }
    const preceding = new Map();
    for (const part of value.parts) {
      if (preceding.has(part.entryId) && preceding.get(part.entryId) !== part.start) throw new Error(`${name}: duplicated or omitted interior source range ${JSON.stringify(part)}`);
      preceding.set(part.entryId, part.end);
    }
    metrics.push({ name, ...value });
    writeFileSync(`${fixture.evidenceDir}/measurements.json`, JSON.stringify({ metrics, pageReads, maxPages, socketsClosed, subscriptions }, null, 2));
    return value;
  };
  await until("initial real comic", () => read(), value => value?.parts.some(part => fixture.expectedSourceText.includes(part.text) && part.text.length > 100));
  await record("initial-end", null, true);
  // Scroll a literal source point into view as a reader would. No model/store injection.
  const target = await evaluateHere(`(() => {
    const source = [...document.querySelectorAll('[data-comic-entry]')].find(node => node.querySelector('[data-comic-text]')?.textContent.startsWith('COMIC_READ_SOURCE_3f'));
    if (!source) throw Error('source not mounted');
    const port = document.querySelector('[data-comic-scroll]').firstElementChild;
    const text = source.querySelector('[data-comic-text]').firstChild;
    const offset = Math.min(650, text.length - 1);
    const range = document.createRange(); range.setStart(text, offset); range.setEnd(text, offset + 1);
    const deltaY = range.getBoundingClientRect().top - port.getBoundingClientRect().top - 24;
    port.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY }));
    port.scrollTop += deltaY;
    return source.dataset.comicEntry;
  })()`);
  // The real reader captures a gesture on animation frame. Finish that gesture
  // before establishing the fixed point or triggering an unrelated font reflow.
  await evaluateHere(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`);
  let point = await until("captured source point", () => read(), value => value?.saved?.mode === "away" && value.saved.entryId === target && value.saved.offset > 0);
  point = { ...point.saved, text: fixture.expectedSourceText };
  await record("away-before-font", point);
  const canonicalTurns = incoming.flatMap(frame => {
    const payload = JSON.parse(frame);
    return payload.result?.thread?.turns ?? payload.result?.turns ?? [];
  }).filter(turn => turn.id === failedTurn.turnId);
  writeFileSync(`${fixture.evidenceDir}/failed-turn-canonical.json`, JSON.stringify(canonicalTurns, null, 2));
  canonicalFailure = canonicalTurns.at(-1);
  if (!canonicalFailure || canonicalFailure.status !== "failed" || canonicalFailure.error?.cause?.kind !== "provider" || canonicalFailure.error.cause.provider !== "openai" || canonicalFailure.items.length !== 2 || canonicalFailure.items[0].type !== "userMessage" || canonicalFailure.items[1].type !== "systemMessage" || canonicalFailure.items[1].eventKind !== "error" || canonicalFailure.items[1].status !== "failed") throw new Error(`unexpected canonical provider failure: ${JSON.stringify(canonicalTurns)}`);
  // A pre-content failure is NOT itemless: the runtime records both user input
  // and a positioned TURN_FAILURE. Keep that distinction, never fake an empty turn.
  await record("provider-failure-away", point);
  if (fixture.requirePrivateFonts) {
    await until("both personal font requests", async () => paused.length, count => count === 2);
    holdFonts = false;
    await Promise.all(paused.map(requestId => page.send("Fetch.continueRequest", { requestId })));
    await until("personal fonts loaded", () => read(), value => value?.font === "ready");
    await record("away-after-late-font", point);
  } else await until("explicit failed font state", () => read(), value => value?.font === "unavailable");

  // A real non-text click used to leave the reader permanently awaiting scroll.
  // Hit-test rendered art, then use CDP input, not element.click or DOM events.
  const clickTarget = await evaluateHere(`(() => {
    const port = document.querySelector('[data-comic-scroll]').firstElementChild;
    const bounds = port.getBoundingClientRect();
    for (const image of port.querySelectorAll('article img')) {
      const rect = image.getBoundingClientRect();
      const left = Math.max(rect.left, bounds.left) + 4;
      const right = Math.min(rect.right, bounds.right) - 4;
      const top = Math.max(rect.top, bounds.top) + 4;
      const bottom = Math.min(rect.bottom, bounds.bottom) - 4;
      for (let y = top; y < bottom; y += 16) for (let x = left; x < right; x += 16) {
        const hit = document.elementFromPoint(x, y);
        if (!hit?.closest('article') || hit.closest('[data-comic-text], [data-comic-identity], button, a')) continue;
        window.__comicClickEvents = [];
        window.__comicClickListener = event => window.__comicClickEvents.push({type:event.type, trusted:event.isTrusted, pointerType:event.pointerType, scrollTop:port.scrollTop});
        port.addEventListener('pointerdown', window.__comicClickListener);
        port.addEventListener('pointerup', window.__comicClickListener);
        return {x, y, scrollTop:port.scrollTop};
      }
    }
    throw Error('no visible non-text panel click target');
  })()`);
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x: clickTarget.x, y: clickTarget.y, button: "left", buttons: 1, clickCount: 1 });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: clickTarget.x, y: clickTarget.y, button: "left", buttons: 0, clickCount: 1 });
  await evaluateHere(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`);
  const clicked = await record("native-click-without-scroll", point);
  const clickEvents = await evaluateHere(`(() => {
    const port = document.querySelector('[data-comic-scroll]').firstElementChild;
    port.removeEventListener('pointerdown', window.__comicClickListener);
    port.removeEventListener('pointerup', window.__comicClickListener);
    return window.__comicClickEvents;
  })()`);
  if (clicked.scrollTop !== clickTarget.scrollTop || clickEvents.map(event => event.type).join(',') !== 'pointerdown,pointerup' || !clickEvents.every(event => event.trusted && event.pointerType === 'mouse' && event.scrollTop === clickTarget.scrollTop)) throw new Error(`native click did not complete without scrolling: ${JSON.stringify({clickTarget, clickEvents, after:clicked.scrollTop})}`);
  writeFileSync(`${fixture.evidenceDir}/native-click.json`, JSON.stringify({ clickTarget, clickEvents, after: clicked.scrollTop }, null, 2));

  for (const width of [1360, 760, 600, 390, 320, 1360]) {
    await page.send("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: 1, mobile: false });
    await evaluateHere(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`);
    await record(`away-width-${width}`, point);
    await screenshot(width === 320 ? "phone" : width === 1360 ? "desktop" : `width-${width}`);
    // Root identities are intentionally fixed. Stress their actual inherited
    // label CSS in a detached-from-transcript probe, not a fake session entry.
    const identity = await evaluateHere(`(() => {
      const original = document.querySelector('[data-comic-entry] article');
      const panel = original.cloneNode(false);
      const label = original.querySelector('[data-comic-identity]').cloneNode(false);
      label.textContent = 'FIXTURE_LONG_AGENT_NAME_' .repeat(12) + ' · FIXTURE_LONG_BADGE_' .repeat(10);
      panel.style.cssText = 'position:absolute;visibility:hidden;width:' + original.getBoundingClientRect().width + 'px';
      panel.append(label); document.querySelector('[data-comic-route]').append(panel);
      const result = {width:innerWidth, labelWidth:label.clientWidth, labelHeight:label.clientHeight, lineHeight:parseFloat(getComputedStyle(label).lineHeight), overflow:label.scrollWidth-label.clientWidth, family:getComputedStyle(label).fontFamily};
      panel.remove(); return result;
    })()`);
    if (identity.overflow > 1 || identity.labelHeight <= identity.lineHeight * 2) throw new Error(`long identity CSS did not wrap: ${JSON.stringify(identity)}`);
    writeFileSync(`${fixture.evidenceDir}/identity-${width}.json`, JSON.stringify(identity, null, 2));
  }
  await select("View", "plain");
  await record("away-plain", point);
  if (fixture.requirePrivateFonts) {
    const doc = await page.send("DOM.getDocument");
    const regular = await page.send("DOM.querySelector", { nodeId: doc.result.root.nodeId, selector: '[data-comic-entry] [data-comic-text]' });
    const bold = await page.send("DOM.querySelector", { nodeId: doc.result.root.nodeId, selector: '[data-comic-entry] strong' });
    const fonts = await Promise.all([regular, bold].map(node => page.send("CSS.getPlatformFontsForNode", { nodeId: node.result.nodeId })));
    writeFileSync(`${fixture.evidenceDir}/fonts.json`, JSON.stringify(fonts.map(face => face.result), null, 2));
    if (!fonts.every(face => face.result.fonts.some(font => font.isCustomFont && font.glyphCount > 0 && /Comic/i.test(font.familyName)))) throw new Error(`Comic Sans not actually rendering both faces: ${JSON.stringify(fonts)}`);
  }
  await select("View", "comic");
  await record("away-comic", point);
  const beforeStyle = await read(point);
  await select("Scene", "field");
  await select("Human expression", "happy-wave");
  const afterStyle = await record("presentation-only", point);
  if (JSON.stringify(beforeStyle.parts) !== JSON.stringify(afterStyle.parts) || !afterStyle.scenes.every(scene => scene === "field") || !afterStyle.humanImages.every(src => src.endsWith("anna-happy-wave.png"))) throw new Error("presentation changed source or failed to change art");

  for (let index = 0; index < 3; index++) {
    const before = pageReads.length;
    if (index === 0) await evaluateHere('window.__comicFailHistory = true');
    if (!await click("Older messages")) throw new Error("older demand unavailable");
    if (index === 0) {
      await until("history transport failure visible", () => evaluateHere(`!![...document.querySelectorAll('button')].find(node => node.textContent.trim() === 'Retry')`));
      await record("history-failed-away", point);
      await until("history retry available", () => click("Retry"));
    }
    await until("older page completed", async () => pageReads.length > before && activePages === 0);
    await record(`prepend-${index + 1}`, point);
  }
  // A later-page anchor is reached by real scrolling through the same bounded port.
  let olderPoint;
  await until("later page source reached", async () => {
    const value = await evaluateHere(`(() => {
      const source = [...document.querySelectorAll('[data-comic-entry]')].find(node => node.querySelector('[data-comic-text]')?.textContent.startsWith('COMIC_HISTORY_27'));
      const port = document.querySelector('[data-comic-scroll]').firstElementChild;
      const deltaY = source ? source.querySelector('[data-comic-text]').getBoundingClientRect().top - port.getBoundingClientRect().top - 24 : -port.clientHeight * 3;
      port.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY }));
      port.scrollTop += deltaY;
      return source?.dataset.comicEntry ?? null;
    })()`);
    if (value) olderPoint = value;
    return value;
  });
  const captured = await until("later page anchor saved", () => read(), value => value?.saved?.entryId === olderPoint);
  point = { ...captured.saved, text: fixture.historyTexts[27] };
  await record("later-page-away", point);
  const stream = async (label, anchor) => {
    const before = incoming.length;
    command("background-role", "target");
    command("background-start-turn", `COMIC_STREAM_${label}`);
    await until("real initial stream delta", async () => incoming.slice(before).some(frame => frame.includes("BACKGROUND_BEFORE_EVICTION")));
    await record(`${label}-stream-start`, anchor, !anchor);
    command("background-phase", "after-eviction");
    await until("real subsequent stream delta", async () => incoming.slice(before).some(frame => frame.includes("BACKGROUND_AFTER_EVICTION")));
    await record(`${label}-stream-grow`, anchor, !anchor);
    command("background-phase", "finish");
    await until("real stream completes", () => readFileSync(fixture.requestLog, "utf8").split("\n").some(line => { try { const row = JSON.parse(line); return row.event === "turn-complete" && row.prompt === `COMIC_STREAM_${label}` && row.ok; } catch { return false; } }));
    await record(`${label}-stream-finished`, anchor, !anchor);
    command("background-role", "history");
  };
  await stream("away", point);
  await complete("COMIC_LIVE_APPEND_away");
  await record("away-live-append", point);
  await page.send("Page.reload");
  await record("reload-sequential-history", point);
  const closedBefore = socketsClosed;
  const subscriptionsBefore = subscriptions;
  await page.send("Network.emulateNetworkConditions", { offline: true, latency: 0, downloadThroughput: 0, uploadThroughput: 0 });
  const closed = await evaluateHere(`(() => { const sockets = window.__comicSockets.filter(socket => socket.readyState === WebSocket.OPEN && new URL(socket.url).pathname === '/rpc'); sockets.forEach(socket => socket.close(4000, 'comic reconnect boundary')); return sockets.length; })()`);
  if (closed !== 1) throw new Error(`expected one shared hub socket, found ${closed}`);
  await complete("COMIC_RECONNECT_GAP");
  await page.send("Network.emulateNetworkConditions", { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await until("real socket resubscription", async () => socketsClosed > closedBefore && subscriptions > subscriptionsBefore);
  await record("reconnect-away", point);
  await click("Jump to latest");
  await record("jump-latest", null, true);
  await stream("end", null);
  await complete("COMIC_LIVE_APPEND_end");
  await record("end-live-append", null, true);
  for (const width of [320, 760, 1360]) {
    await page.send("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: 1, mobile: false });
    await record(`end-width-${width}`, null, true);
  }
  if (maxPages !== 1 || pageReads.length < 6) throw new Error(`shared sequential paging not exercised: ${JSON.stringify({maxPages, pageReads})}`);
  console.log(`Comic responsive reader: ${metrics.length} measured scenarios, ${pageReads.length} history pages, max concurrent ${maxPages}, personal fonts ${fixture.requirePrivateFonts}; ${fixture.evidenceDir}`);
}

function readGeometry(point, ref) {
  const port = document.querySelector('[data-comic-scroll]')?.firstElementChild;
  if (!port) return null;
  const sources = [...port.querySelectorAll('[data-comic-entry]')];
  const parts = sources.map(node => ({ entryId: node.dataset.comicEntry, start: +node.dataset.comicStart, end: +node.dataset.comicEnd, text: node.querySelector('[data-comic-text]')?.textContent ?? '' }));
  const source = point && sources.find(node => node.dataset.comicEntry === point.entryId && +node.dataset.comicStart <= point.offset && +node.dataset.comicEnd > point.offset);
  let selected = null;
  if (source) {
    const text = source.querySelector('[data-comic-text]').firstChild;
    const range = document.createRange(); range.setStart(text, point.offset - +source.dataset.comicStart); range.setEnd(text, point.offset - +source.dataset.comicStart + 1);
    selected = { entryId: point.entryId, offset: point.offset, y: range.getBoundingClientRect().top - port.getBoundingClientRect().top, start: +source.dataset.comicStart, end: +source.dataset.comicEnd };
  }
  const probe = document.querySelector('[data-comic-probe]');
  const probeText = probe?.querySelector('p');
  const panels = [...port.querySelectorAll('article')];
  const style = node => node ? { width: node.getBoundingClientRect().width, font: getComputedStyle(node).font, family: getComputedStyle(node).fontFamily, weight: getComputedStyle(node).fontWeight, size: getComputedStyle(node).fontSize, lineHeight: getComputedStyle(node).lineHeight } : null;
  return {
    path: location.pathname, route: document.querySelector('main')?.dataset.comicRoute, ref: document.querySelector('main')?.dataset.sessionRef,
    width: innerWidth, portWidth: port.clientWidth, mode: document.querySelector('[data-comic-mode]')?.dataset.comicMode, font: document.querySelector('[data-comic-font]')?.dataset.comicFont,
    saved: JSON.parse(sessionStorage.getItem('evener:comic:position:' + ref) ?? 'null')?.position,
    point: selected, parts, mounted: sources.length, scrollTop: port.scrollTop, scrollHeight: port.scrollHeight, clientHeight: port.clientHeight, endGap: port.scrollHeight - port.clientHeight - port.scrollTop,
    errorGroups: [...port.querySelectorAll('section[aria-label="Turn errors"]')].map(group => [...group.querySelectorAll('[data-comic-entry]')].map(node => node.dataset.comicEntry)),
    overflow: Math.max(document.documentElement.scrollWidth - innerWidth, port.scrollWidth - port.clientWidth),
    pageOverflow: document.documentElement.scrollHeight - innerHeight,
    clipped: [...port.querySelectorAll('[data-comic-text], [data-comic-identity]')].filter(node => node.scrollWidth > node.clientWidth + 1 || node.scrollHeight > node.clientHeight + 1).map(node => node.textContent),
    missingArt: [...port.querySelectorAll('img')].filter(node => !node.complete || !node.naturalWidth).map(node => node.src),
    scenes: [...port.querySelectorAll('[data-comic-scene]')].map(node => node.dataset.comicScene),
    humanImages: [...port.querySelectorAll('[data-comic-speaker="human"] img')].map(node => node.src),
    probe: { panel: style(probe), text: style(probeText), inheritedFont: getComputedStyle(probe ?? port).getPropertyValue('--comic-font') },
    panels: panels.map(node => ({ panel: style(node), text: style(node.querySelector('[data-comic-text]')) })),
  };
}
