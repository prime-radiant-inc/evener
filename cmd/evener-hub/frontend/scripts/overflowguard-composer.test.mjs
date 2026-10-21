import assert from "node:assert/strict";
import test from "node:test";
import { assertComposerPlacement } from "./overflowguard/composer.mjs";

function measurement(width, ids, { stop = true, steer = false } = {}) {
  return {
    currentWork: {
      verbCluster: { containerWidth: width, controls: ids.length - 1, top: 20, statusRowBottom: 40 },
      controls: ids.map((testId) => ({ testId, present: true })),
      controlsFound: true,
      controlsContained: true,
      controlsDoNotOverlap: true,
      sharedPaneWithoutOverflow: true,
    },
    scrollers: [],
    menuVerbs: { open: true, stop, steer },
  };
}

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
      m.currentWork.verbCluster.top = 40;
    },
  ]) {
    const m = measurement(399, ["composer-attach", "composer-stop"]);
    breakGeometry(m);
    assert.throws(() => assertComposerPlacement(m, expected), assert.AssertionError);
  }
});
