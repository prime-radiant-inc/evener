import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { StubResizeObserver } from "../resizeObserverTestUtils";
import { AppShell } from "../shell/AppShell";
import { resetWorkspaceStoreForTests } from "../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { connectionStore } from "../stores/connection";
import { resetNavigationStoreForTests } from "../stores/navigation/store";
import { resetThreadsStoreForTests } from "../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../stores/transcriptDisplay";

// Simulate only the browser API boundary, not the production hook or its effects.
class VisualViewportBoundary extends EventTarget {
  height = 620;
  offsetTop = 0;
  scale = 1;

  change(event: "resize" | "scroll", values: Partial<Pick<VisualViewportBoundary, "height" | "offsetTop" | "scale">>) {
    Object.assign(this, values);
    act(() => this.dispatchEvent(new Event(event)));
  }
}

const inset = () => document.documentElement.style.getPropertyValue("--keyboard-inset");

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  vi.stubGlobal("ResizeObserver", StubResizeObserver);
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
  resetTranscriptDisplayStoreForTests();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle" });
  resetThreadsStoreForTests();
  resetNavigationStoreForTests();
  resetWorkspaceStoreForTests();
  resetTranscriptDisplayStoreForTests();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

test("workspace owns keyboard resize, viewport scroll, zoom and close with Comic selected", async () => {
  const viewport = new VisualViewportBoundary();
  vi.stubGlobal("visualViewport", viewport);
  vi.stubGlobal("innerHeight", 620);
  transcriptDisplayStore.getState().setPresentation("desktop", "comic");
  transcriptDisplayStore.getState().setPresentation("mobile", "comic");
  let view!: ReturnType<typeof render>;
  await act(async () => {
    view = render(<AppShell client={new FakeClient("ready")} />);
  });
  try {
    expect(inset()).toBe("0px");
    viewport.change("resize", { height: 360 });
    expect(inset()).toBe("260px");
    viewport.change("scroll", { offsetTop: 40 });
    expect(inset()).toBe("220px");
    viewport.change("resize", { scale: 2 });
    expect(inset()).toBe("0px");
    viewport.change("resize", { scale: 1 });
    expect(inset()).toBe("220px");
    viewport.change("resize", { height: 620, offsetTop: 0 });
    expect(inset()).toBe("0px");
    viewport.change("scroll", { offsetTop: 20 });
    expect(inset()).toBe("0px");
  } finally {
    view.unmount();
  }
  expect(inset()).toBe("");
  viewport.change("resize", { height: 300, offsetTop: 0 });
  viewport.change("scroll", { offsetTop: 40 });
  expect(inset()).toBe("");
});

test("presentation switches retain keyboard ownership until the workspace unmounts", async () => {
  const viewport = new VisualViewportBoundary();
  viewport.height = 360;
  vi.stubGlobal("visualViewport", viewport);
  vi.stubGlobal("innerHeight", 620);
  let view!: ReturnType<typeof render>;
  await act(async () => {
    view = render(<AppShell client={new FakeClient("ready")} />);
  });
  try {
    expect(inset()).toBe("260px");
    await act(async () => transcriptDisplayStore.getState().setPresentation("desktop", "comic"));
    expect(inset()).toBe("260px");
    viewport.change("scroll", { offsetTop: 40 });
    expect(inset()).toBe("220px");
    await act(async () => transcriptDisplayStore.getState().setPresentation("desktop", "standard"));
    expect(inset()).toBe("220px");
    viewport.change("resize", { height: 620, offsetTop: 0 });
    expect(inset()).toBe("0px");
  } finally {
    view.unmount();
  }
  expect(inset()).toBe("");
  viewport.change("resize", { height: 300 });
  expect(inset()).toBe("");
});
