import { act, cleanup, fireEvent, render as renderUI, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { manifest as navigationManifest } from "../../stores/navigation/testing";
import { prefsStore, resetPrefsStoreForTests, SIDEBAR_WIDTH_MAX } from "../../stores/prefs";
import { ClientProvider } from "../clientContext";
import { resetWorkspaceStoreForTests } from "../workspace";
import { RailHost } from "./RailHost";
import { revealSessionInRail, setRailRevealHandler } from "./railController";

// Node 26 shadows jsdom's real localStorage with a non-functional global under
// vitest; RailHost mounts <Rail/>, and prefsStore writes through on
// setSidebarHidden - the same in-memory stand-in every rail/shell test uses.
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

function emptyNavResponse(needsYou = 0) {
  return {
    generation_id: "test-generation",
    revision: 1,
    sources: [],
    attentionSummary: { needsYou, error: 0, working: 0 },
    sections: { live: { count: 0 }, needs_you: { count: needsYou }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  };
}

function jsonResponse(body: unknown): Response {
  return { ok: true, status: 200, statusText: "OK", json: () => Promise.resolve(body) } as Response;
}

function render(ui: ReactElement, client = new FakeClient()) {
  return renderUI(<ClientProvider client={client}>{ui}</ClientProvider>);
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
  resetPrefsStoreForTests();
  resetNavigationStoreForTests();
  resetNavigationStoreForTests();
  navigationStore.setState({ mode: "v1" });
  resetWorkspaceStoreForTests();
  // Quiet, resolving fetch so any mounted <Rail/> refresh() doesn't throw.
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse(emptyNavResponse())),
  );
});

afterEach(() => {
  cleanup();
  setRailRevealHandler(null);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// The old tri-state sidebarMode (auto/pane/rail overlay drawer) was removed
// 2026-07-24: desktop visibility is ONE persisted boolean. Docked by default;
// ☰ hides to a top-left ☰ chip (one glyph owns the toggle both ways); the
// chip (or ⌘B) docks it back.
describe("docked by default", () => {
  test("renders the full-chrome rail with a Hide sidebar affordance, no chip", () => {
    render(<RailHost />);
    expect(screen.getByTestId("rail-search")).toBeTruthy();
    expect(screen.getByRole("button", { name: /new session/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /hide sidebar/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /show sidebar/i })).toBeNull();
  });
});

describe("hide / show", () => {
  test("Hide sidebar collapses to the ☰ chip; the chip docks it back", async () => {
    const user = userEvent.setup();
    render(<RailHost />);

    await user.click(screen.getByRole("button", { name: /hide sidebar/i }));
    expect(prefsStore.getState().sidebarHidden).toBe(true);
    expect(screen.queryByTestId("rail-search")).toBeNull();

    await user.click(screen.getByRole("button", { name: /show sidebar/i }));
    expect(prefsStore.getState().sidebarHidden).toBe(false);
    expect(screen.getByTestId("rail-search")).toBeTruthy();
  });

  test("the hidden state persists (sidebarHidden boolean round-trips localStorage)", async () => {
    const user = userEvent.setup();
    render(<RailHost />);
    await user.click(screen.getByRole("button", { name: /hide sidebar/i }));
    expect(localStorage.getItem("evener.prefs.sidebarHidden")).toBe("1");

    resetPrefsStoreForTests();
    expect(prefsStore.getState().sidebarHidden).toBe(true);
  });

  test("the chip surfaces the needs-you count in its accessible name (color-is-attention badge)", () => {
    prefsStore.getState().setSidebarHidden(true);
    navigationStore.setState({
      mode: "v1",
      manifest: {
        key: { kind: "manifest" },
        data: navigationManifest({
          sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } },
        }),
        loadedRevision: 1,
        targetRevision: 1,
        forceToken: 0,
        etag: '"manifest"',
        loading: false,
        stale: false,
        error: null,
        generationID: "test-generation",
      },
    });
    render(<RailHost />);
    expect(screen.getByRole("button", { name: /show sidebar.*3 need attention/i })).toBeTruthy();
  });
});

test("v1 badge ignores stale legacy tree attention and uses manifest section count", () => {
  prefsStore.getState().setSidebarHidden(true);
  navigationStore.setState({
    mode: "v1",
    manifest: {
      key: { kind: "manifest" },
      data: navigationManifest({
        sections: { live: { count: 0 }, needs_you: { count: 2 }, pin_sections: { count: 0 } },
      }),
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: '"manifest"',
      loading: false,
      stale: false,
      error: null,
      generationID: "generation_test",
    },
  });
  render(<RailHost />);
  expect(screen.getByRole("button", { name: /show sidebar.*2 need attention/i })).toBeTruthy();
});

describe("⌘B toggles hidden", () => {
  function pressCmdB(): boolean {
    const event = new KeyboardEvent("keydown", { key: "b", metaKey: true, bubbles: true, cancelable: true });
    act(() => {
      window.dispatchEvent(event);
    });
    return event.defaultPrevented;
  }

  test("toggles hidden -> docked -> hidden and preventDefaults the browser shortcut", () => {
    render(<RailHost />);
    expect(pressCmdB()).toBe(true);
    expect(prefsStore.getState().sidebarHidden).toBe(true);
    pressCmdB();
    expect(prefsStore.getState().sidebarHidden).toBe(false);
  });

  // Ctrl+B is the macOS emacs-style "move cursor back" binding many native
  // text fields honor - without a focus guard, this chord would hijack it
  // mid-typing (RailHost.tsx accepts meta OR ctrl for cross-platform ⌘B).
  test("Ctrl+B does not toggle when the target is a focused textarea, and does not preventDefault", () => {
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

    expect(prefsStore.getState().sidebarHidden).toBe(false);
    expect(event.defaultPrevented).toBe(false);
  });
});

// The docked rail's width is a pref, not component state, specifically so that
// hiding/re-showing (and a reload) restores the dragged width.
describe("resizable width", () => {
  function separator(): HTMLElement {
    return screen.getByTestId("rail-resize-handle");
  }

  test("the handle reports the persisted width", () => {
    prefsStore.getState().setSidebarWidth(360);
    render(<RailHost />);
    expect(separator().getAttribute("aria-valuenow")).toBe("360");
  });

  test("a keyboard resize persists to evener.prefs.sidebarWidth and survives a remount", () => {
    const { unmount } = render(<RailHost />);
    act(() => {
      fireEvent.keyDown(separator(), { key: "End" });
    });
    expect(localStorage.getItem("evener.prefs.sidebarWidth")).toBe(String(SIDEBAR_WIDTH_MAX));

    unmount();
    resetPrefsStoreForTests(); // exactly what a reload's fresh hydration does
    render(<RailHost />);
    expect(separator().getAttribute("aria-valuenow")).toBe(String(SIDEBAR_WIDTH_MAX));
  });

  test("hiding and re-docking the sidebar restores the resized width", async () => {
    const user = userEvent.setup();
    render(<RailHost />);
    act(() => {
      fireEvent.keyDown(separator(), { key: "ArrowRight" });
    });
    const resized = separator().getAttribute("aria-valuenow");

    await user.click(screen.getByRole("button", { name: /hide sidebar/i }));
    expect(screen.queryByTestId("rail-resize-handle")).toBeNull(); // nothing to resize while hidden
    await user.click(screen.getByRole("button", { name: /show sidebar/i }));

    expect(separator().getAttribute("aria-valuenow")).toBe(resized);
  });

  // Below 899px the Rail fills TreeDrawer's Sheet (Rail.module.css's own
  // max-width:899px block sets width:100%) - there is no rail edge to grab, so
  // the handle must not render at all rather than render inert.
  test("no handle in the mobile drawer", () => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn((media: string) => ({
        media,
        matches: media === "(max-width: 899px)",
        addEventListener: () => {},
        removeEventListener: () => {},
      })),
    );
    render(<RailHost />);
    expect(screen.getByTestId("rail-search")).toBeTruthy(); // the rail itself is there
    expect(screen.queryByTestId("rail-resize-handle")).toBeNull();
  });
});

describe("reveal seam (railController /project)", () => {
  test("revealing while hidden docks the rail first so the row can scroll into view", () => {
    prefsStore.getState().setSidebarHidden(true);
    render(<RailHost />);
    expect(screen.queryByTestId("rail-search")).toBeNull();

    act(() => revealSessionInRail("local:abc"));
    expect(prefsStore.getState().sidebarHidden).toBe(false);
    expect(screen.getByTestId("rail-search")).toBeTruthy();
  });

  test("clears its handler on unmount", () => {
    const { unmount } = render(<RailHost />);
    unmount();
    // No RailHost mounted: a reveal is a no-op-safe call (railController stub).
    expect(() => revealSessionInRail("local:x")).not.toThrow();
  });
});
