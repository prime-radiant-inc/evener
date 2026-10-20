import assert from "node:assert/strict";
import {
  applyViewport,
  assertGuardOrigin,
  clearViewportOverride,
  connectOnlyPage,
  evaluate,
  navigateTo,
  realizedViewport,
  waitForFonts,
} from "../browserGuardCdp.mjs";

const BOOT = { bootExpression: "typeof window.settled !== 'undefined'", bootLabel: "overflow harness" };
const editor = '[role="textbox"][contenteditable="true"]';
const control = (id) => `[data-testid="composer-${id}"]`;

// Condition waits are bounded tripwires, not timing sleeps. The real timer and
// durable dispatch run in the browser, with only AppWire replies faked.
async function until(send, expression) {
  return evaluate(
    send,
    `new Promise((resolve, reject) => {
    const deadline = performance.now() + 3000;
    const check = () => {
      const value = (${expression});
      if (value) return resolve(true);
      if (performance.now() > deadline) return reject(new Error('composer condition timed out: ' + ${JSON.stringify(expression)}));
      requestAnimationFrame(check);
    };
    check();
  })`,
  );
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
async function mouse(send, p, type, clickCount = 1) {
  await send("Input.dispatchMouseEvent", {
    type,
    ...p,
    button: "left",
    buttons: type === "mousePressed" ? 1 : 0,
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
  await tap(send, await point(send, editor), false);
  await send("Input.insertText", { text });
  await until(send, `document.querySelector('${control("submit")}')`);
}
async function clearDraft(send) {
  await tap(send, await point(send, editor), false);
  await key(send, "a", "KeyA", 65, 2);
  await key(send, "Backspace", "Backspace", 8);
  await until(send, `document.querySelector('${control("stop")}')`);
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
      const measured = window.measure().currentWork.verbCluster.containerWidth;
      pane.style.width = (pane.getBoundingClientRect().width + ${contentWidth} - measured) + 'px';
    })()`,
      );
      await until(send, `window.measure().currentWork.verbCluster.containerWidth === ${contentWidth}`);
      await evaluate(send, "new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
      assert.ok(
        (await evaluate(send, "document.getElementById('oh-pane').getBoundingClientRect().width")) < viewportWidth,
        "boundary fixture is a narrow pane within the requested viewport",
      );
    }
    contentWidth ??= await evaluate(send, "window.measure().currentWork.verbCluster.containerWidth");
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

export async function verifyComposerGesture(endpoint, url, width, gesture) {
  const page = await connectOnlyPage(endpoint);
  const { send } = page;
  try {
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
      window.proof = { send: document.querySelector('${control("submit")}'), stopAt: null, events: [] };
      new MutationObserver(() => {
        if (window.proof.stopAt === null && document.querySelector('${control("stop")}')) window.proof.stopAt = performance.now();
      }).observe(document.getElementById('oh-pane'), { childList: true, subtree: true });
      for (const type of ['pointerdown', 'pointerup', 'keydown', 'click']) document.addEventListener(type, event => {
        window.proof.events.push({ type, trusted: event.isTrusted, at: performance.now(), target: event.target.closest?.('[data-testid]')?.dataset.testid, x: event.clientX, y: event.clientY });
      }, true);
    })()`,
    );
    if (gesture === "double-tap") {
      await tap(send, p, width < 900);
    } else {
      // Start on Send, submit through the real editor shortcut while held,
      // then release over the replacement AFTER its delay. Identity, not the
      // disabled interval, must prevent this split press from stopping.
      await mouse(send, p, "mousePressed");
      await tabTo(send, editor);
      await key(send, "Enter", "Enter", 13, 2);
    }
    await until(
      send,
      `window.composerRequests.some(r => r.method === 'turn/queue') && document.querySelector('${control("stop")}')`,
    );
    const transition = await evaluate(
      send,
      `({
      disabled: document.querySelector('${control("stop")}').disabled,
      distinct: window.proof.send !== document.querySelector('${control("stop")}'),
      focus: document.activeElement === document.querySelector('${editor}'),
      hit: document.querySelector('${control("stop")}').contains(document.elementFromPoint(${p.x}, ${p.y})),
      elapsed: performance.now() - window.proof.stopAt
    })`,
    );
    assert.ok(transition.hit, "captured Send coordinate must hit replacement Stop");
    if (gesture === "double-tap") await tap(send, p, width < 900);
    else {
      await until(send, `!document.querySelector('${control("stop")}').disabled`);
      await mouse(send, p, "mouseReleased");
    }
    const protectedState = await evaluate(
      send,
      `({ requests: window.composerRequests, events: window.proof.events, stopAt: window.proof.stopAt, focus: document.activeElement === document.querySelector('${editor}') })`,
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
    assert.ok(transition.distinct, "Send and Stop must have distinct DOM identities");
    assert.ok(transition.focus, `replacement preserves editor focus: ${JSON.stringify(transition)}`);
    assert.ok(transition.disabled, "Stop immediately visible but disabled");
    const pointerUps = protectedState.events.filter((e) => e.type === "pointerup");
    assert.equal(pointerUps.length, gesture === "double-tap" ? 2 : 1);
    assert.ok(
      protectedState.events.every((e) => e.trusted),
      "browser gestures must be trusted",
    );
    const pointerDowns = protectedState.events.filter((e) => e.type === "pointerdown");
    assert.equal(pointerDowns[0]?.target, "composer-submit", "press starts on real Send");
    if (gesture === "double-tap") {
      assert.equal(pointerDowns.length, 2);
      assert.equal(pointerDowns[1].target, "composer-stop", "second tap reaches replacement Stop");
    }
    const lastUp = pointerUps.at(-1);
    const elapsed = lastUp.at - protectedState.stopAt;
    assert.ok(
      elapsed >= 0 && (gesture === "double-tap" ? elapsed < 500 : elapsed >= 500),
      `release must straddle replacement at the intended time: ${elapsed}ms`,
    );
    assert.ok(Math.abs(lastUp.x - p.x) < 1 && Math.abs(lastUp.y - p.y) < 1, "release uses captured Send coordinates");
    await until(send, `!document.querySelector('${control("stop")}').disabled`);
    const readyAt = await evaluate(send, "performance.now() - window.proof.stopAt");
    assert.ok(readyAt >= 490, "inline delay must not be bypassed");
    assert.equal(
      await evaluate(send, "window.composerRequests.filter(r => r.method === 'turn/interrupt').length"),
      0,
      "zero interrupts through automatic delay expiry",
    );
    // Tab navigation, not programmatic focus, must reach the replacement.
    await tabTo(send, control("stop"));
    assert.ok(await evaluate(send, `document.activeElement.matches(':focus-visible')`), "keyboard focus is visible");
    const deliberateAt = await evaluate(send, "performance.now()");
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
      requests.find((r) => r.method === "turn/interrupt").at >= deliberateAt,
      "interrupt belongs to deliberate Stop",
    );
    return {
      gesture,
      width,
      coordinate: p,
      releaseAfterSwapMs: elapsed,
      readyAfterSwapMs: readyAt,
      submissions: 1,
      accidentalInterrupts: 0,
      deliberateInterrupts: 1,
      distinct: transition.distinct,
      swapEditorFocus: transition.focus,
      afterGestureEditorFocus: protectedState.focus,
      trustedEvents: protectedState.events.length,
    };
  } finally {
    await clearViewportOverride(send);
    page.close();
  }
}
