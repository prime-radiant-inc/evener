import assert from "node:assert/strict";
import test from "node:test";

import {
  analyzeDifferentialCapture,
  applyRenderedSamples,
  assertGeometry,
} from "./geometry.mjs";
import { renderedContrastCapabilityFailures } from "../concept-browser.mjs";

const rgba = (red, green, blue, alpha = 255) => [red, green, blue, alpha];

function image(width, height, color) {
  const data = new Uint8ClampedArray(width * height * 4);
  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      data.set(color(x, y), (y * width + x) * 4);
    }
  }
  return { width, height, data };
}

function denseFocusFixture(size, work, lowCoverage = false) {
  const candidateStart = Math.floor(size * 0.25);
  const candidateEnd = Math.ceil(size * 0.75);
  const outerStart = Math.floor(size * 0.15);
  const outerEnd = Math.ceil(size * 0.85);
  const innerStart = Math.floor(size * 0.35);
  const innerEnd = Math.ceil(size * 0.65);
  const inCandidate = (x, y) =>
    x >= candidateStart &&
    x < candidateEnd &&
    y >= candidateStart &&
    y < candidateEnd;
  const inMask = (x, y) =>
    x >= outerStart &&
    x < outerEnd &&
    y >= outerStart &&
    y < outerEnd &&
    !(x >= innerStart && x < innerEnd && y >= innerStart && y < innerEnd);
  const background = (x, y) =>
    inCandidate(x, y) ? rgba(245, 245, 245) : rgba(20, 20, 20);
  const lowCoveragePixel = (x, y) =>
    lowCoverage && x === outerStart && y === outerStart;
  const probe = (paint, lowPaint = paint) => (x, y) =>
    inMask(x, y)
      ? lowCoveragePixel(x, y)
        ? lowPaint
        : paint
      : background(x, y);
  return {
    candidate: {
      id: `dense-focus-${size}`,
      kind: "focus",
      minimum: 3,
      geometry: {
        left: candidateStart,
        top: candidateStart,
        right: candidateEnd,
        bottom: candidateEnd,
        width: candidateEnd - candidateStart,
        height: candidateEnd - candidateStart,
      },
    },
    capture: {
      coordinateSystem: "document-css-pixels",
      clip: { x: 0, y: 0, width: size, height: size, scale: 1 },
      scroll: { x: 0, y: 0 },
      devicePixelRatio: 1,
      work,
      images: {
        original: image(
          size,
          size,
          probe(rgba(255, 255, 255), rgba(8, 8, 8)),
        ),
        hidden: image(size, size, background),
        black: image(size, size, probe(rgba(0, 0, 0))),
        white: image(
          size,
          size,
          probe(rgba(255, 255, 255), rgba(8, 8, 8)),
        ),
      },
    },
  };
}

for (const [name, analyzer] of [
  ["module", analyzeDifferentialCapture],
  [
    "serialized browser",
    Function(`"use strict";return (${analyzeDifferentialCapture.toString()})`)(),
  ],
]) {
  test(`${name} focus analyzer is bounded and spatially pairs each surface`, () => {
    const fixture = denseFocusFixture(80);
    const result = analyzer(fixture.candidate, fixture.capture);
    assert.equal(result.unresolved, undefined);
    assert.equal(result.capabilityFailure, undefined);
    assert.equal(result.work.limit, 25_000_000);
    assert.ok(result.work.operations > 0);
    assert.ok(result.work.operations < 500_000);
    assert.equal(
      result.surfaceVerdicts.inside.sampleCount,
      result.mask.reliablePixelCount,
    );
    assert.equal(
      result.surfaceVerdicts.outside.sampleCount,
      result.mask.reliablePixelCount,
    );
    assert.equal(result.adjacency.method, "exact-euclidean-distance-map");
    assert.equal(
      result.adjacency.matchedPixelCount,
      result.mask.reliablePixelCount,
    );

    const inside = result.surfaceVerdicts.inside.minimumEvidence;
    const outside = result.surfaceVerdicts.outside.minimumEvidence;
    for (const evidence of [inside, outside]) {
      assert.ok(evidence.coordinate.document);
      assert.ok(evidence.backgroundCoordinate.document);
      assert.ok(Number.isFinite(evidence.adjacentDistanceSquared));
    }
    const box = fixture.candidate.geometry;
    assert.ok(
      inside.backgroundCoordinate.document.x >= box.left &&
        inside.backgroundCoordinate.document.x < box.right &&
        inside.backgroundCoordinate.document.y >= box.top &&
        inside.backgroundCoordinate.document.y < box.bottom,
    );
    assert.equal(
      outside.backgroundCoordinate.document.x >= box.left &&
        outside.backgroundCoordinate.document.x < box.right &&
        outside.backgroundCoordinate.document.y >= box.top &&
        outside.backgroundCoordinate.document.y < box.bottom,
      false,
    );
  });
}

test("focus analyzer work scales near-linearly with screenshot area", () => {
  const smallFixture = denseFocusFixture(40);
  const largeFixture = denseFocusFixture(80);
  const small = analyzeDifferentialCapture(
    smallFixture.candidate,
    smallFixture.capture,
  );
  const large = analyzeDifferentialCapture(
    largeFixture.candidate,
    largeFixture.capture,
  );
  assert.ok(large.work.operations > small.work.operations);
  assert.ok(large.work.operations <= small.work.operations * 4.5);
});

test("low-coverage focus mask pixels retain their own matched surfaces", () => {
  const fixture = denseFocusFixture(40, undefined, true);
  const result = analyzeDifferentialCapture(
    fixture.candidate,
    fixture.capture,
  );
  assert.ok(result.mask.reliablePixelCount < result.mask.pixelCount);
  assert.equal(result.adjacency.matchedPixelCount, result.mask.pixelCount);
  assert.equal(
    result.surfaceVerdicts.inside.sampleCount,
    result.mask.pixelCount,
  );
  assert.equal(
    result.surfaceVerdicts.outside.sampleCount,
    result.mask.pixelCount,
  );
});

test("pathological analyzer work becomes explicit gate-RED capability evidence", () => {
  const fixture = denseFocusFixture(40, { limit: 100, operations: 0 });
  const analyzer = Function(
    `"use strict";return (${analyzeDifferentialCapture.toString()})`,
  )();
  const evidence = analyzer(fixture.candidate, fixture.capture);
  assert.equal(evidence.unresolved, "analyzer-work-budget-exceeded");
  assert.deepEqual(evidence.capabilityFailure, {
    code: "contrast-analyzer-work-budget-exceeded",
    candidateId: "dense-focus-40",
    kind: "focus",
    stage: "mask-scan",
    operations: 0,
    requested: 1_600,
    limit: 100,
  });
  const pair = applyRenderedSamples(
    {
      id: fixture.candidate.id,
      kind: "focus",
      minimum: 3,
      raw: { geometry: fixture.candidate.geometry },
    },
    evidence,
  );
  assert.equal(pair.unsupported, "analyzer-work-budget-exceeded");
  const violations = assertGeometry(
    {
      document: { scrollWidth: 1, clientWidth: 1 },
      scrollOwners: [{}],
      controls: [],
      fixedBottom: [],
      duplicateIds: [],
      clippedPrimary: [],
      safeAreas: [],
      focusOrder: [],
      contrastPairs: [pair],
    },
    { route: "fixture" },
  );
  assert.deepEqual(violations, [
    { code: "unmeasurable-contrast", subject: "dense-focus-40" },
  ]);
  assert.deepEqual(renderedContrastCapabilityFailures([pair]), [
    evidence.capabilityFailure,
  ]);
});

test("serialized candidates share one deterministic case work budget", () => {
  const analyzer = Function(
    `"use strict";return (${analyzeDifferentialCapture.toString()})`,
  )();
  const probeFixture = denseFocusFixture(40);
  const probe = analyzer(probeFixture.candidate, probeFixture.capture);
  const work = { limit: probe.work.operations + 100, operations: 0 };
  const firstFixture = denseFocusFixture(40, work);
  const first = analyzer(firstFixture.candidate, firstFixture.capture);
  assert.equal(first.capabilityFailure, undefined);
  const secondFixture = denseFocusFixture(40, work);
  secondFixture.candidate.id = "second-focus-candidate";
  const second = analyzer(secondFixture.candidate, secondFixture.capture);
  assert.equal(second.unresolved, "analyzer-work-budget-exceeded");
  assert.equal(second.capabilityFailure.candidateId, "second-focus-candidate");
  assert.equal(second.capabilityFailure.stage, "mask-scan");
  assert.equal(second.capabilityFailure.operations, probe.work.operations);
  assert.equal(work.operations, probe.work.operations);
});

test("serialized analyzer has no focus Cartesian-product verdict", () => {
  const source = analyzeDifferentialCapture.toString();
  assert.doesNotMatch(
    source,
    /for\s*\(const foregroundPixel[\s\S]*for\s*\(const backgroundEntry/,
  );
});
