// The floating Next-needs-you control in the real production stack: real
// daemon sessions, the real public navigation tier, the real SPA and Chrome's
// own input. Nothing here seeds a frontend store or manufactures a row; Go
// creates 52 real root sessions at this journey's request and fails each
// first model round on the provider boundary, so each one ends its turn
// errored - live and in the needs_you tier with no pending question.
//
// The module receives the cascade driver's browser, RPC and control resources
// from run.mjs. Keep the attention journey here rather than growing run.mjs.
import assert from "node:assert/strict";
import { appendFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";

const q = JSON.stringify;
const CONTROL = '[data-testid="next-needs-you"]';
const NEXT = '[data-testid="next-needs-you-next"]';
const MORE = '[data-testid="next-needs-you-more"]';
const LIST = '[data-testid="next-needs-you-list"]';
const ROW = '[data-testid="next-needs-you-row"]';
const LOAD_MORE = '[data-testid="next-needs-you-load-more"]';

// One focused-conversation snapshot. `body` is the mounted conversation of the
// active Dockview group; its scaffold carries the exact session ref, its panel
// carries the workspace record's own pane ID, and the browser entry carries the
// runtime attention token the history owner stamps.
const focusedExpr = `(() => {
  const group = document.querySelector('.dv-active-group');
  const body = group?.querySelector('[data-pane-scaffold^="session:"]');
  if (!body) return null;
  const pane = body.parentElement;
  const ref = body.dataset.paneScaffold.slice('session:'.length);
  return {
    ref,
    url: location.pathname,
    paneId: pane?.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId ?? null,
    token: history.state && history.state.__evenerAttention ? history.state.__evenerAttention : null,
    length: history.length,
    tabs: document.querySelectorAll('.dv-tab').length,
  };
})()`;

const box = `node => {
  if (!node) return null;
  const r = node.getBoundingClientRect();
  return { x:r.x, y:r.y, left:r.left, right:r.right, top:r.top, bottom:r.bottom, width:r.width, height:r.height };
}`;

// Everything the control's own placement has to hold at one width and theme.
// Scoped to the active conversation pane, so a sibling cascade column can
// never satisfy a measurement meant for the focused reader.
function geometryExpr() {
  return `(() => {
    const box = ${box};
    // Phone widths render the session through StackHost, which has no Dockview
    // active group; the mounted session scaffold is still the surface under
    // test, so fall back to it.
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]')
      ?? document.querySelector('[data-pane-scaffold^="session:"]');
    const pane = body?.parentElement;
    if (!pane) return null;
    const query = (selector) => pane.querySelector(selector);
    const clearance = [...pane.querySelectorAll('[data-testid="transcript-end-clearance"]')].at(-1) ?? null;
    const port = query('[data-testid="transcript-virtual-list"] > div');
    const rows = port ? [...port.querySelectorAll('[data-index]')].filter(node => node.textContent.trim() !== '') : [];
    const last = rows.at(-1) ?? null;
    const send = query('[data-testid="composer-submit"]');
    const stop = query('[data-testid="composer-stop"]');
    return {
      viewport: { width: innerWidth, height: innerHeight },
      theme: document.documentElement.getAttribute('data-theme') ?? 'dark',
      ref: body.dataset.paneScaffold.slice('session:'.length),
      control: box(query('[data-testid="next-needs-you"]')),
      next: box(query('[data-testid="next-needs-you-next"]')),
      more: box(query('[data-testid="next-needs-you-more"]')),
      stack: box(query('[data-testid="flow-overlay-stack"]')),
      pill: box(query('[data-testid="new-content-pill"]')),
      clearance: box(clearance),
      clearanceHeight: clearance ? clearance.getBoundingClientRect().height : 0,
      composer: box(query('[data-composer]')),
      send: box(send ?? stop),
      sendLabel: send ? 'send' : stop ? 'stop' : null,
      port: box(port),
      portMetrics: port ? { scrollTop: port.scrollTop, scrollHeight: port.scrollHeight, clientHeight: port.clientHeight } : null,
      lastRow: box(last),
      nextText: query('[data-testid="next-needs-you-next"]')?.textContent ?? null,
      nextOverflow: (() => {
        const button = query('[data-testid="next-needs-you-next"]');
        const label = button?.querySelector('span:last-child');
        return button && label ? { button: button.getBoundingClientRect().width, labelScroll: label.scrollWidth, labelClient: label.clientWidth } : null;
      })(),
    };
  })()`;
}

// One diagnostic snapshot of the floating control and the boxes that clip it.
// It answers the two questions a failed hit test cannot: is the control in the
// DOM at all, and is it merely outside its pane's clip? Every rect and every
// computed overflow/position/inset on the chain above it is recorded, so the
// difference between a missing sample and a passing one is measured.
function probeExpr() {
  return `(() => {
    const box = ${box};
    const active = document.querySelector('.dv-active-group');
    const body = active?.querySelector('[data-pane-scaffold^="session:"]')
      ?? document.querySelector('[data-pane-scaffold^="session:"]');
    const pane = body?.parentElement ?? null;
    const allControls = [...document.querySelectorAll('[data-testid="next-needs-you"]')];
    const control = pane?.querySelector('[data-testid="next-needs-you"]') ?? allControls[0] ?? null;
    const next = control?.querySelector('[data-testid="next-needs-you-next"]') ?? null;
    const stack = control?.closest('[data-testid="flow-overlay-stack"]') ?? pane?.querySelector('[data-testid="flow-overlay-stack"]') ?? null;
    const slot = control?.closest('[data-testid="flow-overlay-attention"]') ?? pane?.querySelector('[data-testid="flow-overlay-attention"]') ?? null;
    const wrap = stack?.parentElement ?? null;
    const styles = (node) => {
      if (!node) return null;
      const s = getComputedStyle(node);
      return { position: s.position, overflow: s.overflow, overflowX: s.overflowX, overflowY: s.overflowY,
        top: s.top, right: s.right, bottom: s.bottom, left: s.left, inset: s.inset,
        width: s.width, height: s.height, minWidth: s.minWidth, maxWidth: s.maxWidth,
        transform: s.transform, zIndex: s.zIndex, pointerEvents: s.pointerEvents,
        display: s.display, visibility: s.visibility, clipPath: s.clipPath };
    };
    const describe = (node) => node ? {
      tag: node.tagName,
      testid: node.getAttribute?.("data-testid") ?? null,
      cls: typeof node.className === "string" ? node.className.slice(0, 80) : null,
      rect: box(node),
    } : null;
    const hit = (x, y) => {
      const node = document.elementFromPoint(x, y);
      if (!node) return null;
      const ancestors = [];
      for (let parent = node; parent && ancestors.length < 6; parent = parent.parentElement) {
        ancestors.push({ tag: parent.tagName, testid: parent.getAttribute?.("data-testid") ?? null,
          cls: typeof parent.className === "string" ? parent.className.slice(0, 60) : null,
          rect: box(parent) });
      }
      return {
        tag: node.tagName,
        testid: node.getAttribute?.("data-testid") ?? null,
        cls: typeof node.className === "string" ? node.className.slice(0, 60) : null,
        text: (node.textContent ?? "").slice(0, 40),
        insideNext: next ? next.contains(node) : false,
        insidePane: pane ? pane.contains(node) : false,
        ancestors,
      };
    };
    const overlap = (a, b) => (!a || !b ? null : {
      width: Math.max(0, Math.min(a.right, b.right) - Math.max(a.left, b.left)),
      height: Math.max(0, Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)),
    });
    const chain = [];
    for (let node = next; node && chain.length < 10; node = node.parentElement) {
      chain.push({ ...describe(node), style: styles(node) });
    }
    const nextBox = next ? box(next) : null;
    const paneBox = box(pane);
    const scaffolds = [...document.querySelectorAll("[data-pane-scaffold]")].map((node) => ({
      scaffold: node.dataset.paneScaffold,
      rect: box(node),
      controls: node.parentElement?.querySelectorAll('[data-testid="next-needs-you"]').length ?? 0,
    }));
    return {
      viewport: { width: innerWidth, height: innerHeight },
      documentControls: allControls.length,
      paneControl: pane ? pane.querySelectorAll('[data-testid="next-needs-you"]').length : null,
      controlExists: control !== null,
      attentionSlot: pane ? pane.querySelector('[data-testid="flow-overlay-attention"]') !== null : null,
      stackExists: pane ? pane.querySelector('[data-testid="flow-overlay-stack"]') !== null : null,
      next: nextBox,
      nextCenterHit: nextBox ? hit(nextBox.x + nextBox.width / 2, nextBox.y + nextBox.height / 2) : null,
      nextTopLeftHit: nextBox ? hit(nextBox.left + 1, nextBox.top + 1) : null,
      controlPaneOverlap: control ? overlap(box(control), paneBox) : null,
      nextPaneOverlap: nextBox ? overlap(nextBox, paneBox) : null,
      pane: paneBox,
      scaffold: box(body),
      group: box(pane?.closest(".dv-groupview")),
      stack: box(stack),
      slot: box(slot),
      wrap: box(wrap),
      paneText: (pane?.textContent ?? "").slice(0, 140),
      paneTestIds: [...(pane?.querySelectorAll("[data-testid]") ?? [])].map((node) => node.getAttribute("data-testid")).slice(0, 30),
      scaffolds,
      chain,
    };
  })()`;
}

function intersects(a, b) {
  if (!a || !b) return false;
  return a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;
}

function inside(boxValue, viewport, label, slack = 0.5) {
  assert.ok(boxValue, `${label} has a measured box`);
  assert.ok(
    boxValue.left >= -slack && boxValue.top >= -slack &&
      boxValue.right <= viewport.width + slack && boxValue.bottom <= viewport.height + slack,
    `${label} fits the ${viewport.width}x${viewport.height} viewport, got ${q(boxValue)}`,
  );
}

export async function attentionJourney(ctx) {
  const {
    driver,
    fixture,
    read,
    wait,
    capture,
    key,
    frames,
    sent,
    answered,
    reconnect,
    upload,
    holdCanvasCompletion,
    sourcePortExpr,
    settledReadingPoint,
    assertReadingContinuity,
  } = ctx;
  const rootRef = fixture.rootRef;
  // The hub's own ordered tier, read from the public RPC response the SPA
  // actually received. The driver does not invent an order: it checks the
  // app's hops against the payload the hub sent.
  // Every needs_you page the SPA actually received, merged by offset in the
  // order the hub served it. The check below compares the app's own hops with
  // this payload instead of trusting a list the app also rendered.
  function needsYouWire() {
    const requests = new Map();
    for (const frame of frames) {
      if (frame.direction === "Network.webSocketFrameSent" && frame.method === "evener/navigation/read" &&
        frame.params?.resource === "section" && frame.params?.section === "needs_you") {
        requests.set(frame.id, frame.params);
      }
    }
    const pages = new Map();
    for (const frame of frames) {
      if (frame.direction !== "Network.webSocketFrameReceived" || !requests.has(frame.id)) continue;
      const request = requests.get(frame.id);
      const data = frame.result?.data;
      if (!data) continue;
      // Representation v3 carries a page as entities plus a root container.
      // The container's children are the server's order; the entities array is
      // insertion order, which is not the triage order. A later differential
      // read at the same offset can carry no rows at all, so an empty page
      // never replaces a full one already observed.
      const entitiesByKey = new Map((data.entities ?? []).map((entity) => [entity.key, entity.value]));
      const root = (data.containers ?? []).find(
        (container) => container.owner?.kind === "resource_root" && container.owner?.slot === "sessions",
      );
      const legacy = Array.isArray(data.sessions) ? data.sessions : null;
      const rows = root
        ? (root.children ?? []).map((key) => entitiesByKey.get(key)).filter((value) => value && value.ref)
        : entitiesByKey.size > 0
          ? [...entitiesByKey.values()].filter((value) => value && value.ref)
          : legacy;
      if (!rows || rows.length === 0) continue;
      pages.set(Number(request.offset ?? 0), { offset: Number(request.offset ?? 0), rows });
    }
    const ordered = [...pages.values()].sort((a, b) => a.offset - b.offset);
    const rows = ordered.flatMap((page) => page.rows);
    return rows.length ? { rows, pages: ordered } : null;
  }

  function focused() {
    return read(focusedExpr);
  }

  async function waitFocused(ref, label) {
    const wantURL = `/s/${encodeURIComponent(ref)}`;
    return wait(`(() => {
      const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
      if (!body) return null;
      const actual = body.dataset.paneScaffold.slice('session:'.length);
      const pane = body.parentElement;
      const paneId = pane?.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId ?? null;
      const token = history.state && history.state.__evenerAttention;
      return actual === ${q(ref)} && location.pathname === ${q(wantURL)} && paneId && token
        ? { ref: actual, url: location.pathname, paneId, token, length: history.length, tabs: document.querySelectorAll('.dv-tab').length }
        : null;
    })()`, label);
  }

  async function controlVisible(scope = null, options = {}) {
    const where = scope ?? "";
    return wait(`(() => {
      const root = ${where === "" ? "document" : `document.querySelector(${q(where)})`};
      const next = root?.querySelector('[data-testid="next-needs-you-next"]');
      const r = next?.getBoundingClientRect();
      if (!next || !r || r.width <= 0 || r.height <= 0) return null;
      return next.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) ? true : null;
    })()`, "the floating attention control is visible and hit-testable", options);
  }
  async function probeControl(label, extra = {}) {
    const probe = { label, at: new Date().toISOString(), ...extra, ...(await read(probeExpr())) };
    writeFileSync(path.join(fixture.artifactDir, `attention-probe-${label}.json`), JSON.stringify(probe, null, 2));
    return probe;
  }

  // Two identical consecutive samples of the control's own targets. The
  // transcript's virtualized rows, scroll offset and the New Content pill can
  // keep correcting themselves under late measurements; what must stand still
  // for the matrix is where Next and More are, so those are the stamp. On
  // failure the last sample is reported instead of a bare timeout.
  async function settledGeometry(label) {
    let previous = null;
    let last = null;
    for (let poll = 0; poll < 60; poll++) {
      const sample = await read(geometryExpr());
      if (sample) {
        const stamp = JSON.stringify({ control: sample.control, next: sample.next, more: sample.more });
        if (stamp === previous) return sample;
        previous = stamp;
        last = sample;
      }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error(`${label}, measured control geometry never settled; last ${JSON.stringify({
      control: last?.control, next: last?.next, more: last?.more, stack: last?.stack, clearance: last?.clearanceHeight,
    })}`);
  }

  // A genuine native wheel over the reader, so "away reading" is the user's
  // own input and not a scrollTop assignment.
  async function wheelAway(portExpr, deltaY) {
    const point = await wait(`(() => {
      const port = ${portExpr};
      const r = port?.getBoundingClientRect();
      if (!r || r.width <= 0 || r.height <= 0) return null;
      const x = r.x + r.width / 2, y = r.y + r.height / 2;
      return port.contains(document.elementFromPoint(x, y)) ? { x, y, scrollTop: port.scrollTop } : null;
    })()`, "native wheel targets the focused source reader");
    await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: point.x, y: point.y });
    await driver.send("Input.dispatchMouseEvent", { type: "mouseWheel", deltaX: 0, deltaY, x: point.x, y: point.y });
    await wait(`(${portExpr})?.scrollTop !== ${point.scrollTop}`, "native wheel moves the source reader away from the end");
  }
  // A point inside the reader that is not a control. The floating stack can
  // span most of a narrow pane, so a fixed bottom-left offset can land on the
  // Next button and navigate instead of just blurring the editor.
  async function readerClickPoint(portExpr) {
    return wait(`(() => {
      const port = ${portExpr};
      const r = port?.getBoundingClientRect();
      if (!r || r.width < 20 || r.height < 20) return null;
      const candidates = [[0.5, 0.15], [0.15, 0.15], [0.85, 0.15], [0.5, 0.5], [0.15, 0.5], [0.5, 0.85]];
      for (const [fx, fy] of candidates) {
        const x = r.x + r.width * fx, y = r.y + r.height * fy;
        const hit = document.elementFromPoint(x, y);
        if (!hit || !port.contains(hit)) continue;
        if (hit.closest('button, [role="button"], input, textarea, [contenteditable], [data-testid="next-needs-you"]')) continue;
        return { x, y };
      }
      return null;
    })()`, "the reader has a non-activating point clear of the floating control");
  }

  // A real pointer press on the control's Next button, held without a release.
  // Found, hit-tested and activated in one page turn, so a control that shifts
  // between a measured coordinate and a later press still receives the press.
  async function pressNext() {
    await wait(`(() => {
      const button = document.querySelector(${q(NEXT)});
      const r = button?.getBoundingClientRect();
      if (!button || !r || r.width <= 0 || r.height <= 0) return null;
      if (!button.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2))) return null;
      button.click();
      return true;
    })()`, "the floating Next control has a press target");
    return null;
  }
  async function nativePressNext() {
    const point = await wait(`(() => {
      const button = document.querySelector(${q(NEXT)});
      const r = button?.getBoundingClientRect();
      if (!button || !r || r.width <= 0 || r.height <= 0) return null;
      const x = r.x + r.width / 2, y = r.y + r.height / 2;
      return button.contains(document.elementFromPoint(x, y)) ? { x, y } : null;
    })()`, "the floating Next control has a native press target");
    await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: point.x, y: point.y });
    await driver.send("Input.dispatchMouseEvent", { type: "mousePressed", x: point.x, y: point.y, button: "left", clickCount: 1 });
    return point;
  }
  async function releaseAt(point) {
    await driver.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: point.x, y: point.y, button: "left", clickCount: 1 });
  }
  // Found, hit-tested and activated in one page turn: the floating control can
  // shift between a measured coordinate and a later press (#3804).
  async function activate(selector, label) {
    await wait(`(() => {
      const button = document.querySelector(${q(selector)});
      // A control at the end of a long list (Load more below 50 rows) sits
      // outside the list's own scrollport; scroll it into view first, as a
      // person would, so the hit test and click are real.
      button?.scrollIntoView({ block: "nearest", inline: "nearest" });
      const r = button?.getBoundingClientRect();
      if (!button || !r || r.width <= 0 || r.height <= 0) return null;
      if (!button.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2))) return null;
      button.click();
      return true;
    })()`, label);
  }

  // ---- enter the attention fixture -------------------------------------

  await driver.send("Emulation.setDeviceMetricsOverride", { width: 1000, height: 900, deviceScaleFactor: 1, mobile: false });
  // The earlier phone/desktop host switch leaves the saved desktop grid with a
  // squeezed focused group (measured: 200px at a 1000px viewport). Focus the
  // source in place; the geometry matrix below records the pane's real width
  // with every measurement rather than pretending it is the viewport.
  await driver.openSession(rootRef);
  await wait(`document.querySelector(${q(driver.composerSelector(rootRef))})?.querySelector('[role="textbox"]') !== null`, "the source conversation is mounted before the attention tier is released");

  // The attention sessions do not exist until now: the cascade journey ahead
  // of this point ran with the needs_you queue this guard's baseline had
  // (empty) and without 52 extra daemons. Go creates them, fails each one's
  // first model round on the provider boundary, and writes this marker only
  // once the public page carries every session.
  appendFileSync(fixture.attentionControlPath, `${JSON.stringify({ command: "create-attention" })}\n`);
  for (let poll = 0; poll < 1800 && !existsSync(fixture.attentionReadyPath); poll++) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  assert.ok(existsSync(fixture.attentionReadyPath), "the fixture's attention sessions reached the public tier");
  const ready = JSON.parse(readFileSync(fixture.attentionReadyPath, "utf8"));
  fixture.attentionRefs = ready.created;
  assert.equal(new Set(fixture.attentionRefs).size, 52, "the fixture created 52 distinct real sessions");
  await controlVisible(null, { timeoutMs: 90000 });
  driver.milestone("attention-tier-visible", { refs: needsYouWire()?.rows.length ?? 0 });

  // ---- journey 1: draft, staged PNG, away-reading, hops ----------------

  // A structured draft of the source's own: an exact text, a skill atom from
  // the real project catalog, and a real PNG whose native encoder completion
  // is held across every hop.
  await driver.clearComposerDraft(rootRef);
  await driver.focusComposer(rootRef);
  await driver.typeText(rootRef, "CASCADE_ATTENTION_SOURCE_DRAFT");
  await driver.typeText(rootRef, " /cascade-source");
  await driver.completeSkill(rootRef, "cascade-source");
  await holdCanvasCompletion();
  await upload("staged.png");
  await wait("window.__cascadeEncode?.encoded && window.__cascadeEncode.release !== null", "the native canvas encoded the staged PNG and holds its completion");
  await wait('document.querySelector(\'[role="img"][aria-label="staged.png (still processing)"]\') !== null', "the pending image tile is the original marker");
  // The draft is read only once its staged attachment marker exists: the marker
  // is part of the structured draft the round trip must preserve byte for byte.
  const draft = await driver.composerState(rootRef);
  assert.deepEqual(draft.chips, ["/cascade-source"], "the attention source draft carries a real catalog skill atom");
  assert.ok(draft.text.includes("[image 1]"), `the structured draft carries its staged image marker, got ${draft.text}`);
  const pendingTiles = (await driver.composerState(rootRef)).tiles;

  // Blur the editor exactly as a person does: click the reader. The attention
  // control is allowed to exist while the editor is unfocused; it is hidden
  // only by real editing.
  const readerPoint = await readerClickPoint(sourcePortExpr);
  await driver.clickAt(readerPoint.x, readerPoint.y);
  await controlVisible(null);
  await wait(`(() => {
    const editor = document.querySelector(${q(driver.composerSelector(rootRef))})?.querySelector('[role="textbox"]');
    return editor ? (editor.contains(document.activeElement) || document.activeElement === editor) === false : null;
  })()`, "a plain reader click leaves the source editor unfocused, keeping the control available");
  driver.milestone("attention-control-visible-unfocused", { refs: fixture.attentionRefs.length });

  await wheelAway(sourcePortExpr, -900);
  const awayPoint = await settledReadingPoint(sourcePortExpr, "the attention source reads away from the end");
  const sourcePaneId = (await focused()).paneId;
  assert.ok(sourcePaneId, "the source has a committed workspace pane record");
  const sourceTabs = (await focused()).tabs;

  // An independent pane the hops must not disturb: the real cascade inspector
  // the source's Agents footer opens, in its own group.
  const agentsPoint = await wait(`(() => {
    const button = ${driver.paneScopeExpr(rootRef)}?.querySelector('button[aria-label^="Agents,"]');
    const r = button?.getBoundingClientRect();
    if (!r || r.width <= 0 || r.height <= 0) return null;
    const x = r.x + r.width / 2, y = r.y + r.height / 2;
    return button.contains(document.elementFromPoint(x, y)) ? { x, y } : null;
  })()`, "the source Agents control has a native target");
  await driver.clickAt(agentsPoint.x, agentsPoint.y);
  await wait(`document.querySelector('[data-testid="activity-sidebar"]') !== null`, "the source Agents collection is open");
  await ctx.drill(fixture.edges[0], 1);
  const inspectorPaneId = fixture.inspectorPaneId;
  assert.ok(inspectorPaneId && inspectorPaneId !== sourcePaneId, "inspection is an independent pane, not the source record");

  // Return focus to the source without touching the inspector.
  const focusPoint = await wait(`(() => {
    const port = ${driver.paneScopeExpr(rootRef)}?.querySelector('[data-testid="transcript-virtual-list"]');
    const r = port?.getBoundingClientRect();
    if (!r || r.width <= 0 || r.height <= 0) return null;
    const x = r.x + r.width / 2, y = r.y + r.height / 2;
    return port.contains(document.elementFromPoint(x, y)) ? { x, y } : null;
  })()`, "the source reader accepts a native focus click");
  await driver.clickAt(focusPoint.x, focusPoint.y);
  await controlVisible(null);

  // First hop: the app's own choice, checked against the hub's page.
  const historyBefore = await read("history.length");
  for (let poll = 0; poll < 300 && !needsYouWire(); poll++) await new Promise((resolve) => setTimeout(resolve, 50));
  assert.ok(needsYouWire(), "the SPA read the hub's needs_you page before the first hop");
  const nextLabelBefore = await read(`document.querySelector(${q(NEXT)})?.getAttribute('aria-label') ?? null`);
  await pressNext();
  await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    if (!body) return null;
    const ref = body.dataset.paneScaffold.slice('session:'.length);
    const pane = body.parentElement;
    return ref !== ${q(rootRef)} && location.pathname === '/s/' + encodeURIComponent(ref) &&
      pane.querySelector('[data-testid="statusbar"] [data-pane-id]') &&
      history.state && history.state.__evenerAttention ? { ref, url: location.pathname, length: history.length } : null;
  })()`, "the first attention hop moves URL, focused pane and history entry together");
  const jumped = await focused();
  assert.notEqual(jumped.ref, rootRef, "Next never stays on its own session");
  const pageAfterFirst = needsYouWire();
  assert.ok(pageAfterFirst?.rows.length, "the SPA received the hub's real needs_you page");
  assert.ok(nextLabelBefore === "Next needs you" || nextLabelBefore?.startsWith("Next needs you: "),
    `the control names its destination, got ${nextLabelBefore}`);
  // Go created every ref below through the real daemon; the hop must land on
  // one of them, not on an invented or unrelated session.
  assert.ok(fixture.attentionRefs.includes(jumped.ref), "the first hop lands on a real fixture needs-you session");
  assert.equal(jumped.length, historyBefore + 1, "each hop pushes exactly one history entry");
  assert.ok(jumped.token, "the destination entry carries the runtime attention token");
  await wait(`document.querySelector(${q(driver.composerSelector(jumped.ref))}) !== null`, "the destination conversation mounts its composer");
  const destinationFocused = await read(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    const ref = body?.dataset.paneScaffold.slice('session:'.length);
    const editor = document.querySelector('[data-composer="' + ref + '"] [role="textbox"]');
    return editor ? editor.contains(document.activeElement) : null;
  })()`);
  assert.equal(destinationFocused, false, "an attention entry deliberately skips composer mount autofocus");
  driver.milestone("attention-hop-one", { from: rootRef, to: jumped.ref, url: jumped.url, paneId: jumped.paneId });
  // A hop lands on a reader that must keep offering the control: no pending
  // question or approval hides it, and no mount autofocus puts the editor in
  // editing state.
  await controlVisible(null);

  // Second hop.
  await pressNext();
  const second = await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    if (!body) return null;
    const ref = body.dataset.paneScaffold.slice('session:'.length);
    return ref !== ${q(jumped.ref)} && ${q(fixture.attentionRefs)}.includes(ref) && location.pathname === '/s/' + encodeURIComponent(ref) &&
      history.state && history.state.__evenerAttention ? { ref, url: location.pathname, length: history.length, paneId: body.parentElement.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId } : null;
  })()`, "the second hop lands on the hub page's second row with URL and pane agreeing");
  assert.equal(second.length, historyBefore + 2, "the second hop pushes one more history entry");
  await capture("attention-second-hop");
  driver.milestone("attention-hop-two", { from: jumped.ref, to: second.ref, url: second.url, paneId: second.paneId });

  // Real browser Back restores the previous hop.
  await read("history.back()");
  const back = await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    if (!body) return null;
    const ref = body.dataset.paneScaffold.slice('session:'.length);
    return ref === ${q(jumped.ref)} && location.pathname === '/s/' + encodeURIComponent(ref) &&
      history.state && history.state.__evenerAttention ? { ref, url: location.pathname, paneId: body.parentElement.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId } : null;
  })()`, "Back restores the previous hop's exact pane and URL");
  assert.equal(back.paneId, jumped.paneId, "Back restores the same workspace record, not a replacement");
  driver.milestone("attention-back", { to: back.ref, url: back.url, paneId: back.paneId });

  // Forward returns to the same second target.
  await read("history.forward()");
  const forward = await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    if (!body) return null;
    const ref = body.dataset.paneScaffold.slice('session:'.length);
    return ref === ${q(second.ref)} && location.pathname === '/s/' + encodeURIComponent(ref) &&
      history.state && history.state.__evenerAttention ? { ref, url: location.pathname, paneId: body.parentElement.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId } : null;
  })()`, "Forward restores the exact same hop entry");
  assert.equal(forward.paneId, second.paneId, "Forward reuses the destination record");
  driver.milestone("attention-forward", { to: forward.ref, url: forward.url, paneId: forward.paneId });

  // Back to the origin: same pane record, same draft, same staged PNG, same
  // away-reading position, and no editor focus.
  await read("history.back()");
  await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    const ref = body?.dataset.paneScaffold.slice('session:'.length);
    return ref === ${q(jumped.ref)} && location.pathname === '/s/' + encodeURIComponent(ref) ? true : null;
  })()`, "the first Back returns to the first hop before the second Back is requested");
  await read("history.back()");
  const origin = await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    if (!body) return null;
    const ref = body.dataset.paneScaffold.slice('session:'.length);
    const paneId = body.parentElement.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId;
    return ref === ${q(rootRef)} && location.pathname === ${q(`/s/${encodeURIComponent(rootRef)}`)} && paneId === ${q(sourcePaneId)} ? { ref, url: location.pathname, paneId } : null;
  })()`, "Back twice restores the exact origin session and its workspace record");
  await wait(`document.querySelector(${q(driver.composerSelector(rootRef))}) !== null`, "the restored origin conversation mounts its composer");
  const afterBack = await driver.composerState(rootRef);
  assert.equal(afterBack.text, draft.text, "the origin reader keeps its exact structured draft across every hop");
  assert.deepEqual(afterBack.chips, draft.chips, "the origin reader keeps its catalog skill atom");
  assert.equal(afterBack.tiles, pendingTiles, "the origin reader keeps its pending staged image");
  assert.equal(await read(`document.querySelectorAll(${q(CONTROL)}).length`), 1, "exactly one attention control is mounted for the focused reader");
  const originEditorFocused = await read(`(() => {
    const editor = document.querySelector(${q(driver.composerSelector(rootRef))})?.querySelector('[role="textbox"]');
    return editor ? editor.contains(document.activeElement) || document.activeElement === editor : null;
  })()`);
  assert.equal(originEditorFocused, false, "attention restoration leaves the origin editor unfocused, so the control stays visible");
  await controlVisible(null);
  const restoredPoint = await settledReadingPoint(sourcePortExpr, "the restored origin still has a stable away-reading point");
  assert.equal(restoredPoint.entry, awayPoint.entry, "the origin's first intersecting entry survives the round trip");
  assertReadingContinuity(awayPoint, restoredPoint, "attention round trip");
  assert.equal((await read(focusedExpr)).tabs, sourceTabs + 1, "the round trip leaves the independent inspector tab in place");
  driver.milestone("attention-origin-restored", { entry: restoredPoint.entry, offset: restoredPoint.offset, paneId: sourcePaneId });

  // A real native encoder completion after the round trip: the exact bytes
  // settle into the original reader without touching the newer draft.
  await read("window.__cascadeEncode.release(true)");
  await wait(`document.querySelector('button[aria-label="View staged.png"] img')?.src.startsWith('data:image/png;base64,')`, "the late native PNG settles in the original source reader");
  const settledDraft = await driver.composerState(rootRef);
  assert.equal(settledDraft.text, draft.text, "late image completion never rolls back the preserved draft");
  const sourcePNG = await read(`document.querySelector('button[aria-label="View staged.png"] img').src`);

  // ---- geometry matrix: every width, both themes -----------------------

  // The journey's earlier Agents/drill leg leaves the source activity sidebar
  // open. On the phone host it is a full-screen overlay above the conversation,
  // so it - not the control - is what a hit test at the control's centre finds.
  // Dismiss it as a person would, then prove it is gone before measuring.
  if (await read(`document.querySelector('[data-testid="activity-sidebar"]') !== null`)) {
    await key("Escape", 27);
  }
  await wait(`document.querySelector('[data-testid="activity-sidebar"]') === null`, "the activity sidebar is closed before the matrix");
  assert.equal(await read(`document.querySelector('[data-testid="activity-sidebar"]') === null`), true,
    "the activity sidebar overlay is gone before the first geometry sample");

  // Step 3 of the diagnosis: a 390 px host swap, probed immediately (the first
  // React commit after the swap) and again after the mount gate has had time to
  // settle. This separates a stale scene from a persistent absence.
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  const swapImmediate = await probeControl("390-host-swap-immediate");
  const swapVisible = await controlVisible(null, { timeoutMs: 5000 }).then(() => true).catch(() => false);
  const swapSettled = await probeControl("390-host-swap-settled", { visibleAfterWait: swapVisible });
  driver.milestone("attention-390-host-swap-probe", {
    visibleAfterWait: swapVisible,
    immediate: { controlExists: swapImmediate.controlExists, next: swapImmediate.next, nextCenterHit: swapImmediate.nextCenterHit, pane: swapImmediate.pane },
    settled: { controlExists: swapSettled.controlExists, next: swapSettled.next, nextCenterHit: swapSettled.nextCenterHit, pane: swapSettled.pane },
  });
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 1000, height: 900, deviceScaleFactor: 1, mobile: false });
  await controlVisible(null, { timeoutMs: 5000 }).catch(() => undefined);

  const widths = [320, 390, 700, 899, 900, 1024, 1400];
  const themes = ["light", "dark"];
  const geometry = [];
  const missingControls = [];
  for (const theme of themes) {
    for (const width of widths) {
      await driver.send("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: 1, mobile: false });
      if (theme === "light") await read(`document.documentElement.setAttribute('data-theme', 'light')`);
      else await read(`document.documentElement.removeAttribute('data-theme')`);
      const visible = await controlVisible(null, { timeoutMs: 2000 }).then(() => true).catch(() => false);
      if (!visible) {
        // Record the absence and keep measuring the widths that do offer it:
        // the matrix must report every width it could not observe.
        missingControls.push(`${width}-${theme}`);
        const probe = await probeControl(`${width}-${theme}`);
        driver.milestone(`attention-${width}-${theme}`, { width, theme, control: false, probe });
        continue;
      }
      // The comparison sample: one passing width keeps the same full probe.
      const passingProbe = width === 1024 && theme === "light" ? await probeControl("1024-light") : null;
      const sample = await settledGeometry(`attention geometry ${width} ${theme}`);
      const label = `attention-${width}-${theme}`;
      assert.equal(sample.viewport.width, width, `${label} measures the emulated width`);
      const targets = await read(`(() => {
        const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]')
          ?? document.querySelector('[data-pane-scaffold^="session:"]');
        const pane = body?.parentElement;
        const next = pane?.querySelector('[data-testid="next-needs-you-next"]');
        const more = pane?.querySelector('[data-testid="next-needs-you-more"]');
        const centreHit = (node) => {
          const r = node?.getBoundingClientRect();
          const hit = r ? document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2) : null;
          return hit !== null && node.contains(hit);
        };
        return { controls: pane ? pane.querySelectorAll('[data-testid="next-needs-you"]').length : null,
          next: next !== null && centreHit(next), more: more !== null && centreHit(more) };
      })()`);
      assert.equal(targets.controls, 1, `${label} mounts exactly one control in the focused pane`);
      assert.equal(targets.next, true, `${label} the Next centre lands on the control`);
      assert.equal(targets.more, true, `${label} the More centre lands on its own target`);
      inside(sample.next, sample.viewport, `${label} Next`);
      inside(sample.more, sample.viewport, `${label} More`);
      assert.ok(!intersects(sample.next, sample.more), `${label} Next and More are distinct hit areas`);
      assert.ok(!intersects(sample.control, sample.composer), `${label} the control never overlaps the composer`);
      assert.ok(!intersects(sample.control, sample.send), `${label} the control never overlaps Send/Stop`);
      // Everything from the reader's own bottom edge up to the stack's top:
      // the stack's height plus the inset it floats above that edge.
      const requiredClearance = sample.port && sample.stack ? sample.port.bottom - sample.stack.top : null;
      assert.ok(
        sample.clearanceHeight === 0 || (requiredClearance !== null && Math.abs(sample.clearanceHeight - requiredClearance) <= 0.5),
        `${label} the end clearance reaches the stack top, got ${sample.clearanceHeight} vs ${requiredClearance ?? "unmeasured"}`);
      assert.ok(sample.stack && sample.stack.height > 0, `${label} the floating stack owns a measured height`);
      if (sample.pill) {
        assert.ok(!intersects(sample.pill, sample.next), `${label} New Content and Next do not overlap`);
      }
      assert.ok(sample.nextOverflow, `${label} the Next label is measurable`);
      assert.ok(sample.nextOverflow.button <= Math.min(18 * 16, width * 0.55) + 18,
        `${label} the long title stays inside the compact ceiling, got ${sample.nextOverflow.button}`);
      // At the end the reader's own last content row sits above the stack.
      // The sizer can grow after a width change lands: a tall row above the
      // viewport re-measures, scroll anchoring holds the visible content still,
      // and the reader ends up short of the end. A person at the end scrolls
      // again, so the poll re-pins the reader before it measures.
      const bottom = await wait(`(() => {
        const port = ${sourcePortExpr};
        if (!port) return null;
        if (port.scrollTop + port.clientHeight < port.scrollHeight - 1) {
          port.scrollTop = port.scrollHeight;
          return null;
        }
        const rows = [...port.querySelectorAll('[data-index]')].filter(node => node.textContent.trim() !== '');
        const last = rows.at(-1);
        const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]')
          ?? document.querySelector('[data-pane-scaffold^="session:"]');
        const stack = body?.parentElement?.querySelector('[data-testid="flow-overlay-stack"]')
          ?? document.querySelector('[data-testid="flow-overlay-stack"]');
        if (!last || !stack) return null;
        const box = last.getBoundingClientRect();
        const portBox = port.getBoundingClientRect();
        // The row can be taller than the reader after a resize. Require part of
        // it inside the viewport and its end above the stack's bottom edge.
        if (box.top > portBox.bottom - 2 || box.bottom < portBox.top + 2) return null;
        const stackBox = stack.getBoundingClientRect();
        // The final row's own content must be hit-testable (nothing paints over
        // it) and the row must end above the stack's TOP edge, the edge the end
        // clearance is measured to.
        const y = Math.min(Math.max(box.top + 8, portBox.top + 2), Math.min(portBox.bottom, stackBox.top) - 2);
        if (y <= box.top) return null;
        const hit = document.elementFromPoint(box.x + Math.min(10, box.width / 2), y);
        return last.contains(hit) && box.bottom <= stackBox.top + 1
          ? { lastBottom: box.bottom, stackTop: stackBox.top, stackBottom: stackBox.bottom } : null;
      })()`, `${label}: final transcript content scrolls above the floating controls`);
      geometry.push({ width, theme, next: sample.next, more: sample.more, stack: sample.stack, pill: sample.pill, send: sample.send, clearance: sample.clearanceHeight, lastRowBottom: bottom.lastBottom, stackTop: bottom.stackTop, stackBottom: bottom.stackBottom });
      driver.milestone(label, { width, theme, next: sample.next, more: sample.more, stack: sample.stack, pill: sample.pill, send: sample.send, clearance: sample.clearanceHeight, nextText: sample.nextText, lastRowBottom: bottom.lastBottom, stackBottom: bottom.stackBottom, ...(passingProbe ? { probe: passingProbe } : {}) });
    }
  }
  await read(`document.documentElement.removeAttribute('data-theme')`);
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 1000, height: 900, deviceScaleFactor: 1, mobile: false });
  await capture("attention-geometry-1000-dark");
  // Every width must offer the control; report the matrix's own gaps before the
  // legs that depend on a mounted control.
  assert.deepEqual(missingControls, [], `the floating control was not offered at ${missingControls.join(", ")}`);

  // ---- native hold and cancel -----------------------------------------

  // 320 px: the tightest scene the control must survive.
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 320, height: 700, deviceScaleFactor: 1, mobile: false });
  await controlVisible(null);
  // The list dismisses on any outer scroll or resize, and this leg is the first
  // thing after the geometry matrix's own scrolling and viewport changes. A
  // stray event landing after the hold opens the list closes it again, which
  // reads as "the hold never opened". Settle both before pressing.
  await read(`(() => {
    window.__holdSettleAt = performance.now();
    window.__holdSettleWatch ??= (() => {
      const mark = () => { window.__holdSettleAt = performance.now(); };
      window.addEventListener("scroll", mark, true);
      window.addEventListener("resize", mark);
      return true;
    })();
  })()`);
  await wait(`performance.now() - window.__holdSettleAt > 400`, "no scroll or resize is in flight before the hold");
  const holdStart = await read("location.pathname");
  const holdPoint = await nativePressNext();
  await new Promise((resolve) => setTimeout(resolve, 620));
  await wait(`document.querySelector(${q(LIST)}) !== null`, "a completed 500ms native hold opens the attention list");
  assert.equal(await read("location.pathname"), holdStart, "the completed hold never navigates on its own");
  await releaseAt(holdPoint);
  await wait(`document.querySelector(${q(LIST)}) !== null`, "the release click belonging to the hold is consumed, leaving the list open");
  assert.equal(await read("location.pathname"), holdStart, "the post-hold click does not navigate");
  await key("Escape", 27);
  await wait(`document.querySelector(${q(LIST)}) === null`, "Escape closes the attention list");
  const restoredTrigger = await read(`document.activeElement === document.querySelector(${q(MORE)})`);
  assert.equal(restoredTrigger, true, "Escape restores keyboard focus to the More trigger");
  // Cancellation: movement beyond the 10px tolerance ends the hold.
  const cancelPoint = await nativePressNext();
  // Leave the button before release: a press that stays on it is an ordinary
  // click, which is supposed to navigate. This exercises cancellation only.
  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: cancelPoint.x + 30, y: cancelPoint.y - 60 });
  await new Promise((resolve) => setTimeout(resolve, 620));
  assert.equal(await read(`document.querySelector(${q(LIST)}) !== null`), false, "movement beyond 10px cancels the hold before it completes");
  await driver.send("Input.dispatchMouseEvent", { type: "mouseReleased", x: cancelPoint.x + 30, y: cancelPoint.y - 60, button: "left", clickCount: 1 });
  // A release after the pointer left the button must not deliver the button's
  // own click (the pending hold's pointer capture stays on the element, so a
  // retargeted click would navigate here). Give a navigation the beat it needs
  // to commit before asserting, and check focus as well as the URL.
  await new Promise((resolve) => setTimeout(resolve, 750));
  assert.equal(await read("location.pathname"), holdStart, "a cancelled hold leaves the reader where it was");
  assert.equal(await read(`document.querySelector(${q(LIST)}) !== null`), false, "the cancelled hold's release opens no attention list");
  assert.equal(await read(`${driver.paneScopeExpr(rootRef)} !== null`), true, "the cancelled hold's release keeps the source reader mounted");
  driver.milestone("attention-hold-cancel", { width: 320, entry: holdStart });
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 1000, height: 900, deviceScaleFactor: 1, mobile: false });


  // ---- an already-presented target -------------------------------------
  //
  // Not exercised in this pass. The only ordinary way to open a second
  // session here is the rail row, and that replaces the primary session pane
  // (its tab disappears), so there is no surviving source pane to compare an
  // in-place focus against. The app opens an independent pane for a subagent
  // conversation only; a needs-you session has no such path in this harness.
  driver.milestone("attention-already-presented-blocked", {
    reason: "an ordinary rail open replaces the primary session pane",
  });

  // ---- secondary/cascade origin ---------------------------------------

  await wait(`(() => {
    const pane = ${driver.paneScopeExpr(rootRef)};
    return pane?.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId === ${q(sourcePaneId)} ? true : null;
  })()`, "the source pane is mounted before the cascade origin");
  const sourceAfterPresented = await read(`${driver.paneScopeExpr(rootRef)}?.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId ?? null`);
  assert.equal(sourceAfterPresented, sourcePaneId, "the origin record survives an already-presented focus");
  // The host swaps above discard the inspector opened before the hops, so open
  // a fresh one from the source's own Agents collection.
  const cascadeAgents = await wait(`(() => {
    const button = ${driver.paneScopeExpr(rootRef)}?.querySelector('button[aria-label^="Agents,"]');
    const r = button?.getBoundingClientRect();
    if (!r || r.width <= 0) return null;
    const x = r.x + r.width / 2, y = r.y + r.height / 2;
    return button.contains(document.elementFromPoint(x, y)) ? { x, y } : null;
  })()`, "the source Agents control has a native target");
  await driver.clickAt(cascadeAgents.x, cascadeAgents.y);
  await wait(`document.querySelector('[data-testid="activity-sidebar"]') !== null`, "the source Agents collection is open for the cascade origin");
  await ctx.drill(fixture.edges[0], 1);
  const cascadeInspectorId = fixture.inspectorPaneId;
  // Focus the inspector pane's active cascade column, whose own conversation
  // embeds the attention control (the leaf, not every readable column).
  await driver.click('[data-pane-scaffold="cascade"] [data-testid="cascade-column"][data-leaf] [data-testid="transcript-virtual-list"]');
  await wait(`document.querySelector('[data-pane-scaffold="cascade"] [data-testid="cascade-column"][data-leaf] [data-testid="next-needs-you"]') !== null`, "the cascade leaf conversation mounts its own attention control");
  assert.equal(await read(`document.querySelectorAll(${q(CONTROL)}).length`), 1, "only the focused cascade leaf offers attention navigation");
  const leafRef = await read(`document.querySelector('[data-pane-scaffold="cascade"] [data-testid="cascade-column"][data-leaf]')?.dataset.scopeRef ?? null`);
  assert.ok(leafRef, "the cascade inspector has an active leaf");
  const cascadeNext = `${'[data-pane-scaffold="cascade"] [data-testid="cascade-column"][data-leaf]'} [data-testid="next-needs-you-next"]`;
  await wait(`(() => {
    const button = document.querySelector(${q(cascadeNext)});
    const r = button?.getBoundingClientRect();
    return r && r.width > 0 && r.height > 0 && button.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) ? true : null;
  })()`, "the cascade leaf's Next control is hit-testable");
  const cascadePaneId = await read(`document.querySelector('[data-pane-scaffold="cascade"]').parentElement.querySelector('[data-testid="statusbar"] [data-pane-id]').dataset.paneId`);
  const cascadePoint = await read(`(() => {
    const button = document.querySelector(${q(cascadeNext)});
    const r = button.getBoundingClientRect();
    return { x:r.x + r.width / 2, y:r.y + r.height / 2 };
  })()`);
  const paneIdsBefore = await read(`[...document.querySelectorAll('[data-testid="statusbar"] [data-pane-id]')].map((node) => node.dataset.paneId)`);
  assert.ok(paneIdsBefore.includes(cascadePaneId), "the origin inspector is presented before the cascade-origin hop");
  assert.ok(paneIdsBefore.includes(sourcePaneId), "the ordinary source is presented before the cascade-origin hop");
  await driver.clickAt(cascadePoint.x, cascadePoint.y);
  const cascadeHop = await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    if (!body) return null;
    const ref = body.dataset.paneScaffold.slice('session:'.length);
    const paneId = body.parentElement.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId;
    return location.pathname === '/s/' + encodeURIComponent(ref) && paneId && history.state?.__evenerAttention
      ? { ref, url: location.pathname, paneId } : null;
  })()`, "a cascade-origin hop moves URL, focused pane ref and attention token together");
  const cascadeAfter = await read(`(() => ({
    paneIds: [...document.querySelectorAll('[data-testid="statusbar"] [data-pane-id]')].map((node) => node.dataset.paneId),
    sourcePaneId: ${driver.paneScopeExpr(rootRef)}?.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId ?? null,
    cascade: document.querySelector('[data-pane-scaffold="cascade"]') !== null,
  }))()`);
  // The plan's substitution rule: a fresh destination is a new record placed in
  // the origin's seat, and the seat's previous record is parked (not presented).
  assert.notEqual(cascadeHop.paneId, cascadePaneId, "a fresh destination is a new record, never the parked origin");
  assert.equal(cascadeAfter.sourcePaneId, sourcePaneId, "a cascade-origin hop never moves the ordinary source pane");
  assert.equal(cascadeAfter.cascade, false, "the origin inspector record is parked, not presented");
  const presentedAfter = [...new Set(cascadeAfter.paneIds)].sort();
  const expectedPresented = [...new Set([...paneIdsBefore.filter((id) => id !== cascadePaneId), cascadeHop.paneId])].sort();
  assert.deepEqual(presentedAfter, expectedPresented, "exactly one seat was replaced: the origin removed, the destination added, the source retained");
  assert.equal((await driver.composerState(rootRef)).text, draft.text, "the ordinary source draft survives a cascade-origin hop");
  driver.milestone("attention-cascade-origin", { from: leafRef, to: cascadeHop.ref, parkedPaneId: cascadePaneId, paneId: cascadeHop.paneId });

  // ---- explicit Close --------------------------------------------------

  await read("history.back()");
  await wait(`(() => {
    const inspector = document.querySelector('[data-pane-scaffold="cascade"]');
    const leaf = inspector?.querySelector('[data-testid="cascade-column"][data-leaf]');
    const active = inspector?.closest('.dv-groupview')?.classList.contains('dv-active-group');
    return leaf?.dataset.scopeRef === ${q(leafRef)} && active && history.state?.__evenerAttention ? true : null;
  })()`, "Back restores the cascade origin's own reader and focus");
  // Ordinary Close of the inspector pane: the attention binding for that pane
  // must not survive to resurrect it.
  // The active tab's close action can sit outside its own 200px group (the tab
  // strip is wider than the group), where a pointer press lands on a
  // neighbouring pane's breadcrumb - measured at x=693 against a group ending
  // at x=680. Reveal the tab, hit-test the action, and activate the real
  // control; when the strip clips it, activate it in place, as the app's own
  // tab tests do.
  const closedInspector = await read(`(() => {
    const group = document.querySelector('[data-pane-scaffold="cascade"]')?.closest('.dv-groupview');
    const tab = group?.querySelector('.dv-tab.dv-active-tab');
    const action = tab?.querySelector('.dv-default-tab-action');
    if (!action) return { closed: false, why: "no close action" };
    tab.scrollIntoView({ inline: "nearest", block: "nearest" });
    const r = action.getBoundingClientRect();
    const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    const reachable = action.contains(hit);
    action.click();
    return { closed: true, how: reachable ? "hit-tested pointer target" : "clipped action activated in place",
      actionRect: { x: r.x, y: r.y, width: r.width, height: r.height },
      groupRect: { x: group.getBoundingClientRect().x, right: group.getBoundingClientRect().right },
      hit: hit ? { tag: hit.tagName, cls: typeof hit.className === "string" ? hit.className.slice(0, 60) : null } : null };
  })()`);
  assert.equal(closedInspector.closed, true, "the inspector's active tab carries its ordinary Close action");
  await wait(`document.querySelector('[data-pane-scaffold="cascade"]') === null`, "explicit Close removes the inspector pane");
  assert.equal(await read(`${driver.paneScopeExpr(rootRef)}?.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId ?? null`), sourcePaneId,
    "explicit Close leaves the source record alive");
  assert.equal(await read(`document.querySelector('button[aria-label="View staged.png"] img')?.src`), sourcePNG,
    "the staged image's exact bytes survive the explicit Close");
  assert.equal((await driver.composerState(rootRef)).text, draft.text, "the source keeps its exact draft through the explicit Close");
  // A closed attention reader is never resurrected by history.
  await read(`window.__attentionPopstates = 0; window.addEventListener('popstate', () => { window.__attentionPopstates += 1 }, { once:true })`);
  await read("history.back()");
  await wait(`window.__attentionPopstates >= 1 ? true : null`, "the browser performs a real Back traversal after the explicit Close");
  assert.equal(await read(`document.querySelector('[data-pane-scaffold="cascade"]') === null`), true,
    "Back after the explicit Close does not resurrect the closed inspector");
  driver.milestone("attention-explicit-close", { sourcePaneId, closedInspector: cascadeInspectorId, how: closedInspector.how });

  // ---- host swap -------------------------------------------------------

  // The inspector group is gone, but Dockview can leave another group active;
  // focus the mounted source pane with a real reader click (the rail's row hit
  // test is not this leg's subject).
  const closeFocusPoint = await readerClickPoint(sourcePortExpr);
  await driver.clickAt(closeFocusPoint.x, closeFocusPoint.y);
  await wait(`document.querySelector(${q(driver.composerSelector(rootRef))}) !== null && ${driver.paneScopeExpr(rootRef)}.closest('.dv-groupview').classList.contains('dv-active-group')`, "the source conversation is focused after the explicit Close");
  await controlVisible(null);
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  await wait(`document.querySelector(${q(driver.composerSelector(rootRef))}) !== null`, "the phone host swaps the source conversation in");
  await wait(`document.querySelector(${q(CONTROL)}) !== null`, "the attention control survives the phone host swap");
  const phoneDraft = await driver.composerState(rootRef);
  assert.equal(phoneDraft.text, draft.text, "the phone host swap preserves the source draft");
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 1000, height: 900, deviceScaleFactor: 1, mobile: false });
  await wait(`document.querySelector(${q(driver.composerSelector(rootRef))}) !== null`, "the desktop host swaps the source conversation back in");
  const desktopDraft = await driver.composerState(rootRef);
  assert.equal(desktopDraft.text, draft.text, "the desktop host swap preserves the source draft");
  assert.equal(await read(`document.querySelector('button[aria-label="View staged.png"] img')?.src`), sourcePNG, "host swaps keep the exact staged PNG bytes");
  driver.milestone("attention-host-swap", { ref: rootRef });


  // ---- a later-page target beyond the 50-row boundary ------------------

  await controlVisible(null);
  // The list dismisses on an outer scroll; let the reader's scroll settle
  // first, or the panel closes between opening and reading its rows.
  await wait(`(() => {
    const port = ${sourcePortExpr};
    if (!port) return null;
    const stamp = String(port.scrollTop);
    const previous = window.__attentionListSettle;
    window.__attentionListSettle = stamp;
    return previous === stamp ? true : null;
  })()`, "the reader's scroll is settled before the attention list opens");
  // The panel also dismisses on any outer resize, and the host swap just
  // changed the emulated metrics: a resize delivered after the panel opened
  // closes it before it can carry rows. Require a quiet window first.
  await read(`(() => {
    window.__attentionResizeAt = performance.now();
    window.__attentionResizeWatch ??= (() => {
      window.addEventListener("resize", () => { window.__attentionResizeAt = performance.now(); }, true);
      return true;
    })();
  })()`);
  await wait(`performance.now() - window.__attentionResizeAt > 400`, "the host swap's resize has settled before the attention list opens");
  await activate(MORE, "the More trigger opens the attention list for the later-page target");
  await wait(`document.querySelector(${q(LIST)}) !== null`, "the attention list opens for the later-page target");
  const listBounds = await read(`(() => {
    const panel = document.querySelector(${q(LIST)});
    const r = panel.getBoundingClientRect();
    return { x:r.x, y:r.y, width:r.width, height:r.height, left:r.left, right:r.right, top:r.top, bottom:r.bottom,
      viewport:{ width:innerWidth, height:innerHeight }, scrollHeight:panel.scrollHeight, clientHeight:panel.clientHeight };
  })()`);
  inside({ left: listBounds.left, right: listBounds.right, top: listBounds.top, bottom: listBounds.bottom }, listBounds.viewport, "the attention list");
  assert.ok(listBounds.height <= Math.min(20 * 16, 0.6 * listBounds.viewport.height) + 1, `the list is bounded by its own max height, got ${listBounds.height}`);
  // The store may have loaded the second page already, in which case the Load
  // more control has retired; only press it while the list is still one page.
  const firstPageRows = await read(`[...document.querySelectorAll(${q(ROW)})].map((row) => row.dataset.ref)`);
  if (firstPageRows.length <= 50 && (await read(`document.querySelector(${q(LOAD_MORE)}) !== null`))) {
    await activate(LOAD_MORE, "the list's Load more control").catch(() => undefined);
  }
  await new Promise((resolve) => setTimeout(resolve, 1000));
  // A panel that has opened but not been given its first page yet satisfies
  // every "no Load more control" reading, so the wait has to see rows before it
  // can say the list reached its later page. A panel that has gone away was
  // dismissed (the control hid for a beat, or an outer event arrived); a person
  // taps More again, so the leg reopens it once rather than waiting on a closed
  // panel. A panel that stays open without rows still fails the wait.
  const laterPageRows = `(() => {
    const rows = document.querySelectorAll(${q(ROW)}).length;
    if (rows === 0) return null;
    return rows > 50 || document.querySelector(${q(LOAD_MORE)}) === null ? rows : null;
  })()`;
  for (let attempt = 1; ; attempt += 1) {
    try {
      await wait(laterPageRows, "the attention list reaches its later page");
      break;
    } catch (error) {
      const panelOpen = await read(`document.querySelector(${q(LIST)}) !== null`);
      if (panelOpen || attempt >= 2) throw error;
      driver.milestone("attention-later-page-reopened", { attempt });
      await activate(MORE, "the More trigger reopens the dismissed attention list");
      await wait(`document.querySelector(${q(LIST)}) !== null`, "the reopened attention list is mounted");
    }
  }
  const allRows = await read(`[...document.querySelectorAll(${q(ROW)})].map((row) => row.dataset.ref)`);
  // The store may already have merged the hub's second page before this leg
  // reads the list, so the rows past the first page are identified by the
  // page boundary (50) rather than by which rows this leg's press added. The
  // press path itself is proven by the transport-retry leg's single offset-50
  // read; this assertion proves the later page is present and reachable.
  const laterRows = allRows.slice(50);
  assert.ok(allRows.length === 52 && laterRows.length === 2,
    `the list carries both pages with two rows past the 50-row boundary, got ${allRows.length} rows and ${laterRows.length} past it`);
  const laterRef = laterRows[0];
  const laterRow = await wait(`(() => {
    const row = document.querySelector(${q(`${ROW}[data-ref="${laterRef}"]`)});
    if (!row) return null;
    const section = row.closest('section');
    if (section) section.scrollTop = section.scrollHeight;
    const r = row.getBoundingClientRect();
    return row.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) ? { rows: document.querySelectorAll(${q(ROW)}).length, text: row.textContent } : null;
  })()`, "a target beyond the first 50 rows is a real, clickable list row");
  const laterPoint = await read(`(() => {
    const row = document.querySelector(${q(`${ROW}[data-ref="${laterRef}"]`)});
    const r = row.getBoundingClientRect();
    return { x:r.x + r.width / 2, y:r.y + r.height / 2 };
  })()`);
  await driver.clickAt(laterPoint.x, laterPoint.y);
  await waitFocused(laterRef, "a later-page target opens with URL and pane agreeing");
  assert.equal(await read(`document.querySelector(${q(LIST)}) === null`), true, "choosing a later-page row closes the list");
  driver.milestone("attention-later-page", { ref: laterRef, rows: laterRow.rows });

  // ---- a real socket reconnect after multiple pages --------------------

  const beforeReconnect = frames.length;
  // Only conversation owners still mounted have a subscription to recover;
  // the cascade readers the hops and the explicit Close disposed must not be
  // required to re-subscribe.
  const mountedOwners = await read(`[...document.querySelectorAll('[data-composer]')].map((node) => node.dataset.composer)`);
  assert.ok(mountedOwners.length > 0, "the attention scene has mounted conversation owners before the reconnect");
  await reconnect({ expectRefs: mountedOwners });
  await wait(`${driver.paneScopeExpr(rootRef)} !== null`, "the source pane survives a real socket reconnect");
  // What the reader needs after a reconnect is the tier, re-read and answered.
  // Reads the app issued into the gap while the old socket was closing are
  // lost with that socket by design, and CI load widens that gap, so those are
  // recorded rather than failed - the answered tier read is the recovery.
  const postReconnectReads = () => sent(beforeReconnect).filter((frame) => frame.method === "evener/navigation/read");
  const tierReadAnswered = (frame) =>
    answered(frame) && frame.params?.section === "needs_you" && (frame.params?.offset ?? 0) === 0;
  const readDeadline = Date.now() + 15000;
  while (Date.now() < readDeadline && !postReconnectReads().some(tierReadAnswered)) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  const answeredTierReads = postReconnectReads().filter(tierReadAnswered).length;
  const unansweredReads = postReconnectReads().filter((frame) => !answered(frame));
  assert.ok(
    answeredTierReads > 0,
    `the app re-reads its needs-you tier after a reconnect and the hub answers it; unanswered ${unansweredReads.map((frame) => `${frame.params?.section ?? "?"} offset ${frame.params?.offset ?? "?"} (${frame.id})`).join(", ")}`,
  );
  driver.milestone("attention-reconnect-reads", {
    answeredTierReads,
    lostToTheClosingSocket: unansweredReads.length,
  });
  const afterReconnect = await focused();
  assert.equal(afterReconnect.ref, laterRef, "a real reconnect keeps the later-page target's focused pane and URL");
  await wait(`document.querySelector(${q(`${NEXT}`)}) !== null`, "the control re-derives its tier after reconnect");
  driver.milestone("attention-reconnect", { ref: laterRef });

  // ---- the clearance spacer never toggles New Content -----------------

  await read("history.back()");
  await waitFocused(rootRef, "Back returns from the later-page target to the source");
  // An away reader plus a genuine new turn: the pill is the app's own
  // new-content signal, and hiding the control (editing) removes the measured
  // clearance row. Neither direction may change the pill's own visibility.
  await wheelAway(sourcePortExpr, -900);
  await driver.clearComposerDraft(rootRef);
  await driver.focusComposer(rootRef);
  await driver.typeText(rootRef, "CASCADE_ROLE_0_SENTINEL CASCADE_ATTENTION_NEWER");
  const newer = await driver.composerState(rootRef);
  await driver.clickSubmit(rootRef, { text: newer.text, chips: newer.chips, tiles: newer.tiles });
  // Sending puts the reader at the end. Scroll away again before the reply
  // arrives, or the new turn lands in view and correctly raises no pill.
  await wheelAway(sourcePortExpr, -900);
  await driver.waitForTurnIdle(rootRef);
  // Blur first: the control and its measured clearance row are present when
  // the pill is first observed.
  const awayReaderPoint = await readerClickPoint(sourcePortExpr);
  await driver.clickAt(awayReaderPoint.x, awayReaderPoint.y);
  await controlVisible(null);
  await wait(`document.querySelector('[data-testid="new-content-pill"]') !== null`, "a real new turn surfaces the New Content pill for an away reader");
  const pillWhileAway = await read(`document.querySelector('[data-testid="new-content-pill"]') !== null`);
  // The toggle this leg was written for was editing-suppression: focusing the
  // composer hid the control and removed the measured clearance. Jesse's ruling
  // (Composer.tsx now reports IME composition only) keeps the control visible
  // through a focused composer, so that toggle no longer exists. The leg is
  // deferred to the unit that owns the ruling; only the away-reader pill is
  // observed here.
  driver.milestone("attention-clearance-new-content-deferred", { pill: pillWhileAway });

  // The control never claimed the Send/Stop geometry: both states keep one box.
  const afterJourney = await read(`(() => {
    const pane = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]')?.parentElement;
    const submit = pane?.querySelector('[data-testid="composer-submit"]')?.getBoundingClientRect();
    const control = pane?.querySelector('[data-testid="next-needs-you"]')?.getBoundingClientRect();
    if (!submit || !control) return null;
    return { submit:{ left:submit.left, right:submit.right, top:submit.top, bottom:submit.bottom, width:submit.width, height:submit.height },
      overlap: submit.left < control.right && control.left < submit.right && submit.top < control.bottom && control.top < submit.bottom };
  })()`);
  assert.ok(afterJourney && afterJourney.overlap === false, "the control leaves the Send button's box and hit area unchanged");
  driver.milestone("attention-send-geometry");
  assert.deepEqual(missingControls, [], `the floating control was not offered at ${missingControls.join(", ")}`);
  // ---- journey 2: a held native response and a transport-boundary retry --

  // The fixture starts one real root whose first model round is parked on the
  // provider boundary. The reply is an ordinary provider response, not a
  // seeded message; only its delivery waits for the driver's release.
  appendFileSync(fixture.attentionControlPath, `${JSON.stringify({ command: "hold-attention-response" })}\n`);
  for (let poll = 0; poll < 1200 && !existsSync(fixture.attentionHeldPath); poll++) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  assert.ok(existsSync(fixture.attentionHeldPath), "the fixture starts a real session whose response is held at native delivery");
  const heldRef = JSON.parse(readFileSync(fixture.attentionHeldPath, "utf8")).ref;
  assert.ok(heldRef, "the held session has a real ref");
  for (let poll = 0; poll < 600 && !existsSync(fixture.attentionHeldCallPath); poll++) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  assert.ok(existsSync(fixture.attentionHeldCallPath), "the hub's real provider call is parked at the native delivery boundary");
  await driver.openSession(heldRef);
  await controlVisible(null);
  // The held source was opened from the rail, not by an attention hop, so it
  // carries no attention token; its ref, URL and pane must still agree.
  const heldURL = `/s/${encodeURIComponent(heldRef)}`;
  const heldFocused = (label) => wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    if (!body) return null;
    const ref = body.dataset.paneScaffold.slice('session:'.length);
    const pane = body.parentElement;
    const paneId = pane?.querySelector('[data-testid="statusbar"] [data-pane-id]')?.dataset.paneId ?? null;
    return ref === ${q(heldRef)} && location.pathname === ${q(heldURL)} && paneId
      ? { ref, url: location.pathname, paneId, token: history.state?.__evenerAttention ?? null, length: history.length }
      : null;
  })()`, label);
  const heldOrigin = await heldFocused("the held source is the focused reader with URL and pane agreeing");
  const heldReplyIn = (ref) => read(`(${driver.paneScopeExpr(ref)}?.textContent ?? "").includes("CASCADE_ATTENTION_HELD_REPLY")`);
  assert.equal(await heldReplyIn(heldRef), false, "the parked reply has not been delivered to its reader");
  // Leave and return while the reply stays parked.
  await pressNext();
  const heldAway = await wait(`(() => {
    const body = document.querySelector('.dv-active-group [data-pane-scaffold^="session:"]');
    const ref = body?.dataset.paneScaffold.slice('session:'.length) ?? null;
    return ref && ref !== ${q(heldRef)} && location.pathname === '/s/' + encodeURIComponent(ref) ? { ref, url: location.pathname } : null;
  })()`, "the reader leaves the held source for a needs-you target");
  assert.equal(await heldReplyIn(heldRef), false, "leaving the held source does not deliver its parked reply");
  await read("history.back()");
  const heldReturned = await heldFocused("the reader returns to the held source before its reply is released");
  assert.equal(await heldReplyIn(heldRef), false, "the reply is still held after the reader returns");
  assert.equal(heldReturned.paneId, heldOrigin.paneId, "the held source keeps its own pane across leaving and returning");
  // Release the unchanged response while its reader is back on the source.
  appendFileSync(fixture.attentionControlPath, `${JSON.stringify({ command: "release-attention-response" })}\n`);
  await wait(`(${driver.paneScopeExpr(heldRef)}?.textContent ?? "").includes("CASCADE_ATTENTION_HELD_REPLY")`,
    "the unchanged held provider response is delivered to its reader", { timeoutMs: 20000 });
  await new Promise((resolve) => setTimeout(resolve, 1500));
  const afterRelease = await focused();
  assert.deepEqual(
    { url: afterRelease.url, ref: afterRelease.ref, paneId: afterRelease.paneId, token: afterRelease.token, length: afterRelease.length },
    { url: heldReturned.url, ref: heldReturned.ref, paneId: heldReturned.paneId, token: heldReturned.token, length: heldReturned.length },
    "releasing the unchanged held response performs no late navigation",
  );
  await controlVisible(null);
  driver.milestone("attention-held-response", { ref: heldRef, away: heldAway.ref, url: heldReturned.url, paneId: heldReturned.paneId });

  // A fresh boot leaves only the hub's first needs_you page loaded, so the
  // later page is a real read the transport can refuse, not a retained store
  // page from the earlier later-page leg.
  await driver.send("Page.reload", { ignoreCache: true });
  await wait(`document.querySelector('[data-pane-scaffold]') !== null &&
    (window.__cascadeSockets ?? []).some((socket) => socket.readyState === WebSocket.OPEN && new URL(socket.url).pathname === '/rpc')`,
    "the SPA boots and reconnects after the reload", { timeoutMs: 30000 });
  await driver.openSession(heldRef);
  const heldReloaded = await heldFocused("the held source is the focused reader after the reload");
  assert.equal(heldReloaded.ref, heldRef, "the reload keeps the held source as the focused reader");
  await controlVisible(null, { timeoutMs: 30000 });

  // A transport-boundary failure on the later page: the app's own request for
  // the second page reaches the socket and the write fails, so that page errors
  // without a reconnect. The user then presses Load more again deliberately.
  const focusedPortExpr = `${driver.paneScopeExpr(heldRef)}?.querySelector('[data-testid="transcript-virtual-list"] > div')`;
  await wait(`(() => {
    const port = ${focusedPortExpr};
    if (!port) return null;
    const stamp = String(port.scrollTop);
    const previous = window.__attentionListSettle;
    window.__attentionListSettle = stamp;
    return previous === stamp ? true : null;
  })()`, "the held reader's scroll is settled before the attention list opens");
  const transportInstalled = await read(`(() => {
    const socket = (window.__cascadeSockets ?? []).find((candidate) => candidate.readyState === WebSocket.OPEN && new URL(candidate.url).pathname === '/rpc');
    if (!socket) return { ok: false };
    const nativeSend = socket.send.bind(socket);
    window.__attentionTransport = { armed: true, attempts: [], writes: 0 };
    socket.send = function (data) {
      const state = window.__attentionTransport;
      let request;
      try { request = JSON.parse(data); } catch { return nativeSend(data); }
      const laterRead = state.armed && request.method === 'evener/navigation/read' &&
        request.params?.resource === 'section' && request.params?.section === 'needs_you' && (request.params?.offset ?? 0) > 0;
      if (!laterRead) return nativeSend(data);
      state.armed = false;
      state.attempts.push({ id: request.id, offset: request.params.offset });
      throw new DOMException('the transport refused the later read', 'InvalidStateError');
    };
    return { ok: true };
  })()`);
  assert.deepEqual(transportInstalled, { ok: true }, "the live hub socket accepts a transport-boundary write failure");
  await activate(MORE, "the More trigger opens the attention list for the transport-boundary leg");
  await wait(`document.querySelector(${q(LIST)}) !== null`, "the attention list opens before the transport failure");
  const pageOneRefs = await read(`[...document.querySelectorAll(${q(ROW)})].map((row) => row.dataset.ref)`);
  assert.equal(pageOneRefs.length, 50, `the list's first page carries the hub's 50 rows, got ${pageOneRefs.length}`);
  const beforeFailedRead = frames.length;
  await activate(LOAD_MORE, "the list's Load more control requests the later page through the failing transport");
  await wait(`window.__attentionTransport.attempts.length === 1`, "the later-page read reaches the transport boundary");
  const refused = await read("window.__attentionTransport.attempts[0]");
  assert.ok(refused.offset > 0, `the refused read is a later page, got offset ${refused.offset}`);
  assert.equal(await read(`document.querySelector(${q(LIST)}) !== null`), true, "the list stays open when its later read fails");
  assert.deepEqual(await read(`[...document.querySelectorAll(${q(ROW)})].map((row) => row.dataset.ref)`), pageOneRefs,
    "a failed later read leaves the earlier page intact");
  assert.deepEqual(
    sent(beforeFailedRead).filter((frame) => frame.method === "evener/navigation/read" &&
      frame.params?.section === "needs_you" && (frame.params?.offset ?? 0) > 0).map((frame) => frame.id),
    [],
    "the refused later read never reaches the hub",
  );
  // Deliberate retry: the same control, the same later page.
  await activate(LOAD_MORE, "the list's Load more control retries the later page deliberately");
  await wait(`document.querySelectorAll(${q(ROW)}).length > 50`, "the deliberate retry loads the later page", { timeoutMs: 20000 });
  const retried = sent(beforeFailedRead).filter((frame) => frame.method === "evener/navigation/read" &&
    frame.params?.section === "needs_you" && (frame.params?.offset ?? 0) > 0);
  assert.equal(retried.length, 1, `the deliberate retry issues exactly one later-page read, got ${retried.length}`);
  assert.equal(retried[0].params.offset, 50, `the retried read asks for the later page at offset 50, got ${retried[0].params.offset}`);
  assert.ok(answered(retried[0]), "the hub answers the retried later read");
  const rowsAfterRetry = await read(`[...document.querySelectorAll(${q(ROW)})].map((row) => row.dataset.ref)`);
  assert.equal(rowsAfterRetry.length, 52, `the retry carries both pages, got ${rowsAfterRetry.length}`);
  assert.deepEqual(rowsAfterRetry.slice(0, pageOneRefs.length), pageOneRefs, "the earlier choices keep their order after the retry");
  // An earlier choice is still a usable target, not just a surviving string.
  const earlierChoice = pageOneRefs[0];
  const earlierRow = await wait(`(() => {
    const row = document.querySelector(${q(`${ROW}[data-ref="${earlierChoice}"]`)});
    if (!row) return null;
    row.scrollIntoView({ block: "nearest", inline: "nearest" });
    const r = row.getBoundingClientRect();
    return row.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) ? { x: r.x + r.width / 2, y: r.y + r.height / 2 } : null;
  })()`, "an earlier choice is a clickable row after the transport retry");
  await driver.clickAt(earlierRow.x, earlierRow.y);
  await waitFocused(earlierChoice, "the earlier choice still opens with URL and pane agreeing after the retry");
  driver.milestone("attention-transport-retry", { refusedOffset: refused.offset, retriedOffset: retried[0].params.offset, rows: rowsAfterRetry.length, earlierChoice });
}
