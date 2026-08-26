import assert from "node:assert/strict";
import test from "node:test";

import {
  applyRenderedSamples,
  composePaintGroups,
  extractPaintStack,
  selectFocusIndicator,
  stabilizePagePaint,
} from "./geometry.mjs";

test("group opacity composites foreground and own background as one surface", () => {
  const result = composePaintGroups("rgb(255, 255, 255)", [
    { color: "rgb(0, 0, 0)", image: "none", opacity: 0.5 },
    { color: "rgb(0, 0, 255)", image: "none", opacity: 1 },
  ]);
  assert.deepEqual(result.foreground, {
    red: 127.5,
    green: 127.5,
    blue: 255,
    alpha: 1,
  });
  assert.deepEqual(result.background, {
    red: 0,
    green: 0,
    blue: 127.5,
    alpha: 1,
  });
});

test("paint stabilization awaits finite animations and paint frames", async () => {
  let resolveFinite;
  const finite = {
    currentTime: 0,
    effect: {
      getComputedTiming: () => ({ endTime: 120 }),
      getTiming: () => ({ iterations: 1 }),
    },
    finished: new Promise((resolve) => {
      resolveFinite = resolve;
    }),
    playState: "running",
  };
  let frames = 0;
  let settled = false;
  const pending = stabilizePagePaint(
    {
      fonts: { ready: Promise.resolve() },
      getAnimations: () => [finite],
    },
    () => {
      frames += 1;
      return Promise.resolve();
    },
  ).then((value) => {
    settled = true;
    return value;
  });
  await Promise.resolve();
  assert.equal(settled, false);
  resolveFinite();
  const evidence = await pending;
  assert.deepEqual(evidence, {
    finiteAwaited: 1,
    finiteForced: [],
    infiniteStabilized: [],
    capabilityFailures: [],
  });
  assert.equal(frames, 2);
});

test("infinite animation selects and records a visible representative phase", async () => {
  const target = { id: "voice-control", tagName: "BUTTON" };
  const animation = {
    animationName: "voice-pulse",
    currentTime: 20,
    effect: {
      target,
      getComputedTiming: () => ({ endTime: Infinity }),
      getTiming: () => ({ duration: 100, iterations: Infinity }),
    },
    pause() {},
    playState: "running",
  };
  const snapshot = (element) => ({
    element: element.id,
    visible: animation.currentTime >= 50,
    opacity: animation.currentTime >= 50 ? 1 : 0,
    rect:
      animation.currentTime >= 50
        ? { left: 5, top: 6, right: 49, bottom: 50, width: 44, height: 44 }
        : { left: 5, top: 6, right: 5, bottom: 6, width: 0, height: 0 },
  });
  const evidence = await stabilizePagePaint(
    { fonts: { ready: Promise.resolve() }, getAnimations: () => [animation] },
    () => Promise.resolve(),
    snapshot,
  );
  assert.equal(animation.currentTime, 50);
  assert.deepEqual(evidence.infiniteStabilized[0], {
    identity: "voice-pulse",
    affectedElement: "voice-control",
    before: {
      element: "voice-control",
      visible: false,
      opacity: 0,
      rect: { left: 5, top: 6, right: 5, bottom: 6, width: 0, height: 0 },
    },
    after: {
      element: "voice-control",
      visible: true,
      opacity: 1,
      rect: { left: 5, top: 6, right: 49, bottom: 50, width: 44, height: 44 },
    },
    chosenTime: 50,
  });
});

test("paused finite animation is forced terminal without awaiting finished", async () => {
  const target = { id: "route-panel", tagName: "SECTION" };
  const animation = {
    animationName: "route-enter",
    currentTime: 10,
    playbackRate: 0,
    playState: "paused",
    effect: {
      target,
      getComputedTiming: () => ({ endTime: 120 }),
      getTiming: () => ({ duration: 120, iterations: 1 }),
    },
    finished: new Promise(() => {}),
    pause() {},
  };
  const evidence = await stabilizePagePaint(
    { fonts: { ready: Promise.resolve() }, getAnimations: () => [animation] },
    () => Promise.resolve(),
    () => ({
      visible: true,
      opacity: 1,
      rect: {
        left: 0,
        top: 0,
        right: 100,
        bottom: 100,
        width: 100,
        height: 100,
      },
    }),
  );
  assert.equal(animation.currentTime, 120);
  assert.equal(evidence.finiteAwaited, 0);
  assert.equal(evidence.finiteForced[0].chosenTime, 120);
});

test("DOM paint extraction retains candidate and ancestor group opacity", () => {
  const root = { id: "root", parentElement: null };
  const parent = { id: "parent", parentElement: root };
  const candidate = { id: "candidate", parentElement: parent };
  const styles = new Map([
    [
      candidate,
      {
        backgroundColor: "rgb(1, 2, 3)",
        backgroundImage: "none",
        opacity: "0.5",
      },
    ],
    [
      parent,
      {
        backgroundColor: "rgba(4, 5, 6, 0.5)",
        backgroundImage: "none",
        opacity: "0.75",
      },
    ],
    [
      root,
      {
        backgroundColor: "rgb(7, 8, 9)",
        backgroundImage: "url(local.png)",
        opacity: "1",
      },
    ],
  ]);
  const stack = extractPaintStack(candidate, (element, pseudo) => {
    if (pseudo)
      return {
        content: "none",
        backgroundColor: "transparent",
        backgroundImage: "none",
      };
    return styles.get(element);
  });
  assert.deepEqual(stack.layers, [
    { color: "rgb(1, 2, 3)", image: "none", opacity: 0.5, owner: "candidate" },
    {
      color: "rgba(4, 5, 6, 0.5)",
      image: "none",
      opacity: 0.75,
      owner: "parent",
    },
    {
      color: "rgb(7, 8, 9)",
      image: "url(local.png)",
      opacity: 1,
      owner: "root",
    },
  ]);
  assert.equal(stack.candidateOpacity, 0.5);
  assert.deepEqual(stack.ancestorOpacities, [0.75, 1]);
});

test("DOM paint extraction records pseudo paint ownership", () => {
  const candidate = { id: "candidate", parentElement: null };
  const stack = extractPaintStack(candidate, (_element, pseudo) =>
    pseudo === "::before"
      ? {
          content: '""',
          backgroundColor: "rgb(1, 2, 3)",
          backgroundImage: "linear-gradient(red, blue)",
        }
      : pseudo === "::after"
        ? {
            content: "none",
            backgroundColor: "transparent",
            backgroundImage: "none",
          }
        : {
            backgroundColor: "transparent",
            backgroundImage: "none",
            opacity: "1",
          },
  );
  assert.deepEqual(stack.pseudoPaint, [
    {
      owner: "candidate",
      pseudo: "::before",
      color: "rgb(1, 2, 3)",
      image: "linear-gradient(red, blue)",
    },
  ]);
});

test("focus extraction distinguishes changed outline and multiple shadows", () => {
  assert.deepEqual(
    selectFocusIndicator(
      {
        outlineStyle: "none",
        outlineWidth: "0px",
        outlineColor: "rgb(0, 0, 0)",
        outlineOffset: "0px",
        boxShadow: "none",
      },
      {
        outlineStyle: "solid",
        outlineWidth: "2px",
        outlineColor: "rgb(10, 20, 30)",
        outlineOffset: "3px",
        boxShadow: "rgb(1, 2, 3) 0 0 0 1px, rgba(4, 5, 6, 0.5) 0 0 0 3px",
      },
    ),
    {
      changed: true,
      paints: [
        { source: "outline", color: "rgb(10, 20, 30)", width: 2, offset: 3 },
        { source: "box-shadow", color: "rgb(1, 2, 3)" },
        { source: "box-shadow", color: "rgba(4, 5, 6, 0.5)" },
      ],
    },
  );
});

test("rendered sampling resolves image paint and checks both focus surfaces", () => {
  const pair = applyRenderedSamples(
    {
      id: "image-backed focus",
      kind: "focus",
      minimum: 3,
      raw: {
        foreground: "rgb(255, 255, 255)",
        candidateOpacity: 1,
        ancestorOpacities: [1],
        backgrounds: [
          { color: "transparent", image: "url(local.png)", opacity: 1 },
        ],
      },
      unsupported: "background-image",
    },
    {
      inside: [{ red: 0, green: 0, blue: 0, alpha: 1 }],
      outside: [{ red: 90, green: 90, blue: 90, alpha: 1 }],
      points: { inside: [[10, 10]], outside: [[8, 8]] },
    },
  );
  assert.equal(pair.unsupported, undefined);
  assert.equal(pair.sampling.method, "rendered-adjacent-pixels");
  assert.equal(pair.sampledRatios.length, 2);
  assert.equal(pair.ratio, Math.min(...pair.sampledRatios));
  assert.deepEqual(pair.raw.renderedSamples.points, {
    inside: [[10, 10]],
    outside: [[8, 8]],
  });
});

test("rendered fallback refuses non-unit group opacity instead of double-compositing", () => {
  const pair = applyRenderedSamples(
    {
      id: "faded-image-text",
      kind: "text",
      minimum: 4.5,
      raw: {
        foreground: "rgb(255, 255, 255)",
        candidateOpacity: 0.5,
        ancestorOpacities: [1],
        backgrounds: [
          { color: "transparent", image: "url(local.png)", opacity: 0.5 },
        ],
      },
      unsupported: "background-image",
    },
    { inside: [{ red: 0, green: 0, blue: 0, alpha: 1 }], outside: [] },
  );
  assert.equal(pair.unsupported, "rendered-group-opacity");
  assert.equal(pair.ratio, undefined);
});
