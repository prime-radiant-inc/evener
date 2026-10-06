import assert from "node:assert/strict";
import { applyViewport, closePage, connectPage, evaluate, openPage } from "../browserGuardCdp.mjs";

// Direct real-Chrome regression for the guard's input helper. A trusted hover
// moves the same control before its trusted click, as late layout can do.
export async function checkMovingControls(endpoint, clickControl) {
  const target = await openPage(endpoint, "about:blank");
  const page = await connectPage(endpoint, target.id);
  const results = [];
  try {
    await applyViewport(page.send, { width: 1400, height: 900 });
    for (const motion of ["stationary", "hover", "hover-reveal"]) {
      await page.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 0, y: 0 });
      await evaluate(page.send, `(() => {
        document.body.innerHTML = '<button id="control" style="position:fixed;left:100px;top:100px;width:28px;height:28px"><span id="marker">×</span></button>';
        window.overviewGuardState = () => ({});
        const control = document.querySelector('#control');
        window.clickFixture = { control, clicks: [] };
        control.addEventListener('click', event => {
          window.clickFixture.clicks.push({ trusted: event.isTrusted, x: event.clientX, y: event.clientY });
        });
        if (${JSON.stringify(motion)} !== 'stationary') {
          control.addEventListener('mousemove', () => {
            control.style.left = '300px';
            if (${JSON.stringify(motion)} === 'hover-reveal') {
              control.style.visibility = 'hidden';
              requestAnimationFrame(() => { control.style.visibility = 'visible'; });
            }
          }, { once: true });
        }
      })()`);
      await clickControl(page.send, "#marker");
      const result = await evaluate(page.send, `(() => {
        const { control, clicks } = window.clickFixture;
        return { sameControl: control === document.querySelector('#control'), clicks,
          box: control.getBoundingClientRect().toJSON(), visibility: getComputedStyle(control).visibility };
      })()`);
      assert.equal(result.sameControl, true, `${motion}: control identity`);
      assert.equal(result.clicks.length, 1, `${motion}: exactly one activation`);
      assert.equal(result.clicks[0].trusted, true, `${motion}: native click`);
      assert.equal(result.visibility, "visible", `${motion}: visible target`);
      assert.equal(result.box.left, motion === "stationary" ? 100 : 300, `${motion}: actual layout moved`);
      const { x, y } = result.clicks[0];
      assert.ok(x > result.box.left && x < result.box.right && y > result.box.top && y < result.box.bottom,
        `${motion}: click uses current target geometry`);
      results.push({ motion, ...result });
    }
    return results;
  } finally {
    page.close();
    await closePage(endpoint, target.id);
  }
}
