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
const methods = new Set(["evener/thread/jobs/list", "evener/thread/activity/read", "evener/jobs/output", "thread/read"]);
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
    window.WebSocket = class extends Native {
      constructor(...args) { super(...args); window.__backgroundJobsSockets.push(this); }
    };
  })()` });
  await navigateTo(driver.page, fixture.url);
  await driver.openSession(fixture.rootRef);
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
  await waitFrames(() => sent(after).some(f => f.method === "evener/jobs/output" && f.params.ref === fixture.rootRef && f.params.jobId === fixture.laterJobId && response(f)?.result?.data?.tail.includes("BACKGROUND_LATE_FINAL")), "real output RPC targets the authoritative job owner");
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
} catch (error) {
  if (driver.page) await capture("failure").catch(captureError => save("capture-error.json", String(captureError)));
  save("failure.json", { message: String(error), stack: error.stack, frames, sockets, errors, consoleEvents });
  console.error(redact({ error: String(error), evidence: fixture.artifactDir }));
  process.exitCode = 1;
} finally {
  await driver.stop();
}
