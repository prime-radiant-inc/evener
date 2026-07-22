import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { prefsStore } from "../../stores/prefs";
import { resetTreeStoreForTests, treeStore } from "../../stores/tree";
import sheetStyles from "../../widgets/sheet/sheet.module.css";
import { resetWorkspaceStoreForTests } from "../workspace";
import { RailHost } from "./RailHost";
import { revealSessionInRail, setRailRevealHandler } from "./railController";

// Node 26 shadows jsdom's real localStorage with a non-functional global under
// vitest; RailHost mounts <Rail/>, and prefsStore writes through on
// setSidebarMode - the same in-memory stand-in every rail/shell test uses.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

// jsdom has no matchMedia; RailHost queries two features - useIsMobile's
// "(max-width: 899px)" and useSidebarMode's "(min-width: 1200px)". This stub
// answers each from the {mobile, wide} state a test installs.
class FakeMediaQueryList {
  matches: boolean;
  media: string;
  private listeners = new Set<(event: MediaQueryListEvent) => void>();
  constructor(media: string, matches: boolean) {
    this.media = media;
    this.matches = matches;
  }
  addEventListener(type: string, listener: (event: MediaQueryListEvent) => void): void {
    if (type === "change") this.listeners.add(listener);
  }
  removeEventListener(type: string, listener: (event: MediaQueryListEvent) => void): void {
    if (type === "change") this.listeners.delete(listener);
  }
}

function installMatchMedia(state: { mobile: boolean; wide: boolean }) {
  const lists = new Map<string, FakeMediaQueryList>();
  const matchMedia = (query: string) => {
    let list = lists.get(query);
    if (!list) {
      const initial = query.includes("min-width: 1200px")
        ? state.wide
        : query.includes("max-width: 899px")
          ? state.mobile
          : false;
      list = new FakeMediaQueryList(query, initial);
      lists.set(query, list);
    }
    return list;
  };
  window.matchMedia = matchMedia as unknown as typeof window.matchMedia;
}

function emptyTree(needsYou = 0) {
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: [],
    needs_you: [],
    favorites: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou, error: 0, working: 0 },
  };
}

function jsonResponse(body: unknown): Response {
  return { ok: true, status: 200, statusText: "OK", json: () => Promise.resolve(body) } as Response;
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
  resetTreeStoreForTests();
  resetWorkspaceStoreForTests();
  prefsStore.setState({ sidebarMode: "auto" });
  // Quiet, resolving fetch so any mounted <Rail/> refresh() doesn't throw.
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse(emptyTree())),
  );
});

afterEach(() => {
  cleanup();
  setRailRevealHandler(null);
  prefsStore.setState({ sidebarMode: "auto" });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  // @ts-expect-error restore jsdom's honest absence of matchMedia
  delete window.matchMedia;
});

describe("desktop mode resolution", () => {
  test("pane mode renders the rail inline with a Hide sidebar affordance, no chip", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "pane" });
    render(<RailHost />);
    expect(screen.getByRole("heading", { name: "Sessions" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /hide sidebar/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /show sidebar/i })).toBeNull();
  });

  test("auto mode above 1200px renders inline (not collapsed)", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "auto" });
    render(<RailHost />);
    expect(screen.getByRole("heading", { name: "Sessions" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /show sidebar/i })).toBeNull();
  });

  test("auto mode below 1200px collapses to the ☰ chip (rail hidden)", () => {
    installMatchMedia({ mobile: false, wide: false });
    prefsStore.setState({ sidebarMode: "auto" });
    render(<RailHost />);
    expect(screen.getByRole("button", { name: /show sidebar/i })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Sessions" })).toBeNull();
  });

  test("rail (Collapsed) mode collapses to the ☰ chip even on a wide viewport", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "rail" });
    render(<RailHost />);
    expect(screen.getByRole("button", { name: /show sidebar/i })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Sessions" })).toBeNull();
  });
});

describe("the ☰ chip and overlay drawer", () => {
  test("clicking the chip opens the rail as an overlay drawer", async () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "rail" });
    render(<RailHost />);
    expect(screen.queryByRole("dialog")).toBeNull();

    await userEvent.setup().click(screen.getByRole("button", { name: /show sidebar/i }));
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  test("the chip surfaces the needs-you count in its accessible name (color-is-attention badge)", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "rail" });
    treeStore.setState({ tree: emptyTree(3) });
    render(<RailHost />);
    expect(screen.getByRole("button", { name: /show sidebar.*3 need attention/i })).toBeTruthy();
  });

  // The ☰ chip sits top-left (CLASS.chipBar); the drawer it opens must slide
  // in from the same edge, not the opposite one.
  test("the drawer is anchored left, matching the ☰ chip's top-left position", async () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "rail" });
    render(<RailHost />);

    await userEvent.setup().click(screen.getByRole("button", { name: /show sidebar/i }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.className.split(" ")).toContain(sheetStyles.left);
    expect(dialog.className.split(" ")).not.toContain(sheetStyles.right);
  });
});

describe("⌘B cycles the sidebar mode (rail -> pane -> auto)", () => {
  function pressCmdB(): boolean {
    const event = new KeyboardEvent("keydown", { key: "b", metaKey: true, bubbles: true, cancelable: true });
    act(() => {
      window.dispatchEvent(event);
    });
    return event.defaultPrevented;
  }

  test("cycles rail -> pane -> auto -> rail and preventDefaults the browser shortcut", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "rail" });
    render(<RailHost />);

    expect(pressCmdB()).toBe(true);
    expect(prefsStore.getState().sidebarMode).toBe("pane");
    pressCmdB();
    expect(prefsStore.getState().sidebarMode).toBe("auto");
    pressCmdB();
    expect(prefsStore.getState().sidebarMode).toBe("rail");
  });

  test("⌘B does nothing on mobile (the modes are desktop only)", () => {
    installMatchMedia({ mobile: true, wide: false });
    prefsStore.setState({ sidebarMode: "auto" });
    render(<RailHost />);
    pressCmdB();
    expect(prefsStore.getState().sidebarMode).toBe("auto");
  });

  // Ctrl+B is the macOS emacs-style "move cursor back" binding many native
  // text fields honor - without a focus guard, this chord would hijack it
  // mid-typing (RailHost.tsx accepts meta OR ctrl for cross-platform ⌘B).
  test("Ctrl+B does not cycle the mode when the target is a focused textarea, and does not preventDefault", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "rail" });
    render(
      <>
        <RailHost />
        <textarea aria-label="Composer" />
      </>,
    );
    const textarea = screen.getByRole("textbox", { name: "Composer" });
    textarea.focus();
    const event = new KeyboardEvent("keydown", { key: "b", ctrlKey: true, bubbles: true, cancelable: true });
    act(() => {
      textarea.dispatchEvent(event);
    });

    expect(prefsStore.getState().sidebarMode).toBe("rail");
    expect(event.defaultPrevented).toBe(false);
  });
});

describe("hide affordance wiring", () => {
  test("Hide sidebar in an inline mode switches to rail mode, collapsing to the chip", async () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "pane" });
    render(<RailHost />);

    await userEvent.setup().click(screen.getByRole("button", { name: /hide sidebar/i }));
    expect(prefsStore.getState().sidebarMode).toBe("rail");
    expect(screen.getByRole("button", { name: /show sidebar/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /hide sidebar/i })).toBeNull();
  });
});

describe("mobile", () => {
  test("renders the plain rail (no mode logic, no chip) regardless of sidebarMode", () => {
    installMatchMedia({ mobile: true, wide: false });
    prefsStore.setState({ sidebarMode: "rail" }); // would collapse on desktop
    render(<RailHost />);
    expect(screen.getByRole("heading", { name: "Sessions" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /show sidebar/i })).toBeNull();
  });
});

describe("reveal seam (railController /project)", () => {
  test("registers a handler; revealing while collapsed opens the overlay drawer first", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "rail" });
    render(<RailHost />);
    expect(screen.queryByRole("dialog")).toBeNull();

    act(() => revealSessionInRail("local:abc"));
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  test("clears its handler on unmount", () => {
    installMatchMedia({ mobile: false, wide: true });
    prefsStore.setState({ sidebarMode: "pane" });
    const { unmount } = render(<RailHost />);
    unmount();
    // No RailHost mounted: a reveal is a no-op-safe call (railController stub).
    expect(() => revealSessionInRail("local:x")).not.toThrow();
  });
});
