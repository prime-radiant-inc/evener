// Production AppShell/Rail geometry and interaction checks. The oracle uses
// measured DOM boxes and literal spacing from the approved sidebar contract,
// never the production placement helper.
import { writeFile } from "node:fs/promises";
import path from "node:path";
import {
  applyViewport,
  clearViewportOverride,
  closePage,
  connectPage,
  evaluate,
  navigateTo,
  openPage,
  waitForFonts,
} from "../browserGuardCdp.mjs";
import { Driver } from "../skillguard/run.mjs";

const TITLE = '[data-session-ref="local:p0-s0"] [data-testid="rail-row-title"]';
const CASES = [
  { width: 1400, height: 900, sidebar: 200, theme: "dark" },
  { width: 1400, height: 900, sidebar: 560, theme: "light" },
  { width: 390, height: 844, mobile: true, touch: true, theme: "dark" },
];

async function settleCard(driver) {
  await driver.waitPage("window.__sessionHoverGuard.card() !== null", { label: "session context card revealed" });
  await evaluate(
    driver.send,
    `Promise.all(document.getAnimations().filter(a =>
    a.effect.getTiming().iterations !== Infinity).map(a => a.finished))`,
  );
}

async function moveToTitle(driver) {
  const point = await driver.elementBox(TITLE);
  if (!point) throw new Error("session title is not visible");
  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: point.x, y: point.y });
}

async function measureCard(driver) {
  return evaluate(
    driver.send,
    `(() => {
    const state = window.__sessionHoverGuard;
    const card = state.card();
    if (!card) throw new Error('session context card is absent');
    return { sidebar: state.box(state.rail), row: state.box(state.row), before: state.before,
      card: state.box(card), bodyPortal: card.parentElement === document.body,
      pointerEvents: getComputedStyle(card).pointerEvents,
      associated: state.row.getAttribute('aria-describedby')?.split(/\\s+/).includes(card.id),
      title: state.title.textContent, content: card.textContent, id: card.id,
      viewport: { width: innerWidth, height: innerHeight },
      focusUnchanged: document.activeElement === state.initialFocus,
      panes: window.overviewGuardState().panes, errors: window.__shellGuardErrors || [] };
  })()`,
  );
}

function assertCard(result, beside) {
  const { card, sidebar, row, viewport } = result;
  if (card.width <= 0 || card.height <= 0) throw new Error("session card has no visible box");
  if (card.left < 7 || card.right > viewport.width - 7 || card.top < 7 || card.bottom > viewport.height - 7) {
    throw new Error(`card escapes viewport clearance: ${JSON.stringify(result)}`);
  }
  if (!result.bodyPortal || result.pointerEvents !== "none" || !result.associated) {
    throw new Error(`card portal, hit testing or row description changed: ${JSON.stringify(result)}`);
  }
  for (const edge of ["left", "right", "top", "bottom"]) {
    if (Math.abs(row[edge] - result.before[edge]) > 1) throw new Error(`card opening moved its row's ${edge}`);
  }
  if (!result.content.includes(result.title) || result.errors.length) {
    throw new Error(`session context lost content or raised page errors: ${JSON.stringify(result)}`);
  }
  if (beside) {
    if (Math.abs(card.left - sidebar.right - 12) > 1) {
      throw new Error(
        `card must float 12px beyond sidebar, measured ${card.left - sidebar.right}px: ${JSON.stringify(result)}`,
      );
    }
    const centeredTop = (row.top + row.bottom - card.height) / 2;
    const expectedTop = Math.max(8, Math.min(centeredTop, viewport.height - card.height - 8));
    if (Math.abs(card.top - expectedTop) > 1)
      throw new Error(`card is not row-centered or edge-shifted: ${JSON.stringify(result)}`);
  } else {
    const centeredLeft = (row.left + row.right - card.width) / 2;
    const expectedLeft = Math.max(8, Math.min(centeredLeft, viewport.width - card.width - 8));
    if (Math.abs(card.left - expectedLeft) > 1)
      throw new Error(`narrow card is not centered on its row: ${JSON.stringify(result)}`);
    const above = row.top - card.height - 12;
    const below = row.bottom + 12;
    const expectedTop =
      above >= 8
        ? above
        : below + card.height <= viewport.height - 8
          ? below
          : Math.max(8, Math.min(above, viewport.height - card.height - 8));
    if (Math.abs(card.top - expectedTop) > 1)
      throw new Error(`narrow card did not use 12px above/below fallback: ${JSON.stringify(result)}`);
  }
}

async function saveCard(driver, label) {
  if (!process.env.EVENER_SCRATCH_DIR) return;
  const screenshot = await driver.send("Page.captureScreenshot", { format: "png" });
  await writeFile(
    path.join(process.env.EVENER_SCRATCH_DIR, `session-hovercard-${label}.png`),
    Buffer.from(screenshot.result.data, "base64"),
  );
}

async function desktopJourney(driver, viewport) {
  const beforePanes = await evaluate(driver.send, "window.overviewGuardState().panes");
  await moveToTitle(driver);
  await settleCard(driver);
  const initial = await measureCard(driver);
  assertCard(initial, true);
  if (!initial.focusUnchanged || JSON.stringify(initial.panes) !== JSON.stringify(beforePanes))
    throw new Error("hover reveal took focus or activated a session");
  await saveCard(driver, `${viewport.sidebar}-${viewport.theme}`);
  const longTitle = "Session context with a longer title that wraps onto several lines. ".repeat(2);
  await evaluate(driver.send, `window.applyShellNavigationDelta(${JSON.stringify(longTitle)})`);
  await driver.waitPage(`window.__sessionHoverGuard.card()?.textContent.includes(${JSON.stringify(longTitle)})`, {
    label: "visible card receives changed session context",
  });
  await settleCard(driver);
  const changed = await measureCard(driver);
  assertCard(changed, true);
  if (changed.id !== initial.id || changed.card.height <= initial.card.height + 20) {
    throw new Error(`changed context did not resize the same open card: ${JSON.stringify({ initial, changed })}`);
  }
  await evaluate(driver.send, "window.__sessionHoverGuard.scroller.scrollTop += 30");
  await driver.waitPage("window.__sessionHoverGuard.card() === null", { label: "scroll dismisses context card" });
  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 900, y: 20 });
  await evaluate(
    driver.send,
    `(() => {
    const state = window.__sessionHoverGuard;
    state.title.scrollIntoView({ block: 'center' });
    state.before = state.box(state.row);
  })()`,
  );
  await moveToTitle(driver);
  await settleCard(driver);
  assertCard(await measureCard(driver), true);
  await applyViewport(driver.send, { width: 1200, height: 760 });
  await driver.waitPage("window.__sessionHoverGuard.card() === null", { label: "resize dismisses context card" });
  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 900, y: 20 });
  await evaluate(
    driver.send,
    "window.__sessionHoverGuard.before = window.__sessionHoverGuard.box(window.__sessionHoverGuard.row)",
  );
  await moveToTitle(driver);
  await settleCard(driver);
  assertCard(await measureCard(driver), true);

  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 900, y: 20 });
  await evaluate(driver.send, "window.__sessionHoverGuard.row.focus()");
  for (const type of ["keyDown", "keyUp"]) {
    await driver.send("Input.dispatchKeyEvent", {
      type,
      key: "ArrowDown",
      code: "ArrowDown",
      windowsVirtualKeyCode: 40,
    });
  }
  await driver.waitPage(
    `document.activeElement?.getAttribute('role') === 'treeitem' &&
    document.activeElement !== window.__sessionHoverGuard.row && document.activeElement.hasAttribute('aria-describedby')`,
    { label: "real tree keyboard navigation reveals the next row's context" },
  );
  await evaluate(
    driver.send,
    `(() => {
    const state = window.__sessionHoverGuard;
    state.row = document.activeElement;
    state.title = state.row.querySelector('[data-testid="rail-row-title"]');
    state.before = state.box(state.row);
  })()`,
  );
  await settleCard(driver);
  assertCard(await measureCard(driver), true);
  await evaluate(driver.send, "document.activeElement.blur()");
  await driver.waitPage("window.__sessionHoverGuard.card() === null", { label: "keyboard blur dismisses context" });

  await evaluate(
    driver.send,
    `(() => {
    const state = window.__sessionHoverGuard;
    state.title = Array.from(state.rail.querySelectorAll('[data-testid="rail-row-title"]')).at(-2);
    state.row = state.title.closest('[role="treeitem"]');
    state.row.scrollIntoView({ block: 'end' });
    // Keep scroll headroom for the fractional row box on a whole-pixel scroller.
    state.scroller.scrollTop += 1;
  })()`,
  );
  await driver.waitPage(`window.__sessionHoverGuard.row.getBoundingClientRect().bottom <= innerHeight`, {
    label: "bottom session row is fully visible",
  });
  await evaluate(
    driver.send,
    "window.__sessionHoverGuard.before = window.__sessionHoverGuard.box(window.__sessionHoverGuard.row)",
  );
  const lastPoint = await evaluate(
    driver.send,
    `(() => {
    const r = window.__sessionHoverGuard.title.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`,
  );
  await driver.send("Input.dispatchMouseEvent", { type: "mouseMoved", ...lastPoint });
  await settleCard(driver);
  const bottom = await measureCard(driver);
  assertCard(bottom, true);
  if (Math.abs(bottom.card.bottom - bottom.viewport.height + 8) > 1) {
    throw new Error(`bottom collision fixture did not preserve edge clearance: ${JSON.stringify(bottom)}`);
  }
  return { gap: initial.card.left - initial.sidebar.right, initial, changed, bottom };
}

async function touchJourney(driver) {
  const point = await driver.elementBox(TITLE);
  if (!point) throw new Error("touch session title is not visible");
  const beforePanes = await evaluate(driver.send, "window.overviewGuardState().panes");
  await evaluate(
    driver.send,
    `(() => {
    window.__sessionHoverClick = null;
    document.addEventListener('click', event => {
      window.__sessionHoverClick = { trusted: event.isTrusted };
    }, { capture: true, once: true });
  })()`,
  );
  await driver.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: point.x, y: point.y }] });
  await settleCard(driver);
  const card = await measureCard(driver);
  assertCard(card, false);
  await saveCard(driver, "390-touch");
  await driver.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await driver.waitPage("window.__sessionHoverClick !== null", { label: "native long-press compatibility click" });
  const click = await evaluate(driver.send, "window.__sessionHoverClick");
  const afterPanes = await evaluate(driver.send, "window.overviewGuardState().panes");
  if (!click.trusted || JSON.stringify(beforePanes) !== JSON.stringify(afterPanes)) {
    throw new Error("long press activated a session or failed to receive the native compatibility click");
  }
  await driver.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 16, y: 20 }] });
  await driver.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await driver.waitPage("window.__sessionHoverGuard.card() === null", { label: "outside touch dismisses context" });
  await driver.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: point.x, y: point.y }] });
  await driver.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await driver.waitPage(
    `window.overviewGuardState().panes.some(p => p.type === 'session' && p.params.ref === 'local:p0-s0')`,
    { label: "ordinary touch still opens the chosen session" },
  );
  if (await evaluate(driver.send, "window.__sessionHoverGuard.card() !== null"))
    throw new Error("ordinary touch left a context card visible");
  return { gap: card.row.top - card.card.bottom, card, compatibilityClick: click.trusted, ordinaryTap: true };
}

async function onPage(cdpEndpoint, vitePort, viewport) {
  const target = await openPage(cdpEndpoint, "about:blank");
  const page = await connectPage(cdpEndpoint, target.id);
  const driver = new Driver({});
  driver.page = page;
  try {
    await driver.send("Storage.clearDataForOrigin", {
      origin: `http://127.0.0.1:${vitePort}`,
      storageTypes: "local_storage",
    });
    await applyViewport(driver.send, viewport);
    await navigateTo(page, `http://127.0.0.1:${vitePort}/shellguard.html`, {
      bootExpression: "typeof window.settledShell !== 'undefined'",
      bootLabel: "shellguard entry",
    });
    await evaluate(driver.send, "window.settledShell");
    await waitForFonts(driver.send);
    await evaluate(
      driver.send,
      `(async () => {
      const { prefsStore } = await import('/src/stores/prefs.ts');
      prefsStore.getState().setTheme(${JSON.stringify(viewport.theme)});
      prefsStore.getState().setFontSize('m');
      ${viewport.sidebar ? `prefsStore.getState().setSidebarWidth(${viewport.sidebar});` : ""}
    })()`,
    );
    await driver.waitPage(
      `(() => {
      const width = document.querySelector('[data-testid="rail"]')?.getBoundingClientRect().width;
      return ${viewport.sidebar ? `Math.abs(width - ${viewport.sidebar}) < 1` : "width > 0"};
    })()`,
      { label: "visible sessions sidebar" },
    );
    await evaluate(
      driver.send,
      `(async () => {
      const title = document.querySelector(${JSON.stringify(TITLE)});
      title.scrollIntoView({ block: 'center' });
      const row = title.closest('[role="treeitem"]');
      const rail = title.closest('[data-testid="rail"]');
      const box = element => element.getBoundingClientRect().toJSON();
      let scroller = row.parentElement;
      while (scroller && !['auto', 'scroll'].includes(getComputedStyle(scroller).overflowY)) scroller = scroller.parentElement;
      if (!scroller) throw new Error('session scroll owner is absent');
      window.__sessionHoverGuard = { title, row, rail, box, scroller, before: box(row), initialFocus: document.activeElement,
        card() { return document.getElementById(this.title.getAttribute('aria-describedby')); } };
    })()`,
    );
    return viewport.touch ? await touchJourney(driver) : await desktopJourney(driver, viewport);
  } catch (error) {
    const witness = await evaluate(
      driver.send,
      `(() => {
      const state = window.__sessionHoverGuard;
      if (!state) return { errors: window.__shellGuardErrors || [] };
      const r = state.title.getBoundingClientRect();
      return { hoverless: matchMedia('(hover: none)').matches,
        title: state.title.outerHTML, row: state.box(state.row),
        hit: document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)?.outerHTML,
        hovered: state.title.matches(':hover'), errors: window.__shellGuardErrors || [] };
    })()`,
    );
    throw new Error(`${error.message}; witness ${JSON.stringify(witness)}`, { cause: error });
  } finally {
    await clearViewportOverride(driver.send);
    page.close();
    await closePage(cdpEndpoint, target.id);
  }
}

export async function checkSessionHoverCards(cdpEndpoint, vitePort) {
  const failures = [];
  for (const viewport of CASES) {
    const label = `session hover card ${viewport.width}px ${viewport.sidebar ?? "drawer"} ${viewport.theme}`;
    try {
      const result = await onPage(cdpEndpoint, vitePort, viewport);
      console.log(`${label}: ${JSON.stringify(result)}`);
    } catch (error) {
      failures.push(`${label}: ${error.message}`);
    }
  }
  return failures;
}
