/**
 * Foundation 7C lane A — `platformPresentation` unit tests.
 *
 * Behavioral tests use a real fake `EventTarget` standing in for
 * `VisualViewport` (jsdom does not ship one) and assert exact
 * document-element attributes / inline CSS declarations (presence, value,
 * !important priority, and pre-existing style-attribute absence), live
 * updates, listener cleanup with exact add/remove cardinality, preference
 * changes, the no-`visualViewport` fallback, an explicit-`null` fallback seam,
 * negative-offset and Infinity/NaN geometry proofs, the ownership-token
 * guard, and restore of pre-existing document state. No sleeps, fixed
 * flushes, or source regex. `EventTarget.dispatchEvent` is synchronous, so
 * assertions follow dispatch directly.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ContentSizeCategory } from "../native/contract";
import {
  applyPlatformPresentation,
  computeViewportMetrics,
  createViewportCoordinator,
  type PlatformPresentation,
} from "./platformPresentation";

const ORIGINAL_VP = Object.getOwnPropertyDescriptor(window, "visualViewport");
const ORIGINAL_INNER_HEIGHT = window.innerHeight;

function resetDocument() {
  const el = document.documentElement;
  el.removeAttribute("data-theme");
  el.removeAttribute("data-content-size");
  el.removeAttribute("data-reduced-motion");
  el.removeAttribute("data-unrelated");
  el.style.removeProperty("--viewport-height");
  el.style.removeProperty("--keyboard-inset");
  el.style.removeProperty("--app-custom");
  el.removeAttribute("style");
}

function restoreVisualViewport() {
  if (ORIGINAL_VP === undefined) {
    delete (window as unknown as { visualViewport?: unknown }).visualViewport;
  } else {
    Object.defineProperty(window, "visualViewport", ORIGINAL_VP);
  }
  Object.defineProperty(window, "innerHeight", {
    configurable: true,
    value: ORIGINAL_INNER_HEIGHT,
    writable: true,
  });
}

interface FakeVisualViewport extends EventTarget {
  height: number;
  offsetTop: number;
  width: number;
  offsetLeft: number;
  scale: number;
  onresize: ((this: FakeVisualViewport, ev: Event) => void) | null;
  onscroll: ((this: FakeVisualViewport, ev: Event) => void) | null;
}

function createFakeVisualViewport(
  opts: { height?: number; offsetTop?: number } = {},
): FakeVisualViewport {
  const et = new EventTarget();
  return Object.assign(et, {
    height: opts.height ?? 768,
    offsetTop: opts.offsetTop ?? 0,
    width: 375,
    offsetLeft: 0,
    scale: 1,
    onresize: null,
    onscroll: null,
  });
}

function setVisualViewport(vp: FakeVisualViewport | undefined) {
  if (vp === undefined) {
    delete (window as unknown as { visualViewport?: unknown }).visualViewport;
  } else {
    Object.defineProperty(window, "visualViewport", {
      configurable: true,
      value: vp,
      writable: true,
    });
  }
}

/** Synchronous dispatch — EventTarget.dispatchEvent is sync, no flush needed. */
function dispatch(target: EventTarget, type: string): void {
  target.dispatchEvent(new Event(type));
}

const base: PlatformPresentation = {
  theme: "system",
  contentSize: "large",
  reducedMotion: false,
};

describe("7C presentation — document-root preferences", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("explicit light theme sets data-theme=light on documentElement", () => {
    const stop = applyPlatformPresentation(
      { ...base, theme: "light" },
      document,
    );
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    stop();
  });

  it("explicit dark theme sets data-theme=dark on documentElement", () => {
    const stop = applyPlatformPresentation(
      { ...base, theme: "dark" },
      document,
    );
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    stop();
  });

  it("system theme removes the explicit data-theme attribute", () => {
    document.documentElement.setAttribute("data-theme", "dark");
    const stop = applyPlatformPresentation(
      { ...base, theme: "system" },
      document,
    );
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    stop();
  });

  it("content size always sets the exact native category on documentElement", () => {
    const categories: ContentSizeCategory[] = [
      "small",
      "medium",
      "extraLarge",
      "accessibilityLarge",
      "accessibilityExtraExtraExtraLarge",
    ];
    for (const contentSize of categories) {
      const stop = applyPlatformPresentation(
        { ...base, contentSize },
        document,
      );
      expect(document.documentElement.getAttribute("data-content-size")).toBe(
        contentSize,
      );
      stop();
    }
  });

  it("reduced-motion attribute is present only when enabled", () => {
    const off = applyPlatformPresentation(
      { ...base, reducedMotion: false },
      document,
    );
    expect(document.documentElement.hasAttribute("data-reduced-motion")).toBe(
      false,
    );
    off();

    const on = applyPlatformPresentation(
      { ...base, reducedMotion: true },
      document,
    );
    expect(document.documentElement.getAttribute("data-reduced-motion")).toBe(
      "true",
    );
    on();
  });
});

describe("7C presentation — restore pre-existing document state", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("restore returns a pre-existing data-theme attribute", () => {
    document.documentElement.setAttribute("data-theme", "dark");
    const stop = applyPlatformPresentation(
      { ...base, theme: "light" },
      document,
    );
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    stop();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });

  it("restore returns a pre-existing data-content-size attribute", () => {
    document.documentElement.setAttribute("data-content-size", "small");
    const stop = applyPlatformPresentation(
      { ...base, contentSize: "large" },
      document,
    );
    expect(document.documentElement.getAttribute("data-content-size")).toBe(
      "large",
    );
    stop();
    expect(document.documentElement.getAttribute("data-content-size")).toBe(
      "small",
    );
  });

  it("restore removes a data-reduced-motion attribute that did not pre-exist", () => {
    const stop = applyPlatformPresentation(
      { ...base, reducedMotion: true },
      document,
    );
    expect(document.documentElement.hasAttribute("data-reduced-motion")).toBe(
      true,
    );
    stop();
    expect(document.documentElement.hasAttribute("data-reduced-motion")).toBe(
      false,
    );
  });

  it("restore returns a pre-existing data-reduced-motion attribute", () => {
    document.documentElement.setAttribute("data-reduced-motion", "true");
    const stop = applyPlatformPresentation(
      { ...base, reducedMotion: false },
      document,
    );
    expect(document.documentElement.hasAttribute("data-reduced-motion")).toBe(
      false,
    );
    stop();
    expect(document.documentElement.getAttribute("data-reduced-motion")).toBe(
      "true",
    );
  });

  it("restore does not clobber an unrelated pre-existing attribute", () => {
    document.documentElement.setAttribute("data-unrelated", "keepme");
    const stop = applyPlatformPresentation(
      { ...base, theme: "light" },
      document,
    );
    expect(document.documentElement.getAttribute("data-unrelated")).toBe(
      "keepme",
    );
    stop();
    expect(document.documentElement.getAttribute("data-unrelated")).toBe(
      "keepme",
    );
  });

  it("restore does not clobber pre-existing inline CSS variables", () => {
    document.documentElement.style.setProperty("--app-custom", "42px");
    const stop = applyPlatformPresentation(base, document);
    expect(
      document.documentElement.style.getPropertyValue("--app-custom"),
    ).toBe("42px");
    stop();
    expect(
      document.documentElement.style.getPropertyValue("--app-custom"),
    ).toBe("42px");
  });
});

describe("7C presentation — preference changes update documentElement live", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("theme change is reflected on documentElement", () => {
    const stop = applyPlatformPresentation(
      { ...base, theme: "light" },
      document,
    );
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    stop();
    const stop2 = applyPlatformPresentation(
      { ...base, theme: "dark" },
      document,
    );
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    stop2();
  });
});

// ---------------------------------------------------------------------------
// computeViewportMetrics — exact finite-operand formula proofs
// ---------------------------------------------------------------------------

describe("7C computeViewportMetrics — exact finite-operand formula", () => {
  it("no viewport: height=innerHeight, inset=0", () => {
    const m = computeViewportMetrics({ innerHeight: 800 });
    expect(m).toEqual({ viewportHeight: 800, keyboardInset: 0 });
  });

  it("inset = max(0, layoutHeight - (offsetTop + height))", () => {
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({ height: 500, offsetTop: 100 }),
      innerHeight: 800,
    });
    // 800 - (100 + 500) = 200
    expect(m).toEqual({ viewportHeight: 800, keyboardInset: 200 });
  });

  it("negative final inset clamps to 0 (clamps the final result only)", () => {
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({ height: 900, offsetTop: 100 }),
      innerHeight: 800,
    });
    // 800 - (100 + 900) = -200 -> 0
    expect(m).toEqual({ viewportHeight: 800, keyboardInset: 0 });
  });

  it("negative offsetTop is NOT individually clamped: it contributes to the sum", () => {
    // offsetTop = -50, height = 500 -> sum = 450 -> inset = 800 - 450 = 350.
    // If offsetTop had been clamped to 0 first, sum would be 500 and inset 300.
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({ height: 500, offsetTop: -50 }),
      innerHeight: 800,
    });
    expect(m.keyboardInset).toBe(350);
    expect(m.viewportHeight).toBe(800);
  });

  it("negative height is NOT individually clamped: it contributes to the sum", () => {
    // height = -100, offsetTop = 50 -> sum = -50 -> inset = 800 - (-50) = 850.
    // If height had been clamped to 0 first, sum would be 50 and inset 750.
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({ height: -100, offsetTop: 50 }),
      innerHeight: 800,
    });
    expect(m.keyboardInset).toBe(850);
    expect(m.viewportHeight).toBe(800);
  });

  it("NaN offsetTop yields a finite nonnegative inset (0)", () => {
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({
        height: 500,
        offsetTop: Number.NaN,
      }),
      innerHeight: 800,
    });
    expect(Number.isFinite(m.keyboardInset)).toBe(true);
    expect(m.keyboardInset).toBe(0);
    expect(m.viewportHeight).toBe(800);
  });

  it("NaN height yields a finite nonnegative inset (0)", () => {
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({
        height: Number.NaN,
        offsetTop: 100,
      }),
      innerHeight: 800,
    });
    expect(Number.isFinite(m.keyboardInset)).toBe(true);
    expect(m.keyboardInset).toBe(0);
  });

  it("Infinity offsetTop yields a finite nonnegative inset (0)", () => {
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({
        height: 500,
        offsetTop: Number.POSITIVE_INFINITY,
      }),
      innerHeight: 800,
    });
    expect(Number.isFinite(m.keyboardInset)).toBe(true);
    expect(m.keyboardInset).toBe(0);
  });

  it("Infinity height yields a finite nonnegative inset (0)", () => {
    const m = computeViewportMetrics({
      visualViewport: createFakeVisualViewport({
        height: Number.POSITIVE_INFINITY,
        offsetTop: 100,
      }),
      innerHeight: 800,
    });
    expect(Number.isFinite(m.keyboardInset)).toBe(true);
    expect(m.keyboardInset).toBe(0);
  });

  it("non-finite innerHeight yields finite nonnegative viewportHeight (0)", () => {
    expect(
      computeViewportMetrics({ innerHeight: Number.NaN }).viewportHeight,
    ).toBe(0);
    expect(
      computeViewportMetrics({ innerHeight: Number.POSITIVE_INFINITY })
        .viewportHeight,
    ).toBe(0);
    expect(computeViewportMetrics({ innerHeight: -10 }).viewportHeight).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// VisualViewport coordinator — basic values
// ---------------------------------------------------------------------------

describe("7C coordinator — basic values", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("sets --viewport-height to innerHeight and --keyboard-inset to 0 with no keyboard", () => {
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");
    coord.stop();
  });

  it("keyboard inset = max(0, layoutHeight - (offsetTop + height))", () => {
    setVisualViewport(
      createFakeVisualViewport({ height: 500, offsetTop: 100 }),
    );
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe(`${window.innerHeight - (100 + 500)}px`);
    coord.stop();
  });

  it("keyboard inset never negative (clamps final result to 0)", () => {
    setVisualViewport(
      createFakeVisualViewport({ height: 900, offsetTop: 100 }),
    );
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");
    coord.stop();
  });

  it("viewport height is nonnegative as well as finite", () => {
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    const coord = createViewportCoordinator({ document });
    coord.start();
    const vh = Number.parseFloat(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    );
    expect(Number.isFinite(vh)).toBe(true);
    expect(vh).toBeGreaterThanOrEqual(0);
    coord.stop();
  });
});

// ---------------------------------------------------------------------------
// Coordinator — live updates (synchronous dispatch, no flush)
// ---------------------------------------------------------------------------

describe("7C coordinator — live updates", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("updates --keyboard-inset and --viewport-height on visualViewport.resize", () => {
    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    setVisualViewport(vp);
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");

    vp.height = 400;
    vp.offsetTop = 0;
    dispatch(vp, "resize");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe(`${window.innerHeight - 400}px`);
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
    coord.stop();
  });

  it("updates --keyboard-inset on visualViewport.scroll", () => {
    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    setVisualViewport(vp);
    const coord = createViewportCoordinator({ document });
    coord.start();
    vp.height = 300;
    vp.offsetTop = 50;
    dispatch(vp, "scroll");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe(`${window.innerHeight - (50 + 300)}px`);
    coord.stop();
  });
});

// ---------------------------------------------------------------------------
// Coordinator — fallback and explicit null seam
// ---------------------------------------------------------------------------

describe("7C coordinator — fallback and null seam", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("falls back to window.resize when visualViewport is absent", () => {
    setVisualViewport(undefined);
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");

    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: 600,
      writable: true,
    });
    dispatch(window, "resize");
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("600px");
    coord.stop();
  });

  it("explicit null forces fallback even when window.visualViewport is present", () => {
    // window still has a viewport installed.
    setVisualViewport(
      createFakeVisualViewport({ height: 400, offsetTop: 100 }),
    );
    const coord = createViewportCoordinator({ document, visualViewport: null });
    coord.start();
    // Fallback path: inset 0, height = innerHeight (ignores the live viewport).
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
    coord.stop();
  });
});

// ---------------------------------------------------------------------------
// Coordinator — listener cleanup cardinality (exact add/remove proof)
// ---------------------------------------------------------------------------

describe("7C coordinator — listener cleanup cardinality", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("proves resize, scroll, and window-resize listeners are added then removed", () => {
    // Use explicit fake window + fake viewport (both real EventTargets) so
    // add/remove counts are deterministic regardless of jsdom's Window
    // prototype chain. Wrap each target's own add/removeEventListener.
    const fakeWindow = new EventTarget() as unknown as Window &
      typeof globalThis & { innerHeight: number };
    (fakeWindow as { innerHeight: number }).innerHeight = 768;
    // Cast the bound methods to a generic signature so the wrapper can
    // forward any (type, listener, options?) tuple without fighting DOM
    // overloads.
    type AddFn = (
      type: string,
      listener: EventListenerOrEventListenerObject,
      options?: boolean | AddEventListenerOptions,
    ) => void;
    type RemoveFn = (
      type: string,
      listener: EventListenerOrEventListenerObject,
      options?: boolean | EventListenerOptions,
    ) => void;
    const winAdd = fakeWindow.addEventListener.bind(
      fakeWindow,
    ) as unknown as AddFn;
    const winRemove = fakeWindow.removeEventListener.bind(
      fakeWindow,
    ) as unknown as RemoveFn;
    const winAdded: Array<{ type: string }> = [];
    const winRemoved: Array<{ type: string }> = [];
    fakeWindow.addEventListener = ((type: string, listener, options) => {
      winAdded.push({ type });
      winAdd(type, listener, options);
    }) as AddFn;
    fakeWindow.removeEventListener = ((type: string, listener, options) => {
      winRemoved.push({ type });
      winRemove(type, listener, options);
    }) as RemoveFn;

    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    const vpAdd = vp.addEventListener.bind(vp) as unknown as AddFn;
    const vpRemove = vp.removeEventListener.bind(vp) as unknown as RemoveFn;
    const vpAdded: Array<{ type: string }> = [];
    const vpRemoved: Array<{ type: string }> = [];
    vp.addEventListener = ((type: string, listener, options) => {
      vpAdded.push({ type });
      vpAdd(type, listener, options);
    }) as AddFn;
    vp.removeEventListener = ((type: string, listener, options) => {
      vpRemoved.push({ type });
      vpRemove(type, listener, options);
    }) as RemoveFn;

    const coord = createViewportCoordinator({
      document,
      visualViewport: vp,
      window: fakeWindow,
    });
    coord.start();
    // Three listeners: vp.resize, vp.scroll, window.resize.
    expect(vpAdded.map((a) => a.type).sort()).toEqual(["resize", "scroll"]);
    expect(winAdded.map((a) => a.type)).toEqual(["resize"]);
    expect(vpAdded.length + winAdded.length).toBe(3);
    coord.stop();
    // All three removed.
    expect(vpRemoved.map((a) => a.type).sort()).toEqual(["resize", "scroll"]);
    expect(winRemoved.map((a) => a.type)).toEqual(["resize"]);
    expect(vpRemoved.length + winRemoved.length).toBe(3);
  });

  it("removes listeners on stop: no updates after stop()", () => {
    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    setVisualViewport(vp);
    const coord = createViewportCoordinator({ document });
    coord.start();
    coord.stop();
    document.documentElement.style.setProperty("--keyboard-inset", "0px");
    vp.height = 300;
    vp.offsetTop = 0;
    dispatch(vp, "resize");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");
  });

  it("NaN-safe geometry still yields finite nonnegative px", () => {
    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    setVisualViewport(vp);
    const coord = createViewportCoordinator({ document });
    coord.start();
    vp.height = Number.NaN;
    vp.offsetTop = Number.NaN;
    vp.dispatchEvent(new Event("resize"));
    const inset =
      document.documentElement.style.getPropertyValue("--keyboard-inset");
    const vh =
      document.documentElement.style.getPropertyValue("--viewport-height");
    expect(inset).toMatch(/^-?\d+(\.\d+)?px$/);
    expect(vh).toMatch(/^-?\d+(\.\d+)?px$/);
    expect(Number.parseFloat(inset)).toBeGreaterThanOrEqual(0);
    expect(Number.isFinite(Number.parseFloat(inset))).toBe(true);
    expect(Number.isFinite(Number.parseFloat(vh))).toBe(true);
    coord.stop();
  });
});

// ---------------------------------------------------------------------------
// Coordinator — restore of pre-existing inline CSS declarations
// ---------------------------------------------------------------------------

describe("7C coordinator — restores pre-existing viewport CSS declarations", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("stop() restores prior --viewport-height and --keyboard-inset values and priority", () => {
    document.documentElement.style.setProperty(
      "--viewport-height",
      "100dvh",
      "important",
    );
    document.documentElement.style.setProperty(
      "--keyboard-inset",
      "env(keyboard-inset-height, 0px)",
    );
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
    coord.stop();
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("100dvh");
    expect(
      document.documentElement.style.getPropertyPriority("--viewport-height"),
    ).toBe("important");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("env(keyboard-inset-height, 0px)");
  });

  it("stop() removes managed declarations that did not pre-exist (no leftover)", () => {
    // No pre-existing declarations; style attribute absent.
    expect(document.documentElement.hasAttribute("style")).toBe(false);
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(document.documentElement.hasAttribute("style")).toBe(true);
    coord.stop();
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("");
    // The empty style attribute is dropped (no artifact left behind).
    expect(document.documentElement.hasAttribute("style")).toBe(false);
  });

  it("stop() from no inline vars removes declarations, not an empty inline style", () => {
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    const coord = createViewportCoordinator({ document });
    coord.start();
    coord.stop();
    // cssText empty AND style attribute absent.
    expect(document.documentElement.style.cssText).toBe("");
    expect(document.documentElement.hasAttribute("style")).toBe(false);
  });

  it("stop() does not delete unrelated inline styles added later", () => {
    document.documentElement.style.setProperty("--keep", "5px");
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    const coord = createViewportCoordinator({ document });
    coord.start();
    coord.stop();
    expect(document.documentElement.style.getPropertyValue("--keep")).toBe(
      "5px",
    );
    // The style attribute is retained because unrelated declarations remain.
    expect(document.documentElement.hasAttribute("style")).toBe(true);
  });

  it("stop() preserves a pre-existing empty style attribute", () => {
    document.documentElement.setAttribute("style", "");
    expect(document.documentElement.hasAttribute("style")).toBe(true);
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    const coord = createViewportCoordinator({ document });
    coord.start();
    coord.stop();
    // The pre-existing empty style attribute was present, so it is kept.
    expect(document.documentElement.hasAttribute("style")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Coordinator — ownership token guard
// ---------------------------------------------------------------------------

describe("7C coordinator — ownership token guard", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("a stale owner's stop does not clobber a newer owner's declarations", () => {
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
    // First coordinator starts and owns the document; its snapshot is empty.
    const first = createViewportCoordinator({ document });
    first.start();
    // Second coordinator starts and becomes the new owner; its snapshot
    // captures the first owner's written values (0px / innerHeight px).
    const second = createViewportCoordinator({ document });
    second.start();
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");
    // Make the second owner write a nonzero inset.
    const vp = (window as unknown as { visualViewport: FakeVisualViewport })
      .visualViewport;
    vp.height = 300;
    vp.offsetTop = 0;
    vp.dispatchEvent(new Event("resize"));
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe(`${window.innerHeight - 300}px`);
    // The first (stale) owner stopping must NOT restore/clobber the newer
    // owner's live values.
    first.stop();
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe(`${window.innerHeight - 300}px`);
    // The current owner stopping restores to ITS snapshot (first owner's 0px).
    second.stop();
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
  });
});
