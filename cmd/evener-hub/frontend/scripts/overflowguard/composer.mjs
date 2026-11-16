import assert from "node:assert/strict";
import { setTimeout as delay } from "node:timers/promises";
import {
  applyViewport,
  assertGuardOrigin,
  clearViewportOverride,
  connectBrowser,
  connectOnlyPage,
  connectPage,
  evaluate,
  navigateTo,
  realizedViewport,
  waitForFonts,
} from "../browserGuardCdp.mjs";
import { installTimeoutClock } from "./timeoutClock.mjs";

const BOOT = { bootExpression: "typeof window.settled !== 'undefined'", bootLabel: "overflow harness" };
const editor = '[role="textbox"][contenteditable="true"]';
const control = (id) => `[data-testid="composer-${id}"]`;

// Condition waits are native-walltime tripwires, not timing sleeps. Gesture
// pages flush due work without advancing their controlled timeout clock.
async function until(send, expression) {
  return evaluate(
    send,
    `new Promise((resolve, reject) => {
    const deadline = performance.now() + 3000;
    const check = async () => {
      try {
        if (window.composerClock) await window.composerClock.advanceTo(window.composerClock.now);
        const value = (${expression});
        if (value) return resolve(true);
        if (performance.now() > deadline) throw new Error('composer condition timed out: ' + ${JSON.stringify(expression)});
        requestAnimationFrame(check);
      } catch (error) { reject(error); }
    };
    check();
  })`,
  );
}

/** Wait for the resting composer's fold/expand height transition to finish.
 * A measurement taken mid-transition describes the short intermediate box,
 * not the shape the user sees. */
export async function waitForComposerSettled(send) {
  await until(send, `document.querySelector('[data-testid="composer-input-card"][data-animating]') === null`);
}
async function key(send, key, code, windowsVirtualKeyCode, modifiers = 0) {
  for (const type of ["keyDown", "keyUp"]) {
    await send("Input.dispatchKeyEvent", {
      type,
      key,
      code,
      windowsVirtualKeyCode,
      modifiers,
      ...(type === "keyDown" && key === "Enter" && modifiers === 0 ? { text: "\r" } : {}),
    });
  }
}
async function point(send, selector) {
  return evaluate(
    send,
    `(() => {
    const node = document.querySelector(${JSON.stringify(selector)});
    if (!node) throw new Error('missing coordinate target');
    const r = node.getBoundingClientRect();
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    if (!node.contains(document.elementFromPoint(x, y))) throw new Error('coordinate target occluded');
    return { x, y };
  })()`,
  );
}
async function mouse(send, p, type, clickCount = 1, buttons) {
  await send("Input.dispatchMouseEvent", {
    type,
    ...p,
    button: "left",
    buttons: buttons ?? (type === "mousePressed" ? 1 : 0),
    clickCount,
  });
}
async function tap(send, p, touch) {
  if (touch) {
    await send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ ...p, id: 1 }] });
    await send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  } else {
    await mouse(send, p, "mousePressed");
    await mouse(send, p, "mouseReleased");
  }
}
export async function typeComposerDraft(send, text = "browser-proof") {
  // Focus programmatically: a trusted tap on the editor starts the phone
  // footer's editing move, and that pointerup would land where the editor
  // used to be. The gesture under test is the Send press; the draft setup
  // only needs the editor focused and the resulting layout settled.
  await evaluate(send, `document.querySelector(${JSON.stringify(editor)}).focus()`);
  await evaluate(send, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
  await send("Input.insertText", { text });
  await until(send, `document.querySelector('${control("submit")}')`);
  // The editor's focus starts the phone footer's editing state, which moves
  // the dock. Let that commit before a caller measures a control's point,
  // or the tap can land where the control used to be.
  await evaluate(send, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
  // The resting composer folds and expands with a height transition; measure
  // the settled shape, never a frame in the middle of it.
  await waitForComposerSettled(send);
}
async function clearDraft(send) {
  await tap(send, await point(send, editor), false);
  await key(send, "a", "KeyA", 65, process.platform === "darwin" ? 4 : 2);
  await key(send, "Backspace", "Backspace", 8);
  await until(send, `document.querySelector('${control("stop")}')`);
  await waitForComposerSettled(send);
}
async function tabTo(send, selector) {
  for (let i = 0; i < 30; i++) {
    await key(send, "Tab", "Tab", 9);
    if (await evaluate(send, `document.activeElement?.matches(${JSON.stringify(selector)})`)) return;
  }
  throw new Error(`keyboard Tab did not reach ${selector}`);
}

// Expected placement is explicit input from the runner, never a production
// constant or a verdict returned by the fixture.
export function assertComposerPlacement(measurement, { contentWidth, primary, inlineSteer, menuSteer }) {
  const work = measurement.currentWork;
  assert.ok(Number.isFinite(contentWidth) && contentWidth > 0, "composer content-box width is measured");
  assert.equal(work.verbCluster.containerWidth, contentWidth, "actual composer content-box width");
  const present = work.controls
    .filter((c) => c.present)
    .map((c) => c.testId)
    .sort();
  const expected = ["composer-attach", `composer-${primary}`, ...(inlineSteer ? ["composer-steer"] : [])].sort();
  assert.deepEqual(present, expected, "independent primary slot and inline Steer");
  assert.equal(work.verbCluster.controls, inlineSteer ? 2 : 1);
  assert.ok(work.verbCluster.top < work.verbCluster.statusRowBottom - 1, "verb row must not wrap");
  assert.ok(
    work.controlsFound && work.controlsContained && work.controlsDoNotOverlap && work.sharedPaneWithoutOverflow,
    "controls contained without overlap or overflow",
  );
  assert.equal(measurement.scrollers.length, 0, "no horizontal scroll containers");
  assert.equal(measurement.menuVerbs.open, true);
  assert.equal(measurement.menuVerbs.stop, true, "menu Stop at every width and draft state");
  assert.equal(measurement.menuVerbs.steer, menuSteer, "independent menu Steer placement");
}

export async function measureComposerPlacement(endpoint, url, { viewportWidth, contentWidth, steer }) {
  const page = await connectOnlyPage(endpoint);
  const { send } = page;
  try {
    await applyViewport(send, {
      width: viewportWidth,
      height: 900,
      mobile: viewportWidth < 900,
      touch: viewportWidth < 900,
    });
    await navigateTo(page, url, BOOT);
    await assertGuardOrigin(send, new URL(url).host);
    await evaluate(send, "window.settled");
    await waitForFonts(send);
    const viewport = await realizedViewport(send);
    assert.equal(viewport.windowInnerWidth, viewportWidth);
    assert.equal(viewport.documentClientWidth, viewportWidth);
    assert.equal(viewport.visualViewportWidth, viewportWidth);
    // Drafts intentionally survive navigation. Clear through the editor rather
    // than assuming a freshly loaded page has an empty local draft.
    await clearDraft(send);
    // Calibrate only the fixture pane. Measure the actual content box again
    // after ResizeObserver commits, rather than calling 399 a viewport width.
    if (contentWidth !== undefined) {
      await evaluate(send, `document.getElementById('oh-pane').style.width = '${contentWidth}px'`);
      await evaluate(
        send,
        `(() => {
      const pane = document.getElementById('oh-pane');
      const measured = window.measureComposerContentWidth();
      pane.style.width = (pane.getBoundingClientRect().width + ${contentWidth} - measured) + 'px';
    })()`,
      );
      await until(send, `window.measureComposerContentWidth() === ${contentWidth}`);
      await evaluate(send, "new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
      assert.ok(
        (await evaluate(send, "document.getElementById('oh-pane').getBoundingClientRect().width")) < viewportWidth,
        "boundary fixture is a narrow pane within the requested viewport",
      );
    }
    contentWidth ??= await evaluate(send, "window.measureComposerContentWidth()");
    const inlineSteer = steer && contentWidth > 399;
    const menuSteer = steer && contentWidth <= 399;
    const results = [];
    for (const primary of ["stop", "submit", "stop"]) {
      if (primary === "submit") await typeComposerDraft(send);
      else if (results.length) await clearDraft(send);
      assert.ok(
        await evaluate(send, `document.activeElement === document.querySelector('${editor}')`),
        "typing and clearing preserve editor focus through slot replacement",
      );
      const measurement = await evaluate(send, "window.measure()");
      measurement.menuVerbs = await evaluate(send, "window.measureMenuVerbs()");
      assertComposerPlacement(measurement, { contentWidth, primary, inlineSteer, menuSteer });
      await key(send, "Escape", "Escape", 27);
      results.push({
        primary,
        contentWidth: measurement.currentWork.verbCluster.containerWidth,
        inline: measurement.currentWork.controls.filter((c) => c.present).map((c) => c.testId),
        menu: measurement.menuVerbs,
        horizontalScrollers: measurement.scrollers.length,
      });
    }
    return {
      viewportWidth,
      paneWidth: await evaluate(send, "document.getElementById('oh-pane').getBoundingClientRect().width"),
      states: results,
    };
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}

// Component-browser proof: product callbacks run on a bounded JS timeout clock,
// while trusted input, RAF and native timestamps remain real. Each case owns its
// target, including failures during navigation or clock installation.
export async function verifyComposerGesture(endpoint, url, width, gesture, { driverDelayMs = 0 } = {}) {
  let browser, context, page;
  try {
    browser = await connectBrowser(endpoint);
    context = (await browser.send("Target.createBrowserContext")).result.browserContextId;
    const target = (await browser.send("Target.createTarget", { url: "about:blank", browserContextId: context })).result
      .targetId;
    page = await connectPage(endpoint, target);
    const { send } = page;
    const driverDelays = [];
    const delayedSend = async (method, params) => {
      const requestedWallMs = performance.now();
      if (driverDelayMs) await delay(driverDelayMs);
      const sentWallMs = performance.now();
      const reply = await send(method, params);
      const acknowledgedWallMs = performance.now();
      if (driverDelayMs) await delay(driverDelayMs);
      if (driverDelayMs)
        driverDelays.push({
          method,
          requestedWallMs,
          sentWallMs,
          acknowledgedWallMs,
          receivedWallMs: performance.now(),
        });
      return reply;
    };
    await applyViewport(send, { width, height: 900, mobile: width < 900, touch: width < 900 });
    await navigateTo(page, url, BOOT);
    await assertGuardOrigin(send, new URL(url).host);
    await evaluate(send, "window.settled");
    await waitForFonts(send);
    await typeComposerDraft(send);
    const p = await point(send, control("submit"));
    await evaluate(
      send,
      `(() => {
      window.composerClock = (${installTimeoutClock.toString()})();
      window.proof = { send: document.querySelector('${control("submit")}'), stopLogicalMs: null, stopWallMs: null, ready: null, events: [], handlers: [] };
      proof.observer = new MutationObserver(() => {
        const stop = document.querySelector('${control("stop")}');
        if (!stop) return;
        if (proof.stopLogicalMs === null) {
          proof.stopLogicalMs = composerClock.now;
          proof.stopWallMs = performance.now();
        }
        if (!stop.disabled && proof.ready === null) proof.ready = { logicalMs: composerClock.now, wallMs: performance.now(), inputs: proof.events.length };
      });
      proof.observer.observe(document.getElementById('oh-pane'), { childList: true, subtree: true, attributes: true, attributeFilter: ['disabled'] });
      for (const type of ['pointerdown', 'pointerup', 'keydown', 'keyup', 'click']) {
        const handler = event => proof.events.push({ type, trusted: event.isTrusted, logicalMs: composerClock.now, wallMs: performance.now(), timestamp: event.timeStamp, pointerType: event.pointerType, target: event.target.closest?.('[data-testid]')?.dataset.testid, x: event.clientX, y: event.clientY, disabled: document.querySelector('${control("stop")}')?.disabled ?? null });
        proof.handlers.push([type, handler]);
        document.addEventListener(type, handler, true);
      }
    })()`,
    );
    if (gesture === "double-tap") {
      await tap(send, p, width < 900);
    } else {
      // Submit through the real editor shortcut while the mouse is held on
      // Send, then release over the distinct Stop AFTER it becomes ready.
      await mouse(send, p, "mousePressed");
      await tabTo(send, editor);
      await key(send, "Enter", "Enter", 13, 2);
    }
    await until(
      send,
      `window.composerRequests.some(r => r.method === 'turn/queue') && window.proof.stopLogicalMs !== null`,
    );
    const transition = await evaluate(
      send,
      `({
      disabled: document.querySelector('${control("stop")}').disabled,
      distinct: window.proof.send !== document.querySelector('${control("stop")}'),
      focus: document.activeElement === document.querySelector('${editor}'),
      hit: document.querySelector('${control("stop")}').contains(document.elementFromPoint(${p.x}, ${p.y})),
      logicalMs: composerClock.now, inputs: proof.events.length
    })`,
    );
    assert.equal(transition.logicalMs, 0);
    assert.ok(transition.disabled, "Stop immediately visible but disabled at logical 0");
    assert.ok(transition.distinct, "Send and Stop must have distinct DOM identities");
    assert.ok(transition.focus, `replacement preserves editor focus: ${JSON.stringify(transition)}`);
    assert.ok(transition.hit, "captured Send coordinate must hit replacement Stop");
    // Independent DOM boundary oracle, not a hook-name/stack or scheduled-delay
    // selector. Advancing invokes ALL due callbacks, including unrelated timers.
    await evaluate(
      send,
      `(async () => {
      await composerClock.advanceTo(499);
      await new Promise(resolve => requestAnimationFrame(resolve));
    })()`,
    );
    const before = await evaluate(
      send,
      `({ disabled: document.querySelector('${control("stop")}').disabled, inputs: proof.events.length })`,
    );
    assert.equal(before.disabled, true, "Stop stays disabled through logical 499");
    assert.equal(before.inputs, transition.inputs, "no input needed to reach the boundary");
    if (gesture === "double-tap") await tap(delayedSend, p, width < 900);
    const inputsAtExpiry = await evaluate(send, "proof.events.length");
    assert.equal(
      await evaluate(send, "composerRequests.filter(r => r.method === 'turn/interrupt').length"),
      0,
      "zero interrupts before expiry",
    );
    await evaluate(send, "composerClock.advanceTo(500)");
    await until(send, `!document.querySelector('${control("stop")}').disabled && proof.ready !== null`);
    const ready = await evaluate(send, "({ ...proof.ready, inputsNow: proof.events.length })");
    assert.equal(ready.logicalMs, 500, "Stop enables at logical 500");
    assert.equal(ready.inputs, inputsAtExpiry, "expiry needs no additional input");
    assert.equal(ready.inputsNow, inputsAtExpiry);
    if (gesture === "split-press") await mouse(delayedSend, p, "mouseReleased");
    const protectedState = await evaluate(
      send,
      `({ requests: window.composerRequests, events: proof.events, stopLogicalMs: proof.stopLogicalMs, stopWallMs: proof.stopWallMs, focus: document.activeElement === document.querySelector('${editor}') })`,
    );
    assert.equal(
      protectedState.requests.filter((r) => r.method === "turn/queue").length,
      1,
      "exactly one real submission",
    );
    assert.deepEqual(
      protectedState.requests.find((r) => r.method === "turn/queue").params.input,
      [{ type: "text", text: "browser-proof" }],
      "real editor draft reaches AppWire once",
    );
    assert.equal(
      protectedState.requests.filter((r) => r.method === "turn/interrupt").length,
      0,
      `accidental ${gesture} must not interrupt: ${JSON.stringify(protectedState)}`,
    );
    const pointerUps = protectedState.events.filter((e) => e.type === "pointerup");
    assert.equal(pointerUps.length, gesture === "double-tap" ? 2 : 1);
    assert.ok(
      protectedState.events.every((e) => e.trusted),
      "browser gestures must be trusted",
    );
    const pointerDowns = protectedState.events.filter((e) => e.type === "pointerdown");
    assert.equal(pointerDowns[0]?.target, "composer-submit", "press starts on real Send");
    const lastUp = pointerUps.at(-1);
    if (gesture === "double-tap") {
      assert.equal(pointerDowns.length, 2);
      assert.equal(pointerDowns[1].target, "composer-stop", "second tap reaches replacement Stop");
      assert.equal(pointerDowns[1].pointerType, width < 900 ? "touch" : "mouse");
      assert.equal(pointerDowns[1].disabled, true);
      assert.equal(lastUp.target, "composer-stop");
      assert.equal(lastUp.logicalMs, 499);
    }
    const releaseLogicalMs = lastUp.logicalMs - protectedState.stopLogicalMs;
    assert.ok(
      releaseLogicalMs >= 0 && (gesture === "double-tap" ? releaseLogicalMs < 500 : releaseLogicalMs >= 500),
      `logical release after replacement: ${releaseLogicalMs}ms`,
    );
    assert.ok(Math.abs(lastUp.x - p.x) < 1 && Math.abs(lastUp.y - p.y) < 1, "release uses captured Send coordinates");
    assert.equal(
      await evaluate(send, "composerRequests.filter(r => r.method === 'turn/interrupt').length"),
      0,
      "zero interrupts through automatic delay expiry",
    );
    // Tab navigation, not programmatic focus, must reach the replacement.
    await tabTo(send, control("stop"));
    assert.ok(await evaluate(send, `document.activeElement.matches(':focus-visible')`), "keyboard focus is visible");
    const deliberateWallMs = await evaluate(send, "performance.now()");
    await key(send, "Enter", "Enter", 13);
    await until(send, "window.composerRequests.some(r => r.method === 'turn/interrupt')");
    const requests = await evaluate(send, "window.composerRequests");
    assert.equal(requests.filter((r) => r.method === "turn/queue").length, 1);
    assert.equal(
      requests.filter((r) => r.method === "turn/interrupt").length,
      1,
      "deliberate keyboard Stop interrupts once",
    );
    assert.ok(
      requests.find((r) => r.method === "turn/interrupt").at >= deliberateWallMs,
      "interrupt belongs to deliberate Stop",
    );
    return {
      gesture,
      width,
      coordinate: p,
      driverDelayMs,
      driverDelays,
      releaseAfterSwapLogicalMs: releaseLogicalMs,
      releaseAfterSwapWallMs: lastUp.wallMs - protectedState.stopWallMs,
      readyAfterSwapLogicalMs: ready.logicalMs - protectedState.stopLogicalMs,
      readyAfterSwapWallMs: ready.wallMs - protectedState.stopWallMs,
      observedTimeoutDelaysMs: await evaluate(
        send,
        "[...new Set(composerClock.snapshot().history.filter(e => e.type === 'scheduled').map(e => e.requested))]",
      ),
      submissions: 1,
      accidentalInterrupts: 0,
      deliberateInterrupts: 1,
      distinct: transition.distinct,
      swapEditorFocus: transition.focus,
      afterGestureEditorFocus: protectedState.focus,
      trustedEvents: protectedState.events.length,
    };
  } finally {
    try {
      if (page) {
        try {
          const restored = await evaluate(
            page.send,
            `(() => {
            window.proof?.observer?.disconnect();
            for (const [type, handler] of window.proof?.handlers ?? []) document.removeEventListener(type, handler, true);
            return window.composerClock?.dispose().restored;
          })()`,
          );
          if (restored) assert.deepEqual(restored, { setTimeout: true, clearTimeout: true });
        } finally {
          page.close();
        }
      }
    } finally {
      try {
        if (context) await browser.send("Target.disposeBrowserContext", { browserContextId: context });
      } finally {
        browser?.close();
      }
    }
  }
}

/**
 * An outside press must not fold the composer before its click lands.
 *
 * The card is in flow, so a fold at pointerdown shrinks it before mouseup and
 * the control under the pointer moves away before it receives its click: the
 * user clicks a transcript button or disclosure and nothing happens. A trusted
 * press-release over a real disclosure is the only oracle that can see this -
 * the loss depends on browser hit-testing at release, which no synthetic event
 * reproduces. The drag case pins the other half of the rule: a press that ends
 * without a click leaves the card alone.
 */
export async function verifyComposerOutsidePressClick(endpoint, url, width) {
  let browser, context, page;
  try {
    browser = await connectBrowser(endpoint);
    context = (await browser.send("Target.createBrowserContext")).result.browserContextId;
    const target = (await browser.send("Target.createTarget", { url: "about:blank", browserContextId: context })).result
      .targetId;
    page = await connectPage(endpoint, target);
    const { send } = page;
    await applyViewport(send, { width, height: 900, mobile: width < 900, touch: width < 900 });
    await navigateTo(page, url, BOOT);
    await assertGuardOrigin(send, new URL(url).host);
    await evaluate(send, "window.settled");
    await waitForFonts(send);

    // Open the editor and settle on the expanded card. A trusted tap on the
    // editor would itself move the footer, so focus it programmatically.
    await evaluate(send, `document.querySelector('${editor}').focus()`);
    await until(send, `!document.querySelector('[data-testid="composer-input-card"]').hasAttribute('data-folded')`);
    await waitForComposerSettled(send);

    // Aim at a transcript disclosure that sits below the card, so the fold's
    // height change is what moves it.
    const aim = await evaluate(
      send,
      `(() => {
      const summaries = [...document.querySelectorAll('[data-testid="turn-block"] details > summary')];
      for (const node of summaries) {
        const r = node.getBoundingClientRect();
        const x = r.left + r.width / 2, y = r.top + r.height / 2;
        if (r.height <= 0 || r.width <= 0) continue;
        if (!node.contains(document.elementFromPoint(x, y))) continue;
        const details = node.closest('details');
        return { x, y, open: details.open, label: (node.textContent || '').trim().slice(0, 40) };
      }
      return null;
    })()`,
    );
    assert.ok(aim, "no reachable transcript disclosure in the harness");

    // Press, hold long enough that a mid-press fold would have landed, then
    // release. Editing must survive the hold: the fold waits for the click.
    await mouse(send, { x: aim.x, y: aim.y }, "mousePressed");
    await delay(400);
    const held = await evaluate(
      send,
      `({
      editing: document.querySelector('[data-composer]').hasAttribute('data-composer-editing'),
      folded: document.querySelector('[data-testid="composer-input-card"]').getAttribute('data-folded'),
      hit: !!document.elementFromPoint(${aim.x}, ${aim.y})?.closest('summary'),
    })`,
    );
    assert.ok(held.editing, "editing must survive the outside press until its click lands");
    assert.equal(held.folded, null, "the card must not fold mid-press");
    assert.ok(held.hit, "the pressed disclosure must still sit under the pointer before release");

    await mouse(send, { x: aim.x, y: aim.y }, "mouseReleased");
    const toggledExpr = `[...document.querySelectorAll('[data-testid="turn-block"] details')].some(d => d.open !== ${aim.open})`;
    await until(send, toggledExpr);
    const settled = await evaluate(
      send,
      `({
      toggled: ${toggledExpr},
      folded: document.querySelector('[data-testid="composer-input-card"]').getAttribute('data-folded'),
      editing: document.querySelector('[data-composer]').hasAttribute('data-composer-editing'),
    })`,
    );
    assert.ok(settled.toggled, "the transcript disclosure must receive its click");
    assert.equal(settled.folded, "true", "the card folds after the click lands on an empty composer");
    assert.equal(settled.editing, false, "the click ends editing");

    // A press-drag produces no click and must leave the card as it was.
    await evaluate(send, `document.querySelector('${editor}').focus()`);
    await until(send, `!document.querySelector('[data-testid="composer-input-card"]').hasAttribute('data-folded')`);
    await waitForComposerSettled(send);
    const dragStart = await evaluate(
      send,
      `(() => {
      const node = document.querySelector('[data-testid="turn-block"] p');
      const r = node.getBoundingClientRect();
      return { x: r.left + 4, y: r.top + Math.min(8, r.height / 2) };
    })()`,
    );
    await mouse(send, dragStart, "mousePressed");
    await mouse(send, { x: dragStart.x + 120, y: dragStart.y + 40 }, "mouseMoved", 1, 1);
    // The collapse this case must catch lands within one frame and a React
    // commit; there is no height transition to outlast.
    await evaluate(send, "new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const midDrag = await evaluate(
      send,
      `document.querySelector('[data-testid="composer-input-card"]').getAttribute('data-folded')`,
    );
    assert.equal(midDrag, null, "a text-selection drag must not fold the card mid-gesture");
    await mouse(send, { x: dragStart.x + 120, y: dragStart.y + 40 }, "mouseReleased");
    return {
      disclosure: aim.label,
      heldEditing: held.editing,
      toggled: settled.toggled,
      foldedAfterClick: settled.folded,
      dragHeldCard: midDrag === null,
    };
  } finally {
    try {
      page?.close();
    } finally {
      try {
        if (context) await browser.send("Target.disposeBrowserContext", { browserContextId: context });
      } finally {
        browser?.close();
      }
    }
  }
}
