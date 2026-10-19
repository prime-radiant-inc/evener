import assert from "node:assert/strict";
import { applyViewport, clearViewportOverride, closePage, connectPage, evaluate, openPage } from "../browserGuardCdp.mjs";

// Removing native content sizing from applyViewport must fail this case on macOS:
// after a tab switch Chrome still reports the emulated viewport and delivers a
// trusted wheel, but will not scroll outside the smaller native content bounds.
export async function assertNativeViewportWheel(endpoint) {
  const tabs = [];
  const failures = [];
  try {
    for (let i = 0; i < 2; i += 1) {
      const target = await openPage(endpoint, "about:blank");
      const tab = { target, page: null };
      tabs.push(tab);
      tab.page = await connectPage(endpoint, target.id);
    }
    const [reader, sibling] = tabs;
    const { send } = reader.page;
    await send("Page.enable");
    const original = await evaluate(send, "({width: innerWidth, height: innerHeight})");
    // The desktop point reproduces #4745. The narrow point also exercises
    // height beyond the default native window after a responsive resize.
    const cases = [
      { viewport: { width: 1440, height: 900 }, x: 860, y: 453 },
      { viewport: { width: 420, height: 844 }, x: 210, y: 650 },
    ];
    for (const { viewport, x, y } of cases) {
      await send("Page.bringToFront");
      await applyViewport(send, viewport);
      await evaluate(send, `(async () => {
        document.body.innerHTML = '<div id="port" style="position:absolute;left:${x - 100}px;top:${y - 100}px;width:200px;height:200px;overflow:auto"><div style="height:20000px"></div></div>';
        const port = document.querySelector('#port');
        port.scrollTop = 6326;
        await new Promise(requestAnimationFrame);
        return true;
      })()`);
      await sibling.page.send("Page.bringToFront");
      await send("Page.bringToFront");
      await evaluate(send, `(() => {
        const port = document.querySelector('#port');
        const before = port.scrollTop;
        const evidence = { before, wheel: null };
        port.addEventListener('wheel', event => {
          evidence.wheel = { trusted: event.isTrusted, prevented: event.defaultPrevented, delta: event.deltaY };
        }, { once: true, passive: true });
        window.nativeWheelDone = new Promise(resolve => {
          const finish = () => {
            clearTimeout(timer);
            port.removeEventListener('scroll', onScroll);
            resolve({ ...evidence, after: port.scrollTop });
          };
          const onScroll = () => { if (port.scrollTop > before) finish(); };
          const timer = setTimeout(finish, 15000);
          port.addEventListener('scroll', onScroll);
        });
        return true;
      })()`);
      await send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: 0, deltaY: 8 });
      const result = await evaluate(send, "window.nativeWheelDone");
      assert.ok(result.wheel?.trusted && !result.wheel.prevented && result.wheel.delta === 8,
        `native wheel delivery after tab switch: ${JSON.stringify(result)}`);
      assert.ok(result.after > result.before,
        `native wheel must scroll at (${x},${y}) after tab switch: ${JSON.stringify(result)}`);
    }
    await clearViewportOverride(send);
    const restored = await evaluate(send, `(async () => {
      const deadline = performance.now() + 15000;
      while ((innerWidth !== ${original.width} || innerHeight !== ${original.height}) && performance.now() < deadline) {
        await new Promise(requestAnimationFrame);
      }
      return {width: innerWidth, height: innerHeight};
    })()`);
    assert.deepEqual(restored, original, "clearing viewport restores original native content bounds");

    // A failed native restore must not prevent either emulation cleanup. Only
    // this protocol error is injected; Chrome supplies the observable effects.
    const features = "({scale: devicePixelRatio, coarse: matchMedia('(pointer: coarse)').matches})";
    const originalFeatures = await evaluate(send, features);
    let rejectRestore = false;
    const interruptedSend = (method, params) => {
      if (rejectRestore && method === "Browser.setWindowBounds") {
        return Promise.reject(new Error("injected native window restore failure"));
      }
      return send(method, params);
    };
    await applyViewport(interruptedSend, {
      width: 420, height: 844, deviceScaleFactor: originalFeatures.scale + 1, mobile: true, touch: true,
    });
    assert.deepEqual(await evaluate(send, features), { scale: originalFeatures.scale + 1, coarse: true });
    rejectRestore = true;
    await clearViewportOverride(interruptedSend);
    assert.deepEqual(await evaluate(send, features), originalFeatures,
      "native restore failure still clears metrics and touch emulation");
    rejectRestore = false;
    await clearViewportOverride(interruptedSend);
    const retried = await evaluate(send, `(async () => {
      const deadline = performance.now() + 15000;
      while ((innerWidth !== ${original.width} || innerHeight !== ${original.height}) && performance.now() < deadline) {
        await new Promise(requestAnimationFrame);
      }
      return {width: innerWidth, height: innerHeight};
    })()`);
    assert.deepEqual(retried, original, "retrying viewport cleanup restores original native content bounds");
  } catch (error) {
    failures.push(error);
  } finally {
    const closed = await Promise.allSettled(tabs.map(async ({ target, page }) => {
      page?.close();
      await closePage(endpoint, target.id);
    }));
    failures.push(...closed.filter(result => result.status === "rejected").map(result => result.reason));
  }
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) {
    throw new AggregateError(failures, `native viewport assertion or cleanup failed: ${failures.map(String).join("\n")}`);
  }
}
