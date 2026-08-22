/**
 * Foundation 7C lane A — `platformPresentation` unit tests.
 *
 * Behavioral tests use a real fake `EventTarget` standing in for
 * `VisualViewport` (jsdom does not ship one) and assert exact
 * document-element attributes / inline CSS variables, live updates, listener
 * cleanup, preference changes, the no-`visualViewport` fallback, NaN-safe
 * geometry, and restore of pre-existing document state. No sleeps, fixed
 * flushes, or source regex.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ContentSizeCategory } from "../native/contract";
import {
  applyPlatformPresentation,
  createViewportCoordinator,
  type PlatformPresentation,
  type ViewportCoordinator,
} from "./platformPresentation";

const ORIGINAL_VP = Object.getOwnPropertyDescriptor(window, "visualViewport");

function resetDocument() {
  const el = document.documentElement;
  el.removeAttribute("data-theme");
  el.removeAttribute("data-content-size");
  el.removeAttribute("data-reduced-motion");
  el.style.removeProperty("--viewport-height");
  el.style.removeProperty("--keyboard-inset");
}

function restoreVisualViewport() {
  if (ORIGINAL_VP === undefined) {
    delete (window as unknown as { visualViewport?: unknown }).visualViewport;
  } else {
    Object.defineProperty(window, "visualViewport", ORIGINAL_VP);
  }
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

/** Dispatch the named event on the target, then yield a microtask. */
async function dispatch(target: EventTarget, type: string): Promise<void> {
  target.dispatchEvent(new Event(type));
  await Promise.resolve();
}

const base: PlatformPresentation = {
  theme: "system",
  contentSize: "large",
  reducedMotion: false,
};

describe("7C presentation — document-root preferences", () => {
  beforeEach(resetDocument);

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
// VisualViewport coordinator
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

  it("keyboard inset never negative (clamps to 0)", () => {
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
});

describe("7C coordinator — live updates", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("updates --keyboard-inset and --viewport-height on visualViewport.resize", async () => {
    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    setVisualViewport(vp);
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");

    vp.height = 400;
    vp.offsetTop = 0;
    await dispatch(vp, "resize");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe(`${window.innerHeight - 400}px`);
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
    coord.stop();
  });

  it("updates --keyboard-inset on visualViewport.scroll", async () => {
    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    setVisualViewport(vp);
    const coord = createViewportCoordinator({ document });
    coord.start();
    vp.height = 300;
    vp.offsetTop = 50;
    await dispatch(vp, "scroll");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe(`${window.innerHeight - (50 + 300)}px`);
    coord.stop();
  });
});

describe("7C coordinator — fallback and cleanup", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("falls back to window.resize when visualViewport is absent", async () => {
    setVisualViewport(undefined);
    const coord = createViewportCoordinator({ document });
    coord.start();
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("0px");

    const prev = window.innerHeight;
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: 600,
      writable: true,
    });
    await dispatch(window, "resize");
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("600px");
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: prev,
      writable: true,
    });
    coord.stop();
  });

  it("removes listeners on stop: no updates after stop()", async () => {
    const vp = createFakeVisualViewport({ height: 768, offsetTop: 0 });
    setVisualViewport(vp);
    const coord = createViewportCoordinator({ document });
    coord.start();
    coord.stop();
    document.documentElement.style.setProperty("--keyboard-inset", "0px");
    vp.height = 300;
    vp.offsetTop = 0;
    await dispatch(vp, "resize");
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

describe("7C coordinator — restores pre-existing viewport CSS variables", () => {
  beforeEach(resetDocument);
  afterEach(restoreVisualViewport);

  it("stop() restores prior --viewport-height and --keyboard-inset values", () => {
    document.documentElement.style.setProperty("--viewport-height", "100dvh");
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
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("env(keyboard-inset-height, 0px)");
  });
});

// Keep type imports referenced for tsc.
void (undefined as unknown as ViewportCoordinator);
