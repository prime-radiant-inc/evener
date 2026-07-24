import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { resetTreeStoreForTests } from "../../stores/tree";
import { resetWorkspaceStoreForTests } from "../workspace";
import { RailHost } from "./RailHost";
import { revealSessionInRail, setRailRevealHandler } from "./railController";

// Node 26 shadows jsdom's real localStorage with a non-functional global under
// vitest; RailHost mounts <Rail/>, whose prefs/store reads write through - the
// same in-memory stand-in every rail/shell test uses.
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

function emptyTree() {
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: [],
    needs_you: [],
    favorites: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
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

// Collapsed mode was removed outright (2026-07-24, Jesse's direction): the
// sidebar is ALWAYS docked - no ☰ chip, no overlay drawer, no sidebarMode
// preference, no ⌘B. RailHost is now just the reveal-seam wrapper around the
// one full-chrome <Rail/>.
describe("always-docked rail", () => {
  test("renders the full-chrome rail: search, + New session", () => {
    render(<RailHost />);
    expect(screen.getByTestId("rail-search")).toBeTruthy();
    expect(screen.getByRole("button", { name: /new session/i })).toBeTruthy();
  });

  test("no collapse affordances exist: no chip, no Hide sidebar", () => {
    render(<RailHost />);
    expect(screen.queryByRole("button", { name: /show sidebar/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /hide sidebar/i })).toBeNull();
  });

  test("⌘B is not intercepted (no mode to cycle; the browser keeps its default)", () => {
    render(<RailHost />);
    const event = new KeyboardEvent("keydown", { key: "b", metaKey: true, bubbles: true, cancelable: true });
    act(() => {
      window.dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(false);
  });
});

describe("reveal seam (railController /project)", () => {
  test("registers a handler; a reveal reaches the always-mounted rail without opening anything", () => {
    render(<RailHost />);
    // No drawer/dialog exists to open; the reveal is just a prop handoff.
    act(() => revealSessionInRail("local:abc"));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByTestId("rail-search")).toBeTruthy();
  });

  test("clears its handler on unmount", () => {
    const { unmount } = render(<RailHost />);
    unmount();
    // No RailHost mounted: a reveal is a no-op-safe call (railController stub).
    expect(() => revealSessionInRail("local:x")).not.toThrow();
  });
});
