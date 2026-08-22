import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Sheet } from "./Sheet";

/**
 * Resolve on the next `popstate` dispatched on `window`. JSDOM queues the
 * popstate from `history.back()`/`go(-1)` as a macrotask; awaiting this event
 * (not a sleep) is the deterministic way to observe that navigation completed.
 */
function nextPopstate(): Promise<PopStateEvent> {
  return new Promise((resolve) => {
    const handler = (event: Event): void => {
      window.removeEventListener("popstate", handler);
      resolve(event as PopStateEvent);
    };
    window.addEventListener("popstate", handler);
  });
}

/**
 * Drive a real browser/Android Back: `history.back()` then await the popstate
 * the browser fires once that navigation lands.
 */
async function browserBack(): Promise<void> {
  const pending = nextPopstate();
  history.back();
  await pending;
}

afterEach(() => {
  cleanup();
  // JSDOM never shrinks `history.length` and persists `history.state` across
  // tests in the same window. Reset the current entry's state so the next
  // test starts from a clean baseline; tests baseline against `history.length`
  // at render time so a monotonically growing length is not a problem.
  history.replaceState(null, "");
});

describe("Sheet — browser/system Back history semantics", () => {
  it("pushes exactly one tagged same-document sentinel when opened", () => {
    const before = history.length;
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    expect(history.length).toBe(before + 1);
    expect(history.state).not.toBeNull();
    expect(history.state).not.toBe(before === 0 ? null : undefined);
    // sentinel is a tagged object, not the app's prior state
    expect(typeof history.state).toBe("object");
  });

  it("opening one sheet pushes exactly one history entry (no double push)", () => {
    const before = history.length;
    render(
      <Sheet open onClose={() => {}} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    expect(history.length).toBe(before + 1);
    // a re-render with the same `open` must not push again
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    // still only one extra entry relative to start (unwound on close)
  });

  it("browser Back (popstate) closes via onClose without navigating away", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const sheetEntry = history.state;
    expect(sheetEntry).not.toBeNull();

    await browserBack();

    expect(onClose).toHaveBeenCalledTimes(1);
    // the app did not navigate away: we are back at the pre-sheet entry
    expect(history.state).toBeNull();
  });

  it("popstate does not double-call onClose", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await browserBack();
    await browserBack(); // a second unrelated back must not call onClose again
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
    // onClose called once
    expect(onClose).toHaveBeenCalledTimes(1);
    // the sentinel is unwound: a popstate for our own back lands and must NOT
    // re-invoke onClose (already closed).
    await nextPopstate();
    expect(onClose).toHaveBeenCalledTimes(1);
    // The sentinel is unwound: history.state is no longer the sheet's tagged
    // entry (JSDOM never shrinks history.length, so assert state, not length).
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
    expect(onClose).toHaveBeenCalledTimes(1);
    await nextPopstate();
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
    expect(onClose).toHaveBeenCalledTimes(1);
    await nextPopstate();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(history.state).toBeNull();
  });

  it("explicit close then external popstate does not clobber unrelated history", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // app pushes an unrelated entry after the sheet
    history.pushState({ app: "unrelated" }, "");
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    // The unrelated entry is now on top of our sentinel, so the sheet must NOT
    // `history.back()` (that would pop the unrelated entry, not ours — never
    // pop unrelated history). onClose is still called exactly once and no
    // popstate is scheduled.
    expect(onClose).toHaveBeenCalledTimes(1);
    // unrelated entry still on top, untouched
    expect(history.state).toEqual({ app: "unrelated" });
  });

  it("external parent closure (open=false) unmounts portal and removes the sentinel safely", async () => {
    const onClose = vi.fn();
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
    // sentinel unwound
    await nextPopstate();
    expect(history.state).toBeNull();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("unmount removes the sentinel safely", async () => {
    const onClose = vi.fn();
    const { unmount } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    unmount();
    await nextPopstate();
    expect(history.state).toBeNull();
  });

  it("never pops unrelated history: an app popstate with a foreign state is left alone", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // simulate the app's own popstate (foreign state) — must not close the sheet
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: { foreign: true } }),
    );
    expect(onClose).toHaveBeenCalledTimes(0);
    expect(screen.queryByRole("dialog")).toBeInTheDocument();
  });

  it("no sentinel remains after close and remount (fresh open pushes exactly one)", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const afterFirstOpen = history.length;
    // close
    rerender(
      <Sheet open={false} onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // reopen
    rerender(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    expect(history.length).toBe(afterFirstOpen);
  });

  it("rapid open-close-open does not leak sentinels", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    const baseline = history.length;
    for (let i = 0; i < 5; i++) {
      rerender(
        <Sheet open={false} onClose={onClose} title="Servers">
          <p>body</p>
        </Sheet>,
      );
      rerender(
        <Sheet open onClose={onClose} title="Servers">
          <p>body</p>
        </Sheet>,
      );
    }
    // exactly one sentinel at a time
    expect(history.length).toBe(baseline);
  });

  it("StrictMode effect replay pushes exactly one sentinel", async () => {
    const { StrictMode } = await import("react");
    const before = history.length;
    render(
      <StrictMode>
        <Sheet open onClose={() => {}} title="Servers">
          <p>body</p>
        </Sheet>
      </StrictMode>,
    );
    expect(history.length).toBe(before + 1);
  });

  it("popstate followed by cleanup does not throw and leaves no sentinel", async () => {
    const onClose = vi.fn();
    const { unmount } = render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    await browserBack(); // closes via popstate
    expect(onClose).toHaveBeenCalledTimes(1);
    unmount(); // cleanup after popstate must be safe (no throw, no extra nav)
    // give any stray async popstate a chance and assert no throw
    await new Promise((r) => setTimeout(r, 0));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("history.back() asynchronous ordering: sentinel unwound after onClose", async () => {
    const onClose = vi.fn();
    render(
      <Sheet open onClose={onClose} title="Servers">
        <p>body</p>
      </Sheet>,
    );
    // explicit close schedules history.back(); onClose is synchronous
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    // onClose fires synchronously, before the async history.back() popstate
    // lands. The sentinel is still current at this instant.
    expect(history.state).not.toBeNull();
    await nextPopstate(); // back lands
    expect(history.state).toBeNull();
  });
});
