import assert from "node:assert/strict";
import test from "node:test";

import { assertGeometry } from "./geometry.mjs";

const validMeasurements = {
  viewport: { width: 393, height: 852 },
  document: { scrollWidth: 393, clientWidth: 393 },
  scrollOwners: [{ id: "sessions-scroll", overflowY: "auto" }],
  controls: [
    {
      id: "switch-concept",
      width: 44,
      height: 44,
      platform: "ios",
    },
  ],
  fixedBottom: [{ id: "primary-nav", bottom: 818, keyboardTop: 852 }],
  duplicateIds: [],
  clippedPrimary: [],
  safeAreas: [
    {
      id: "app-shell",
      ownerCount: 1,
      top: 59,
      bottom: 34,
      expectedTop: 59,
      expectedBottom: 34,
    },
  ],
  focusOrder: [
    { id: "switch-concept", order: 1, visible: true },
    { id: "lab-controls", order: 2, visible: true },
    { id: "session-mobile-release", order: 3, visible: true },
  ],
  contrastPairs: [
    { id: "session-title", kind: "text", ratio: 7.1, minimum: 4.5 },
    { id: "focus-outline", kind: "nontext", ratio: 3.2, minimum: 3 },
  ],
};

const definition = { route: "sessions" };
const clone = () => structuredClone(validMeasurements);
const codes = (measurements) =>
  assertGeometry(measurements, definition).map(({ code }) => code);

function requireViolation(measurements, code, subject) {
  assert.deepEqual(assertGeometry(measurements, definition), [
    { code, subject },
  ]);
}

test("accepts a valid measurement set", () => {
  assert.deepEqual(assertGeometry(clone(), definition), []);
});

test("reports horizontal document overflow", () => {
  const measurements = clone();
  measurements.document.scrollWidth = 394;
  requireViolation(measurements, "horizontal-overflow", "document");
});

test("requires exactly one primary scroll owner", async (t) => {
  await t.test("zero", () => {
    const measurements = clone();
    measurements.scrollOwners = [];
    requireViolation(measurements, "scroll-owner-count", "sessions");
  });
  await t.test("multiple", () => {
    const measurements = clone();
    measurements.scrollOwners.push({ id: "nested", overflowY: "scroll" });
    requireViolation(measurements, "scroll-owner-count", "sessions");
  });
});

test("enforces independent literal iOS 44-pixel targets", async (t) => {
  for (const [dimension, width, height] of [
    ["width", 43.99, 44],
    ["height", 44, 43.99],
  ]) {
    await t.test(dimension, () => {
      const measurements = clone();
      measurements.controls = [
        { id: `ios-${dimension}`, width, height, platform: "ios" },
      ];
      requireViolation(measurements, "undersized-control", `ios-${dimension}`);
    });
  }
});

test("enforces independent literal Android 48-pixel targets", async (t) => {
  for (const [dimension, width, height] of [
    ["width", 47.99, 48],
    ["height", 48, 47.99],
  ]) {
    await t.test(dimension, () => {
      const measurements = clone();
      measurements.controls = [
        { id: `android-${dimension}`, width, height, platform: "android" },
      ];
      requireViolation(
        measurements,
        "undersized-control",
        `android-${dimension}`,
      );
    });
  }
});

test("allows targets exactly at each platform minimum", () => {
  const measurements = clone();
  measurements.controls = [
    { id: "ios-exact", width: 44, height: 44, platform: "ios" },
    { id: "android-exact", width: 48, height: 48, platform: "android" },
  ];
  assert.deepEqual(assertGeometry(measurements, definition), []);
});

test("reports controls occluded by the keyboard viewport", () => {
  const measurements = clone();
  measurements.fixedBottom = [
    { id: "composer", bottom: 612.5, keyboardTop: 612 },
  ];
  requireViolation(measurements, "keyboard-occlusion", "composer");
});

test("reports every duplicate id", () => {
  const measurements = clone();
  measurements.duplicateIds = ["answer-note", "answer-note"];
  assert.deepEqual(assertGeometry(measurements, definition), [
    { code: "duplicate-id", subject: "answer-note" },
    { code: "duplicate-id", subject: "answer-note" },
  ]);
});

test("reports clipped primary actions", () => {
  const measurements = clone();
  measurements.clippedPrimary = ["start-session"];
  requireViolation(measurements, "clipped-primary", "start-session");
});

test("reports missing, duplicate, and mismatched safe-area ownership", async (t) => {
  for (const [name, patch] of [
    ["missing owner", { ownerCount: 0 }],
    ["duplicate owner", { ownerCount: 2 }],
    ["top mismatch", { top: 0 }],
    ["bottom mismatch", { bottom: 0 }],
  ]) {
    await t.test(name, () => {
      const measurements = clone();
      Object.assign(measurements.safeAreas[0], patch);
      requireViolation(measurements, "safe-area-ownership", "app-shell");
    });
  }
});

test("reports out-of-order actual keyboard focus", () => {
  const measurements = clone();
  measurements.focusOrder[1].order = 3;
  requireViolation(measurements, "focus-order-or-visibility", "lab-controls");
});

test("reports keyboard focus without a visible focus indicator", () => {
  const measurements = clone();
  measurements.focusOrder[2].visible = false;
  requireViolation(
    measurements,
    "focus-order-or-visibility",
    "session-mobile-release",
  );
});

test("reports insufficient text contrast", () => {
  const measurements = clone();
  measurements.contrastPairs[0].ratio = 4.49;
  requireViolation(measurements, "insufficient-contrast", "session-title");
});

test("reports insufficient non-text contrast independently", () => {
  const measurements = clone();
  measurements.contrastPairs[1].ratio = 2.99;
  requireViolation(measurements, "insufficient-contrast", "focus-outline");
});

test("returns every violation deterministically rather than stopping early", () => {
  const measurements = clone();
  measurements.document.scrollWidth = 500;
  measurements.scrollOwners = [];
  measurements.controls = [
    { id: "tiny", width: 1, height: 1, platform: "android" },
  ];
  measurements.fixedBottom = [
    { id: "composer", bottom: 701, keyboardTop: 700 },
  ];
  measurements.duplicateIds = ["duplicate"];
  measurements.clippedPrimary = ["submit"];
  measurements.safeAreas[0].ownerCount = 0;
  measurements.focusOrder[0].visible = false;
  measurements.contrastPairs[0].ratio = 1;

  assert.deepEqual(assertGeometry(measurements, definition), [
    { code: "clipped-primary", subject: "submit" },
    { code: "duplicate-id", subject: "duplicate" },
    { code: "focus-order-or-visibility", subject: "switch-concept" },
    { code: "horizontal-overflow", subject: "document" },
    { code: "insufficient-contrast", subject: "session-title" },
    { code: "keyboard-occlusion", subject: "composer" },
    { code: "safe-area-ownership", subject: "app-shell" },
    { code: "scroll-owner-count", subject: "sessions" },
    { code: "undersized-control", subject: "tiny" },
  ]);
  assert.deepEqual(codes(measurements), [
    "clipped-primary",
    "duplicate-id",
    "focus-order-or-visibility",
    "horizontal-overflow",
    "insufficient-contrast",
    "keyboard-occlusion",
    "safe-area-ownership",
    "scroll-owner-count",
    "undersized-control",
  ]);
});
