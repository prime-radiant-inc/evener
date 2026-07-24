import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { prefsStore, resetPrefsStoreForTests } from "../../stores/prefs";
import { resetTreeStoreForTests, treeStore } from "../../stores/tree";
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
  resetPrefsStoreForTests();
  resetTreeStoreForTests();
  resetWorkspaceStoreForTests();
  // Quiet, resolving fetch so any mounted <Rail/> refresh() doesn't throw.
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse(emptyTree())),
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
// « hides to a top-left ☰ chip; the chip (or ⌘B) docks it back.
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
    expect(localStorage.getItem("serf.prefs.sidebarHidden")).toBe("1");

    resetPrefsStoreForTests();
    expect(prefsStore.getState().sidebarHidden).toBe(true);
  });

  test("the chip surfaces the needs-you count in its accessible name (color-is-attention badge)", () => {
    prefsStore.getState().setSidebarHidden(true);
    treeStore.setState({ tree: emptyTree(3) });
    render(<RailHost />);
    expect(screen.getByRole("button", { name: /show sidebar.*3 need attention/i })).toBeTruthy();
  });
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
