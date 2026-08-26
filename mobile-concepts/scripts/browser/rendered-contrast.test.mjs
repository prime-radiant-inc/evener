import assert from "node:assert/strict";
import test from "node:test";

import {
  analyzeDifferentialCapture,
  applyRenderedSamples,
} from "./geometry.mjs";
import { sampleRenderedContrast } from "../concept-browser.mjs";

const rgba = (red, green, blue, alpha = 255) => [red, green, blue, alpha];

function image(width, height, colors) {
  const data = new Uint8ClampedArray(width * height * 4);
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const value = typeof colors === "function" ? colors(x, y) : colors;
      data.set(value, (y * width + x) * 4);
    }
  }
  return { width, height, data };
}

function capture({
  width,
  height,
  original,
  hidden,
  black,
  white,
  clip = { x: 0, y: 0, width, height, scale: 1 },
  scroll = { x: 0, y: 0 },
}) {
  return {
    coordinateSystem: "document-css-pixels",
    clip,
    scroll,
    devicePixelRatio: width / clip.width,
    images: {
      original: image(width, height, original),
      hidden: image(width, height, hidden),
      black: image(width, height, black),
      white: image(width, height, white),
    },
  };
}

const candidate = (overrides = {}) => ({
  id: "candidate",
  kind: "text",
  minimum: 4.5,
  geometry: { left: 0, top: 0, right: 4, bottom: 1, width: 4, height: 1 },
  ...overrides,
});

function measured(pair, evidence) {
  return applyRenderedSamples(
    {
      id: pair.id,
      kind: pair.kind,
      minimum: pair.minimum,
      raw: { foreground: "rgb(255, 255, 255)", geometry: pair.geometry },
      unsupported: "background-image",
    },
    evidence,
  );
}

test("offscreen candidates are unresolved instead of clamped to an edge", () => {
  const pair = candidate({
    geometry: { left: 0, top: 120, right: 10, bottom: 130, width: 10, height: 10 },
  });
  const result = analyzeDifferentialCapture(
    pair,
    capture({
      width: 100,
      height: 100,
      original: rgba(255, 255, 255),
      hidden: rgba(0, 0, 0),
      black: rgba(0, 0, 0),
      white: rgba(255, 255, 255),
    }),
  );
  assert.equal(result.unresolved, "candidate-outside-capture");
  assert.deepEqual(result.mapping.candidateDocument, {
    left: 0,
    top: 120,
    right: 10,
    bottom: 130,
  });
  assert.equal(result.mask.pixelCount, 0);
});

test("DPR 2 maps fractional CSS geometry through pixel centers", () => {
  const pair = candidate({
    geometry: { left: 10.25, top: 5.25, right: 11.25, bottom: 6.25, width: 1, height: 1 },
  });
  const hidden = rgba(0, 0, 0);
  const result = analyzeDifferentialCapture(
    pair,
    capture({
      width: 40,
      height: 20,
      clip: { x: 0, y: 0, width: 20, height: 10, scale: 1 },
      original: (x, y) => (x === 21 && y === 11 ? rgba(255, 255, 255) : hidden),
      hidden,
      black: hidden,
      white: (x, y) => (x === 21 && y === 11 ? rgba(255, 255, 255) : hidden),
    }),
  );
  assert.equal(result.unresolved, undefined);
  assert.equal(result.mapping.scaleX, 2);
  assert.deepEqual(result.minimumEvidence.coordinate, {
    image: { x: 21, y: 11 },
    document: { x: 10.75, y: 5.75 },
    viewport: { x: 10.75, y: 5.75 },
  });
});

test("a low-contrast background equal to the foreground survives and wins", () => {
  const pair = candidate({ geometry: { left: 0, top: 0, right: 2, bottom: 1, width: 2, height: 1 } });
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 2,
      height: 1,
      hidden: (x) => (x === 0 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      original: rgba(255, 255, 255),
      black: rgba(0, 0, 0),
      white: rgba(255, 255, 255),
    }),
  );
  const result = measured(pair, evidence);
  assert.equal(result.ratio, 1);
  assert.deepEqual(result.raw.renderedSamples.surfacePalette.behind[0].color, {
    red: 255,
    green: 255,
    blue: 255,
    alpha: 1,
  });
});

test("white text over mixed white and black reports the white 1:1 worst case", () => {
  const pair = candidate();
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 4,
      height: 1,
      hidden: (x) => (x < 2 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      original: rgba(255, 255, 255),
      black: rgba(0, 0, 0),
      white: rgba(255, 255, 255),
    }),
  );
  assert.equal(measured(pair, evidence).ratio, 1);
  assert.equal(evidence.mask.pixelCount, 4);
  assert.equal(evidence.surfacePalette.behind.length, 2);
});

test("dense mask finds a sparse low-contrast gradient stop between old fixed points", () => {
  const pair = candidate({ geometry: { left: 0, top: 0, right: 9, bottom: 1, width: 9, height: 1 } });
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 9,
      height: 1,
      hidden: (x) => (x === 4 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      original: rgba(255, 255, 255),
      black: rgba(0, 0, 0),
      white: rgba(255, 255, 255),
    }),
  );
  assert.equal(measured(pair, evidence).ratio, 1);
  assert.equal(evidence.minimumEvidence.backgroundCoordinate.image.x, 4);
});

test("rounded or clipped paint mask excludes unrelated element-box corners", () => {
  const pair = candidate({ kind: "nontext", minimum: 3, geometry: { left: 0, top: 0, right: 5, bottom: 5, width: 5, height: 5 } });
  const inShape = (x, y) => !(x === 0 && y === 0) && !(x === 4 && y === 0) && !(x === 0 && y === 4) && !(x === 4 && y === 4);
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 5,
      height: 5,
      hidden: (x, y) => (inShape(x, y) ? rgba(0, 0, 0) : rgba(255, 255, 255)),
      original: (x, y) => (inShape(x, y) ? rgba(255, 255, 255) : rgba(255, 255, 255)),
      black: rgba(0, 0, 0),
      white: (x, y) => (inShape(x, y) ? rgba(255, 255, 255) : rgba(0, 0, 0)),
    }),
  );
  assert.equal(evidence.mask.pixelCount, 21);
  assert.equal(evidence.mask.runs.some((run) => run.y === 0 && run.startX === 0), false);
});

test("antialiased glyph pixels reveal rather than impersonate the behind-glyph background", () => {
  const pair = candidate({ geometry: { left: 0, top: 0, right: 1, bottom: 1, width: 1, height: 1 } });
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 1,
      height: 1,
      hidden: rgba(0, 0, 0),
      original: rgba(128, 128, 128),
      black: rgba(0, 0, 0),
      white: rgba(128, 128, 128),
    }),
  );
  assert.deepEqual(evidence.minimumEvidence.background, { red: 0, green: 0, blue: 0, alpha: 1 });
  assert.ok(evidence.minimumEvidence.coverage > 0.49 && evidence.minimumEvidence.coverage < 0.51);
  assert.ok(measured(pair, evidence).ratio > 20);
});

test("focus mask ignores an unchanged decorative shadow", () => {
  const pair = candidate({ kind: "focus", minimum: 3, geometry: { left: 2, top: 0, right: 4, bottom: 1, width: 2, height: 1 } });
  const decorative = rgba(20, 20, 20);
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 6,
      height: 1,
      hidden: (x) => (x === 0 ? decorative : rgba(0, 0, 0)),
      original: (x) => (x === 0 ? decorative : x === 1 || x === 4 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      black: (x) => (x === 0 ? decorative : rgba(0, 0, 0)),
      white: (x) => (x === 0 ? decorative : x === 1 || x === 4 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
    }),
  );
  assert.equal(evidence.mask.pixelCount, 2);
  assert.equal(evidence.mask.runs.some((run) => run.startX === 0), false);
});

test("focus retains and evaluates inside and outside adjacent surfaces separately", () => {
  const pair = candidate({ kind: "focus", minimum: 3, geometry: { left: 2, top: 0, right: 4, bottom: 1, width: 2, height: 1 } });
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 6,
      height: 1,
      hidden: (x) => (x >= 2 && x < 4 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      original: (x) => (x === 1 || x === 4 ? rgba(255, 255, 255) : x >= 2 && x < 4 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      black: (x) => (x >= 2 && x < 4 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      white: (x) => (x === 1 || x === 4 ? rgba(255, 255, 255) : x >= 2 && x < 4 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
    }),
  );
  assert.ok(evidence.surfacePalette.inside.length > 0);
  assert.ok(evidence.surfacePalette.outside.length > 0);
  assert.ok(evidence.surfaceVerdicts.inside);
  assert.ok(evidence.surfaceVerdicts.outside);
});

test("full-page mapping includes scroll offsets and records capture timing", () => {
  const pair = candidate({ geometry: { left: 1, top: 5, right: 2, bottom: 6, width: 1, height: 1 } });
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({
      width: 10,
      height: 10,
      clip: { x: 0, y: 100, width: 10, height: 10, scale: 1 },
      scroll: { x: 0, y: 100 },
      hidden: rgba(0, 0, 0),
      original: (x, y) => (x === 1 && y === 5 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
      black: rgba(0, 0, 0),
      white: (x, y) => (x === 1 && y === 5 ? rgba(255, 255, 255) : rgba(0, 0, 0)),
    }),
  );
  assert.deepEqual(evidence.mapping.candidateDocument, { left: 1, top: 105, right: 2, bottom: 106 });
  assert.deepEqual(evidence.minimumEvidence.coordinate.document, { x: 1.5, y: 105.5 });
  assert.deepEqual(evidence.timing.sequence, ["stabilized", "geometry", "original", "hidden", "black-probe", "white-probe"]);
});

test("ambiguous or missing differential mask stays unresolved", () => {
  const pair = candidate();
  const same = rgba(10, 20, 30);
  const evidence = analyzeDifferentialCapture(
    pair,
    capture({ width: 4, height: 1, original: same, hidden: same, black: same, white: same }),
  );
  const result = measured(pair, evidence);
  assert.equal(evidence.unresolved, "missing-differential-mask");
  assert.equal(result.unsupported, "missing-differential-mask");
  assert.equal(result.ratio, undefined);
});

test("concept-browser uses the exported sampler to apply auditable evidence", async () => {
  const pair = {
    id: "wired",
    kind: "text",
    minimum: 4.5,
    raw: { geometry: { left: 0, top: 0, right: 1, bottom: 1, width: 1, height: 1 }, foreground: "rgb(255, 255, 255)" },
    unsupported: "background-image",
  };
  let calls = 0;
  const result = await sampleRenderedContrast(null, [pair], {
    captureCandidates: async (candidates) => {
      calls += 1;
      assert.equal(candidates[0].id, "wired");
      return [
        analyzeDifferentialCapture(
          { ...candidates[0], geometry: candidates[0].raw.geometry },
          capture({ width: 1, height: 1, original: rgba(255, 255, 255), hidden: rgba(0, 0, 0), black: rgba(0, 0, 0), white: rgba(255, 255, 255) }),
        ),
      ];
    },
  });
  assert.equal(calls, 1);
  assert.equal(result[0].unsupported, undefined);
  assert.ok(result[0].raw.renderedSamples.mask.runs.length > 0);
});

test("the exact analyzer serialized into concept-browser is self-contained", () => {
  const browserAnalyzer = Function(
    `"use strict";return (${analyzeDifferentialCapture.toString()})`,
  )();
  const result = browserAnalyzer(
    candidate({ geometry: { left: 0, top: 0, right: 1, bottom: 1, width: 1, height: 1 } }),
    capture({
      width: 1,
      height: 1,
      original: rgba(255, 255, 255),
      hidden: rgba(0, 0, 0),
      black: rgba(0, 0, 0),
      white: rgba(255, 255, 255),
    }),
  );
  assert.ok(result.ratio > 20);
});
