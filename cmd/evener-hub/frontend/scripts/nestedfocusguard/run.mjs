import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { applyViewport, connectOnlyPage, evaluate, navigateTo } from "../browserGuardCdp.mjs";
import { startBrowserGuard, waitForBrowserReady } from "../browserGuardProcess.mjs";

const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const artifacts = process.env.DOCUMENT_FILE_LINKS_ARTIFACT_DIR;
assert(artifacts, "Go fixture must provide a retained artifact directory");
mkdirSync(artifacts, { recursive: true });
// Literal fixture expectations, not values derived from route placement.
const ownerRef = "local:02wMz5Txv1C3Hut0M8GCeB";
const childRef = "local:02wMz5TxvEMoJEDTDGOTil";
assert.equal(process.env.DOCUMENT_FILE_LINKS_REF, ownerRef);
assert.equal(process.env.DOCUMENT_FILE_LINKS_CHILD_REF, childRef);
const childPath = `/s/${encodeURIComponent(childRef)}`;
const q = JSON.stringify;
const result = { assertions: "fail", ownerRef, childRef };
const browserErrors = [];
let guard;
let page;

async function read(expression) {
  return evaluate(page.send, expression);
}
async function until(expression) {
  return read(`(async () => {
    const deadline = performance.now() + 15000;
    for (;;) {
      const value = (${expression});
      if (value) return value;
      if (performance.now() >= deadline) throw new Error(${q(`condition did not settle: ${expression}`)});
      await new Promise(resolve => requestAnimationFrame(resolve));
    }
  })()`);
}
const snapshot = "window.nestedFocusProbe.snapshot()";
const childAck = `s.trace.some(e => e.kind === 'post-appshell-effect' && e.detail.ref === ${q(childRef)})`;
const ownerRefocused = `(() => {
  const first = s.trace.find(e => e.kind === 'focus' && e.workspace.focusedRef === ${q(childRef)});
  return first && s.trace.some(e => e.seq > first.seq && e.kind === 'focus' && e.workspace.focusedRef === ${q(ownerRef)});
})()`;
function assertNoOwnerRefocus(observation) {
  const first = observation.trace.find((e) => e.kind === "focus" && e.workspace.focusedRef === childRef);
  assert(first, "page subscription must see the row action select the child");
  const unwanted = observation.trace.filter(
    (e) => e.seq > first.seq && e.kind === "focus" && e.workspace.focusedRef === ownerRef,
  );
  assert.deepEqual(unwanted, [], "owner focus was published after the row selected the child, see result.json trace");
  return first;
}
function assertPlacement(observation, ownerID, childID) {
  const panes = observation.workspace.panes;
  assert.equal(panes.length, 2, "only the real owner and one child exist");
  const owners = panes.filter((p) => p.type === "session" && p.ref === ownerRef);
  const children = panes.filter((p) => p.type === "session" && p.ref === childRef);
  assert.equal(owners.length, 1);
  assert.equal(children.length, 1);
  assert.equal(owners[0].id, ownerID, "original owner pane identity retained");
  assert.equal(owners[0].slot, "main");
  assert.equal(children[0].id, childID, "row-open child pane identity retained");
  assert.equal(children[0].slot, "secondary");
  assert.equal(children[0].parentRef, ownerRef, "literal immediate Back edge retained");
  assert.equal(observation.workspace.focusedPaneId, childID);
  assert.equal(observation.workspace.focusedRef, childRef);
}

try {
  guard = await startBrowserGuard({ frontend, profilePrefix: "nestedfocus-chrome-" });
  page = await connectOnlyPage(await waitForBrowserReady(guard));
  await page.send("Runtime.enable");
  page.ws.addEventListener("message", (event) => {
    const frame = JSON.parse(event.data);
    if (frame.method === "Runtime.exceptionThrown") browserErrors.push(frame.params.exceptionDetails);
  });
  await applyViewport(page.send, { width: 390, height: 844 });
  // Hold only the real child's location response. Release passes the original
  // native MessageEvent to the original listener, with no RPC/event fabrication.
  await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `(() => {
    const Native = window.WebSocket;
    const requests = new Set();
    const hold = window.__nestedFocusHold = {
      active: true, requests: [], replies: [],
      release() {
        if (!hold.replies.length) throw new Error('native child location response has not arrived');
        hold.active = false;
        for (const reply of hold.replies) {
          window.nestedFocusProbe.record('location-release', reply.evidence);
          reply.release();
        }
      },
      snapshot() { return { active: hold.active, requests: hold.requests, replies: hold.replies.map(r => r.evidence) }; },
    };
    window.WebSocket = class extends Native {
      send(data) {
        const request = JSON.parse(data);
        if (hold.active && request.method === 'evener/navigation/read' && request.params?.resource === 'location' && request.params.ref === ${q(childRef)}) {
          const evidence = { id: request.id, method: request.method, params: request.params };
          requests.add(request.id);
          hold.requests.push(evidence);
          window.nestedFocusProbe?.record('location-request', evidence);
        }
        return super.send(data);
      }
      set onmessage(listener) {
        super.onmessage = listener === null ? null : event => {
          const response = JSON.parse(event.data);
          if (hold.active && requests.has(response.id)) {
            const evidence = { id: response.id, nativeMessageEvent: event instanceof MessageEvent,
              trusted: event.isTrusted, result: response.result, error: response.error };
            hold.replies.push({ evidence, release: () => listener.call(this, event) });
            window.nestedFocusProbe?.record('location-held', evidence);
          } else listener.call(this, event);
        };
      }
      get onmessage() { return super.onmessage; }
    };
  })()`,
  });
  const url = new URL(`http://127.0.0.1:${guard.vitePort}/@fs/${frontend}/documentfilelinksharness.html`);
  url.searchParams.set("ref", ownerRef);
  url.searchParams.set("child", childRef);
  await navigateTo(page, `${url.origin}/auth/${process.env.DOCUMENT_FILE_LINKS_TOKEN}`);
  await navigateTo(page, url.href);
  await until("window.nestedFocusProbe?.observing && !!window.fileLinksFixture");
  await read("window.fileLinksFixture.ready");
  await until(`(() => {
    const s = ${snapshot};
    return s.trace.some(e => e.kind === 'post-appshell-effect' && e.detail.ref === ${q(ownerRef)}) &&
      s.dom.some(d => d.visible && d.composers.some(c => c.ref === ${q(ownerRef)} && c.visible && c.textbox));
  })()`);
  const before = await read(snapshot);
  result.before = before;
  assert.equal(before.viewport.width, 390);
  assert.equal(before.workspace.panes.length, 1, "no child pane or child deep link before the click");
  const owner = before.workspace.panes[0];
  assert.equal(owner.type, "session");
  assert.equal(owner.ref, ownerRef);
  assert.equal(owner.slot, "main");
  assert.equal(before.workspace.focusedPaneId, owner.id);
  assert.equal(before.pathname, `/s/${encodeURIComponent(ownerRef)}`);
  assert.equal(before.ownerLocation.data.ref, ownerRef);
  assert.equal(before.ownerLocation.data.top_level, true);
  assert.equal(before.childLocation, null, "child location has not been preloaded");
  assert.deepEqual((await read("window.__nestedFocusHold.snapshot()")).requests, []);

  const point = await until(`(() => {
    const button = document.querySelector('[data-nested-focus-open] button');
    if (!button || button.disabled) return null;
    const r = button.getBoundingClientRect();
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    return r.width > 0 && r.height > 0 && button.contains(document.elementFromPoint(x, y)) ? { x, y } : null;
  })()`);
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", button: "left", clickCount: 1, ...point });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", button: "left", clickCount: 1, ...point });
  await until(`(() => {
    const s = ${snapshot};
    return s.trace.some(e => e.kind === 'focus' && e.workspace.focusedRef === ${q(childRef)}) &&
      s.trace.some(e => e.kind === 'route' && e.pathname === ${q(childPath)}) && window.__nestedFocusHold.replies.length > 0;
  })()`);
  const opened = await read(snapshot);
  result.openedBeforeLocation = opened;
  result.transport = await read("window.__nestedFocusHold.snapshot()");
  const firstChildFocus = assertNoOwnerRefocus(opened);
  const child = firstChildFocus.workspace.panes.find((p) => p.type === "session" && p.ref === childRef);
  assert(child, "production row opener published a child conversation pane");
  assertPlacement(opened, owner.id, child.id);
  assert.equal(opened.pathname, childPath);
  assert.equal(opened.childLocation?.data ?? null, null, "route publication precedes child location settlement");
  const clicks = opened.trace.filter((e) => e.kind === "row-open-click");
  assert.equal(clicks.length, 1);
  assert.deepEqual(clicks[0].detail, { trusted: true, button: 0 });
  const childRoute = opened.trace.find((e) => e.kind === "route" && e.pathname === childPath);
  assert(firstChildFocus.seq < childRoute.seq, "child selection precedes StackHost route publication");
  assert.equal(result.transport.requests.length, 1);
  assert.equal(result.transport.requests[0].method, "evener/navigation/read");
  assert.equal(result.transport.requests[0].params.resource, "location");
  assert.equal(result.transport.requests[0].params.ref, childRef);
  assert.equal(result.transport.replies.length, 1);
  assert.equal(result.transport.replies[0].nativeMessageEvent, true);
  assert.equal(result.transport.replies[0].trusted, true);
  assert.equal(result.transport.replies[0].error ?? null, null);
  assert(result.transport.replies[0].result, "hub produced a real successful location reply");

  await read("window.__nestedFocusHold.release(); true");
  await until(`(() => { const s = ${snapshot}; return ${childAck} || ${ownerRefocused}; })()`);
  assertNoOwnerRefocus(await read(snapshot));
  await until(`(() => {
    const s = ${snapshot};
    return s.dom.some(d => d.paneId === ${q(child.id)} && d.visible &&
      d.scaffolds.includes(${q(`session:${childRef}`)}) &&
      d.composers.some(c => c.ref === ${q(childRef)} && c.visible && c.textbox)) || ${ownerRefocused};
  })()`);
  const final = await read(`window.nestedFocusProbe.record('final-dom'); ${snapshot}`);
  result.final = final;
  assert.equal(final.observing, true, "page subscription is still live through final DOM checks");
  assertPlacement(final, owner.id, child.id);
  assertNoOwnerRefocus(final);
  assert.deepEqual(
    final.workspace.panes.find((p) => p.id === owner.id),
    owner,
  );
  assert.deepEqual(
    final.workspace.panes.find((p) => p.id === child.id),
    opened.workspace.panes.find((p) => p.id === child.id),
  );
  assert.equal(final.pathname, childPath);
  assert.equal(final.childLocation.loading, false);
  assert.equal(final.childLocation.stale, false);
  assert.equal(final.childLocation.error, null);
  assert.notEqual(final.childLocation.loadedRevision, null);
  assert.equal(final.childLocation.data.ref, childRef);
  assert.equal(final.childLocation.data.top_level, false);
  assert.equal(
    final.childLocation.data.top_level_ref,
    ownerRef,
    "real navigation identifies the literal fixture owner",
  );
  const settled = final.trace.find(
    (e) =>
      e.kind === "location" &&
      e.detail.ref === childRef &&
      e.detail.resource?.data &&
      !e.detail.resource.loading &&
      !e.detail.resource.stale &&
      e.detail.resource.error === null,
  );
  const ack = final.trace.find(
    (e) =>
      e.kind === "post-appshell-effect" &&
      e.detail.ref === childRef &&
      e.detail.resource.identity === final.childLocation.identity,
  );
  const released = final.trace.find((e) => e.kind === "location-release");
  assert(settled, "page-side navigation subscription saw successful resource completion");
  assert(ack, "React sibling after AppShell acknowledged that successful location in its passive effect");
  assert.equal(ack.detail.sameResourceAsRender, true);
  assert.equal(ack.detail.renderPathname, childPath);
  assert.equal(ack.detail.resource.identity, settled.detail.resource.identity);
  assert.equal(ack.detail.resource.loadedRevision, settled.detail.resource.loadedRevision);
  assert(childRoute.seq < released.seq && released.seq < settled.seq && settled.seq < ack.seq);
  assert(
    ack.seq < final.trace.at(-1).seq,
    "observation extends beyond the passive-effect ack through final DOM checks",
  );
  const visible = final.dom.filter((d) => d.visible);
  assert.equal(visible.length, 1, "phone renders only the selected conversation");
  assert.equal(visible[0].paneId, child.id);
  assert(visible[0].scaffolds.includes(`session:${childRef}`));
  assert(visible[0].composers.some((c) => c.ref === childRef && c.visible && c.textbox));
  assert.deepEqual(browserErrors, [], "no uncaught page exceptions");
  result.assertions = "pass";
  result.ownerFocusReproduced = false;
  console.log(
    JSON.stringify({
      assertions: "pass",
      ownerFocusReproduced: false,
      childFocus: firstChildFocus.seq,
      childRoute: childRoute.seq,
      locationRelease: released.seq,
      locationSettled: settled.seq,
      postAppShellEffect: ack.seq,
      finalDOM: final.trace.at(-1).seq,
      ownerPane: owner.id,
      childPane: child.id,
    }),
  );
} catch (error) {
  result.failure = error.stack ?? String(error);
  throw error;
} finally {
  try {
    if (page) {
      result.observation = await read("window.nestedFocusProbe?.snapshot() ?? null");
      result.transportFinal = await read("window.__nestedFocusHold?.snapshot() ?? null");
      if (result.observation) {
        const first = result.observation.trace.find((e) => e.kind === "focus" && e.workspace.focusedRef === childRef);
        result.ownerFocusReproduced =
          !!first &&
          result.observation.trace.some(
            (e) => e.seq > first.seq && e.kind === "focus" && e.workspace.focusedRef === ownerRef,
          );
      }
    }
  } catch (error) {
    result.diagnosticFailure = String(error);
  } finally {
    result.browserErrors = browserErrors;
    writeFileSync(path.join(artifacts, "result.json"), JSON.stringify(result, null, 2));
    page?.close();
    await guard?.cleanup();
  }
}
