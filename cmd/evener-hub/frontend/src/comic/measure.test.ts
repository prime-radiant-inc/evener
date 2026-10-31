import { afterEach, expect, test, vi } from "vitest";
import { projectComicMarkdown } from "./markdown";
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
  const source = "a visible source";
  const doc = projectComicMarkdown(source, false);
  const part = doc.parts[0];
  if (!part) throw new Error("missing part");
  measurer.fits(doc, part, "wide", "dialogue");
  const probe = container.querySelector("[data-comic-probe]");
  expect(create).toHaveBeenCalledWith("article");
  expect(getStyle).toHaveBeenCalledWith(probe?.querySelector("[data-comic-text]"));
  expect(probe?.ownerDocument).toBe(foreignDocument);
  expect(probe?.querySelector("[data-comic-text]")?.textContent).toBe(source);
  expect((probe as HTMLElement).style.width).toBe("777px");
  measurer.fits(doc, part, "paired", "dialogue");
  expect((probe as HTMLElement).style.width).toBe("381px");
  measurer.dispose();
  expect(container.querySelector("[data-comic-probe]")).toBeNull();
});

test("the balloon budget measures semantic shared markup with document references", () => {
  const source = "# Heading\n\n**bold** [link][ref]\n\n[ref]: https://example.com";
  const doc = projectComicMarkdown(source, true);
  const part = doc.parts[0];
  if (!part) throw new Error("missing part");
  const host = document.createElement("div");
  document.body.append(host);
  const geometry = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    return new DOMRect(0, 0, 390, this.matches("[data-comic-text]") && this.querySelector("h1, strong, a") ? 1000 : 10);
  });
  const measurer = createComicMeasurer(host);
  try {
    expect(measurer.fits(doc, part, "wide", "thought")).toBe(false);
    expect(host.querySelectorAll("h1, strong, a")).toHaveLength(3);
    expect(host.querySelector("a")?.getAttribute("href")).toBe("https://example.com");
    expect(host.querySelector("[data-comic-live]")).not.toBeNull();
    expect(measurer.fits(doc, part, "insert", "thought")).toBe(true);
  } finally {
    geometry.mockRestore();
    measurer.dispose();
  }
});
