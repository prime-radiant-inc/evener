import assert from "node:assert/strict";
import test from "node:test";

import {
  assertGeometry,
  buildContrastMeasurements,
  buildSafeAreaMeasurements,
  classifyGeometrySnapshot,
  compositeColor,
  normalizeCssPixel,
  parseCssColor,
} from "./geometry.mjs";

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
  requireViolation(
    measurements,
    "insufficient-nontext-contrast",
    "focus-outline",
  );
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

test("collector reads independent safe-area values and marked owner edges", () => {
  assert.deepEqual(
    buildSafeAreaMeasurements({
      root: { top: "59px", right: "2px", bottom: "34px", left: "3px" },
      owners: [
        { id: "top", edges: { top: "59px" } },
        { id: "sides", edges: { right: "2px", left: "3px" } },
        { id: "bottom", edges: { bottom: "34px" } },
      ],
    }),
    [
      {
        edge: "bottom",
        rootValue: 34,
        owners: [{ id: "bottom", computedValue: 34 }],
      },
      {
        edge: "left",
        rootValue: 3,
        owners: [{ id: "sides", computedValue: 3 }],
      },
      {
        edge: "right",
        rootValue: 2,
        owners: [{ id: "sides", computedValue: 2 }],
      },
      {
        edge: "top",
        rootValue: 59,
        owners: [{ id: "top", computedValue: 59 }],
      },
    ],
  );
  assert.deepEqual(
    buildSafeAreaMeasurements({
      root: { top: "0px", right: "0px", bottom: "0px", left: "0px" },
      owners: [],
    }).map(({ edge, owners }) => ({ edge, ownerCount: owners.length })),
    [
      { edge: "bottom", ownerCount: 0 },
      { edge: "left", ownerCount: 0 },
      { edge: "right", ownerCount: 0 },
      { edge: "top", ownerCount: 0 },
    ],
  );
});

test("collector parses RGBA and composites every translucent paint layer", () => {
  assert.deepEqual(parseCssColor("rgba(255, 0, 0, 0.5)"), {
    red: 255,
    green: 0,
    blue: 0,
    alpha: 0.5,
  });
  assert.deepEqual(parseCssColor("color(srgb 0.5 0.25 1 / 0.75)"), {
    red: 127.5,
    green: 63.75,
    blue: 255,
    alpha: 0.75,
  });
  assert.deepEqual(
    compositeColor(
      { red: 255, green: 255, blue: 255, alpha: 0.5 },
      { red: 0, green: 0, blue: 255, alpha: 1 },
    ),
    { red: 127.5, green: 127.5, blue: 255, alpha: 1 },
  );
  const [pair] = buildContrastMeasurements([
    {
      id: "alpha-text",
      kind: "text",
      foreground: "rgba(255, 255, 255, 0.5)",
      opacity: 1,
      backgrounds: [
        { color: "rgba(255, 0, 0, 0.5)", image: "none", opacity: 1 },
        { color: "rgb(0, 0, 255)", image: "none", opacity: 1 },
      ],
      minimum: 4.5,
    },
  ]);
  assert.equal(pair.kind, "text");
  assert.deepEqual(pair.raw.foreground, "rgba(255, 255, 255, 0.5)");
  assert.deepEqual(pair.effectiveBackground, {
    red: 127.5,
    green: 0,
    blue: 127.5,
    alpha: 1,
  });
  assert.deepEqual(pair.effectiveForeground, {
    red: 191.25,
    green: 127.5,
    blue: 191.25,
    alpha: 1,
  });
});

test("collector emits distinct non-text and changed-focus contrast pairs", () => {
  const pairs = buildContrastMeasurements([
    {
      id: "control",
      kind: "nontext",
      foreground: "rgb(80, 80, 80)",
      backgrounds: [{ color: "rgb(255, 255, 255)", image: "none", opacity: 1 }],
      minimum: 3,
    },
    {
      id: "focus",
      kind: "focus",
      foreground: "rgb(0, 95, 204)",
      backgrounds: [{ color: "rgb(255, 255, 255)", image: "none", opacity: 1 }],
      minimum: 3,
      changed: true,
    },
    {
      id: "decorative-shadow",
      kind: "focus",
      foreground: "rgb(0, 0, 0)",
      backgrounds: [{ color: "rgb(255, 255, 255)", image: "none", opacity: 1 }],
      minimum: 3,
      changed: false,
    },
    {
      id: "gradient",
      kind: "text",
      foreground: "rgb(0, 0, 0)",
      backgrounds: [
        {
          color: "transparent",
          image: "linear-gradient(red, blue)",
          opacity: 1,
        },
      ],
      minimum: 4.5,
    },
  ]);
  assert.deepEqual(
    pairs.map(({ id, kind, changed, unsupported }) => ({
      id,
      kind,
      changed,
      unsupported,
    })),
    [
      {
        id: "control",
        kind: "nontext",
        changed: undefined,
        unsupported: undefined,
      },
      { id: "focus", kind: "focus", changed: true, unsupported: undefined },
      {
        id: "decorative-shadow",
        kind: "focus",
        changed: false,
        unsupported: "unchanged-focus-indicator",
      },
      {
        id: "gradient",
        kind: "text",
        changed: undefined,
        unsupported: "background-image",
      },
    ],
  );
});

test("collector normalizes raster noise but preserves meaningful underages", () => {
  assert.equal(normalizeCssPixel(43.99994, 2), 44);
  assert.equal(normalizeCssPixel(47.99997, 2), 48);
  assert.equal(normalizeCssPixel(43.99, 2), 43.99);
  assert.equal(normalizeCssPixel(47.99, 2), 47.99);
});

test("collector retains nested scrollers and computes target reachability", () => {
  const result = classifyGeometrySnapshot({
    devicePixelRatio: 2,
    controls: [
      { id: "target", width: 43.99994, height: 43.99994, platform: "ios" },
    ],
    scrollers: [
      {
        id: "primary",
        primary: true,
        overflowX: "hidden",
        overflowY: "auto",
        scrollWidth: 393,
        clientWidth: 393,
      },
      {
        id: "nested",
        primary: false,
        overflowX: "auto",
        overflowY: "auto",
        scrollWidth: 500,
        clientWidth: 300,
      },
    ],
    primaryActions: [
      {
        id: "reachable",
        rect: { left: 10, right: 100, top: 900, bottom: 944 },
        scroller: {
          top: 0,
          bottom: 800,
          scrollTop: 0,
          scrollHeight: 1000,
          clientHeight: 800,
        },
      },
      {
        id: "clipped",
        rect: { left: -10, right: 100, top: 10, bottom: 54 },
        scroller: null,
      },
    ],
    viewport: { width: 393, height: 852 },
  });
  assert.deepEqual(result.controls[0], {
    id: "target",
    width: 44,
    height: 44,
    platform: "ios",
  });
  assert.deepEqual(
    result.scrollOwners.map(({ id }) => id),
    ["primary"],
  );
  assert.deepEqual(
    result.nestedScrollers.map(({ id }) => id),
    ["nested"],
  );
  assert.deepEqual(result.clippedPrimary, ["clipped"]);
});

test("oracle distinguishes focus contrast and unchanged indicators", () => {
  const low = clone();
  low.contrastPairs = [
    { id: "focus-low", kind: "focus", ratio: 2.99, minimum: 3 },
  ];
  requireViolation(low, "insufficient-focus-contrast", "focus-low");

  const unchanged = clone();
  unchanged.contrastPairs = [
    {
      id: "focus-static-shadow",
      kind: "focus",
      minimum: 3,
      unsupported: "unchanged-focus-indicator",
    },
  ];
  requireViolation(unchanged, "missing-focus-indicator", "focus-static-shadow");
});

test("oracle never hardcodes zero safe-area ownership green", () => {
  const measurements = clone();
  measurements.safeAreas = buildSafeAreaMeasurements({
    root: { top: "0px", right: "0px", bottom: "0px", left: "0px" },
    owners: [],
  });
  assert.deepEqual(
    assertGeometry(measurements, {
      ...definition,
      safeArea: { top: 0, right: 0, bottom: 0, left: 0 },
    }),
    [
      { code: "safe-area-ownership", subject: "safe-area-bottom" },
      { code: "safe-area-ownership", subject: "safe-area-left" },
      { code: "safe-area-ownership", subject: "safe-area-right" },
      { code: "safe-area-ownership", subject: "safe-area-top" },
    ],
  );
});
