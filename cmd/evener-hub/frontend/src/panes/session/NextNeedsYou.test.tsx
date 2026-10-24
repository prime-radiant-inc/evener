// @vitest-environment jsdom
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { NeedsYouProvider } from "../../shell/NeedsYouProvider";
import { installNeedsYouPage, needsYouRow, updateNeedsYouPage } from "../../shell/needsYouTestUtils";
import { closePalette, openPalette } from "../../shell/palette/paletteController";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { NextNeedsYou } from "./NextNeedsYou";
import "./index";

const row = needsYouRow;

function installRows(rows: NavigationSessionSummary[] = [row("local:a"), row("local:b")], manifestCount = rows.length) {
  installNeedsYouPage(0, rows);
  navigationStore.setState({
    mode: "v3",
    manifest: {
      key: { kind: "manifest" },
      data: {
        generation_id: "g",
        revision: 1,
        sources: [],
        attentionSummary: { needsYou: manifestCount, error: 0, working: 0 },
        sections: {
          live: { count: 0 },
          needs_you: { count: manifestCount },
          pin_sections: { count: 0 },
        },
        catalogs: {
          projects: { count: 0 },
          archived_projects: { count: 0 },
          test_runs: { count: 0 },
        },
      },
      loadedRevision: 1,
      targetRevision: null,
      forceToken: 0,
      etag: null,
      loading: false,
      stale: false,
      error: null,
      generationID: "g",
    },
  });
}

function seedSource(): string {
  window.history.replaceState({}, "", "/s/local%3Aa");
  const id = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(id);
  return id;
}

function controlTree(hidden = false): ReactElement {
  return (
    <NeedsYouProvider>
      <NextNeedsYou sessionRef="local:a" viewId="view:a" hidden={hidden} />
    </NeedsYouProvider>
  );
}

function nextTrigger(): HTMLElement {
  return screen.getByTestId("next-needs-you-next");
}

function whenNavigation(predicate: (state: ReturnType<typeof navigationStore.getState>) => boolean): Promise<void> {
  if (predicate(navigationStore.getState())) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = navigationStore.subscribe((state) => {
      if (!predicate(state)) return;
      unsubscribe();
      resolve();
    });
  });
}

beforeEach(() => {
  resetNavigationStoreForTests();
  resetWorkspaceStoreForTests();
  closePalette();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  closePalette();
  resetNavigationStoreForTests();
  resetWorkspaceStoreForTests();
  window.history.replaceState({}, "", "/");
});

test("renders nothing when the shell has no needs-you owner", () => {
  const view = render(<NextNeedsYou sessionRef="local:a" viewId="view:a" hidden={false} />);

  expect(view.container.firstChild).toBeNull();
});

test("hold opens once without navigating", () => {
  vi.useFakeTimers();
  installRows();
  const origin = seedSource();
  render(controlTree());

  expect(document.querySelectorAll('[data-testid="next-needs-you"]')).toHaveLength(1);
  const next = nextTrigger();

  fireEvent.pointerDown(next, { button: 0, clientX: 10, clientY: 10, pointerId: 1 });
  act(() => vi.advanceTimersByTime(499));
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
  expect(window.location.pathname).toBe("/s/local%3Aa");

  act(() => vi.advanceTimersByTime(1));
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();
  expect(workspaceStore.getState().focusedPaneId).toBe(origin);
  expect(window.location.pathname).toBe("/s/local%3Aa");

  fireEvent.pointerUp(next, { clientX: 10, clientY: 10, pointerId: 1 });
  fireEvent.click(next);
  // The release click belongs to the completed hold: it opens nothing else and
  // must not also navigate.
  expect(window.location.pathname).toBe("/s/local%3Aa");
  expect(workspaceStore.getState().focusedPaneId).toBe(origin);
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();

  // The next intentional click is the trigger's ordinary action.
  fireEvent.click(next);
  expect(window.location.pathname).toBe("/s/local%3Ab");
  expect(workspaceStore.getState().focusedPaneId).not.toBe(origin);
});

test("a hold longer than the click window still consumes its release click", () => {
  vi.useFakeTimers();
  installRows();
  const origin = seedSource();
  render(controlTree());
  const next = nextTrigger();

  fireEvent.pointerDown(next, { button: 0, clientX: 10, clientY: 10, pointerId: 5 });
  // The list opens at 500 ms. Keep the pointer down well past that plus the
  // 350 ms click window, so a suppression window that started at completion
  // would already have expired while the pointer is still held.
  act(() => vi.advanceTimersByTime(1200));
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();

  fireEvent.pointerUp(next, { clientX: 10, clientY: 10, pointerId: 5 });
  fireEvent.click(next);
  // However long the hold lasted, its release click belongs to the hold.
  expect(window.location.pathname).toBe("/s/local%3Aa");
  expect(workspaceStore.getState().focusedPaneId).toBe(origin);
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();
});

test("a completed hold keeps its suppression through a drift and a slow release", () => {
  vi.useFakeTimers();
  installRows();
  const origin = seedSource();
  render(controlTree());
  const next = nextTrigger();

  fireEvent.pointerDown(next, { button: 0, clientX: 10, clientY: 10, pointerId: 7 });
  act(() => vi.advanceTimersByTime(600));
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();

  // The hold already completed. The pointer then drifts off the button and its
  // capture goes away while the press is still down - the sequence that used to
  // arm the click window early, so a release inside Next more than 350ms later
  // navigated the reader away instead of belonging to the hold.
  fireEvent.pointerMove(next, { clientX: 80, clientY: 10, pointerId: 7 });
  fireEvent.lostPointerCapture(next, { pointerId: 7 });
  act(() => vi.advanceTimersByTime(600));
  fireEvent.pointerUp(next, { clientX: 20, clientY: 10, pointerId: 7 });
  fireEvent.click(next);

  expect(window.location.pathname).toBe("/s/local%3Aa");
  expect(workspaceStore.getState().focusedPaneId).toBe(origin);
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();
});

test("cancellation does not poison the next click", () => {
  vi.useFakeTimers();
  installRows();
  seedSource();
  render(controlTree());
  const next = nextTrigger();

  fireEvent.pointerDown(next, { button: 0, clientX: 10, clientY: 10, pointerId: 2 });
  // Ten CSS pixels is still a hold; eleven is a scroll.
  fireEvent.pointerMove(next, { clientX: 21, clientY: 10, pointerId: 2 });
  act(() => vi.advanceTimersByTime(600));
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
  fireEvent.pointerUp(next, { pointerId: 2 });

  fireEvent.click(next);
  expect(window.location.pathname).toBe("/s/local%3Ab");
});

// A conversation in a Dockview popout publishes its pointer events in that
// window, so a gesture bound to the opener's window never sees the movement that
// should cancel the hold. jsdom cannot dispatch a cross-window pointer event,
// so this pins the binding itself: the press has to listen on its own document
// and not on the opener's window. Movement cancellation itself is covered
// behaviorally by the cases around it.
test("a hold listens for its gesture on the button's own document", ({ onTestFinished }) => {
  vi.useFakeTimers();
  installRows();
  seedSource();
  render(controlTree());
  const next = nextTrigger();
  const documentListeners = vi.spyOn(document, "addEventListener");
  const windowListeners = vi.spyOn(window, "addEventListener");
  onTestFinished(() => {
    documentListeners.mockRestore();
    windowListeners.mockRestore();
  });

  fireEvent.pointerDown(next, { button: 0, clientX: 10, clientY: 10, pointerId: 13 });

  const boundTo = (spy: typeof documentListeners) => spy.mock.calls.map(([type]) => type);
  expect(boundTo(documentListeners)).toContain("pointermove");
  expect(boundTo(documentListeners)).toContain("pointerup");
  expect(boundTo(windowListeners)).not.toContain("pointermove");
});

test("a hidden document cancels a pending hold and never consumes the next click", () => {
  vi.useFakeTimers();
  installRows();
  seedSource();
  render(controlTree());
  const next = nextTrigger();
  const visibility = vi.spyOn(document, "visibilityState", "get");

  fireEvent.pointerDown(next, { button: 0, clientX: 10, clientY: 10, pointerId: 4 });
  act(() => vi.advanceTimersByTime(200));
  visibility.mockReturnValue("hidden");
  act(() => fireEvent(document, new Event("visibilitychange")));
  act(() => vi.advanceTimersByTime(600));
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();

  visibility.mockReturnValue("visible");
  act(() => fireEvent(document, new Event("visibilitychange")));
  fireEvent.pointerUp(next, { pointerId: 4 });
  // The cancelled hold owns no click: this one navigates as usual.
  fireEvent.click(next);
  expect(window.location.pathname).toBe("/s/local%3Ab");
  visibility.mockRestore();
});

test.each(["release", "cancel", "lost-capture", "unmount"] as const)(
  "an early %s leaves the control usable and never opens the list",
  (ending) => {
    vi.useFakeTimers();
    installRows();
    seedSource();
    const view = render(controlTree());
    const next = nextTrigger();

    fireEvent.pointerDown(next, { button: 0, clientX: 10, clientY: 10, pointerId: 3 });
    act(() => vi.advanceTimersByTime(400));
    if (ending === "release") fireEvent.pointerUp(next, { pointerId: 3 });
    if (ending === "cancel") fireEvent.pointerCancel(next, { pointerId: 3 });
    if (ending === "lost-capture") fireEvent.lostPointerCapture(next, { pointerId: 3 });
    if (ending === "unmount") view.unmount();
    act(() => vi.advanceTimersByTime(600));

    expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
    if (ending !== "unmount") {
      fireEvent.click(next);
      expect(window.location.pathname).toBe("/s/local%3Ab");
    }
  },
);

test("More lists the other sessions with full accessible titles and restores the trigger on Escape", () => {
  installRows([
    row("local:a"),
    row("local:b", "A deliberately long session title that is visually truncated by the row"),
    row("local:c"),
  ]);
  seedSource();
  render(controlTree());

  const more = screen.getByTestId("next-needs-you-more");
  expect(more.textContent).toContain("…");
  expect(more.textContent).not.toContain("▾");
  more.focus();
  fireEvent.click(more);

  const list = screen.getByTestId("next-needs-you-list");
  expect(list).toBeTruthy();
  const longRow = screen.getByRole("button", {
    name: "A deliberately long session title that is visually truncated by the row",
  });
  expect(longRow).toBeTruthy();
  // Focus moves into the independent list, not into the editor.
  expect(document.activeElement).toBe(longRow);

  fireEvent.keyDown(longRow, { key: "Escape" });
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
  expect(document.activeElement).toBe(more);
});

test("outside dismissal leaves focus where the click put it", () => {
  installRows();
  seedSource();
  render(controlTree());
  const more = screen.getByTestId("next-needs-you-more");
  fireEvent.click(more);
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();

  const editor = document.createElement("textarea");
  document.body.append(editor);
  // jsdom dispatches no default focus action for mousedown, so this focus is
  // where the browser's click would leave it. Nothing below restores it: a
  // dismissal that moved focus would show up in the assertion.
  editor.focus();
  fireEvent.mouseDown(editor);
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
  expect(document.activeElement).toBe(editor);
  editor.remove();
});

test("outer scroll dismisses the list while its own scrolling does not", () => {
  installRows();
  seedSource();
  render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));
  const longRow = screen.getAllByTestId("next-needs-you-row")[0];
  if (!longRow) throw new Error("expected a list row");

  fireEvent.scroll(longRow);
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();

  fireEvent.scroll(document.body);
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
});

test("a row whose page turns stale after render is refused and never navigates", () => {
  installRows([row("local:a"), row("local:b")]);
  const origin = seedSource();
  render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));
  const rowButton = screen.getByTestId("next-needs-you-row");
  expect(rowButton.getAttribute("data-ref")).toBe("local:b");
  expect(rowButton.getAttribute("aria-disabled")).toBeNull();

  act(() => updateNeedsYouPage(0, { stale: true, loading: true }));

  expect(rowButton.getAttribute("aria-disabled")).toBe("true");
  expect(rowButton.getAttribute("data-refresh-state")).toBe("refreshing");

  act(() => updateNeedsYouPage(0, { stale: true, loading: false, error: new Error("refresh failed") }));
  expect(rowButton.getAttribute("aria-disabled")).toBe("true");
  expect(rowButton.getAttribute("data-refresh-state")).toBe("failed");
  fireEvent.click(rowButton);

  expect(window.location.pathname).toBe("/s/local%3Aa");
  expect(workspaceStore.getState().focusedPaneId).toBe(origin);
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();
});

test("opening a list with refused rows focuses the first available control", () => {
  installRows([row("local:a"), row("local:b"), row("local:c")]);
  seedSource();
  updateNeedsYouPage(0, { stale: true, error: new Error("refresh failed") });

  render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));

  expect(document.activeElement).toBe(screen.getByTestId("next-needs-you-load-more"));
  for (const refused of screen.getAllByTestId("next-needs-you-row")) {
    expect(refused.getAttribute("data-refresh-state")).toBe("failed");
  }
});

test("opening a list with no available control focuses the list", () => {
  installRows([row("local:a"), row("local:b")]);
  seedSource();
  act(() => updateNeedsYouPage(0, { stale: true, loading: true }));

  render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));

  expect(document.activeElement).toBe(screen.getByLabelText("Needs you sessions"));
});

test("a refused page does not refuse rows from another current page", () => {
  installRows([row("local:a"), row("local:b")]);
  installNeedsYouPage(50, [row("local:c")]);
  updateNeedsYouPage(0, { stale: true, error: new Error("refresh failed") });
  seedSource();
  render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));

  const rows = screen.getAllByTestId("next-needs-you-row");
  const refused = rows.find((item) => item.getAttribute("data-ref") === "local:b");
  const current = rows.find((item) => item.getAttribute("data-ref") === "local:c");
  if (!refused || !current) throw new Error("expected the refused and current rows");
  expect(refused.getAttribute("aria-disabled")).toBe("true");
  expect(refused.getAttribute("data-refresh-state")).toBe("failed");
  expect(current.getAttribute("aria-disabled")).toBeNull();
  expect(current.getAttribute("data-refresh-state")).toBeNull();
  expect(document.activeElement).toBe(current);

  fireEvent.click(current);
  expect(window.location.pathname).toBe("/s/local%3Ac");
});

test("a selectable row closes the list and navigates", () => {
  installRows([row("local:a"), row("local:b")]);
  const origin = seedSource();
  render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));

  fireEvent.click(screen.getByTestId("next-needs-you-row"));

  expect(window.location.pathname).toBe("/s/local%3Ab");
  expect(workspaceStore.getState().focusedPaneId).not.toBe(origin);
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
});

test("a refused row recovers when its page becomes current again", () => {
  installRows([row("local:a"), row("local:b")]);
  const origin = seedSource();
  render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));

  act(() => updateNeedsYouPage(0, { stale: true, loading: true }));
  const refused = screen.getByTestId("next-needs-you-row");
  expect(refused.getAttribute("aria-disabled")).toBe("true");
  expect(refused.getAttribute("data-refresh-state")).toBe("refreshing");
  act(() => updateNeedsYouPage(0, { stale: false, loading: false }));

  const rowButton = screen.getByTestId("next-needs-you-row");
  expect(rowButton.getAttribute("aria-disabled")).toBeNull();
  fireEvent.click(rowButton);

  expect(window.location.pathname).toBe("/s/local%3Ab");
  expect(workspaceStore.getState().focusedPaneId).not.toBe(origin);
});

test("hidden and shared blocking suppression hide the control and clear automatically", async () => {
  installRows();
  seedSource();
  const view = render(controlTree());
  expect(screen.getByTestId("next-needs-you")).toBeTruthy();

  view.rerender(controlTree(true));
  expect(screen.queryByTestId("next-needs-you")).toBeNull();
  view.rerender(controlTree(false));
  expect(screen.getByTestId("next-needs-you")).toBeTruthy();

  act(() => openPalette());
  await waitFor(() => expect(screen.queryByTestId("next-needs-you")).toBeNull());
  act(() => closePalette());
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());

  const modal = document.createElement("div");
  modal.setAttribute("aria-modal", "true");
  document.body.append(modal);
  await waitFor(() => expect(screen.queryByTestId("next-needs-you")).toBeNull());
  modal.remove();
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());
});

test("the control hides when the known queue holds no other eligible session", () => {
  installRows([row("local:a")], 1);
  seedSource();
  const view = render(controlTree());
  expect(screen.queryByTestId("next-needs-you")).toBeNull();

  act(() => {
    installRows([row("local:a"), row("local:b")], 2);
  });
  expect(screen.getByTestId("next-needs-you")).toBeTruthy();
  view.unmount();
});

test("More requests the next bounded page when more data exists", async () => {
  const client = new FakeClient("ready");
  const offsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "paging",
          sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "paging",
      );
    }
    if (params.resource === "section" && params.section === "needs_you") {
      offsets.push(params.offset ?? -1);
      const first = (params.offset ?? 0) === 0;
      return wireSnapshot(
        params,
        { sessions: first ? [row("local:a"), row("local:b")] : [row("local:c")], remaining: first ? 1 : 0 },
        `"page-${params.offset}"`,
        1,
        "paging",
      );
    }
    throw new Error(`unexpected navigation resource ${params.resource}`);
  });
  let release = () => {};
  try {
    await act(async () => {
      release = navigationStore.init(client, capability("paging"));
      await whenNavigation(
        (state) =>
          !!state.manifest?.data &&
          [...state.resources.values()].some(
            (resource) =>
              resource.key.kind === "section" &&
              resource.key.section === "needs_you" &&
              !!resource.data &&
              !resource.loading,
          ),
      );
    });
    seedSource();
    render(controlTree());
    fireEvent.click(screen.getByTestId("next-needs-you-more"));
    expect(screen.getByTestId("next-needs-you-load-more")).toBeTruthy();

    await act(async () => {
      fireEvent.click(screen.getByTestId("next-needs-you-load-more"));
      await whenNavigation((state) =>
        [...state.resources.values()].some(
          (resource) =>
            resource.key.kind === "section" &&
            resource.key.section === "needs_you" &&
            resource.key.offset === 2 &&
            !!resource.data &&
            !resource.loading,
        ),
      );
    });
    expect(offsets).toEqual([0, 2]);
    expect(screen.getAllByTestId("next-needs-you-row")).toHaveLength(2);
    expect(screen.queryByTestId("next-needs-you-load-more")).toBeNull();
    await act(async () => {});
  } finally {
    await act(async () => {
      release();
    });
  }
});

test("a hidden transition cancels the open list and a later click still works", () => {
  installRows();
  const originId = seedSource();
  const view = render(controlTree());
  fireEvent.click(screen.getByTestId("next-needs-you-more"));
  expect(screen.getByTestId("next-needs-you-list")).toBeTruthy();

  view.rerender(controlTree(true));
  expect(screen.queryByTestId("next-needs-you-list")).toBeNull();
  expect(screen.queryByTestId("next-needs-you")).toBeNull();

  view.rerender(controlTree(false));
  expect(nextTrigger().getAttribute("aria-label")).toContain("local:b");
  expect(workspaceStore.getState().focusedPaneId).toBe(originId);
  fireEvent.click(nextTrigger());
  const focused = workspaceStore.getState().panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId);
  expect(focused?.id).not.toBe(originId);
  expect(focused?.params).toEqual({ ref: "local:b" });
  // The hop must move the URL with the pane, or Back and reload land on the
  // session the reader came from. An earlier probe of this case reported a
  // path/pane disagreement, so pin it here rather than trusting pane identity.
  expect(window.location.pathname).toBe("/s/local%3Ab");
});

test("the next action button names the full next session even when its display truncates", () => {
  installRows([row("local:a"), row("local:b", "A long next-session title that truncates in the control")]);
  seedSource();
  render(controlTree());
  const next = nextTrigger();
  expect(next.getAttribute("aria-label")).toContain("A long next-session title that truncates in the control");
});
