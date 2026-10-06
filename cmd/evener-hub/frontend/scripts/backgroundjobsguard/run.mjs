#!/usr/bin/env node
// Run through TestBackgroundJobsBrowser. JSON stdin supplies an isolated real
// hub and Go-owned producer barriers, never frontend state or RPC responses.
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { Driver } from "../skillguard/run.mjs";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";

if (process.argv.includes("--help")) {
  console.log("Usage: go test -tags browserguard ./cmd/evener-hub -run '^TestBackgroundJobsBrowser$' -count=1 -v\nRun from the repository root after make build-web. The Go fixture supplies JSON stdin and retains browser evidence.");
  process.exit(0);
}
const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;
const sidebar = '[data-testid="activity-sidebar"]';
const viewport = `${sidebar} [class*="_body_"]`;
const anchor = (jobId) => `job:${q([fixture.rootRef, jobId])}`;
const row = (jobId) => `${sidebar} [data-activity-anchor=${q(anchor(jobId))}]`;
const frames = [], sockets = [], errors = [], consoleEvents = [];
const requests = new Map();
const methods = new Set(["evener/thread/jobs/list", "evener/thread/activity/read", "evener/jobs/output", "evener/jobs/get", "thread/read"]);
const read = (source) => evaluate(driver.send, source);
const wait = (source, label) => {
  new Function(`return (${source})`);
  return driver.waitPage(source, { label });
};
const token = new URL(fixture.url).pathname.split("/auth/")[1];
const redact = (value) => {
  let text = q(value).replaceAll(fixture.url, "[authenticated hub URL]");
  if (token) text = text.replaceAll(token, "[fixture token]");
  return text;
};
const save = (name, value) => writeFileSync(path.join(fixture.artifactDir, name), redact(value));

function observe(event) {
  const value = JSON.parse(event.data);
  if (value.method === "Runtime.exceptionThrown") errors.push(value.params);
  if (value.method === "Runtime.consoleAPICalled") {
    consoleEvents.push(value.params);
    if (["error", "warning"].includes(value.params.type)) errors.push(value.params);
  }
  if (["Network.webSocketCreated", "Network.webSocketClosed"].includes(value.method)) {
    sockets.push({ method: value.method, requestId: value.params.requestId });
  }
  if (!["Network.webSocketFrameSent", "Network.webSocketFrameReceived"].includes(value.method)) return;
  let message;
  try { message = JSON.parse(value.params.response.payloadData); } catch { return; }
  const key = q([value.params.requestId, message.id]);
  if (value.method === "Network.webSocketFrameSent") {
    if (!methods.has(message.method)) return;
    requests.set(key, message.method);
  } else if (!requests.has(key)) return;
  // Subscription acknowledgements can include unrelated session configuration.
  if (requests.get(key) === "thread/read" && Object.hasOwn(message, "result")) message.result = { subscribed: true };
  frames.push({ direction: value.method, socketId: value.params.requestId, ...message });
}
const sent = (after = 0) => frames.slice(after).filter(f => f.direction === "Network.webSocketFrameSent");
const response = (request) => frames.find(f => f.direction === "Network.webSocketFrameReceived" && f.socketId === request.socketId && f.id === request.id);
async function waitFrames(predicate, label) {
  if (predicate()) return;
  await new Promise((resolve, reject) => {
    const finish = (error) => {
      clearTimeout(timer);
      driver.page.ws.removeEventListener("message", check);
      error ? reject(error) : resolve();
    };
    const check = () => { if (predicate()) finish(); };
    const timer = setTimeout(() => finish(new Error(`actual RPC condition did not arrive: ${label}`)), 15000);
    driver.page.ws.addEventListener("message", check);
    check();
  });
}

// Follow actual cursor-bearing responses, never a synthetic complete snapshot.
function walk(after = 0) {
  const calls = sent(after).filter(f => f.method === "evener/thread/jobs/list" && f.params.ref === fixture.rootRef);
  for (const first of calls.filter(f => !f.params.cursor).reverse()) {
    const pages = [];
    let request = first;
    for (let page = 0; page < 10 && request; page++) {
      const reply = response(request);
      if (!reply?.result || reply.error) break;
      pages.push({ request, result: reply.result });
      if (reply.result.page.complete) return pages;
      const cursor = reply.result.page.nextCursor;
      request = calls.find(f => f.socketId === first.socketId && f.params.cursor === cursor);
    }
  }
  return null;
}
async function completeWalk(after, label) {
  await waitFrames(() => walk(after) !== null, label);
  const pages = walk(after);
  assert.ok(pages.length >= 4, `${label}: default 50-row pages reach the oldest producer`);
  const jobs = pages.flatMap(page => page.result.jobs);
  assert.equal(jobs.length, fixture.eligibleCount);
  assert.equal(new Set(jobs.map(job => anchor(job.jobId))).size, fixture.eligibleCount);
  assert.ok(jobs.every(job => job.ownerRef === fixture.rootRef && job.background));
  assert.ok(jobs.every(job => job.jobId !== fixture.excludedJobId && !job.description.startsWith("Foreground job ")));
  assert.ok(pages.every(page => !page.result.page.issues.length));
  assert.ok(pages.every(page => (page.request.params.scope ?? "session") === "session"));
  return { pages, jobs };
}

function geometryExpr(jobId) {
  return `(() => {
    const rows = document.querySelectorAll(${q(row(jobId))});
    const body = document.querySelector(${q(viewport)});
    if (!body || rows.length !== 1) return null;
    const node = rows[0], r = node.getBoundingClientRect(), v = body.getBoundingClientRect();
    const glyph = node.querySelector('[aria-hidden="true"]'), meta = node.querySelector('[class*="_rowMeta_"]');
    const first = [...body.querySelectorAll('[data-activity-anchor]')].find(n => n.getBoundingClientRect().bottom > v.top);
    return { anchor: node.dataset.activityAnchor, text: node.textContent, offset: r.top - v.top,
      row: {top:r.top,bottom:r.bottom,height:r.height}, viewport: {top:v.top,bottom:v.bottom,height:v.height},
      visible: r.bottom > v.top && r.top < v.bottom, scrollTop: body.scrollTop,
      glyphColor: getComputedStyle(glyph).color, metaColor: getComputedStyle(meta).color,
      firstAnchor: first?.dataset.activityAnchor, count: rows.length };
  })()`;
}
async function quiet(jobId, wording, visible = true) {
  const state = await read(geometryExpr(jobId));
  assert.ok(state, "one real owner-qualified row");
  assert.equal(state.count, 1);
  assert.equal(state.anchor, anchor(jobId));
  assert.equal(state.glyphColor, state.metaColor, "terminal glyph uses ordinary quiet text color");
  assert.ok(state.text.includes(wording), `truthful outcome ${wording}: ${state.text}`);
  if (visible) assert.ok(state.visible, "later-page row intersects the real activity viewport");
  return state;
}
async function scrollBottom() {
  const before = await read(`(() => { const b = document.querySelector(${q(viewport)}), r = b.getBoundingClientRect(); return {top:b.scrollTop,x:r.x+r.width/2,y:r.y+r.height/2}; })()`);
  await driver.send("Input.dispatchMouseEvent", { type: "mouseWheel", x: before.x, y: before.y, deltaX: 0, deltaY: 20000 });
  await wait(`(() => { const b = document.querySelector(${q(viewport)}); return b.scrollTop > ${before.top} && Math.abs(b.scrollTop - (b.scrollHeight-b.clientHeight)) < 1; })()`, "native wheel reaches later history");
}
async function retained(jobId, baseline, after, label, outputBytes) {
  const recovered = await completeWalk(after, `${label} rereads the complete loaded boundary`);
  assert.equal(recovered.jobs.find(job => job.jobId === fixture.laterJobId)?.outputBytes, outputBytes, `${label} observes real producer output`);
  await wait(`(() => { const s = ${geometryExpr(jobId)}; return s && s.visible && Math.abs(s.offset-(${baseline.offset})) <= 2; })()`, `${label} restores the same visible semantic row within two pixels`);
  const state = await quiet(jobId, "completed");
  assert.ok(Math.abs(state.offset - baseline.offset) <= 2);
  assert.equal(await read(`document.querySelector(${q(sidebar + ' [role="radio"][aria-checked="true"]')})?.textContent.trim().startsWith('Jobs')`), true);
  assert.equal(await read(`document.querySelector(${q(row(fixture.laterJobId))}) !== null`), true);
  driver.milestone(label, { ...state, pageCount: recovered.pages.length, outputBytes });
  await capture(label);
}
async function capture(name) {
  await driver.screenshot(name);
  const state = await read(`(() => {
    const b = document.querySelector(${q(viewport)}), r = b?.getBoundingClientRect();
    return {
      rows: b ? [...b.querySelectorAll('[data-activity-anchor]')].map(n => {
        const r = n.getBoundingClientRect();
        return {anchor:n.dataset.activityAnchor,text:n.textContent,top:r.top,bottom:r.bottom};
      }) : [],
      viewport: b ? {top:r.top,bottom:r.bottom,scrollTop:b.scrollTop,clientHeight:b.clientHeight,scrollHeight:b.scrollHeight} : null,
      sidebarStorage: localStorage.getItem('evener.activity-sidebar.v1'),
      html: document.documentElement.outerHTML,
    };
  })()`);
  const { html, ...page } = state;
  save(`${name}-state.json`, { frames, sockets, errors, consoleEvents, ...page });
  writeFileSync(path.join(fixture.artifactDir, `${name}.html`), JSON.parse(redact(html)));
}

try {
  await driver.start();
  driver.page.ws.addEventListener("message", observe);
  await driver.send("Page.addScriptToEvaluateOnNewDocument", { source: `(() => {
    const Native = window.WebSocket;
    window.__backgroundJobsSockets = [];
    window.__pagingWire = { holdSend: false, holdReply: false, holdRange: null, deliveredOutputs: 0, sent: [], replies: [], methods: new Map() };
    window.WebSocket = class extends Native {
      constructor(...args) {
        super(...args);
        window.__backgroundJobsSockets.push(this);
        super.addEventListener('message', event => {
          const state = window.__pagingWire;
          let message;
          try { message = JSON.parse(event.data); } catch { this.callback?.(event); return; }
          if (state.holdReply && state.methods.get(message.id) === 'evener/jobs/output') {
            state.holdReply = false;
            state.replies.push({socket: this, event});
          } else {
            this.callback?.(event);
            if(state.methods.get(message.id)==='evener/jobs/output') state.deliveredOutputs++;
          }
        });
      }
      set onmessage(callback) { this.callback = callback; }
      get onmessage() { return this.callback; }
      send(data) {
        const state = window.__pagingWire;
        const message = JSON.parse(data);
        state.methods.set(message.id, message.method);
        if ((state.holdSend || (state.holdRange && message.params?.beforeBytes>state.holdRange.start && message.params.beforeBytes<state.holdRange.end)) && message.method === 'evener/jobs/output' && message.params.beforeBytes !== undefined) {
          state.holdSend = false;
          state.holdRange = null;
          state.sent.push({socket: this, data, params: message.params});
        } else super.send(data);
      }
      releasePagingSend(data) { super.send(data); }
    };
  })()` });
  await navigateTo(driver.page, fixture.url);
  await driver.openSession(fixture.rootRef);
  if (fixture.journey === "output-paging") {
    await runOutputPagingJourney(fixture);
  } else {
  await driver.click('[data-testid="statusbar"] button[aria-label^="Jobs,"]');
  await wait(`(() => { const s=document.querySelector(${q(sidebar)}); return s && getComputedStyle(s).transform === 'none'; })()`, "Jobs sidebar entrance settled");
  const initial = await completeWalk(0, "closed history finds live work on the fourth page");
  assert.equal(initial.pages[3].result.jobs[0].jobId, fixture.laterJobId);
  const failed = initial.jobs.find(job => job.status === "command_exited_nonzero");
  const stopped = initial.jobs.find(job => job.status === "cancelled");
  assert.ok(failed && stopped, "real nonzero and stopped producers");
  await wait(`document.querySelector(${q(row(fixture.laterJobId))}) !== null`, "late live work is reachable with history closed");
  assert.equal(await read(`document.querySelector(${q(row(failed.jobId))}) !== null`), false);
  assert.equal(await read(`document.querySelector(${q(row(stopped.jobId))}) !== null`), false);
  driver.milestone("closed-history-late-live", { pageCount: initial.pages.length, activeAnchor: anchor(fixture.laterJobId), eligibleCount: initial.jobs.length });
  await capture("closed-history-late-live");

  const disclosure = `${sidebar} details > summary`;
  const terminalCount = initial.jobs.filter(job => job.terminal).length;
  assert.equal(terminalCount, 149, "only the two real barrier-held commands remain current");
  await wait(`document.querySelector(${q(disclosure)})?.textContent.includes('${terminalCount} completed jobs')`, "terminal history disclosure reports the real loaded count");
  await driver.click(disclosure);
  await wait(`document.querySelector(${q(row(failed.jobId))}) !== null`, "native summary opens terminal history");
  await scrollBottom();
  const failedState = await quiet(failed.jobId, "Command failed");
  await quiet(stopped.jobId, "cancelled");
  const visibleAnchor = failedState.firstAnchor;
  const visibleId = JSON.parse(visibleAnchor.slice(4))[1];
  assert.ok(initial.pages[2].result.jobs.some(job => job.jobId === visibleId), "actual visible anchor belongs to page three");
  const baseline = await quiet(visibleId, "completed");
  save("anchor-before-save-state.json", { geometry: baseline, sidebarStorage: await read("localStorage.getItem('evener.activity-sidebar.v1')") });
  await wait(`(() => { const v=JSON.parse(localStorage.getItem('evener.activity-sidebar.v1') ?? '{}')[${q(fixture.rootRef)}]; return v?.open && v.tab==='jobs' && v.categories.jobs.anchor?.id===${q(visibleAnchor)} && Math.abs(v.categories.jobs.anchor.offset-(${baseline.offset}))<=2; })()`, "native wheel persists the actual visible anchor");
  driver.milestone("quiet-later-history", { ...baseline, failed: failedState });
  await capture("quiet-later-history");

  let after = frames.length;
  driver.control("refresh");
  await retained(visibleId, baseline, after, "refresh-extent", Buffer.byteLength("BACKGROUND_LATE_INITIAL\nBACKGROUND_LATE_REFRESH\n"));

  after = frames.length;
  const socketAfter = sockets.length;
  await driver.send("Network.emulateNetworkConditions", { offline: true, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  const closed = await read(`(() => { const live=window.__backgroundJobsSockets.filter(s => s.readyState===WebSocket.OPEN && new URL(s.url).pathname==='/rpc'); for (const s of live) s.close(4000,'background Jobs guard transport boundary'); return live.length; })()`);
  assert.equal(closed, 1, "interrupt the actual shared hub WebSocket");
  await waitFrames(() => sockets.slice(socketAfter).some(s => s.method === "Network.webSocketClosed"), "actual hub socket closes");
  driver.control("reconnect");
  await driver.send("Network.emulateNetworkConditions", { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await waitFrames(() => sent(after).some(f => f.method === "thread/read" && f.params.ref === fixture.rootRef && f.params.subscribe && response(f)?.result), "real session subscription recovers on a new socket");
  await retained(visibleId, baseline, after, "reconnect-extent", Buffer.byteLength("BACKGROUND_LATE_INITIAL\nBACKGROUND_LATE_REFRESH\nBACKGROUND_LATE_RECONNECT\n"));
  const previousSocket = initial.pages[0].request.socketId;
  assert.ok(walk(after).every(page => page.request.socketId !== previousSocket), "recovery RPCs use a genuinely new connection");

  after = frames.length;
  await driver.send("Page.reload", { ignoreCache: true });
  await retained(visibleId, baseline, after, "reload-extent", Buffer.byteLength("BACKGROUND_LATE_INITIAL\nBACKGROUND_LATE_REFRESH\nBACKGROUND_LATE_RECONNECT\n"));
  await quiet(failed.jobId, "Command failed");
  await quiet(stopped.jobId, "cancelled");

  after = frames.length;
  driver.control("finish");
  await waitFrames(() => walk(after)?.flatMap(p => p.result.jobs).some(job => job.jobId === fixture.laterJobId && job.terminal && job.exitCode === 2), "held real command settles through the loaded collection");
  await wait(`(() => { const n=document.querySelector(${q(row(fixture.laterJobId))}); return n && n.textContent.includes('Command failed'); })()`, "late job moves into open Completed history with true nonzero outcome");
  await quiet(fixture.laterJobId, "Command failed", false);
  await driver.click(row(fixture.laterJobId));
  await waitFrames(() => sent(after).some(f => f.method === "evener/jobs/output" && f.params.ref === fixture.rootRef && f.params.jobId === fixture.laterJobId && response(f)?.result?.data?.encoding === "utf8" && response(f)?.result?.data?.data?.includes("BACKGROUND_LATE_FINAL")), "real output RPC targets the authoritative job owner");
  await wait("document.querySelector('[data-testid=joblog-content]')?.textContent === 'BACKGROUND_LATE_INITIAL\\nBACKGROUND_LATE_REFRESH\\nBACKGROUND_LATE_RECONNECT\\nBACKGROUND_LATE_FINAL\\n'", "actual readonly secondary output pane renders exact retained command output");
  await wait(`(() => { const layout=JSON.parse(localStorage.getItem('evener.workspace.layout.v2') ?? 'null');
    const panes=Object.values(layout?.panels ?? {}).map(p => p.params);
    return panes.some(p => p.paneType==='session' && p.paneParams.ref===${q(fixture.rootRef)})
      && panes.filter(p => p.paneType==='transcript' && p.paneParams.ref===${q('job:' + fixture.laterJobId)} && p.paneParams.parentRef===${q(fixture.rootRef)}).length===1;
  })()`, "actual job transcript pane retains owner identity beside the original session");
  driver.milestone("settled-output", { ownerRef: fixture.rootRef, jobId: fixture.laterJobId });
  await capture("settled-output");

  assert.deepEqual(errors, [], "new journey has no page exceptions or error/warning console messages");
  driver.milestone("post-journey-errors", { errors, consoleEvents });
  await capture("post-journey-errors");
  console.log("backgroundjobsguard: actual producer, quiet history, later-page recovery and owner output passed");
  }
} catch (error) {
  if (driver.page) await capture("failure").catch(captureError => save("capture-error.json", String(captureError)));
  save("failure.json", { message: String(error), stack: error.stack, frames, sockets, errors, consoleEvents });
  console.error(redact({ error: String(error), evidence: fixture.artifactDir }));
  process.exitCode = 1;
} finally {
  await driver.stop();
}

async function runOutputPagingJourney(fixture) {
  const content = '[data-testid="joblog-content"]';
  const longStart = 9000*126;
  const scrollerExpr = `(() => {
    const content = document.querySelector(${q(content)});
    if (!content) return null;
    return [...content.querySelectorAll('*'), content].find(n => ['auto','scroll'].includes(getComputedStyle(n).overflowY));
  })()`;
  const stateExpr = `(() => {
    const content = document.querySelector(${q(content)}), scroller = ${scrollerExpr};
    if (!content || !scroller) return null;
    const viewport = scroller.getBoundingClientRect();
    const rows = [...content.querySelectorAll('[data-joblog-kind]')].map(n => {
      const r = n.getBoundingClientRect();
      return {kind:n.dataset.joblogKind,start:Number(n.dataset.sourceStart),end:Number(n.dataset.sourceEnd),text:n.textContent,
        top:r.top-viewport.top,bottom:r.bottom-viewport.top,visible:r.bottom>viewport.top && r.top<viewport.bottom};
    });
    return {rows,first:rows.find(r=>r.kind==='output' && r.visible),
      older:Number(content.dataset.joblogOlderBytes),live:Number(content.dataset.joblogLiveBytes),union:Number(content.dataset.joblogSourceBytes),
      top:scroller.scrollTop,height:scroller.clientHeight,totalHeight:scroller.scrollHeight,
      following:scroller.scrollHeight-scroller.clientHeight-scroller.scrollTop<=4};
  })()`;
  const state = () => read(stateExpr);
  const outputCalls = (after = 0) => sent(after).filter(f => f.method === 'evener/jobs/output' && f.params.ref === fixture.outputOwnerRef && f.params.jobId === fixture.outputJobId);
  const page = request => response(request)?.result?.data;
  const bytes = page => page.encoding === 'base64' ? Buffer.from(page.data, 'base64') : Buffer.from(page.data, 'utf8');
  const checkPages = () => {
    const oracle = readFileSync(fixture.outputOraclePath);
    const pages = [];
    for (const request of outputCalls()) {
      const value = page(request);
      if (!value) continue;
      const raw = bytes(value);
      assert.equal(raw.length, value.bytesReturned);
      assert.deepEqual(raw, oracle.subarray(value.offsetBytes, value.offsetBytes + value.bytesReturned));
      assert.ok(value.retainedStartBytes <= value.offsetBytes);
      assert.ok(value.offsetBytes + value.bytesReturned <= value.totalBytes);
      if (request.params.beforeBytes !== undefined) {
        assert.equal(value.offsetBytes + value.bytesReturned, request.params.beforeBytes);
        assert.ok(value.bytesReturned <= request.params.maxBytes);
      } else assert.equal(value.offsetBytes + value.bytesReturned, value.totalBytes);
      pages.push({request, page:value});
    }
    save('raw-pages.json', pages);
    return pages;
  };
  const bounded = async () => {
    const value = await state();
    assert.ok(value.older <= 512*1024 && value.live <= 512*1024 && value.union <= 1024*1024, q(value));
    return value;
  };
  const captureOutput = async name => {
    checkPages();
    const value = await bounded();
    save(`${name}-output.json`, {state:value, frames, sockets, errors, consoleEvents});
    await driver.screenshot(name);
    driver.milestone(name, value);
    return value;
  };
  const wheel = async deltaY => {
    const point = await read(`(() => { const scroller=${scrollerExpr}; if (!scroller) return null; const r=scroller.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2,extent:scroller.scrollHeight+scroller.clientHeight}; })()`);
    assert.ok(point, 'actual output scroller');
    const {extent,...position}=point;
    await driver.send('Input.dispatchMouseEvent', {type:'mouseWheel',...position,deltaX:0,deltaY:Math.abs(deltaY)>=40000 ? Math.sign(deltaY)*extent : deltaY});
  };
  const latest = async (marker, after = 0, totalBytes = 0) => {
    await waitFrames(() => outputCalls(after).some(request => page(request)?.totalBytes >= totalBytes && bytes(page(request)).includes(Buffer.from(marker))), `real latest contains ${marker}`);
  };
  let phase = 1;
  const control = async command => {
    const ackPath = path.join(fixture.artifactDir, `phase-${String(++phase).padStart(2,'0')}.json`);
    return new Promise((resolve, reject) => {
      // Poll rather than fs.watch: the producer writes in place with os.WriteFile
      // and macOS coalesces directory events, so the only event can arrive while
      // the acknowledgement is still empty.
      const poll = setInterval(check, 100);
      const timer = setTimeout(() => finish(new Error(`producer did not acknowledge ${command}`)), 15000);
      function finish(error, value) { clearInterval(poll); clearTimeout(timer); error ? reject(error) : resolve(value); }
      function check() {
        let value;
        try { value = JSON.parse(readFileSync(ackPath,'utf8')); } catch { return; }
        if (value.command !== command) { finish(new Error(`wrong producer acknowledgement ${q(value)}`)); return; }
        finish(null,value);
      }
      driver.control(`output-${command}`);
      check();
    });
  };
  const assertAnchor = async baseline => {
    await wait(`(() => { const value=${stateExpr}; const row=value?.rows.find(row=>row.kind==='output' && row.start===${baseline.start}); return row?.visible && Math.abs(row.top-(${baseline.top}))<=2; })()`, 'same byte row stays within two pixels');
  };
  const glyphExpr = byte => `(() => {
    const scroller=${scrollerExpr};
    const node=[...document.querySelectorAll(${q(content)}+' [data-joblog-kind="output"]')].find(n=>Number(n.dataset.sourceStart)<=${byte} && Number(n.dataset.sourceEnd)>${byte});
    if (!node || !scroller) return null;
    let offset=${byte}-Number(node.dataset.sourceStart);
    const walker=document.createTreeWalker(node,NodeFilter.SHOW_TEXT);
    for(let text=walker.nextNode();text;text=walker.nextNode()) {
      if(offset<text.length) {
        const range=document.createRange(); range.setStart(text,offset); range.setEnd(text,offset+1);
        const r=range.getBoundingClientRect(),v=scroller.getBoundingClientRect();
        return {byte:${byte},top:r.top,bottom:r.bottom,viewportTop:v.top,viewportBottom:v.bottom,rowStart:Number(node.dataset.sourceStart)};
      }
      offset-=text.length;
    }
    return null;
  })()`;
  const assertAdjacentHistory = (requests, progress, message) => {
    for (const request of requests.slice(progress.checked)) {
      const value = page(request);
      if (progress.end !== undefined) assert.equal(value.offsetBytes, progress.end, message);
      progress.end = value.offsetBytes + value.bytesReturned;
      progress.checked++;
    }
  };
  let wrappedRepair;
  const backward = async (checkGlyph = false) => {
    const after = frames.length;
    const delivered=checkGlyph ? await read('window.__pagingWire.deliveredOutputs') : 0;
    if(!checkGlyph || !(await read('window.__pagingWire.sent.length'))) await wheel(-40000);
    if(checkGlyph) await wait(`window.__pagingWire.sent.length===1 || window.__pagingWire.deliveredOutputs>${delivered}`,'actual history response or exact held partial-line selector');
    if(checkGlyph && (await read('window.__pagingWire.sent.length'))) {
      const held=await read('window.__pagingWire.sent[0].params');
      const byte=held.beforeBytes;
      assert.ok(byte>longStart+11 && byte<longStart+65536,'history repair crosses the real long-line beginning');
      const before=await read(glyphExpr(byte));
      assert.ok(before && before.bottom>before.viewportTop && before.top<before.viewportBottom,'source glyph is visible before repair');
      await read('window.__pagingWire.holdReply=true');
      await read('(() => { const entry=window.__pagingWire.sent.shift(); entry.socket.releasePagingSend(entry.data); })()');
      await wait('window.__pagingWire.replies.length===1','hold the owner response before renderer publication');
      await read('(() => { const entry=window.__pagingWire.replies.shift(); entry.socket.callback(entry.event); })()');
      await wait(`(() => { const glyph=${glyphExpr(byte)}; return glyph && glyph.rowStart<${byte}; })()`,'partial source fragment has been structurally repaired');
      const afterGlyph=await read(glyphExpr(byte));
      wrappedRepair={before,after:afterGlyph,shift:afterGlyph.top-before.top,allowedShift:2};
      save('paging-wrapped-repair-glyph.json',wrappedRepair);
      assert.ok(Math.abs(wrappedRepair.shift)<=2,`source glyph ${byte} moved ${wrappedRepair.shift}px during wrapped repair, allowed 2px`);
    }
    await waitFrames(() => outputCalls(after).some(request => request.params.beforeBytes !== undefined && page(request)), 'trusted wheel fetches adjacent retained history, latest-only output cannot satisfy this');
    await bounded();
    return outputCalls(after).find(request => request.params.beforeBytes !== undefined && page(request));
  };
  await driver.click('[data-testid="statusbar"] button[aria-label^="Jobs,"]');
  // A row clicked mid-slide is measured where it is still moving; the press
  // then misses it (#3897 clips the slide's overhang, so nothing scrolls the
  // row back into view).
  await wait(`(() => { const s=document.querySelector(${q(sidebar)}); return s && getComputedStyle(s).transform === 'none'; })()`, 'Jobs sidebar entrance settled');
  await wait(`document.querySelector(${q(row(fixture.outputJobId))}) !== null`, 'separate real output owner is listed');
  await driver.click(row(fixture.outputJobId));
  await latest('SPLIT_é_');
  const initialGeometry = await state();
  const firstHistory = await backward();
  assert.ok(page(firstHistory).offsetBytes < page(outputCalls()[0]).offsetBytes);
  assert.equal(initialGeometry.following,true,'opening latest follows the real bottom');
  await wheel(40000);
  await wait(`${stateExpr}?.following === true`, 'trusted wheel returns to the real bottom');
  await control('grow');
  await latest('GROW_MARKER_1');
  await wait(`document.querySelector(${q(content)})?.textContent.includes('SPLIT_é_😀')`, 'split scalar repaired in real rows');
  assert.equal(await read(`document.querySelector(${q(content)}+' [data-ansi-fg="red"]')?.textContent`),'SGR_RED_é');
  assert.ok(await read(`document.querySelector(${q(content)})?.textContent.includes('OSC_VISIBLE')`));
  assert.ok(await read(`document.querySelector(${q(content)})?.textContent.includes('MALFORMED_�')`));
  checkPages();
  await driver.screenshot('paging-bottom');
  driver.milestone('paging-bottom',await state());

  // This is the behavioral RED against latest-only source: a native wheel must
  // reach history through the owner RPC, not merely move a bounded tail.
  await backward();
  for (let index = 0; index < 3; index++) await backward();
  let baseline = (await state()).first;
  assert.ok(baseline, 'later loaded page has a visible source-byte row');
  const historyAfter = frames.length;
  await control('middle');
  await latest('MIDDLE_MARKER', historyAfter);
  await assertAnchor(baseline);
  await captureOutput('paging-history');

  const remembered = baseline.start;
  for (let index = 0; index < 10; index++) await backward();
  assert.ok((await bounded()).older > 400*1024, 'older window crossed its raw trim threshold');
  const beforeRefetch = frames.length;
  const refetched = () => outputCalls(beforeRefetch).some(request => page(request) && page(request).offsetBytes <= remembered && page(request).offsetBytes+page(request).bytesReturned > remembered);
  const forwardHistory = () => outputCalls(beforeRefetch).filter(request => request.params.beforeBytes !== undefined && page(request));
  const forwardProgress = { checked: 0, end: undefined };
  for(let index=0;index<16 && !refetched();index++) {
    assertAdjacentHistory(forwardHistory(), forwardProgress, 'forward pages meet at the raw byte boundary');
    if (refetched()) break;
    save(`refetch-${index}-before.json`,await state());
    await wheel(40000);
    await waitFrames(()=>forwardHistory().length > forwardProgress.checked, 'forward wheel reads one adjacent retained page');
    assertAdjacentHistory(forwardHistory(), forwardProgress, 'forward pages meet at the raw byte boundary');
    save(`refetch-${index}-after.json`,await state());
  }
  assertAdjacentHistory(forwardHistory(), forwardProgress, 'forward pages meet at the raw byte boundary');
  assert.ok(refetched(),'forward wheel refetches the evicted retained row without leaping across unread bytes');
  await captureOutput('paging-refetch');

  // Walk the real retained gap in both directions. Independent page-oracle
  // equality below also pins pages that contain only ANSI controls.
  await read(`window.__pagingWire.holdRange={start:${longStart+11},end:${longStart+65536}}`);
  for (let index = 0; index < 80; index++) {
    if (outputCalls().some(request => page(request)?.offsetBytes <= longStart)) break;
    await backward(true);
  }
  assert.ok(wrappedRepair,'real journey observed a wrapped structural repair');
  assert.ok(outputCalls().some(request => {
    const value=page(request);
    return value && value.offsetBytes>=longStart+600*1024+20 && value.offsetBytes+value.bytesReturned<=longStart+600*1024+20+32768*4;
  }), 'actual controls-only page advances across the unloaded interval');
  await captureOutput('paging-gap');
  for (let index = 0; index < 80; index++) {
    const before = await state();
    if (before.rows.some(row => row.kind === 'output' && row.visible && row.start <= longStart && row.end > longStart)) break;
    assert.ok(before.first, 'loaded long-line history has a visible byte row');
    await wheel(before.first.start > longStart ? -before.height : before.height);
    await wait(`(() => { const value=${stateExpr}; return value && (value.top!==${before.top} || value.first?.start!==${before.first?.start} || value.first?.top!==${before.first?.top}); })()`, 'trusted wheel advances within loaded long-line history');
  }
  await wait(`(() => { const value=${stateExpr}; return value?.rows.some(row=>row.kind==='output' && row.text.includes('LONG_BEGIN_')); })()`, 'long-line beginning can be read after backward paging');
  assert.ok((await state()).rows.filter(row=>row.kind==='output').every(row=>row.end-row.start<=4096), 'long-line fragments remain bounded');
  await captureOutput('paging-text');

  baseline = (await state()).first;
  assert.ok(baseline);
  const liveBefore = checkPages().at(-1).page.totalBytes;
  for (let index = 0; index < 17; index++) {
    const after = frames.length;
    const ack = await control('grow');
    await latest('GROW_MARKER_2', after, ack.totalBytes);
    await assertAnchor(baseline);
    await bounded();
  }
  assert.ok(checkPages().at(-1).page.totalBytes - liveBefore > 512*1024, 'acknowledged contiguous live growth exceeds the live source-byte limit');
  assert.ok((await bounded()).live > 400*1024, 'real live window accumulates and trims contiguous pages');
  await captureOutput('paging-live-budget');
  const refreshAfter = frames.length;
  const refreshPoint=await read(`(() => { const root=document.querySelector(${q(content)}).closest('[role="tabpanel"]'); const button=[...root.querySelectorAll('[data-testid="pane-actions"] button')].find(n=>n.textContent==='Refresh'); const r=button.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`);
  await driver.clickAt(refreshPoint.x,refreshPoint.y);
  await waitFrames(() => outputCalls(refreshAfter).some(request => page(request)), 'Refresh performs real output read');
  await assertAnchor(baseline);
  await captureOutput('paging-refresh');
  let after = frames.length;
  let socketAfter = sockets.length;
  await driver.send('Network.emulateNetworkConditions',{offline:true,latency:0,downloadThroughput:-1,uploadThroughput:-1});
  assert.equal(await read(`(() => { const live=window.__backgroundJobsSockets.filter(s=>s.readyState===WebSocket.OPEN && new URL(s.url).pathname==='/rpc'); for(const s of live) s.close(4000,'paging transport boundary'); return live.length; })()`),1);
  await waitFrames(()=>sockets.slice(socketAfter).some(s=>s.method==='Network.webSocketClosed'),'real output socket closes');
  await driver.send('Network.emulateNetworkConditions',{offline:false,latency:0,downloadThroughput:-1,uploadThroughput:-1});
  await waitFrames(()=>outputCalls(after).some(request=>page(request)),'output resumes after real reconnect');
  await assertAnchor(baseline);
  await captureOutput('paging-reconnect');

  await read('window.__pagingWire.holdSend=true');
  await wheel(-40000);
  await wait('window.__pagingWire.sent.length===1','hold actual history send before owner dispatch');
  const held = await read('window.__pagingWire.sent[0].params');
  assert.ok(held.beforeBytes < (await control('rollover')).totalBytes-8*1024*1024);
  await read(`(() => { const entry=window.__pagingWire.sent.shift(); entry.socket.releasePagingSend(entry.data); })()`);
  await waitFrames(()=>outputCalls().some(request=>request.params.beforeBytes===held.beforeBytes && response(request)?.error?.code===-32014 && response(request)?.error?.data?.evenerErrorInfo==='jobOutputPruned'),'owner returns typed pruning for the actual held selector');
  await latest('ROLLOVER_MARKER');
  const rolloverPage = outputCalls().find(request => page(request) && bytes(page(request)).includes(Buffer.from('ROLLOVER_MARKER')));
  const liveStart = page(rolloverPage).offsetBytes;
  await wait(`(() => { const value=${stateExpr}; return value?.rows.some(row=>row.kind==='pruned') && value.rows.some(row=>row.kind==='output' && row.start<${(await control('grow')).totalBytes-8*1024*1024}); })()`, 'pruned notice preserves useful cached output below the floor');
  await captureOutput('paging-pruned');

  await read('window.__pagingWire.holdReply=true');
  await wait('window.__pagingWire.replies.length===1','hold preterminal real output response');
  const preterminal = outputCalls().at(-1);
  assert.ok(page(preterminal));
  await control('finish');
  await waitFrames(()=>sent().some(request=>request.method==='evener/jobs/get' && request.params.jobId===fixture.outputJobId && response(request)?.result?.data?.terminal),'terminal metadata observed while earlier output remains held');
  after=frames.length; socketAfter=sockets.length;
  await driver.send('Network.emulateNetworkConditions',{offline:true,latency:0,downloadThroughput:-1,uploadThroughput:-1});
  await read(`(() => { for(const s of window.__backgroundJobsSockets.filter(s=>s.readyState===WebSocket.OPEN)) s.close(4000,'fresh terminal drain boundary'); window.__pagingWire.replies.length=0; })()`);
  await waitFrames(()=>sockets.slice(socketAfter).some(s=>s.method==='Network.webSocketClosed'),'terminal drain connection closes');
  await driver.send('Network.emulateNetworkConditions',{offline:false,latency:0,downloadThroughput:-1,uploadThroughput:-1});
  await latest('FINAL_AFTER_PENDING_READ_é_😀',after);
  const finalPage = outputCalls(after).find(request=>page(request) && bytes(page(request)).includes(Buffer.from('FINAL_AFTER_PENDING_READ_é_😀')));
  assert.ok(page(finalPage).totalBytes > page(preterminal).totalBytes,'fresh drain reads bytes written after pending request');
  const finalLive = page(finalPage).totalBytes - liveStart;
  assert.ok(finalLive <= 512*1024, 'post-rollover contiguous interval fits the live budget');
  await wait(`${stateExpr}?.live === ${finalLive}`, 'fresh final bytes enter the retained live window');
  const finalHistoryAfter = frames.length;
  const finalHistoryPages = () => outputCalls(finalHistoryAfter).filter(request => request.params.beforeBytes !== undefined && page(request));
  const finalProgress = { checked: 0, end: undefined };
  const checkFinalHistory = () => {
    assertAdjacentHistory(finalHistoryPages(), finalProgress, 'finished-history pages remain adjacent');
  };
  for (let index = 0; index < 132; index++) {
    checkFinalHistory();
    if (finalProgress.end >= liveStart) break;
    await wheel(40000);
    await waitFrames(() => finalHistoryPages().length > finalProgress.checked, 'trusted wheel advances the retained post-rollover gap');
    checkFinalHistory();
    await bounded();
  }
  assert.ok(finalProgress.end >= liveStart, 'trusted paging reaches the independently captured live interval');
  await wait(`(() => { const value=${stateExpr}; return value?.rows.every(row => row.kind !== 'unloaded' || row.end !== ${liveStart}); })()`, 'retained gap joins the actual live rows');
  await wheel(40000);
  await wait(`document.querySelector(${q(content)})?.textContent.includes('FINAL_AFTER_PENDING_READ_é_😀')`,'literal final marker renders after drain recovery');
  await backward();
  await captureOutput('paging-final-drain');
  assert.deepEqual(errors.filter(event => event.type !== 'warning'),[],'complete paging journey has no page or console errors');
  await captureOutput('paging-post-errors');
  console.log('backgroundjobsguard: literal byte oracle, trusted paging, retention and final drain passed');
}
