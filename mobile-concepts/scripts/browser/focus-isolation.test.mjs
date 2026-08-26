import assert from "node:assert/strict";
import test from "node:test";

import { analyzeDifferentialCapture } from "./geometry.mjs";

const rgba = (red, green, blue, alpha = 255) => [red, green, blue, alpha];

function focusFixture({
  width = 100,
  height = 100,
  target = { left: 40, top: 40, right: 60, bottom: 60 },
  components,
  work,
}) {
  const changed = new Set(
    components.flatMap((component) =>
      component.map(([x, y]) => y * width + x),
    ),
  );
  const backgroundAt = (x, y) =>
    x >= target.left && x < target.right && y >= target.top && y < target.bottom
      ? rgba(238, 238, 238)
      : rgba(24, 24, 24);
  const makeImage = (paint) => {
    const data = new Uint8ClampedArray(width * height * 4);
    for (let y = 0; y < height; y += 1) {
      for (let x = 0; x < width; x += 1) {
        data.set(
          changed.has(y * width + x) ? paint : backgroundAt(x, y),
          (y * width + x) * 4,
        );
      }
    }
    return { width, height, data };
  };
  const hiddenData = new Uint8ClampedArray(width * height * 4);
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1)
      hiddenData.set(backgroundAt(x, y), (y * width + x) * 4);
  }
  return {
    candidate: {
      id: "target focus indicator",
      kind: "focus",
      minimum: 3,
      geometry: {
        ...target,
        width: target.right - target.left,
        height: target.bottom - target.top,
      },
      raw: {
        paint: {
          paints: [{ source: "outline", width: 2, offset: 2 }],
        },
      },
    },
    capture: {
      coordinateSystem: "document-css-pixels",
      clip: { x: 0, y: 0, width, height, scale: 1 },
      scroll: { x: 0, y: 0 },
      devicePixelRatio: 1,
      work,
      images: {
        original: makeImage(rgba(30, 120, 240)),
        hidden: { width, height, data: hiddenData },
        black: makeImage(rgba(0, 0, 0)),
        white: makeImage(rgba(255, 255, 255)),
      },
    },
  };
}

function rectangleRing(left, top, right, bottom) {
  const pixels = [];
  for (let x = left; x < right; x += 1) {
    pixels.push([x, top], [x, bottom - 1]);
  }
  for (let y = top + 1; y < bottom - 1; y += 1) {
    pixels.push([left, y], [right - 1, y]);
  }
  return pixels;
}

for (const [name, analyzer] of [
  ["module", analyzeDifferentialCapture],
  [
    "serialized browser",
    Function(`"use strict";return (${analyzeDifferentialCapture.toString()})`)(),
  ],
]) {
  test(`${name} retains only target-local focus components`, () => {
    const local = rectangleRing(36, 36, 64, 64);
    const distant = [
      [2, 2],
      [3, 2],
      [2, 3],
      [3, 3],
    ];
    const fixture = focusFixture({ components: [local, distant] });
    const result = analyzer(fixture.candidate, fixture.capture);

    assert.equal(result.unresolved, undefined);
    assert.equal(result.mask.pixelCount, local.length);
    assert.deepEqual(result.mask.bounds, {
      left: 36,
      top: 36,
      right: 64,
      bottom: 64,
    });
    assert.deepEqual(
      result.focusIsolation.retained.map(({ pixelCount, bounds }) => ({
        pixelCount,
        bounds,
      })),
      [{ pixelCount: local.length, bounds: result.mask.bounds }],
    );
    assert.deepEqual(
      result.focusIsolation.discarded.map(({ pixelCount, bounds }) => ({
        pixelCount,
        bounds,
      })),
      [
        {
          pixelCount: distant.length,
          bounds: { left: 2, top: 2, right: 4, bottom: 4 },
        },
      ],
    );
  });
}

test("separate rounded-corner focus lobes attributable to one target are retained", () => {
  const lobes = [
    [
      [36, 36],
      [37, 36],
      [36, 37],
    ],
    [
      [62, 36],
      [63, 36],
      [63, 37],
    ],
    [
      [36, 62],
      [36, 63],
      [37, 63],
    ],
    [
      [63, 62],
      [62, 63],
      [63, 63],
    ],
  ];
  const fixture = focusFixture({ components: lobes });
  const result = analyzeDifferentialCapture(fixture.candidate, fixture.capture);

  assert.equal(result.unresolved, undefined);
  assert.equal(result.focusIsolation.retained.length, 4);
  assert.equal(result.focusIsolation.discarded.length, 0);
  assert.equal(result.mask.pixelCount, lobes.flat().length);
});

test("a component just beyond declared focus paint reach is explicit unresolved", () => {
  const local = rectangleRing(36, 36, 64, 64);
  const ambiguous = [
    [34, 48],
    [34, 49],
  ];
  const fixture = focusFixture({ components: [local, ambiguous] });
  const result = analyzeDifferentialCapture(fixture.candidate, fixture.capture);

  assert.equal(result.unresolved, "ambiguous-focus-component-attribution");
  assert.equal(result.focusIsolation.retained.length, 1);
  assert.deepEqual(result.focusIsolation.ambiguous, [
    {
      id: 1,
      pixelCount: 2,
      bounds: { left: 34, top: 48, right: 35, bottom: 50 },
      seedPixelCount: 0,
      ambiguityPixelCount: 2,
    },
  ]);
});

test("constellation-sized contamination remains far below the case budget", () => {
  const target = { left: 300, top: 720, right: 524, bottom: 792 };
  const local = rectangleRing(296, 716, 528, 796);
  const distant = [
    [36, 226],
    [823, 226],
    [36, 1717],
    [823, 1717],
  ];
  const fixture = focusFixture({
    width: 824,
    height: 1830,
    target,
    components: [local, ...distant.map((pixel) => [pixel])],
    work: { limit: 250_000_000, operations: 0 },
  });
  const analyzer = Function(
    `"use strict";return (${analyzeDifferentialCapture.toString()})`,
  )();
  const result = analyzer(fixture.candidate, fixture.capture);

  assert.equal(result.unresolved, undefined);
  assert.equal(result.capabilityFailure, undefined);
  assert.equal(result.mask.pixelCount, local.length);
  assert.equal(result.focusIsolation.discarded.length, 4);
  assert.equal(result.work.operations, 1_983_360);
  assert.ok(result.adjacency.region.right - result.adjacency.region.left < 240);
  assert.ok(result.adjacency.region.bottom - result.adjacency.region.top < 90);
});

test("pathological dimensions fail before any pixel read or analyzer allocation", () => {
  const width = 1_000_000;
  const height = 1_000_000;
  const unreadableData = new Proxy(
    { length: width * height * 4 },
    {
      get(target, property) {
        if (property === "length") return target.length;
        throw new Error(`unexpected pixel read: ${String(property)}`);
      },
    },
  );
  const fixture = {
    candidate: {
      id: "pathological focus",
      kind: "focus",
      geometry: { left: 10, top: 10, right: 20, bottom: 20 },
      raw: { paint: { paints: [{ source: "outline", width: 2, offset: 2 }] } },
    },
    capture: {
      coordinateSystem: "document-css-pixels",
      clip: { x: 0, y: 0, width, height, scale: 1 },
      scroll: { x: 0, y: 0 },
      work: { limit: 1_000_000, operations: 0 },
      images: Object.fromEntries(
        ["original", "hidden", "black", "white"].map((name) => [
          name,
          { width, height, data: unreadableData },
        ]),
      ),
    },
  };

  const result = analyzeDifferentialCapture(fixture.candidate, fixture.capture);
  assert.equal(result.unresolved, "analyzer-work-budget-exceeded");
  assert.equal(result.capabilityFailure.stage, "mask-scan");
  assert.equal(result.capabilityFailure.operations, 0);
});
