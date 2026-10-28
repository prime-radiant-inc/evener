import { afterEach, expect, test, vi } from "vitest";
import { createComicMeasurer } from "./measure";

afterEach(() => {
  document.body.replaceChildren();
});

test("creates and measures the hidden panel in its container's document", () => {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  const foreignDocument = frame.contentDocument;
  const foreignWindow = frame.contentWindow;
  if (!foreignDocument || !foreignWindow) throw new Error("iframe document unavailable");
  const container = foreignDocument.createElement("div");
  foreignDocument.body.append(container);
  Object.defineProperty(foreignWindow, "innerWidth", { configurable: true, value: 777 });
  const create = vi.spyOn(foreignDocument, "createElement");
  const getStyle = vi.spyOn(foreignWindow, "getComputedStyle");

  const measurer = createComicMeasurer(container);
  measurer.fits("a visible source", "wide", "dialogue");
  const probe = container.querySelector("[data-comic-probe]");
  expect(create).toHaveBeenCalledWith("article");
  expect(getStyle).toHaveBeenCalledWith(probe?.querySelector("p"));
  expect(probe?.ownerDocument).toBe(foreignDocument);
  expect(probe?.querySelector("p")?.textContent).toBe("a visible source");
  expect((probe as HTMLElement).style.width).toBe("777px");
  measurer.fits("paired", "paired", "dialogue");
  expect((probe as HTMLElement).style.width).toBe("381px");
  measurer.dispose();
  expect(container.querySelector("[data-comic-probe]")).toBeNull();
});
