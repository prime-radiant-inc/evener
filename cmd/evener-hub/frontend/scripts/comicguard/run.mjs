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

async function reloadPage(page) {
  const before = await evaluate(page.send, 'performance.timeOrigin');
  await page.send('Page.reload');
  await until('new document after reload', async () => {
    try { return await evaluate(page.send, 'performance.timeOrigin'); }
    catch { return null; } // The old execution context is destroyed during navigation.
  }, value => typeof value === 'number' && value !== before);
}

function repliesSince(trace, start, accept) {
  const requests = new Map();
  const replies = [];
  for (const row of trace.slice(start)) {
    if (!row.frame) continue;
    const payload = JSON.parse(row.frame);
    const key = `${row.connection}:${payload.id}`;
    if (row.event === 'Network.webSocketFrameSent' && accept(payload)) requests.set(key, payload);
    if (row.event === 'Network.webSocketFrameReceived' && requests.has(key)) {
      replies.push({connection:row.connection,request:requests.get(key),response:payload});
    }
  }
  return replies;
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
  const outgoing = [];
  const wireTrace = [];
  let canonicalFailure;
  const paused = [];
  let holdFonts = fixture.requirePrivateFonts;
  await page.send("Network.enable");
  await page.send("DOM.enable");
  await page.send("CSS.enable");
  page.ws.addEventListener("message", event => {
    const message = JSON.parse(event.data);
    if (["Network.webSocketFrameSent", "Network.webSocketFrameReceived", "Network.webSocketClosed"].includes(message.method)) {
      wireTrace.push({ at: Date.now(), event: message.method, connection: message.params.requestId, frame: message.params.response?.payloadData });
    }
    if (message.method === "Network.webSocketClosed") socketsClosed++;
    if (message.method === "Fetch.requestPaused") {
      if (holdFonts) paused.push(message.params.requestId);
      else void page.send("Fetch.continueRequest", { requestId: message.params.requestId });
    }
    if (message.method === "Network.webSocketFrameSent") {
      try {
        const payload = JSON.parse(message.params.response.payloadData);
        outgoing.push(payload);
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
    wireTrace.push({ at: Date.now(), checkpoint: name, pageCount: pageReads.length });
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
  const beforeReload = wireTrace.length;
  await reloadPage(page);
  const freshRoot = await until('fresh root history after reload', () => repliesSince(wireTrace, beforeReload,
    request => request.method === 'thread/read' && request.params?.ref === fixture.refs.live && request.params.includeTurns === true)
    .find(reply => reply.response.result?.thread?.turns?.length > 0));
  writeFileSync(`${fixture.evidenceDir}/reload-history-reply.json`, JSON.stringify(freshRoot, null, 2));
  await evaluateHere(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`);
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
  writeFileSync(`${fixture.evidenceDir}/root-reads.json`, JSON.stringify({ reads: outgoing.filter(frame => frame.method === 'thread/read'), maxPages, pageReads, wireTrace }, null, 2));
  if (maxPages !== 1 || pageReads.length < 6) throw new Error(`shared sequential paging not exercised: ${JSON.stringify({maxPages, pageReads})}`);
  try {
    await childJourney(page, fixture, { command, complete, incoming, outgoing, screenshot });
    await rosterJourney(page, fixture, { command, complete, incoming, outgoing, screenshot, wireTrace });
    await remoteJourney(page, fixture, { incoming, outgoing, screenshot });
    await authorshipJourney(page, fixture, { incoming, outgoing, screenshot });
  }
  catch (error) {
    await screenshot('child-failure');
    writeFileSync(`${fixture.evidenceDir}/failure-wire.json`, JSON.stringify(wireTrace, null, 2));
    writeFileSync(`${fixture.evidenceDir}/child-failure.json`, JSON.stringify({error:String(error), dom:await evaluateHere('document.querySelector("main")?.outerHTML'), requests:outgoing.filter(frame => frame.method === 'thread/read'), frames:incoming.map(frame => JSON.parse(frame)).filter(frame => frame.method || frame.result?.context || frame.result?.thread || frame.result?.turns)}, null, 2));
    throw error;
  }
  console.log(`Comic responsive reader: ${metrics.length} measured scenarios, ${pageReads.length} history pages, max concurrent ${maxPages}, personal fonts ${fixture.requirePrivateFonts}; ${fixture.evidenceDir}`);
}

function readGeometry(point, ref) {
  const port = document.querySelector('[data-comic-scroll]')?.firstElementChild;
  if (!port) return null;
  const sources = [...port.querySelectorAll('[data-comic-entry]')];
  const parts = sources.map(node => ({ sessionRef: node.dataset.comicSession, path: JSON.parse(node.dataset.comicPath ?? '[]'), entryId: node.dataset.comicEntry, start: +node.dataset.comicStart, end: +node.dataset.comicEnd, text: node.querySelector('[data-comic-text]')?.textContent ?? '' }));
  const source = point && sources.find(node => node.dataset.comicSession === (point.sessionRef ?? ref) && node.dataset.comicPath === JSON.stringify(point.path ?? []) && node.dataset.comicEntry === point.entryId && +node.dataset.comicStart <= point.offset && +node.dataset.comicEnd > point.offset);
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

async function remoteJourney(page, fixture, { incoming, outgoing, screenshot }) {
  const evaluateHere = expression => evaluate(page.send, expression);
  const firstRequest = outgoing.length;
  const remoteURL = new URL(fixture.baseURL);
  remoteURL.pathname = '/comic/s/' + encodeURIComponent(fixture.refs.remote);
  remoteURL.search = '';
  await navigateTo(page, remoteURL.href);
  await until('real remote parent activity', () => incoming.some(frame => {
    const result = JSON.parse(frame).result;
    return result?.context?.ref === fixture.refs.remote && Array.isArray(result.delegates) && result.delegates.length === 50;
  }));
  const header = await until('remote-qualified real heading', async () => evaluateHere(`(() => {
    const port=document.querySelector('[data-comic-scroll]')?.firstElementChild;if(!port)return null;
    const header=port.querySelector('[data-comic-delegate]');
    if(!header){port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:400}));port.scrollTop+=400;return null;}
    header.querySelector('button').click();
    return {key:header.dataset.comicDelegate,ref:header.dataset.comicChildRef,text:header.textContent,portrait:header.querySelector('img').getAttribute('src')};
  })()`));
  if (!header.ref.startsWith('comic-remote:') || !header.key.includes(fixture.refs.remote)) throw Error('unqualified real remote heading: '+JSON.stringify(header));
  const source = async () => evaluateHere(`(() => {
    const panel=[...document.querySelectorAll('[data-comic-entry]')].find(panel=>panel.dataset.comicSession===${JSON.stringify(header.ref)} && panel.textContent.includes('COMIC_ROSTER_LEAF literal answer'));
    if(panel) return {ref:panel.dataset.comicSession,identity:panel.querySelector('[data-comic-identity]').textContent};
    const port=document.querySelector('[data-comic-scroll]')?.firstElementChild;if(port){port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:300}));port.scrollTop+=300;}return null;
  })()`);
  const inline = await until('real remote child source', source);
  const local = JSON.parse(readFileSync(`${fixture.evidenceDir}/activity-roster.json`,'utf8')).observed.find(row=>row.ref.split(':').at(-1)===header.ref.split(':').at(-1));
  if (!local || local.key===header.key || !header.text.includes(fixture.refs.remote)) throw Error('remote identity borrowed local attribution');
  await screenshot('remote-inline');
  await until('remote full-page action', async () => evaluateHere(`(() => {
    const node=[...document.querySelectorAll('[data-comic-delegate]')].find(node=>node.dataset.comicChildRef===${JSON.stringify(header.ref)});
    if(node){[...node.querySelectorAll('button')].find(button=>button.textContent==='Open conversation').click();return true;}
    const port=document.querySelector('[data-comic-scroll]').firstElementChild;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:-300}));port.scrollTop-=300;return false;
  })()`));
  await until('real remote direct route', () => evaluateHere('document.querySelector("main")?.dataset.sessionRef'), value=>value===header.ref);
  const direct = await until('remote direct persona', source, value=>value?.identity===inline.identity);
  const requests=outgoing.slice(firstRequest);
  if (requests.some(frame=>frame.method==='turn/start'||frame.method==='thread/resume'||frame.method==='thread/read'&&frame.params.ref.startsWith('local:'))) throw Error('remote read mutated or borrowed a local session');
  writeFileSync(`${fixture.evidenceDir}/remote-identity.json`,JSON.stringify({header,local,inline,direct,requests},null,2));
}

async function rosterJourney(page, fixture, { command, complete, incoming, outgoing, screenshot, wireTrace }) {
  const evaluateHere = expression => evaluate(page.send, expression);
  command('background-role', 'comic-roster');
  await complete('COMIC_ROSTER_ROOT');
  await evaluateHere(`([...document.querySelectorAll('button')].find(button=>button.textContent==='Jump to latest')).click()`);
  const firstRequest = outgoing.length;
  const firstFrame = incoming.length;
  const firstTrace = wireTrace.length;
  const continuations = [];
  await reloadPage(page);
  const pages = () => incoming.slice(firstFrame).map(frame => JSON.parse(frame).result).filter(result => Array.isArray(result?.delegates) && result.page && result.context?.sessionId === fixture.refs.live.split(':').at(-1));
  await until('real first activity page', () => pages(), rows => rows.some(row => row.delegates.length === 50 && row.page.nextCursor));
  for (let index = 0; index < 2; index++) {
    const requestStart = outgoing.length;
    await until('activity continuation button', async () => {
      await evaluateHere(`(() => { const port=document.querySelector('[data-comic-scroll]')?.firstElementChild; if(port) {port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:port.scrollHeight-port.scrollTop}));port.scrollTop=port.scrollHeight;} })()`);
      return evaluateHere(`(() => { const button=[...document.querySelectorAll('button')].find(button=>button.textContent==='More delegates'&&!button.disabled); if(!button)return false; button.click(); return true; })()`);
    });
    const request = await until('real continued activity page', () => outgoing.slice(requestStart).find(frame => frame.method === 'evener/thread/delegates/list' && frame.params?.ref === fixture.refs.live && frame.params.cursor));
    continuations.push(await until('activity page response', () => repliesSince(wireTrace, firstTrace,
      sent => sent.id === request.id && sent.method === request.method && sent.params?.ref === fixture.refs.live && sent.params.cursor === request.params.cursor)
      .find(reply => Array.isArray(reply.response.result?.delegates))));
  }
  writeFileSync(`${fixture.evidenceDir}/activity-continuation-replies.json`, JSON.stringify(continuations, null, 2));
  const rows = new Map(pages().flatMap(page => page.delegates).map(row => [row.delegateId, row]));
  if (rows.size !== 102 || !pages().some(page => page.page.complete)) throw Error('real activity pages incomplete: ' + JSON.stringify({count:rows.size,pages:pages().map(page=>({count:page.delegates.length,page:page.page}))}));
  const observed = new Map();
  let maxMountedHeaders = 0;
  await evaluateHere(`(() => {const port=document.querySelector('[data-comic-scroll]').firstElementChild;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:-port.scrollTop}));port.scrollTop=0;})()`);
  for (let index = 0; index < 160; index++) {
    await evaluateHere('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))');
    const step = await evaluateHere(`(() => { const port=document.querySelector('[data-comic-scroll]').firstElementChild; const headers=[...document.querySelectorAll('[data-comic-delegate]')].map(header=>({key:header.dataset.comicDelegate,ref:header.dataset.comicChildRef,name:header.querySelector('strong').textContent,text:header.textContent,portrait:header.querySelector('img').getAttribute('src')})); const end=port.scrollTop+port.clientHeight>=port.scrollHeight-2; const deltaY=Math.max(200,port.clientHeight-100);port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));port.scrollTop+=deltaY; return {headers,end}; })()`);
    for (const header of step.headers) observed.set(header.key, header);
    maxMountedHeaders = Math.max(maxMountedHeaders, step.headers.length);
    if (step.end) break;
  }
  const named = [...observed.values()].filter(header => header.name.includes('comic-same-name'));
  writeFileSync(`${fixture.evidenceDir}/activity-roster.json`, JSON.stringify({pages:pages(),observed:[...observed.values()],named,maxMountedHeaders,requests:outgoing.slice(firstRequest)},null,2));
  if (observed.size !== 102 || maxMountedHeaders > 35) throw Error('real roster virtual bounds/coverage: '+JSON.stringify({seen:observed.size,maxMountedHeaders}));
  if (named.length !== 2 || named[0].key === named[1].key || named[0].ref === named[1].ref || named[0].text === named[1].text) throw Error('same-named real delegates not distinctly attributed: ' + JSON.stringify(named));
  const reads = outgoing.slice(firstRequest).filter(frame => frame.method === 'thread/read');
  const leafRefs = new Set([...rows.values()].map(row => row.childRef));
  if (reads.some(frame => leafRefs.has(frame.params?.ref))) throw Error('collapsed named child acquired a transcript');
  await screenshot('activity-roster');
  writeFileSync(`${fixture.evidenceDir}/activity-roster.json`, JSON.stringify({pages:pages(),observed:[...observed.values()],named,maxMountedHeaders,requests:outgoing.slice(firstRequest)},null,2));
  const directChild = named[0];
  const ownedRow = [...rows.values()].find(row=>row.childRef===directChild.ref);
  await until('same-named explicit child route',async()=>evaluateHere(`(() => {
    const header=[...document.querySelectorAll('[data-comic-delegate]')].find(node=>node.dataset.comicChildRef===${JSON.stringify(directChild.ref)});
    if(header){[...header.querySelectorAll('button')].find(button=>button.textContent==='Open conversation').click();return true;}
    const port=document.querySelector('[data-comic-scroll]').firstElementChild;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:-300}));port.scrollTop-=300;return false;
  })()`));
  const readNamedIdentity = () => evaluateHere(`(() => {const panel=[...document.querySelectorAll('[data-comic-entry]')].find(node=>node.dataset.comicSession===${JSON.stringify(directChild.ref)}&&node.textContent.includes('COMIC_ROSTER_LEAF literal answer'));return panel?.querySelector('[data-comic-identity]')?.textContent;})()`);
  const directIdentity = await until('named direct child identity', readNamedIdentity, value=>value===`comic-same-name · ${ownedRow.delegateId}`);
  await reloadPage(page);
  const refreshedIdentity = await until('named direct child refresh identity', readNamedIdentity, value=>value===directIdentity);
  writeFileSync(`${fixture.evidenceDir}/named-direct.json`, JSON.stringify({directChild,ownedRow,directIdentity,refreshedIdentity},null,2));
}

// Additive real-daemon journey. DOM input only, never store/model injection.
async function childJourney(page, fixture, { command, complete, incoming, outgoing, screenshot }) {
  const evaluateHere = expression => evaluate(page.send, expression);
  const firstRequest = outgoing.length;
  command("background-role", "comic-subchats");
  command("background-start-turn", "COMIC_SUBCHAT_ROOT");
  await until('real child history production', () => readFileSync(fixture.requestLog, 'utf8').split('\n').some(line => {
    try { return JSON.parse(line).event === 'comic-child-produced'; } catch { return false; }
  }));
  const metrics = [];
  const reveal = async (selector, text = "", direction = -1) => until("reveal real child " + text, async () => {
    const result = await evaluateHere(`(() => {
      const port = document.querySelector('[data-comic-scroll]')?.firstElementChild;
      if (!port) return null;
      const node = [...port.querySelectorAll(${JSON.stringify(selector)})].find(node => node.textContent.includes(${JSON.stringify(text)}));
      const deltaY = node ? node.getBoundingClientRect().top - port.getBoundingClientRect().top - 24 : ${direction} * port.clientHeight * .7;
      port.dispatchEvent(new WheelEvent('wheel', {bubbles:true, deltaY})); port.scrollTop += deltaY;
      return {found:node ? { ref:node.dataset.comicChildRef, key:node.dataset.comicDelegate } : null, boundary:${direction} < 0 ? port.scrollTop <= 1 : port.scrollTop + port.clientHeight >= port.scrollHeight - 1};
    })()`);
    await evaluateHere('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))');
    if (result?.boundary) direction *= -1;
    return result?.found;
  });
  const header = await reveal('[data-comic-delegate]');
  if (!header.ref || !header.key.includes(fixture.refs.live)) throw Error('delegate has no qualified runtime ownership ' + JSON.stringify(header));
  const operate = async (ref, label, keyboard = false) => {
    const found = await evaluateHere(`(() => {
    const header = [...document.querySelectorAll('[data-comic-delegate]')].find(node => node.dataset.comicChildRef === ${JSON.stringify(ref)});
    const button = [...(header?.querySelectorAll('button') ?? [])].find(node => node.textContent.trim() === ${JSON.stringify(label)});
    if (!button) return false; button.focus({preventScroll:true}); if (!${keyboard}) button.click(); return true;
    })()`);
    if (found && keyboard) {
      await page.send('Input.dispatchKeyEvent', {type:'keyDown', key:'Enter', code:'Enter', windowsVirtualKeyCode:13, text:'\r', unmodifiedText:'\r'});
      await page.send('Input.dispatchKeyEvent', {type:'keyUp', key:'Enter', code:'Enter', windowsVirtualKeyCode:13});
    }
    return found;
  };
  if (!await operate(header.ref, 'Expand conversation', true)) throw Error('missing real expansion');
  await until('real child activity', async () => incoming.some(frame => {
    const result = JSON.parse(frame).result;
    return result?.context?.ref === header.ref && Array.isArray(result.delegates) && result.delegates.length > 0;
  }));
  await reveal('[data-comic-child-ref=' + JSON.stringify(header.ref) + ']', 'Meanwhile');
  // Seek the child's own heading, independently of its parent's mounted header.
  let grandchild;
  await until('real nested heading', async () => {
    const value = await evaluateHere(`(() => {
      const port = document.querySelector('[data-comic-scroll]').firstElementChild;
      const node = [...port.querySelectorAll('[data-comic-delegate]')].find(node => node.dataset.comicChildRef !== ${JSON.stringify(header.ref)});
      if (node) return {ref:node.dataset.comicChildRef, key:node.dataset.comicDelegate, unplaced:[...port.querySelectorAll('[data-comic-flow="unplaced-group"]')].some(group => group.dataset.comicSession === ${JSON.stringify(header.ref)})};
      port.dispatchEvent(new WheelEvent('wheel', {bubbles:true, deltaY:400})); port.scrollTop += 400; return null;
    })()`);
    if (value) grandchild = value;
    return value;
  });
  if (!grandchild.key.includes(header.ref) || grandchild.ref === header.ref) throw Error('nested identity is not independently qualified');
  if (!grandchild.unplaced) throw Error('earlier causal event was not outside loaded child history');
  if (!await operate(grandchild.ref, 'Expand conversation')) throw Error('missing grandchild expansion');
  await reveal('[data-comic-entry]', 'COMIC_THOUGHT_GRANDCHILD', 1);
  let saved = await until('nested source anchor', () => evaluateHere(`JSON.parse(sessionStorage.getItem('evener:comic:position:' + ${JSON.stringify(fixture.refs.live)}) ?? 'null')?.position`), value => value?.sessionRef === grandchild.ref && value.path.length === 2 && value.mode === 'away');
  const measure = async name => {
    const geometry = await until(name, () => evaluateHere(`(${readGeometry.toString()})(${JSON.stringify(saved)}, ${JSON.stringify(fixture.refs.live)})`), value => value?.point && Math.abs(value.point.y - saved.viewportOffset) <= 2);
    const nestedGeometry = await evaluateHere(`(() => {
      const port = document.querySelector('[data-comic-scroll]').firstElementChild;
      return {scrollports:document.querySelectorAll('[data-comic-scroll]').length, editors:document.querySelectorAll('textarea,[contenteditable=true]').length,
        headers:[...port.querySelectorAll('[data-comic-delegate]')].map(node => ({ref:node.dataset.comicChildRef, width:node.getBoundingClientRect().width, text:node.textContent})),
        thoughts:[...port.querySelectorAll('[data-comic-balloon=thought]')].map(node => { const text = node.querySelector('[data-comic-text]'); const box = node.getBoundingClientRect(); const inside = text.getBoundingClientRect(); return {text:node.textContent, width:node.clientWidth, height:node.clientHeight, overflow:Math.max(text.scrollWidth-text.clientWidth,text.scrollHeight-text.clientHeight), contained:inside.left >= box.left && inside.right <= box.right && inside.top >= box.top && inside.bottom <= box.bottom}; })};
    })()`);
    if (geometry.overflow > 1 || geometry.pageOverflow > 1 || geometry.clipped.length || geometry.mounted > 35 || nestedGeometry.scrollports !== 1 || nestedGeometry.editors !== 0 || nestedGeometry.thoughts.some(thought => thought.overflow > 1 || !thought.contained)) throw Error(name + ': bad child geometry ' + JSON.stringify({geometry,nestedGeometry}));
    if (geometry.width <= 760 && nestedGeometry.headers.some(item => item.width < geometry.portWidth - 40)) throw Error('compounded mobile indentation');
    metrics.push({name,geometry,nestedGeometry});
    writeFileSync(`${fixture.evidenceDir}/children.json`, JSON.stringify({header,grandchild,saved,metrics}, null, 2));
  };
  for (const width of [1360,760,600,390,320,1360]) {
    await page.send('Emulation.setDeviceMetricsOverride', {width,height:900,deviceScaleFactor:1,mobile:false});
    await measure('nested-width-' + width); await screenshot('nested-' + width);
  }
  const beforeGrowth = incoming.length;
  command('background-phase', 'comic-child-grow');
  await until('actual child stream growth', () => incoming.slice(beforeGrowth).some(frame => frame.includes('COMIC_ANSWER_CHILD')));
  await measure('nested-child-stream-grow');
  command('background-phase', 'comic-child-finish');
  await until('real child producer settled', () => readFileSync(fixture.requestLog, 'utf8').split('\n').some(line => {
    try { const row = JSON.parse(line); return row.event === 'turn-complete' && row.prompt === 'COMIC_SUBCHAT_ROOT' && row.ok; } catch { return false; }
  }));
  await measure('nested-child-stream-finished');
  await reloadPage(page);
  await measure('nested-reload');
  // Recover only the saved branch, then expose the child's explicit full-page route.
  if (!await operate(grandchild.ref, 'Open conversation')) throw Error('missing child route');
  await until('full child page', () => evaluateHere('document.querySelector("main")?.dataset.sessionRef'), value => value === grandchild.ref);
  await evaluateHere('history.back()');
  await measure('child-back');
  for (const mode of ['plain', 'comic']) {
    await evaluateHere(`(() => { const control = document.querySelector('select'); control.value = ${JSON.stringify(mode)}; control.dispatchEvent(new Event('change', {bubbles:true})); })()`);
    await measure('nested-' + mode);
  }
  if (!await operate(grandchild.ref, 'Collapse conversation', true)) throw Error('missing keyboard collapse');
  await until('collapsed focus remains on strip', () => evaluateHere(`document.activeElement?.closest('[data-comic-child-ref]')?.dataset.comicChildRef === ${JSON.stringify(grandchild.ref)} && document.activeElement?.getAttribute('aria-expanded') === 'false'`));
  if (!await operate(grandchild.ref, 'Expand conversation', true)) throw Error('missing keyboard reopen');
  await until('keyboard reopened strip', () => evaluateHere(`document.activeElement?.closest('[data-comic-child-ref]')?.dataset.comicChildRef === ${JSON.stringify(grandchild.ref)} && document.activeElement?.getAttribute('aria-expanded') === 'true'`));
  writeFileSync(`${fixture.evidenceDir}/child-keyboard.json`, JSON.stringify(await evaluateHere(`({focusedRef:document.activeElement.closest('[data-comic-child-ref]').dataset.comicChildRef, expanded:document.activeElement.getAttribute('aria-expanded'), saved:JSON.parse(sessionStorage.getItem('evener:comic:position:' + ${JSON.stringify(fixture.refs.live)})).position})`), null, 2));
  // Native Enter may scroll its focused heading into view. Reestablish an
  // inside-child source before testing collapse's retained child anchor.
  await reveal('[data-comic-entry]', 'COMIC_THOUGHT_GRANDCHILD', 1);
  saved = await until('inside child before collapse', () => evaluateHere(`JSON.parse(sessionStorage.getItem('evener:comic:position:' + ${JSON.stringify(fixture.refs.live)})).position`), value => value?.sessionRef === grandchild.ref && value.path.length === 2 && value.mode === 'away');
  if (!await operate(grandchild.ref, 'Collapse conversation')) throw Error('missing anchored collapse');
  await until('anchored collapse settled', () => evaluateHere(`document.activeElement?.getAttribute('aria-expanded') === 'false'`));
  if (!await operate(grandchild.ref, 'Expand conversation')) throw Error('missing anchored reopen');
  await measure('nested-reopen');
  const historySelector = '[data-comic-flow="history"][data-comic-session=' + JSON.stringify(header.ref) + ']';
  const beforePaging = outgoing.length;
  for (let index = 0; index < 12; index++) {
    await reveal(historySelector);
    const result = await evaluateHere(`(() => {
      const row = document.querySelector(${JSON.stringify(historySelector)});
      const button = [...(row?.querySelectorAll('button') ?? [])].find(node => node.textContent === 'Older messages');
      if (!button) return 'exhausted'; if (button.disabled) return 'pending';
      button.focus({preventScroll:true}); button.click(); return 'requested';
    })()`);
    if (result === 'exhausted') break;
    await until('child page settled', () => evaluateHere(`!document.querySelector(${JSON.stringify(historySelector)})?.querySelector('button')?.disabled`));
  }
  const childPages = outgoing.slice(beforePaging).filter(frame => frame.method === 'thread/turns/list' && frame.params.ref === header.ref);
  if (childPages.length < 3) throw Error('three child history pages were not read: ' + childPages.length);
  await reveal('[data-comic-child-ref=' + JSON.stringify(grandchild.ref) + ']', '', 1);
  const relocated = await evaluateHere(`(() => { const node = document.querySelector('[data-comic-child-ref=' + ${JSON.stringify(JSON.stringify(grandchild.ref))} + ']'); return {key:node?.dataset.comicDelegate, expanded:node?.querySelector('[aria-expanded]')?.getAttribute('aria-expanded')}; })()`);
  if (relocated.key !== grandchild.key || relocated.expanded !== 'true') throw Error('causal placement replaced expanded nested identity');
  await reveal('[data-comic-entry][data-comic-session=' + JSON.stringify(header.ref) + ']', 'COMIC_CHILD_SOURCE_05', 1);
  await evaluateHere(`(() => { const port = document.querySelector('[data-comic-scroll]').firstElementChild; const source = [...port.querySelectorAll('[data-comic-text]')].find(node => node.textContent.startsWith('COMIC_CHILD_SOURCE_05')); const deltaY = source.getBoundingClientRect().top - port.getBoundingClientRect().top + 80; port.dispatchEvent(new WheelEvent('wheel', {bubbles:true, deltaY})); port.scrollTop += deltaY; })()`);
  saved = await until('later child source anchor', () => evaluateHere(`JSON.parse(sessionStorage.getItem('evener:comic:position:' + ${JSON.stringify(fixture.refs.live)}) ?? 'null')?.position`), value => value?.sessionRef === header.ref && value.path.length === 1 && value.mode === 'away' && value.offset > 0);
  await measure('later-child-page');
  await evaluateHere("window.__comicSockets.forEach(socket => socket.close(4000, 'comic child recovery'))");
  await measure('later-child-disconnected');
  await until('child socket reconnected', () => evaluateHere('window.__comicSockets.some(socket => socket.readyState === WebSocket.OPEN)'));
  await measure('later-child-reconnected');
  const beforeReload = outgoing.length;
  await reloadPage(page);
  await measure('later-child-reload');
  if (outgoing.slice(beforeReload).some(frame => frame.method === 'thread/read' && frame.params.ref === grandchild.ref && frame.params.includeTurns !== false)) throw Error('restoration fetched an unopened descendant');
  await page.send('Page.navigate', {url:new URL('/comic/s/' + encodeURIComponent(header.ref), fixture.baseURL).href});
  await until('later child full page', () => evaluateHere('document.querySelector("main")?.dataset.sessionRef'), value => value === header.ref);
  await evaluateHere('history.back()');
  await measure('later-child-back');
  await page.send('Page.navigate', {url:new URL('/comic/s/' + encodeURIComponent(grandchild.ref), fixture.baseURL).href});
  await until('named-parent grandchild page', () => evaluateHere('document.querySelector("main")?.dataset.sessionRef'), value => value === grandchild.ref);
  const parentIdentity = await until('named parent instruction identity', () => evaluateHere(`(() => {
    const panel=[...document.querySelectorAll('[data-comic-entry]')].find(node=>node.dataset.comicSession===${JSON.stringify(grandchild.ref)}&&node.textContent.includes('COMIC_PARENT_INSTRUCTION literal parent steering'));
    if(panel)return panel.querySelector('[data-comic-identity]')?.textContent;
    const port=document.querySelector('[data-comic-scroll]')?.firstElementChild;if(port){port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:-300}));port.scrollTop-=300;}return null;
  })()`), value=>value===`comic-named-parent · ${JSON.parse(header.key)[1]}`);
  writeFileSync(`${fixture.evidenceDir}/named-parent.json`, JSON.stringify({header,grandchild,parentIdentity},null,2));
  await evaluateHere('history.back()');
  await measure('named-parent-back');
  const mutations = outgoing.slice(firstRequest).filter(frame => frame.method === 'turn/start' || frame.method === 'thread/resume');
  if (mutations.length) throw Error('child reads mutated a session: ' + JSON.stringify(mutations));
  writeFileSync(`${fixture.evidenceDir}/child-reads.json`, JSON.stringify({childPages, requests:outgoing.slice(firstRequest), relocated}, null, 2));
  const actualTurns = incoming.flatMap(frame => { const data = JSON.parse(frame); return data.result?.thread?.turns ?? data.result?.turns ?? []; });
  const wire = JSON.stringify(actualTurns);
  if (!wire.includes('COMIC_THOUGHT_GRANDCHILD') || !wire.includes('COMIC_ANSWER_CHILD')) throw Error('missing canonical real child transcript evidence');
  writeFileSync(`${fixture.evidenceDir}/child-canonical.json`, JSON.stringify(actualTurns.filter(turn => JSON.stringify(turn).includes('COMIC_')), null, 2));
  command('background-role', 'history');
}

// Independent original-author oracle: fresh real root, two explicit fork_context
// calls, no fabricated transcript/store data, and the same literal leaf sources
// after paging, reconnect, reload, inline expansion and direct navigation.
async function authorshipJourney(page, fixture, { incoming, outgoing, screenshot }) {
  const command = (command, phase = '') => appendFileSync(fixture.authorshipControl, `${JSON.stringify({command,phase})}\n`);
  const evaluateHere = expression => evaluate(page.send, expression);
  const firstRequest = outgoing.length;
  const firstFrame = incoming.length;
  const human = 'COMIC_AUTH_HUMAN literal inherited human';
  command('background-mode');
  command('background-role', 'comic-authorship');
  command('background-start-turn', human);
  await until('nested fork producer complete', () => readFileSync(fixture.authorshipLog, 'utf8').split('\n').some(line => {
    try { const row = JSON.parse(line); return row.event === 'turn-complete' && row.prompt === human && row.ok; } catch { return false; }
  }));
  const navigate = async ref => {
    await page.send('Page.navigate', {url:new URL('/comic/s/' + encodeURIComponent(ref), fixture.baseURL).href});
    await until('authored conversation route', () => evaluateHere('document.querySelector("main")?.dataset.sessionRef'), value=>value===ref);
  };
  await navigate(fixture.authorshipRef);
  const expand = async name => {
    let direction=1;
    const header = await until('real authored child ' + name, async () => {
      const result=await evaluateHere(`(() => {
      const port=document.querySelector('[data-comic-scroll]')?.firstElementChild;
      const header=[...document.querySelectorAll('[data-comic-delegate]')].find(node=>node.textContent.includes(${JSON.stringify(name)}));
      if(!header){if(!port)return null;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:${direction}*500}));port.scrollTop+=${direction}*500;return {boundary:${direction}<0?port.scrollTop<=1:port.scrollTop+port.clientHeight>=port.scrollHeight-1};}
      const button=[...header.querySelectorAll('button')].find(node=>node.textContent==='Expand conversation');
      if(button)button.click();
      return {ref:header.dataset.comicChildRef,key:header.dataset.comicDelegate};
      })()`);
      if(result?.boundary)direction*=-1;
      return result?.ref ? result : null;
    });
    if(!header.ref || !header.key)throw Error('missing authored owned identity');
    return header;
  };
  const child = await expand('comic-auth-child');
  const leaf = await expand('comic-auth-leaf');
  const childID = JSON.parse(child.key)[1];
  if(JSON.parse(child.key)[0]!==fixture.authorshipRef || JSON.parse(leaf.key)[0]!==child.ref)throw Error('fork ancestry lost');
  const cases = [
    {text:human,identity:'Anna · human · inherited',kind:'speech',speaker:'human',author:{kind:'human'}},
    ...['ROOT','CHILD'].flatMap(level=>[
      {text:`COMIC_AUTH_ANSWER_${level} literal original answer`,kind:'speech'},
      {text:`COMIC_AUTH_THOUGHT_${level} literal original thought`,kind:'thought'},
    ].map(value=>({...value,identity:level==='ROOT'?`Xeno · ${fixture.authorshipRef} · inherited`:`comic-auth-child · ${childID} · inherited`,speaker:'assistant',author:level==='ROOT'?{kind:'agent',sessionRef:fixture.authorshipRef}:{kind:'agent',sessionRef:child.ref,ownerRef:fixture.authorshipRef,delegateId:childID,delegateName:'comic-auth-child'}}))),
    {text:'COMIC_AUTH_UPDATE_CHILD literal original update',identity:`comic-auth-child · ${childID} · inherited`,kind:'speech',speaker:'assistant',author:{kind:'agent',sessionRef:child.ref,ownerRef:fixture.authorshipRef,delegateId:childID,delegateName:'comic-auth-child'}},
  ];
  const observations=[];
  const collect = async name => {
    const found=new Map();
    let direction=1;
    await until(name, async()=>{
      const result=await evaluateHere(`(() => {
        const port=document.querySelector('[data-comic-scroll]')?.firstElementChild;if(!port)return null;
        const entries=[...port.querySelectorAll('[data-comic-entry]')].filter(node=>node.dataset.comicSession===${JSON.stringify(leaf.ref)}).map(node=>({id:node.dataset.comicEntry,path:JSON.parse(node.dataset.comicPath),start:+node.dataset.comicStart,end:+node.dataset.comicEnd,text:node.querySelector('[data-comic-text]')?.textContent,identity:node.querySelector('[data-comic-identity]')?.textContent,kind:node.querySelector('[data-comic-balloon]')?.dataset.comicBalloon,speaker:node.querySelector('article')?.dataset.comicSpeaker}));
        const history=document.querySelector('main')?.dataset.sessionRef===${JSON.stringify(leaf.ref)}
          ? port.parentElement.previousElementSibling
          : [...port.querySelectorAll('[data-comic-flow="history"]')].find(node=>node.dataset.comicSession===${JSON.stringify(leaf.ref)});
        const older=[...(history?.querySelectorAll('button')??[])].find(node=>node.textContent==='Older messages');
        if(older&&!older.disabled)older.click();
        port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:${direction}*500}));port.scrollTop+=${direction}*500;
        return {entries,boundary:${direction}<0?port.scrollTop<=1:port.scrollTop+port.clientHeight>=port.scrollHeight-1};
      })()`);
      if(result?.boundary)direction*=-1;
      for(const entry of result?.entries??[])if(cases.some(value=>value.text===entry.text))found.set(entry.text,entry);
      writeFileSync(`${fixture.evidenceDir}/authorship-search.json`,JSON.stringify({name,root:fixture.authorshipRef,child,leaf,found:[...found.values()],last:result},null,2));
      await evaluateHere('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');
      return found.size===cases.length;
    });
    for(const expected of cases){
      const entry=found.get(expected.text);
      if(entry.identity!==expected.identity || entry.kind!==expected.kind || entry.speaker!==expected.speaker || entry.start!==0 || entry.end!==expected.text.length)throw Error(name+' wrong original author '+JSON.stringify({expected,entry}));
    }
    observations.push({name,entries:[...found.values()]});
    writeFileSync(`${fixture.evidenceDir}/authorship.json`,JSON.stringify({root:fixture.authorshipRef,child,leaf,cases,observations},null,2));
  };
  await collect('nested-inline-original-authors');
  if(observations[0].entries.some(entry=>entry.path.length!==2))throw Error('author proof did not use nested inline leaf');
  await screenshot('authorship-inline');
  await navigate(leaf.ref);
  await collect('direct-leaf-original-authors');
  await reloadPage(page);
  await collect('reloaded-leaf-original-authors');
  const beforeReconnect=outgoing.length;
  await evaluateHere("window.__comicSockets.forEach(socket=>socket.close(4000,'authorship recovery'))");
  await until('authored leaf resubscribed',()=>outgoing.slice(beforeReconnect).some(frame=>frame.method==='thread/read'&&frame.params.ref===leaf.ref&&frame.params.subscribe));
  await collect('reconnected-leaf-original-authors');
  await screenshot('authorship-direct');
  const pages=outgoing.slice(firstRequest).filter(frame=>frame.method==='thread/turns/list'&&frame.params.ref===leaf.ref);
  if(pages.length<2)throw Error('authored inherited sources did not cross real history pages');
  const turns=incoming.slice(firstFrame).flatMap(frame=>{const value=JSON.parse(frame);return value.result?.thread?.turns??value.result?.turns??value.result?.data??[];});
  const items=turns.flatMap(turn=>turn.items??[]);
  for(const expected of cases){
    const matches=items.filter(item=>item.authorship?.inherited && JSON.stringify(item).includes(expected.text));
    if(!matches.length || matches.some(item=>Object.entries(expected.author).some(([key,value])=>item.authorship.author[key]!==value)))throw Error('canonical inherited author mismatch '+JSON.stringify({expected,matches}));
  }
  const mutations=outgoing.slice(firstRequest).filter(frame=>frame.method==='turn/start'||frame.method==='thread/resume');
  if(mutations.length)throw Error('authored reads mutated a session');
  writeFileSync(`${fixture.evidenceDir}/authorship-wire.json`,JSON.stringify({pages,turns,requests:outgoing.slice(firstRequest)},null,2));
}
