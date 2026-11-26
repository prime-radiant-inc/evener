import { afterEach, expect, test, vi } from "vitest";
import { connectIPhoneGlass } from "./iPhoneGlass";

const create = vi.hoisted(() => vi.fn());
vi.mock("@glass-sdk/liquid-glass", () => ({
  createGlassScene: create,
  TelemetryCounter: { workerGlass: "worker.glass" },
}));

afterEach(() => {
  document.body.replaceChildren();
  delete document.body.dataset.iphoneGlass;
  delete document.documentElement.dataset.theme;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

test("only the active session owns the worker and its inline and portaled surfaces", async () => {
  const contrast = Object.assign(new EventTarget(), { matches: true });
  const reduce = Object.assign(new EventTarget(), { matches: false });
  vi.stubGlobal("matchMedia", (query: string) => (query.includes("contrast") ? contrast : reduce));
  vi.spyOn(HTMLElement.prototype, "getClientRects").mockImplementation(function (this: HTMLElement) {
    return [this.getBoundingClientRect()] as unknown as DOMRectList;
  });
  const surfaces = new Set<Element>();
  let report: (() => void) | undefined;
  const worker = {
    error: null as string | null,
    appearance: "light",
    configure: vi.fn(),
    telemetry: {
      setEnabled: vi.fn(),
      subscribe(listener: (sample: unknown) => void) {
        report = () => listener({ counters: { "worker.glass": { count: 1 } } });
        return () => {
          report = undefined;
        };
      },
    },
    addSurface(element: HTMLElement) {
      surfaces.add(element);
      return { remove: () => surfaces.delete(element) };
    },
    destroy: vi.fn(() => {
      for (const element of surfaces) element.removeAttribute("data-lg-fallback");
      surfaces.clear();
    }),
  };
  create.mockImplementation(() => {
    worker.error = null;
    return worker;
  });
  const pane = document.createElement("div");
  pane.dataset.paneScaffoldRoot = "";
  const reader = document.createElement("section");
  reader.dataset.readingScrollHost = "";
  const form = document.createElement("form");
  form.dataset.iphoneComposer = "focused";
  const card = document.createElement("div");
  card.dataset.promptCard = "";
  card.id = "glass-test-trigger";
  form.append(card);
  pane.append(reader, form);
  document.body.append(pane);
  const stop = connectIPhoneGlass();
  try {
    expect(create).not.toHaveBeenCalled();
    document.body.dataset.iphoneGlass = "regular";
    await vi.waitFor(() => expect(surfaces.has(card)).toBe(true));
    expect(create.mock.calls[0]?.[0].root).toBe(reader);
    expect(card.hasAttribute("data-iphone-glass-drawn")).toBe(false);
    report?.();
    expect(card.hasAttribute("data-iphone-glass-drawn")).toBe(true);

    const menu = document.createElement("ul");
    menu.setAttribute("role", "menu");
    menu.setAttribute("aria-labelledby", card.id);
    const unrelated = document.createElement("ul");
    unrelated.setAttribute("role", "menu");
    const completion = document.createElement("div");
    completion.setAttribute("role", "listbox");
    card.append(completion);
    document.body.append(menu, unrelated);
    await Promise.resolve();
    expect(surfaces).toEqual(new Set([card, menu, completion]));
    expect(create).toHaveBeenCalledTimes(1);
    expect(menu.hasAttribute("data-iphone-glass-drawn")).toBe(false);
    report?.();
    expect(menu.hasAttribute("data-iphone-glass-drawn")).toBe(true);
    menu.remove();
    completion.remove();
    await Promise.resolve();
    expect(surfaces).toEqual(new Set([card]));
    expect(menu.hasAttribute("data-iphone-glass-drawn")).toBe(false);

    reader.style.colorScheme = "dark";
    document.documentElement.dataset.theme = "dark";
    await Promise.resolve();
    expect(worker.configure).toHaveBeenCalledWith({ appearance: "dark" });
    worker.error = "worker unavailable";
    card.setAttribute("data-lg-fallback", "");
    await Promise.resolve();
    await Promise.resolve();
    expect(card.hasAttribute("data-iphone-glass-drawn")).toBe(false);
    expect(create).toHaveBeenCalledTimes(1);
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(create).toHaveBeenCalledTimes(1);
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(create).toHaveBeenCalledTimes(2);
    report?.();
    expect(card.hasAttribute("data-iphone-glass-drawn")).toBe(true);
    const otherReader = reader.cloneNode() as HTMLElement;
    reader.replaceWith(otherReader);
    await Promise.resolve();
    expect(create.mock.lastCall?.[0].root).toBe(otherReader);
    expect(create).toHaveBeenCalledTimes(3);
    contrast.matches = false;
    contrast.dispatchEvent(new Event("change"));
    expect(surfaces.size).toBe(0);
  } finally {
    stop();
  }
  expect(report).toBeUndefined();
});
