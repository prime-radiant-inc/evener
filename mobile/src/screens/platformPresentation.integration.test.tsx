/**
 * Foundation 7C lane A — RootShell integration: document-root presentation
 * preferences and the visualViewport coordinator installed once.
 *
 * Asserts the shell applies theme/content-size/reduced-motion to
 * `document.documentElement` (not only `.evener-shell`), reflects preference
 * changes live, sets `--viewport-height`, restores document state on unmount,
 * and that a StrictMode double-mount does not leak or clobber unrelated state.
 */
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createConnectionStore } from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";
import { createShellServices } from "./fixture-services";
import { RootShell } from "./RootShell";

const ORIGINAL_VP = Object.getOwnPropertyDescriptor(window, "visualViewport");

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
}

afterEach(() => {
  cleanup();
});

beforeEach(() => {
  resetDocument();
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
  beforeEach(() => {
    setVisualViewport(createFakeVisualViewport({ height: 768, offsetTop: 0 }));
  });
  afterEach(() => {
    if (ORIGINAL_VP === undefined) {
      delete (window as unknown as { visualViewport?: unknown }).visualViewport;
    } else {
      Object.defineProperty(window, "visualViewport", ORIGINAL_VP);
    }
  });

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

  it("documentElement reflects preference changes live", async () => {
    const services = makeServices();
    const stores = makeStores(services);
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
  });

  it("coordinator sets --viewport-height on mount", async () => {
    const services = makeServices();
    const stores = makeStores(services);
    render(<RootShell services={services} stores={stores} />);
    await waitForTabs();
    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe(`${window.innerHeight}px`);
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

  it("StrictMode double-mount does not leak or clobber document state", async () => {
    const { StrictMode } = await import("react");
    const services = makeServices();
    const stores = makeStores(services);
    stores.preferences.getState().setTheme("dark");
    stores.preferences.getState().setReducedMotion(true);
    document.documentElement.setAttribute("data-unrelated", "keepme");
    const { unmount } = render(
      <StrictMode>
        <RootShell services={services} stores={stores} />
      </StrictMode>,
    );
    await waitForTabs();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(document.documentElement.getAttribute("data-reduced-motion")).toBe(
      "true",
    );
    unmount();
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(document.documentElement.hasAttribute("data-reduced-motion")).toBe(
      false,
    );
    expect(document.documentElement.getAttribute("data-unrelated")).toBe(
      "keepme",
    );
  });
});
