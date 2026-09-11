import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { assertEditorialGeometry } from "./editorial-preview-measure.mjs";

// Complete, unmodified measurements from the accepted private Chrome run.
// The committed JSON records the primary artifact hash and exact source commit.
const { samples: [evidence, collaborators] } = JSON.parse(
  readFileSync(new URL("./editorial-preview.touch-samples.json", import.meta.url), "utf8"),
);

function check(measurement, context = {}) {
  assertEditorialGeometry(assert, measurement, "explicit workflow context", context);
}

test("primary phone evidence and collaborator samples pass their actual contexts", () => {
  assert.doesNotThrow(() => check(evidence));
  assert.doesNotThrow(() => check(collaborators, { collaborators: true }));
  assert.equal(evidence.buttons.some(button => button.label === "Open transcript"), false);
});

for (const label of ["Send", "Open", "Open transcript"]) {
  for (const [width, height] of [[1, 1], [1, 44], [44, 1]]) {
    test(`rejects ${label} target at ${width}x${height} without dropping legacy labels`, () => {
      const measurement = structuredClone(collaborators);
      const target = measurement.buttons.find(button => button.label === (label === "Open" ? "Open transcript" : label));
      assert(target);
      assert.equal(target.width, 44);
      assert.equal(target.height, 44);
      // The generic Open case retains that pre-existing oracle contract using
      // the same genuine Open geometry; every other case keeps its real label.
      target.label = label;
      Object.assign(target, { width, height, right: target.x + width, bottom: target.y + height });
      assert.throws(() => check(measurement, { collaborators: true }), /below 44px/);
    });
  }
}

test("phone Send must have a measured visible sample", () => {
  const measurement = structuredClone(evidence);
  measurement.buttons = measurement.buttons.filter(button => button.label !== "Send");
  assert.throws(() => check(measurement), /phone Send sample missing/);
});

test("collaborator Open transcript must have a measured visible sample", () => {
  const measurement = structuredClone(collaborators);
  measurement.buttons = measurement.buttons.filter(button => button.label !== "Open transcript");
  assert.throws(() => check(measurement, { collaborators: true }), /collaborator Open transcript sample missing/);
});

test("evidence-only context does not require offscreen Open controls regardless of diagnostic label", () => {
  const measurement = structuredClone(evidence);
  measurement.label = collaborators.label;
  assert.doesNotThrow(() => check(measurement, { collaborators: false }));
});
