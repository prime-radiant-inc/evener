import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { AppShell } from "../shell/AppShell";
import { ComicApp } from "./ComicApp";

vi.mock("../shell/useBrowserConnection", () => ({
  useBrowserConnection: () => ({ client: null, onClientReplaced: () => {} }),
}));

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

afterEach(() => {
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

test("Comic follows keyboard resize, viewport scroll, zoom and close with the shared hook", () => {
  const viewport = new VisualViewportBoundary();
  vi.stubGlobal("visualViewport", viewport);
  vi.stubGlobal("innerHeight", 620);
  window.history.replaceState(null, "", "/comic");
  const view = render(<ComicApp />);
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

test("Comic unmount releases viewport ownership before the regular shell mounts", () => {
  const viewport = new VisualViewportBoundary();
  viewport.height = 360;
  vi.stubGlobal("visualViewport", viewport);
  vi.stubGlobal("innerHeight", 620);
  window.history.replaceState(null, "", "/comic");
  const comic = render(<ComicApp />);
  try {
    expect(inset()).toBe("260px");
  } finally {
    comic.unmount();
  }
  expect(inset()).toBe("");
  viewport.change("scroll", { offsetTop: 40 });
  expect(inset()).toBe("");

  window.history.replaceState(null, "", "/");
  const regular = render(<AppShell client={new FakeClient("ready")} />);
  try {
    expect(inset()).toBe("220px");
    viewport.change("resize", { height: 620, offsetTop: 0 });
    expect(inset()).toBe("0px");
  } finally {
    regular.unmount();
  }
  viewport.change("resize", { height: 300 });
  expect(inset()).toBe("");
});
