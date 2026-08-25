import { describe, expect, it, vi } from "vitest";
import { installViewportMetrics } from "./viewport";

class VisualViewportTarget extends EventTarget {
  height = 500;
  offsetTop = 100;
}

describe("installViewportMetrics", () => {
  it("tracks resize and scroll and removes the exact listeners", () => {
    const visualViewport = new VisualViewportTarget();
    const add = vi.spyOn(visualViewport, "addEventListener");
    const remove = vi.spyOn(visualViewport, "removeEventListener");
    const target = {
      innerHeight: 900,
      visualViewport,
    } as unknown as Window;
    const root = document.createElement("div");

    const dispose = installViewportMetrics(target, root);
    expect(root.style.getPropertyValue("--visual-viewport-height")).toBe(
      "500px",
    );
    expect(root.style.getPropertyValue("--keyboard-inset")).toBe("300px");

    visualViewport.height = 700;
    visualViewport.offsetTop = 250;
    visualViewport.dispatchEvent(new Event("resize"));
    expect(root.style.getPropertyValue("--visual-viewport-height")).toBe(
      "700px",
    );
    expect(root.style.getPropertyValue("--keyboard-inset")).toBe("0px");

    visualViewport.height = 600;
    visualViewport.offsetTop = 50;
    visualViewport.dispatchEvent(new Event("scroll"));
    expect(root.style.getPropertyValue("--keyboard-inset")).toBe("250px");

    dispose();
    expect(add).toHaveBeenCalledTimes(2);
    expect(remove).toHaveBeenCalledTimes(2);
    expect(remove.mock.calls[0]).toEqual(["resize", add.mock.calls[0]?.[1]]);
    expect(remove.mock.calls[1]).toEqual(["scroll", add.mock.calls[1]?.[1]]);
  });

  it("falls back to the layout viewport", () => {
    const target = { innerHeight: 812, visualViewport: null } as Window;
    const root = document.createElement("div");
    const dispose = installViewportMetrics(target, root);

    expect(root.style.getPropertyValue("--visual-viewport-height")).toBe(
      "812px",
    );
    expect(root.style.getPropertyValue("--keyboard-inset")).toBe("0px");
    dispose();
  });
});
