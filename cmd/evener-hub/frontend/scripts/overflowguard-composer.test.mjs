import assert from "node:assert/strict";
import test from "node:test";
import { assertComposerPlacement } from "./overflowguard/composer.mjs";

function measurement(width, ids, { stop = true, steer = false, statusBottom = 40, triggerTop = 20 } = {}) {
  return {
    currentWork: {
      verbCluster: { containerWidth: width, controls: ids.length - 1, top: 20, statusRowBottom: statusBottom },
      controls: ids.map((testId) => ({ testId, present: true })),
      controlGeometry: Object.fromEntries(
        ids.map((testId) => [
          testId,
          { left: 0, right: 44, top: triggerTop, bottom: triggerTop + 44, width: 44, height: 44 },
        ]),
      ),
      controlsFound: true,
      controlsContained: true,
      controlsDoNotOverlap: true,
      composerCardFound: true,
      composerFitsPane: true,
      found: false,
    },
    trigger: { left: 0, right: 44, top: triggerTop, bottom: triggerTop + 44 },
    scrollers: [],
    menuVerbs: { open: true, stop, steer },
  };
}

test("a compact folded composer checks menu alignment when no status row exists", () => {
  const expected = { contentWidth: 358, primary: "stop", inlineSteer: false, menuSteer: false };
  const aligned = measurement(358, ["composer-attach", "composer-stop"], { statusBottom: null, triggerTop: 20 });
  assertComposerPlacement(aligned, expected);
  const wrapped = measurement(358, ["composer-attach", "composer-stop"], { statusBottom: null, triggerTop: 64 });
  assert.throws(() => assertComposerPlacement(wrapped, expected), assert.AssertionError);
  const staleWork = structuredClone(aligned);
  staleWork.currentWork.found = true;
  assert.throws(() => assertComposerPlacement(staleWork, expected), assert.AssertionError);
});

for (const contentWidth of [399, 400]) {
  for (const primary of ["stop", "submit"]) {
    const inlineSteer = contentWidth === 400;
    const expected = { contentWidth, primary, inlineSteer, menuSteer: !inlineSteer };
    const ids = ["composer-attach", `composer-${primary}`, ...(inlineSteer ? ["composer-steer"] : [])];
    test(`${contentWidth}px ${primary} accepts the independent primary and Steer locations`, () => {
      assertComposerPlacement(measurement(contentWidth, ids, { steer: !inlineSteer }), expected);
    });
    test(`${contentWidth}px ${primary} rejects wrong content width, both primary buttons, missing menu Stop and misplaced Steer`, () => {
      const good = measurement(contentWidth, ids, { steer: !inlineSteer });
      const wrongWidth = structuredClone(good);
      wrongWidth.currentWork.verbCluster.containerWidth--;
      assert.throws(() => assertComposerPlacement(wrongWidth, expected), assert.AssertionError);
      const both = structuredClone(good);
      both.currentWork.controls.push({
        testId: primary === "stop" ? "composer-submit" : "composer-stop",
        present: true,
      });
      assert.throws(() => assertComposerPlacement(both, expected), assert.AssertionError);
      const noStop = structuredClone(good);
      noStop.menuVerbs.stop = false;
      assert.throws(() => assertComposerPlacement(noStop, expected), assert.AssertionError);
      const movedSteer = structuredClone(good);
      movedSteer.menuVerbs.steer = inlineSteer;
      assert.throws(() => assertComposerPlacement(movedSteer, expected), assert.AssertionError);
      const narrowAttach = structuredClone(good);
      narrowAttach.currentWork.controlGeometry["composer-attach"].width = 43;
      assert.throws(() => assertComposerPlacement(narrowAttach, expected), assert.AssertionError);
    });
  }
}
test("placement rejects overflow, overlapping controls and wrapped verbs", () => {
  const expected = { contentWidth: 399, primary: "stop", inlineSteer: false, menuSteer: false };
  for (const breakGeometry of [
    (m) => {
      m.scrollers.push({});
    },
    (m) => {
      m.currentWork.controlsDoNotOverlap = false;
    },
    (m) => {
      m.currentWork.composerFitsPane = false;
    },
    (m) => {
      m.currentWork.verbCluster.top = 40;
    },
  ]) {
    const m = measurement(399, ["composer-attach", "composer-stop"]);
    breakGeometry(m);
    assert.throws(() => assertComposerPlacement(m, expected), assert.AssertionError);
  }
});
