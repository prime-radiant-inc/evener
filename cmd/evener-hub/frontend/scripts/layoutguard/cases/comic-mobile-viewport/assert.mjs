import { evaluate } from "../../../browserGuardCdp.mjs";

const frame = (send) => evaluate(send, "new Promise(resolve => requestAnimationFrame(resolve))");

async function until(send, expression) {
  const deadline = Date.now() + 5000;
  do {
    const value = await evaluate(send, expression);
    if (value) return value;
    await frame(send);
  } while (Date.now() < deadline);
  throw Error(
    `Comic geometry did not settle: ${expression}: ${await evaluate(send, "window.comicGeometryError || document.body.innerHTML")}`,
  );
}

function pickerGeometry() {
  const root = document.querySelector("main");
  const section = root.querySelector('section[aria-label="live"]');
  const lastRow = section.querySelector("li:last-child button");
  const more = section.querySelector(":scope > button");
  const rect = (node) => {
    if (!node) return null;
    const r = node.getBoundingClientRect();
    const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    return {
      top: r.top,
      bottom: r.bottom,
      left: r.left,
      right: r.right,
      x: r.x + r.width / 2,
      y: r.y + r.height / 2,
      hit: hit === node || node.contains(hit),
    };
  };
  return {
    height: innerHeight,
    width: innerWidth,
    root: rect(root),
    scrollTop: root.scrollTop,
    documentScroll: scrollY,
    overflow: document.documentElement.scrollWidth - innerWidth,
    rows: section.querySelectorAll("li").length,
    lastRow: rect(lastRow),
    more: rect(more),
  };
}

function sessionGeometry() {
  const rect = (node) => {
    if (!node) return null;
    const r = node.getBoundingClientRect();
    const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
    return { top: r.top, bottom: r.bottom, height: r.height, left: r.left, right: r.right, hit: node.contains(hit) };
  };
  const sources = [...document.querySelectorAll("[data-comic-entry]")];
  const source = window.comicGeometry.source;
  return {
    height: innerHeight,
    width: innerWidth,
    root: rect(document.querySelector("main")),
    composer: rect(document.querySelector("[data-comic-composer]")),
    editor: rect(document.querySelector('[role="textbox"]')),
    submit: rect(document.querySelector('[data-testid="composer-submit"]')),
    reader: rect(document.querySelector("[data-comic-scroll]")),
    draft: document.querySelector('[role="textbox"]')?.textContent,
    boxSizing: getComputedStyle(document.querySelector("main")).boxSizing,
    overflow: document.documentElement.scrollWidth - innerWidth,
    sourceParts: sources.length,
    literal: sources.every(
      (node) =>
        node.querySelector("[data-comic-text]")?.textContent ===
        source.slice(+node.dataset.comicStart, +node.dataset.comicEnd),
    ),
  };
}

function modelToolbarViewport(largeHeight, visibleHeight) {
  // Desktop headless Chrome has no retractable URL bar: vh and dvh resolve
  // equally even with mobile emulation. Record that native fact first. Then
  // resolve the REAL Comic stylesheet's viewport-unit declarations against
  // explicit large/dynamic dimensions, leaving selectors and cascade intact.
  // This tests the CSS constraint, not an actual mobile toolbar implementation.
  const probe = document.createElement("div");
  document.body.append(probe);
  const native = {};
  for (const unit of ["vh", "dvh"]) {
    probe.style.height = `100${unit}`;
    native[unit] = probe.getBoundingClientRect().height;
  }
  probe.remove();
  const sheet = [...document.styleSheets].find((sheet) =>
    sheet.ownerNode?.dataset.viteDevId?.endsWith("/src/comic/comic.module.css"),
  );
  if (!sheet) throw Error("real Comic stylesheet missing");
  const rules = [...sheet.cssRules].map((rule) => rule.cssText).join("\n");
  const substitutions = [];
  const modeled = rules.replace(/(\d+(?:\.\d+)?)(dvh|svh|lvh|vh)\b/g, (token, amount, unit) => {
    const px = (Number(amount) / 100) * (unit === "dvh" || unit === "svh" ? visibleHeight : largeHeight);
    substitutions.push({ token, px });
    return `${px}px`;
  });
  const style = document.createElement("style");
  style.id = "modeled-toolbar-viewport";
  style.textContent = modeled;
  document.head.append(style);
  return { native, largeHeight, visibleHeight, substitutions };
}

export default async function assert(_measurement, { page }) {
  const { send } = page;
  const failures = [];
  const evidence = [];
  const readPicker = () => evaluate(send, `(${pickerGeometry})()`);
  const readSession = () => evaluate(send, `(${sessionGeometry})()`);
  const reachable = (box, width, height) =>
    box && box.top >= -1 && box.bottom <= height + 1 && box.left >= -1 && box.right <= width + 1;

  for (const width of [320, 390, 1360]) {
    await send("Emulation.setDeviceMetricsOverride", { width, height: 620, deviceScaleFactor: 1, mobile: width < 900 });
    await send("Emulation.setTouchEmulationEnabled", { enabled: width < 900 });
    await evaluate(send, 'window.comicGeometry.render("picker", 0)');
    await frame(send);
    const empty = await readPicker();
    if (empty.rows || empty.overflow > 1 || !reachable(empty.root, width, 620))
      failures.push(`empty picker ${width}: ${JSON.stringify(empty)}`);

    for (const gesture of width < 900 ? ["wheel", "touch"] : ["wheel"]) {
      await evaluate(
        send,
        'window.comicGeometry.render("picker", 50); document.querySelector("main").scrollTop = 0; window.scrollTo(0, 0)',
      );
      await frame(send);
      const before = await readPicker();
      if (before.rows !== 50 || before.lastRow.bottom < 620 || before.more.bottom < 620)
        throw Error(`long picker fixture is not below the fold: ${JSON.stringify(before)}`);
      // Native input only: never scrollTop/scrollIntoView to make targets reachable.
      if (gesture === "wheel") {
        await send("Input.dispatchMouseEvent", { type: "mouseWheel", x: width / 2, y: 400, deltaX: 0, deltaY: 10000 });
      } else {
        for (let swipe = 0; swipe < 5; swipe++) {
          await send("Input.dispatchTouchEvent", {
            type: "touchStart",
            touchPoints: [{ x: width - 20, y: 550, id: 1 }],
          });
          for (const y of [450, 350, 250, 150, 50]) {
            await send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x: width - 20, y, id: 1 }] });
            await frame(send);
          }
          await send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
        }
      }
      // A native scroll may commit after the CDP reply. Stop at the actual end,
      // or a short bound that exposes the locked pre-fix document.
      const deadline = Date.now() + 1500;
      let after;
      do {
        await frame(send);
        after = await readPicker();
        if (reachable(after.more, width, 620) && after.more.hit) break;
      } while (Date.now() < deadline);
      evidence.push({ width, gesture, before, after });
      if (
        !reachable(after.lastRow, width, 620) ||
        !after.lastRow.hit ||
        !reachable(after.more, width, 620) ||
        !after.more.hit ||
        after.overflow > 1 ||
        (after.scrollTop <= before.scrollTop && after.documentScroll <= before.documentScroll)
      ) {
        failures.push(`picker ${width} ${gesture}: lower row/pagination unreachable ${JSON.stringify(after)}`);
      } else {
        await evaluate(send, "window.selectedComicRef = null");
        await send("Input.dispatchMouseEvent", {
          type: "mousePressed",
          x: after.lastRow.x,
          y: after.lastRow.y,
          button: "left",
          clickCount: 1,
        });
        await send("Input.dispatchMouseEvent", {
          type: "mouseReleased",
          x: after.lastRow.x,
          y: after.lastRow.y,
          button: "left",
          clickCount: 1,
        });
        if ((await evaluate(send, "window.selectedComicRef")) !== "local:geometry-49")
          throw Error("native lower-row selection did not reach the real picker callback");
      }
    }

    await evaluate(send, 'window.scrollTo(0, 0); window.comicGeometry.render("session")');
    await until(
      send,
      '!!document.querySelector("[data-comic-composer]") && !!document.querySelector("[data-comic-entry]")',
    );
    await evaluate(send, "document.fonts.ready");
    await frame(send);
    const native = await readSession();
    const checkSession = (name, state) => {
      if (
        state.overflow > 1 ||
        !state.sourceParts ||
        !state.literal ||
        !state.reader ||
        state.reader.height <= 0 ||
        ![state.root, state.composer, state.editor, state.submit].every((box) => reachable(box, width, 620))
      )
        failures.push(`${name} ${width}: ${JSON.stringify(state)}`);
    };
    checkSession("native session", native);
    const model = await evaluate(send, `(${modelToolbarViewport})(760, 620)`);
    await frame(send);
    const toolbar = await readSession();
    checkSession("modeled toolbar session", toolbar);
    if (model.native.dvh !== 620 || model.native.vh !== 620 || !model.substitutions.length)
      throw Error(`unexpected native viewport/unit model: ${JSON.stringify(model)}`);
    // Independently resolve the untouched declaration in the native browser.
    await evaluate(send, 'document.getElementById("modeled-toolbar-viewport").remove()');
    await frame(send);
    const restored = await readSession();
    checkSession("restored native session", restored);
    evidence.push({ width, native, model, toolbar, restored });

    // Keyboard boundary simulation, not physical Safari coverage. The layout
    // viewport stays 620px while visualViewport shrinks and scrolls independently.
    await evaluate(send, "window.comicGeometry.simulateViewport()");
    const draft = `KEYBOARD_DRAFT_${width}`;
    await evaluate(send, `(() => {
      const editor = document.querySelector('[role="textbox"]');
      editor.focus();
      const range = document.createRange();
      range.selectNodeContents(editor);
      window.getSelection().removeAllRanges();
      window.getSelection().addRange(range);
    })()`);
    await send("Input.insertText", { text: draft });
    await frame(send);
    const beforeKeyboard = await readSession();
    for (const [name, event, values, visibleBottom] of [
      ["open", "resize", { height: 440, offsetTop: 0, scale: 1 }, 440],
      ["scroll", "scroll", { offsetTop: 40 }, 480],
      ["pinch", "resize", { scale: 2 }, 620],
      ["unpinch", "resize", { scale: 1 }, 480],
      ["close", "resize", { height: 620, offsetTop: 0 }, 620],
    ]) {
      await evaluate(send, `window.comicGeometry.viewport(${JSON.stringify(event)}, ${JSON.stringify(values)})`);
      await frame(send);
      const state = await readSession();
      const bottom = width < 900 ? visibleBottom : 620;
      if (
        state.boxSizing !== "border-box" ||
        state.root.bottom !== 620 ||
        state.overflow > 1 ||
        state.reader.height <= 0 ||
        !state.sourceParts ||
        !state.literal ||
        state.draft !== draft ||
        !state.editor.hit ||
        !state.submit.hit ||
        ![state.composer, state.editor, state.submit].every((box) => reachable(box, width, bottom)) ||
        Math.abs(state.composer.bottom - bottom) > 1
      ) failures.push(`keyboard ${name} ${width}: expected bottom ${bottom}, ${JSON.stringify(state)}`);
      if ((name === "close" || width >= 900) && Math.abs(state.reader.height - beforeKeyboard.reader.height) > 1)
        failures.push(`keyboard ${name} ${width}: reader height did not restore`);
      evidence.push({ width, keyboard: name, composerBottom: state.composer.bottom, readerHeight: state.reader.height });
    }
    await evaluate(send, "window.comicGeometry.restoreViewport()");
  }
  return {
    pass: !failures.length,
    reason: failures.length
      ? failures.join("\n")
      : `320/390/1360px empty/long picker, native wheel/touch, lower-row selection and pagination hit-testing, real session/composer within 620px under native and modeled 760px large viewport, simulated visualViewport keyboard open/scroll/pinch/close, draft/source retained and reader restored. ${JSON.stringify(evidence.map(({ width, gesture, after, toolbar, keyboard, composerBottom, readerHeight }) => ({ width, gesture, pickerScroll: after?.scrollTop, composerBottom: composerBottom ?? toolbar?.composer.bottom, keyboard, readerHeight })))}`,
  };
}
