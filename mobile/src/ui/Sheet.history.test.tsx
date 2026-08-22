import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  __resetSheetHistory,
  __sheetHistoryDebug,
  __sheetHistorySettled,
  Sheet,
} from "./Sheet";

/**
 * Real browsers structured-clone `history.state` on `pushState`/`replaceState`,
 * discarding symbol-keyed properties and breaking object identity. JSDOM does
 * NOT — it keeps the same reference. This seam patches `pushState` so the
 * state is structured-cloned (symbols dropped, identity broken), mirroring
 * real-Chrome semantics. Tests that rely on object identity or symbol markers
 * would fail under this seam exactly as they fail in real Chrome.
 */
function installStructuredCloneSeam(): () => void {
  const originalPush = history.pushState.bind(history);
  const originalReplace = history.replaceState.bind(history);
  const clone = (state: unknown): unknown =>
    state === null || state === undefined
      ? state
      : typeof structuredClone === "function"
        ? structuredClone(state)
        : JSON.parse(JSON.stringify(state));
  history.pushState = function patchedPushState(
    state: unknown,
    unused: string,
    url?: string | URL | null,
  ) {
    return originalPush.call(history, clone(state), unused, url ?? null);
  };
  history.replaceState = function patchedReplaceState(
    state: unknown,
    unused: string,
    url?: string | URL | null,
  ) {
    return originalReplace.call(history, clone(state), unused, url ?? null);
  };
  return () => {
    history.pushState = originalPush;
    history.replaceState = originalReplace;
  };
}

/** Resolve on the next `popstate` dispatched on `window`. */
function nextPopstate(): Promise<PopStateEvent> {
  return new Promise((resolve) => {
    const handler = (event: Event): void => {
      window.removeEventListener("popstate", handler);
      resolve(event as PopStateEvent);
    };
    window.addEventListener("popstate", handler);
  });
}

/** Drive a real browser/Android Back and await its popstate. */
async function browserBack(): Promise<void> {
  const pending = nextPopstate();
  history.back();
  await pending;
}

/** Drive browser Forward and await the exposed entry's popstate. */
async function browserForward(): Promise<void> {
  const pending = nextPopstate();
  history.forward();
  await pending;
}

/**
 * Await all pending coordinator traversals (unwinds, skips) so the history
 * stack is quiescent before the next assertion or test teardown.
 */
async function settled(): Promise<void> {
  await __sheetHistorySettled();
}

/** Read the current sheet sentinel token, or null if not a sheet marker. */
function currentToken(): string | null {
  const state = history.state as {
    __evener_sheet?: boolean;
    token?: string;
  } | null;
  if (state?.__evener_sheet === true && typeof state.token === "string") {
    return state.token;
  }
  return null;
}

let restoreStructuredClone: (() => void) | null = null;

beforeEach(() => {
  restoreStructuredClone = installStructuredCloneSeam();
});

afterEach(async () => {
  cleanup();
  await settled();
  __resetSheetHistory();
  vi.restoreAllMocks();
  // Reset the current entry so the next test starts from a clean baseline.
  history.replaceState(null, "");
  restoreStructuredClone?.();
  restoreStructuredClone = null;
});

describe("Sheet — browser/system Back history semantics", () => {
  it("pushes exactly one tagged same-document sentinel with a plain-string token when opened", () => {
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const state = history.state as Record<string, unknown> | null;
    expect(state).not.toBeNull();
    expect(state?.__evener_sheet).toBe(true);
    expect(typeof state?.token).toBe("string");
    const token = state?.token;
    expect(typeof token === "string" && token.length > 0).toBe(true);
    // No symbol-keyed properties survive structured clone.
    expect(Object.getOwnPropertySymbols(state as object).length).toBe(0);
  });

  it("opening one sheet pushes exactly one history entry (no double push)", () => {
    const pushState = vi.spyOn(history, "pushState");
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    expect(pushState).toHaveBeenCalledTimes(1);
    expect(currentToken()).not.toBeNull();
  });

  it("browser Back (popstate) closes via onClose without navigating away", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    expect(currentToken()).not.toBeNull();

    await browserBack();

    expect(onClose).toHaveBeenCalledTimes(1);
    // The app did not navigate away: we are back at the pre-sheet entry.
    expect(history.state).toBeNull();
  });

  it("browser Back preserves plain router fields and restores non-null app state", async () => {
    const appState = { route: "/servers", revision: 7 };
    history.replaceState(appState, "");
    const observedStates: unknown[] = [];
    const observe = (event: PopStateEvent) => observedStates.push(event.state);
    window.addEventListener("popstate", observe);
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );

    await browserBack();
    window.removeEventListener("popstate", observe);

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(observedStates).toHaveLength(1);
    // Router fields remain top-level even before the coordinator unwraps its
    // namespaced base metadata.
    expect(observedStates[0]).toMatchObject(appState);
    expect(history.state).toEqual(appState);
  });

  it("browser Back restores a non-object structured-clone state exactly", async () => {
    const appState = ["/servers", 7, true];
    history.replaceState(appState, "");
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );

    await browserBack();

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(history.state).toEqual(appState);
  });

  it("system Back closes once without another back", async () => {
    const onClose = vi.fn();
    const backSpy = vi.spyOn(history, "back");
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    backSpy.mockClear();
    await browserBack();
    // browserBack itself calls history.back (the user's Back). Clear it so
    // we only count coordinator-initiated backs after the popstate settled.
    backSpy.mockClear();
    await settled();
    expect(onClose).toHaveBeenCalledTimes(1);
    // System Back must not schedule an additional history.back (no double nav).
    expect(backSpy).not.toHaveBeenCalled();
    expect(history.state).toBeNull();
  });

  it("Forward cannot expose a closed sentinel as a live history stop", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await browserBack();
    expect(__sheetHistoryDebug().forwardDead).toBe(true);

    await browserForward();
    await settled();

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(currentToken()).toBeNull();
    expect(history.state).toBeNull();
    expect(__sheetHistoryDebug()).toEqual({
      hasOwner: false,
      queueLength: 0,
      pendingBack: false,
      buriedCount: 0,
      forwardDead: false,
      listenerInstalled: false,
    });
  });

  it("an unrelated app push truncates the dead Forward branch and tears down the listener", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await browserBack();
    expect(__sheetHistoryDebug()).toMatchObject({
      forwardDead: true,
      listenerInstalled: true,
    });

    const appState = { route: "/new" };
    history.pushState(appState, "");

    expect(history.state).toEqual(appState);
    expect(__sheetHistoryDebug()).toEqual({
      hasOwner: false,
      queueLength: 0,
      pendingBack: false,
      buriedCount: 0,
      forwardDead: false,
      listenerInstalled: false,
    });
  });

  it("popstate does not double-call onClose", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await browserBack();
    // A second unrelated popstate (e.g., the app navigating further back) must
    // not call onClose again. Dispatch a synthetic popstate rather than a real
    // back, since at the bottom of the stack JSDOM may not fire popstate.
    window.dispatchEvent(new PopStateEvent("popstate", { state: null }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Done button closes through one idempotent path and unwinds the sentinel", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    await settled();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(history.state).toBeNull();
  });

  it("overlay click closes and unwinds the sentinel", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    fireEvent.click(
      document.querySelector(".evener-sheet-overlay") as HTMLElement,
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    await settled();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(history.state).toBeNull();
  });

  it("Escape closes (capture phase) and unwinds the sentinel", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    await settled();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(history.state).toBeNull();
  });

  it("explicit close requests at most one unwind (one history.back)", async () => {
    const onClose = vi.fn();
    const backSpy = vi.spyOn(history, "back");
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    backSpy.mockClear();
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(backSpy).toHaveBeenCalledTimes(1);
    await settled();
    // No additional back calls after the unwind settles.
    expect(backSpy).toHaveBeenCalledTimes(1);
    expect(history.state).toBeNull();
  });

  it("external parent closure (open=false) unmounts portal and unwinds the sentinel", async () => {
    const onClose = vi.fn();
    const backSpy = vi.spyOn(history, "back");
    const { rerender } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    rerender(
      <Sheet open={false} onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    expect(backSpy).toHaveBeenCalledTimes(1);
    await settled();
    expect(backSpy).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
    expect(history.state).toBeNull();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("unmount unwinds the sentinel safely", async () => {
    const onClose = vi.fn();
    const { unmount } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    unmount();
    await settled();
    expect(history.state).toBeNull();
  });

  it("never pops unrelated history: a foreign popstate is left alone", () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: { foreign: true } }),
    );
    expect(onClose).toHaveBeenCalledTimes(0);
    expect(screen.queryByRole("dialog")).toBeInTheDocument();
  });

  it("foreign route above sentinel: explicit close does not pop the unrelated entry and retires the token", () => {
    const onClose = vi.fn();
    const backSpy = vi.spyOn(history, "back");
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // App pushes an unrelated entry over the sentinel.
    history.pushState({ app: "unrelated" }, "");
    backSpy.mockClear();
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    // Must NOT back (would pop the unrelated entry).
    expect(backSpy).not.toHaveBeenCalled();
    // The unrelated entry is preserved on top.
    expect(history.state).toEqual({ app: "unrelated" });
  });

  it("foreign route above sentinel then real Back: unrelated entry preserved, inert sentinel skipped", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const sentinelToken = currentToken();
    expect(sentinelToken).not.toBeNull();
    // App pushes an unrelated entry over the sentinel.
    history.pushState({ app: "unrelated" }, "");
    // Close the sheet (token retired, no back since buried).
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(history.state).toEqual({ app: "unrelated" });
    // User presses Back over the unrelated entry — lands on the inert sentinel.
    await browserBack();
    // The inert sentinel must not close anything (already retired).
    expect(onClose).toHaveBeenCalledTimes(1);
    // The coordinator skips the inert sentinel: history.state is no longer the
    // sheet's marker.
    await settled();
    expect(currentToken()).not.toBe(sentinelToken);
    expect(__sheetHistoryDebug()).toEqual({
      hasOwner: false,
      queueLength: 0,
      pendingBack: false,
      buriedCount: 0,
      forwardDead: false,
      listenerInstalled: false,
    });
  });

  it("fresh token on reopen (no stale token reuse)", async () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const firstToken = currentToken();
    rerender(
      <Sheet open={false} onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await settled();
    rerender(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const secondToken = currentToken();
    expect(secondToken).not.toBe(firstToken);
  });

  it("no sentinel remains after close and remount (one sentinel at a time)", async () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const firstToken = currentToken();
    rerender(
      <Sheet open={false} onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await settled();
    rerender(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // The new sentinel has a fresh token, not the old one.
    const secondToken = currentToken();
    expect(secondToken).not.toBe(firstToken);
    expect(secondToken).not.toBeNull();
  });

  it("rapid open-close-open does not leak sentinels", async () => {
    const onClose = vi.fn();
    const pushState = vi.spyOn(history, "pushState");
    const { rerender } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const firstPushCount = pushState.mock.calls.length;
    for (let i = 0; i < 5; i++) {
      rerender(
        <Sheet open={false} onClose={onClose} title="Servers">
          <p>body</p>
        </Sheet>,
      );
      await settled();
      rerender(
        <Sheet open onClose={onClose} title="Servers">
          <p>body</p>
        </Sheet>,
      );
    }
    // Each reopen pushes exactly one sentinel; the close unwinds it. Total
    // pushState calls = initial + 5 reopens.
    expect(pushState.mock.calls.length).toBe(firstPushCount + 5);
    expect(currentToken()).not.toBeNull();
  });

  it("StrictMode effect replay pushes exactly one sentinel", async () => {
    const { StrictMode } = await import("react");
    const pushState = vi.spyOn(history, "pushState");
    render(
      <StrictMode>
        <Sheet open onClose={() => {}} title="Servers">
          <p>body</p>
        </Sheet>
      </StrictMode>,
    );
    await settled();
    // Exactly one sentinel push across the StrictMode double-invoke.
    expect(pushState).toHaveBeenCalledTimes(1);
    expect(currentToken()).not.toBeNull();
  });

  it("StrictMode replay followed by close requests exactly one unwind", async () => {
    const { StrictMode } = await import("react");
    const backSpy = vi.spyOn(history, "back");
    const { rerender } = render(
      <StrictMode>
        <Sheet open onClose={() => {}} title="Servers">
          <p>body</p>
        </Sheet>
      </StrictMode>,
    );
    backSpy.mockClear();
    rerender(
      <StrictMode>
        <Sheet open={false} onClose={() => {}} title="Servers">
          <p>body</p>
        </Sheet>
      </StrictMode>,
    );
    expect(backSpy).toHaveBeenCalledTimes(1);
    await settled();
    expect(backSpy).toHaveBeenCalledTimes(1);
  });

  it("popstate followed by cleanup does not throw and leaves no sentinel", async () => {
    const onClose = vi.fn();
    const { unmount } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await browserBack();
    expect(onClose).toHaveBeenCalledTimes(1);
    unmount();
    await settled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("history.back() completion precedes the parent close callback", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // The active sentinel is already inert while the traversal is pending.
    expect(currentToken()).toBeNull();
    expect(history.state).not.toBeNull();
    await settled();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(history.state).toBeNull();
  });

  it("queues an unrelated app push until the pending Back completes", async () => {
    const onClose = vi.fn();
    const backSpy = vi.spyOn(history, "back");
    const forwardSpy = vi.spyOn(history, "forward");
    const pushSpy = vi.spyOn(history, "pushState");
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );

    pushSpy.mockClear();
    const traversal = nextPopstate();
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(backSpy).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
    expect((history.state as Record<string, unknown>).__evener_sheet_dead).toBe(
      true,
    );

    const appEntry = { route: "/new", revision: 8 };
    history.pushState(appEntry, "");
    // The write is held at the shared boundary; it cannot become the entry that
    // the already-requested Back would pop.
    expect(pushSpy).not.toHaveBeenCalled();
    expect(history.state).not.toEqual(appEntry);

    await traversal;
    await settled();

    expect(forwardSpy).not.toHaveBeenCalled();
    expect(backSpy).toHaveBeenCalledTimes(1);
    expect(pushSpy).toHaveBeenCalledTimes(1);
    expect(history.state).toEqual(appEntry);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(__sheetHistoryDebug()).toEqual({
      hasOwner: false,
      queueLength: 0,
      pendingBack: false,
      buriedCount: 0,
      forwardDead: false,
      listenerInstalled: false,
    });
  });

  it("rapid close/reopen while unwind is pending does not let the old back pop the new sentinel", async () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const firstToken = currentToken();
    // Close — initiates an async history.back() (pending unwind).
    rerender(
      <Sheet open={false} onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // Immediately reopen before the back lands.
    rerender(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // The reopen is queued and hidden until the old traversal actually lands.
    expect(currentToken()).toBeNull();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await settled();
    const secondToken = currentToken();
    expect(secondToken).not.toBe(firstToken);
    expect(secondToken).not.toBeNull();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    // The old back landed before the new sentinel was pushed.
    expect(currentToken()).toBe(secondToken);
  });

  it("two concurrent instances serialize visibility and ownership", async () => {
    const onClose1 = vi.fn();
    const onClose2 = vi.fn();
    const pushState = vi.spyOn(history, "pushState");
    function Harness() {
      const [firstOpen, setFirstOpen] = useState(true);
      const [secondOpen, setSecondOpen] = useState(true);
      return (
        <>
          <Sheet
            open={firstOpen}
            onClose={() => {
              onClose1();
              setFirstOpen(false);
            }}
            title="First"
          >
            <p>first</p>
          </Sheet>
          <Sheet
            open={secondOpen}
            onClose={() => {
              onClose2();
              setSecondOpen(false);
            }}
            title="Second"
          >
            <p>second</p>
          </Sheet>
        </>
      );
    }
    render(<Harness />);
    // Only one sentinel pushed (one global owner enforced).
    expect(pushState).toHaveBeenCalledTimes(1);
    // Only the owner is visible; the queued sheet is not an orphan dialog.
    expect(screen.getByText("first")).toBeInTheDocument();
    expect(screen.queryByText("second")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose1).not.toHaveBeenCalled();
    expect(onClose2).not.toHaveBeenCalled();
    await settled();

    expect(onClose1).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("first")).not.toBeInTheDocument();
    expect(await screen.findByText("second")).toBeInTheDocument();
    expect(pushState).toHaveBeenCalledTimes(2);
  });

  it("Escape remains capture-phase and overlay is noninteractive to AT", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const overlay = document.querySelector(".evener-sheet-overlay");
    expect(overlay?.getAttribute("aria-hidden")).toBe("true");
    // Escape handler is registered on capture phase.
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    await settled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
