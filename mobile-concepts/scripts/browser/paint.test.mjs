import assert from "node:assert/strict";
import test from "node:test";

import {
  applyRenderedSamples,
  composePaintGroups,
  extractPaintStack,
  mergeStabilizationEvidence,
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
    viewportIntersection:
      animation.currentTime >= 50
        ? { left: 5, top: 6, right: 49, bottom: 50, width: 44, height: 44 }
        : { left: 5, top: 6, right: 5, bottom: 6, width: 0, height: 0 },
    clipIntersection:
      animation.currentTime >= 50
        ? { left: 5, top: 6, right: 49, bottom: 50, width: 44, height: 44 }
        : { left: 5, top: 6, right: 5, bottom: 6, width: 0, height: 0 },
    intersection:
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
      viewportIntersection: {
        left: 5,
        top: 6,
        right: 5,
        bottom: 6,
        width: 0,
        height: 0,
      },
      clipIntersection: {
        left: 5,
        top: 6,
        right: 5,
        bottom: 6,
        width: 0,
        height: 0,
      },
      intersection: {
        left: 5,
        top: 6,
        right: 5,
        bottom: 6,
        width: 0,
        height: 0,
      },
    },
    after: {
      element: "voice-control",
      visible: true,
      opacity: 1,
      rect: { left: 5, top: 6, right: 49, bottom: 50, width: 44, height: 44 },
      viewportIntersection: {
        left: 5,
        top: 6,
        right: 49,
        bottom: 50,
        width: 44,
        height: 44,
      },
      clipIntersection: {
        left: 5,
        top: 6,
        right: 49,
        bottom: 50,
        width: 44,
        height: 44,
      },
      intersection: {
        left: 5,
        top: 6,
        right: 49,
        bottom: 50,
        width: 44,
        height: 44,
      },
    },
    chosenTime: 50,
  });
});

test("all invisible infinite phases become an explicit capability failure", async () => {
  const target = { id: "hidden-control", tagName: "BUTTON" };
  const animation = {
    animationName: "hidden-loop",
    currentTime: 10,
    effect: {
      target,
      getComputedTiming: () => ({ endTime: Infinity }),
      getTiming: () => ({ duration: 100, iterations: Infinity }),
    },
    pause() {},
  };
  const invisible = () => ({
    element: "hidden-control",
    visible: false,
    opacity: 0,
    rect: { left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0 },
    viewportIntersection: {
      left: 0,
      top: 0,
      right: 0,
      bottom: 0,
      width: 0,
      height: 0,
    },
    clipIntersection: {
      left: 0,
      top: 0,
      right: 0,
      bottom: 0,
      width: 0,
      height: 0,
    },
    intersection: { left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0 },
  });
  const evidence = await stabilizePagePaint(
    { fonts: { ready: Promise.resolve() }, getAnimations: () => [animation] },
    () => Promise.resolve(),
    invisible,
  );
  assert.deepEqual(evidence.infiniteStabilized, []);
  assert.deepEqual(evidence.capabilityFailures, [
    {
      code: "infinite-animation-no-visible-representative",
      identity: "hidden-loop",
      affectedElement: "hidden-control",
      before: invisible(),
      sampledPhases: [10, 0, 25, 50, 75],
    },
  ]);
});

test("offscreen and ancestor-clipped infinite phases cannot win", async () => {
  const target = { id: "moving-control", tagName: "BUTTON" };
  const animation = {
    animationName: "moving-loop",
    currentTime: 0,
    effect: {
      target,
      getComputedTiming: () => ({ endTime: Infinity }),
      getTiming: () => ({ duration: 100, iterations: Infinity }),
    },
    pause() {},
  };
  const snapshot = () => {
    const time = animation.currentTime;
    const visible = time === 50;
    return {
      element: "moving-control",
      visible,
      opacity: 1,
      rect:
        time === 0
          ? { left: 500, top: 0, right: 544, bottom: 44, width: 44, height: 44 }
          : { left: 10, top: 10, right: 54, bottom: 54, width: 44, height: 44 },
      viewportIntersection:
        time === 0
          ? { left: 500, top: 0, right: 393, bottom: 44, width: 0, height: 44 }
          : { left: 10, top: 10, right: 54, bottom: 54, width: 44, height: 44 },
      clipIntersection: visible
        ? { left: 10, top: 10, right: 54, bottom: 54, width: 44, height: 44 }
        : { left: 10, top: 10, right: 10, bottom: 54, width: 0, height: 44 },
      intersection: visible
        ? { left: 10, top: 10, right: 54, bottom: 54, width: 44, height: 44 }
        : { left: 10, top: 10, right: 10, bottom: 54, width: 0, height: 44 },
    };
  };
  const evidence = await stabilizePagePaint(
    { fonts: { ready: Promise.resolve() }, getAnimations: () => [animation] },
    () => Promise.resolve(),
    snapshot,
  );
  assert.equal(evidence.infiniteStabilized[0].chosenTime, 50);
  assert.equal(evidence.infiniteStabilized[0].after.intersection.width, 44);
});

test("default DOM snapshot intersects viewport and clipping ancestors", async () => {
  const documentTarget = {
    fonts: { ready: Promise.resolve() },
    defaultView: {
      innerWidth: 100,
      innerHeight: 100,
      getComputedStyle(element) {
        return element === parent
          ? {
              display: "block",
              visibility: "visible",
              opacity: "1",
              overflowX: "hidden",
              overflowY: "hidden",
            }
          : {
              display: "block",
              visibility: "visible",
              opacity: "1",
              overflowX: "visible",
              overflowY: "visible",
            };
      },
    },
  };
  const parent = {
    id: "clip",
    tagName: "DIV",
    ownerDocument: documentTarget,
    parentElement: null,
    getBoundingClientRect: () => ({
      left: 0,
      top: 0,
      right: 20,
      bottom: 20,
      width: 20,
      height: 20,
    }),
  };
  const target = {
    id: "moving",
    tagName: "BUTTON",
    ownerDocument: documentTarget,
    parentElement: parent,
    getBoundingClientRect() {
      if (animation.currentTime === 0)
        return {
          left: 200,
          top: 0,
          right: 244,
          bottom: 44,
          width: 44,
          height: 44,
        };
      if (animation.currentTime === 25)
        return {
          left: 30,
          top: 0,
          right: 74,
          bottom: 44,
          width: 44,
          height: 44,
        };
      return { left: 5, top: 5, right: 15, bottom: 15, width: 10, height: 10 };
    },
  };
  const animation = {
    animationName: "moving-dom-loop",
    currentTime: 0,
    effect: {
      target,
      getComputedTiming: () => ({ endTime: Infinity }),
      getTiming: () => ({ duration: 100, iterations: Infinity }),
    },
    pause() {},
  };
  documentTarget.getAnimations = () => [animation];
  const evidence = await stabilizePagePaint(documentTarget, () =>
    Promise.resolve(),
  );
  assert.equal(evidence.infiniteStabilized[0].chosenTime, 50);
  assert.deepEqual(
    evidence.infiniteStabilized[0].after.clippingAncestors.map(
      ({ element }) => element,
    ),
    ["clip"],
  );
  assert.deepEqual(evidence.infiniteStabilized[0].after.intersection, {
    left: 5,
    top: 5,
    right: 15,
    bottom: 15,
    width: 10,
    height: 10,
  });
});

for (const [name, ancestorStyle] of [
  ["zero opacity", { opacity: "0", display: "block", visibility: "visible" }],
  ["display none", { opacity: "1", display: "none", visibility: "visible" }],
  [
    "hidden visibility",
    { opacity: "1", display: "block", visibility: "hidden" },
  ],
]) {
  test(`default DOM rejects target through ancestor ${name}`, async () => {
    const documentTarget = {
      fonts: { ready: Promise.resolve() },
      defaultView: {
        innerWidth: 100,
        innerHeight: 100,
        getComputedStyle(element) {
          return element === parent
            ? { ...ancestorStyle, overflowX: "visible", overflowY: "visible" }
            : {
                opacity: "1",
                display: "block",
                visibility: "visible",
                overflowX: "visible",
                overflowY: "visible",
              };
        },
      },
    };
    const rectangle = {
      left: 10,
      top: 10,
      right: 54,
      bottom: 54,
      width: 44,
      height: 44,
    };
    const parent = {
      id: "visibility-parent",
      tagName: "DIV",
      ownerDocument: documentTarget,
      parentElement: null,
      getBoundingClientRect: () => rectangle,
    };
    const target = {
      id: "visible-target",
      tagName: "BUTTON",
      ownerDocument: documentTarget,
      parentElement: parent,
      getBoundingClientRect: () => rectangle,
    };
    const animation = {
      animationName: "ancestor-visibility-loop",
      currentTime: 10,
      effect: {
        target,
        getComputedTiming: () => ({ endTime: Infinity }),
        getTiming: () => ({ duration: 100, iterations: Infinity }),
      },
      pause() {},
    };
    documentTarget.getAnimations = () => [animation];
    const evidence = await stabilizePagePaint(documentTarget, () =>
      Promise.resolve(),
    );
    assert.deepEqual(evidence.infiniteStabilized, []);
    assert.equal(
      evidence.capabilityFailures[0].code,
      "infinite-animation-no-visible-representative",
    );
    const snapshot = evidence.capabilityFailures[0].before;
    assert.equal(snapshot.effectiveOpacity, Number(ancestorStyle.opacity));
    assert.equal(snapshot.effectiveVisible, false);
    assert.deepEqual(snapshot.ancestorStyles, [
      {
        element: "visibility-parent",
        display: ancestorStyle.display,
        visibility: ancestorStyle.visibility,
        opacity: Number(ancestorStyle.opacity),
      },
    ]);
  });
}

for (const stage of [
  "phase-assignment",
  "phase-frame",
  "phase-snapshot",
  "final-assignment",
]) {
  test(`infinite animation ${stage} exception becomes capability evidence`, async () => {
    const target = { id: "fragile-control", tagName: "BUTTON" };
    let currentTime = 10;
    let assignment = 0;
    const animation = {
      animationName: "fragile-loop",
      effect: {
        target,
        getComputedTiming: () => ({ endTime: Infinity }),
        getTiming: () => ({ duration: 100, iterations: Infinity }),
      },
      pause() {},
      get currentTime() {
        return currentTime;
      },
      set currentTime(value) {
        assignment += 1;
        if (stage === "phase-assignment" && assignment === 1)
          throw new Error("assignment exploded");
        if (stage === "final-assignment" && assignment === 6)
          throw new Error("final exploded");
        currentTime = value;
      },
    };
    let frameCount = 0;
    const frame = async () => {
      frameCount += 1;
      if (stage === "phase-frame" && frameCount === 1)
        throw new Error("frame exploded");
    };
    let snapshots = 0;
    const snapshot = () => {
      snapshots += 1;
      if (stage === "phase-snapshot" && snapshots === 2)
        throw new Error("snapshot exploded");
      return {
        element: "fragile-control",
        visible: true,
        opacity: 1,
        rect: { left: 0, top: 0, right: 44, bottom: 44, width: 44, height: 44 },
        viewportIntersection: {
          left: 0,
          top: 0,
          right: 44,
          bottom: 44,
          width: 44,
          height: 44,
        },
        clipIntersection: {
          left: 0,
          top: 0,
          right: 44,
          bottom: 44,
          width: 44,
          height: 44,
        },
        intersection: {
          left: 0,
          top: 0,
          right: 44,
          bottom: 44,
          width: 44,
          height: 44,
        },
      };
    };
    const evidence = await stabilizePagePaint(
      { fonts: { ready: Promise.resolve() }, getAnimations: () => [animation] },
      frame,
      snapshot,
    );
    assert.equal(evidence.infiniteStabilized.length, 0);
    assert.equal(
      evidence.capabilityFailures[0].code,
      "infinite-animation-evaluation-failed",
    );
    assert.equal(evidence.capabilityFailures[0].identity, "fragile-loop");
    assert.equal(
      evidence.capabilityFailures[0].affectedElement,
      "fragile-control",
    );
    assert.equal(evidence.capabilityFailures[0].stage, stage);
  });
}

for (const stage of [
  "pause",
  "get-computed-timing",
  "get-timing",
  "duration-read",
  "before-snapshot",
  "current-time-read",
  "final-frame",
  "final-snapshot",
]) {
  test(`infinite animation isolates ${stage} exception stage`, async () => {
    const target = { id: "staged-control", tagName: "BUTTON" };
    let currentTime = 10;
    let frameCount = 0;
    let snapshotCount = 0;
    const timing = {};
    Object.defineProperty(timing, "duration", {
      get() {
        if (stage === "duration-read") throw new Error("duration exploded");
        return 100;
      },
    });
    timing.iterations = Infinity;
    const animation = {
      animationName: "staged-loop",
      effect: {
        target,
        getComputedTiming() {
          if (stage === "get-computed-timing")
            throw new Error("computed timing exploded");
          return { endTime: Infinity };
        },
        getTiming() {
          if (stage === "get-timing") throw new Error("timing exploded");
          return timing;
        },
      },
      pause() {
        if (stage === "pause") throw new Error("pause exploded");
      },
      get currentTime() {
        if (stage === "current-time-read")
          throw new Error("current time exploded");
        return currentTime;
      },
      set currentTime(value) {
        currentTime = value;
      },
    };
    const frame = async () => {
      frameCount += 1;
      if (stage === "final-frame" && frameCount === 6)
        throw new Error("final frame exploded");
    };
    const snapshot = () => {
      snapshotCount += 1;
      if (stage === "before-snapshot" && snapshotCount === 1)
        throw new Error("before snapshot exploded");
      if (stage === "final-snapshot" && snapshotCount === 7)
        throw new Error("final snapshot exploded");
      return {
        element: "staged-control",
        visible: true,
        opacity: 1,
        rect: { left: 0, top: 0, right: 44, bottom: 44, width: 44, height: 44 },
        viewportIntersection: {
          left: 0,
          top: 0,
          right: 44,
          bottom: 44,
          width: 44,
          height: 44,
        },
        clipIntersection: {
          left: 0,
          top: 0,
          right: 44,
          bottom: 44,
          width: 44,
          height: 44,
        },
        intersection: {
          left: 0,
          top: 0,
          right: 44,
          bottom: 44,
          width: 44,
          height: 44,
        },
      };
    };
    const evidence = await stabilizePagePaint(
      { fonts: { ready: Promise.resolve() }, getAnimations: () => [animation] },
      frame,
      snapshot,
    );
    assert.equal(evidence.infiniteStabilized.length, 0);
    assert.equal(evidence.capabilityFailures[0].identity, "staged-loop");
    assert.equal(
      evidence.capabilityFailures[0].affectedElement,
      "staged-control",
    );
    assert.equal(evidence.capabilityFailures[0].stage, stage);
  });
}

test("missing infinite duration and current time is explicit capability evidence", async () => {
  const target = { id: "timeless-control", tagName: "BUTTON" };
  const animation = {
    animationName: "timeless-loop",
    currentTime: null,
    effect: {
      target,
      getComputedTiming: () => ({ endTime: Infinity }),
      getTiming: () => ({ duration: "auto", iterations: Infinity }),
    },
    pause() {},
  };
  const evidence = await stabilizePagePaint(
    { fonts: { ready: Promise.resolve() }, getAnimations: () => [animation] },
    () => Promise.resolve(),
    () => null,
  );
  assert.equal(
    evidence.capabilityFailures[0].code,
    "infinite-animation-no-representative-time",
  );
  assert.deepEqual(evidence.infiniteStabilized, []);
});

test("stabilization evidence retains post-focus result and deduplicates failures", () => {
  const failure = { code: "post-focus-failure", identity: "animation" };
  const merged = mergeStabilizationEvidence({
    preCollection: { capabilityFailures: [] },
    focusSteps: [{ capabilityFailures: [failure] }],
    postFocus: { capabilityFailures: [failure] },
  });
  assert.equal(merged.stabilization.postFocus.capabilityFailures[0], failure);
  assert.deepEqual(merged.capabilityFailures, [failure]);
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
        {
          source: "box-shadow",
          color: "rgb(1, 2, 3)",
          offsetX: 0,
          offsetY: 0,
          blur: 0,
          spread: 1,
          inset: false,
          reach: 1,
        },
        {
          source: "box-shadow",
          color: "rgba(4, 5, 6, 0.5)",
          offsetX: 0,
          offsetY: 0,
          blur: 0,
          spread: 3,
          inset: false,
          reach: 3,
        },
      ],
    },
  );
});

test("rendered sampling resolves image paint and retains both focus surfaces", () => {
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
      ratio: 2,
      mask: { pixelCount: 2, bounds: { left: 8, top: 8, right: 11, bottom: 11 }, runs: [{ y: 8, startX: 8, endX: 8 }, { y: 10, startX: 10, endX: 10 }] },
      surfacePalette: {
        behind: [],
        inside: [{ color: { red: 0, green: 0, blue: 0, alpha: 1 }, count: 1 }],
        outside: [{ color: { red: 90, green: 90, blue: 90, alpha: 1 }, count: 1 }],
      },
      surfaceVerdicts: {
        inside: { ratio: 21, minimumEvidence: { ratio: 21 } },
        outside: { ratio: 2, minimumEvidence: { ratio: 2 } },
      },
      minimumEvidence: { ratio: 2 },
    },
  );
  assert.equal(pair.unsupported, undefined);
  assert.equal(pair.sampling.method, "rendered-differential-mask");
  assert.equal(pair.sampledRatios.length, 2);
  assert.equal(pair.ratio, Math.min(...pair.sampledRatios));
  assert.equal(pair.raw.renderedSamples.surfacePalette.inside.length, 1);
  assert.equal(pair.raw.renderedSamples.surfacePalette.outside.length, 1);
});

test("differential rendered evidence supports already-composited group opacity", () => {
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
    {
      ratio: 4.6,
      mask: { pixelCount: 1, bounds: { left: 0, top: 0, right: 1, bottom: 1 }, runs: [{ y: 0, startX: 0, endX: 0 }] },
      surfacePalette: { behind: [{ color: { red: 0, green: 0, blue: 0, alpha: 1 }, count: 1 }], inside: [], outside: [] },
      surfaceVerdicts: { behind: { ratio: 4.6, minimumEvidence: { ratio: 4.6 } } },
      minimumEvidence: { ratio: 4.6 },
    },
  );
  assert.equal(pair.unsupported, undefined);
  assert.equal(pair.ratio, 4.6);
});
