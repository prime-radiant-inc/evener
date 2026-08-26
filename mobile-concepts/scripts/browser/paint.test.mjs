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

test("paint stabilization awaits finite animations and freezes infinite ones", async () => {
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
  const infinite = {
    currentTime: 20,
    effect: {
      getComputedTiming: () => ({ endTime: Infinity }),
      getTiming: () => ({ iterations: Infinity }),
    },
    finished: new Promise(() => {}),
    pauseCalled: false,
    pause() {
      this.pauseCalled = true;
    },
    playState: "running",
  };
  let frames = 0;
  let settled = false;
  const pending = stabilizePagePaint(
    {
      fonts: { ready: Promise.resolve() },
      getAnimations: () => [finite, infinite],
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
  assert.equal(infinite.pauseCalled, true);
  assert.equal(infinite.currentTime, 0);
  assert.equal(settled, false);
  resolveFinite();
  const evidence = await pending;
  assert.deepEqual(evidence, { finiteAwaited: 1, infiniteFrozen: 1 });
  assert.equal(frames, 2);
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
