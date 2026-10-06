// Real production SPA only. Go creates the delegates through actual tools and
// passes the public identities; this driver never seeds frontend stores or RPCs.
import assert from "node:assert/strict";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { Driver } from "../skillguard/run.mjs";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";

const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;
const inspectorRoot = '[data-pane-scaffold="cascade"]';
const inspectorFooter = `${inspectorRoot} ~ [data-testid="pane-edge-footer"] [data-testid="statusbar"]`;
const inspectorIdExpr = `document.querySelector(${q(`${inspectorFooter} [data-pane-id]`)})?.dataset.paneId`;
const sourceFooter = `[data-pane-scaffold=${q(`session:${fixture.rootRef}`)}] ~ [data-testid="pane-edge-footer"] [data-testid="statusbar"]`;
const sourceAgents = `${sourceFooter} button[aria-label^="Agents,"]`;
const column = (ref) => `${inspectorRoot} [data-testid="cascade-column"][data-scope-ref=${q(ref)}]`;
const row = (edge) => `[data-activity-anchor=${q(`delegate:${JSON.stringify([edge.childRef, edge.delegateId])}`)}]`;
const wait = (expression, label) => {
  new Function(`return (${expression})`);
  return driver.waitPage(expression, { label });
};
const read = (expression) => evaluate(driver.send, expression);
const scope = (ref) => `${inspectorRoot} [data-scope-ref=${q(ref)}]`;
const peekChip = (ref, kind) => `${scope(ref)} button[aria-label^=${q(`${kind},`)}][aria-label$=${q(`- peek at ${ref}`)}]`;
const scroll = (ref) => `${column(ref)} [data-testid="transcript-virtual-list"] > div`;
const stagedPNG = 'button[aria-label="View staged.png"] img';
const errors = [];
const consoleEvents = [];
const frames = [];
const readingPoints = [];
let failed = false;
let measurementScriptId;

async function capture(name) {
  const paint = await read(`([...document.querySelectorAll('[data-testid="transcript-virtual-list"]')]).map(list => {
    const box = node => { const r = node.getBoundingClientRect(); return { x:r.x, y:r.y, width:r.width, height:r.height, top:r.top, bottom:r.bottom }; };
    const port = list.firstElementChild;
    const bounds = port.getBoundingClientRect();
    const chain = [];
    for (let node = port; node && chain.length < 12; node = node.parentElement) {
      const style = getComputedStyle(node);
      chain.push({ tag:node.tagName, class:node.className, box:box(node), display:style.display, height:style.height, overflow:style.overflow, opacity:style.opacity, visibility:style.visibility, transform:style.transform });
    }
    return { ref:list.closest('[data-scope-ref]')?.dataset.scopeRef, surface:list.closest('[data-pane-scaffold]')?.dataset.paneScaffold, scrollTop:port.scrollTop, scrollHeight:port.scrollHeight, clientHeight:port.clientHeight, chain,
      rows:[...port.querySelectorAll('[data-index]')].map(node => { const r = node.getBoundingClientRect(); return { index:node.dataset.index, box:box(node), intersects:r.bottom > bounds.top && r.top < bounds.bottom, textLength:node.textContent.length }; }) };
  })`);
  writeFileSync(path.join(fixture.artifactDir, `${name}-paint.json`), JSON.stringify(paint, null, 2));
  writeFileSync(path.join(fixture.artifactDir, `${name}-native-hits.json`), JSON.stringify(await read('window.__cascadeNativeHits'), null, 2));
  writeFileSync(path.join(fixture.artifactDir, `${name}-scroll-writers.json`), JSON.stringify(await read('window.__cascadeStartupTrace'), null, 2));
  writeFileSync(path.join(fixture.artifactDir, `${name}.html`), await read("document.documentElement.outerHTML"));
  const screenshot = await driver.send("Page.captureScreenshot", { format: "png" });
  writeFileSync(path.join(fixture.artifactDir, `${name}.png`), Buffer.from(screenshot.result.data, "base64"));
  return paint;
}

async function pinSourceDom() {
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.rootRef))}) !== null && ${driver.paneScopeExpr(fixture.rootRef)}?.querySelector('[data-testid="transcript-virtual-list"]') != null`, "real source composer and transcript are mounted");
  await read(`(() => {
    const scope = ${driver.paneScopeExpr(fixture.rootRef)};
    const editor = document.querySelector(${q(driver.composerSelector(fixture.rootRef))});
    const reader = scope?.querySelector('[data-testid="transcript-virtual-list"]');
    if (!scope || !editor || !reader) throw new Error('Source DOM is not mounted');
    window.__cascadeSourceDom = { scope, editor, reader };
  })()`);
}

async function assertSourceDom() {
  assert.equal(await read(`(() => {
    const previous = window.__cascadeSourceDom;
    return previous.scope.isConnected && previous.editor.isConnected && previous.reader.isConnected &&
      previous.scope === ${driver.paneScopeExpr(fixture.rootRef)} &&
      previous.editor === document.querySelector(${q(driver.composerSelector(fixture.rootRef))}) &&
      previous.reader === previous.scope.querySelector('[data-testid="transcript-virtual-list"]');
  })()`), true, "the exact source DOM survives inspection");
}

const sourceAnchorExpr = `(() => {
  const port = ${driver.paneScopeExpr(fixture.rootRef)}?.querySelector('[data-testid="transcript-virtual-list"] > div');
  if (!port) return null;
  const bounds = port.getBoundingClientRect();
  if (bounds.height <= 0 || bounds.width <= 0) return null;
  const row = [...port.querySelectorAll('[data-row-id]')].find(node => {
    const r = node.getBoundingClientRect();
    return r.bottom > bounds.top && r.top < bounds.bottom;
  });
  return row?.dataset.rowId ?? null;
})()`;

async function sourceAnchor() {
  return wait(sourceAnchorExpr, "real center transcript has a visible semantic anchor");
}

function readingPointExpr(portExpr) {
  return `(() => {
    const port = ${portExpr};
    if (!port || port.clientWidth <= 0 || port.clientHeight <= 0) return null;
    const bounds = port.getBoundingClientRect();
    const entries = [...port.querySelectorAll('[data-view-anchor-id]')].filter(node => {
      const rect = node.getBoundingClientRect();
      return node.getClientRects().length > 0 && rect.width > 0 && rect.height > 0 &&
        rect.bottom > bounds.top && rect.top < bounds.bottom;
    });
    const crossing = entries.filter(node => node.getBoundingClientRect().top <= bounds.top);
    const candidates = crossing.length > 0 ? crossing : entries;
    const entry = candidates.find(node => !candidates.some(other => other !== node && node.contains(other))) ?? candidates[0];
    if (!entry) return null;
    const box = node => {
      const rect = node.getBoundingClientRect();
      return { top:rect.top, bottom:rect.bottom, left:rect.left, right:rect.right, width:rect.width, height:rect.height };
    };
    const entryBox = box(entry);
    const row = entry.closest('[data-row-id]');
    const walker = document.createTreeWalker(entry, NodeFilter.SHOW_TEXT);
    let useful = false;
    let visibleText = null;
    let text;
    while ((text = walker.nextNode())) {
      if (!text.textContent?.trim()) continue;
      const range = document.createRange();
      range.selectNodeContents(text);
      if ([...range.getClientRects()].some(rect => rect.width > 0 && rect.height > 0 &&
        rect.bottom > bounds.top && rect.top < bounds.bottom &&
        rect.right > bounds.left && rect.left < bounds.right)) {
        useful = true;
        visibleText = text.textContent.trim().slice(0, 100);
        break;
      }
    }
    return { entry:entry.dataset.viewAnchorId, row:row?.dataset.rowId, offset:entryBox.top - bounds.top,
      height:entryBox.height, viewport:port.clientHeight, width:port.clientWidth, scrollTop:port.scrollTop,
      scrollHeight:port.scrollHeight, followingBottom:port.scrollHeight - port.clientHeight - port.scrollTop <= 4,
      useful, visibleText, entryBox, rowBox:row ? box(row) : null, portBox:box(port) };
  })()`;
}

const sourcePortExpr = `${driver.paneScopeExpr(fixture.rootRef)}?.querySelector('[data-testid="transcript-virtual-list"] > div')`;
const sourceReadingPointExpr = readingPointExpr(sourcePortExpr);

function assertReadingContinuity(before, after, label) {
  assert.ok(before && after, `${label} has independently observed source entries`);
  assert.equal(after.entry, before.entry, `${label} preserves the semantic entry`);
  assert.equal(after.useful, true, `${label} keeps actual nonblank text in the viewport`);
  if (before.followingBottom) {
    assert.ok(after.scrollHeight - after.viewport - after.scrollTop <= 1.5, `${label} keeps end following`);
  } else if (before.width !== after.width) {
    const oldDepth = Math.max(0, before.height - before.viewport);
    const progress = oldDepth > 0 ? Math.max(0, Math.min(1, -before.offset / oldDepth)) : 0;
    const wantedOffset = before.offset >= 0 ? before.offset : -progress * Math.max(0, after.height - after.viewport);
    const start = after.scrollTop + after.offset;
    const wantedScroll = Math.max(0, Math.min(start - wantedOffset, Math.max(0, after.scrollHeight - after.viewport)));
    assert.ok(Math.abs(after.scrollTop - wantedScroll) <= 2,
      `${label} preserves feasible within-entry progress, actual ${after.scrollTop}, wanted ${wantedScroll}`);
  }
}

async function settledReadingPoint(portExpr, label) {
  return wait(`(() => {
    const point = ${readingPointExpr(portExpr)};
    window.__cascadeReadingAttempt = { label:${q(label)}, point };
    if (!point?.useful) return null;
    const stamp = JSON.stringify(point);
    const previous = window.__cascadeReadingSample;
    window.__cascadeReadingSample = { label:${q(label)}, stamp };
    return previous?.label === ${q(label)} && previous.stamp === stamp ? point : null;
  })()`, `${label}, useful reading geometry is stable`);
}

async function settledReadingContinuity(before, portExpr, label) {
  return wait(`(() => {
    const before = ${q(before)};
    const after = ${readingPointExpr(portExpr)};
    window.__cascadeReadingAttempt = { label:${q(label)}, point:after };
    if (!before || !after?.useful || after.entry !== before.entry) return null;
    if (before.followingBottom) {
      return after.scrollHeight - after.viewport - after.scrollTop <= 1.5 ? after : null;
    }
    if (before.width === after.width) return after;
    const oldDepth = Math.max(0, before.height - before.viewport);
    const progress = oldDepth > 0 ? Math.max(0, Math.min(1, -before.offset / oldDepth)) : 0;
    const wantedOffset = before.offset >= 0 ? before.offset : -progress * Math.max(0, after.height - after.viewport);
    const start = after.scrollTop + after.offset;
    const wantedScroll = Math.max(0, Math.min(start - wantedOffset, Math.max(0, after.scrollHeight - after.viewport)));
    return Math.abs(after.scrollTop - wantedScroll) <= 2 ? after : null;
  })()`, `${label}, actual source reading position finishes restoration`);
}

async function nativeReaderWheel(portExpr, deltaY) {
  const point = await wait(`(() => {
    const port = ${portExpr};
    if (!port || port.clientWidth <= 0 || port.clientHeight <= 0) return null;
    const r = port.getBoundingClientRect();
    const box = node => {
      const bounds = node.getBoundingClientRect();
      return { x:bounds.x, y:bounds.y, width:bounds.width, height:bounds.height };
    };
    const target = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    if (!port.contains(target)) return null;
    window.__cascadeReaderWheels ??= [];
    document.addEventListener('wheel', event => window.__cascadeReaderWheels.push({
      deltaY:event.deltaY, trusted:event.isTrusted, intended:port.contains(event.target),
      ref:event.target.closest('[data-scope-ref]')?.dataset.scopeRef,
      surface:event.target.closest('[data-pane-scaffold]')?.dataset.paneScaffold,
      scrollTop:port.scrollTop, width:port.clientWidth, height:port.clientHeight,
      eventX:event.clientX, eventY:event.clientY, portBox:box(port), viewport:innerWidth
    }), { capture:true, once:true, passive:true });
    return { x:r.x + r.width / 2, y:r.y + r.height / 2, scrollTop:port.scrollTop, eventIndex:window.__cascadeReaderWheels.length,
      portBox:box(port), intendedAtMeasure:port.contains(target), viewport:innerWidth,
      hitSurface:target?.closest('[data-pane-scaffold]')?.dataset.paneScaffold };
  })()`, "native wheel midpoint actually hits its intended reader");
  const observation = { kind:"wheel-target", point };
  readingPoints.push(observation);
  await driver.send("Input.dispatchMouseEvent", { type:"mouseMoved", x:point.x, y:point.y });
  await driver.send("Input.dispatchMouseEvent", { type:"mouseWheel", deltaX:0, deltaY, x:point.x, y:point.y });
  const observed = await wait(`window.__cascadeReaderWheels[${point.eventIndex}]`, "native wheel event reaches the page");
  observation.observed = observed;
  assert.equal(observed.trusted, true, "reader movement uses genuine native wheel input");
  assert.equal(observed.intended, true, "native wheel targets its intended reader");
  await wait(`(${portExpr})?.scrollTop !== ${point.scrollTop}`, "native wheel actually moves its intended reader");
}

async function readInsideTallEntry(portExpr, label) {
  await nativeReaderWheel(portExpr, -900);
  return wait(`(() => {
    const point = ${readingPointExpr(portExpr)};
    window.__cascadeReadingAttempt = { label:${q(label)}, point };
    return point?.useful && !point.followingBottom && point.height > point.viewport && point.offset < -100 ? point : null;
  })()`, `${label}, native wheel selects useful content inside a tall entry`);
}

async function widthOnlyReflow(portExpr, width, height, label) {
  const before = await settledReadingPoint(portExpr, `${label} before`);
  assert.ok(!before.followingBottom && before.height > before.viewport && before.offset < -100, `${label} begins inside a tall entry away from the end`);
  await driver.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor:1, mobile:false });
  await wait(`window.innerWidth === ${width} && (${portExpr})?.clientWidth !== ${before.width}`, `${label}, actual reader width changes`);
  const after = await settledReadingPoint(portExpr, `${label} after`);
  readingPoints.push({ kind:"width-only", label, before, after });
  assert.notEqual(after.width, before.width, `${label} observes a width-only transition`);
  assertReadingContinuity(before, after, label);
  driver.milestone(label, { entry:after.entry, row:after.row, oldWidth:before.width, width:after.width, offset:after.offset });
}

async function sharedWidthJourney() {
  await readInsideTallEntry(sourcePortExpr, "ordinary source");
  await widthOnlyReflow(sourcePortExpr, 1280, 900, "ordinary-reader-width-reflow");
  await widthOnlyReflow(sourcePortExpr, 1000, 900, "ordinary-reader-width-return");
  await assertSourceDom();
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  await driver.click('nav[aria-label="Agent path"] button:first-child');
  await wait(`document.querySelectorAll('[data-testid="cascade-column"]').length === 1 && document.querySelector(${q(column(fixture.rootRef))}) !== null`, "real root pop supplies a read-only cascade reader");
  await driver.send("Emulation.setDeviceMetricsOverride", { width:1640, height:900, deviceScaleFactor:1, mobile:false });
  const portExpr = `document.querySelector(${q(scroll(fixture.rootRef))})`;
  await wait(`(() => {
    const port = ${portExpr}, bounds = port?.getBoundingClientRect();
    return port?.clientWidth > 0 && port.clientHeight > 0 && bounds.width > 0 && bounds.height > 0;
  })()`, "read-only cascade has a visible native wheel target");
  await readInsideTallEntry(portExpr, "read-only cascade");
  await widthOnlyReflow(portExpr, 2000, 900, "cascade-reader-width-reflow");
  await widthOnlyReflow(portExpr, 1640, 900, "cascade-reader-width-return");
  await assertInspectorReadOnly();
  await capture("shared-reader-width-reflow");
  await returnToSource();
  await driver.send("Emulation.setDeviceMetricsOverride", { width:1000, height:900, deviceScaleFactor:1, mobile:false });
  await settledReadingPoint(sourcePortExpr, "ordinary source after shared width proof");
}

async function openSourceVerbosity() {
  const menu = await wait(`(() => {
    const button = ${driver.paneScopeExpr(fixture.rootRef)}?.querySelector('[data-session-actions-ref]')?.closest('button');
    const r = button?.getBoundingClientRect();
    return r?.width > 0 && r.height > 0 ? { x:r.x + r.width / 2, y:r.y + r.height / 2 } : null;
  })()`, "the original source has a visible Session menu button");
  await driver.clickAt(menu.x, menu.y);
  // Mounted menu DOM can precede the compositor's scrollbar hit-test update.
  await driver.send("Page.captureScreenshot", { format:"png" });
  const verbosity = await wait(`(() => {
    const button = [...document.querySelectorAll('[role="menuitem"]')].find(node => node.textContent.trim() === 'Verbosity…');
    const r = button?.getBoundingClientRect();
    return r?.width > 0 && r.height > 0 ? { x:r.x + r.width / 2, y:r.y + r.height / 2 } : null;
  })()`, "the actual source menu offers Verbosity");
  await driver.clickAt(verbosity.x, verbosity.y);
  return wait(`(() => {
    const panel = document.querySelector('[data-testid="transcript-detail-control"]');
    const selected = panel?.querySelector('[role="radio"][aria-checked="true"]')?.textContent.trim();
    return selected ? { selected, local:panel.textContent.includes('Local Desktop view') } : null;
  })()`, "the real Verbosity dialog exposes its current preset");
}

async function clickVerbosityChoice(label, role = "radio") {
  const point = await wait(`(() => {
    const panel = document.querySelector('[data-testid="transcript-detail-control"]');
    const button = [...panel?.querySelectorAll(${q(role === "radio" ? '[role="radio"]' : 'button')}) ?? []].find(node => node.textContent.trim() === ${q(label)});
    const r = button?.getBoundingClientRect();
    return r?.width > 0 && r.height > 0 ? { x:r.x + r.width / 2, y:r.y + r.height / 2 } : null;
  })()`, `the actual Verbosity ${label} control is visible`);
  await driver.clickAt(point.x, point.y);
}

async function nativePositioningInterruption(input) {
  assert.ok(input === "wheel" || input === "pill" || input === "Shift-Space", "the native interruption uses a real reader input");
  const original = await openSourceVerbosity();
  assert.ok(["Chat", "Tools"].includes(original.selected), "the native interruption starts from a real Chat or Tools preset");
  if (original.selected !== "Chat") {
    await clickVerbosityChoice("Chat");
    await wait('document.querySelector("[data-testid=transcript-detail-control] [role=radio][aria-checked=true]")?.textContent.trim() === "Chat"', "the real source has a Chat baseline before the measurement hold");
  }
  const before = await settledReadingPoint(sourcePortExpr, "native interruption before width");
  assert.ok(!before.followingBottom && before.height > before.viewport && before.offset < -100,
    "the native interruption starts inside useful tall content away from the end");
  const observation = { kind:`${input}-interruption`, original, baselinePreset:"Chat", before };
  readingPoints.push(observation);
  await read(`(() => {
    const state = window.__cascadeMeasurements, port = ${sourcePortExpr};
    const entry = [...port.querySelectorAll('[data-view-anchor-id]')].find(node => node.dataset.viewAnchorId === ${q(before.entry)});
    const row = entry?.closest('[data-index]');
    if (!state || !row?.isConnected || !port.contains(row) || state.queued.length) throw new Error('No connected selected virtual row for the measurement hold');
    state.port = port;
    state.row = row;
  })()`);
  try {
    await driver.send("Emulation.setDeviceMetricsOverride", { width:920, height:900, deviceScaleFactor:1, mobile:false });
    await wait(`window.innerWidth === 920 && (${sourcePortExpr})?.clientWidth !== ${before.width}`, "interruption preparation changes actual source width");
    const other = "Tools";
    await clickVerbosityChoice(other);
    await wait(`document.querySelector('[data-testid="transcript-detail-control"] [role="radio"][aria-checked="true"]')?.textContent.trim() === ${q(other)}`, "the real source preset change is published");
    await key("Escape", 27);
    await wait('document.querySelector("[data-testid=transcript-detail-control]") === null', "Verbosity closes before input reaches the source port");
    observation.prepared = await wait(`(() => {
      const state = window.__cascadeMeasurements, port = ${sourcePortExpr};
      const row = state?.row, bounds = row?.getBoundingClientRect();
      const entries = state?.queued.flatMap(pending => pending.entries) ?? [];
      const point = ${sourceReadingPointExpr};
      if (state?.port !== port || !row?.isConnected || !port.contains(row) || !entries.length ||
        !entries.every(entry => entry.target === row && entry instanceof ResizeObserverEntry) ||
        bounds.width <= 0 || bounds.height <= 0 || !point?.useful || point.width === ${before.width}) return null;
      return { point, connected:true, index:row.dataset.index, rowWidth:bounds.width, rowHeight:bounds.height,
        entries:entries.map(entry => ({ native:entry instanceof ResizeObserverEntry, connected:entry.target.isConnected,
          index:entry.target.dataset.index, width:entry.contentRect.width, height:entry.contentRect.height })) };
    })()`, "native input checkpoint has a connected selected row and genuine queued target measurements");
    assert.ok(Math.abs(observation.prepared.rowHeight - before.rowBox.height) > 1.5,
      "the prepared virtual row differs from the stable pre-hold measurement beyond committed-geometry tolerance");
    await capture(`${input}-interruption-prepared`);
    if (input === "wheel") {
      await nativeReaderWheel(sourcePortExpr, -900);
    } else if (input === "Shift-Space") {
      const target = await wait(`(() => {
        const port = ${sourcePortExpr}, bounds = port?.getBoundingClientRect();
        if (!bounds || bounds.width <= 0 || bounds.height <= 0) return null;
        const x = bounds.x + bounds.width / 2, y = bounds.y + bounds.height / 2;
        const hit = document.elementFromPoint(x, y);
        if (!port.contains(hit) || hit.closest('button, summary, input, textarea, select, [contenteditable]')) return null;
        window.__cascadeNativeKeys ??= [];
        document.addEventListener('keydown', event => window.__cascadeNativeKeys.push({
          trusted:event.isTrusted, intended:port.contains(event.target), key:event.key, shift:event.shiftKey,
          tag:event.target.tagName, prevented:event.defaultPrevented
        }), { once:true });
        return { x, y, scrollTop:port.scrollTop, eventIndex:window.__cascadeNativeKeys.length };
      })()`, "native Shift-Space has a real non-activating transcript target");
      await driver.clickAt(target.x, target.y);
      observation.keyFocus = await read(`(() => {
        (${sourcePortExpr}).focus({ preventScroll:true });
        const active = document.activeElement;
        return { tag:active.tagName, intended:(${sourcePortExpr}).contains(active), tabIndex:active.tabIndex };
      })()`);
      assert.equal(observation.keyFocus.intended, true, "the unchanged native viewport accepts keyboard focus");
      for (const type of ["keyDown", "keyUp"]) {
        await driver.send("Input.dispatchKeyEvent", { type, key:" ", code:"Space", modifiers:8,
          windowsVirtualKeyCode:32, nativeVirtualKeyCode:32,
          ...(type === "keyDown" ? { text:" ", unmodifiedText:" " } : {}) });
      }
      observation.key = await wait(`window.__cascadeNativeKeys[${target.eventIndex}]`, "native Shift-Space reaches its real keyboard target");
      assert.equal(observation.key.trusted, true, "Shift-Space uses genuine native input");
      assert.equal(observation.key.intended, true, "Shift-Space targets the actual source reader");
      assert.equal(observation.key.key, " ", "the native event carries Space");
      assert.equal(observation.key.shift, true, "the native event carries Shift");
      assert.equal(observation.key.prevented, false, "the browser receives the native scrolling default");
      await wait(`(${sourcePortExpr}).scrollTop < ${target.scrollTop}`, "native Shift-Space scrolls the actual reader backward");
    } else {
      const click = await wait(`(() => {
        const pill = ${driver.paneScopeExpr(fixture.rootRef)}?.querySelector('[data-testid="new-content-pill"]');
        const bounds = pill?.getBoundingClientRect();
        if (!bounds || bounds.width <= 0 || bounds.height <= 0) return null;
        const x = bounds.x + bounds.width / 2, y = bounds.y + bounds.height / 2;
        if (!pill.contains(document.elementFromPoint(x, y))) return null;
        window.__cascadePillClicks ??= [];
        document.addEventListener('click', event => window.__cascadePillClicks.push({
          trusted:event.isTrusted, intended:pill.contains(event.target),
          x:event.clientX, y:event.clientY,
          focusedPill:document.activeElement === pill
        }), { capture:true, once:true });
        return { x, y, eventIndex:window.__cascadePillClicks.length };
      })()`, "the real current-state pill has a visible native click target");
      await driver.clickAt(click.x, click.y);
      observation.command = await wait(`window.__cascadePillClicks[${click.eventIndex}]`, "the native pill click reaches its real command");
      assert.equal(observation.command.trusted, true, "the positioning command uses genuine native input");
      assert.equal(observation.command.intended, true, "the native click reaches the real source pill");
      assert.equal(observation.command.focusedPill, true, "native pill focus identifies the command checkpoint");
      await wait(`(() => {
        const point = ${sourceReadingPointExpr};
        return point?.useful && point.followingBottom ? point : null;
      })()`, "the actual pill command reaches useful live content before measurement release");
    }
    observation.newer = await settledReadingPoint(sourcePortExpr, `native interruption after admitted ${input}`);
    assert.ok(observation.newer.entry !== before.entry || Math.abs(observation.newer.offset - before.offset) >= 100,
      "the actual native input distinguishes the newer reading point from the pre-trigger point");
    assert.ok(observation.newer.entry !== observation.prepared.point.entry || Math.abs(observation.newer.offset - observation.prepared.point.offset) >= 100,
      "the admitted input meaningfully moves the prepared reader");
  } finally {
    await read('window.__cascadeMeasurements.release()');
    observation.released = await read(`(() => {
      const state = window.__cascadeMeasurements;
      return { queued:state.queued.length, held:state.port !== null || state.row !== null };
    })()`);
  }
  assert.deepEqual(observation.released, { queued:0, held:false }, "all genuine queued measurements are released");
  observation.after = await settledReadingPoint(sourcePortExpr, "native interruption after release");
  const { newer, after } = observation;
  const oldDepth = Math.max(0, before.height - before.viewport);
  const progress = oldDepth > 0 ? Math.max(0, Math.min(1, -before.offset / oldDepth)) : 0;
  const oldOffset = before.offset >= 0 ? before.offset : -progress * Math.max(0, after.height - after.viewport);
  const oldTarget = Math.max(0, Math.min(after.scrollTop + after.offset - oldOffset, Math.max(0, after.scrollHeight - after.viewport)));
  const retainedNewer = after.entry === newer.entry && Math.abs(after.offset - newer.offset) <= 2;
  observation.staleReplay = !retainedNewer && after.entry === before.entry && Math.abs(after.scrollTop - oldTarget) <= 2;
  observation.oldTarget = oldTarget;
  await capture(`${input}-interruption-released`);
  assert.ok(retainedNewer, observation.staleReplay
    ? "released measurements replay the obsolete pre-trigger entry/alignment after admitted native input"
    : "released measurements displace the newer reading point without an obsolete-target replay witness");
  if (input === "pill") assert.equal(after.followingBottom, true, "released measurements retain the real live command result");
  driver.milestone(`${input}-interruption-precedence`, { entry:after.entry, offset:after.offset });
  if (input === "pill") await read(`(() => {
    const port = ${sourcePortExpr};
    window.__cascadeCleanupMutations = new MutationObserver(records => {
      window.__cascadeTraceStartup('cleanup-layout-mutation', { records:records.map(record => ({
        type:record.type, tag:record.target.nodeName, index:record.target.dataset?.index,
        sizer:record.target === port.firstElementChild, attribute:record.attributeName,
        oldValue:record.oldValue, style:record.target.getAttribute?.('style'),
        added:[...record.addedNodes].map(node => ({ tag:node.nodeName, index:node.dataset?.index, style:node.getAttribute?.('style') })),
        removed:[...record.removedNodes].map(node => ({ tag:node.nodeName, index:node.dataset?.index, style:node.getAttribute?.('style') }))
      })) });
    });
    window.__cascadeCleanupMutations.observe(port, { subtree:true, childList:true, attributes:true, attributeOldValue:true, attributeFilter:['style'] });
  })()`);
  await openSourceVerbosity();
  await clickVerbosityChoice(original.local ? original.selected : "Use hub default", original.local ? "radio" : "button");
  await wait(`document.querySelector('[data-testid="transcript-detail-control"] [role="radio"][aria-checked="true"]')?.textContent.trim() === ${q(original.selected)}`, "native UI restores the original source preset");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=transcript-detail-control]") === null', "restored Verbosity dialog closes");
  await driver.send("Emulation.setDeviceMetricsOverride", { width:1000, height:900, deviceScaleFactor:1, mobile:false });
  const cleanedUp = await settledReadingPoint(sourcePortExpr, "source after native interruption preparation cleanup");
  if (input === "pill") assert.equal(cleanedUp.followingBottom, true, "restoring the source preset and width preserves the admitted live command");
  await assertSourceDom();
}

async function assertInspectorReadOnly() {
  assert.equal(await read(`document.querySelector(${q(inspectorRoot)}).querySelectorAll('[role="textbox"], input[type="file"]').length`), 0, "only inspection is read only");
  assert.ok(await read(`document.querySelector(${q(driver.composerSelector(fixture.rootRef))})?.querySelector('[role="textbox"]') != null`), "the center keeps its original editor");
  assert.ok(await read(`${driver.paneScopeExpr(fixture.rootRef)}?.querySelector('input[type="file"]') != null`), "the center keeps its original file picker");
}

async function returnToSource() {
  const removedInspectorId = fixture.inspectorPaneId;
  assert.ok(removedInspectorId, "Return begins with an actual inspector");
  const beforeReturnAnchor = await sourceAnchor();
  const beforeReturnPoint = await read(sourceReadingPointExpr);
  await read('window.__cascadeTraceSource?.("before-return")');
  await driver.clickByText("Return to previous view");
  await read('window.__cascadeTraceSource?.("after-return-click")');
  await wait(`document.querySelector(${q(inspectorRoot)}) === null`, "Return removes only inspection");
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.rootRef))})?.querySelector('[role="textbox"]')?.contains(document.activeElement) === true`, "Return requests keyboard focus in the exact source editor");
  const returnedLayout = await wait(`(() => {
    const saved = ${layoutExpr};
    return saved?.panels[${q(fixture.sourcePaneId)}]?.params?.paneType === 'session' &&
      !saved.panels[${q(removedInspectorId)}] ? saved : null;
  })()`, "debounced layout records inspector removal and surviving source");
  assert.equal(returnedLayout.activeGroup, placement(returnedLayout, fixture.sourcePaneId).group.id);
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount);
  const afterReturnPoint = await settledReadingContinuity(beforeReturnPoint, sourcePortExpr, "Return");
  readingPoints.push({ kind:"return", before:beforeReturnPoint, after:afterReturnPoint, sourceRow:beforeReturnAnchor });
  assert.equal(await sourceAnchor(), beforeReturnAnchor, "Return preserves the visible source row");
  assertReadingContinuity(beforeReturnPoint, afterReturnPoint, "Return");
  await assertSourceDom();
  fixture.inspectorPaneId = null;
  return removedInspectorId;
}

async function key(key, keyCode) {
  for (const type of ["keyDown", "keyUp"]) {
    await driver.send("Input.dispatchKeyEvent", { type, key, code: key, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode,
      ...(key === "Enter" && type === "keyDown" ? { text: "\r", unmodifiedText: "\r" } : {}) });
  }
}

async function upload(name) {
  const tree = await driver.send("DOM.getDocument");
  const node = await driver.send("DOM.querySelector", { nodeId: tree.result.root.nodeId, selector: `${driver.composerSelector(fixture.rootRef)} input[type="file"]` });
  assert.ok(node.result.nodeId, "real source file picker");
  await driver.send("DOM.setFileInputFiles", { nodeId: node.result.nodeId, files: [path.join(fixture.artifactDir, name)] });
}

async function holdCanvasCompletion() {
  await read(`(() => {
    const native = HTMLCanvasElement.prototype.toBlob;
    window.__cascadeEncode = { release: null, encoded: false };
    HTMLCanvasElement.prototype.toBlob = function(callback, ...args) {
      HTMLCanvasElement.prototype.toBlob = native;
      return native.call(this, blob => {
        window.__cascadeEncode.encoded = blob !== null;
        window.__cascadeEncode.release = success => callback(success ? blob : null);
      }, ...args);
    };
  })()`);
}

async function openPeek(ref, kind) {
  await driver.click(peekChip(ref, kind));
  await wait('document.querySelector("[data-testid=activity-peek]") !== null', `${kind} ancestor peek`);
}

async function installSourceTrace() {
  await read(`(() => {
    const port = ${driver.paneScopeExpr(fixture.rootRef)}.querySelector('[data-testid="transcript-virtual-list"] > div');
    window.__cascadeSourceTrace = [];
    window.__cascadeTraceSource = (phase, detail) => {
      const bounds = port.getBoundingClientRect();
      const rows = [...port.querySelectorAll('[data-index]')].map(node => {
        const r = node.getBoundingClientRect();
        return { index: node.dataset.index, row: node.querySelector('[data-row-id]')?.dataset.rowId,
          start: r.top - bounds.top + port.scrollTop, height: r.height, offset: r.top - bounds.top };
      });
      window.__cascadeSourceTrace.push({ phase, detail, at: performance.now(), scrollTop: port.scrollTop,
        scrollHeight: port.scrollHeight, width: bounds.width, height: bounds.height,
        clientWidth: port.clientWidth, clientHeight: port.clientHeight,
        offsetWidth: port.offsetWidth, offsetHeight: port.offsetHeight,
        sizerHeight: port.firstElementChild?.getBoundingClientRect().height,
        sizerStyleHeight: port.firstElementChild?.style.height, rows });
    };
    const nativeScroll = Element.prototype.scrollTo;
    Element.prototype.scrollTo = function(...args) {
      if (this === port) window.__cascadeTraceSource('scrollTo', { args, stack: new Error().stack });
      return nativeScroll.apply(this, args);
    };
    port.addEventListener('scroll', () => window.__cascadeTraceSource('scroll'));
    const observer = new ResizeObserver(() => window.__cascadeTraceSource('viewport-resize'));
    observer.observe(port);
    window.__cascadeTraceSource('source-mounted');
  })()`);
}

async function pendingImage(name, success) {
  const rejectedMarker = `[image ${(await driver.composerState(fixture.rootRef)).tiles + 1}]`;
  await read(`window.__cascadeTraceSource('pending-start', ${q(name)})`);
  await holdCanvasCompletion();
  await upload(name);
  await wait("window.__cascadeEncode?.encoded && window.__cascadeEncode.release !== null", "native canvas encoded before completion hold");
  await wait(`document.querySelector('[role="img"][aria-label=${q(`${name} (still processing)`)}]') !== null`, "original pending tile");
  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, ` CASCADE_NEWER_IMAGE_${success ? "SUCCESS" : "FAILURE"}`);
  const newerDraft = await driver.composerState(fixture.rootRef);
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  await read('window.__cascadeTraceSource("after-drill")');
  await assertInspectorReadOnly();
  await assertSourceDom();
  await read(`window.__cascadeEncode.release(${success})`);
  await read('window.__cascadeTraceSource("after-release")');
  await returnToSource();
  if (success) {
    await wait(`document.querySelector('button[aria-label=${q(`View ${name}`)}] img')?.src.startsWith('data:image/png;base64,')`, "late real PNG completion in original source");
  } else {
    await wait(`${driver.toastExpr()}.includes(${q(`${name} (image decode failed)` )})`, "explicit native canvas failure toast");
    assert.equal(await read(`document.querySelector('button[aria-label=${q(`Remove ${name}`)}]') !== null`), false, "failed original tile removed");
  }
  const expectedText = success ? newerDraft.text : newerDraft.text.replace(rejectedMarker, "");
  assert.equal((await driver.composerState(fixture.rootRef)).text, expectedText, "late image completion never rolls back a newer source draft");
  driver.milestone(success ? "pending-image-success" : "pending-image-failure", { name, recipient: fixture.rootRef });
}

async function holdStorageAcknowledgement(ref, input) {
  await read(`(() => {
    const nativeAdd = IDBObjectStore.prototype.add;
    const nativeListen = IDBTransaction.prototype.addEventListener;
    const held = window.__cascadeStorage = { committed: false, record: null, release: null };
    let selected;
    IDBTransaction.prototype.addEventListener = function(type, listener, options) {
      if (type !== 'complete' || this.mode !== 'readwrite' || this.db.name !== 'evener-mutation-outbox' || !this.objectStoreNames.contains('outbox')) return nativeListen.call(this, type, listener, options);
      return nativeListen.call(this, type, event => {
        if (this !== selected) return listener.call(this, event);
        held.committed = true;
        held.release = () => listener.call(this, event);
      }, options);
    };
    IDBObjectStore.prototype.add = function(record, ...args) {
      if (this.name === 'outbox' && this.transaction.db.name === 'evener-mutation-outbox' && record.targetRef === ${q(ref)} && (record.composerText === ${q(input)} || record.payload?.input?.some(item => item.text === ${q(input)}))) {
        selected = this.transaction;
        held.record = structuredClone(record);
        IDBObjectStore.prototype.add = nativeAdd;
        IDBTransaction.prototype.addEventListener = nativeListen;
      }
      return nativeAdd.call(this, record, ...args);
    };
  })()`);
}

async function providerHeld() {
  await new Promise((resolve, reject) => {
    const file = path.join(fixture.artifactDir, "provider-held.json");
    // Polled rather than watched: a watch event can arrive while the file is
    // still empty and be the only one delivered (macOS coalesces them, #3808).
    const poll = setInterval(() => check(), 100);
    const timer = setTimeout(() => finish(new Error("real provider never acknowledged its held source input")), 15000);
    const finish = (error) => { clearTimeout(timer); clearInterval(poll); error ? reject(error) : resolve(); };
    const check = () => {
      if (!existsSync(file)) return;
      try {
        const value = JSON.parse(readFileSync(file, "utf8"));
        assert.deepEqual(value, { role: 0, input: "CASCADE_BUSY_INPUT" });
        finish();
      } catch (error) { if (!(error instanceof SyntaxError)) finish(error); }
    };
    check();
  });
}

const readableGeometrySettled = `(() => {
  const columns = [...document.querySelectorAll('[data-testid="cascade-column"]')];
  return columns.length === 2 && Math.abs(columns[0].getBoundingClientRect().width - 400) < .75 && columns[1].getBoundingClientRect().width >= 439.5
    && columns.every(node => getComputedStyle(node).transform === 'none');
})()`;

async function observeGeometryAndFocus(ref) {
  await wait(readableGeometrySettled, "settled geometry before live status updates");
  await read(`(() => {
    document.querySelector(${q(peekChip(ref, "Tasks"))}).focus();
    window.__cascadeFocused = document.activeElement;
    window.__cascadeGeometryChanges = [];
    window.__cascadeGeometryObserver = new MutationObserver(records => {
      for (const record of records) if (record.target.matches('[data-scope-ref]')) window.__cascadeGeometryChanges.push({ ref: record.target.dataset.scopeRef, style: record.target.getAttribute('style') });
    });
    window.__cascadeGeometryObserver.observe(document.querySelector('[data-pane-scaffold="cascade"]'), { subtree: true, attributes: true, attributeFilter: ['style'] });
  })()`);
}

async function sourceMutationJourney() {
  const ref = fixture.rootRef;
  const busyInput = "CASCADE_ROLE_0_SENTINEL CASCADE_BUSY_INPUT";
  const queueInput = "CASCADE_ROLE_0_SENTINEL CASCADE_QUEUE_INPUT";
  await driver.clearComposerDraft(ref);
  await driver.focusComposer(ref);
  await driver.typeText(ref, busyInput);
  await holdStorageAcknowledgement(ref, busyInput);
  const after = frames.length;
  await driver.clickSubmit(ref, { text: busyInput, chips: [], tiles: 0 });
  await wait("window.__cascadeStorage.committed && window.__cascadeStorage.release !== null", "real outbox commit with application acknowledgement held");
  await providerHeld();
  const held = await read("window.__cascadeStorage.record");
  assert.ok(held?.clientMutationId, "real committed source mutation retains its identity");
  assert.ok(held.payload.input.some(item => item.text === busyInput), "actual committed record contains the original source input");
  assert.equal(held.targetRef, ref);
  assert.equal(held.method, "turn/start");
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  await returnToSource();
  await driver.focusComposer(ref);
  await driver.typeText(ref, " CASCADE_NEWER_STORAGE_DRAFT");
  const newerDraft = await driver.composerState(ref);
  await read("window.__cascadeStorage.release()");
  await providerHeld();
  await waitFrames(() => sent(after).some(frame => frame.method === "turn/start" && frame.params.clientMutationId === held.clientMutationId && answered(frame)), "original mutation accepted once");
  assert.equal((await driver.composerState(ref)).text, newerDraft.text, "receipt cannot clear a newer returned draft");
  driver.milestone("unresolved-storage-source", { clientMutationId: held.clientMutationId, recipient: ref });

  await driver.clearComposerDraft(ref);
  await driver.focusComposer(ref);
  await driver.typeText(ref, queueInput);
  await driver.clickSubmit(ref, { text: queueInput, chips: [], tiles: 0 });
  await wait(`(() => { const strip = ${driver.queueStripExpr(ref)}; return strip?.rows.some(row => row.text.includes('CASCADE_QUEUE_INPUT')); })()`, "real source queue while original provider call is held");
  await waitFrames(() => sent(after).some(frame => frame.method === "turn/queue" && frame.params.ref === ref && answered(frame)), "real queue acknowledgement");
  const queued = sent(after).find(frame => frame.method === "turn/queue" && frame.params.ref === ref);
  assert.ok(queued.params.clientMutationId && queued.params.clientMutationId !== held.clientMutationId);
  await wait(`document.querySelector(${q(driver.composerSelector(ref))})?.querySelector('[role="textbox"]').textContent === ''`, "queue receipt clears only the submitted source draft");
  await driver.focusComposer(ref);
  await driver.typeText(ref, "CASCADE_UNSENT_AFTER_QUEUE");
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  await observeGeometryAndFocus(ref);
  const releasedAfter = frames.length;
  driver.control("release-provider");
  await waitFrames(() => frames.slice(releasedAfter).some(frame => frame.direction === "Network.webSocketFrameReceived" && frame.method === "thread/status/changed" && frame.params.ref === ref && frame.params.status.type === "idle"), "actual source status settles after queued provider delivery");
  assert.equal(await read("document.activeElement === window.__cascadeFocused"), true, "actual source status changes preserve focused element");
  assert.deepEqual(await read("window.__cascadeGeometryChanges"), [], "actual provider completion never changes geometry");
  await read("window.__cascadeGeometryObserver.disconnect()");
  driver.milestone("live-status-stable-geometry");
  await returnToSource();
  await wait(`(() => { const strip = ${driver.queueStripExpr(ref)}; return !strip || strip.rows.length === 0; })()`, "original queue drains");
  assert.equal((await driver.composerState(ref)).text, "CASCADE_UNSENT_AFTER_QUEUE");
  for (const mutation of [held, queued.params]) {
    const requests = sent(after).filter(frame => frame.params?.clientMutationId === mutation.clientMutationId);
    assert.equal(requests.length, 1, "inspection and return never duplicate original mutation identity");
    assert.equal(requests[0].params.ref, ref, "original recipient is retained");
  }
  driver.milestone("queued-source-single-delivery", { clientMutationId: queued.params.clientMutationId, recipient: ref });
  await capture("queued-source-single-delivery");
}

// Both rows come from the root's real project catalog. A native row click,
// selected by its description, distinguishes identities with identical labels.
async function completeOverlap(ref, kind) {
  const description = `Cascade overlap ${kind} fixture`;
  const point = await wait(`(() => {
    const button = [...document.querySelectorAll('[data-testid="composer-slash-menu"] button')].find(node => node.textContent.includes(${q(description)}));
    if (!button) return null;
    const r = button.getBoundingClientRect(); return { x:r.x + r.width / 2, y:r.y + r.height / 2 };
  })()`, `real overlapping ${kind} catalog row`);
  await driver.clickAt(point.x, point.y);
  await wait(`document.querySelector(${q(driver.composerSelector(ref))})?.querySelector('[data-${kind}-name="cascade-overlap"]') !== null && document.querySelector('[data-testid="composer-slash-menu"]') === null`, `selected overlapping ${kind} atom`);
}

function overlapMentions(text) {
  const first = text.indexOf('/cascade-overlap');
  const second = text.indexOf('/cascade-overlap', first + 1);
  assert.ok(first >= 0 && second > first && text.indexOf('/cascade-overlap', second + 1) > second, 'two atoms plus same-spelling inert prose');
  return [{ kind: 'command', name: 'cascade-overlap', offset: first }, { kind: 'skill', name: 'cascade-overlap', offset: second }];
}

async function assertOverlap(ref, text, label) {
  const state = await driver.composerState(ref);
  assert.equal(state.text, text, `${label}, exact UTF-16 text and inert prose`);
  assert.deepEqual(state.chips, ['/cascade-overlap'], `${label}, only the selected skill is a skill atom`);
  const atoms = await read(`(() => {
    const editor = ${driver.editorExpr(ref)};
    return [...editor.querySelectorAll('[data-command-name], [data-skill-name]')].map(node => {
      const range = document.createRange(); range.selectNodeContents(editor); range.setEndBefore(node);
      return { kind:node.hasAttribute('data-command-name') ? 'command' : 'skill', name:node.dataset.commandName ?? node.dataset.skillName, offset:range.toString().length };
    });
  })()`);
  assert.deepEqual(atoms, overlapMentions(text), `${label}, atom kind and exact text-owned offsets`);
  const stored = await read(`JSON.parse(localStorage.getItem(${q(`evener.composer.draft.v2.${ref}`)}))`);
  assert.deepEqual(stored, { text, skillNames:['cascade-overlap'], commandNames:['cascade-overlap'], mentions:overlapMentions(text) }, `${label}, complete real persisted draft`);
}

async function mixedSourceJourney() {
  const ref = fixture.rootRef;
  await driver.clearComposerDraft(ref);
  await driver.focusComposer(ref);
  await driver.typeText(ref, '🙂 CASCADE_ROLE_0_SENTINEL CASCADE_MIXED_INPUT ');
  await upload('mixed-first.png');
  await wait("document.querySelector('button[aria-label=\"View mixed-first.png\"] img')?.src.startsWith('data:image/png;base64,')", 'first real overlap image');
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' /cascade-overlap');
  await completeOverlap(ref, 'command');
  await holdCanvasCompletion();
  await upload('mixed-failure.png');
  await wait('window.__cascadeEncode?.encoded && window.__cascadeEncode.release !== null', 'real middle-image native callback held');
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' /cascade-overlap');
  await completeOverlap(ref, 'skill');
  await upload('mixed-last.png');
  await wait("document.querySelector('button[aria-label=\"View mixed-last.png\"] img')?.src.startsWith('data:image/png;base64,')", 'last real overlap image');
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' /cascade-overlap');
  await key('Escape', 27);
  const before = await driver.composerState(ref);
  const markers = before.text.match(/\[image \d+\]/g) ?? [];
  assert.equal(markers.length, 3, 'each real upload inserted its own marker');
  assert.equal(new Set(markers).size, 3, 'retained source allocates unique image markers');
  const [firstMarker, failedMarker, lastMarker] = markers;
  await assertOverlap(ref, before.text, 'before mounted image settlement');
  const imageBytes = await read("['mixed-first.png','mixed-last.png'].map(name => document.querySelector(`button[aria-label=\"View ${name}\"] img`).src)");
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  await read('window.__cascadeEncode.release(false)');
  await wait(`${driver.toastExpr()}.includes('mixed-failure.png (image decode failed)')`, 'actual failed decode settles while source editor stays mounted');
  await returnToSource();
  const settledText = before.text.replace(failedMarker, '');
  await assertOverlap(ref, settledText, 'after mounted image settlement and Return');
  assert.equal((await driver.composerState(ref)).tiles, 2, 'only failed middle image is removed');
  assert.deepEqual(await read("['mixed-first.png','mixed-last.png'].map(name => document.querySelector(`button[aria-label=\"View ${name}\"] img`).src)"), imageBytes, 'exact retained PNG bytes');
  driver.milestone('mixed-mounted-image-return', { recipient:ref, mentions:overlapMentions(settledText) });

  await holdStorageAcknowledgement(ref, settledText);
  const after = frames.length;
  await driver.clickSubmit(ref, { text:settledText, chips:['/cascade-overlap'], tiles:2 });
  await wait('window.__cascadeStorage.committed && window.__cascadeStorage.release !== null', 'real mixed native outbox acknowledgement held');
  const held = await read('window.__cascadeStorage.record');
  assert.equal(held.targetRef, ref, 'mixed record retains original recipient');
  assert.equal(held.method, 'turn/start');
  assert.equal(held.composerText, settledText);
  assert.deepEqual(held.composerMentions, overlapMentions(settledText));
  assert.deepEqual(held.payload.input.filter(item => ['command','skill'].includes(item.type)).map(({type,name}) => ({type,name})), [{type:'skill',name:'cascade-overlap'}, {type:'command',name:'cascade-overlap'}], 'actual input keeps both identities in public wire order');
  assert.equal(held.attachments.length, 2, 'actual outbox retains both image payloads');
  assert.ok(held.clientMutationId, 'native committed mutation has an identity');
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  await returnToSource();
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' CASCADE_NEWER_MIXED_DRAFT');
  const newer = await driver.composerState(ref);
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  await read('window.__cascadeStorage.release()');
  await waitFrames(() => sent(after).some(frame => frame.params?.clientMutationId === held.clientMutationId && answered(frame)), 'mixed original mutation acknowledged exactly once');
  await returnToSource();
  await wait(`document.querySelector(${q(driver.composerSelector(ref))})?.querySelectorAll('[data-testid="attachment-tile"]').length === 0`, 'submitted images retire after native acknowledgement');
  const cleaned = newer.text.replace(firstMarker, '').replace(lastMarker, '');
  await assertOverlap(ref, cleaned, 'after independent submitted marker cleanup and Return');
  const requests = sent(after).filter(frame => frame.params?.clientMutationId === held.clientMutationId);
  assert.equal(requests.length, 1, 'mixed inspection and Return never duplicate delivery');
  assert.equal(requests[0].params.ref, ref);
  driver.milestone('mixed-held-storage-return', { recipient:ref, clientMutationId:held.clientMutationId, mentions:overlapMentions(cleaned) });
  await capture('mixed-held-storage-return');
}

const layoutExpr = "JSON.parse(localStorage.getItem('evener.workspace.layout.v2') || 'null')";

function findPlacement(layout, id) {
  function find(node, path = []) {
    if (node.type === "leaf") return node.data.views.includes(id) ? { path, size: node.size, group: node.data } : null;
    for (let index = 0; index < node.data.length; index++) {
      const found = find(node.data[index], [...path, index]);
      if (found) return found;
    }
    return null;
  }
  return layout?.grid?.root ? find(layout.grid.root) : null;
}

function placement(layout, id) {
  const found = findPlacement(layout, id);
  assert.ok(found, `actual saved grid contains ${id}`);
  return found;
}

// Found and clicked in one page turn, so a layout shift between measuring and
// pressing cannot send the press to a neighbour (#3804, #3819). A button that
// is disabled or covered at its center is not clicked; the wait keeps polling.
async function clickColumnAction(ref, text) {
  await wait(`(() => {
    const button = [...document.querySelectorAll(${q(`${column(ref)} button`)})].find(node => node.textContent.trim() === ${q(text)});
    if (!button || button.matches(':disabled, [aria-disabled="true"]')) return null;
    button.scrollIntoView({ block: 'center', inline: 'nearest' });
    const r = button.getBoundingClientRect();
    if (!button.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2))) return null;
    button.click();
    return true;
  })()`, `${text} in actual ${ref} column`);
}

async function activeTabText(scaffold) {
  return wait(`document.querySelector(${q(scaffold)})?.closest('.dv-groupview')?.querySelector('.dv-active-tab')?.textContent`, "the actual pane group exposes its active tab label");
}

async function assertInspectorFocus(layout) {
  const group = placement(layout, fixture.inspectorPaneId).group;
  assert.equal(layout.activeGroup, group.id, "the saved active group owns inspection");
  assert.equal(group.activeView, fixture.inspectorPaneId, "the saved active tab is the same inspector");
  assert.equal(await read(`document.querySelector(${q(inspectorRoot)})?.closest('.dv-groupview')?.classList.contains('dv-active-group') === true`), true, "the actual active Dockview group owns inspection");
}

async function activateTab(text, scaffold, paneId) {
  const point = await wait(`(() => {
    const saved = (${findPlacement.toString()})(${layoutExpr}, ${q(paneId)});
    const group = document.querySelector(${q(scaffold)})?.closest('.dv-groupview');
    const tabs = [...(group?.querySelectorAll('.dv-tab') ?? [])];
    if (!saved || tabs.length !== saved.group.views.length) return null;
    const tab = tabs[saved.group.views.indexOf(${q(paneId)})];
    if (tab?.textContent !== ${q(text)}) return null;
    tab.scrollIntoView({ block:'center', inline:'nearest' });
    const r = tab.getBoundingClientRect(); return { x:r.x + r.width / 2, y:r.y + r.height / 2 };
  })()`, "the exact observed independent tab is available for native activation");
  await driver.clickAt(point.x, point.y);
}

async function installLateAncestryHold() {
  const installed = await driver.send("Page.addScriptToEvaluateOnNewDocument", { source: `(() => {
    const Native = window.WebSocket;
    const hold = window.__cascadeLate = { active: true, replies: [], requests: new Map() };
    const methods = new Set(['evener/thread/activity/read', 'evener/thread/delegates/list', 'evener/thread/jobs/list', 'evener/thread/watches/list']);
    window.WebSocket = class extends Native {
      send(data) {
        const request = JSON.parse(data);
        const isAncestry = methods.has(request.method) && request.params?.ref === ${q(fixture.refs[6])};
        const isLocation = request.method === 'evener/navigation/read' && request.params?.resource === 'location' && request.params.ref === ${q(fixture.rootRef)};
        if (hold.active && (isAncestry || isLocation)) hold.requests.set(request.id, isLocation ? 'location' : 'ancestry');
        return super.send(data);
      }
      set onmessage(listener) {
        super.onmessage = listener === null ? null : event => {
          const response = JSON.parse(event.data);
          if (hold.active && hold.requests.has(response.id)) {
            hold.replies.push({ id: response.id, location: hold.requests.get(response.id) === 'location', response: response.result, error: response.error, context: response.result?.context, release: () => listener.call(this, event) });
          } else listener.call(this, event);
        };
      }
      get onmessage() { return super.onmessage; }
    };
  })()` });
  return installed.result.identifier;
}

async function reloadAndMobileJourney() {
  await driver.clearComposerDraft(fixture.rootRef);
  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, "CASCADE_UNSENT_RELOAD_SOURCE /cascade-source");
  await driver.completeSkill(fixture.rootRef, "cascade-source");
  const source = await driver.composerState(fixture.rootRef);
  assert.deepEqual(source.chips, ["/cascade-source"]);
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  const openedInspectorTabText = await activeTabText(inspectorRoot);
  await clickColumnAction(fixture.childRef, "Open conversation");
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.childRef))}) !== null`, "explicit independent child conversation");
  await driver.focusComposer(fixture.childRef);
  await driver.typeText(fixture.childRef, "CASCADE_UNSENT_UNRELATED_DRAFT");
  const unrelatedId = await read(`${driver.paneScopeExpr(fixture.childRef)}.querySelector('[data-testid="statusbar"] [data-pane-id]').dataset.paneId`);
  assert.ok(unrelatedId && unrelatedId !== fixture.sourcePaneId);
  const unrelatedTabText = await activeTabText(`[data-pane-scaffold=${q(`session:${fixture.childRef}`)}]`);
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount + 2, "explicit Open adds an independent tab beside the inspector");
  fixture.sourceTabCount++;
  const removedInspectorId = fixture.inspectorPaneId;
  await activateTab(openedInspectorTabText, `[data-pane-scaffold=${q(`session:${fixture.childRef}`)}]`, removedInspectorId);
  await wait(`document.querySelector(${q(column(fixture.rootRef))}) !== null && ${inspectorIdExpr} === ${q(removedInspectorId)}`, "the same inspector is active before its original-conversation action");
  await clickColumnAction(fixture.rootRef, "Open conversation");
  await wait(`document.querySelector(${q(inspectorRoot)}) === null && document.querySelector(${q(driver.composerSelector(fixture.rootRef))})?.querySelector('[role="textbox"]')?.contains(document.activeElement) === true`, "original Open closes inspection and focuses its existing source editor");
  const baseline = await wait(`(() => {
    const saved = ${layoutExpr};
    return saved?.panels[${q(fixture.sourcePaneId)}]?.params?.paneType === 'session' && saved.panels[${q(unrelatedId)}] && !saved.panels[${q(removedInspectorId)}] ? saved : null;
  })()`, "native debounce saved both actual ordinary panels");
  assert.equal(baseline.activeGroup, placement(baseline, fixture.sourcePaneId).group.id);
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount);
  await assertSourceDom();
  fixture.inspectorPaneId = null;
  const sourcePanel = baseline.panels[fixture.sourcePaneId];
  const unrelatedPanel = baseline.panels[unrelatedId];
  const baselinePlacement = placement(baseline, unrelatedId);
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  for (let level = 2; level <= 6; level++) await drill(fixture.edges[level - 1], level);
  assert.notEqual(fixture.inspectorPaneId, removedInspectorId, "retired inspection is not reused on fresh entry");
  const saved = await wait(`(() => {
    const saved = ${layoutExpr}, panel = saved?.panels[${q(fixture.inspectorPaneId)}];
    return panel?.params?.paneType === 'sessionZoom' && panel.params.paneParams.ref === ${q(fixture.refs[6])} && saved.panels[${q(fixture.sourcePaneId)}]?.params?.paneType === 'session' ? saved : null;
  })()`, "native debounce persisted selected leaf intent");
  const expectedIntent = {
    ref: fixture.refs[6], source: { type: "transcript", params: { ref: fixture.rootRef } },
    edges: fixture.edges.map(({ ownerRef, childRef, delegateId }) => ({ ownerRef, childRef, delegateId })),
    inspection: { origin: { paneId: fixture.sourcePaneId, type: "session", ref: fixture.rootRef } },
  };
  assert.deepEqual(saved.panels[fixture.inspectorPaneId].params.paneParams, expectedIntent, "saved inspector matches independently supplied real edges and exact original source");
  assert.deepEqual(saved.panels[fixture.sourcePaneId], sourcePanel, "the saved center remains its original session panel");
  const inspectorPlacement = placement(saved, fixture.inspectorPaneId);
  assert.notEqual(inspectorPlacement.group.id, placement(saved, fixture.sourcePaneId).group.id, "inspection is saved in a separate grid group");
  await assertInspectorFocus(saved);
  const inspectorTabText = await activeTabText(inspectorRoot);
  assert.deepEqual(saved.panels[unrelatedId], unrelatedPanel);
  const unrelatedPlacement = placement(saved, unrelatedId);
  assert.deepEqual(unrelatedPlacement.path, baselinePlacement.path);
  assert.equal(unrelatedPlacement.size, baselinePlacement.size);
  assert.equal(unrelatedPlacement.group.id, baselinePlacement.group.id);
  assert.deepEqual(unrelatedPlacement.group.views.filter(id => id !== fixture.inspectorPaneId), baselinePlacement.group.views, "all ordinary tabs keep their order beside inspection");
  assert.equal(unrelatedPlacement.group.views.at(-1), fixture.inspectorPaneId, "inspection is appended to the existing secondary group");
  assert.equal(JSON.stringify(saved).includes("data:image"), false, "layout never stores source image bytes");
  writeFileSync(path.join(fixture.artifactDir, "before-reload-layout.json"), JSON.stringify(saved, null, 2));
  const holdScriptId = await installLateAncestryHold();
  try {
    await driver.send("Page.reload", { ignoreCache: true });
    await wait(`document.querySelector(${q(column(fixture.refs[6]))}) !== null && document.querySelector(${q(inspectorRoot)}).textContent.includes('Earlier ancestry is incomplete')`, "saved path paints before actual ancestry delivery");
    const actualContext = await wait(`window.__cascadeLate.replies.find(reply => reply.context?.ancestryKnown && reply.context.ancestors.length === 6)?.context`, "actual known leaf ancestry reply is held at native message delivery");
    assert.deepEqual(actualContext.ancestors.map(ancestor => ancestor.ref), fixture.refs.slice(0, 6));
    await wait("window.__cascadeLate.replies.some(reply => reply.location && reply.response && !reply.error)", "actual successful route location reply is held at native message delivery");
    assert.deepEqual(await read(`[...document.querySelectorAll(${q(`${inspectorRoot} [data-scope-ref]`)})].map(node => node.dataset.scopeRef)`), fixture.refs, "unknown ancestry preserves six saved edges");
    assert.equal(await read(inspectorIdExpr), fixture.inspectorPaneId);
    assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount + 1);
    await pinSourceDom();
    await assertInspectorFocus(await read(layoutExpr));
    await observeGeometryAndFocus(fixture.refs[5]);
    await read("(() => { const hold = window.__cascadeLate; hold.active = false; for (const reply of hold.replies) reply.release(); hold.replies = []; })()");
    await wait("!document.querySelector('[data-pane-scaffold=\"cascade\"]').textContent.includes('Earlier ancestry is incomplete')", "actual ancestry and route recovery settle");
    assert.equal(await read("document.activeElement === window.__cascadeFocused"), true, "late ancestry and location preserve exact focus");
    assert.deepEqual(await read("window.__cascadeGeometryChanges"), [], "late ancestry never animates geometry");
    await assertInspectorFocus(await read(layoutExpr));
    await assertSourceDom();
    driver.milestone("late-ancestry-stable-geometry", { inspectorPaneId: fixture.inspectorPaneId, heldLocation: true });
  } finally {
    await read("(() => { const hold = window.__cascadeLate; if (!hold) return; hold.active = false; for (const reply of hold.replies) reply.release(); hold.replies = []; window.__cascadeGeometryObserver?.disconnect(); })()");
    await driver.send("Page.removeScriptToEvaluateOnNewDocument", { identifier: holdScriptId });
  }
  const restored = await read(layoutExpr);
  assert.deepEqual(restored.panels[fixture.inspectorPaneId].params.paneParams, expectedIntent);
  assert.deepEqual(restored.panels[fixture.sourcePaneId], sourcePanel);
  assert.deepEqual(placement(restored, fixture.inspectorPaneId), inspectorPlacement);
  assert.deepEqual(await read(`[...document.querySelectorAll(${q(`${inspectorRoot} [data-scope-ref]`)})].map(node => node.dataset.scopeRef)`), fixture.refs);
  assert.deepEqual(restored.panels[unrelatedId], unrelatedPanel);
  assert.deepEqual(placement(restored, unrelatedId), unrelatedPlacement);
  // Inactive secondary tabs retain their work under normal mounting rules.
  // Activate the actual saved tab before observing its recovered composer.
  await activateTab(unrelatedTabText, inspectorRoot, unrelatedId);
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.childRef))}) !== null`, "restored unrelated pane's composer mounts");
  assert.equal(await read(`${driver.paneScopeExpr(fixture.childRef)}.querySelector('[data-testid="statusbar"] [data-pane-id]').dataset.paneId`), unrelatedId);
  assert.equal((await driver.composerState(fixture.childRef)).text, "CASCADE_UNSENT_UNRELATED_DRAFT");
  await activateTab(inspectorTabText, `[data-pane-scaffold=${q(`session:${fixture.childRef}`)}]`, fixture.inspectorPaneId);
  await wait(`document.querySelector(${q(column(fixture.refs[6]))}) !== null && ${inspectorIdExpr} === ${q(fixture.inspectorPaneId)}`, "native tab activation restores the same saved inspector and selected path");
  const savedGroupExpr = inspectorPlacement.path.reduce((node, index) => `${node}.data[${index}]`, "saved.grid.root") + ".data";
  const refocused = await wait(`(() => {
    const saved = ${layoutExpr};
    return saved?.activeGroup === ${q(inspectorPlacement.group.id)} && ${savedGroupExpr}.activeView === ${q(fixture.inspectorPaneId)} ? saved : null;
  })()`, "debounced layout records the native inspector reactivation");
  await assertInspectorFocus(refocused);
  assert.deepEqual(refocused.panels[fixture.inspectorPaneId].params.paneParams, expectedIntent);
  assert.deepEqual(placement(refocused, unrelatedId), unrelatedPlacement);
  assert.deepEqual(await read(`[...document.querySelectorAll(${q(`${inspectorRoot} [data-scope-ref]`)})].map(node => node.dataset.scopeRef)`), fixture.refs);
  await assertSourceDom();
  driver.milestone("unrelated-pane-reload", { sourcePaneId: fixture.sourcePaneId, inspectorPaneId: fixture.inspectorPaneId, unrelatedPaneId: unrelatedId, selectedRef: fixture.refs[6], edges: expectedIntent.edges });
  await capture("unrelated-pane-reload");
  await returnToSource();
  const desktopReturned = await driver.composerState(fixture.rootRef);
  assert.equal(desktopReturned.text, source.text);
  assert.deepEqual(desktopReturned.chips, source.chips);
  assert.deepEqual((await read(layoutExpr)).panels[unrelatedId], unrelatedPanel);
  assert.deepEqual(placement(await read(layoutExpr), unrelatedId), baselinePlacement);
  await capture("desktop-source-return-after-reload");
  await driver.click(sourceAgents);
  await drill(fixture.edges[0], 1);
  for (let level = 2; level <= 6; level++) await drill(fixture.edges[level - 1], level);
  const phoneLayout = await wait(`(() => {
    const saved = ${layoutExpr};
    return saved?.panels[${q(fixture.inspectorPaneId)}]?.params?.paneParams.ref === ${q(fixture.refs[6])} ? saved : null;
  })()`, "the new phone inspector has an actual saved selected-leaf intent");
  assert.deepEqual(phoneLayout.panels[fixture.inspectorPaneId].params.paneParams, expectedIntent);
  await assertInspectorFocus(phoneLayout);
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-sidebar]") === null', "close desktop sidebar before phone scene");
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  await wait(`document.querySelectorAll('[data-testid="cascade-column"]').length === 1 && document.querySelector(${q(column(fixture.refs[6]))}) !== null && document.querySelectorAll('[data-testid="cascade-spine"]').length === 0`, "saved phone cascade contains only selected readonly leaf");
  assert.equal(await read('document.querySelectorAll("[role=textbox]").length'), 0, "saved phone cascade has no composer");
  await wait(`document.querySelector(${q(column(fixture.refs[6]))}).textContent.includes('CASCADE_ROLE_6_SENTINEL')`, "real selected phone transcript");
  driver.milestone("mobile-saved-cascade");
  await capture("mobile-saved-cascade");
  await driver.clickByText("Return to previous view");
  await wait(`document.querySelector(${q(inspectorRoot)}) === null && document.querySelector(${q(driver.composerSelector(fixture.rootRef))}) !== null`, "phone Return removes inspection and leaves its original source useful");
  assert.equal(await read(`${driver.paneScopeExpr(fixture.rootRef)}.querySelector(${q(`[data-session-actions-ref=${q(fixture.rootRef)}]`)})?.dataset.paneId`), fixture.sourcePaneId);
  fixture.inspectorPaneId = null;
  const returned = await driver.composerState(fixture.rootRef);
  assert.equal(returned.text, source.text);
  assert.deepEqual(returned.chips, source.chips);
  await capture("phone-source-return");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-sidebar]") === null', "dismiss restored source Overview before phone menu gesture");
  await readInsideTallEntry(sourcePortExpr, "phone source");
  await widthOnlyReflow(sourcePortExpr, 430, 844, "phone-reader-width-reflow");
  await widthOnlyReflow(sourcePortExpr, 390, 844, "phone-reader-width-return");
  await driver.click(`${driver.composerSelector(fixture.rootRef)} [data-testid="session-chrome-inline"] button[aria-haspopup="menu"]`);
  const overview = await wait(`(() => {
    const button = [...document.querySelectorAll('[role="menuitem"]')].find(node => node.textContent.trim().startsWith('Overview'));
    if (!button) return null; const r = button.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  })()`, "existing phone Overview menu action");
  await driver.clickAt(overview.x, overview.y);
  await wait(`(() => {
    const sidebar = document.querySelector('[data-testid="activity-sidebar"]');
    return sidebar && getComputedStyle(sidebar).transform === 'none';
  })()`, "phone Overview entrance settled before physical row tap");
  const mobileRow = await reveal(fixture.edges[0], '[data-testid="activity-sidebar"]');
  await driver.click(mobileRow);
  await wait('document.querySelectorAll("[data-testid=transcript-virtual-list]").length === 1 && document.querySelectorAll("[role=textbox]").length === 0 && document.querySelector("[data-pane-scaffold=cascade]") === null', "ordinary mobile Agents transcript action stays readonly without cascade");
  await wait("document.body.textContent.includes('CASCADE_ROLE_1_SENTINEL')", "real ordinary phone child transcript");
  assert.equal(await read('document.querySelectorAll("[data-testid=cascade-column], [data-testid=cascade-spine]").length'), 0);
  assert.deepEqual((await read(layoutExpr)).panels[unrelatedId], unrelatedPanel, "phone action retains independent pane identity");
  driver.milestone("mobile-agents-transcript", { childRef: fixture.childRef, unrelatedPaneId: unrelatedId });
  await capture("mobile-agents-transcript");
}

async function reveal(edge, container) {
  const selector = `${container} ${row(edge)}`;
  await wait(`document.querySelector(${q(selector)}) !== null || [...document.querySelectorAll(${q(`${container} button`)})].some(b => b.textContent.trim().startsWith('Inactive subagents ('))`, "real direct delegate collection");
  for (let boundary = 0; boundary < 10; boundary++) {
    if (await read(`document.querySelector(${q(selector)}) !== null`)) return selector;
    const visible = await read(`document.querySelectorAll(${q(`${container} [data-activity-anchor]`)}).length`);
    // The control is found and activated in one page turn. Scrolling a Show or
    // Load more control into view can make the page boundary under it load the
    // next page by itself; when that page lands it removes the boundary and the
    // list shifts, so a press measured before it can land on a neighbouring
    // delegate row and drill into it (#3804). The click still requires the
    // control to be the topmost element at its center, as a real press would.
    const step = await wait(`(() => {
      if (document.querySelector(${q(selector)})) return { revealed: true };
      const buttons = [...document.querySelectorAll(${q(`${container} button`)})];
      const b = buttons.find(n => !n.disabled && (${visible} === 0 ? n.textContent.trim().startsWith('Inactive subagents (') : n.textContent.trim().startsWith('Show ') || n.textContent.trim() === 'Load more subagents'));
      if (!b) return null;
      b.scrollIntoView({ block: 'center' });
      const r = b.getBoundingClientRect();
      if (!b.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2))) return null;
      const before = document.querySelector(${q(container)}).textContent;
      b.click();
      return { before };
    })()`, "real disclosure or direct page boundary");
    if (step.revealed) return selector;
    await wait(`document.querySelector(${q(selector)}) !== null || document.querySelector(${q(container)}).textContent !== ${q(step.before)}`, "direct collection progresses");
  }
  throw new Error(`real delegate did not become visible through ten boundaries: ${edge.delegateId}`);
}

async function waitFrames(predicate, label) {
  if (predicate()) return;
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => finish(new Error(`actual RPC condition did not arrive: ${label}`)), 15000);
    const check = () => { if (predicate()) finish(); };
    const finish = (error) => {
      clearTimeout(timer);
      driver.page.ws.removeEventListener("message", check);
      error ? reject(error) : resolve();
    };
    driver.page.ws.addEventListener("message", check);
    check();
  });
}

const sent = (after = 0) => frames.slice(after).filter(f => f.direction === "Network.webSocketFrameSent");
const answered = (request) => frames.some(f => f.direction === "Network.webSocketFrameReceived" && f.id === request.id && Object.hasOwn(f, "result") && !f.error);

async function reconnect() {
  const after = frames.length;
  const sockets = await read(`(() => {
    const sockets = window.__cascadeSockets.filter(s => s.readyState === WebSocket.OPEN && new URL(s.url).pathname === '/rpc');
    for (const socket of sockets) socket.close(4000, 'cascade guard transport boundary');
    return sockets.length;
  })()`);
  assert.equal(sockets, 1, "one actual shared hub socket is closed");
  await waitFrames(() => fixture.refs.every(ref => sent(after).some(f => f.method === "thread/read" && f.params.ref === ref && f.params.subscribe && answered(f))), "all seven real subscription owners recover");
  const subscriptions = sent(after).filter(f => f.method === "thread/read" && f.params.subscribe);
  assert.ok(subscriptions.length >= 7);
  assert.ok(subscriptions.every(f => f.params.replaceSubscription === false), "every admitted thread membership is additive");
  return after;
}

async function drill(edge, level) {
  const selector = await reveal(edge, '[data-testid="activity-sidebar"]');
  await driver.click(selector);
  await wait(`document.querySelector(${q(column(edge.childRef))}) !== null`, `selected column at level ${level}`);
  const expected = fixture.refs.slice(Math.max(0, level - 1), level + 1);
  await wait(`JSON.stringify([...document.querySelectorAll('[data-testid="cascade-column"]')].map(n => n.dataset.scopeRef)) === ${q(JSON.stringify(expected))}`, `exact column pair ${level}`);
  await wait(`document.querySelector(${q(column(edge.childRef))}).textContent.includes(${q(`CASCADE_ROLE_${level}_SENTINEL`)})`, `real retained transcript ${level}`);
  const id = await wait(inspectorIdExpr, "secondary inspector has a committed pane ID");
  assert.notEqual(id, fixture.sourcePaneId, "inspection never replaces the source panel");
  if (fixture.inspectorPaneId) assert.equal(id, fixture.inspectorPaneId, "drill reuses the inspector");
  else fixture.inspectorPaneId = id;
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount + 1);
  await assertInspectorReadOnly();
}

try {
  await driver.start();
  const measurementScript = await driver.send("Page.addScriptToEvaluateOnNewDocument", { source: `(() => {
    const Native = window.ResizeObserver;
    const state = window.__cascadeMeasurements = {
      Native, port: null, row: null, queued: [],
      release() {
        this.port = null;
        this.row = null;
        const pending = this.queued.splice(0);
        for (const { callback, entries, observer } of pending) {
          const connected = entries.filter(entry => entry.target.isConnected);
          if (connected.length) callback(connected, observer);
        }
      }
    };
    window.ResizeObserver = class extends Native {
      constructor(callback) {
        super((entries, observer) => {
          const held = entries.filter(entry => state.port && state.row &&
            state.row.isConnected && state.port.contains(state.row) && entry.target === state.row);
          const immediate = entries.filter(entry => !held.includes(entry));
          if (immediate.length) callback(immediate, observer);
          if (held.length) state.queued.push({ callback, entries: held, observer });
        });
      }
      unobserve(target) {
        for (const pending of state.queued) pending.entries = pending.entries.filter(entry => entry.target !== target);
        state.queued = state.queued.filter(pending => pending.entries.length > 0);
        super.unobserve(target);
      }
      disconnect() {
        state.queued = state.queued.filter(pending => pending.observer !== this);
        super.disconnect();
      }
    };
  })()` });
  measurementScriptId = measurementScript.result.identifier;
  driver.page.ws.addEventListener("message", (event) => {
    const value = JSON.parse(event.data);
    if (value.method === "Runtime.exceptionThrown") errors.push(value.params);
    if (value.method === "Runtime.consoleAPICalled") {
      consoleEvents.push(value.params);
      if (["error", "warning"].includes(value.params.type)) errors.push(value.params);
    }
    if (["Network.webSocketFrameSent", "Network.webSocketFrameReceived"].includes(value.method)) {
      try { frames.push({ direction: value.method, requestId: value.params.requestId, ...JSON.parse(value.params.response.payloadData) }); } catch {}
    }
  });
  await driver.send("Page.addScriptToEvaluateOnNewDocument", { source: `(() => {
    const Native = window.WebSocket;
    window.__cascadeSockets = [];
    window.__cascadeNativeHits = [];
    window.__cascadeStartupTrace = [];
    window.__cascadeTraceStartup = (phase, detail) => {
      const port = ${sourcePortExpr};
      if (!port || window.__cascadeStartupTrace.length >= 2500) return;
      const bounds = port.getBoundingClientRect();
      const rows = [...port.querySelectorAll('[data-index]')].map(node => {
        const box = node.getBoundingClientRect();
        return { index:node.dataset.index, row:node.querySelector('[data-row-id]')?.dataset.rowId,
          offset:box.top - bounds.top, height:box.height };
      });
      const active = document.activeElement;
      window.__cascadeStartupTrace.push({ phase, detail, at:performance.now(), scrollTop:port.scrollTop,
        scrollHeight:port.scrollHeight, width:port.clientWidth, height:port.clientHeight, rows,
        sizerHeight:port.firstElementChild?.getBoundingClientRect().height,
        sizerStyle:port.firstElementChild?.getAttribute('style'),
        active:{ tag:active?.tagName, role:active?.getAttribute('role'), label:active?.getAttribute('aria-label'),
          anchor:active?.closest('[data-view-anchor-id]')?.dataset.viewAnchorId, inside:port.contains(active) },
        overflowAnchor:getComputedStyle(port).overflowAnchor, scrollBehavior:getComputedStyle(port).scrollBehavior });
    };
    const targetDetail = target => {
      const port = ${sourcePortExpr}, bounds = target.getBoundingClientRect();
      return { tag:target.tagName, role:target.getAttribute('role'), label:target.getAttribute('aria-label'),
        text:target.textContent.slice(0, 100), inside:port?.contains(target),
        surface:target.closest('[data-pane-scaffold]')?.dataset.paneScaffold,
        anchor:target.closest('[data-view-anchor-id]')?.dataset.viewAnchorId,
        bounds:{ x:bounds.x, y:bounds.y, width:bounds.width, height:bounds.height } };
    };
    const nativeFocus = HTMLElement.prototype.focus;
    HTMLElement.prototype.focus = function(...args) {
      window.__cascadeTraceStartup('focus-before', { target:targetDetail(this), args, stack:new Error().stack });
      const result = nativeFocus.apply(this, args);
      window.__cascadeTraceStartup('focus-after');
      return result;
    };
    for (const method of ['scrollIntoView', 'scrollBy']) {
      const native = Element.prototype[method];
      Element.prototype[method] = function(...args) {
        window.__cascadeTraceStartup(method + '-before', { target:targetDetail(this), args, stack:new Error().stack });
        const result = native.apply(this, args);
        window.__cascadeTraceStartup(method + '-after');
        return result;
      };
    }
    const nativeScroll = Element.prototype.scrollTo;
    Element.prototype.scrollTo = function(...args) {
      const source = this === ${sourcePortExpr};
      if (source) window.__cascadeTraceStartup('scrollTo-before', { args, stack:new Error().stack });
      const result = nativeScroll.apply(this, args);
      if (source) window.__cascadeTraceStartup('scrollTo-after');
      return result;
    };
    const scrollTop = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop');
    if (scrollTop?.set) Object.defineProperty(Element.prototype, 'scrollTop', {
      ...scrollTop,
      set(value) {
        const source = this === ${sourcePortExpr};
        if (source) window.__cascadeTraceStartup('scrollTop-before', { value, stack:new Error().stack });
        const result = scrollTop.set.call(this, value);
        if (source) window.__cascadeTraceStartup('scrollTop-after');
        return result;
      }
    });
    for (const type of ['scroll', 'scrollend']) document.addEventListener(type, event => {
      if (event.target === ${sourcePortExpr}) window.__cascadeTraceStartup(type);
    }, { capture:true, passive:true });
    for (const type of ['focusin', 'focusout', 'keydown', 'wheel']) document.addEventListener(type, event => {
      window.__cascadeTraceStartup(type, { key:event.key, deltaY:event.deltaY, trusted:event.isTrusted, tag:event.target.tagName,
        role:event.target.getAttribute('role'), label:event.target.getAttribute('aria-label'),
        anchor:event.target.closest('[data-view-anchor-id]')?.dataset.viewAnchorId });
    }, { capture:true, passive:true });
    for (const type of ['mousedown', 'mouseup', 'click']) document.addEventListener(type, event => {
      window.__cascadeNativeHits.push({ type, x:event.clientX, y:event.clientY, viewportWidth:innerWidth,
        hitAnchor:event.target.closest('[data-activity-anchor]')?.dataset.activityAnchor ?? null,
        button:event.target.closest('button')?.textContent.slice(0, 100) ?? null });
    }, true);
    window.WebSocket = class extends Native {
      constructor(...args) { super(...args); window.__cascadeSockets.push(this); }
    };
  })()` });
  await navigateTo(driver.page, fixture.url);
  await driver.openSession(fixture.rootRef);
  fixture.sourcePaneId = await read(`document.querySelector(${q(`${sourceFooter} [data-pane-id]`)})?.dataset.paneId`);
  assert.ok(fixture.sourcePaneId, "source has a committed Dockview pane ID");
  fixture.sourceTabCount = await read("document.querySelectorAll('.dv-tab').length");
  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, "CASCADE_UNSENT_SOURCE_DRAFT");
  await driver.typeText(fixture.rootRef, " /cascade-source");
  await driver.completeSkill(fixture.rootRef, "cascade-source");
  await upload("staged.png");
  await wait(`document.querySelector(${q(stagedPNG)})?.src.startsWith("data:image/png;base64,")`, "staged real PNG bytes");
  const sourcePNG = await read(`document.querySelector(${q(stagedPNG)}).src`);
  const sourceDraft = await driver.composerState(fixture.rootRef);
  assert.deepEqual(sourceDraft.chips, ["/cascade-source"], "skill comes from real daemon catalog");
  await pinSourceDom();
  await installSourceTrace();
  const bottomAnchor = await sourceAnchor();
  const sourcePoint = await read(`(() => {
    const port = ${driver.paneScopeExpr(fixture.rootRef)}.querySelector('[data-testid="transcript-virtual-list"] > div');
    const r = port.getBoundingClientRect();
    const row = [...port.querySelectorAll('[data-row-id]')].find(node => node.dataset.rowId === ${q(bottomAnchor)});
    if (!row) throw new Error('Source anchor disappeared before native scroll');
    return { x:r.x + r.width / 2, y:r.y + r.height / 2, deltaY:Math.min(-200, row.getBoundingClientRect().top - r.top - 1) };
  })()`);
  await driver.send("Input.dispatchMouseEvent", { type: "mouseWheel", deltaX: 0, ...sourcePoint });
  await wait(`(() => {
    const port = ${driver.paneScopeExpr(fixture.rootRef)}.querySelector('[data-testid="transcript-virtual-list"] > div');
    return port.scrollHeight - port.clientHeight - port.scrollTop > 100 && ${sourceAnchorExpr} !== ${q(bottomAnchor)};
  })()`, "native source scroll leaves bottom-following for an older visible row");
  const beforeEntryAnchor = await sourceAnchor();
  await read('window.__cascadeTraceSource("before-entry")');
  const inspectionAfter = frames.length;
  await driver.click(sourceAgents);
  await read('window.__cascadeTraceSource("after-sidebar-open")');
  await drill(fixture.edges[0], 1);
  await wait(readableGeometrySettled, "first secondary split has settled readable geometry");
  await read('window.__cascadeTraceSource("after-first-drill")');
  await assertSourceDom();
  assert.equal(await sourceAnchor(), beforeEntryAnchor, "secondary entry preserves the visible source row");
  driver.milestone("root-child", { sourcePaneId: fixture.sourcePaneId, inspectorPaneId: fixture.inspectorPaneId, sourceAnchor: beforeEntryAnchor });
  for (let level = 2; level <= 6; level++) await drill(fixture.edges[level - 1], level);
  await wait(`(() => {
    const spines = [...document.querySelectorAll('[data-testid="cascade-spine"]')];
    const columns = [...document.querySelectorAll('[data-testid="cascade-column"]')];
    return spines.length === 5 && spines.every(n => Math.abs(n.getBoundingClientRect().width - 52) < 0.75)
      && columns.length === 2 && Math.abs(columns[0].getBoundingClientRect().width - 400) < 0.75
      && columns[1].getBoundingClientRect().width >= 439.5;
  })()`, "settled six-edge geometry");
  const boxes = await read(`([...document.querySelectorAll('[data-testid="cascade-spine"], [data-testid="cascade-column"]')]).map(n => {
    const r = n.getBoundingClientRect(); return { ref: n.dataset.scopeRef, kind: n.dataset.testid, x:r.x, y:r.y, width:r.width, height:r.height };
  })`);
  assert.deepEqual(boxes.map(n => n.ref), fixture.refs);
  driver.milestone("six-edges", { boxes, sourcePaneId: fixture.sourcePaneId, inspectorPaneId: fixture.inspectorPaneId, tabs: fixture.sourceTabCount + 1 });
  const paint = await capture("six-edges");
  const cascadePaint = paint.filter(view => view.surface === "cascade");
  assert.equal(cascadePaint.length, 2, "inspection has exactly two readable transcript viewports");
  assert.equal(paint.filter(view => view.surface === `session:${fixture.rootRef}`).length, 1, "the mounted center has its own transcript viewport");
  assert.ok(cascadePaint[0].chain[0].box.width >= 398.5, `parent transcript fills its readable column, got ${cascadePaint[0].chain[0].box.width}px`);
  assert.ok(cascadePaint[1].chain[0].box.width >= 439.5, `leaf transcript fills its readable column, got ${cascadePaint[1].chain[0].box.width}px`);
  assert.ok(paint.every(view => view.rows.some(row => row.intersects && row.box.width > 0 && row.textLength > 0)), "every real transcript viewport contains rendered text rows");
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 1000, height: 900, deviceScaleFactor: 1, mobile: false });
  // A user pop/drill after the resize must reveal the selected leaf. Resizing
  // itself is not a drill and must not move the retained reader's position.
  await wait("window.innerWidth === 1000", "native narrow desktop viewport applied");
  await wait(`(() => {
    const node = ${driver.paneScopeExpr(fixture.rootRef)};
    if (!node) return false;
    const r = node.getBoundingClientRect(), style = getComputedStyle(node);
    return r.width > 0 && r.height > 0 && r.right > 0 && r.left < innerWidth && style.visibility === 'visible' && style.display !== 'none';
  })()`, "the original center remains visible on narrow desktop");
  await assertSourceDom();
  const parentCrumb = 'nav[aria-label="Agent path"] button:nth-last-child(2)';
  await read(`document.querySelector(${q(parentCrumb)}).focus()`);
  await wait(`document.activeElement === document.querySelector(${q(parentCrumb)})`, "parent breadcrumb owns keyboard focus");
  await key("Enter", 13);
  await wait(`document.querySelector(${q(`${column(fixture.refs[5])}[data-leaf]`)}) !== null && document.querySelector(${q(column(fixture.refs[6]))}) === null`, "actual narrow-desktop parent pop");
  await drill(fixture.edges[5], 6);
  const narrow = await wait(`(() => {
    const leaf = document.querySelector(${q(column(fixture.refs[6]))});
    const track = leaf?.parentElement;
    if (!track || track.scrollWidth <= track.clientWidth) return null;
    const a = track.getBoundingClientRect(), b = leaf.getBoundingClientRect();
    return b.right <= a.right + 1 && b.right > a.left ? { width: track.clientWidth, extent: track.scrollWidth, left: track.scrollLeft, leafRight: b.right, trackRight: a.right } : null;
  })()`, "narrow desktop overflow and selected leaf revealed");
  // Both columns are virtualized, and under 2-core load a late row-height
  // correction can leave a column a few pixels off the bottom it was pinned
  // to: the app holds the reader's visual position, so the numeric gap never
  // closes on its own (sighted once as a steady 32px gap with a quiet RPC
  // stream and an empty console). Retained bottom is the precondition the
  // independence check below scrolls FROM, not the behavior under test, so
  // the guard establishes it the same way that check sets its explicit
  // offsets: settle the heights, re-pin both columns, then require the
  // retained-bottom condition to hold on its own.
  await wait(`(() => {
    const nodes = [${q(scroll(fixture.refs[5]))}, ${q(scroll(fixture.refs[6]))}].map((s) => document.querySelector(s));
    if (nodes.includes(null)) return null;
    const stamp = nodes.map((node) => node.scrollHeight).join(",");
    const settled = window.__cascadeScrollHeights === stamp;
    window.__cascadeScrollHeights = stamp;
    return settled;
  })()`, "both cascade columns' scroll geometry settled before the independence check");
  await read(`[${q(scroll(fixture.refs[5]))},${q(scroll(fixture.refs[6]))}].map((s) => { const port = document.querySelector(s); if (port) port.scrollTop = port.scrollHeight; })`);
  await wait(`${readableGeometrySettled} && (() => {
    const ports = [${q(scroll(fixture.refs[5]))},${q(scroll(fixture.refs[6]))}].map(selector => document.querySelector(selector));
    return ports.every(port => port && port.clientHeight > 0 && port.scrollHeight > port.clientHeight
      && Math.abs(port.scrollHeight - port.clientHeight - port.scrollTop) <= 1);
  })()`, "settled readable geometry and retained bottom before independent scrolling");
  const beforeScroll = await read(`[${q(scroll(fixture.refs[5]))},${q(scroll(fixture.refs[6]))}].map(s => document.querySelector(s).scrollTop)`);
  await read(`document.querySelector(${q(scroll(fixture.refs[5]))}).scrollTop = 200`);
  await wait(`document.querySelector(${q(scroll(fixture.refs[5]))}).scrollTop === 200`, "parent scrolled independently");
  assert.equal(await read(`document.querySelector(${q(scroll(fixture.refs[6]))}).scrollTop`), beforeScroll[1]);
  await read(`document.querySelector(${q(scroll(fixture.refs[6]))}).scrollTop = 320`);
  await wait(`document.querySelector(${q(scroll(fixture.refs[6]))}).scrollTop === 320`, "leaf scrolled independently");
  assert.equal(await read(`document.querySelector(${q(scroll(fixture.refs[5]))}).scrollTop`), 200);
  driver.milestone("narrow-independent-scroll", narrow);

  const leafChrome = await read(`({ footer: document.querySelector(${q(inspectorFooter)}).textContent, sidebar: document.querySelector('[data-testid="activity-sidebar"]').textContent })`);
  await read(`(() => { const node = document.querySelector(${q(column(fixture.refs[5]))}).querySelector('[data-testid="turn-block"]'); const range = document.createRange(); range.selectNodeContents(node); const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range); })()`);
  assert.ok(await read("window.getSelection().toString().length > 0"));
  assert.equal(await read('document.querySelector("[data-testid=cascade-column][data-leaf]").dataset.scopeRef'), fixture.refs[6]);
  assert.deepEqual(await read(`({ footer: document.querySelector(${q(inspectorFooter)}).textContent, sidebar: document.querySelector('[data-testid="activity-sidebar"]').textContent })`), leafChrome);
  driver.milestone("parent-selection-leaf-scope");

  await openPeek(fixture.rootRef, "Tasks");
  await wait('document.querySelector("[data-testid=activity-peek]").textContent.includes("No tasks")', "Tasks is rendered inside ancestor peek");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-peek]") === null', "single Escape closes only peek");
  assert.ok(await read('document.querySelector("[data-testid=activity-sidebar]") !== null'));
  assert.equal(await read('document.querySelectorAll("[data-testid=cascade-spine]").length'), 5);
  driver.milestone("peek-tasks-single-escape");
  await driver.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
  assert.equal(await read('matchMedia("(prefers-reduced-motion: reduce)").matches'), true);
  const beforePeek = frames.length;
  await openPeek(fixture.rootRef, "Agents");
  const peekRow = await reveal(fixture.branch, '[data-testid="activity-peek"]');
  const paging = sent(beforePeek).filter(f => f.method === 'evener/thread/delegates/list' && f.params.ref === fixture.rootRef && f.params.cursor);
  assert.ok(paging.length > 0 && paging.every(answered), "open ancestor peek advances the real direct-page cursor");
  await read(`document.querySelector(${q(peekRow)}).focus()`);
  await wait(`document.activeElement === document.querySelector(${q(peekRow)})`, "real peek row owns keyboard focus");
  await key("Enter", 13);
  await wait(`document.querySelector(${q(column(fixture.branch.childRef))}) !== null && document.querySelectorAll(${q(`${inspectorRoot} [data-scope-ref]`)}).length === 2`, "keyboard ancestor branch truncation");
  assert.deepEqual(await read(`[...document.querySelectorAll(${q(`${inspectorRoot} [data-scope-ref]`)})].map(n => n.dataset.scopeRef)`), [fixture.rootRef, fixture.branch.childRef], "the sibling replaces the old six-edge suffix");
  assert.equal(await read('document.querySelectorAll("[data-testid=activity-peek]").length'), 0);
  assert.equal(await read('document.getAnimations().filter(a => a.effect?.target?.matches("[data-scope-ref]") && a.playState === "running").length'), 0);
  await driver.click('nav[aria-label="Agent path"] button:first-child');
  await wait(`document.querySelectorAll('[data-testid="cascade-column"]').length === 1 && document.querySelector(${q(column(fixture.rootRef))}) !== null`, "pop to root before restoring real branch");
  await drill(fixture.edges[0], 1);
  for (let level = 2; level <= 6; level++) await drill(fixture.edges[level - 1], level);
  assert.equal(await read('document.getAnimations().filter(a => a.effect?.target?.matches("[data-scope-ref]") && a.playState === "running").length'), 0);
  driver.milestone("keyboard-branch-reduced-motion");

  await openPeek(fixture.rootRef, "Agents");
  const rootPeekRow = await reveal(fixture.branch, '[data-testid="activity-peek"]');
  await read(`(() => {
    document.querySelector(${q(rootPeekRow)}).focus();
    window.__cascadeFocused = document.activeElement;
    window.__cascadeGeometryChanges = [];
    window.__cascadeGeometryObserver = new MutationObserver(records => {
      for (const record of records) if (record.target.matches('[data-scope-ref]')) window.__cascadeGeometryChanges.push({ ref: record.target.dataset.scopeRef, style: record.target.getAttribute('style') });
    });
    window.__cascadeGeometryObserver.observe(document.querySelector('[data-pane-scaffold="cascade"]'), { subtree: true, attributes: true, attributeFilter: ['style'] });
  })()`);
  await driver.send("Emulation.setEmulatedMedia", { features: [] });
  const openAfter = await reconnect();
  await waitFrames(() => sent(openAfter).some(f => f.method === "evener/thread/delegates/list" && f.params.ref === fixture.rootRef && f.params.cursor && answered(f)), "open peek recovers its full observed page extent");
  assert.ok(await read(`document.querySelector(${q(rootPeekRow)}) !== null`), "retained older-page row remains useful after reconnect");
  assert.equal(await read("document.activeElement === window.__cascadeFocused"), true, "reconnect and republished context keep keyboard focus");
  assert.deepEqual(await read("window.__cascadeGeometryChanges"), [], "reconnect and runtime/context refresh never animate cascade geometry");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-peek]") === null', "root peek closes without changing center demand");
  const closedRef = fixture.refs[4];
  const closedPeekAfter = frames.length;
  await openPeek(closedRef, "Agents");
  await waitFrames(() => sent(closedPeekAfter).some(f => f.method === "evener/thread/delegates/list" && f.params.ref === closedRef && answered(f)), "the independent ancestor peek owns a real delegate read");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-peek]") === null', "independent ancestor peek relinquishes direct collection demand");
  const closedAfter = await reconnect();
  await waitFrames(() => sent(closedAfter).some(f => f.method === "evener/thread/delegates/list" && f.params.ref === fixture.refs[6] && answered(f)), "selected-leaf sidebar resumes its actual direct collection");
  assert.equal(sent(closedAfter).filter(f => f.method === 'evener/thread/delegates/list' && f.params.ref === closedRef).length, 0, "closed independent peek causes no delegate-page read on recovery");
  assert.deepEqual(await read("window.__cascadeGeometryChanges"), []);
  await read("window.__cascadeGeometryObserver.disconnect()");
  driver.milestone("reconnect-extent-closed-peek", { extent: fixture.rootDelegateCount, closedPeekRef: closedRef, rootDelegateReads: sent(closedAfter).filter(f => f.method === 'evener/thread/delegates/list' && f.params.ref === fixture.rootRef).length, subscriptions: sent(openAfter).filter(f => f.method === 'thread/read' && f.params.subscribe).length });

  const removedInspectorId = await returnToSource();
  assert.equal((await driver.composerState(fixture.rootRef)).text, sourceDraft.text);
  assert.deepEqual((await driver.composerState(fixture.rootRef)).chips, sourceDraft.chips);
  assert.equal(await read(`document.querySelector(${q(stagedPNG)}).src`), sourcePNG, "exact processed PNG bytes survive inspection");
  assert.equal(await read(`document.querySelector(${q(`${sourceFooter} [data-pane-id]`)})?.dataset.paneId`), fixture.sourcePaneId);
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount);
  assert.deepEqual(sent(inspectionAfter).filter(frame => frame.method?.startsWith("turn/") || frame.method === "thread/clear"), [], "inspection sends no input or control RPC");
  driver.milestone("return-source", { sourcePaneId: fixture.sourcePaneId, removedInspectorId });
  await capture("return-source");
  await pendingImage("pending-success.png", true);
  await pendingImage("pending-failure.png", false);
  await mixedSourceJourney();
  await sourceMutationJourney();
  await sharedWidthJourney();
  await nativePositioningInterruption("Shift-Space");
  await nativePositioningInterruption("wheel");
  await nativePositioningInterruption("pill");
  await reloadAndMobileJourney();
  assert.deepEqual(errors, [], "unexpected browser errors or warnings");
} catch (error) {
  failed = true;
  console.error(error.stack ?? String(error));
  if (driver.page) await capture("failure").catch((failure) => console.error(failure));
} finally {
  try {
    if (measurementScriptId) {
      try {
        await read("(() => { const state = window.__cascadeMeasurements; if (!state) return; state.release(); window.ResizeObserver = state.Native; delete window.__cascadeMeasurements; })()");
      } finally {
        await driver.send("Page.removeScriptToEvaluateOnNewDocument", { identifier:measurementScriptId });
      }
    }
  } finally {
    try {
      writeFileSync(path.join(fixture.artifactDir, "console.json"), JSON.stringify(consoleEvents, null, 2));
      writeFileSync(path.join(fixture.artifactDir, "rpc-frames.json"), JSON.stringify(frames, null, 2));
      writeFileSync(path.join(fixture.artifactDir, "reading-points.json"), JSON.stringify(readingPoints, null, 2));
      if (driver.page) writeFileSync(path.join(fixture.artifactDir, "reading-attempt.json"), JSON.stringify(await read('window.__cascadeReadingAttempt ?? null'), null, 2));
      if (driver.page) writeFileSync(path.join(fixture.artifactDir, "reader-wheels.json"), JSON.stringify(await read('window.__cascadeReaderWheels ?? []'), null, 2));
      if (driver.page) writeFileSync(path.join(fixture.artifactDir, "reveal-events.json"), JSON.stringify(await read('window.__cascadeRevealEvents ?? []'), null, 2));
      if (driver.page) writeFileSync(path.join(fixture.artifactDir, "source-scroll-trace.json"), JSON.stringify(await read('window.__cascadeSourceTrace ?? []'), null, 2));
      if (driver.page) writeFileSync(path.join(fixture.artifactDir, "source-startup-trace.json"), JSON.stringify(await read('window.__cascadeStartupTrace ?? []'), null, 2));
    } finally {
      await driver.stop();
    }
  }
}
if (failed) process.exitCode = 1;
