#!/usr/bin/env node
import { spawn } from "node:child_process";
// Production hub + real daemon fixture. Go supplies an independently recorded literal source turn.
import { appendFileSync, mkdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import {
  connectBrowser,
  connectOnlyPage,
  connectPage,
  devtoolsHttpURL,
  evaluate,
  navigateTo,
  waitForHttp,
} from "../browserGuardCdp.mjs";
import {
  chromeProfileEnvironment,
  chromeProfileIsolationArgs,
  createBrowserProcessCleanup,
  createChromeProfileDir,
  findChrome,
  parseChromeDevToolsAnnouncement,
  requestBrowserClose,
} from "../browserGuardProcess.mjs";

export function comicScenario(scenario) {
  if (scenario === undefined || scenario === "") return { focused: false, rosterCount: 102 };
  if (scenario === "remote-navigation") return { focused: true, rosterCount: 101 };
  if (scenario === "recovery") return { focused: true };
  throw new Error(`unknown Comic scenario: ${scenario}`);
}

export async function run(fixturePath) {
  const fixture = JSON.parse(readFileSync(fixturePath, "utf8"));
  comicScenario(fixture.scenario);
  if (!fixture.baseURL || !fixture.refs?.live || !fixture.evidenceDir || !fixture.expectedSourceText)
    throw new Error("incomplete real-session fixture");
  mkdirSync(fixture.evidenceDir, { recursive: true });
  const profileDir = createChromeProfileDir("comicguard-");
  const lifecycle = createBrowserProcessCleanup({ profileDir });
  let page;
  let endpoint;
  const chrome = spawn(
    findChrome(),
    [
      "--headless=new",
      "--disable-gpu",
      ...chromeProfileIsolationArgs(),
      "--remote-debugging-port=0",
      `--user-data-dir=${profileDir}`,
      "--no-first-run",
      "--disable-extensions",
      "--window-size=1200,900",
      "about:blank",
    ],
    {
      stdio: ["ignore", "ignore", "pipe"],
      env: chromeProfileEnvironment(profileDir),
      detached: process.platform !== "win32",
    },
  );
  lifecycle.addChild(chrome, {
    processGroupId: process.platform === "win32" ? null : chrome.pid,
    gracefulClose: () => requestBrowserClose(endpoint),
  });
  const stop = async () => {
    try {
      page?.close();
    } catch {}
    await lifecycle.cleanup();
  };
  process.once("SIGTERM", () => {
    void stop().then(() => process.exit(143));
  });
  process.once("SIGINT", () => {
    void stop().then(() => process.exit(130));
  });
  try {
    endpoint = await new Promise((resolve, reject) => {
      let buffer = "";
      const timer = setTimeout(() => reject(new Error("Chrome DevTools endpoint unavailable")), 15000);
      const settle = (fn, value) => {
        clearTimeout(timer);
        fn(value);
      };
      chrome.once("error", (error) => settle(reject, error));
      chrome.once("exit", (code) => settle(reject, new Error(`Chrome exited ${code} before readiness`)));
      chrome.stderr.on("data", (chunk) => {
        buffer += String(chunk);
        const lines = buffer.split(/\r?\n/);
        buffer = lines.pop() ?? "";
        for (const line of lines) {
          if (!line.startsWith("DevTools listening on ")) continue;
          try {
            const parsed = parseChromeDevToolsAnnouncement(line);
            if (parsed) settle(resolve, parsed);
          } catch (error) {
            settle(reject, error);
          }
        }
      });
    });
    await waitForHttp(devtoolsHttpURL(endpoint, "/json/version"), "chrome devtools endpoint");
    page = await connectOnlyPage(endpoint);
    await page.send("Page.enable");
    await page.send("Runtime.enable");
    await journey(page, fixture, endpoint);
  } finally {
    await stop();
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = process.argv.slice(2);
  if (args.length !== 2 || args[0] !== "--fixture") {
    console.error("usage: node scripts/comicguard/run.mjs --fixture <absolute-json-path>");
    process.exitCode = 2;
  } else
    run(args[1]).catch((error) => {
      console.error(error);
      process.exitCode = 1;
    });
}

async function until(label, read, accept = Boolean) {
  const deadline = Date.now() + 20000;
  let value;
  do {
    value = await read();
    if (accept(value)) return value;
    await new Promise((resolve) => setTimeout(resolve, 40));
  } while (Date.now() < deadline);
  throw new Error(`${label}: ${JSON.stringify(value)}`);
}

async function reloadPage(page) {
  const before = await evaluate(page.send, "performance.timeOrigin");
  await page.send("Page.reload");
  await until(
    "new document after reload",
    async () => {
      try {
        return await evaluate(page.send, "performance.timeOrigin");
      } catch {
        return null;
      } // The old execution context is destroyed during navigation.
    },
    (value) => typeof value === "number" && value !== before,
  );
}

function repliesSince(trace, start, accept) {
  const requests = new Map();
  const replies = [];
  for (const row of trace.slice(start)) {
    if (!row.frame) continue;
    const payload = JSON.parse(row.frame);
    const key = `${row.connection}:${payload.id}`;
    if (row.event === "Network.webSocketFrameSent" && accept(payload)) requests.set(key, payload);
    if (row.event === "Network.webSocketFrameReceived" && requests.has(key)) {
      replies.push({ connection: row.connection, request: requests.get(key), response: payload });
    }
  }
  return replies;
}

async function nativeClick(page, label, expression) {
  const point = await until(label, () =>
    evaluate(
      page.send,
      `(async () => {
    const node = (${expression});
    if (!node || node.disabled) return null;
    const before = node.getBoundingClientRect();
    await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
    const r = node.getBoundingClientRect();
    if (!node.isConnected || Math.abs(before.x-r.x)>0.1 || Math.abs(before.y-r.y)>0.1) return null;
    const x = r.x + r.width / 2, y = r.y + r.height / 2;
    return r.width && r.height && node.contains(document.elementFromPoint(x, y)) ? {x,y} : null;
  })()`,
    ),
  );
  await page.send("Input.dispatchMouseEvent", {
    type: "mousePressed",
    ...point,
    button: "left",
    buttons: 1,
    clickCount: 1,
  });
  await page.send("Input.dispatchMouseEvent", {
    type: "mouseReleased",
    ...point,
    button: "left",
    buttons: 0,
    clickCount: 1,
  });
}

async function paneSnapshot(page) {
  return evaluate(
    page.send,
    `(() => {
    const ref=decodeURIComponent(location.pathname.slice(3));
    const owner = [...document.querySelectorAll('[data-session-actions-ref]')].find(node=>node.dataset.sessionActionsRef===ref && node.dataset.paneId);
    const scope = window.__comicRoot?.() ?? owner?.closest('[data-testid="pane-footer"]')?.parentElement ?? document;
    const editor = scope.querySelector('[role="textbox"][aria-label="Message"]');
    return {path:location.pathname, origin:performance.timeOrigin,
      ref:owner?.dataset.sessionActionsRef, pane:owner?.dataset.paneId,
      editor:editor?.innerHTML ?? null,
      attachments:JSON.stringify([...scope.querySelectorAll('[data-testid="attachment-tile"]')].map(tile=>({src:tile.querySelector('img')?.src,name:tile.querySelector('button[aria-label^="Remove "]')?.getAttribute('aria-label')}))),
      comic:!!window.__comicRoot?.(),
      sockets:window.__comicSockets?.filter(s=>s.readyState===WebSocket.OPEN && new URL(s.url).pathname==='/rpc').length};
  })()`,
  );
}

async function presentation(page, mode) {
  const before = await paneSnapshot(page);
  if (!before.ref || !before.pane || before.sockets !== 1)
    throw Error(`missing single pane/socket before mode change ${JSON.stringify(before)}`);
  await nativeClick(
    page,
    "Session actions reachable",
    `[...document.querySelectorAll('[data-session-actions-ref]')].find(node=>node.dataset.sessionActionsRef===decodeURIComponent(location.pathname.slice(3)) && node.dataset.paneId)?.closest('button')`,
  );
  await nativeClick(
    page,
    "Verbosity menu item reachable",
    `[...document.querySelectorAll('[role="menuitem"]')].find(n=>n.textContent.trim()==='Verbosity…')`,
  );
  await nativeClick(
    page,
    `${mode} Presentation reachable`,
    `[...document.querySelectorAll('[role="radiogroup"]')].find(group=>(group.getAttribute('aria-label')??document.getElementById(group.getAttribute('aria-labelledby'))?.textContent)==='Presentation')?.querySelector('[role="radio"][aria-label="${mode}"]')`,
  );
  await until(
    `${mode} selected`,
    () => paneSnapshot(page),
    (value) => value.comic === (mode === "Comic"),
  );
  if (await evaluate(page.send, `!!document.querySelector('[role="dialog"]')`)) {
    await nativeClick(page, "close Verbosity", `document.querySelector('[role="dialog"] button[aria-label="Close"]')`);
  }
  await until("Verbosity closed", () => evaluate(page.send, `!document.querySelector('[role="dialog"]')`));
  const after = await until(
    `${mode} same-pane presentation`,
    () => paneSnapshot(page),
    (value) => value.comic === (mode === "Comic") && value.editor === before.editor,
  );
  for (const key of ["path", "origin", "ref", "pane", "editor", "attachments", "sockets"]) {
    if (before[key] !== after[key])
      throw Error(`presentation changed ${key} ${JSON.stringify({ mode, before, after })}`);
  }
  return { mode, before, after };
}

async function evictChildHistory(page, ref, partial) {
  // This is the real browser persistence boundary, not a store setter or RPC
  // stub. Retain only the newest authentic read certificate for the partial
  // case, then require the missing older source to return over the real socket.
  return evaluate(
    page.send,
    `new Promise((resolve,reject) => {
    const open=indexedDB.open('evener-session-cache');
    open.onerror=()=>reject(open.error);
    open.onsuccess=()=>{
      const db=open.result, names=['transcriptItems','transcriptTurns','transcriptCoverage','transcriptMeta'];
      const tx=db.transaction(names,'readwrite'), rows={};
      let pending=names.length, observation;
      tx.onerror=()=>reject(tx.error);
      tx.oncomplete=()=>{db.close();resolve(observation);};
      for(const name of names){const request=tx.objectStore(name).getAll();request.onsuccess=()=>{
        rows[name]=request.result;if(--pending)return;
        const meta=rows.transcriptMeta.find(row=>row.requestedRefs?.some(value=>value.ref===${JSON.stringify(ref)}));
        if(!meta){tx.abort();reject(Error('child cache metadata absent'));return;}
        const key=meta.historyKey;
        const coverage=rows.transcriptCoverage.filter(row=>row.historyKey===key);
        const read=coverage.filter(row=>row.kind==='read'&&row.start).sort((a,b)=>b.start.entry-a.start.entry||b.start.item-a.start.item)[0];
        if(${partial}&&!read){tx.abort();reject(Error('newest authentic cache read certificate absent'));return;}
        const deleted={},retained={};
        for(const name of names){deleted[name]=0;retained[name]=0;const cursor=tx.objectStore(name).openCursor();cursor.onsuccess=()=>{
          const c=cursor.result;if(!c)return;const row=c.value;
          if(row.historyKey===key){
            const keep=${partial}&&(name==='transcriptMeta'||name==='transcriptTurns'||(name==='transcriptCoverage'&&row.rangeId===read.rangeId)||(name==='transcriptItems'&&(row.position.entry>read.start.entry||(row.position.entry===read.start.entry&&row.position.item>=read.start.item))));
            if(keep)retained[name]++;else{c.delete();deleted[name]++;}
          }c.continue();
        };}
        observation={ref:${JSON.stringify(ref)},partial:${partial},read:read&&{start:read.start,end:read.end,serverCursor:read.serverCursor},deleted,retained};
      };}
    };
  })`,
  );
}

async function journey(page, fixture, endpoint) {
  const { focused } = comicScenario(fixture.scenario);
  const command = (command, phase = "") =>
    appendFileSync(fixture.controlPath, `${JSON.stringify({ command, phase })}\n`);
  const complete = async (text, failed = false) => {
    command("background-start-turn", text);
    return until("real turn completion", () => {
      const records = readFileSync(fixture.requestLog, "utf8")
        .trim()
        .split("\n")
        .flatMap((line) => {
          try {
            return [JSON.parse(line)];
          } catch {
            return [];
          }
        });
      const record = records.find((record) => record.event === "turn-complete" && record.prompt === text);
      if (record && (failed ? record.ok || record.status !== "failed" : !record.ok))
        throw new Error(`unexpected real producer outcome: ${JSON.stringify(record)}`);
      return record;
    });
  };
  command("background-mode");
  command("background-role", "history");
  if (!focused) for (const text of fixture.historyTexts) await complete(text);
  await complete(fixture.expectedSourceText);
  let failedTurn;
  if (!focused) {
    command("background-role", "precontent-error");
    failedTurn = await complete("COMIC_PROVIDER_FAILURE", true);
    command("background-role", "history");
    writeFileSync(`${fixture.evidenceDir}/failed-turn.json`, JSON.stringify(failedTurn, null, 2));
  }

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
  const anonymousResponses = [];
  let canonicalFailure;
  const paused = [];
  let holdFonts = false;
  await page.send("Network.enable");
  await page.send("DOM.enable");
  await page.send("CSS.enable");
  page.ws.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (
      message.method === "Network.responseReceived" &&
      message.params.type === "Document" &&
      new URL(message.params.response.url).pathname === `/s/${encodeURIComponent(fixture.refs.live)}`
    ) {
      anonymousResponses.push({ status: message.params.response.status });
    }
    if (
      [
        "Network.webSocketCreated",
        "Network.webSocketFrameSent",
        "Network.webSocketFrameReceived",
        "Network.webSocketClosed",
      ].includes(message.method)
    ) {
      const row = {
        at: Date.now(),
        event: message.method,
        connection: message.params.requestId,
        frame: message.params.response?.payloadData,
      };
      wireTrace.push(row);
      appendFileSync(`${fixture.evidenceDir}/wire.jsonl`, `${JSON.stringify(row)}\n`);
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
          pageReads.push({
            request: requests.get(payload.id).params,
            turns: payload.result?.turns?.length,
            error: !!payload.error,
          });
          requests.delete(payload.id);
          activePages--;
        }
      } catch {}
    }
  });
  // Observe actual native sockets. A chosen outgoing history frame is dropped by
  // closing that transport, never by injecting a model or fabricated RPC reply.
  await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `(() => {
    window.__comicInlineGlyphs = ${inlineFixtureGlyphs.toString()};
    window.__comicInlinePoint = ${inlineFixturePoint.toString()};
    window.__comicFixtureSources = ${JSON.stringify([fixture.expectedSourceText, ...fixture.historyTexts, ...Array.from({ length: 45 }, (_, index) => `COMIC_CHILD_SOURCE_${String(index).padStart(2, "0")} literal child history with *source* characters.${index === 5 ? " Later-page child text preserves its literal reading offset. ".repeat(30) : ""}`)])};
    // Observation only. Ordinary child navigation can retain an owner pane,
    // so every Comic measurement belongs to the ref in the unchanged URL.
    window.__comicRoot = () => [...document.querySelectorAll('[data-comic-font][data-session-ref]')].find(node=>node.dataset.sessionRef===decodeURIComponent(location.pathname.slice(3)));
    const Native = window.WebSocket;
    window.__comicSockets = [];
    window.WebSocket = class extends Native {
      constructor(...args) {
        super(...args); window.__comicSockets.push(this);
        this.addEventListener('message', event => {
          const reply = JSON.parse(event.data);
          const originRequest = this.comicOriginRequests?.get(reply.id);
          if (originRequest) {
            this.comicOriginRequests.delete(reply.id);
            if (JSON.stringify(reply.result).includes('comic_delegate_GRANDCHILD')) {
              event.stopImmediatePropagation();
              const data = event.data;
              window.__comicOriginHold = {request:originRequest,response:reply,releaseCount:0};
              window.__comicReleaseOrigin = () => {
                if (window.__comicOriginHold.releaseCount++) throw Error('origin response already released');
                window.__comicHoldOriginRef = undefined;
                this.dispatchEvent(new MessageEvent('message', {data,origin:event.origin,lastEventId:event.lastEventId}));
              };
              return;
            }
          }
          if (this.comicResumeRequest && JSON.parse(event.data).id === this.comicResumeRequest.id) {
            event.stopImmediatePropagation();
            const hold = window.__comicResumeHold;
            const data = event.data;
            hold.responseBytes = data;
            hold.heldAt = Date.now();
            window.__comicReleaseResume = () => {
              if (hold.releaseCount !== 0) throw Error('resume response already released');
              this.comicResumeRequest = undefined;
              hold.releaseCount++;
              hold.releasedBytes = data;
              hold.releasedAt = Date.now();
              this.dispatchEvent(new MessageEvent('message', {data, origin:event.origin, lastEventId:event.lastEventId}));
            };
            return;
          }
          if (this.comicRecoveryRequest && JSON.parse(event.data).id === this.comicRecoveryRequest.id) {
            event.stopImmediatePropagation();
            window.__comicRecoveryIntercepted = {request:this.comicRecoveryRequest,response:JSON.parse(event.data)};
            window.__comicCloseRecovery = () => this.close(4000, 'comic accepted before acknowledgement');
            return;
          }
          if (this.comicHeldId !== undefined && JSON.parse(event.data).id === this.comicHeldId) {
            event.stopImmediatePropagation();
            const data = event.data;
            window.__comicReleaseApproval = () => { this.comicHeldId = undefined; this.dispatchEvent(new MessageEvent('message', {data})); };
          }
        });
      }
      send(data) {
        if (window.__comicFailHistory && JSON.parse(data).method === 'thread/turns/list') {
          window.__comicFailHistory = false;
          this.close(4000, 'comic history transport failure');
          return;
        }
        const request = JSON.parse(data);
        if (request.method === 'thread/turns/list' && request.params?.ref === window.__comicHoldOriginRef) {
          this.comicOriginRequests ??= new Map();
          this.comicOriginRequests.set(request.id, request);
        }
        const resumeHold = window.__comicResumeHold;
        if (resumeHold && request.method === 'turn/start') {
          resumeHold.starts.push({socket:window.__comicSockets.indexOf(this), beforeRelease:resumeHold.releaseCount === 0, bytes:data});
        }
        if (resumeHold && !resumeHold.requestBytes && request.method === 'thread/resume' && request.params?.ref === resumeHold.ref) {
          this.comicResumeRequest = request;
          resumeHold.requestBytes = data;
          resumeHold.socket = window.__comicSockets.indexOf(this);
        }
        if (request.method === 'turn/start' && window.__comicRecoveryBoundary) {
          const boundary = window.__comicRecoveryBoundary;
          window.__comicRecoveryBoundary = undefined;
          if (boundary === 'before') {
            window.__comicRecoveryIntercepted = {request};
            window.__comicCloseRecovery = () => this.close(4000, 'comic before delivery');
            return;
          }
          this.comicRecoveryRequest = request;
        }
        if (request.method === 'evener/sandbox/escalation/resolve') {
          if (window.__comicFailApproval) {
            window.__comicFailApproval = false;
            window.__comicFailedApproval = request;
            // Drop the actual send, then let the driver observe busy controls
            // before closing the native socket. No response is fabricated.
            window.__comicCloseApproval = () => this.close(4000, 'comic approval transport failure');
            return;
          }
          if (window.__comicHoldApproval) {
            window.__comicHoldApproval = false;
            this.comicHeldId = request.id;
          }
        }
        super.send(data);
      }
    };
  })()`,
  });
  if (fixture.requireFonts)
    await page.send("Fetch.enable", { patterns: [{ urlPattern: "*/webassets/comic-fonts/*" }] });
  else await page.send("Network.setBlockedURLs", { urls: ["*/webassets/comic-fonts/*"] });
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 1360,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await navigateTo(page, new URL(`/s/${encodeURIComponent(fixture.refs.live)}`, fixture.baseURL).href);
  await until(
    "unauthenticated conversation rejection",
    () => anonymousResponses.at(-1),
    (response) => response?.status === 401,
  );
  if (await evaluate(page.send, '!!document.querySelector("[data-comic-font]")'))
    throw Error("unauthenticated Comic mounted");
  writeFileSync(
    `${fixture.evidenceDir}/authentication.json`,
    JSON.stringify({ anonymous: anonymousResponses.at(-1), mounted: false }, null, 2),
  );
  const entryRef = fixture.authorshipRef || fixture.refs.live;
  const authenticated = await evaluate(
    page.send,
    `fetch(${JSON.stringify(fixture.baseURL)},{credentials:'include'}).then(response=>response.status)`,
  );
  if (authenticated !== 200) throw Error(`real authentication endpoint rejected ${authenticated}`);
  const base = new URL(fixture.baseURL).origin;
  const route = `${base}/s/${encodeURIComponent(fixture.refs.live)}`;
  // Page.load waits for paused fonts. The actual reader, not that event, is the readiness contract.
  await page.send("Page.navigate", { url: `${base}/s/${encodeURIComponent(entryRef)}` });
  await until(
    "normal session pane ready",
    () => paneSnapshot(page),
    (value) => value.ref === entryRef && value.editor !== null && value.sockets === 1,
  );
  if ((await paneSnapshot(page)).comic) throw Error("fresh browser did not default to Standard");
  const modeEntry = [await presentation(page, "Comic")];
  writeFileSync(`${fixture.evidenceDir}/presentation-entry.json`, JSON.stringify(modeEntry, null, 2));
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const read = (point = null) =>
    evaluateHere(`(${readGeometry.toString()})(${JSON.stringify(point)}, ${JSON.stringify(fixture.refs.live)})`);
  let sourceCoverage;
  const click = (name) =>
    evaluateHere(
      `(() => { const button = [...document.querySelectorAll('button')].find(node => (node.textContent.trim() === ${JSON.stringify(name)} || node.getAttribute("aria-label") === ${JSON.stringify(name)})); if (!button || button.disabled) return false; button.click(); return true; })()`,
    );
  const select = (name, value) => selectAppearance(page, name, value);
  const screenshot = async (name) => {
    const shot = await page.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(`${fixture.evidenceDir}/${name}.png`, Buffer.from(shot.result.data, "base64"));
  };
  const record = async (name, point, end = false) => {
    const value = await until(
      name,
      () => read(point),
      (value) =>
        value &&
        value.mounted > 0 &&
        !value.missingArt.length &&
        (end ? value.endGap <= 2 : point ? value.point && Math.abs(value.point.y - point.viewportOffset) <= 2 : true) &&
        (!canonicalFailure ||
          !name.startsWith("initial-error-") ||
          value.parts.some((part) => part.entryId === `${canonicalFailure.items[1].id}:turn-error`)) &&
        (value.mode !== "comic" ||
          !name.startsWith("away-width") ||
          value.panels.some(
            (panel) =>
              Math.abs(panel.panel.width - value.probe.panel.width) < 1 &&
              Math.abs(panel.text.width - value.probe.text.width) < 1 &&
              panel.text.font === value.probe.text.font,
          )),
    );
    if (
      value.overflow > 1 ||
      value.pageOverflow > 1 ||
      value.clipped.length ||
      value.mounted > 35 ||
      value.missingArt.length
    )
      throw new Error(`${name}: bad geometry ${JSON.stringify(value)}`);
    if (value.path !== new URL(route).pathname || value.ref !== fixture.refs.live)
      throw new Error(`${name}: wrong authenticated conversation route ${JSON.stringify(value)}`);
    if (name === "away-before-font" && sourceCoverage !== fixture.expectedSourceText)
      throw new Error("complete independent literal source was not rendered exactly once");
    if (point && (value.saved?.entryId !== point.entryId || value.saved?.offset !== point.offset))
      throw new Error(`${name}: anchor identity changed ${JSON.stringify(value.saved)}`);
    if (end && name !== "initial-end" && value.saved?.mode !== "end")
      throw new Error(`${name}: lost explicit end-following ${JSON.stringify(value.saved)}`);
    if (canonicalFailure && point?.text === fixture.expectedSourceText) {
      const failureItem = canonicalFailure.items.find((item) => item.eventKind === "error");
      if (
        value.errorGroups.length ||
        value.parts.some(
          (part) => part.entryId.endsWith(":turn-error") && part.entryId !== `${failureItem.id}:turn-error`,
        )
      )
        throw new Error(`${name}: canonical item-backed error misgrouped ${JSON.stringify(value)}`);
    }
    if (
      value.mode === "comic" &&
      name.startsWith("away-width") &&
      !value.panels.some(
        (panel) =>
          Math.abs(panel.panel.width - value.probe.panel.width) < 1 &&
          Math.abs(panel.text.width - value.probe.text.width) < 1 &&
          panel.text.font === value.probe.text.font,
      )
    )
      throw new Error(`${name}: probe does not match a rendered panel ${JSON.stringify(value)}`);
    if (canonicalFailure && name.startsWith("initial-error-"))
      verifyCanonicalErrorMounted(value, `${canonicalFailure.items[1].id}:turn-error`, name);
    if (
      value.mode === "comic" &&
      (name === "initial-end" || name.startsWith("away-width") || name === "real-pair-850")
    ) {
      const geometry = await evaluateHere(`(${panelWidthCases.toString()})()`);
      verifyPanelWidths(geometry, name, name === "real-pair-850");
      writeFileSync(`${fixture.evidenceDir}/panel-width-${value.width}.json`, JSON.stringify(geometry, null, 2));
    }
    if (point?.text)
      collectSourceSlices(
        new Map(),
        value.parts.filter((part) => part.entryId === point.entryId),
        point.text,
      );
    const preceding = new Map();
    for (const part of value.parts) {
      if (preceding.has(part.entryId) && preceding.get(part.entryId) !== part.start)
        throw new Error(`${name}: duplicated or omitted interior source range ${JSON.stringify(part)}`);
      preceding.set(part.entryId, part.end);
    }
    metrics.push({ name, pageCount: pageReads.length, ...value });
    wireTrace.push({ at: Date.now(), checkpoint: name, pageCount: pageReads.length });
    writeFileSync(
      `${fixture.evidenceDir}/measurements.json`,
      JSON.stringify({ metrics, pageReads, maxPages, socketsClosed, subscriptions }, null, 2),
    );
    return value;
  };
  // Choose both viewport preferences through their real controls. Merely resizing
  // must not copy the desktop choice into the as-yet untouched mobile preference.
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 390,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await until(
    "mobile starts independently Standard",
    () => paneSnapshot(page),
    (value) => !value.comic && value.editor !== null,
  );
  modeEntry.push(await presentation(page, "Comic"));
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 1360,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await until(
    "desktop Comic retained",
    () => paneSnapshot(page),
    (value) => value.comic,
  );
  modeEntry.push(await presentation(page, "Standard"));
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 390,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await until(
    "desktop Standard leaves mobile Comic intact",
    () => paneSnapshot(page),
    (value) => value.comic,
  );
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 1360,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await until(
    "desktop Standard restored",
    () => paneSnapshot(page),
    (value) => !value.comic && value.editor !== null,
  );
  modeEntry.push(await presentation(page, "Comic"));
  const preferences = await evaluateHere(
    `Object.fromEntries(['desktop','mobile'].map(v=>[v,localStorage.getItem('evener.prefs.conversationPresentation.'+v)]))`,
  );
  if (Object.values(preferences).some((value) => value !== "comic"))
    throw Error(`mode preferences not independently persisted ${JSON.stringify(preferences)}`);
  writeFileSync(`${fixture.evidenceDir}/presentation-entry.json`, JSON.stringify({ modeEntry, preferences }, null, 2));
  // The ordinary Standard reader may legitimately prefetch history. Keep the
  // root history cold until its paging proof, without changing either store.
  await until(
    "entry fonts settled",
    () => evaluateHere(`window.__comicRoot()?.dataset.comicFont`),
    (value) => value === "ready" || value === "unavailable",
  );
  holdFonts = fixture.requireFonts;
  await page.send("Network.setCacheDisabled", { cacheDisabled: true });
  await page.send("Page.navigate", { url: route });
  await until(
    "initial real comic",
    () => read(),
    (value) => value?.parts.some((part) => part.text.startsWith("COMIC_READ_SOURCE_3f") && part.text.length > 100),
  );
  if (!focused) {
    const initialEnd = await record("initial-end", null, true);
    const canonicalTurns = incoming
      .flatMap((frame) => {
        const payload = JSON.parse(frame);
        return payload.result?.thread?.turns ?? payload.result?.turns ?? [];
      })
      .filter((turn) => turn.id === failedTurn.turnId);
    writeFileSync(`${fixture.evidenceDir}/failed-turn-canonical.json`, JSON.stringify(canonicalTurns, null, 2));
    canonicalFailure = canonicalTurns.at(-1);
    if (
      canonicalFailure?.status !== "failed" ||
      canonicalFailure.error?.cause?.kind !== "provider" ||
      canonicalFailure.error.cause.provider !== "openai" ||
      canonicalFailure.items.length !== 2 ||
      canonicalFailure.items[0].type !== "userMessage" ||
      canonicalFailure.items[1].type !== "systemMessage" ||
      canonicalFailure.items[1].eventKind !== "error" ||
      canonicalFailure.items[1].status !== "failed"
    )
      throw new Error(`unexpected canonical provider failure: ${JSON.stringify(canonicalTurns)}`);
    verifyCanonicalErrorMounted(initialEnd, `${canonicalFailure.items[1].id}:turn-error`, "initial-end");
    // The end-following checkpoint keeps this item mounted while changing width
    // and presentation; an away-point virtual window may omit it legitimately.
    const mountLatestError = async () =>
      evaluateHere(`(() => {
    const port=window.__comicRoot().querySelector('[data-comic-scroll]').firstElementChild;
    port.scrollTop=port.scrollHeight;
    return new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  })()`);
    for (const width of [390, 1360]) {
      await page.send("Emulation.setDeviceMetricsOverride", {
        width,
        height: 900,
        deviceScaleFactor: 1,
        mobile: false,
      });
      await mountLatestError();
      await record(`initial-error-width-${width}`, null);
    }
    await select("View", "plain");
    await mountLatestError();
    await record("initial-error-plain", null);
    await select("View", "comic");
    await mountLatestError();
    await record("initial-error-comic", null);
    // Scroll a literal source point into view as a reader would. No model/store injection.
    const findSource = () =>
      until("literal source mounted within virtual rows", () =>
        evaluateHere(`(() => {
    const source = [...window.__comicRoot()?.querySelectorAll('[data-comic-entry]')].find(node => node.querySelector('[data-comic-text]')?.textContent.startsWith('COMIC_READ_SOURCE_3f'));
    const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
    if (!source) {
      const deltaY = -Math.min(180, port.clientHeight / 2);
      port.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY }));
      port.scrollTop = Math.max(0, port.scrollTop + deltaY);
      return null;
    }
    return source.dataset.comicEntry;
  })()`),
      );
    const sourceEntryId = await findSource();
    const covered = new Map();
    for (let step = 0; step < 80; step++) {
      const parts = (await read()).parts.filter((part) => part.entryId === sourceEntryId);
      collectSourceSlices(covered, parts, fixture.expectedSourceText);
      const ordered = [...covered.values()].sort((a, b) => a.start - b.start);
      if (
        ordered[0]?.start === 0 &&
        ordered.at(-1)?.end === fixture.expectedSourceText.length &&
        ordered.every((part, index) => index === 0 || ordered[index - 1].end === part.start)
      ) {
        sourceCoverage = ordered.map((part) => fixture.expectedSourceText.slice(part.start, part.end)).join("");
        break;
      }
      const moved = await evaluateHere(`(() => {
      const port=window.__comicRoot().querySelector('[data-comic-scroll]').firstElementChild;
      const before=port.scrollTop;
      const deltaY=Math.max(100,port.clientHeight/2);
      port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));
      port.scrollTop+=deltaY;
      return port.scrollTop>before;
    })()`);
      if (!moved) break;
      await evaluateHere("new Promise(resolve=>requestAnimationFrame(resolve))");
    }
    if (sourceCoverage !== fixture.expectedSourceText)
      throw Error("complete independent literal source was not rendered exactly once across virtual rows");
    writeFileSync(
      `${fixture.evidenceDir}/literal-source-coverage.json`,
      JSON.stringify(
        [...covered.values()].sort((a, b) => a.start - b.start).map(({ rowId, start, end }) => ({ rowId, start, end })),
        null,
        2,
      ),
    );
    await findSource();
    const target = await evaluateHere(`(() => {
    const source = [...window.__comicRoot()?.querySelectorAll('[data-comic-entry]')].find(node => node.querySelector('[data-comic-text]')?.textContent.startsWith('COMIC_READ_SOURCE_3f'));
    if (!source) throw Error('source unmounted after traversal');
    const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
    const glyph = window.__comicInlinePoint(source, ${JSON.stringify(fixture.expectedSourceText)}, 650);
    const deltaY = glyph.top - port.getBoundingClientRect().top - 24;
    port.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY }));
    port.scrollTop += deltaY;
    return source.dataset.comicEntry;
  })()`);
    // The real reader captures a gesture on animation frame. Finish that gesture
    // before establishing the fixed point or triggering an unrelated font reflow.
    await evaluateHere(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`);
    let point = await until(
      "captured source point",
      () => read(),
      (value) => value?.saved?.mode === "away" && value.saved.entryId === target && value.saved.offset > 0,
    );
    point = { ...point.saved, text: fixture.expectedSourceText };
    await record("away-before-font", point);
    // A pre-content failure is NOT itemless: the runtime records both user input
    // and a positioned TURN_FAILURE. Keep that distinction, never fake an empty turn.
    await record("provider-failure-away", point);
    if (fixture.requireFonts) {
      await until(
        "bundled regular font request",
        async () => paused.length,
        (count) => count === 1,
      );
      holdFonts = false;
      await Promise.all(paused.map((requestId) => page.send("Fetch.continueRequest", { requestId })));
      await until(
        "bundled font loaded",
        () => read(),
        (value) => value?.font === "ready",
      );
      await page.send("Network.setCacheDisabled", { cacheDisabled: false });
      await record("away-after-late-font", point);
    } else
      await until(
        "explicit failed font state",
        () => read(),
        (value) => value?.font === "unavailable",
      );

    // A real non-text click used to leave the reader permanently awaiting scroll.
    // Hit-test rendered art, then use CDP input, not element.click or DOM events.
    const clickTarget = await evaluateHere(`(() => {
    const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
    const bounds = port.getBoundingClientRect();
    for (const panel of port.querySelectorAll('article')) {
      const rect = panel.getBoundingClientRect();
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
        window.__comicClickDiagnostics = [];
        window.__comicClickDiagnostic = event => window.__comicClickDiagnostics.push({type:event.type, scrollTop:port.scrollTop, collapsed:document.getSelection()?.isCollapsed, selectionNode:document.getSelection()?.anchorNode?.nodeName, saved:sessionStorage.getItem('evener:comic:position:' + window.__comicRoot()?.dataset.sessionRef)});
        document.addEventListener('selectionchange', window.__comicClickDiagnostic);
        port.addEventListener('scroll', window.__comicClickDiagnostic);
        return {x, y, scrollTop:port.scrollTop};
      }
    }
    throw Error('no visible non-text panel click target');
  })()`);
    await page.send("Input.dispatchMouseEvent", {
      type: "mousePressed",
      x: clickTarget.x,
      y: clickTarget.y,
      button: "left",
      buttons: 1,
      clickCount: 1,
    });
    await page.send("Input.dispatchMouseEvent", {
      type: "mouseReleased",
      x: clickTarget.x,
      y: clickTarget.y,
      button: "left",
      buttons: 0,
      clickCount: 1,
    });
    await evaluateHere(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`);
    writeFileSync(
      `${fixture.evidenceDir}/native-click-diagnostics.json`,
      JSON.stringify(
        await evaluateHere(`(() => {
    document.removeEventListener('selectionchange', window.__comicClickDiagnostic);
    window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild.removeEventListener('scroll', window.__comicClickDiagnostic);
    return {target:${JSON.stringify(clickTarget)}, events:window.__comicClickEvents, diagnostics:window.__comicClickDiagnostics};
  })()`),
        null,
        2,
      ),
    );
    const clicked = await record("native-click-without-scroll", point);
    const clickEvents = await evaluateHere(`(() => {
    const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
    port.removeEventListener('pointerdown', window.__comicClickListener);
    port.removeEventListener('pointerup', window.__comicClickListener);
    return window.__comicClickEvents;
  })()`);
    if (
      clicked.scrollTop !== clickTarget.scrollTop ||
      clickEvents.map((event) => event.type).join(",") !== "pointerdown,pointerup" ||
      !clickEvents.every(
        (event) => event.trusted && event.pointerType === "mouse" && event.scrollTop === clickTarget.scrollTop,
      )
    )
      throw new Error(
        `native click did not complete without scrolling: ${JSON.stringify({ clickTarget, clickEvents, after: clicked.scrollTop })}`,
      );
    writeFileSync(
      `${fixture.evidenceDir}/native-click.json`,
      JSON.stringify({ clickTarget, clickEvents, after: clicked.scrollTop }, null, 2),
    );

    for (const width of [1360, 850, 760, 600, 390, 320, 1360]) {
      await page.send("Emulation.setDeviceMetricsOverride", {
        width,
        height: 900,
        deviceScaleFactor: 1,
        mobile: false,
      });
      await record(`away-width-${width}`, point);
      await screenshot(width === 320 ? "phone" : width === 1360 ? "desktop" : `width-${width}`);
      // Root identities are intentionally fixed. Stress their actual inherited
      // label CSS in a detached-from-transcript probe, not a fake session entry.
      const identity = await evaluateHere(`(() => {
      const original = window.__comicRoot()?.querySelector('[data-comic-entry] article');
      const panel = original.cloneNode(false);
      const label = original.querySelector('[data-comic-identity]').cloneNode(false);
      label.textContent = 'FIXTURE_LONG_AGENT_NAME_' .repeat(12) + ' · FIXTURE_LONG_BADGE_' .repeat(10);
      panel.style.cssText = 'position:absolute;visibility:hidden;width:' + original.getBoundingClientRect().width + 'px';
      panel.append(label); window.__comicRoot().append(panel);
      const result = {width:innerWidth, labelWidth:label.clientWidth, labelHeight:label.clientHeight, lineHeight:parseFloat(getComputedStyle(label).lineHeight), overflow:label.scrollWidth-label.clientWidth, family:getComputedStyle(label).fontFamily};
      panel.remove(); return result;
    })()`);
      if (identity.overflow > 1 || identity.labelHeight <= identity.lineHeight * 2)
        throw new Error(`long identity CSS did not wrap: ${JSON.stringify(identity)}`);
      writeFileSync(`${fixture.evidenceDir}/identity-${width}.json`, JSON.stringify(identity, null, 2));
    }
    await select("View", "plain");
    await record("away-plain", point);
    if (fixture.requireFonts) {
      const doc = await page.send("DOM.getDocument");
      const regular = await page.send("DOM.querySelector", {
        nodeId: doc.result.root.nodeId,
        selector: "[data-comic-entry] [data-comic-text]",
      });
      const bold = await page.send("DOM.querySelector", {
        nodeId: doc.result.root.nodeId,
        selector: "[data-comic-entry] strong",
      });
      const fonts = await Promise.all(
        [regular, bold].map((node) => page.send("CSS.getPlatformFontsForNode", { nodeId: node.result.nodeId })),
      );
      writeFileSync(
        `${fixture.evidenceDir}/fonts.json`,
        JSON.stringify(
          fonts.map((face) => face.result),
          null,
          2,
        ),
      );
      if (
        !fonts.every((face) =>
          face.result.fonts.some((font) => font.isCustomFont && font.glyphCount > 0 && /Comic/i.test(font.familyName)),
        )
      )
        throw new Error(
          `Comic Sans not actually rendering regular and synthesized-bold text: ${JSON.stringify(fonts)}`,
        );
    }
    await select("View", "comic");
    // Switching from plain changes measured row heights. Wait for the bounded
    // virtual range to settle before checking its error row or presentation.
    let previousParts;
    let stableSamples = 0;
    await until("comic range settled after plain view", async () => {
      const parts = JSON.stringify((await read(point)).parts);
      stableSamples = parts === previousParts ? stableSamples + 1 : 0;
      previousParts = parts;
      return stableSamples >= 3;
    });
    await record("away-comic", point);
    const beforeStyle = await read(point);
    await select("Scene", "field");
    await select("Human expression", "happy-wave");
    const afterStyle = await record("presentation-only", point);
    if (
      JSON.stringify(beforeStyle.parts) !== JSON.stringify(afterStyle.parts) ||
      !afterStyle.scenes.every((scene) => scene === "field") ||
      !afterStyle.humanImages.every((src) => src.endsWith("anna-happy-wave.png"))
    )
      throw new Error("presentation changed source or failed to change art");

    for (let index = 0; index < 3; index++) {
      const before = pageReads.length;
      if (index === 0) await evaluateHere("window.__comicFailHistory = true");
      await until(`Older messages available for page ${index + 1}`, () => click("Older messages"));
      if (index === 0) {
        await until("history transport failure visible", () =>
          evaluateHere(`!![...document.querySelectorAll('button')].find(node => node.textContent.trim() === 'Retry')`),
        );
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
      const source = [...window.__comicRoot()?.querySelectorAll('[data-comic-entry]')].find(node => node.querySelector('[data-comic-text]')?.textContent.startsWith('COMIC_HISTORY_27'));
      const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
      const deltaY = source ? source.querySelector('[data-comic-text]').getBoundingClientRect().top - port.getBoundingClientRect().top - 24 : -port.clientHeight * 3;
      port.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY }));
      port.scrollTop += deltaY;
      return source?.dataset.comicEntry ?? null;
    })()`);
      if (value) olderPoint = value;
      return value;
    });
    const captured = await until(
      "later page anchor saved",
      () => read(),
      (value) => value?.saved?.entryId === olderPoint,
    );
    point = { ...captured.saved, text: fixture.historyTexts[27] };
    await record("later-page-away", point);
    const rootHandoff = [await presentation(page, "Standard"), await presentation(page, "Comic")];
    await record("away-mode-handoff", point);
    writeFileSync(`${fixture.evidenceDir}/root-mode-handoff.json`, JSON.stringify(rootHandoff, null, 2));
    for (const width of [320, 760, 1360]) {
      await page.send("Emulation.setDeviceMetricsOverride", {
        width,
        height: 900,
        deviceScaleFactor: 1,
        mobile: false,
      });
      await record(`later-page-width-${width}`, point);
    }
    const stream = async (label, anchor) => {
      const before = incoming.length;
      command("background-role", "target");
      command("background-start-turn", `COMIC_STREAM_${label}`);
      await until("real initial stream delta", async () =>
        incoming.slice(before).some((frame) => frame.includes("BACKGROUND_BEFORE_EVICTION")),
      );
      await record(`${label}-stream-start`, anchor, !anchor);
      command("background-phase", "after-eviction");
      await until("real subsequent stream delta", async () =>
        incoming.slice(before).some((frame) => frame.includes("BACKGROUND_AFTER_EVICTION")),
      );
      await record(`${label}-stream-grow`, anchor, !anchor);
      command("background-phase", "finish");
      await until("real stream completes", () =>
        readFileSync(fixture.requestLog, "utf8")
          .split("\n")
          .some((line) => {
            try {
              const row = JSON.parse(line);
              return row.event === "turn-complete" && row.prompt === `COMIC_STREAM_${label}` && row.ok;
            } catch {
              return false;
            }
          }),
      );
      await record(`${label}-stream-finished`, anchor, !anchor);
      command("background-role", "history");
    };
    await stream("away", point);
    await complete("COMIC_LIVE_APPEND_away");
    await record("away-live-append", point);
    const beforeReload = wireTrace.length;
    await reloadPage(page);
    const freshRoot = await until("fresh root history after reload", () =>
      repliesSince(
        wireTrace,
        beforeReload,
        (request) =>
          request.method === "thread/read" &&
          request.params?.ref === fixture.refs.live &&
          request.params.includeTurns === true,
      ).find((reply) => reply.response.result?.thread?.turns?.length > 0),
    );
    writeFileSync(`${fixture.evidenceDir}/reload-history-reply.json`, JSON.stringify(freshRoot, null, 2));
    await evaluateHere(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))`);
    await record("reload-sequential-history", point);
    const closedBefore = socketsClosed;
    const subscriptionsBefore = subscriptions;
    await page.send("Network.emulateNetworkConditions", {
      offline: true,
      latency: 0,
      downloadThroughput: 0,
      uploadThroughput: 0,
    });
    const closed = await evaluateHere(
      `(() => { const sockets = window.__comicSockets.filter(socket => socket.readyState === WebSocket.OPEN && new URL(socket.url).pathname === '/rpc'); sockets.forEach(socket => socket.close(4000, 'comic reconnect boundary')); return sockets.length; })()`,
    );
    if (closed !== 1) throw new Error(`expected one shared hub socket, found ${closed}`);
    await complete("COMIC_RECONNECT_GAP");
    await page.send("Network.emulateNetworkConditions", {
      offline: false,
      latency: 0,
      downloadThroughput: -1,
      uploadThroughput: -1,
    });
    await until(
      "real socket resubscription",
      async () => socketsClosed > closedBefore && subscriptions > subscriptionsBefore,
    );
    await record("reconnect-away", point);
    await click("Jump to latest");
    await record("jump-latest", null, true);
    await stream("end", null);
    await complete("COMIC_LIVE_APPEND_end");
    await record("end-live-append", null, true);
    for (const width of [320, 760, 1360]) {
      await page.send("Emulation.setDeviceMetricsOverride", {
        width,
        height: 900,
        deviceScaleFactor: 1,
        mobile: false,
      });
      await record(`end-width-${width}`, null, true);
    }
    await page.send("Emulation.setDeviceMetricsOverride", {
      width: 850,
      height: 900,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await until("genuine paired source row at transition width", async () => {
      const found = await evaluateHere(`(() => {
      const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;
      const pair=[...port.querySelectorAll('[data-comic-row]')].find(row=>row.querySelectorAll('[data-comic-entry] article').length===2);
      if(pair)return pair.dataset.comicRow;
      const deltaY=-Math.max(120,port.clientHeight/2);
      port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));
      port.scrollTop=Math.max(0,port.scrollTop+deltaY);
      return null;
    })()`);
      if (!found) await evaluateHere("new Promise(resolve=>requestAnimationFrame(resolve))");
      return found;
    });
    await record("real-pair-850", null);
    writeFileSync(
      `${fixture.evidenceDir}/root-reads.json`,
      JSON.stringify(
        { reads: outgoing.filter((frame) => frame.method === "thread/read"), maxPages, pageReads, wireTrace },
        null,
        2,
      ),
    );
    if (maxPages !== 1 || pageReads.length < 6)
      throw new Error(`shared sequential paging not exercised: ${JSON.stringify({ maxPages, pageReads })}`);
  }
  try {
    if (!focused)
      await formattedJourney(page, fixture, {
        command,
        complete,
        outgoing,
        screenshot,
        wireTrace,
        holdFonts: async () => {
          paused.length = 0;
          holdFonts = fixture.requireFonts;
          await page.send("Network.setCacheDisabled", { cacheDisabled: true });
        },
        releaseFonts: async () => {
          if (fixture.requireFonts) await until("technical font request held", async () => paused.length);
          holdFonts = false;
          await Promise.all(paused.splice(0).map((requestId) => page.send("Fetch.continueRequest", { requestId })));
          await page.send("Network.setCacheDisabled", { cacheDisabled: false });
        },
      });
    if (!focused) await childJourney(page, fixture, { command, complete, incoming, outgoing, screenshot, wireTrace });
    if (fixture.scenario !== "recovery") {
      await rosterJourney(page, fixture, { command, complete, incoming, outgoing, screenshot, wireTrace });
      await remoteJourney(page, fixture, { incoming, wireTrace, screenshot });
    }
    if (!focused) {
      await authorshipJourney(page, fixture, { incoming, outgoing, screenshot });
      await composerJourney(page, fixture, { command, wireTrace, screenshot });
      await interactionJourney(page, fixture, { command, wireTrace, screenshot });
    }
    if (!focused || fixture.scenario === "recovery") {
      await sendRecoveryJourney(page, fixture, { command, wireTrace, screenshot });
      await resumeJourney(page, fixture, { wireTrace, screenshot, endpoint });
    }
  } catch (error) {
    await screenshot("child-failure");
    writeFileSync(`${fixture.evidenceDir}/failure-wire.json`, JSON.stringify(wireTrace, null, 2));
    writeFileSync(
      `${fixture.evidenceDir}/child-failure.json`,
      JSON.stringify(
        {
          error: String(error),
          dom: await evaluateHere("window.__comicRoot()?.outerHTML"),
          requests: outgoing.filter((frame) => frame.method === "thread/read"),
          frames: incoming
            .map((frame) => JSON.parse(frame))
            .filter((frame) => frame.method || frame.result?.context || frame.result?.thread || frame.result?.turns),
        },
        null,
        2,
      ),
    );
    throw error;
  }
  if (focused) console.log(`Focused Comic ${fixture.scenario} only, not the full gate; ${fixture.evidenceDir}`);
  else
    console.log(
      `Comic responsive reader: ${metrics.length} measured scenarios, ${pageReads.length} history pages, max concurrent ${maxPages}, bundled font ${fixture.requireFonts}; ${fixture.evidenceDir}`,
    );
}

export function verifyComposerReceipts(trace, ref) {
  const methods = ["turn/start", "turn/queue", "turn/interrupt"];
  const pairs = repliesSince(trace, 0, (request) => methods.includes(request.method));
  const ids = new Set();
  for (const [index, method] of methods.entries()) {
    const sent = trace.filter(
      (row) => row.event === "Network.webSocketFrameSent" && row.frame && JSON.parse(row.frame).method === method,
    );
    const matches = pairs.filter((pair) => pair.request.method === method);
    if (sent.length !== 1 || matches.length !== 1)
      throw new Error(`${method}: expected one request and correlated receipt`);
    const { request, response } = matches[0];
    const receipt = response.result?.receipt;
    if (
      request.params?.ref !== ref ||
      !request.params.clientMutationId ||
      ids.has(request.params.clientMutationId) ||
      response.error ||
      receipt?.clientMutationId !== request.params.clientMutationId ||
      receipt.disposition !== "applied" ||
      !receipt.threadId
    )
      throw new Error(`${method}: invalid mutation identity or receipt`);
    ids.add(request.params.clientMutationId);
    if (
      index < 2 &&
      JSON.stringify(request.params.input) !==
        JSON.stringify([
          {
            type: "text",
            text:
              index === 0 ? "/settings COMIC_COMPOSER_SEND literal input" : "COMIC_COMPOSER_QUEUE literal queued input",
          },
        ])
    )
      throw new Error(`${method}: changed literal input`);
    if (method === "turn/queue" && !receipt.queueEntryIds?.length)
      throw new Error("queue: missing durable entry identity");
  }
  return pairs;
}

async function composerJourney(page, fixture, { command, wireTrace, screenshot }) {
  const start = wireTrace.length;
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const records = () =>
    readFileSync(fixture.requestLog, "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
  command("background-auto", "off");
  command("background-role", "comic-composer");
  command("background-phase", "comic-composer-ready");
  await until("composer provider configured", () =>
    records().some((row) => row.event === "background-phase-ack" && row.phase === "comic-composer-ready"),
  );
  await navigateTo(page, new URL(`/s/${encodeURIComponent(fixture.refs.live)}`, fixture.baseURL).href);
  const click = async (testId) =>
    until(testId, () =>
      evaluateHere(
        `(() => { const button = document.querySelector('[data-testid="${testId}"]'); if (!button || button.disabled) return false; button.click(); return true; })()`,
      ),
    );
  const type = async (text) => {
    await until("committed full-page editor", () =>
      evaluateHere(
        `(() => { const editor = document.querySelector('[role="textbox"]'); if (!editor) return false; editor.focus(); return document.activeElement === editor; })()`,
      ),
    );
    await page.send("Input.insertText", { text });
    await until(
      "structured editor text",
      () => evaluateHere(`document.querySelector('[role="textbox"]')?.textContent`),
      (value) => value === text,
    );
  };
  const receipt = (method) =>
    until(`${method} real receipt`, () =>
      repliesSince(wireTrace, start, (request) => request.method === method).find(
        (pair) => pair.response.result?.receipt,
      ),
    );
  const provider = (text) =>
    until(`provider received ${text}`, () =>
      records().find(
        (row) =>
          row.event === "request" &&
          row.kind === "turn" &&
          row.request?.messages?.some(
            (message) => message.role === "user" && message.content?.some((part) => part.text === text),
          ),
      ),
    );
  await type("/settings COMIC_COMPOSER_SEND literal input");
  // CDP supplies a real file to the ordinary picker. The production decode and
  // source pipeline stage it; no attachment store or editor state is injected.
  const imagePath = `${fixture.evidenceDir}/mode-attachment.png`;
  writeFileSync(
    imagePath,
    Buffer.from(
      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII=",
      "base64",
    ),
  );
  const handoffs = [];
  try {
    handoffs.push(await presentation(page, "Standard"));
    const document = await page.send("DOM.getDocument");
    const input = await page.send("DOM.querySelector", {
      nodeId: document.result.root.nodeId,
      selector: 'input[type="file"]',
    });
    await page.send("DOM.setFileInputFiles", { nodeId: input.result.nodeId, files: [imagePath] });
    await until("staged attachment decoded", () =>
      evaluateHere(`!!document.querySelector('[data-testid="attachment-tile"] img')?.naturalWidth`),
    );
    for (const mode of ["Comic", "Standard", "Comic"]) handoffs.push(await presentation(page, mode));
    await nativeClick(
      page,
      "remove staged attachment",
      `document.querySelector('button[aria-label="Remove mode-attachment.png"]')`,
    );
    await until("attachment removed before provider fixture", () =>
      evaluateHere(`!document.querySelector('[data-testid="attachment-tile"]')`),
    );
  } finally {
    unlinkSync(imagePath);
  }
  await click("composer-submit");
  await receipt("turn/start");
  const sent = await provider("/settings COMIC_COMPOSER_SEND literal input");
  await until("first live streamed reply", () =>
    evaluateHere(
      `window.__comicRoot()?.querySelector('[data-comic-scroll]')?.textContent.includes('COMIC_COMPOSER_REPLY streamed first')`,
    ),
  );
  await type("COMIC_COMPOSER_QUEUE literal queued input");
  await click("composer-submit");
  await receipt("turn/queue");
  for (const mode of ["Standard", "Comic"]) handoffs.push(await presentation(page, mode));
  command("background-phase", "comic-composer-finish");
  const queued = await provider("COMIC_COMPOSER_QUEUE literal queued input");
  await until("queued turn live stream", () =>
    evaluateHere(
      `window.__comicRoot()?.querySelector('[data-comic-scroll]')?.textContent.includes('COMIC_COMPOSER_QUEUE streaming before Stop')`,
    ),
  );
  await click("composer-stop");
  await receipt("turn/interrupt");
  await until("provider cancelled by Stop", () => records().some((row) => row.event === "comic-composer-cancelled"));
  const pairs = verifyComposerReceipts(wireTrace.slice(start), fixture.refs.live);
  writeFileSync(
    `${fixture.evidenceDir}/composer.json`,
    JSON.stringify(
      { ref: fixture.refs.live, handoffs, pairs, provider: [sent, queued], wireTrace: wireTrace.slice(start) },
      null,
      2,
    ),
  );
  await screenshot("composer-stopped");
  console.log(
    "Comic composer: literal Send, live partial reply, Queue consumed by provider, Stop receipt and provider cancellation",
  );
}

export function verifyApprovalRecovery(recovery, failed) {
  for (const [phase, disabled] of [
    ["busy", true],
    ["retry", false],
  ]) {
    const state = recovery[phase];
    if (
      state?.ref !== failed.params.ref ||
      state?.disabled?.length !== 2 ||
      !state.disabled.every((value) => value === disabled)
    )
      throw Error(`approval recovery missing ${phase}`);
  }
  if (!recovery.wireTrace.some((row) => row.event === "Network.webSocketClosed"))
    throw Error("approval failure did not close native socket");
  if (
    recovery.wireTrace.some(
      (row) => row.event === "Network.webSocketFrameSent" && JSON.parse(row.frame).method === failed.method,
    )
  )
    throw Error("dropped approval reached the wire before explicit retry");
}

async function sendRecoveryJourney(page, fixture, { command, wireTrace, screenshot }) {
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const records = () =>
    readFileSync(fixture.requestLog, "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
  command("background-role", "history");
  command("background-phase", "comic-recovery-ready");
  await until("recovery provider configured", () =>
    records().some((row) => row.event === "background-phase-ack" && row.phase === "comic-recovery-ready"),
  );
  await navigateTo(page, new URL(`/s/${encodeURIComponent(fixture.refs.live)}`, fixture.baseURL).href);
  const editor = '[role="textbox"][aria-label="Message"]';
  const text = () => evaluateHere(`document.querySelector(${JSON.stringify(editor)})?.textContent`);
  const replace = async (value) => {
    await until("recovery editor focus", () =>
      evaluateHere(
        `(() => {const e=document.querySelector(${JSON.stringify(editor)});if(!e||e.closest('[inert]'))return false;e.focus();return document.activeElement===e;})()`,
      ),
    );
    await page.send("Input.dispatchKeyEvent", {
      type: "keyDown",
      key: "a",
      code: "KeyA",
      windowsVirtualKeyCode: 65,
      modifiers: 2,
    });
    await page.send("Input.dispatchKeyEvent", {
      type: "keyUp",
      key: "a",
      code: "KeyA",
      windowsVirtualKeyCode: 65,
      modifiers: 2,
    });
    await page.send("Input.insertText", { text: value });
    await until("recovery exact editor text", text, (current) => current === value);
  };
  const evidence = [];
  for (const boundary of ["before", "after"]) {
    const start = wireTrace.length;
    const marker = `COMIC_RECOVERY_${boundary}`;
    const newer = `COMIC_NEWER_DRAFT_${boundary}`;
    await replace(marker);
    await evaluateHere(
      `window.__comicRecoveryBoundary=${JSON.stringify(boundary)};window.__comicRecoveryIntercepted=undefined`,
    );
    await until("recovery submit", () =>
      evaluateHere(
        `(() => {const b=document.querySelector('[data-testid="composer-submit"]');if(!b||b.disabled)return false;b.click();return true;})()`,
      ),
    );
    const intercepted = await until("actual recovery boundary", () =>
      evaluateHere("window.__comicRecoveryIntercepted"),
    );
    if (boundary === "after" && intercepted.response?.result?.receipt?.disposition !== "applied")
      throw Error("missing actual pre-loss acceptance");
    await replace(newer);
    await evaluateHere("window.__comicCloseRecovery()");
    await until("recovery native socket closed", () =>
      wireTrace.slice(start).some((row) => row.event === "Network.webSocketClosed"),
    );
    const reloadStart = wireTrace.length;
    await reloadPage(page);
    await until("newer draft survives reload", text, (value) => value === newer);
    const pairs = await until(
      "correlated recovery receipt",
      () =>
        repliesSince(
          wireTrace,
          start,
          (request) =>
            request.method === "turn/start" &&
            request.params?.clientMutationId === intercepted.request.params.clientMutationId,
        ),
      (pairs) => pairs.some((pair) => pair.response.result?.receipt?.disposition === "applied"),
    );
    const canonical = await until("post-reload authoritative accepted input", () =>
      repliesSince(
        wireTrace,
        reloadStart,
        (request) => request.method === "thread/read" && request.params.ref === fixture.refs.live,
      )
        .flatMap((pair) => pair.response.result?.thread?.turns ?? [])
        .flatMap((turn) => turn.items ?? [])
        .find(
          (item) =>
            item.type === "userMessage" &&
            item.text === marker &&
            item.clientMutationId === intercepted.request.params.clientMutationId,
        ),
    );
    await until("real recovered provider input", () =>
      records().some(
        (row) =>
          row.event === "request" &&
          row.kind === "turn" &&
          row.request?.messages?.some(
            (message) => message.role === "user" && message.content?.some((part) => part.text === marker),
          ),
      ),
    );
    for (const mode of ["Standard", "Comic"]) {
      await presentation(page, mode);
      await until(`newer draft survives ${mode}`, text, (value) => value === newer);
    }
    const attempts = wireTrace
      .slice(start)
      .filter((row) => row.event === "Network.webSocketFrameSent")
      .map((row) => JSON.parse(row.frame))
      .filter(
        (request) => request.method === "turn/start" && request.params.input?.some((part) => part.text === marker),
      );
    if (
      attempts.length < 1 ||
      attempts.length > 2 ||
      attempts.some((request) => request.params.clientMutationId !== intercepted.request.params.clientMutationId) ||
      pairs.filter((pair) => pair.response.result?.receipt?.disposition === "applied").length !== 1 ||
      pairs.some((pair) => pair.response.error)
    )
      throw Error(`recovery did not retain one accepted mutation ${JSON.stringify({ attempts, pairs })}`);
    evidence.push({
      boundary,
      marker,
      newer,
      intercepted,
      canonical,
      attempts,
      pairs,
      wireTrace: wireTrace.slice(start),
    });
    writeFileSync(`${fixture.evidenceDir}/send-recovery.json`, JSON.stringify(evidence, null, 2));
    await screenshot(`recovery-${boundary}`);
  }
  console.log(
    "Comic send recovery: native loss before delivery and after acceptance, reload/reconnect, one mutation identity and newer draft through both clients",
  );
}

async function pendingViewportJourney(page, fixture, kind, selector, screenshot) {
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const observations = [];
  await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
  await page.send("Emulation.setSafeAreaInsetsOverride", { insets: { bottom: 34 } });
  try {
    for (const width of [1360, 760, 600, 390, 320]) {
      await page.send("Emulation.setDeviceMetricsOverride", {
        width,
        height: width <= 390 ? 620 : 900,
        deviceScaleFactor: 1,
        mobile: width <= 760,
      });
      await evaluateHere("new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))");
      const state = await evaluateHere(`(() => {
        const root=document.querySelector(${JSON.stringify(selector)});
        const rect=node=>{const r=node.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,bottom:r.bottom,right:r.right};};
        const probe=document.createElement('div');probe.style.paddingBottom='env(safe-area-inset-bottom)';document.body.append(probe);
        const inset=parseFloat(getComputedStyle(probe).paddingBottom);probe.remove();
        return {width:innerWidth,height:innerHeight,inset,reduced:matchMedia('(prefers-reduced-motion: reduce)').matches,ref:window.__comicRoot()?.dataset.sessionRef,overflow:document.documentElement.scrollWidth-innerWidth,root:root&&rect(root),text:root?.textContent,editor:document.querySelector('[role="textbox"][aria-label="Message"]')?.textContent,controls:[...(root?.querySelectorAll('button,input')??[])].map(node=>({tag:node.tagName,type:node.type,disabled:node.disabled,checked:node.checked,value:node.value,rect:rect(node)}))};
      })()`);
      observations.push(state);
      writeFileSync(
        `${fixture.evidenceDir}/interactions-${kind}-viewports.json`,
        JSON.stringify(observations, null, 2),
      );
      if (
        !state.root ||
        state.width !== width ||
        state.inset !== 34 ||
        !state.reduced ||
        state.ref !== fixture.refs.live ||
        state.overflow > 1 ||
        !state.controls.length ||
        state.controls.some((control) => control.rect.right > width + 1 || control.rect.x < -1)
      )
        throw Error(`pending ${kind} geometry ${JSON.stringify(state)}`);
      const answers = (value) => value.controls.map(({ type, checked, value }) => ({ type, checked, value }));
      if (
        state.text !== observations[0].text ||
        JSON.stringify(answers(state)) !== JSON.stringify(answers(observations[0]))
      )
        throw Error(`pending ${kind} source or answers changed on reflow`);
      // Real keyboard navigation must keep an enabled answering control reachable.
      await evaluateHere(`document.querySelector(${JSON.stringify(`${selector} button`)})?.focus()`);
      const focus = await evaluateHere(
        `(() => {const node=document.activeElement;const r=node?.getBoundingClientRect();return {inside:!!node?.closest(${JSON.stringify(selector)}),disabled:node?.disabled,bottom:r?.bottom,top:r?.top};})()`,
      );
      if (!focus.inside || focus.disabled || focus.top < 0 || focus.bottom > state.height - state.inset)
        throw Error(`pending ${kind} keyboard/safe-area ${JSON.stringify({ state, focus })}`);
      state.focus = focus;
      state.reachable = [];
      for (let step = 0; step < state.controls.length + 1; step++) {
        const reachable = await evaluateHere(
          `(() => {const root=document.querySelector(${JSON.stringify(selector)});const node=document.activeElement;const controls=[...root.querySelectorAll('button,input')];const index=controls.indexOf(node);const r=node.getBoundingClientRect();const hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {index,type:node.type,top:r.top,bottom:r.bottom,left:r.left,right:r.right,hit:node===hit||node.contains(hit)||!!node.closest('label')?.contains(hit),last:node===[...root.querySelectorAll('button')].at(-1)};})()`,
        );
        if (
          reachable.index < 0 ||
          !reachable.hit ||
          reachable.top < 0 ||
          reachable.bottom > state.height - state.inset ||
          reachable.left < 0 ||
          reachable.right > width
        )
          throw Error(`pending ${kind} unreachable keyboard control ${JSON.stringify({ state, reachable })}`);
        state.reachable.push(reachable);
        if (reachable.last) break;
        await page.send("Input.dispatchKeyEvent", {
          type: "keyDown",
          key: "Tab",
          code: "Tab",
          windowsVirtualKeyCode: 9,
        });
        await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 });
      }
      if (
        !state.reachable.at(-1)?.last ||
        (kind.includes("question") && !state.reachable.some((control) => control.type === "text"))
      )
        throw Error(`pending ${kind} note/footer not reached ${JSON.stringify(state)}`);
      const after = await evaluateHere(
        `(() => {const root=document.querySelector(${JSON.stringify(selector)});return {text:root.textContent,controls:[...root.querySelectorAll('button,input')].map(({type,checked,value})=>({type,checked,value}))};})()`,
      );
      if (after.text !== state.text || JSON.stringify(after.controls) !== JSON.stringify(answers(state)))
        throw Error(`pending ${kind} traversal changed source or answers`);
      writeFileSync(
        `${fixture.evidenceDir}/interactions-${kind}-viewports.json`,
        JSON.stringify(observations, null, 2),
      );
      await screenshot(`interactions-${kind}-${width}`);
    }
  } finally {
    await page.send("Emulation.setSafeAreaInsetsOverride", { insets: { bottom: 0 } });
    await page.send("Emulation.setEmulatedMedia", { features: [] });
    await page.send("Emulation.setDeviceMetricsOverride", {
      width: 1360,
      height: 900,
      deviceScaleFactor: 1,
      mobile: false,
    });
  }
}

async function selectAppearance(page, name, value) {
  const evaluateHere = (expression) => evaluate(page.send, expression);
  await nativeClick(
    page,
    "Appearance trigger",
    `[...window.__comicRoot().querySelectorAll('button')].find(node=>node.textContent.trim()==='Appearance')`,
  );
  await until("Appearance popup open", () =>
    evaluateHere(`!!document.querySelector('[role="dialog"][aria-label="Appearance"]')`),
  );
  await evaluateHere(
    `(() => { const popup=document.querySelector('[role="dialog"][aria-label="Appearance"]'); const r=popup.getBoundingClientRect(); if(r.left<0||r.right>innerWidth||r.top<0||r.bottom>innerHeight)throw Error('Appearance outside viewport '+JSON.stringify(r)); const label = [...popup.querySelectorAll('label')].find(node => node.textContent.startsWith(${JSON.stringify(name)})); const control = label?.control ?? label?.querySelector('select'); if (!control) throw Error('missing choice'); control.value = ${JSON.stringify(value)}; control.dispatchEvent(new Event('change', { bubbles: true })); })()`,
  );
  await page.send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "Escape",
    code: "Escape",
    windowsVirtualKeyCode: 27,
  });
  await page.send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "Escape",
    code: "Escape",
    windowsVirtualKeyCode: 27,
  });
  await until("Appearance dismissed with focus return", () =>
    evaluateHere(
      `!document.querySelector('[role="dialog"][aria-label="Appearance"]')&&window.__comicRoot().contains(document.activeElement)&&document.activeElement?.textContent.trim()==='Appearance'`,
    ),
  );
}

async function inspectActionFonts(page, fixture, name, selectors) {
  if (!fixture.requireFonts) return;
  const document = await page.send("DOM.getDocument");
  const observations = [];
  for (const selector of selectors) {
    const nodes = await page.send("DOM.querySelectorAll", {
      nodeId: document.result.root.nodeId,
      selector: `${selector}, ${selector} *`,
    });
    const fonts = [];
    for (const nodeId of nodes.result.nodeIds) {
      const result = await page.send("CSS.getPlatformFontsForNode", { nodeId });
      fonts.push(...result.result.fonts.filter((font) => font.glyphCount > 0));
    }
    observations.push({ selector, fonts });
  }
  writeFileSync(`${fixture.evidenceDir}/${name}-fonts.json`, JSON.stringify(observations, null, 2));
  if (
    observations.some(
      (observation) =>
        !observation.fonts.length ||
        observation.fonts.some((font) => !font.isCustomFont || !/Comic/.test(font.familyName)),
    )
  )
    throw Error(`${name} action text not rendered in Comic Sans ${JSON.stringify(observations)}`);
}

export function verifyRecoveryRetirement({
  trace,
  oldRef,
  connection,
  acknowledgedAt,
  backIndex,
  documentOrigin,
  currentDocumentOrigin,
}) {
  const retired = repliesSince(
    trace.slice(0, backIndex),
    0,
    (request) => request.method === "thread/unsubscribe" && request.params?.ref === oldRef,
  ).find((pair) => pair.connection === connection && !pair.response.error);
  const fenced = repliesSince(
    trace,
    backIndex,
    (request) =>
      request.method === "thread/read" && request.params?.ref === oldRef && request.params.includeTurns === true,
  ).find(
    (pair) =>
      !pair.response.error &&
      pair.response.result?.thread?.evener?.resumeRequired === true &&
      pair.response.result.thread.evener.resumeOnlyFoldable !== true,
  );
  const requestRow = (pair) =>
    trace.find(
      (row) =>
        row.event === "Network.webSocketFrameSent" &&
        row.connection === pair?.connection &&
        JSON.parse(row.frame).id === pair.request.id,
    );
  const retiredRequest = requestRow(retired),
    freshRequest = requestRow(fenced);
  if (
    !retiredRequest ||
    retiredRequest.at < acknowledgedAt ||
    !freshRequest ||
    freshRequest.at < acknowledgedAt ||
    fenced.connection !== connection ||
    documentOrigin !== currentDocumentOrigin ||
    trace.some((row) => row.event === "Network.webSocketCreated")
  )
    throw Error("invalid completed retirement and fresh recovery read");
  return { retired, retiredRequest, fenced, freshRequest };
}

export function verifyResumeAdmission({ trace, oldRef, destination, hold, ui, releaseIndex, afterUI }) {
  const sent = (method) =>
    trace.filter((row) => row.event === "Network.webSocketFrameSent" && JSON.parse(row.frame).method === method);
  const resumes = repliesSince(trace, 0, (request) => request.method === "thread/resume");
  const resumed = resumes[0];
  const thread = resumed?.response.result?.thread;
  if (
    sent("thread/resume").length !== 1 ||
    resumes.length !== 1 ||
    resumed.response.error ||
    resumed.request.params.ref !== oldRef ||
    thread?.evener.ref !== destination ||
    destination === oldRef ||
    !thread.id
  )
    throw Error("unexpected changed identity response");
  // Explicit resume intentionally reconnects. Only the pre-Send retirement
  // proof belongs to the original socket, not this request and its reply.
  const connection = resumed.connection;
  const responseRow = trace.find(
    (row) =>
      row.event === "Network.webSocketFrameReceived" &&
      row.connection === connection &&
      JSON.parse(row.frame).id === resumed.request.id,
  );
  if (
    !hold ||
    !Number.isInteger(hold.socket) ||
    hold.socket < 0 ||
    hold.requestBytes !== sent("thread/resume")[0].frame ||
    hold.responseBytes !== responseRow?.frame
  )
    throw Error("resume hold did not capture authentic native bytes");
  if (ui?.ref !== oldRef || ui.beforeText !== "COMIC_RESUME_CHANGED" || ui.text !== "" || ui.disabled !== true)
    throw Error("held resume lacks the observed post-enqueue editor transition");
  const starts = sent("turn/start");
  if (releaseIndex === undefined) {
    if (hold.releaseCount !== 0 || hold.starts.length !== 0 || starts.length !== 0)
      throw Error("premature start or response delivery while resume held");
    return { resumed };
  }
  const nativeStart = hold.starts[0];
  if (
    hold.releaseCount !== 1 ||
    hold.releasedBytes !== hold.responseBytes ||
    hold.starts.length !== 1 ||
    nativeStart.beforeRelease ||
    nativeStart.socket !== hold.socket ||
    starts.length !== 1 ||
    trace.indexOf(starts[0]) < releaseIndex ||
    nativeStart.bytes !== starts[0].frame
  )
    throw Error("resume release did not precede exactly one native start");
  const mutations = repliesSince(trace, 0, (request) => request.method === "turn/start");
  const changed = mutations[0];
  const params = changed?.request.params,
    receipt = changed?.response.result?.receipt;
  const instance = thread.evener.instanceId ?? thread.id;
  if (
    mutations.length !== 1 ||
    changed.connection !== connection ||
    changed.response.error ||
    params.ref !== destination ||
    params.expectedInstanceId !== instance ||
    !params.clientMutationId ||
    receipt?.clientMutationId !== params.clientMutationId ||
    receipt.disposition !== "applied" ||
    receipt.instanceId !== instance ||
    receipt.threadId !== thread.id ||
    JSON.stringify(params.input) !== JSON.stringify([{ type: "text", text: "COMIC_RESUME_CHANGED" }])
  )
    throw Error("changed resume mutation lacks exact input, instance fence or correlated applied receipt");
  if (afterUI?.ref !== destination || afterUI.text !== "COMIC_NEWER_DESTINATION_DRAFT")
    throw Error("resume handoff lost route or newer draft");
  return { resumed, changed };
}

async function resumeJourney(page, fixture, { wireTrace, screenshot, endpoint }) {
  const start = wireTrace.length;
  const stopped = await fetch(`${fixture.checkpointURL}/stop-root`, { method: "POST" });
  if (!stopped.ok) throw Error(`real root shutdown failed: ${await stopped.text()}`);
  await navigateTo(page, new URL(`/s/${encodeURIComponent(fixture.refs.live)}`, fixture.baseURL).href);
  const evaluateHere = (expression) => evaluate(page.send, expression);
  await until("saved root editable", () =>
    evaluateHere(
      `(() => {const e=document.querySelector('[role="textbox"][aria-label="Message"]');if(!e)return false;e.focus();return document.activeElement===e;})()`,
    ),
  );
  if (
    repliesSince(wireTrace, start, (request) => request.method === "thread/resume" || request.method === "turn/start")
      .length
  )
    throw Error("opening saved root launched a daemon");
  await page.send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "a",
    code: "KeyA",
    windowsVirtualKeyCode: 65,
    modifiers: 2,
  });
  await page.send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "a",
    code: "KeyA",
    windowsVirtualKeyCode: 65,
    modifiers: 2,
  });
  await page.send("Input.insertText", { text: "COMIC_RESUME_SAME" });
  await until("saved root Send resumes", () =>
    evaluateHere(
      `(() => {const b=document.querySelector('[data-testid="composer-submit"]');if(!b||b.disabled)return false;b.click();return true;})()`,
    ),
  );
  const applied = await until("resumed input receipt", () =>
    repliesSince(
      wireTrace,
      start,
      (request) =>
        request.method === "turn/start" && request.params.input?.some((part) => part.text === "COMIC_RESUME_SAME"),
    ).find((pair) => pair.response.result?.receipt?.disposition === "applied"),
  );
  if (
    applied.request.params.ref !== fixture.refs.live ||
    `local:${applied.response.result.receipt.instanceId}` !== fixture.refs.live
  )
    throw Error(`same-identity resume changed ref ${JSON.stringify(applied)}`);
  await until("resumed actual provider reply", () =>
    evaluateHere(
      `window.__comicRoot()?.querySelector('[data-comic-scroll]')?.textContent.includes('skillguard turn complete')`,
    ),
  );
  writeFileSync(
    `${fixture.evidenceDir}/resume.json`,
    JSON.stringify({ applied, wireTrace: wireTrace.slice(start) }, null, 2),
  );
  await screenshot("resumed-root");
  const replaceDraft = async (target, text) => {
    await target.send("Page.bringToFront");
    await until("resume composer focus", () =>
      evaluate(
        target.send,
        `(() => {const e=document.querySelector('[role="textbox"][aria-label="Message"]');if(!e)return false;e.focus();return document.activeElement===e;})()`,
      ),
    );
    await target.send("Input.dispatchKeyEvent", {
      type: "keyDown",
      key: "a",
      code: "KeyA",
      windowsVirtualKeyCode: 65,
      modifiers: 2,
    });
    await target.send("Input.dispatchKeyEvent", {
      type: "keyUp",
      key: "a",
      code: "KeyA",
      windowsVirtualKeyCode: 65,
      modifiers: 2,
    });
    await target.send("Input.insertText", { text });
    await until(
      "resume draft stored",
      () => evaluate(target.send, `document.querySelector('[role="textbox"][aria-label="Message"]')?.textContent`),
      (value) => value === text,
    );
  };
  await replaceDraft(page, "COMIC_RESUME_CHANGED");
  const clearResponse = await fetch(`${fixture.checkpointURL}/clear-and-crash-root`, { method: "POST" });
  if (!clearResponse.ok) throw Error(`real exited-process recovery acknowledgement: ${await clearResponse.text()}`);
  const preparation = await clearResponse.json();
  const acknowledgedAt = Date.now();
  const cleared = preparation.cleared;
  const destination = `local:${cleared.thread.id}`;
  if (destination === fixture.refs.live || cleared.ref !== fixture.refs.live)
    throw Error(`clear did not retain original alias with new identity ${JSON.stringify(cleared)}`);
  writeFileSync(`${fixture.evidenceDir}/resume-cleared.json`, JSON.stringify(preparation, null, 2));
  // Acknowledgement is out of band. Reopen through ordinary UI instead of
  // inventing a recovery notification or issuing a read from the driver.
  const reopenStart = wireTrace.length;
  const documentOrigin = await evaluateHere("performance.timeOrigin");
  await nativeClick(
    page,
    "normal Settings navigation reachable",
    `document.querySelector('[data-testid="rail-settings"]')`,
  );
  await until("Settings replaced the conversation pane", () =>
    evaluateHere(
      `location.pathname.startsWith('/settings') && ![...document.querySelectorAll('[data-session-actions-ref]')].some(node=>node.dataset.paneId) && !window.__comicRoot()?.querySelector('[data-comic-scroll]')`,
    ),
  );
  // The last shared transcript/activity holder, not navigation itself, owns
  // unsubscribe. Let its final read settle before Back can cancel retirement.
  const retired = await until("old root final membership retired", () =>
    repliesSince(
      wireTrace,
      reopenStart,
      (request) => request.method === "thread/unsubscribe" && request.params?.ref === fixture.refs.live,
    ).find((pair) => pair.connection === applied.connection && !pair.response.error),
  );
  const retiredRequest = wireTrace
    .slice(reopenStart)
    .find(
      (row) =>
        row.event === "Network.webSocketFrameSent" &&
        row.connection === retired.connection &&
        JSON.parse(row.frame).id === retired.request.id,
    );
  if (
    !retiredRequest ||
    retiredRequest.at < acknowledgedAt ||
    (await evaluateHere("performance.timeOrigin")) !== documentOrigin ||
    wireTrace.slice(reopenStart).some((row) => row.event === "Network.webSocketCreated")
  )
    throw Error("retirement borrowed another ref, document or connection");
  const reacquireStart = wireTrace.length;
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x: 10, y: 10, button: "back", clickCount: 1 });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: 10, y: 10, button: "back", clickCount: 1 });
  await until(
    "native Back reopens exact old ref",
    () => evaluateHere(`window.__comicRoot()?.dataset.sessionRef`),
    (value) => value === fixture.refs.live,
  );
  const fenced = await until("original connection real non-foldable recovery snapshot", () =>
    repliesSince(
      wireTrace,
      reacquireStart,
      (request) =>
        request.method === "thread/read" &&
        request.params.ref === fixture.refs.live &&
        request.params.includeTurns === true,
    ).find(
      (pair) =>
        pair.response.result?.thread?.evener?.resumeRequired === true &&
        pair.response.result.thread.evener.resumeOnlyFoldable !== true,
    ),
  );
  const freshRequest = wireTrace
    .slice(reacquireStart)
    .find(
      (row) =>
        row.event === "Network.webSocketFrameSent" &&
        row.connection === fenced.connection &&
        JSON.parse(row.frame).id === fenced.request.id,
    );
  if (
    fenced.connection !== applied.connection ||
    !freshRequest ||
    freshRequest.at < acknowledgedAt ||
    (await evaluateHere("performance.timeOrigin")) !== documentOrigin ||
    wireTrace.slice(reopenStart).some((row) => row.event === "Network.webSocketCreated")
  )
    throw Error("recovery reopen replaced the document/connection or borrowed an earlier read");
  const retirement = {
    trace: wireTrace.slice(reopenStart),
    oldRef: fixture.refs.live,
    connection: applied.connection,
    acknowledgedAt,
    backIndex: reacquireStart - reopenStart,
    documentOrigin,
    currentDocumentOrigin: await evaluateHere("performance.timeOrigin"),
  };
  verifyRecoveryRetirement(retirement);
  // This snapshot is a transport observation, not proof of store acceptance.
  // The held-resume admission check below proves the application's behavior.
  writeFileSync(
    `${fixture.evidenceDir}/resume-fenced.json`,
    JSON.stringify(
      { retired, retiredRequest, reacquireStart, fenced, acknowledgedAt, documentOrigin, freshRequest, retirement },
      null,
      2,
    ),
  );
  const browser = await connectBrowser(endpoint);
  let destinationPage, targetId;
  try {
    const created = await browser.send("Target.createTarget", { url: "about:blank" });
    targetId = created.result.targetId;
    destinationPage = await connectPage(endpoint, targetId);
    await destinationPage.send("Page.enable");
    await destinationPage.send("Runtime.enable");
    await destinationPage.send("Network.enable");
    destinationPage.ws.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      if (
        ![
          "Network.webSocketCreated",
          "Network.webSocketFrameSent",
          "Network.webSocketFrameReceived",
          "Network.webSocketClosed",
        ].includes(message.method)
      )
        return;
      const row = {
        at: Date.now(),
        event: message.method,
        connection: message.params.requestId,
        frame: message.params.response?.payloadData,
      };
      appendFileSync(`${fixture.evidenceDir}/resume-destination-wire.jsonl`, `${JSON.stringify(row)}\n`);
    });
    await navigateTo(destinationPage, new URL(`/s/${encodeURIComponent(destination)}`, fixture.baseURL).href);
    await replaceDraft(destinationPage, "COMIC_NEWER_DESTINATION_DRAFT");
  } catch (error) {
    if (destinationPage)
      writeFileSync(
        `${fixture.evidenceDir}/resume-destination-failure.json`,
        JSON.stringify(
          await evaluate(
            destinationPage.send,
            `({path:location.pathname,width:innerWidth,body:document.body.innerText.slice(0,3000),editors:[...document.querySelectorAll('[role="textbox"]')].map(node=>({label:node.getAttribute('aria-label'),editable:node.contentEditable})),owners:[...document.querySelectorAll('[data-session-actions-ref]')].map(node=>node.dataset.sessionActionsRef)})`,
          ),
          null,
          2,
        ),
      );
    throw error;
  } finally {
    destinationPage?.close();
    if (targetId) await browser.send("Target.closeTarget", { targetId });
    browser.close();
  }
  await page.send("Page.bringToFront");
  const beforeText = await evaluateHere(
    `document.querySelector('[role="textbox"][aria-label="Message"]')?.textContent`,
  );
  if (beforeText !== "COMIC_RESUME_CHANGED") throw Error("old source draft lost before explicit resume");
  const changedStart = wireTrace.length;
  await evaluateHere(`window.__comicResumeHold={ref:${JSON.stringify(fixture.refs.live)},releaseCount:0,starts:[]}`);
  await until("changed alias Send enabled", () =>
    evaluateHere(
      `(() => {const b=document.querySelector('[data-testid="composer-submit"]');if(!b||b.disabled)return false;b.click();return true;})()`,
    ),
  );
  const held = await until(
    "authentic client resume response held",
    () => evaluateHere("window.__comicResumeHold"),
    (value) => !!value?.responseBytes,
  );
  await until("client-owned changed identity resume", () =>
    repliesSince(wireTrace, changedStart, (request) => request.method === "thread/resume").find(
      (pair) => pair.response.result?.thread,
    ),
  );
  // Send resolves after durable enqueue and starts resume asynchronously. The
  // editor clearing on the old route is actual progress, not an arbitrary delay.
  const ui = await until(
    "processed Send clears old editor while resume held",
    () =>
      evaluateHere(
        `({ref:window.__comicRoot()?.dataset.sessionRef,text:document.querySelector('[role="textbox"][aria-label="Message"]')?.textContent,disabled:document.querySelector('[data-testid="composer-submit"]')?.disabled})`,
      ),
    (value) => value.disabled === true && value.text === "",
  );
  ui.beforeText = beforeText;
  const admission = {
    oldRef: fixture.refs.live,
    destination,
    ui,
    hold: await evaluateHere("window.__comicResumeHold"),
    trace: wireTrace.slice(changedStart),
  };
  writeFileSync(`${fixture.evidenceDir}/resume-held.json`, JSON.stringify(admission, null, 2));
  verifyResumeAdmission(admission);
  admission.releaseIndex = wireTrace.length - changedStart;
  await evaluateHere("window.__comicReleaseResume()");
  await until("changed alias mutation applied", () =>
    repliesSince(
      wireTrace,
      changedStart,
      (request) =>
        request.method === "turn/start" && request.params.input?.some((part) => part.text === "COMIC_RESUME_CHANGED"),
    ).find((pair) => pair.response.result?.receipt?.disposition === "applied"),
  );
  admission.afterUI = await until(
    "changed identity route and newer destination draft",
    () =>
      evaluateHere(
        `({ref:window.__comicRoot()?.dataset.sessionRef,text:document.querySelector('[role="textbox"][aria-label="Message"]')?.textContent})`,
      ),
    (value) => value.ref === destination && value.text === "COMIC_NEWER_DESTINATION_DRAFT",
  );
  admission.hold = await evaluateHere("window.__comicResumeHold");
  admission.trace = wireTrace.slice(changedStart);
  const { resumed, changed } = verifyResumeAdmission(admission);
  writeFileSync(
    `${fixture.evidenceDir}/resume-changed.json`,
    JSON.stringify({ destination, fenced, resumed, changed, held, admission, wireTrace: admission.trace }, null, 2),
  );
  console.log(
    "Comic changed resume: authentic response held, post-enqueue editor transition, zero premature starts, one released response and fenced applied mutation, newer destination draft retained",
  );
}

async function interactionJourney(page, fixture, { command, wireTrace, screenshot }) {
  const start = wireTrace.length;
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const records = () =>
    readFileSync(fixture.requestLog, "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
  const method = "evener/sandbox/escalation/resolve";
  const pairs = () => repliesSince(wireTrace, start, (request) => request.method === method);
  const frames = () =>
    wireTrace
      .slice(start)
      .filter((row) => row.frame)
      .map((row) => JSON.parse(row.frame));
  const snapshot = () =>
    evaluateHere(`(() => {
    const editor = document.querySelector('[role="textbox"][aria-label="Message"]');
    return {ref:window.__comicRoot()?.dataset.sessionRef, text:editor?.textContent, focused:document.activeElement===editor, selected:document.getSelection()?.toString(), controls:[...(window.__comicRoot()?.querySelectorAll('[data-comic-composer] button') ?? [])].map(button=>({text:button.textContent,disabled:button.disabled})), questions:window.__comicRoot()?.querySelectorAll('[data-ask-response-dock] [role="tab"]').length, approvals:document.querySelectorAll('[aria-label="Sandbox approval"]').length};
  })()`);
  const button = async (selector) =>
    until(`interaction control ${selector}`, () =>
      evaluateHere(
        `(() => {const button=document.querySelector(${JSON.stringify(selector)});if(!button||button.disabled)return false;button.click();return true;})()`,
      ),
    );
  const visibleButton = async (pattern) =>
    until(`interaction action ${pattern}`, () =>
      evaluateHere(
        `(() => {const button=[...document.querySelectorAll('button')].find(button=>new RegExp(${JSON.stringify(pattern)},'i').test(button.textContent)&&!button.disabled);if(!button)return false;button.click();return true;})()`,
      ),
    );
  const type = async (text) => {
    await until("interactive root composer", () =>
      evaluateHere(
        `(() => {const editor=document.querySelector('[role="textbox"][aria-label="Message"]');if(!editor||editor.closest('[inert]'))return false;editor.focus();return document.activeElement===editor;})()`,
      ),
    );
    await page.send("Input.insertText", { text });
    await until("interaction draft", snapshot, (value) => value.text === text);
  };
  const send = async (marker) => {
    await type(marker);
    await button('[data-testid="composer-submit"]');
  };
  command("background-role", "comic-interactions");
  command("background-phase", "comic-interactions-ready");
  await until("interaction provider configured", () =>
    records().some((row) => row.event === "background-phase-ack" && row.phase === "comic-interactions-ready"),
  );
  await inspectActionFonts(page, fixture, "composer-action", ['[data-testid="composer-submit"]']);
  await send("COMIC_INTERACTION_ASK");
  await until("real multi-question ask", () =>
    evaluateHere(
      `(() => {const dock=window.__comicRoot()?.querySelector('[data-ask-response-dock]');return dock?.querySelectorAll('[role="tab"]').length===2 && dock.querySelector('[role="tabpanel"]')?.textContent.includes('COMIC_QUESTION_SINGLE');})()`,
    ),
  );
  if (!(await evaluateHere(`window.__comicRoot()?.querySelector('[data-comic-composer] form')?.hasAttribute('inert')`)))
    throw Error("ordinary input active while answering");
  await screenshot("interactions-question");
  await pendingViewportJourney(page, fixture, "question", "[data-ask-response-dock]", screenshot);
  if (fixture.requireFonts) {
    const inspect = async () => {
      const doc = await page.send("DOM.getDocument");
      const fonts = [];
      for (const selector of ["[data-ask-question] > div:nth-child(2)", "[data-ask-response-dock] button"]) {
        const node = await page.send("DOM.querySelector", { nodeId: doc.result.root.nodeId, selector });
        const face = await page.send("CSS.getPlatformFontsForNode", { nodeId: node.result.nodeId });
        const geometry = await evaluateHere(
          `(() => {const node=document.querySelector(${JSON.stringify(selector)});const rect=node.getBoundingClientRect();const style=getComputedStyle(node);return {x:rect.x,y:rect.y,width:rect.width,height:rect.height,font:style.font};})()`,
        );
        fonts.push({ selector, geometry, ...face.result });
      }
      return fonts;
    };
    const fonts = await inspect();
    await presentation(page, "Standard");
    await until("regular-client real question", () =>
      evaluateHere(`document.querySelector('[data-ask-question]')?.textContent.includes('COMIC_QUESTION_SINGLE')`),
    );
    await evaluateHere("document.fonts.ready.then(() => true)");
    const regular = await inspect();
    await screenshot("interactions-question-regular");
    writeFileSync(`${fixture.evidenceDir}/interaction-fonts.json`, JSON.stringify({ comic: fonts, regular }, null, 2));
    if (regular.some((face) => face.fonts.some((font) => /Comic/i.test(font.familyName))))
      throw Error("Comic font leaked into regular client");
    await presentation(page, "Comic");
    await until("Comic real question restored", () =>
      evaluateHere(`document.querySelector('[data-ask-question]')?.textContent.includes('COMIC_QUESTION_SINGLE')`),
    );
    if (
      !fonts.every((face) =>
        face.fonts.some((font) => font.isCustomFont && font.glyphCount > 0 && /Comic/i.test(font.familyName)),
      )
    )
      throw Error(`question/control does not render Comic Sans: ${JSON.stringify(fonts)}`);
  }
  const option = async (text) =>
    until(`fixture option ${text}`, () =>
      evaluateHere(
        `(() => {const option=[...document.querySelectorAll('label')].find(node=>node.textContent.includes(${JSON.stringify(text)}));if(!option)return false;option.click();return true;})()`,
      ),
    );
  await option("COMIC_SINGLE_B");
  await option("COMIC_MULTI_A");
  await option("COMIC_MULTI_B");
  await pendingViewportJourney(page, fixture, "answered-question", "[data-ask-response-dock]", screenshot);
  await visibleButton("send answers");
  const answered = await until("actual provider receives both answers", () =>
    records().find(
      (row) =>
        row.event === "request" &&
        row.kind === "turn" &&
        row.request.messages.some(
          (message) =>
            message.role === "user" &&
            ["COMIC_SINGLE_B", "COMIC_MULTI_A", "COMIC_MULTI_B"].every((text) =>
              JSON.stringify(message.content).includes(text),
            ),
        ),
    ),
  );
  await until("question surface settled", snapshot, (value) => value.questions === 0);
  await until("answer restores composer focus", snapshot, (value) => value.focused);
  await screenshot("interactions-answered");
  const decisions = [];
  let recovery;
  for (const [marker, approve] of [
    ["APPROVE", true],
    ["DENY", false],
    ["LATE", true],
  ]) {
    await send(`COMIC_INTERACTION_${marker}`);
    const pending = await until(`runtime sandbox request ${marker}`, () =>
      frames().find(
        (frame) =>
          frame.method === "evener/sandbox/escalation/requested" &&
          frame.params?.deniedPath?.endsWith(`COMIC_INTERACTION_${marker}`),
      ),
    );
    const params = pending.params;
    const group = '[role="group"][aria-label="Sandbox approval"]';
    await until(
      "actual requested scope",
      () => evaluateHere(`document.querySelector(${JSON.stringify(group)})?.textContent`),
      (text) => text?.includes(params.deniedPath) && text.includes("write_file") && text.includes("workspace-write"),
    );
    if (pairs().some((pair) => pair.request.params.escalationId === params.escalationId))
      throw Error("approval submitted before explicit choice");
    if (marker === "APPROVE") {
      await screenshot("interactions-approval");
      await inspectActionFonts(page, fixture, "approval-actions", [
        `${group} [data-sandbox-escalation-allow]`,
        `${group} button:last-child`,
      ]);
      await pendingViewportJourney(page, fixture, "approval", group, screenshot);
      const failureStart = wireTrace.length;
      const controls = () =>
        evaluateHere(
          `({ref:window.__comicRoot()?.dataset.sessionRef,disabled:[...document.querySelectorAll(${JSON.stringify(`${group} button`)})].map(button=>button.disabled)})`,
        );
      await evaluateHere("window.__comicFailApproval = true");
      await button(`${group} [data-sandbox-escalation-allow]`);
      const busy = await until(
        "failed approval attempt busy",
        controls,
        (state) => state.disabled.length === 2 && state.disabled.every((value) => value === true),
      );
      await until("actual approval send intercepted", () =>
        evaluateHere('typeof window.__comicCloseApproval === "function"'),
      );
      await evaluateHere("window.__comicCloseApproval(); window.__comicCloseApproval = undefined");
      await until("failed approval native socket closed", () =>
        wireTrace.slice(failureStart).some((row) => row.event === "Network.webSocketClosed"),
      );
      const retry = await until(
        "approval retry re-enabled",
        controls,
        (state) => state.disabled.length === 2 && state.disabled.every((value) => value === false),
      );
      recovery = { busy, retry, wireTrace: wireTrace.slice(failureStart) };
      verifyApprovalRecovery(recovery, await evaluateHere("window.__comicFailedApproval"));
      await until("native transport reconnected", () =>
        evaluateHere("window.__comicSockets.some(socket=>socket.readyState===WebSocket.OPEN)"),
      );
    }
    if (marker === "LATE") await evaluateHere("window.__comicHoldApproval = true");
    if (approve) await button(`${group} [data-sandbox-escalation-allow]`);
    else await visibleButton("^Deny$");
    const pair = await until(`correlated decision ${marker}`, () =>
      pairs().find((pair) => pair.request.params.escalationId === params.escalationId),
    );
    if (pair.request.params.ref !== fixture.refs.live || pair.request.params.approve !== approve || pair.response.error)
      throw Error(`decision mismatch ${JSON.stringify(pair)}`);
    decisions.push({ marker, pending, pair });
    if (marker === "LATE") {
      await until("actual response withheld", () =>
        evaluateHere('typeof window.__comicReleaseApproval === "function"'),
      );
      if (
        !(await evaluateHere(
          `[...document.querySelectorAll(${JSON.stringify(`${group} button`)})].every(button=>button.disabled)`,
        ))
      )
        throw Error("pending approval controls not busy");
      await evaluateHere(
        `history.pushState(null,'','/s/'+encodeURIComponent(${JSON.stringify(fixture.authorshipRef)})); window.dispatchEvent(new PopStateEvent('popstate'));`,
      );
      await until("second actual root selected", snapshot, (value) => value.ref === fixture.authorshipRef);
      await type("COMIC_SECOND_ROOT_DRAFT");
      // Hydration and the editor's draft projection must settle before the
      // baseline, otherwise B's own readiness looks like an A response effect.
      await until("second root send ready before A settlement", () =>
        evaluateHere('document.querySelector("[data-testid=composer-submit]")?.disabled === false'),
      );
      await evaluateHere(
        `(() => {const editor=document.querySelector('[role="textbox"][aria-label="Message"]');const range=document.createRange();range.setStart(editor.firstChild,1);range.setEnd(editor.firstChild,5);const selection=document.getSelection();selection.removeAllRanges();selection.addRange(range);})()`,
      );
      const before = await snapshot();
      await evaluateHere("window.__comicReleaseApproval(); window.__comicReleaseApproval = undefined");
      await evaluateHere("new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))");
      const after = await snapshot();
      if (JSON.stringify(before) !== JSON.stringify(after))
        throw Error(`late original-ref approval changed B ${JSON.stringify({ before, after })}`);
      decisions.at(-1).secondRoot = { before, after };
      await screenshot("interactions-late-second-root");
    } else await until("decision surface settled", snapshot, (value) => value.approvals === 0);
    // Real tool follow-up, not a DOM-only disappearance, proves runtime progress.
    await until(`tool result reaches provider ${marker}`, () =>
      records().some(
        (row) =>
          row.event === "request" &&
          row.kind === "turn" &&
          row.request.messages.some((message) =>
            message.content.some(
              (part) => part.tool_result?.tool_call_id === `comic_interaction_COMIC_INTERACTION_${marker}`,
            ),
          ),
      ),
    );
  }
  const actual = frames().filter((frame) => frame.method === method);
  if (
    actual.length !== 3 ||
    new Set(actual.map((frame) => frame.params.escalationId)).size !== 3 ||
    actual.some((frame) => frame.params.ref !== fixture.refs.live)
  )
    throw Error(`approval replay or wrong root ${JSON.stringify(actual)}`);
  const failed = await evaluateHere("window.__comicFailedApproval");
  if (
    failed.params.escalationId !== decisions[0].pending.params.escalationId ||
    failed.params.ref !== fixture.refs.live ||
    failed.params.approve !== true
  )
    throw Error("retry changed original request identity");
  writeFileSync(
    `${fixture.evidenceDir}/interactions.json`,
    JSON.stringify({ answered, decisions, failed, recovery, wireTrace: wireTrace.slice(start) }, null, 2),
  );
  console.log(
    "Comic root interactions: real batch answered, explicit approve after transport failure, explicit decline, original-ref late approval leaves second root unchanged, no replay",
  );
}

export function savedSourcePlaced(geometry, expected) {
  const sameSource = (value) =>
    value &&
    value.sessionRef === expected.sessionRef &&
    JSON.stringify(value.path) === JSON.stringify(expected.path) &&
    value.entryId === expected.entryId &&
    value.offset === expected.offset;
  return !!(
    expected?.mode === "away" &&
    geometry?.saved?.mode === "away" &&
    sameSource(geometry.saved) &&
    geometry.saved.viewportOffset === expected.viewportOffset &&
    sameSource(geometry.point) &&
    Math.abs(geometry.point.y - expected.viewportOffset) <= 2
  );
}

export async function sampleCachedReloadRecovery(readTrace, observe, parameters) {
  const trace = readTrace();
  const observation = await observe();
  const relevant = (rows) => {
    const ids = new Set();
    return rows.filter((row) => {
      if (row.at < parameters.documentOrigin) return false;
      if (["Network.webSocketCreated", "Network.webSocketClosed"].includes(row.event)) return true;
      if (!row.frame) return false;
      const frame = JSON.parse(row.frame),
        key = JSON.stringify([row.connection, frame.id]);
      if (
        row.event === "Network.webSocketFrameSent" &&
        ["thread/read", "thread/turns/list"].includes(frame.method) &&
        frame.params.ref === parameters.ref
      ) {
        ids.add(key);
        return true;
      }
      return row.event === "Network.webSocketFrameReceived" && ids.has(key);
    });
  };
  const wireUnchanged = JSON.stringify(relevant(trace)) === JSON.stringify(relevant(readTrace()));
  const evidence = cachedReloadRecovery({ trace, ...observation, ...parameters });
  // Never append later replies to an older DOM observation. The next ordinary
  // until sample will observe the application after its own frozen replies.
  return {
    documentOrigin: parameters.documentOrigin,
    ...observation,
    ...evidence,
    wireUnchanged,
    ready: evidence.ready && wireUnchanged,
  };
}

export function recordReloadEvents(ref) {
  window.__comicReloadEvents = [];
  window.__comicReloadInputSeen = false;
  const inputTypes = [
    "wheel",
    "pointerdown",
    "pointerup",
    "pointercancel",
    "keydown",
    "touchstart",
    "touchmove",
    "touchend",
  ];
  for (const type of ["scroll", "focusin", "selectionchange", ...inputTypes]) {
    document.addEventListener(
      type,
      (event) => {
        if (inputTypes.includes(event.type)) window.__comicReloadInputSeen = true;
        const port = window.__comicRoot()?.querySelector("[data-comic-scroll]")?.firstElementChild;
        window.__comicReloadEvents.push({
          at: performance.now(),
          type: event.type,
          trusted: event.isTrusted,
          scrollTop: port?.scrollTop,
          selectionInPort: !!port?.contains(document.getSelection()?.anchorNode),
          saved: JSON.parse(sessionStorage.getItem(`evener:comic:position:${ref}`) ?? "null")?.position,
        });
        if (window.__comicReloadEvents.length > 100) window.__comicReloadEvents.shift();
      },
      { capture: true, passive: true },
    );
  }
}

export function cachedReloadRecovery({
  trace,
  documentOrigin,
  currentDocumentOrigin,
  ref,
  geometry,
  expected,
  intent,
  inputSeen,
}) {
  const connections = trace.filter((row) => row.event === "Network.webSocketCreated" && row.at >= documentOrigin);
  const connection = connections.length === 1 ? connections[0].connection : undefined;
  const requests = [];
  const byID = new Map();
  for (const [index, row] of trace.entries()) {
    if (!connection || row.connection !== connection || row.at < documentOrigin || !row.frame) continue;
    const frame = JSON.parse(row.frame);
    if (
      row.event === "Network.webSocketFrameSent" &&
      ["thread/read", "thread/turns/list"].includes(frame.method) &&
      frame.params.ref === ref
    ) {
      const request = { connection, request: frame, requestIndex: index };
      requests.push(request);
      byID.set(frame.id, request);
    } else if (row.event === "Network.webSocketFrameReceived" && byID.has(frame.id)) {
      Object.assign(byID.get(frame.id), { response: frame, replyIndex: index });
    }
  }
  const success = (pair) => !!pair?.response?.result && !pair.response.error;
  const hydration = requests.findLast(
    (pair) => pair.request.method === "thread/read" && pair.request.params.includeTurns === true,
  );
  // A reply plus cached placement can precede application of the fresh window.
  // This fixture drops the saved older body, so ensuing page demand witnesses
  // that application. Await every issued child read/page, not a fixed page count.
  const pages = requests.filter(
    (pair) => pair.request.method === "thread/turns/list" && pair.requestIndex > hydration?.replyIndex,
  );
  const noInput =
    !inputSeen &&
    intent.every(
      (event) =>
        ![
          "wheel",
          "pointerdown",
          "pointerup",
          "pointercancel",
          "keydown",
          "touchstart",
          "touchmove",
          "touchend",
        ].includes(event.type),
    );
  const ready =
    currentDocumentOrigin === documentOrigin &&
    !!connection &&
    !trace.some((row) => row.event === "Network.webSocketClosed" && row.connection === connection) &&
    success(hydration) &&
    pages.length > 0 &&
    requests.every(success) &&
    noInput &&
    savedSourcePlaced(geometry, expected);
  return { ready, connection, hydration, pages, requests, noInput };
}

export function verifyPanelWidths(geometry, name, requirePaired = false) {
  const cap = 26 * geometry.rootFontSize;
  if (requirePaired && !geometry.paired)
    throw Error(`${name}: real paired row not mounted ${JSON.stringify(geometry)}`);
  for (const [composition, measured] of Object.entries({ single: geometry.single, paired: geometry.paired })) {
    if (!measured) continue;
    if (
      measured.panel > cap + 1 ||
      measured.panel > geometry.availableWidth + 1 ||
      (geometry.availableWidth > cap + 1 && measured.panel >= geometry.availableWidth) ||
      Math.abs(measured.panel - measured.probe) > 1
    )
      throw Error(`${name}: ${composition} panel/probe width exceeds 26rem or disagrees ${JSON.stringify(geometry)}`);
    if (Math.abs(measured.left - geometry.readerLeft) > 1)
      throw Error(`${name}: ${composition} panel lost the left reading edge ${JSON.stringify(geometry)}`);
  }
  if (geometry.availableWidth > cap + 1 && geometry.single.right >= geometry.readerRight)
    throw Error(`${name}: single panel stretched into its gutter ${JSON.stringify(geometry)}`);
  if (
    geometry.paired &&
    geometry.width > 760 &&
    Math.abs(geometry.paired.secondLeft - geometry.paired.firstRight - 15) > 1
  )
    throw Error(`${name}: paired panels lost their 15px gap ${JSON.stringify(geometry)}`);
  if (
    geometry.width <= 760 &&
    (geometry.single.right > geometry.readerRight + 1 ||
      (geometry.paired && geometry.paired.right > geometry.readerRight + 1))
  )
    throw Error(`${name}: phone panel outside reader ${JSON.stringify(geometry)}`);
}

export function verifyCanonicalErrorMounted(value, entryId, name) {
  if (!value.mounted || !value.parts.some((part) => part.entryId === entryId))
    throw Error(`${name}: canonical item-backed error missing at mounted checkpoint`);
  if (value.errorGroups.length)
    throw Error(`${name}: canonical item-backed error misgrouped ${JSON.stringify(value.errorGroups)}`);
}

export function collectSourceSlices(covered, parts, sourceText) {
  const seen = new Set();
  for (const part of parts) {
    if (seen.has(part.start)) throw Error("literal source slice duplicated in virtual row");
    seen.add(part.start);
    const previous = covered.get(part.start);
    if (previous && previous.rowId !== part.rowId)
      throw Error("literal source range assigned to different virtual row");
    const raw = sourceText.slice(part.start, part.end);
    const expected = part.formatted
      ? `${inlineFixtureGlyphs(raw)
          .map((glyph) => glyph.char)
          .join("")}\n`
      : raw;
    if (part.text !== expected || (previous && previous.end !== part.end))
      throw Error("literal source slice changed during virtual traversal");
    covered.set(part.start, part);
  }
}

// Only the inline markup authored in the original history fixtures. This is
// deliberately not a Markdown parser and imports no production source mapper.
export function inlineFixtureGlyphs(source) {
  const hidden = new Set();
  for (const match of source.matchAll(/\*\*([^*\n]+)\*\*|\*([^*\n]+)\*|`([^`\n]+)`/g)) {
    const size = match[0].startsWith("**") ? 2 : 1;
    for (let index = 0; index < size; index++) {
      hidden.add(match.index + index);
      hidden.add(match.index + match[0].length - 1 - index);
    }
  }
  return Array.from({ length: source.length }, (_, index) => ({ char: source[index], source: index })).filter(
    (glyph) => !hidden.has(glyph.source),
  );
}

export function verifyFormattedObservation(value, expected) {
  if (value.overflow > 1 || JSON.stringify(value.kinds) !== JSON.stringify(["prose", "code", "table", "prose"]))
    throw Error("formatted source order or viewport overflow");
  if (expected.source !== undefined) {
    const code = value.parts.filter((part) => part.kind === "code");
    const table = value.parts.filter((part) => part.kind === "table");
    if (
      value.parts[0]?.start !== 0 ||
      value.parts.at(-1)?.end !== expected.source.length ||
      value.parts.some(
        (part, index) => part.end <= part.start || (index > 0 && value.parts[index - 1].end !== part.start),
      ) ||
      code.length !== 1 ||
      code[0].start !== expected.source.indexOf("```text") ||
      code[0].end !== expected.tableStart ||
      table.length !== 1 ||
      table[0].start !== expected.tableStart ||
      table[0].end !== expected.proseStart
    )
      throw Error("formatted original source ranges shifted or duplicated");
  }
  const semantics = value.semantics;
  if (
    !semantics.heading ||
    !semantics.strong ||
    !semantics.emphasis ||
    !semantics.quote ||
    semantics.listStart !== 7 ||
    !semantics.inlineCode ||
    !semantics.safeLink ||
    semantics.unsafeLinks
  )
    throw Error("formatted semantic elements or link safety");
  if (value.code.text !== expected.code || JSON.stringify(value.table.cells) !== JSON.stringify(expected.cells))
    throw Error("technical source whitespace or cell content changed");
  for (const block of [value.code, value.table]) {
    if (
      block.stage ||
      block.width > Math.min(40 * value.rootFontSize, value.availableWidth) + 1 ||
      block.scrollHeight > block.clientHeight + 1 ||
      block.size < 12
    )
      throw Error("technical insert stage, size or vertical clipping");
    if (block.scrollWidth > block.clientWidth + 1 && block.scrollLeft <= 0)
      throw Error("technical overflow is not horizontally readable");
  }
}

export function observeFormattedStream() {
  const port = window.__comicRoot()?.querySelector("[data-comic-scroll]")?.firstElementChild;
  const parts = [...(port?.querySelectorAll("[data-comic-entry]") ?? [])];
  const code = parts.find((node) => node.querySelector("pre code")?.textContent.includes("COMIC_STREAM_CODE"));
  if (!code) return null;
  const ids = new Set(
    parts
      .filter((node) => /COMIC_STREAM_(BEFORE|CODE|END|CELL|AFTER)\b/.test(node.textContent))
      .map((node) => node.dataset.comicEntry),
  );
  const same = parts.filter((node) => ids.has(node.dataset.comicEntry));
  return {
    id: code.dataset.comicEntry,
    code: code.querySelector("pre code").textContent,
    cells: same.flatMap((node) => [...node.querySelectorAll("th,td")].map((cell) => cell.textContent)),
    after: same.some((node) => node.querySelector("em")?.textContent === "done"),
    literalFence: same.some((node) => node.textContent.includes(String.fromCharCode(96).repeat(3))),
    blocks: same.map((node) => ({
      id: node.dataset.comicEntry,
      start: +node.dataset.comicStart,
      end: +node.dataset.comicEnd,
      kind: node.querySelector("[data-comic-markdown]")?.dataset.comicMarkdown,
      prose: [...node.querySelectorAll('[data-comic-markdown="prose"] p')].map((p) => p.textContent),
      codes: [...node.querySelectorAll("pre code")].map((code) => code.textContent),
      cells: [...node.querySelectorAll("th,td")].map((cell) => cell.textContent),
    })),
  };
}

// These are the authored provider chunks, not the production part mapper.
const streamPrefix = "COMIC_STREAM_BEFORE **bold**\n\n";
const streamPartial = `${streamPrefix}\`\`\`text\n  COMIC_STREAM_CODE`;
const streamTable = `${streamPartial}\n\tCOMIC_STREAM_END  \n\`\`\`\n\n| key | value |\n| --- | --- |\n| COMIC_STREAM_CELL |`;
const streamComplete = `${streamTable} **settled** |\n\nCOMIC_STREAM_AFTER *done*`;
function expectedStreamBlocks(source, id) {
  const table = source.indexOf("| key"),
    after = source.indexOf("COMIC_STREAM_AFTER");
  const block = (start, end, kind, prose = [], codes = [], cells = []) => ({
    id,
    start,
    end,
    kind,
    prose,
    codes,
    cells,
  });
  const result = [
    block(0, streamPrefix.length, "prose", ["COMIC_STREAM_BEFORE bold"]),
    block(
      streamPrefix.length,
      table < 0 ? source.length : table,
      "code",
      [],
      [table < 0 ? "  COMIC_STREAM_CODE" : "  COMIC_STREAM_CODE\n\tCOMIC_STREAM_END  "],
    ),
  ];
  if (table >= 0)
    result.push(
      block(
        table,
        after < 0 ? source.length : after,
        "table",
        [],
        [],
        ["key", "value", "COMIC_STREAM_CELL", after < 0 ? "" : "settled"],
      ),
    );
  if (after >= 0) result.push(block(after, source.length, "prose", ["COMIC_STREAM_AFTER done"]));
  return result;
}

export function verifyCreationChronology(value, completed) {
  if (!(value.before >= 0 && value.launch > value.before && value.after > value.launch))
    throw Error("parent messages do not surround actual launch");
  if (completed) {
    verifyCreationChronology(completed);
    for (const key of ["before", "launch", "after"]) {
      if (value.entries[value[key]]?.id !== completed.entries[completed[key]]?.id)
        throw Error("completion changed launch neighbors or strip identity");
    }
  }
}

export function observeDelegateRelocation(parentRef, childRef, origin) {
  const port = window.__comicRoot().querySelector("[data-comic-scroll]").firstElementChild;
  const heading = [...port.querySelectorAll("[data-comic-delegate]")].find(
    (node) => node.dataset.comicChildRef === childRef,
  );
  const strip = heading?.closest('[data-comic-flow="strip"]');
  const path = strip?.dataset.comicPath;
  const owned = (node) => node.dataset.comicSession === parentRef && node.dataset.comicPath === path;
  // Group labels and strips are sibling virtual rows, never ancestors. Ignore
  // nested readers while following the owning reader's most recent group/row.
  const rows = [...port.querySelectorAll("[data-comic-flow], [data-comic-row]")].filter(owned);
  let unplaced = false;
  for (const row of rows) {
    if (row === strip) break;
    if (row.dataset.comicFlow === "unplaced-group") unplaced = true;
    else if (row.dataset.comicFlow !== "strip") unplaced = false;
  }
  const entries = [...port.querySelectorAll("[data-comic-entry]")].filter(owned);
  const index = entries.findIndex((node) => node.contains(heading));
  const beforeId = entries[index - 1]?.dataset.comicEntry;
  const afterId = entries[index + 1]?.dataset.comicEntry;
  return {
    found: !!heading,
    unplaced,
    beforeId,
    afterId,
    atLaunch: !!strip && !unplaced && beforeId === origin.beforeId && afterId === origin.afterId,
    entries: entries.map((node) => ({ id: node.dataset.comicEntry, text: node.textContent })),
  };
}

export function recordedRelocationOrigin(held, parentRef, grandchild) {
  const items = held.response.result.data.flatMap((turn) => turn.items);
  const launchIndex = items.findIndex((item) => item.callId === "comic_delegate_GRANDCHILD");
  const creation = items[launchIndex];
  const before = items[launchIndex - 1];
  const after = items[launchIndex + 1];
  const receipt = creation?.output && JSON.parse(creation.output);
  if (
    held.request.params.ref !== parentRef ||
    creation?.toolName !== "delegate" ||
    receipt?.transcript_ref !== grandchild.ref ||
    !grandchild.key.includes(receipt.delegate_id) ||
    before?.type !== "userMessage" ||
    before.text !== "COMIC_SUBCHAT_CHILD" ||
    after?.type !== "agentMessage" ||
    !after.text.startsWith("COMIC_CHILD_SOURCE_00 ") ||
    !(before.position.entry < creation.position.entry && creation.position.entry < after.position.entry)
  )
    throw Error("missing recorded launch and neighboring source entries");
  return {
    creationId: creation.id,
    beforeId: before.id,
    afterId: after.roundId ? `round:${after.roundId}:agentMessage` : after.id,
    receipt,
  };
}

export function verifyDelegateRelocation(value) {
  if (value.relocated.key !== value.grandchild.key || value.relocated.expanded !== "true")
    throw Error("causal placement replaced expanded nested identity");
  if (value.lifetime.length) throw Error("late creation relocation replaced the existing child reader");
  if (!value.relocation.found || value.relocation.unplaced || !value.relocation.atLaunch)
    throw Error("late launch not between its recorded source neighbors");
  if (!savedSourcePlaced(value.before, value.expected) || !savedSourcePlaced(value.after, value.expected))
    throw Error("late launch changed the pinned source glyph or viewport offset");
}

export function verifyFormattedStream(value) {
  if (
    value.ids.length !== 3 ||
    !value.ids[0] ||
    new Set(value.ids).size !== 1 ||
    value.partialCode !== "  COMIC_STREAM_CODE" ||
    value.code !== "  COMIC_STREAM_CODE\n\tCOMIC_STREAM_END  " ||
    JSON.stringify(value.cells) !== JSON.stringify(["key", "value", "COMIC_STREAM_CELL", "settled"]) ||
    !value.after ||
    value.literalFence
  )
    throw Error("streamed Markdown source or completion changed");
  for (const [phase, source] of [
    ["partial", streamPartial],
    ["table", streamTable],
    ["final", streamComplete],
    ["settled", streamComplete],
  ]) {
    const snapshot = value.snapshots?.[phase];
    if (!snapshot || JSON.stringify(snapshot.blocks) !== JSON.stringify(expectedStreamBlocks(source, value.ids[0])))
      throw Error(`streamed Markdown ${phase} content, identity or source ranges changed: ${JSON.stringify(snapshot)}`);
  }
}

export function verifyBranchRetirement(value) {
  if (
    !value.mountedCollapsed ||
    !value.siblingExpanded ||
    !value.retired.includes(value.collapsed) ||
    value.retired.includes(value.sibling) ||
    value.reread.includes(value.sibling)
  )
    throw Error("mounted collapse did not retire only its branch");
}

async function formattedStreamJourney(page, fixture, command, wireTrace) {
  const here = (expression) => evaluate(page.send, expression);
  command("background-auto", "off");
  command("background-role", "comic-formatted");
  command("background-start-turn", "COMIC_FORMATTED_STREAM");
  await until("latest for streamed Markdown", () =>
    here(
      `(() => {const button=window.__comicRoot()?.querySelector('button[aria-label="Jump to latest"]');if(!button)return false;button.click();return true;})()`,
    ),
  );
  const read = () => here(`(${observeFormattedStream.toString()})()`);
  const partial = await until("unfinished streamed code", read, (value) => value?.code === "  COMIC_STREAM_CODE");
  command("background-phase", "comic-formatted-code");
  const table = await until("streamed partial table", read, (value) => value?.cells.includes("COMIC_STREAM_CELL"));
  command("background-phase", "comic-formatted-finish");
  const final = await until("streamed complete table and prose", read, (value) => value?.after);
  await until("formatted stream producer completion", () =>
    readFileSync(fixture.requestLog, "utf8")
      .split("\n")
      .some((line) => {
        try {
          const row = JSON.parse(line);
          return row.event === "turn-complete" && row.prompt === "COMIC_FORMATTED_STREAM" && row.ok;
        } catch {
          return false;
        }
      }),
  );
  const canonical = await until("canonical streamed source and completed turn", () => {
    const updates = wireTrace
      .filter((row) => row.event === "Network.webSocketFrameReceived")
      .map((row) => JSON.parse(row.frame))
      .filter((frame) => frame.method === "history/updated" && frame.params?.ref === fixture.refs.live);
    const item = updates
      .flatMap((frame) => frame.params.items ?? [])
      .find((item) => item.type === "agentMessage" && item.text === streamComplete && item.status === "completed");
    const turn =
      item &&
      updates
        .flatMap((frame) => frame.params.turns ?? [])
        .find((turn) => turn.id === item.turnId && turn.status === "completed");
    return turn ? { item, turn } : null;
  });
  await here("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
  const settled = await read();
  const value = {
    ids: [partial.id, table.id, final.id],
    partialCode: partial.code,
    ...final,
    snapshots: { partial, table, final, settled },
  };
  writeFileSync(
    `${fixture.evidenceDir}/formatted-stream.json`,
    JSON.stringify(
      {
        partial,
        table,
        final,
        settled,
        canonical,
        expected: { partial: streamPartial, table: streamTable, final: streamComplete },
        value,
      },
      null,
      2,
    ),
  );
  verifyFormattedStream(value);
  command("background-role", "history");
}

function formattedObservation(entryId) {
  const root = window.__comicRoot(),
    port = root.querySelector("[data-comic-scroll]").firstElementChild;
  const entries = [...port.querySelectorAll("[data-comic-entry]")].filter(
    (node) => node.dataset.comicEntry === entryId,
  );
  const parts = entries.map((node) => ({
    start: +node.dataset.comicStart,
    end: +node.dataset.comicEnd,
    kind: node.querySelector("[data-comic-markdown]")?.dataset.comicMarkdown,
  }));
  const kinds = parts.map((part) => part.kind).filter((kind, index, all) => kind !== all[index - 1]);
  const find = (selector) => entries.flatMap((node) => [...node.querySelectorAll(selector)]);
  const code = find("pre code")[0],
    table = find("table")[0];
  if (
    !code ||
    !table ||
    !find("h2").some((node) => node.textContent === "COMIC_FORMATTED_HEADING") ||
    !find("strong").some((node) => node.textContent === "settled")
  )
    return null;
  const block = (node) => {
    const content = node.closest("[data-comic-text]"),
      article = node.closest("article");
    content.scrollLeft = content.scrollWidth;
    const result = {
      width: article.getBoundingClientRect().width,
      height: article.getBoundingClientRect().height,
      clientHeight: content.clientHeight,
      scrollHeight: content.scrollHeight,
      clientWidth: content.clientWidth,
      scrollWidth: content.scrollWidth,
      scrollLeft: content.scrollLeft,
      stage: !!article.querySelector("[data-comic-scene],[data-comic-balloon],img"),
      size: parseFloat(getComputedStyle(node).fontSize),
      family: getComputedStyle(node).fontFamily,
    };
    content.scrollLeft = 0;
    return result;
  };
  return {
    width: innerWidth,
    visualWidth: visualViewport.width,
    scrollTop: port.scrollTop,
    scrollHeight: port.scrollHeight,
    clientHeight: port.clientHeight,
    mounted: port.querySelectorAll("[data-comic-entry]").length,
    rootFontSize: parseFloat(getComputedStyle(document.documentElement).fontSize),
    availableWidth: entries[0].closest("[data-comic-row]").getBoundingClientRect().width,
    overflow: Math.max(document.documentElement.scrollWidth - innerWidth, port.scrollWidth - port.clientWidth),
    parts,
    kinds,
    semantics: {
      heading: find("h2").some((node) => node.textContent === "COMIC_FORMATTED_HEADING"),
      strong: find("strong").some((node) => node.textContent === "bold"),
      emphasis: find("em").some((node) => node.textContent === "emphasis"),
      quote: find("blockquote").length === 1,
      listStart: find("ol")[0]?.start,
      inlineCode: find("p code").some((node) => node.textContent === "inline"),
      safeLink: find("a").some((node) => node.href === "https://example.com/comic" && node.rel.includes("noopener")),
      unsafeLinks: find("a").filter((node) => /^javascript:/i.test(node.getAttribute("href") ?? "")).length,
    },
    code: { ...block(code), text: code.textContent },
    table: { ...block(table), cells: [...table.querySelectorAll("th,td")].map((node) => node.textContent) },
  };
}

// Locate fixture-authored text directly in actual DOM nodes. Offsets come from
// the untouched fixture, not data-comic-start or the product's binding cache.
function formattedGlyph(entryId, selector, literal, sourceOffset, local = 0) {
  const port = window.__comicRoot()?.querySelector("[data-comic-scroll]")?.firstElementChild;
  if (!port) return null;
  const entry = [...port.querySelectorAll("[data-comic-entry]")].find(
    (node) =>
      node.dataset.comicEntry === entryId &&
      [...node.querySelectorAll(selector)].some((element) => element.textContent.includes(literal)),
  );
  const element = entry && [...entry.querySelectorAll(selector)].find((node) => node.textContent.includes(literal));
  if (!element) return null;
  const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
  let remaining = element.textContent.indexOf(literal) + local;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (remaining >= node.length) {
      remaining -= node.length;
      continue;
    }
    const range = document.createRange();
    range.setStart(node, remaining);
    range.setEnd(node, remaining + 1);
    const rect = range.getBoundingClientRect();
    const whole = document.createRange();
    whole.selectNodeContents(element);
    const lines = [...new Set([...whole.getClientRects()].filter((rect) => rect.width > 0).map((rect) => rect.top))];
    return {
      sourceOffset: sourceOffset + local,
      glyph: range.toString(),
      nodeText: node.data,
      nodeOffset: remaining,
      parent: node.parentElement.tagName,
      connected: node.isConnected,
      lines,
      whiteSpace: getComputedStyle(element).whiteSpace,
      elementWidth: element.getBoundingClientRect().width,
      tableRow: element.closest("tr")?.rowIndex ?? null,
      rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
      y: rect.top - port.getBoundingClientRect().top,
    };
  }
  return null;
}

async function formattedJourney(
  page,
  fixture,
  { command, complete, outgoing, screenshot, wireTrace, holdFonts, releaseFonts },
) {
  const firstRequest = outgoing.length;
  await formattedStreamJourney(page, fixture, command, wireTrace);
  await complete(fixture.formattedSource);
  for (let index = 0; index < 45; index++) await complete(`COMIC_FORMATTED_TAIL_${index}`);
  const here = (expression) => evaluate(page.send, expression);
  let direction = -1;
  const entryId = await until("formatted fixture source mounted", () =>
    here(`(() => {
    const port=window.__comicRoot().querySelector('[data-comic-scroll]').firstElementChild;
    const entry=[...port.querySelectorAll('[data-comic-entry]')].find(node=>node.textContent.includes('COMIC_FORMATTED_HEADING'));
    if(entry)return entry.dataset.comicEntry;
    port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:-400}));port.scrollTop-=400;return null;
  })()`),
  );
  const observations = [];
  for (const width of [1360, 760, 600, 390, 320, 1360]) {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width,
      height: 900,
      deviceScaleFactor: 1,
      mobile: width <= 390,
    });
    await here("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const value = await until(`complete formatted blocks at ${width}`, async () => {
      const value = await here(`(${formattedObservation.toString()})(${JSON.stringify(entryId)})`);
      if (value) return value;
      const boundary = await here(
        `(() => {const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;if(!port)return false;const deltaY=${direction}*250;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));port.scrollTop+=deltaY;return port.scrollTop<=1||port.scrollTop+port.clientHeight>=port.scrollHeight-1;})()`,
      );
      if (boundary) direction *= -1;
      return null;
    });
    writeFileSync(`${fixture.evidenceDir}/formatted-checkpoint.json`, JSON.stringify({ width, value }, null, 2));
    verifyFormattedObservation(value, {
      code: fixture.formattedCode,
      cells: fixture.formattedCells,
      source: fixture.formattedSource,
      tableStart: fixture.formattedSource.indexOf("| COMIC_TABLE_KEY"),
      proseStart: fixture.formattedSource.indexOf("COMIC_FORMATTED_AFTER"),
    });
    if (value.width !== width || Math.abs(value.visualWidth - width) > 1)
      throw Error("actual mobile viewport not realized");
    const glyphs = [];
    for (const [selector, literal, local] of [
      ["strong", "bold", 0],
      ["p", "A   collapsed gap.", 4],
      ["pre code", "COMIC_CODE_TAB", 3],
      ["td", "COMIC_CELL_WIDE", 2],
    ]) {
      const offset = fixture.formattedSource.indexOf(literal);
      const glyph = await until(`mounted formatted glyph ${selector}`, () =>
        here(
          `(${formattedGlyph.toString()})(${JSON.stringify(entryId)},${JSON.stringify(selector)},${JSON.stringify(literal)},${offset},${local})`,
        ),
      );
      if (!glyph?.connected || glyph.glyph !== fixture.formattedSource[offset + local] || glyph.rect.height <= 0)
        throw Error(`fixture glyph missing or wrong ${JSON.stringify({ selector, literal, offset, glyph })}`);
      if (selector === "td" && glyph.tableRow !== 2)
        throw Error(`table fixture did not expose its second body row ${JSON.stringify(glyph)}`);
      glyphs.push(glyph);
    }
    observations.push({ ...value, glyphs, mobile: width <= 390 });
    writeFileSync(
      `${fixture.evidenceDir}/formatted.json`,
      JSON.stringify(
        {
          source: fixture.formattedSource,
          code: fixture.formattedCode,
          cells: fixture.formattedCells,
          entryId,
          observations,
        },
        null,
        2,
      ),
    );
    await screenshot(`formatted-${width}`);
  }
  const codeStart = fixture.formattedSource.indexOf(fixture.formattedCode);
  const glyphExpression = (offset) =>
    `(${formattedGlyph.toString()})(${JSON.stringify(entryId)},'pre code',${JSON.stringify(fixture.formattedCode)},${codeStart},${offset - codeStart})`;
  const target = await here(glyphExpression(codeStart + fixture.formattedCode.indexOf("COMIC_CODE_BODY")));
  await here(
    `(() => {const port=window.__comicRoot().querySelector('[data-comic-scroll]').firstElementChild;const deltaY=${target.y}+8;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));port.scrollTop+=deltaY;})()`,
  );
  const saved = await until(
    "inside technical source position",
    () =>
      here(
        `JSON.parse(sessionStorage.getItem('evener:comic:position:'+${JSON.stringify(fixture.refs.live)})??'null')?.position`,
      ),
    (value) =>
      value?.mode === "away" &&
      value.entryId === entryId &&
      value.offset > codeStart &&
      value.offset < codeStart + fixture.formattedCode.length,
  );
  const recovery = [];
  const measure = async (name) => {
    const glyph = await until(
      name,
      () => here(glyphExpression(saved.offset)),
      (value) => value?.connected && Math.abs(value.y - saved.viewportOffset) <= 2,
    );
    if (glyph.glyph !== fixture.formattedSource[saved.offset])
      throw Error("technical restore reached a different source glyph");
    recovery.push({ name, saved, glyph });
    writeFileSync(`${fixture.evidenceDir}/formatted-recovery.json`, JSON.stringify(recovery, null, 2));
  };
  for (const width of [760, 390, 320, 1360]) {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width,
      height: 900,
      deviceScaleFactor: 1,
      mobile: width <= 390,
    });
    await measure(`technical-resize-${width}`);
  }
  await presentation(page, "Standard");
  await presentation(page, "Comic");
  await measure("technical-mode-handoff");
  await holdFonts();
  await reloadPage(page);
  await measure("technical-before-font");
  await releaseFonts();
  const font = await until(
    "technical font settled",
    () => here("window.__comicRoot()?.dataset.comicFont"),
    (value) => value === (fixture.requireFonts ? "ready" : "unavailable"),
  );
  await measure("technical-reload");
  writeFileSync(
    `${fixture.evidenceDir}/formatted-font.json`,
    JSON.stringify({ font, requireFonts: fixture.requireFonts, saved }, null, 2),
  );
  await here("window.__comicSockets.forEach(socket=>socket.close(4000,'formatted source recovery'))");
  await until("formatted socket reconnected", () =>
    here("window.__comicSockets.some(socket=>socket.readyState===WebSocket.OPEN)"),
  );
  await measure("technical-reconnect");
  const eviction = await evictChildHistory(page, fixture.refs.live, false);
  const networkStart = wireTrace.length;
  const recorder = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `(${recordReloadEvents.toString()})(${JSON.stringify(fixture.refs.live)})`,
  });
  try {
    await reloadPage(page);
    await measure("technical-cold-history-reload");
    const pages = repliesSince(
      wireTrace,
      networkStart,
      (request) => request.method === "thread/turns/list" && request.params.ref === fixture.refs.live,
    );
    const input = await here("({seen:window.__comicReloadInputSeen,events:window.__comicReloadEvents})");
    writeFileSync(
      `${fixture.evidenceDir}/formatted-cold-history.json`,
      JSON.stringify({ saved, eviction, pages, input }, null, 2),
    );
    if (
      !eviction.deleted.transcriptItems ||
      pages.length < 2 ||
      new Set(pages.map((pair) => pair.request.params.cursor)).size < 2 ||
      pages.some((pair) => pair.response.error) ||
      input.seen !== false
    )
      throw Error("technical source did not recover through two real pages without input");
  } finally {
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: recorder.result.identifier });
  }
  if (outgoing.slice(firstRequest).some((frame) => ["turn/start", "thread/resume"].includes(frame.method)))
    throw Error("formatted reading mutated a session");
}

function inlineFixturePoint(source, original, offset) {
  const content = source.querySelector("[data-comic-text]");
  const start = +source.dataset.comicStart;
  const formatted = !!content?.querySelector("em, code");
  const glyphs = formatted
    ? window
        .__comicInlineGlyphs(original)
        .filter((glyph) => glyph.source >= start && glyph.source < +source.dataset.comicEnd)
    : Array.from({ length: content.textContent.length }, (_, index) => ({
        char: content.textContent[index],
        source: start + index,
      }));
  const nearest = glyphs.reduce((best, glyph) =>
    Math.abs(glyph.source - offset) <= Math.abs(best.source - offset) ? glyph : best,
  );
  const wanted = glyphs.indexOf(nearest);
  const walker = document.createTreeWalker(content, NodeFilter.SHOW_TEXT);
  let index = 0;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    for (let local = 0; local < node.length; local++, index++) {
      if (index !== wanted) continue;
      if (node.data[local] !== nearest.char) throw Error("independent fixture glyph differs from rendered node");
      const range = document.createRange();
      range.setStart(node, local);
      range.setEnd(node, local + 1);
      const rect = range.getBoundingClientRect();
      return {
        top: rect.top,
        left: rect.left,
        width: rect.width,
        height: rect.height,
        glyph: range.toString(),
        sourceOffset: nearest.source,
        nodeText: node.data,
        nodeOffset: local,
        connected: node.isConnected,
        parent: node.parentElement.tagName,
      };
    }
  }
  throw Error("independent fixture glyph absent");
}

function readGeometry(point, ref) {
  const port = window.__comicRoot()?.querySelector("[data-comic-scroll]")?.firstElementChild;
  if (!port) return null;
  const sources = [...port.querySelectorAll("[data-comic-entry]")];
  const parts = sources.map((node) => ({
    sessionRef: node.dataset.comicSession,
    path: JSON.parse(node.dataset.comicPath ?? "[]"),
    rowId: node.closest("[data-comic-row]")?.dataset.comicRow,
    entryId: node.dataset.comicEntry,
    start: +node.dataset.comicStart,
    end: +node.dataset.comicEnd,
    formatted: !!node.querySelector("[data-comic-text] em, [data-comic-text] code"),
    text: node.querySelector("[data-comic-text]")?.textContent ?? "",
  }));
  const source =
    point &&
    sources.find(
      (node) =>
        node.dataset.comicSession === (point.sessionRef ?? ref) &&
        node.dataset.comicPath === JSON.stringify(point.path ?? []) &&
        node.dataset.comicEntry === point.entryId &&
        +node.dataset.comicStart <= point.offset &&
        (+node.dataset.comicEnd > point.offset || (point.offset === 0 && +node.dataset.comicEnd === 0)),
    );
  let selected = null;
  if (source) {
    const text = source.querySelector("[data-comic-text]");
    let rect = source.getBoundingClientRect();
    if (text) {
      const original =
        point.text ??
        window.__comicFixtureSources.find(
          (raw) =>
            window
              .__comicInlineGlyphs(raw.slice(+source.dataset.comicStart, +source.dataset.comicEnd))
              .map((glyph) => glyph.char)
              .join("") +
              "\n" ===
            text.textContent,
        ) ??
        text.textContent;
      rect = window.__comicInlinePoint(source, original, point.offset);
    }
    selected = {
      sessionRef: source.dataset.comicSession,
      path: JSON.parse(source.dataset.comicPath),
      entryId: point.entryId,
      offset: point.offset,
      y: rect.top - port.getBoundingClientRect().top,
      start: +source.dataset.comicStart,
      end: +source.dataset.comicEnd,
      glyph: rect.glyph,
      nodeText: rect.nodeText,
      nodeOffset: rect.nodeOffset,
      connected: rect.connected,
      parent: rect.parent,
    };
  }
  const probe = window.__comicRoot()?.querySelector("[data-comic-probe]");
  const probeText = probe?.querySelector("[data-comic-text]");
  const panels = [...port.querySelectorAll("article")];
  const style = (node) =>
    node
      ? {
          width: node.getBoundingClientRect().width,
          font: getComputedStyle(node).font,
          family: getComputedStyle(node).fontFamily,
          weight: getComputedStyle(node).fontWeight,
          size: getComputedStyle(node).fontSize,
          lineHeight: getComputedStyle(node).lineHeight,
        }
      : null;
  return {
    path: location.pathname,
    ref: window.__comicRoot()?.dataset.sessionRef,
    width: innerWidth,
    portWidth: port.clientWidth,
    mode: window.__comicRoot()?.querySelector("[data-comic-mode]")?.dataset.comicMode,
    font: window.__comicRoot()?.dataset.comicFont,
    saved: JSON.parse(sessionStorage.getItem(`evener:comic:position:${ref}`) ?? "null")?.position,
    point: selected,
    parts,
    mounted: sources.length,
    scrollTop: port.scrollTop,
    scrollHeight: port.scrollHeight,
    clientHeight: port.clientHeight,
    endGap: port.scrollHeight - port.clientHeight - port.scrollTop,
    errorGroups: [...port.querySelectorAll('section[aria-label="Turn errors"]')].map((group) =>
      [...group.querySelectorAll("[data-comic-entry]")].map((node) => node.dataset.comicEntry),
    ),
    overflow: Math.max(document.documentElement.scrollWidth - innerWidth, port.scrollWidth - port.clientWidth),
    pageOverflow: document.documentElement.scrollHeight - innerHeight,
    clipped: [...port.querySelectorAll("[data-comic-text], [data-comic-identity]")]
      .filter((node) => node.scrollWidth > node.clientWidth + 1 || node.scrollHeight > node.clientHeight + 1)
      .map((node) => node.textContent),
    missingArt: [...port.querySelectorAll("img")]
      .filter((node) => !node.complete || !node.naturalWidth)
      .map((node) => node.src),
    scenes: [...port.querySelectorAll("[data-comic-scene]")].map((node) => node.dataset.comicScene),
    humanImages: [...port.querySelectorAll('[data-comic-speaker="human"] img')].map((node) => node.src),
    probe: {
      panel: style(probe),
      text: style(probeText),
      inheritedFont: getComputedStyle(probe ?? port).getPropertyValue("--comic-font"),
    },
    panels: panels.map((node) => ({ panel: style(node), text: style(node.querySelector("[data-comic-text]")) })),
  };
}

function panelWidthCases() {
  const root = window.__comicRoot();
  const port = root?.querySelector("[data-comic-scroll]")?.firstElementChild;
  const original = port?.querySelector("[data-comic-entry]");
  const row = original?.closest("[data-comic-row]");
  const article = original?.querySelector("article");
  const probe = root?.querySelector("[data-comic-probe]");
  if (!port || !row || !article || !probe) throw Error("panel geometry fixtures not mounted");
  const availableWidth = row.getBoundingClientRect().width;
  const rootFontSize = parseFloat(getComputedStyle(document.documentElement).fontSize);
  const sample = row.cloneNode(false);
  sample.removeAttribute("data-comic-row");
  sample.style.width = `${availableWidth}px`;
  sample.style.position = "absolute";
  sample.style.visibility = "hidden";
  const append = () => {
    const wrapper = original.cloneNode(false);
    wrapper.removeAttribute("data-comic-entry");
    wrapper.append(article.cloneNode(true));
    sample.append(wrapper);
    return wrapper.querySelector("article");
  };
  const singleProbe = probe.cloneNode(true);
  singleProbe.style.width = `${availableWidth}px`;
  const pairedProbe = probe.cloneNode(true);
  pairedProbe.style.width = `${innerWidth > 760 ? (availableWidth - 15) / 2 : availableWidth}px`;
  root.append(sample, singleProbe, pairedProbe);
  try {
    const first = append();
    const width = (node) => node.getBoundingClientRect().width;
    const single = {
      panel: width(first),
      probe: width(singleProbe),
      left: first.getBoundingClientRect().left,
      right: first.getBoundingClientRect().right,
    };
    const pairedRow = [...port.querySelectorAll("[data-comic-row]")].find(
      (node) => node.querySelectorAll("[data-comic-entry] article").length === 2,
    );
    const [pairedFirst, pairedSecond] = pairedRow?.querySelectorAll("[data-comic-entry] article") ?? [];
    const paired =
      pairedFirst && pairedSecond
        ? {
            rowId: pairedRow.dataset.comicRow,
            panel: width(pairedFirst),
            probe: width(pairedProbe),
            left: pairedFirst.getBoundingClientRect().left,
            firstRight: pairedFirst.getBoundingClientRect().right,
            secondLeft: pairedSecond.getBoundingClientRect().left,
            right: pairedSecond.getBoundingClientRect().right,
          }
        : null;
    return {
      width: innerWidth,
      availableWidth,
      rootFontSize,
      readerLeft: row.getBoundingClientRect().left,
      readerRight: row.getBoundingClientRect().right,
      single,
      paired,
    };
  } finally {
    sample.remove();
    singleProbe.remove();
    pairedProbe.remove();
  }
}

export function verifyRemoteRequests(trace, { parentRef, childRef } = {}) {
  const connections = new Set(
    trace.filter((row) => row.event === "Network.webSocketCreated").map((row) => row.connection),
  );
  if (!connections.size) throw Error("remote document did not open a connection");
  const sent = trace.filter((row) => row.event === "Network.webSocketFrameSent");
  if (sent.some((row) => ["turn/start", "thread/resume"].includes(JSON.parse(row.frame).method)))
    throw Error("remote read mutated a session");
  const requests = sent.filter((row) => connections.has(row.connection)).map((row) => JSON.parse(row.frame));
  for (const [index, row] of trace.entries()) {
    if (row.event !== "Network.webSocketFrameSent" || !connections.has(row.connection)) continue;
    const request = JSON.parse(row.frame);
    if (request.method !== "thread/read" || request.params.ref.startsWith("comic-remote:")) continue;
    const ref = request.params.ref;
    // The web-wide Live owner reads independently of panes. Its permission must
    // precede this read on this connection, never come from the observed read.
    const discovered = repliesSince(
      trace.slice(0, index),
      0,
      (request) =>
        request.method === "evener/navigation/read" &&
        request.params?.resource === "section" &&
        request.params.section === "live",
    ).some(
      (pair) =>
        pair.connection === row.connection &&
        !pair.response.error &&
        pair.response.result?.status === "ok" &&
        pair.response.result.data?.entities?.some(
          (entity) =>
            entity.kind === "session" &&
            entity.value?.kind === "session" &&
            entity.value.live === true &&
            entity.value.ref === ref,
        ),
    );
    if (!ref.startsWith("local:") || ref === childRef?.replace("comic-remote:", "local:") || !discovered)
      throw Error("remote read borrowed a local session");
  }
  const pairs = repliesSince(trace, 0, (request) => request.method === "thread/read");
  for (const ref of [parentRef, childRef]) {
    if (
      !ref?.startsWith("comic-remote:") ||
      !pairs.some(
        (pair) =>
          connections.has(pair.connection) &&
          pair.request.params?.ref === ref &&
          !pair.response.error &&
          pair.response.result?.thread?.evener?.ref === ref &&
          pair.response.result.thread.id === ref.slice("comic-remote:".length),
      )
    )
      throw Error(`remote read lacks successful qualified reply: ${ref}`);
  }
  return requests;
}

// Shared by the page-evaluated driver and its decision-logic tests.
export function delegateLookupStep({ mounted, top, height, viewport }, target, direction = -1) {
  const index = mounted.findIndex((node) => node.ref === target.ref && node.key === target.key);
  if (index >= 0) return { index, direction, deltaY: 0 };
  const boundary = direction < 0 ? top <= 1 : top + viewport >= height - 2;
  if (boundary) direction *= -1;
  return { index: -1, direction, boundary, deltaY: direction * Math.min(300, viewport * 0.8) };
}

async function remoteJourney(page, fixture, { incoming, wireTrace, screenshot }) {
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const firstFrame = wireTrace.length;
  const remoteURL = new URL(fixture.baseURL);
  remoteURL.pathname = `/s/${encodeURIComponent(fixture.refs.remote)}`;
  remoteURL.search = "";
  await navigateTo(page, remoteURL.href);
  await until("real remote parent activity", () =>
    incoming.some((frame) => {
      const result = JSON.parse(frame).result;
      return (
        result?.context?.ref === fixture.refs.remote &&
        Array.isArray(result.delegates) &&
        result.delegates.length === 50
      );
    }),
  );
  const header = await until("remote-qualified real heading", async () =>
    evaluateHere(`(() => {
    const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;if(!port)return null;
    const header=port.querySelector('[data-comic-delegate]');
    if(!header){port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:400}));port.scrollTop+=400;return null;}
    header.querySelector('button').click();
    return {key:header.dataset.comicDelegate,ref:header.dataset.comicChildRef,text:header.textContent,portrait:header.querySelector('img').getAttribute('src')};
  })()`),
  );
  if (!header.ref.startsWith("comic-remote:") || !header.key.includes(fixture.refs.remote))
    throw Error(`unqualified real remote heading: ${JSON.stringify(header)}`);
  const source = async () =>
    evaluateHere(`(() => {
    const panel=[...window.__comicRoot()?.querySelectorAll('[data-comic-entry]')].find(panel=>panel.dataset.comicSession===${JSON.stringify(header.ref)} && panel.textContent.includes('COMIC_ROSTER_LEAF literal answer'));
    if(panel) return {ref:panel.dataset.comicSession,identity:panel.querySelector('[data-comic-identity]').textContent};
    const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;if(port){port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:300}));port.scrollTop+=300;}return null;
  })()`);
  const inline = await until("real remote child source", source);
  const local = JSON.parse(readFileSync(`${fixture.evidenceDir}/activity-roster.json`, "utf8")).observed.find(
    (row) => row.ref.split(":").at(-1) === header.ref.split(":").at(-1),
  );
  if (!local || local.key === header.key || !header.text.includes(fixture.refs.remote))
    throw Error("remote identity borrowed local attribution");
  await screenshot("remote-inline");
  const search = [];
  // The expanded source can shift the virtual window before the full-page
  // action lookup. Visit the newer end first, then reverse at its boundary.
  let direction = 1;
  try {
    await until("remote full-page action", async () => {
      await evaluateHere("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
      const step = await evaluateHere(`(() => {
      const port=window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
      const headers=[...window.__comicRoot()?.querySelectorAll('[data-comic-delegate]')];
      const before={top:port.scrollTop,height:port.scrollHeight,viewport:port.clientHeight};
      const mounted=headers.map(node=>({key:node.dataset.comicDelegate,ref:node.dataset.comicChildRef,y:node.getBoundingClientRect().top}));
      const decision=(${delegateLookupStep.toString()})({...before,mounted},${JSON.stringify(header)},${direction});
      const node=headers[decision.index];
      if(node){[...node.querySelectorAll('button')].find(button=>button.textContent==='Open conversation').click();return {found:true,before,mounted,decision};}
      port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:decision.deltaY}));port.scrollTop+=decision.deltaY;
      return {found:false,before,mounted,decision,command:{deltaY:decision.deltaY,top:port.scrollTop}};
    })()`);
      search.push(step);
      direction = step.decision.direction;
      return step.found;
    });
  } finally {
    writeFileSync(
      `${fixture.evidenceDir}/remote-route-search.json`,
      JSON.stringify({ target: header, steps: search }, null, 2),
    );
  }
  await until(
    "real remote direct route",
    () =>
      evaluateHere(
        `({path:location.pathname,ref:window.__comicRoot()?.dataset.sessionRef,roots:[...document.querySelectorAll('[data-comic-font][data-session-ref]')].map(node=>node.dataset.sessionRef),owners:[...document.querySelectorAll('[data-session-actions-ref][data-pane-id]')].map(node=>({ref:node.dataset.sessionActionsRef,pane:node.dataset.paneId}))})`,
      ),
    (value) => value.ref === header.ref,
  );
  const direct = await until("remote direct persona", source, (value) => value?.identity === inline.identity);
  const requests = verifyRemoteRequests(wireTrace.slice(firstFrame), {
    parentRef: fixture.refs.remote,
    childRef: header.ref,
  });
  writeFileSync(
    `${fixture.evidenceDir}/remote-identity.json`,
    JSON.stringify({ header, local, inline, direct, requests }, null, 2),
  );
}

async function rosterJourney(page, fixture, { command, complete, incoming, outgoing, screenshot, wireTrace }) {
  const { rosterCount } = comicScenario(fixture.scenario);
  const evaluateHere = (expression) => evaluate(page.send, expression);
  command("background-role", "comic-roster");
  await complete("COMIC_ROSTER_ROOT");
  await evaluateHere(
    `([...document.querySelectorAll('button')].find(button=>button.textContent==='Jump to latest')).click()`,
  );
  const firstRequest = outgoing.length;
  const firstFrame = incoming.length;
  const firstTrace = wireTrace.length;
  const continuations = [];
  await reloadPage(page);
  const pages = () =>
    incoming
      .slice(firstFrame)
      .map((frame) => JSON.parse(frame).result)
      .filter(
        (result) =>
          Array.isArray(result?.delegates) &&
          result.page &&
          result.context?.sessionId === fixture.refs.live.split(":").at(-1),
      );
  await until(
    "real first activity page",
    () => pages(),
    (rows) => rows.some((row) => row.delegates.length === 50 && row.page.nextCursor),
  );
  for (let index = 0; index < 2; index++) {
    const requestStart = outgoing.length;
    await until("activity continuation button", async () => {
      await evaluateHere(
        `(() => { const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild; if(port) {port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:port.scrollHeight-port.scrollTop}));port.scrollTop=port.scrollHeight;} })()`,
      );
      return evaluateHere(
        `(() => { const button=[...document.querySelectorAll('button')].find(button=>button.textContent==='More delegates'&&!button.disabled); if(!button)return false; button.click(); return true; })()`,
      );
    });
    const request = await until("real continued activity page", () =>
      outgoing
        .slice(requestStart)
        .find(
          (frame) =>
            frame.method === "evener/thread/delegates/list" &&
            frame.params?.ref === fixture.refs.live &&
            frame.params.cursor,
        ),
    );
    continuations.push(
      await until("activity page response", () =>
        repliesSince(
          wireTrace,
          firstTrace,
          (sent) =>
            sent.id === request.id &&
            sent.method === request.method &&
            sent.params?.ref === fixture.refs.live &&
            sent.params.cursor === request.params.cursor,
        ).find((reply) => Array.isArray(reply.response.result?.delegates)),
      ),
    );
  }
  writeFileSync(`${fixture.evidenceDir}/activity-continuation-replies.json`, JSON.stringify(continuations, null, 2));
  const rows = new Map(
    pages()
      .flatMap((page) => page.delegates)
      .map((row) => [row.delegateId, row]),
  );
  if (rows.size !== rosterCount || !pages().some((page) => page.page.complete))
    throw Error(
      "real activity pages incomplete: " +
        JSON.stringify({
          count: rows.size,
          pages: pages().map((page) => ({ count: page.delegates.length, page: page.page })),
        }),
    );
  const observed = new Map();
  let maxMountedHeaders = 0;
  let direction = 1;
  const steps = [];
  await evaluateHere(
    `(() => {const port=window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:-port.scrollTop}));port.scrollTop=0;})()`,
  );
  // The 26rem cap makes each row taller, so reaching the last real delegate
  // takes more short reader scrolls than the former full-width composition.
  for (let index = 0; index < 400; index++) {
    await evaluateHere("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const step = await evaluateHere(
      `(() => { const port=window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild; const headers=[...window.__comicRoot()?.querySelectorAll('[data-comic-delegate]')].map(header=>({key:header.dataset.comicDelegate,ref:header.dataset.comicChildRef,name:header.querySelector('strong').textContent,text:header.textContent,portrait:header.querySelector('img').getAttribute('src')})); const before={top:port.scrollTop,height:port.scrollHeight,viewport:port.clientHeight}; const boundary=${direction}<0?before.top<=1:before.top+before.viewport>=before.height-2; const deltaY=${direction}*Math.max(200,port.clientHeight-100);port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));port.scrollTop+=deltaY; return {headers,boundary,before,command:{deltaY,top:port.scrollTop}}; })()`,
    );
    for (const header of step.headers) observed.set(header.key, header);
    maxMountedHeaders = Math.max(maxMountedHeaders, step.headers.length);
    steps.push({ index, direction, ...step, uncommandedDelta: step.before.top - (steps.at(-1)?.command.top ?? 0) });
    if (observed.size === rosterCount) break;
    if (step.boundary) direction *= -1;
  }
  const named = [...observed.values()].filter((header) => header.name.includes("comic-same-name"));
  writeFileSync(
    `${fixture.evidenceDir}/activity-roster.json`,
    JSON.stringify(
      {
        pages: pages(),
        observed: [...observed.values()],
        named,
        maxMountedHeaders,
        steps,
        requests: outgoing.slice(firstRequest),
      },
      null,
      2,
    ),
  );
  if (observed.size !== rosterCount || maxMountedHeaders > 35)
    throw Error(`real roster virtual bounds/coverage: ${JSON.stringify({ seen: observed.size, maxMountedHeaders })}`);
  if (
    named.length !== 2 ||
    named[0].key === named[1].key ||
    named[0].ref === named[1].ref ||
    named[0].text === named[1].text
  )
    throw Error(`same-named real delegates not distinctly attributed: ${JSON.stringify(named)}`);
  const reads = outgoing.slice(firstRequest).filter((frame) => frame.method === "thread/read");
  const leafRefs = new Set([...rows.values()].map((row) => row.childRef));
  if (reads.some((frame) => leafRefs.has(frame.params?.ref)))
    throw Error("collapsed named child acquired a transcript");
  await screenshot("activity-roster");
  writeFileSync(
    `${fixture.evidenceDir}/activity-roster.json`,
    JSON.stringify(
      {
        pages: pages(),
        observed: [...observed.values()],
        named,
        maxMountedHeaders,
        steps,
        requests: outgoing.slice(firstRequest),
      },
      null,
      2,
    ),
  );
  const revealNamed = async (target) => {
    let direction = -1;
    return until(`mounted named branch ${target.ref}`, async () => {
      const step = await evaluateHere(
        `(() => {const port=window.__comicRoot().querySelector('[data-comic-scroll]').firstElementChild;const header=[...port.querySelectorAll('[data-comic-delegate]')].find(node=>node.dataset.comicChildRef===${JSON.stringify(target.ref)});if(header){const deltaY=header.getBoundingClientRect().top-port.getBoundingClientRect().top-24;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));port.scrollTop+=deltaY;return {found:true};}const deltaY=${direction}*400;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));port.scrollTop+=deltaY;return {boundary:port.scrollTop<=1||port.scrollTop+port.clientHeight>=port.scrollHeight-1};})()`,
      );
      if (step.boundary) direction *= -1;
      return step.found;
    });
  };
  const toggleNamed = (target, expanded) =>
    evaluateHere(
      `(() => {const header=[...window.__comicRoot().querySelectorAll('[data-comic-delegate]')].find(node=>node.dataset.comicChildRef===${JSON.stringify(target.ref)});const button=header?.querySelector('[aria-expanded="${expanded}"]');if(!button)return false;button.click();return true;})()`,
    );
  for (const target of named) {
    await revealNamed(target);
    if (!(await toggleNamed(target, false))) throw Error("missing mounted sibling expansion");
    await until("real sibling transcript read", () =>
      repliesSince(
        wireTrace,
        0,
        (request) => request.method === "thread/read" && request.params.ref === target.ref,
      ).some((pair) => !pair.response.error && pair.response.result?.thread),
    );
  }
  await revealNamed(named[0]);
  const collapseStart = wireTrace.length;
  if (!(await toggleNamed(named[0], true))) throw Error("missing mounted branch collapse");
  const retired = await until(
    "collapsed branch unsubscribe receipt",
    () =>
      repliesSince(wireTrace, collapseStart, (request) => request.method === "thread/unsubscribe").filter(
        (pair) => !pair.response.error,
      ),
    (pairs) => pairs.some((pair) => pair.request.params.ref === named[0].ref),
  );
  const mounted = await evaluateHere(
    `(() => {const headers=[...window.__comicRoot().querySelectorAll('[data-comic-delegate]')];const find=ref=>headers.find(node=>node.dataset.comicChildRef===ref);return {mountedCollapsed:find(${JSON.stringify(named[0].ref)})?.querySelector('[aria-expanded]')?.getAttribute('aria-expanded')==='false',siblingExpanded:find(${JSON.stringify(named[1].ref)})?.querySelector('[aria-expanded]')?.getAttribute('aria-expanded')==='true'};})()`,
  );
  const collapseRequests = wireTrace
    .slice(collapseStart)
    .filter((row) => row.event === "Network.webSocketFrameSent")
    .map((row) => JSON.parse(row.frame));
  const retirement = {
    ...mounted,
    collapsed: named[0].ref,
    sibling: named[1].ref,
    retired: collapseRequests
      .filter((request) => request.method === "thread/unsubscribe")
      .map((request) => request.params.ref),
    reread: collapseRequests.filter((request) => request.method === "thread/read").map((request) => request.params.ref),
  };
  writeFileSync(
    `${fixture.evidenceDir}/mounted-collapse.json`,
    JSON.stringify({ retirement, retired, wireTrace: wireTrace.slice(collapseStart) }, null, 2),
  );
  verifyBranchRetirement(retirement);
  await revealNamed(named[1]);
  if (!(await toggleNamed(named[1], true))) throw Error("missing sibling cleanup collapse");
  const directChild = named[0];
  const ownedRow = [...rows.values()].find((row) => row.childRef === directChild.ref);
  let namedDirection = 1;
  const namedSearch = [];
  try {
    await until("same-named explicit child route", async () => {
      await evaluateHere("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
      const step = await evaluateHere(`(() => {
    const headers=[...window.__comicRoot()?.querySelectorAll('[data-comic-delegate]')];
    const mounted=headers.map(node=>({key:node.dataset.comicDelegate,ref:node.dataset.comicChildRef,y:node.getBoundingClientRect().top}));
    const port=window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;const before={top:port.scrollTop,height:port.scrollHeight,viewport:port.clientHeight};
    const decision=(${delegateLookupStep.toString()})({...before,mounted},${JSON.stringify(directChild)},${namedDirection});
    const header=headers[decision.index];
    if(header){[...header.querySelectorAll('button')].find(button=>button.textContent==='Open conversation').click();return {found:true,before,mounted,decision};}
    const deltaY=decision.deltaY;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY}));port.scrollTop+=deltaY;return {found:false,before,mounted,decision,command:{deltaY,top:port.scrollTop}};
  })()`);
      namedSearch.push(step);
      namedDirection = step.decision.direction;
      return step.found;
    });
  } finally {
    writeFileSync(
      `${fixture.evidenceDir}/named-route-search.json`,
      JSON.stringify({ target: directChild.ref, steps: namedSearch }, null, 2),
    );
  }
  const readNamedIdentity = () =>
    evaluateHere(
      `(() => {const panel=[...(window.__comicRoot()?.querySelectorAll('[data-comic-entry]')??[])].find(node=>node.dataset.comicSession===${JSON.stringify(directChild.ref)}&&node.textContent.includes('COMIC_ROSTER_LEAF literal answer'));return panel?.querySelector('[data-comic-identity]')?.textContent;})()`,
    );
  const directIdentity = await until(
    "named direct child identity",
    readNamedIdentity,
    (value) => value === `comic-same-name · ${ownedRow.delegateId} · ${fixture.refs.live}`,
  );
  await reloadPage(page);
  const refreshedIdentity = await until(
    "named direct child refresh identity",
    readNamedIdentity,
    (value) => value === directIdentity,
  );
  writeFileSync(
    `${fixture.evidenceDir}/named-direct.json`,
    JSON.stringify({ directChild, ownedRow, directIdentity, refreshedIdentity }, null, 2),
  );
}

// Additive real-daemon journey. DOM input only, never store/model injection.
async function childJourney(page, fixture, { command, incoming, outgoing, screenshot, wireTrace }) {
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const firstRequest = outgoing.length;
  command("background-role", "comic-subchats");
  command("background-start-turn", "COMIC_SUBCHAT_ROOT");
  await until("real child history production", () =>
    readFileSync(fixture.requestLog, "utf8")
      .split("\n")
      .some((line) => {
        try {
          return JSON.parse(line).event === "comic-child-produced";
        } catch {
          return false;
        }
      }),
  );
  const metrics = [];
  const reveal = async (selector, text = "", direction = -1) =>
    until(`reveal real child ${text}`, async () => {
      const result = await evaluateHere(`(() => {
      const port = window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;
      if (!port) return null;
      const node = [...port.querySelectorAll(${JSON.stringify(selector)})].find(node => node.textContent.includes(${JSON.stringify(text)}));
      const deltaY = node ? node.getBoundingClientRect().top - port.getBoundingClientRect().top - 24 : ${direction} * port.clientHeight * .7;
      port.dispatchEvent(new WheelEvent('wheel', {bubbles:true, deltaY})); port.scrollTop += deltaY;
      return {found:node ? { ref:node.dataset.comicChildRef, key:node.dataset.comicDelegate, sessionRef:node.dataset.comicSession, path:JSON.parse(node.dataset.comicPath??'[]'), entryId:node.dataset.comicEntry } : null, boundary:${direction} < 0 ? port.scrollTop <= 1 : port.scrollTop + port.clientHeight >= port.scrollHeight - 1};
    })()`);
      await evaluateHere("new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
      if (result?.boundary) direction *= -1;
      return result?.found;
    });
  const header = await reveal("[data-comic-delegate]");
  if (!header.ref || !header.key.includes(fixture.refs.live))
    throw Error(`delegate has no qualified runtime ownership ${JSON.stringify(header)}`);
  const creation = await until("recorded child creation output", () =>
    incoming
      .flatMap((frame) => JSON.parse(frame).params?.items ?? [])
      .find((item) => item.callId === "comic_delegate_CHILD" && item.output),
  );
  const launch = JSON.parse(creation.output);
  if (launch.transcript_ref !== header.ref || !header.key.includes(launch.delegate_id))
    throw Error("strip not bound to real recorded creation");
  await reveal("[data-comic-entry]", "COMIC_SUBCHAT_ROOT");
  const chronology = await until(
    "parent messages surround actual launch",
    () =>
      evaluateHere(
        `(() => {const nodes=[...window.__comicRoot().querySelectorAll('[data-comic-entry]')];const before=nodes.findIndex(node=>node.textContent.includes('COMIC_SUBCHAT_ROOT'));const launch=nodes.findIndex(node=>node.dataset.comicEntry===${JSON.stringify(header.key)});const after=nodes.findIndex(node=>node.textContent.includes('COMIC_THOUGHT_ROOT'));return {before,launch,after,entries:nodes.map(node=>({id:node.dataset.comicEntry,session:node.dataset.comicSession,text:node.textContent.slice(0,100)}))};})()`,
      ),
    (value) => value.before >= 0 && value.launch > value.before && value.after > value.launch,
  );
  verifyCreationChronology(chronology);
  writeFileSync(
    `${fixture.evidenceDir}/creation-chronology.json`,
    JSON.stringify({ header, creation, chronology }, null, 2),
  );
  const capturedSource = async (name, source, minimumOffset = 0) => {
    const geometry = await until(
      name,
      () =>
        evaluateHere(`(() => {
      const saved=JSON.parse(sessionStorage.getItem('evener:comic:position:'+${JSON.stringify(fixture.refs.live)})??'null')?.position;
      return (${readGeometry.toString()})(saved,${JSON.stringify(fixture.refs.live)});
    })()`),
      (value) =>
        value?.saved?.sessionRef === source.sessionRef &&
        JSON.stringify(value.saved.path) === JSON.stringify(source.path) &&
        value.saved.entryId === source.entryId &&
        value.saved.offset >= minimumOffset &&
        savedSourcePlaced(value, value.saved),
    );
    return geometry.saved;
  };
  const operate = async (ref, label, keyboard = false) => {
    const found = await evaluateHere(`(() => {
    const header = [...window.__comicRoot()?.querySelectorAll('[data-comic-delegate]')].find(node => node.dataset.comicChildRef === ${JSON.stringify(ref)});
    const button = [...(header?.querySelectorAll('button') ?? [])].find(node => node.textContent.trim() === ${JSON.stringify(label)});
    if (!button) return false; button.focus({preventScroll:true}); if (!${keyboard}) button.click(); return true;
    })()`);
    if (found && keyboard) {
      await page.send("Input.dispatchKeyEvent", {
        type: "keyDown",
        key: "Enter",
        code: "Enter",
        windowsVirtualKeyCode: 13,
        text: "\r",
        unmodifiedText: "\r",
      });
      await page.send("Input.dispatchKeyEvent", {
        type: "keyUp",
        key: "Enter",
        code: "Enter",
        windowsVirtualKeyCode: 13,
      });
    }
    return found;
  };
  if (!(await operate(header.ref, "Expand conversation", true))) throw Error("missing real expansion");
  await until("real child activity", async () =>
    incoming.some((frame) => {
      const result = JSON.parse(frame).result;
      return result?.context?.ref === header.ref && Array.isArray(result.delegates) && result.delegates.length > 0;
    }),
  );
  await reveal(`[data-comic-child-ref=${JSON.stringify(header.ref)}]`, "Meanwhile");
  // Seek the child's own heading, independently of its parent's mounted header.
  let grandchild;
  await until("real nested heading", async () => {
    const value = await evaluateHere(`(() => {
      const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
      const node = [...port.querySelectorAll('[data-comic-delegate]')].find(node => node.dataset.comicChildRef !== ${JSON.stringify(header.ref)});
      if (node) {
        const history=[...port.querySelectorAll('[data-comic-flow="history"]')].find(row=>row.dataset.comicSession===${JSON.stringify(header.ref)});
        const message=[...port.querySelectorAll('[data-comic-entry]')].find(row=>row.dataset.comicSession===${JSON.stringify(header.ref)}&&row.querySelector('[data-comic-text]'));
        return {ref:node.dataset.comicChildRef,key:node.dataset.comicDelegate,unplaced:[...port.querySelectorAll('[data-comic-flow="unplaced-group"]')].some(group=>group.dataset.comicSession===${JSON.stringify(header.ref)}),afterHistory:!!history&&!!(history.compareDocumentPosition(node)&Node.DOCUMENT_POSITION_FOLLOWING),beforeMessages:!!message&&!!(node.compareDocumentPosition(message)&Node.DOCUMENT_POSITION_FOLLOWING)};
      }
      port.dispatchEvent(new WheelEvent('wheel', {bubbles:true, deltaY:400})); port.scrollTop += 400; return null;
    })()`);
    if (value) grandchild = value;
    return value;
  });
  if (!grandchild.key.includes(header.ref) || grandchild.ref === header.ref)
    throw Error("nested identity is not independently qualified");
  if (!grandchild.unplaced) throw Error("earlier causal event was not outside loaded child history");
  if (!grandchild.afterHistory || !grandchild.beforeMessages)
    throw Error("unknown launch is not between history boundary and messages");
  if (!(await operate(grandchild.ref, "Expand conversation"))) throw Error("missing grandchild expansion");
  const nestedSource = await reveal("[data-comic-entry]", "COMIC_THOUGHT_GRANDCHILD", 1);
  if (nestedSource.sessionRef !== grandchild.ref || nestedSource.path.length !== 2) throw Error("wrong nested source");
  let saved = await capturedSource("nested source anchor", nestedSource);
  const measure = async (name) => {
    const geometry = await until(
      name,
      () =>
        evaluateHere(`(${readGeometry.toString()})(${JSON.stringify(saved)}, ${JSON.stringify(fixture.refs.live)})`),
      (value) => savedSourcePlaced(value, saved),
    );
    const nestedGeometry = await evaluateHere(`(() => {
      const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
      return {scrollports:window.__comicRoot()?.querySelectorAll('[data-comic-scroll]').length, editors:port.querySelectorAll('textarea,[contenteditable=true]').length, fullPageEditors:window.__comicRoot()?.querySelectorAll('[data-comic-composer] [contenteditable=true]').length,
        headers:[...port.querySelectorAll('[data-comic-delegate]')].map(node => ({ref:node.dataset.comicChildRef, width:node.getBoundingClientRect().width, text:node.textContent})),
        thoughts:[...port.querySelectorAll('[data-comic-balloon=thought]')].map(node => { const text = node.querySelector('[data-comic-text]'); const box = node.getBoundingClientRect(); const inside = text.getBoundingClientRect(); return {text:node.textContent, width:node.clientWidth, height:node.clientHeight, overflow:Math.max(text.scrollWidth-text.clientWidth,text.scrollHeight-text.clientHeight), contained:inside.left >= box.left && inside.right <= box.right && inside.top >= box.top && inside.bottom <= box.bottom}; })};
    })()`);
    if (
      geometry.overflow > 1 ||
      geometry.pageOverflow > 1 ||
      geometry.clipped.length ||
      geometry.mounted > 35 ||
      nestedGeometry.scrollports !== 1 ||
      nestedGeometry.editors !== 0 ||
      nestedGeometry.fullPageEditors !== 1 ||
      nestedGeometry.thoughts.some((thought) => thought.overflow > 1 || !thought.contained)
    )
      throw Error(`${name}: bad child geometry ${JSON.stringify({ geometry, nestedGeometry })}`);
    if (geometry.width <= 760 && nestedGeometry.headers.some((item) => item.width < geometry.portWidth - 40))
      throw Error("compounded mobile indentation");
    metrics.push({ name, geometry, nestedGeometry });
    writeFileSync(
      `${fixture.evidenceDir}/children.json`,
      JSON.stringify({ header, grandchild, saved, metrics }, null, 2),
    );
  };
  for (const width of [1360, 760, 600, 390, 320, 1360]) {
    await page.send("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: 1, mobile: false });
    await measure(`nested-width-${width}`);
    await screenshot(`nested-${width}`);
  }
  const beforeGrowth = incoming.length;
  command("background-phase", "comic-child-grow");
  await until("actual child stream growth", () =>
    incoming.slice(beforeGrowth).some((frame) => frame.includes("COMIC_ANSWER_CHILD")),
  );
  await measure("nested-child-stream-grow");
  command("background-phase", "comic-child-finish");
  await until("real child producer settled", () =>
    readFileSync(fixture.requestLog, "utf8")
      .split("\n")
      .some((line) => {
        try {
          const row = JSON.parse(line);
          return row.event === "turn-complete" && row.prompt === "COMIC_SUBCHAT_ROOT" && row.ok;
        } catch {
          return false;
        }
      }),
  );
  await measure("nested-child-stream-finished");
  const nestedHandoff = [await presentation(page, "Standard"), await presentation(page, "Comic")];
  await measure("nested-mode-handoff");
  writeFileSync(`${fixture.evidenceDir}/nested-mode-handoff.json`, JSON.stringify(nestedHandoff, null, 2));
  await reloadPage(page);
  await measure("nested-reload");
  // Recover only the saved branch, then expose the child's explicit full-page route.
  if (!(await operate(grandchild.ref, "Open conversation"))) throw Error("missing child route");
  await until(
    "full child page",
    () => evaluateHere("window.__comicRoot()?.dataset.sessionRef"),
    (value) => value === grandchild.ref,
  );
  await evaluateHere("history.back()");
  await measure("child-back");
  // Back changes focus, not workspace ownership. The explicit direct page
  // remains a secondary pane until its native tab close control retires it.
  const directPaneBefore = await evaluateHere(
    `[...document.querySelectorAll('[data-session-actions-ref][data-pane-id]')].map(node=>({ref:node.dataset.sessionActionsRef,pane:node.dataset.paneId}))`,
  );
  if (directPaneBefore.filter((owner) => owner.ref === grandchild.ref).length !== 1)
    throw Error("missing unique direct child pane owner");
  await nativeClick(
    page,
    "close direct child workspace pane",
    `[...document.querySelectorAll('[data-session-actions-ref][data-pane-id]')].find(node=>node.dataset.sessionActionsRef===${JSON.stringify(grandchild.ref)})?.closest('.dv-groupview')?.querySelector('.dv-tab.dv-active-tab .dv-default-tab-action')`,
  );
  await until("direct child pane owner retired", () =>
    evaluateHere(
      `![...document.querySelectorAll('[data-session-actions-ref][data-pane-id]')].some(node=>node.dataset.sessionActionsRef===${JSON.stringify(grandchild.ref)})`,
    ),
  );
  await measure("child-direct-pane-closed");
  writeFileSync(
    `${fixture.evidenceDir}/child-direct-pane-retirement.json`,
    JSON.stringify(
      {
        before: directPaneBefore,
        after: await evaluateHere(
          `[...document.querySelectorAll('[data-session-actions-ref][data-pane-id]')].map(node=>({ref:node.dataset.sessionActionsRef,pane:node.dataset.paneId}))`,
        ),
      },
      null,
      2,
    ),
  );
  for (const mode of ["plain", "comic"]) {
    await selectAppearance(page, "View", mode);
    await measure(`nested-${mode}`);
  }
  if (!(await operate(grandchild.ref, "Collapse conversation", true))) throw Error("missing keyboard collapse");
  await until("collapsed focus remains on strip", () =>
    evaluateHere(
      `document.activeElement?.closest('[data-comic-child-ref]')?.dataset.comicChildRef === ${JSON.stringify(grandchild.ref)} && document.activeElement?.getAttribute('aria-expanded') === 'false'`,
    ),
  );
  if (!(await operate(grandchild.ref, "Expand conversation", true))) throw Error("missing keyboard reopen");
  await until("keyboard reopened strip", () =>
    evaluateHere(
      `document.activeElement?.closest('[data-comic-child-ref]')?.dataset.comicChildRef === ${JSON.stringify(grandchild.ref)} && document.activeElement?.getAttribute('aria-expanded') === 'true'`,
    ),
  );
  writeFileSync(
    `${fixture.evidenceDir}/child-keyboard.json`,
    JSON.stringify(
      await evaluateHere(
        `({focusedRef:document.activeElement.closest('[data-comic-child-ref]').dataset.comicChildRef, expanded:document.activeElement.getAttribute('aria-expanded'), saved:JSON.parse(sessionStorage.getItem('evener:comic:position:' + ${JSON.stringify(fixture.refs.live)})).position})`,
      ),
      null,
      2,
    ),
  );
  await evaluateHere(`(() => {
    const port=window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild;
    window.__comicRestoreEvents=[];
    const record=event=>{window.__comicRestoreEvents.push({at:performance.now(),type:event.type,trusted:event.isTrusted,scrollTop:port.scrollTop,focusedRef:document.activeElement?.closest('[data-comic-child-ref]')?.dataset.comicChildRef,selectionInPort:port.contains(document.getSelection()?.anchorNode),saved:JSON.parse(sessionStorage.getItem('evener:comic:position:'+${JSON.stringify(fixture.refs.live)})??'null')?.position});if(window.__comicRestoreEvents.length>100)window.__comicRestoreEvents.shift();};
    for(const event of ['scroll','wheel','pointerdown','pointerup','pointercancel','keydown','focusin'])port.addEventListener(event,record,{passive:true});
    document.addEventListener('selectionchange',record);
  })()`);
  const restoreCheckpoints = [];
  const recordRestore = async (stage) => {
    restoreCheckpoints.push({
      stage,
      geometry: await evaluateHere(
        `(${readGeometry.toString()})(${JSON.stringify(saved)},${JSON.stringify(fixture.refs.live)})`,
      ),
      events: await evaluateHere("window.__comicRestoreEvents"),
    });
    writeFileSync(
      `${fixture.evidenceDir}/nested-restore-checkpoints.json`,
      JSON.stringify({ expected: saved, header, grandchild, checkpoints: restoreCheckpoints }, null, 2),
    );
  };
  // Native Enter may scroll its focused heading into view. Reestablish an
  // inside-child source before testing collapse's retained child anchor.
  await reveal("[data-comic-entry]", "COMIC_THOUGHT_GRANDCHILD", 1);
  saved = await capturedSource("inside child before collapse", nestedSource);
  await recordRestore("before-collapse");
  if (!(await operate(grandchild.ref, "Collapse conversation"))) throw Error("missing anchored collapse");
  const collapsedAnchor = {
    mode: "away",
    sessionRef: header.ref,
    path: saved.path.slice(0, -1),
    entryId: grandchild.key,
    offset: 0,
    viewportOffset: 0,
  };
  await until(
    "anchored collapse settled",
    () =>
      evaluateHere(
        `({collapsed:document.activeElement?.getAttribute('aria-expanded') === 'false',geometry:(${readGeometry.toString()})(${JSON.stringify(collapsedAnchor)},${JSON.stringify(fixture.refs.live)})})`,
      ),
    (value) => value.collapsed && savedSourcePlaced(value.geometry, collapsedAnchor),
  );
  await recordRestore("after-collapse");
  if (!(await operate(grandchild.ref, "Expand conversation"))) throw Error("missing anchored reopen");
  await recordRestore("after-reopen");
  try {
    await measure("nested-reopen");
  } finally {
    await recordRestore("reopen-verdict");
  }
  const historySelector = `[data-comic-flow="history"][data-comic-session=${JSON.stringify(header.ref)}]`;
  const beforePaging = outgoing.length;
  await evaluateHere(`window.__comicHoldOriginRef=${JSON.stringify(header.ref)}`);
  for (let index = 0; index < 12; index++) {
    await reveal(historySelector);
    const result = await evaluateHere(`(() => {
      const row = document.querySelector(${JSON.stringify(historySelector)});
      const button = [...(row?.querySelectorAll('button') ?? [])].find(node => node.textContent === 'Older messages');
      if (!button) return 'exhausted'; if (button.disabled) return 'pending';
      button.focus({preventScroll:true}); button.click(); return 'requested';
    })()`);
    if (result === "exhausted") break;
    await until("child page settled", () =>
      evaluateHere(
        `!!window.__comicOriginHold || !document.querySelector(${JSON.stringify(historySelector)})?.querySelector('button')?.disabled`,
      ),
    );
    if (await evaluateHere("!!window.__comicOriginHold")) break;
  }
  const heldOrigin = await evaluateHere("window.__comicOriginHold");
  if (heldOrigin?.releaseCount !== 0) throw Error("origin-revealing page was not held");
  const relocationOrigin = recordedRelocationOrigin(heldOrigin, header.ref, grandchild);
  // The final page has not reached the reader yet. Pin once here, then release
  // only the untouched native reply. No reveal, scroll or capture follows release.
  const relocationSource = await reveal("[data-comic-entry]", "COMIC_THOUGHT_GRANDCHILD");
  const relocationExpected = await capturedSource("pre-origin pinned glyph", relocationSource);
  const relocationBefore = await evaluateHere(
    `(${readGeometry.toString()})(${JSON.stringify(relocationExpected)},${JSON.stringify(fixture.refs.live)})`,
  );
  const observeRelocation = () =>
    evaluateHere(
      `(${observeDelegateRelocation.toString()})(${JSON.stringify(header.ref)},${JSON.stringify(grandchild.ref)},${JSON.stringify(relocationOrigin)})`,
    );
  const pendingRelocation = await observeRelocation();
  if (!pendingRelocation.found || !pendingRelocation.unplaced || pendingRelocation.atLaunch)
    throw Error("pending launch was not observed in the sibling unplaced group");
  await evaluateHere("window.__comicReleaseOrigin()");
  const appliedRelocation = await until(
    "origin reply applied without reader input",
    observeRelocation,
    (value) => value.found && !value.unplaced && value.atLaunch,
  );
  await evaluateHere("new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))");
  const relocationAfter = await evaluateHere(
    `(${readGeometry.toString()})(${JSON.stringify(relocationExpected)},${JSON.stringify(fixture.refs.live)})`,
  );
  writeFileSync(
    `${fixture.evidenceDir}/late-creation-position.json`,
    JSON.stringify(
      {
        expected: relocationExpected,
        before: relocationBefore,
        after: relocationAfter,
        heldOrigin,
        relocationOrigin,
        pendingRelocation,
        appliedRelocation,
      },
      null,
      2,
    ),
  );
  if (
    !savedSourcePlaced(relocationBefore, relocationExpected) ||
    !savedSourcePlaced(relocationAfter, relocationExpected)
  )
    throw Error("late launch changed the pinned source glyph or viewport offset");
  const childPages = outgoing
    .slice(beforePaging)
    .filter((frame) => frame.method === "thread/turns/list" && frame.params.ref === header.ref);
  if (childPages.length < 3) throw Error(`three child history pages were not read: ${childPages.length}`);
  await reveal(`[data-comic-child-ref=${JSON.stringify(grandchild.ref)}]`, "", 1);
  const relocated = await evaluateHere(
    `(() => { const node = window.__comicRoot()?.querySelector('[data-comic-child-ref=' + ${JSON.stringify(JSON.stringify(grandchild.ref))} + ']'); return {key:node?.dataset.comicDelegate, expanded:node?.querySelector('[aria-expanded]')?.getAttribute('aria-expanded')}; })()`,
  );
  const lifetime = outgoing
    .slice(beforePaging)
    .filter(
      (frame) => ["thread/read", "thread/unsubscribe"].includes(frame.method) && frame.params?.ref === grandchild.ref,
    );
  const relocation = await observeRelocation();
  verifyDelegateRelocation({
    grandchild,
    relocated,
    lifetime,
    relocation,
    expected: relocationExpected,
    before: relocationBefore,
    after: relocationAfter,
  });
  writeFileSync(
    `${fixture.evidenceDir}/late-creation-relocation.json`,
    JSON.stringify(
      {
        grandchild,
        relocated,
        lifetime,
        relocation,
        relocationOrigin,
        pendingRelocation,
        appliedRelocation,
        childPages,
        expected: relocationExpected,
        before: relocationBefore,
        after: relocationAfter,
      },
      null,
      2,
    ),
  );
  const laterSource = await reveal(
    `[data-comic-entry][data-comic-session=${JSON.stringify(header.ref)}]`,
    "COMIC_CHILD_SOURCE_05",
    1,
  );
  await evaluateHere(
    `(() => { const port = window.__comicRoot()?.querySelector('[data-comic-scroll]').firstElementChild; const source = [...port.querySelectorAll('[data-comic-text]')].find(node => node.textContent.startsWith('COMIC_CHILD_SOURCE_05')); const deltaY = source.getBoundingClientRect().top - port.getBoundingClientRect().top + 80; port.dispatchEvent(new WheelEvent('wheel', {bubbles:true, deltaY})); port.scrollTop += deltaY; })()`,
  );
  if (laterSource.sessionRef !== header.ref || laterSource.path.length !== 1) throw Error("wrong later child source");
  saved = await capturedSource("later child source anchor", laterSource, 1);
  await measure("later-child-page");
  await evaluateHere("window.__comicSockets.forEach(socket => socket.close(4000, 'comic child recovery'))");
  await measure("later-child-disconnected");
  await until("child socket reconnected", () =>
    evaluateHere("window.__comicSockets.some(socket => socket.readyState === WebSocket.OPEN)"),
  );
  await measure("later-child-reconnected");
  const beforeReload = outgoing.length;
  const paneOwnersBefore = await evaluateHere(
    `[...document.querySelectorAll('[data-session-actions-ref][data-pane-id]')].map(node=>({ref:node.dataset.sessionActionsRef,pane:node.dataset.paneId,visible:!!node.getClientRects().length}))`,
  );
  if (paneOwnersBefore.some((owner) => owner.ref === grandchild.ref))
    throw Error("direct descendant pane still owns the reload");
  const diagnosticScript = await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `(${recordReloadEvents.toString()})(${JSON.stringify(fixture.refs.live)})`,
  });
  const reloadBefore = await until(
    "immediate pre-reload source placement",
    () => evaluateHere(`(${readGeometry.toString()})(${JSON.stringify(saved)},${JSON.stringify(fixture.refs.live)})`),
    (value) => savedSourcePlaced(value, saved),
  );
  const cachedReloadStart = wireTrace.length;
  await reloadPage(page);
  const cachedDocumentOrigin = await evaluateHere("performance.timeOrigin");
  try {
    await measure("later-child-reload");
  } finally {
    writeFileSync(
      `${fixture.evidenceDir}/later-child-restore-checkpoints.json`,
      JSON.stringify(
        {
          expected: saved,
          before: reloadBefore,
          after: await evaluateHere(
            `(${readGeometry.toString()})(${JSON.stringify(saved)},${JSON.stringify(fixture.refs.live)})`,
          ),
          paneOwnersBefore,
          paneOwnersAfter: await evaluateHere(
            `[...document.querySelectorAll('[data-session-actions-ref][data-pane-id]')].map(node=>({ref:node.dataset.sessionActionsRef,pane:node.dataset.paneId,visible:!!node.getClientRects().length}))`,
          ),
          events: await evaluateHere("window.__comicReloadEvents"),
          requests: outgoing.slice(beforeReload),
        },
        null,
        2,
      ),
    );
    await page.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: diagnosticScript.result.identifier });
  }
  let cachedRecovery;
  try {
    await until(
      "cached child post-hydration source recovery",
      async () => {
        // Freeze wire evidence first, then measure DOM after those replies.
        cachedRecovery = await sampleCachedReloadRecovery(
          () => wireTrace.slice(cachedReloadStart),
          () =>
            evaluateHere(
              `({currentDocumentOrigin:performance.timeOrigin,geometry:(${readGeometry.toString()})(${JSON.stringify(saved)},${JSON.stringify(fixture.refs.live)}),intent:window.__comicReloadEvents,inputSeen:window.__comicReloadInputSeen})`,
            ),
          { documentOrigin: cachedDocumentOrigin, ref: header.ref, expected: saved },
        );
        return cachedRecovery;
      },
      (value) => value.ready,
    );
  } finally {
    writeFileSync(
      `${fixture.evidenceDir}/later-child-cached-recovery.json`,
      JSON.stringify({ expected: saved, ...cachedRecovery }, null, 2),
    );
  }
  if (
    outgoing
      .slice(beforeReload)
      .some(
        (frame) =>
          frame.method === "thread/read" && frame.params.ref === grandchild.ref && frame.params.includeTurns !== false,
      )
  )
    throw Error("restoration fetched an unopened descendant");
  for (const partial of [true, false]) {
    const eviction = await evictChildHistory(page, header.ref, partial);
    if (!eviction.deleted.transcriptItems || (partial && !eviction.retained.transcriptItems))
      throw Error(`cache boundary did not remove older source ${JSON.stringify(eviction)}`);
    const networkStart = wireTrace.length;
    await reloadPage(page);
    await measure(partial ? "later-child-partial-cache-reload" : "later-child-cold-cache-reload");
    const pages = repliesSince(
      wireTrace,
      networkStart,
      (frame) => frame.method === "thread/turns/list" && frame.params.ref === header.ref,
    );
    writeFileSync(
      `${fixture.evidenceDir}/later-child-${partial ? "partial" : "cold"}-cache.json`,
      JSON.stringify({ eviction, expected: saved, pages }, null, 2),
    );
    if (
      pages.length < 2 ||
      pages.some((page) => page.response.error || !page.response.result) ||
      new Set(pages.map((page) => page.request.params.cursor)).size < 2
    )
      throw Error(
        `child reload did not continue through two distinct successful real network pages ${JSON.stringify(pages)}`,
      );
  }
  await page.send("Page.navigate", { url: new URL(`/s/${encodeURIComponent(header.ref)}`, fixture.baseURL).href });
  await until(
    "later child full page",
    () => evaluateHere("window.__comicRoot()?.dataset.sessionRef"),
    (value) => value === header.ref,
  );
  await evaluateHere("history.back()");
  await measure("later-child-back");
  await page.send("Page.navigate", { url: new URL(`/s/${encodeURIComponent(grandchild.ref)}`, fixture.baseURL).href });
  await until(
    "named-parent grandchild page",
    () => evaluateHere("window.__comicRoot()?.dataset.sessionRef"),
    (value) => value === grandchild.ref,
  );
  const parentIdentity = await until(
    "named parent instruction identity",
    () =>
      evaluateHere(`(() => {
    const panel=[...window.__comicRoot()?.querySelectorAll('[data-comic-entry]')].find(node=>node.dataset.comicSession===${JSON.stringify(grandchild.ref)}&&node.textContent.includes('COMIC_PARENT_INSTRUCTION literal parent steering'));
    if(panel)return panel.querySelector('[data-comic-identity]')?.textContent;
    const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;if(port){port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:-300}));port.scrollTop-=300;}return null;
  })()`),
    (value) => value === `comic-named-parent · ${JSON.parse(header.key)[1]} · ${JSON.parse(header.key)[0]}`,
  );
  writeFileSync(
    `${fixture.evidenceDir}/named-parent.json`,
    JSON.stringify({ header, grandchild, parentIdentity }, null, 2),
  );
  await evaluateHere("history.back()");
  await measure("named-parent-back");
  // All expanded-reader preservation checks are complete. Collapse the same
  // real strip only to mount both original parent neighbors in one observation.
  await reveal(`[data-comic-child-ref=${JSON.stringify(header.ref)}]`);
  if (!(await operate(header.ref, "Collapse conversation"))) throw Error("missing completed child disclosure");
  await reveal("[data-comic-entry]", "COMIC_SUBCHAT_ROOT");
  const completedChronology = await until(
    "same launch neighbors mounted after completion",
    () =>
      evaluateHere(`(() => {
    const nodes=[...window.__comicRoot().querySelectorAll('[data-comic-entry]')];
    const ids=${JSON.stringify(["before", "launch", "after"].map((key) => chronology.entries[chronology[key]].id))};
    return {before:nodes.findIndex(node=>node.dataset.comicEntry===ids[0]),launch:nodes.findIndex(node=>node.dataset.comicEntry===ids[1]),after:nodes.findIndex(node=>node.dataset.comicEntry===ids[2]),entries:nodes.map(node=>({id:node.dataset.comicEntry,session:node.dataset.comicSession,text:node.textContent.slice(0,100)}))};
  })()`),
    (value) => value.before >= 0 && value.launch >= 0 && value.after >= 0,
  );
  writeFileSync(
    `${fixture.evidenceDir}/creation-chronology-completed.json`,
    JSON.stringify({ header, creation, before: chronology, after: completedChronology }, null, 2),
  );
  verifyCreationChronology(chronology, completedChronology);
  const mutations = outgoing
    .slice(firstRequest)
    .filter((frame) => frame.method === "turn/start" || frame.method === "thread/resume");
  if (mutations.length) throw Error(`child reads mutated a session: ${JSON.stringify(mutations)}`);
  writeFileSync(
    `${fixture.evidenceDir}/child-reads.json`,
    JSON.stringify({ childPages, requests: outgoing.slice(firstRequest), relocated }, null, 2),
  );
  const actualTurns = incoming.flatMap((frame) => {
    const data = JSON.parse(frame);
    return data.result?.thread?.turns ?? data.result?.turns ?? [];
  });
  const wire = JSON.stringify(actualTurns);
  if (!wire.includes("COMIC_THOUGHT_GRANDCHILD") || !wire.includes("COMIC_ANSWER_CHILD"))
    throw Error("missing canonical real child transcript evidence");
  writeFileSync(
    `${fixture.evidenceDir}/child-canonical.json`,
    JSON.stringify(
      actualTurns.filter((turn) => JSON.stringify(turn).includes("COMIC_")),
      null,
      2,
    ),
  );
  // Exercise the real phone host: opening a document unmounts the Comic reader.
  // A subsequent real reconnect must not replace the focused document, and Back
  // must return to the same containing pane rather than a separately read child.
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 390,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  const documentLinks = [];
  for (const level of ["ROOT", "CHILD"]) {
    if (level === "CHILD") {
      await until("committed child document disclosure", async () => {
        await reveal(`[data-comic-child-ref=${JSON.stringify(header.ref)}]`);
        return operate(header.ref, "Expand conversation");
      });
    }
    const label = `COMIC_DOCUMENT_${level}`;
    const expectedRef = level === "ROOT" ? fixture.refs.live : header.ref;
    const linkExpression = `[...window.__comicRoot().querySelectorAll('a')].find(node=>node.textContent===${JSON.stringify(label)})`;
    const href = await until(`${label} served anchor mounted`, async () => {
      await reveal("[data-comic-entry]", label, 1);
      return evaluateHere(`${linkExpression}?.href`);
    });
    const before = await paneSnapshot(page);
    const url = new URL(href);
    const settled = repliesSince(
      wireTrace,
      0,
      (frame) =>
        frame.method === "evener/navigation/read" &&
        frame.params?.resource === "location" &&
        frame.params?.ref === fixture.refs.live,
    )
      .filter((reply) => reply.response.result && !reply.response.error)
      .at(-1);
    if (!settled) throw Error("document source route has no successful real location response");
    if (
      url.pathname !== "/doc/file" ||
      url.searchParams.get("session") !== expectedRef ||
      !url.searchParams.get("path")?.endsWith(`/comic-document-${level.toLowerCase()}.md`)
    )
      throw Error(`document link lost source ownership ${href}`);
    await evaluateHere(`(() => {
      window.__comicDocLink = ${linkExpression};
      window.__comicDocEvents = [];
      window.__comicDocObserve = event => window.__comicDocEvents.push({type:event.type, target:event.target.outerHTML?.slice(0,600), originalConnected:window.__comicDocLink.isConnected, same:window.__comicDocLink.contains(event.target), prevented:event.defaultPrevented});
      for(const type of ['pointerdown','mousedown','pointerup','mouseup','click']) document.addEventListener(type,window.__comicDocObserve,true);
    })()`);
    const documentVisible = () =>
      evaluateHere(
        `document.body.textContent.includes(${JSON.stringify(`COMIC_DOCUMENT_BODY_${level}`)}) && !document.querySelector('[data-comic-font][data-session-ref]')`,
      );
    try {
      await nativeClick(page, `activate ${label}`, linkExpression);
      await until(`${label} actual file content with Comic unmounted`, documentVisible);
    } finally {
      const activation = await evaluateHere(`(async () => {
        for(const type of ['pointerdown','mousedown','pointerup','mouseup','click']) document.removeEventListener(type,window.__comicDocObserve,true);
        let readFailure;
        if (!document.body.textContent.includes(${JSON.stringify(`COMIC_DOCUMENT_BODY_${level}`)})) {
          const response = await fetch(${JSON.stringify(href)}, {credentials:'same-origin'});
          readFailure = {status:response.status, body:(await response.text()).slice(0,1000)};
        }
        return {events:window.__comicDocEvents, originalConnected:window.__comicDocLink.isConnected, body:document.body.innerText, url:location.href, readFailure};
      })()`);
      writeFileSync(
        `${fixture.evidenceDir}/document-${level.toLowerCase()}-activation.json`,
        JSON.stringify(activation, null, 2),
      );
    }
    const reconnectStart = wireTrace.length;
    await evaluateHere("window.__comicSockets.forEach(socket=>socket.close(4000,'document companion reconciliation'))");
    await until("document socket reconnected", () =>
      evaluateHere("window.__comicSockets.some(socket=>socket.readyState===WebSocket.OPEN)"),
    );
    const rehydrated = await until("document source hydrated after reconnect", () =>
      repliesSince(
        wireTrace,
        reconnectStart,
        (frame) => frame.method === "thread/read" && frame.params?.ref === expectedRef,
      ).find((reply) => reply.response.result && !reply.response.error),
    );
    await evaluateHere("new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))");
    await until(`${label} stays focused after reconnect`, documentVisible);
    await screenshot(`document-${level.toLowerCase()}-phone`);
    await nativeClick(page, `Back from ${label}`, `document.querySelector('button[aria-label="Back"]')`);
    const after = await until(
      `${label} Back to containing Comic pane`,
      () => paneSnapshot(page),
      (value) => value.comic && value.pane === before.pane && value.ref === before.ref,
    );
    documentLinks.push({ level, expectedRef, href, before, settled, rehydrated, after });
  }
  writeFileSync(`${fixture.evidenceDir}/document-links-phone.json`, JSON.stringify(documentLinks, null, 2));
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 1360,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  // Back focuses the source but intentionally retains its document pane. Close
  // those real readers before the later collapsed-roster reload: DocPane owns
  // a thread claim even when its tab is inactive, independently of Comic.
  const documentTab = `[...document.querySelectorAll('.dv-tab')].find(node=>/comic-document-(root|child)\\.md/.test(node.textContent))`;
  await until("retained document desktop tab", () => evaluateHere(`!!${documentTab}`));
  const retiredDocuments = [];
  for (let index = 0; index < 2; index++) {
    const title = await evaluateHere(`${documentTab}?.textContent`);
    if (!title) break;
    await nativeClick(
      page,
      "close completed document reader",
      `${documentTab}?.querySelector('.dv-default-tab-action')`,
    );
    await until("completed document tab retired", () =>
      evaluateHere(
        `![...document.querySelectorAll('.dv-tab')].some(node=>node.textContent===${JSON.stringify(title)})`,
      ),
    );
    retiredDocuments.push(title);
  }
  if (await evaluateHere(`!!${documentTab}`)) throw Error("completed document reader still retained");
  await until(
    "Comic source remains after document retirement",
    () => paneSnapshot(page),
    (value) => value.comic && value.ref === fixture.refs.live,
  );
  writeFileSync(`${fixture.evidenceDir}/document-reader-retirement.json`, JSON.stringify(retiredDocuments, null, 2));
  command("background-role", "history");
}

// Independent original-author oracle: fresh real root, two explicit fork_context
// calls, no fabricated transcript/store data, and the same literal leaf sources
// after paging, reconnect, reload, inline expansion and direct navigation.
async function authorshipJourney(page, fixture, { incoming, outgoing, screenshot }) {
  const command = (command, phase = "") =>
    appendFileSync(fixture.authorshipControl, `${JSON.stringify({ command, phase })}\n`);
  const evaluateHere = (expression) => evaluate(page.send, expression);
  const firstRequest = outgoing.length;
  const firstFrame = incoming.length;
  const human = "COMIC_AUTH_HUMAN literal inherited human";
  command("background-mode");
  command("background-role", "comic-authorship");
  command("background-start-turn", human);
  await until("nested fork producer complete", () =>
    readFileSync(fixture.authorshipLog, "utf8")
      .split("\n")
      .some((line) => {
        try {
          const row = JSON.parse(line);
          return row.event === "turn-complete" && row.prompt === human && row.ok;
        } catch {
          return false;
        }
      }),
  );
  const navigate = async (ref) => {
    await page.send("Page.navigate", { url: new URL(`/s/${encodeURIComponent(ref)}`, fixture.baseURL).href });
    await until(
      "authored conversation route",
      () => evaluateHere("window.__comicRoot()?.dataset.sessionRef"),
      (value) => value === ref,
    );
  };
  await navigate(fixture.authorshipRef);
  const expand = async (name) => {
    let direction = 1;
    const header = await until(`real authored child ${name}`, async () => {
      const result = await evaluateHere(`(() => {
      const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;
      const header=[...window.__comicRoot()?.querySelectorAll('[data-comic-delegate]')].find(node=>node.textContent.includes(${JSON.stringify(name)}));
      if(!header){if(!port)return null;port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:${direction}*500}));port.scrollTop+=${direction}*500;return {boundary:${direction}<0?port.scrollTop<=1:port.scrollTop+port.clientHeight>=port.scrollHeight-1};}
      const button=[...header.querySelectorAll('button')].find(node=>node.textContent==='Expand conversation');
      if(button)button.click();
      return {ref:header.dataset.comicChildRef,key:header.dataset.comicDelegate};
      })()`);
      if (result?.boundary) direction *= -1;
      return result?.ref ? result : null;
    });
    if (!header.ref || !header.key) throw Error("missing authored owned identity");
    return header;
  };
  const child = await expand("comic-auth-child");
  const leaf = await expand("comic-auth-leaf");
  const childID = JSON.parse(child.key)[1];
  if (JSON.parse(child.key)[0] !== fixture.authorshipRef || JSON.parse(leaf.key)[0] !== child.ref)
    throw Error("fork ancestry lost");
  const cases = [
    { text: human, originalRef: fixture.authorshipRef, kind: "speech", speaker: "human", author: { kind: "human" } },
    ...["ROOT", "CHILD"].flatMap((level) =>
      [
        { text: `COMIC_AUTH_ANSWER_${level} literal original answer`, kind: "speech" },
        { text: `COMIC_AUTH_THOUGHT_${level} literal original thought`, kind: "thought" },
      ].map((value) => ({
        ...value,
        originalRef: level === "ROOT" ? fixture.authorshipRef : child.ref,
        speaker: "assistant",
        author:
          level === "ROOT"
            ? { kind: "agent", sessionRef: fixture.authorshipRef }
            : {
                kind: "agent",
                sessionRef: child.ref,
                ownerRef: fixture.authorshipRef,
                delegateId: childID,
                delegateName: "comic-auth-child",
              },
      })),
    ),
    {
      text: "COMIC_AUTH_UPDATE_CHILD literal original update",
      originalRef: child.ref,
      kind: "speech",
      speaker: "assistant",
      author: {
        kind: "agent",
        sessionRef: child.ref,
        ownerRef: fixture.authorshipRef,
        delegateId: childID,
        delegateName: "comic-auth-child",
      },
    },
  ];
  // Observe the same six fixture sources in their original conversations.
  // The expected identity is never assembled from production label wording.
  const baselines = new Map();
  let baselineDirection = 1;
  await until("six original non-inherited author baselines", async () => {
    const result = await evaluateHere(`(() => {
      const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;if(!port)return null;
      const entries=[...port.querySelectorAll('[data-comic-entry]')].map(node=>({ref:node.dataset.comicSession,text:node.querySelector('[data-comic-text]')?.textContent,identity:node.querySelector('[data-comic-identity]')?.textContent,kind:node.querySelector('[data-comic-balloon]')?.dataset.comicBalloon,speaker:node.querySelector('article')?.dataset.comicSpeaker}));
      const controls=[port.parentElement.previousElementSibling,...port.querySelectorAll('[data-comic-flow="history"]')];
      for(const control of controls){const older=[...(control?.querySelectorAll('button')??[])].find(node=>node.textContent==='Older messages');if(older&&!older.disabled)older.click();}
      port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:${baselineDirection}*500}));port.scrollTop+=${baselineDirection}*500;
      return {entries,boundary:${baselineDirection}<0?port.scrollTop<=1:port.scrollTop+port.clientHeight>=port.scrollHeight-1};
    })()`);
    if (result?.boundary) baselineDirection *= -1;
    for (const entry of result?.entries ?? [])
      if (cases.some((value) => value.text === entry.text && value.originalRef === entry.ref))
        baselines.set(entry.text, entry);
    await evaluateHere("new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))");
    return baselines.size === cases.length;
  });
  for (const expected of cases) {
    const baseline = baselines.get(expected.text);
    if (!baseline.identity || baseline.kind !== expected.kind || baseline.speaker !== expected.speaker)
      throw Error(`invalid original author baseline ${JSON.stringify({ expected, baseline })}`);
    if (
      expected.author.kind === "agent" &&
      (expected.author.delegateId
        ? !baseline.identity.includes(expected.author.delegateId) ||
          !baseline.identity.includes(expected.author.delegateName) ||
          !baseline.identity.includes(expected.author.ownerRef)
        : !baseline.identity.includes(expected.author.sessionRef))
    )
      throw Error(`original identity is not qualified ${JSON.stringify({ expected, baseline })}`);
  }
  const observations = [];
  const collect = async (name) => {
    const found = new Map();
    let direction = 1;
    await until(name, async () => {
      const result = await evaluateHere(`(() => {
        const port=window.__comicRoot()?.querySelector('[data-comic-scroll]')?.firstElementChild;if(!port)return null;
        const entries=[...port.querySelectorAll('[data-comic-entry]')].filter(node=>node.dataset.comicSession===${JSON.stringify(leaf.ref)}).map(node=>({id:node.dataset.comicEntry,path:JSON.parse(node.dataset.comicPath),start:+node.dataset.comicStart,end:+node.dataset.comicEnd,text:node.querySelector('[data-comic-text]')?.textContent,identity:node.querySelector('[data-comic-identity]')?.textContent,kind:node.querySelector('[data-comic-balloon]')?.dataset.comicBalloon,speaker:node.querySelector('article')?.dataset.comicSpeaker}));
        const history=window.__comicRoot()?.dataset.sessionRef===${JSON.stringify(leaf.ref)}
          ? port.parentElement.previousElementSibling
          : [...port.querySelectorAll('[data-comic-flow="history"]')].find(node=>node.dataset.comicSession===${JSON.stringify(leaf.ref)});
        const older=[...(history?.querySelectorAll('button')??[])].find(node=>node.textContent==='Older messages');
        if(older&&!older.disabled)older.click();
        port.dispatchEvent(new WheelEvent('wheel',{bubbles:true,deltaY:${direction}*500}));port.scrollTop+=${direction}*500;
        return {entries,boundary:${direction}<0?port.scrollTop<=1:port.scrollTop+port.clientHeight>=port.scrollHeight-1};
      })()`);
      if (result?.boundary) direction *= -1;
      for (const entry of result?.entries ?? [])
        if (cases.some((value) => value.text === entry.text)) found.set(entry.text, entry);
      writeFileSync(
        `${fixture.evidenceDir}/authorship-search.json`,
        JSON.stringify(
          { name, root: fixture.authorshipRef, child, leaf, found: [...found.values()], last: result },
          null,
          2,
        ),
      );
      await evaluateHere("new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))");
      return found.size === cases.length;
    });
    const indicators = new Set();
    for (const expected of cases) {
      const entry = found.get(expected.text);
      const baseline = baselines.get(expected.text);
      if (
        !entry.identity?.startsWith(baseline.identity) ||
        entry.identity === baseline.identity ||
        entry.kind !== expected.kind ||
        entry.speaker !== expected.speaker ||
        entry.start !== 0 ||
        entry.end !== expected.text.length
      )
        throw Error(`${name} wrong original author ${JSON.stringify({ expected, baseline, entry })}`);
      indicators.add(entry.identity.slice(baseline.identity.length));
    }
    if (indicators.size !== 1)
      throw Error(`${name} inconsistent inheritance distinction ${JSON.stringify([...indicators])}`);
    observations.push({ name, entries: [...found.values()] });
    writeFileSync(
      `${fixture.evidenceDir}/authorship.json`,
      JSON.stringify(
        { root: fixture.authorshipRef, child, leaf, cases, baselines: [...baselines.values()], observations },
        null,
        2,
      ),
    );
  };
  await collect("nested-inline-original-authors");
  if (observations[0].entries.some((entry) => entry.path.length !== 2))
    throw Error("author proof did not use nested inline leaf");
  await screenshot("authorship-inline");
  await navigate(leaf.ref);
  await collect("direct-leaf-original-authors");
  await reloadPage(page);
  await collect("reloaded-leaf-original-authors");
  const beforeReconnect = outgoing.length;
  await evaluateHere("window.__comicSockets.forEach(socket=>socket.close(4000,'authorship recovery'))");
  await until("authored leaf resubscribed", () =>
    outgoing
      .slice(beforeReconnect)
      .some((frame) => frame.method === "thread/read" && frame.params.ref === leaf.ref && frame.params.subscribe),
  );
  await collect("reconnected-leaf-original-authors");
  await screenshot("authorship-direct");
  const pages = outgoing
    .slice(firstRequest)
    .filter((frame) => frame.method === "thread/turns/list" && frame.params.ref === leaf.ref);
  if (pages.length < 2) throw Error("authored inherited sources did not cross real history pages");
  const turns = incoming.slice(firstFrame).flatMap((frame) => {
    const value = JSON.parse(frame);
    return value.result?.thread?.turns ?? value.result?.turns ?? value.result?.data ?? [];
  });
  const items = turns.flatMap((turn) => turn.items ?? []);
  for (const expected of cases) {
    const matches = items.filter((item) => item.authorship?.inherited && JSON.stringify(item).includes(expected.text));
    if (
      !matches.length ||
      matches.some((item) =>
        Object.entries(expected.author).some(([key, value]) => item.authorship.author[key] !== value),
      )
    )
      throw Error(`canonical inherited author mismatch ${JSON.stringify({ expected, matches })}`);
  }
  const mutations = outgoing
    .slice(firstRequest)
    .filter((frame) => frame.method === "turn/start" || frame.method === "thread/resume");
  if (mutations.length) throw Error("authored reads mutated a session");
  writeFileSync(
    `${fixture.evidenceDir}/authorship-wire.json`,
    JSON.stringify({ pages, turns, requests: outgoing.slice(firstRequest) }, null, 2),
  );
}
