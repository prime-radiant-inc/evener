/**
 * Foundation 7C lane A — RootShell integration: document-root presentation
 * preferences and the visualViewport coordinator installed once.
 *
 * Asserts the shell applies theme/content-size/reduced-motion to
 * `document.documentElement` (not only `.evener-shell`), reflects theme, motion,
 * AND content-size preference changes live, sets `--viewport-height`, restores
 * document state on unmount, and that a StrictMode double-mount does not leak
 * or clobber unrelated state — including pre-existing managed attributes and
 * viewport CSS variables seeded before mount and restored after unmount.
 */
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ContentSizeCategory } from "../native/contract";
import { createConnectionStore } from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import { createShellServices } from "./fixture-services";
import { RootShell } from "./RootShell";

const ORIGINAL_VP = Object.getOwnPropertyDescriptor(window, "visualViewport");
const ORIGINAL_INNER_HEIGHT = window.innerHeight;

const PROFILES = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
] as const;

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

afterEach(() => {
  cleanup();
  resetDocument();
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
});

beforeEach(() => {
  resetDocument();
  setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
});

function makeServices() {
  return createShellServices({ profiles: PROFILES, activeProfileId: "p1" });
}

function makeStores(services: ReturnType<typeof createShellServices>) {
  return {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
}

async function waitForTabs() {
  await screen.findByRole("tab", { name: /sessions/i });
}

describe("7C RootShell — document-root presentation preferences", () => {
  it("applies theme/content-size/reduced-motion to documentElement", async () => {
    const services = makeServices();
    const stores = makeStores(services);
    // The mount refresh consumes getContentSize into the store; keep it
    // aligned with the preference we assert against.
    vi.spyOn(services.native, "getContentSize").mockResolvedValue("extraLarge");
    stores.preferences.getState().setTheme("dark");
    stores.preferences.getState().setContentSize("extraLarge");
    stores.preferences.getState().setReducedMotion(true);
    render(<RootShell services={services} stores={stores} />);
    await waitForTabs();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(document.documentElement.getAttribute("data-content-size")).toBe(
      "extraLarge",
    );
    expect(document.documentElement.getAttribute("data-reduced-motion")).toBe(
      "true",
    );
  });

  it("documentElement reflects theme, motion, AND content-size changes live", async () => {
    const services = makeServices();
    const stores = makeStores(services);
    // Keep getContentSize aligned with the live content-size we set, so the
    // mount refresh does not fight the later preference change.
    let currentSize: ContentSizeCategory = "large";
    vi.spyOn(services.native, "getContentSize").mockImplementation(async () => {
      return currentSize;
    });
    render(<RootShell services={services} stores={stores} />);
    await waitForTabs();
    // System default: no explicit data-theme.
    expect(document.documentElement.getAttribute("data-theme")).toBeNull();
    act(() => {
      stores.preferences.getState().setTheme("light");
    });
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    act(() => {
      stores.preferences.getState().setReducedMotion(true);
    });
    expect(document.documentElement.getAttribute("data-reduced-motion")).toBe(
      "true",
    );
    // Live content-size change.
    currentSize = "extraExtraLarge";
    act(() => {
      stores.preferences.getState().setContentSize("extraExtraLarge");
    });
    expect(document.documentElement.getAttribute("data-content-size")).toBe(
      "extraExtraLarge",
    );
  });

  it("coordinator sets --viewport-height on mount (finite nonnegative)", async () => {
    const services = makeServices();
    const stores = makeStores(services);
    render(<RootShell services={services} stores={stores} />);
    await waitForTabs();
    const vh =
      document.documentElement.style.getPropertyValue("--viewport-height");
    expect(vh).toBe(`${window.innerHeight}px`);
    expect(Number.parseFloat(vh)).toBeGreaterThanOrEqual(0);
    expect(Number.isFinite(Number.parseFloat(vh))).toBe(true);
  });

  it("unmount restores documentElement attributes and CSS variables", async () => {
    const services = makeServices();
    const stores = makeStores(services);
    stores.preferences.getState().setTheme("dark");
    document.documentElement.setAttribute("data-unrelated", "keepme");
    const { unmount } = render(
      <RootShell services={services} stores={stores} />,
    );
    await waitForTabs();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    unmount();
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(document.documentElement.hasAttribute("data-content-size")).toBe(
      false,
    );
    expect(document.documentElement.hasAttribute("data-reduced-motion")).toBe(
      false,
    );
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("");
    expect(document.documentElement.getAttribute("data-unrelated")).toBe(
      "keepme",
    );
  });

  it("StrictMode double-mount seeds and restores pre-existing managed attrs, viewport CSS vars, and unrelated state", async () => {
    const { StrictMode } = await import("react");
    const services = makeServices();
    const stores = makeStores(services);
    stores.preferences.getState().setTheme("dark");
    stores.preferences.getState().setReducedMotion(true);
    // Seed pre-existing managed attributes and viewport CSS variables plus
    // an unrelated attribute and inline var, all of which must survive.
    document.documentElement.setAttribute("data-theme", "light");
    document.documentElement.setAttribute("data-content-size", "small");
    document.documentElement.setAttribute("data-reduced-motion", "true");
    document.documentElement.setAttribute("data-unrelated", "keepme");
    document.documentElement.style.setProperty(
      "--viewport-height",
      "100dvh",
      "important",
    );
    document.documentElement.style.setProperty(
      "--keyboard-inset",
      "env(keyboard-inset-height, 0px)",
    );
    document.documentElement.style.setProperty("--app-custom", "42px");

    const { unmount } = render(
      <StrictMode>
        <RootShell services={services} stores={stores} />
      </StrictMode>,
    );
    await waitForTabs();
    // After settling, the latest applied preference state wins.
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(document.documentElement.getAttribute("data-reduced-motion")).toBe(
      "true",
    );
    // Unrelated state is never clobbered during the run.
    expect(document.documentElement.getAttribute("data-unrelated")).toBe(
      "keepme",
    );
    expect(
      document.documentElement.style.getPropertyValue("--app-custom"),
    ).toBe("42px");

    unmount();
    // After unmount, pre-existing managed attributes are restored exactly.
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(document.documentElement.getAttribute("data-content-size")).toBe(
      "small",
    );
    expect(document.documentElement.getAttribute("data-reduced-motion")).toBe(
      "true",
    );
    // Pre-existing viewport CSS variables restored with priority.
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("100dvh");
    expect(
      document.documentElement.style.getPropertyPriority("--viewport-height"),
    ).toBe("important");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("env(keyboard-inset-height, 0px)");
    // Unrelated state survives unmount.
    expect(document.documentElement.getAttribute("data-unrelated")).toBe(
      "keepme",
    );
    expect(
      document.documentElement.style.getPropertyValue("--app-custom"),
    ).toBe("42px");
  });
});
