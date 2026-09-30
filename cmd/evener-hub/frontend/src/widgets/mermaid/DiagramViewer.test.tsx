import { readFileSync } from "node:fs";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { requireClass } from "../internal/requireClass";
import { DiagramViewer } from "./DiagramViewer";
import styles from "./mermaid.module.css";

// A committed fixture SVG is passed straight in as the `svg` prop: the viewer
// only re-inserts already-sanitized markup, so rendering real mermaid here
// would spend ~0.5s per case proving the sanitizer (covered by
// security.test.ts) rather than the viewer. readFileSync resolves relative to
// vitest's root (this package directory), the pattern security.test.ts uses
// because jsdom's import.meta.url is not a file: URL.
const FIXTURE_DIR = "src/widgets/mermaid/testdata";
const FIXTURE_SVG = readFileSync(`${FIXTURE_DIR}/flowchart.svg`, "utf8");
const ZOOM_CONTENT_CLASS = requireClass(styles.zoomContent, "mermaid.module.css", "zoomContent");

// jsdom implements neither the Pointer Capture API nor any layout. The capture
// calls are recorded (and a no-op is enough) so the drag path can run; layout
// is not needed because every assertion reads the transform string, not pixels.
beforeEach(() => {
  Element.prototype.setPointerCapture = function setPointerCapture(_pointerId: number) {};
  Element.prototype.releasePointerCapture = function releasePointerCapture(_pointerId: number) {};
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("DiagramViewer", () => {
  it("opens, toggles to source and back, and closes", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={onClose} />);
    expect(screen.getByRole("dialog")).toBeTruthy();
    // The fixture markup really reached the DOM (the viewer's whole point).
    expect(screen.getByRole("dialog").querySelector("svg")).not.toBeNull();
    await user.click(screen.getByRole("button", { name: /show source/i }));
    expect(screen.getByText("graph TD; A-->B")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: /show diagram/i }));
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalled();
  });

  it("Copy source writes the diagram source text to the clipboard", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Copy source" }));
    expect(writeText).toHaveBeenCalledExactlyOnceWith("graph TD; A-->B");
  });

  it("zooms with the wheel and pans by dragging", () => {
    const { container } = render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={vi.fn()} />);
    const content = container.querySelector<HTMLElement>(`.${ZOOM_CONTENT_CLASS}`);
    expect(content).not.toBeNull();
    const surface = content!.parentElement;
    expect(surface).not.toBeNull();
    expect(content!.style.transform).toContain("scale(1)");

    // jsdom's getBoundingClientRect is all zeros, so the cursor sits at the
    // surface's top-left; a wheel-up step multiplies the scale by 1.2.
    fireEvent.wheel(surface!, { deltaY: -100, clientX: 0, clientY: 0 });
    expect(content!.style.transform).toContain("scale(1.2)");

    // A drag from (0,0) to (30,20) moves the content by exactly that delta.
    fireEvent.pointerDown(surface!, { pointerId: 1, clientX: 0, clientY: 0 });
    fireEvent.pointerMove(surface!, { pointerId: 1, clientX: 30, clientY: 20 });
    expect(content!.style.transform).toContain("translate(30px, 20px)");
    fireEvent.pointerUp(surface!, { pointerId: 1, clientX: 30, clientY: 20 });
  });

  it("Reset zoom restores the initial transform", () => {
    const { container } = render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={vi.fn()} />);
    const content = container.querySelector<HTMLElement>(`.${ZOOM_CONTENT_CLASS}`)!;
    fireEvent.wheel(content.parentElement!, { deltaY: -100, clientX: 0, clientY: 0 });
    expect(content.style.transform).not.toContain("scale(1)");
    fireEvent.click(screen.getByRole("button", { name: /reset zoom/i }));
    expect(content.style.transform).toContain("translate(0px, 0px) scale(1)");
  });

  it("ignores a horizontal wheel and stops a zoom from chain-scrolling the page", () => {
    const { container } = render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={vi.fn()} />);
    const content = container.querySelector<HTMLElement>(`.${ZOOM_CONTENT_CLASS}`)!;
    const surface = content.parentElement!;

    // A zero-deltaY (horizontal) wheel is not a zoom gesture: it must be left
    // alone, not routed to the ternary's zoom-out branch.
    fireEvent.wheel(surface, { deltaY: 0, clientX: 0, clientY: 0 });
    expect(content.style.transform).toContain("scale(1)");

    // A real zoom cancels the event, so it cannot bubble into a scrolling
    // ancestor. fireEvent returns false exactly when preventDefault ran.
    expect(fireEvent.wheel(surface, { deltaY: -100, clientX: 0, clientY: 0 })).toBe(false);
    expect(content.style.transform).toContain("scale(1.2)");
  });

  it("anchors the zoom at the cursor after a drag", () => {
    const { container } = render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={vi.fn()} />);
    const content = container.querySelector<HTMLElement>(`.${ZOOM_CONTENT_CLASS}`)!;
    const surface = content.parentElement!;
    // jsdom's getBoundingClientRect is all zeros, which puts the cursor at the
    // surface origin and makes an anchored zoom indistinguishable from a
    // top-left one. A real rect puts the cursor away from the origin.
    vi.spyOn(surface, "getBoundingClientRect").mockReturnValue({
      left: 0,
      top: 0,
      width: 400,
      height: 300,
      right: 400,
      bottom: 300,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    } as DOMRect);

    // Drag the diagram to (30, 20) first.
    fireEvent.pointerDown(surface, { pointerId: 1, clientX: 0, clientY: 0 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 30, clientY: 20 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 30, clientY: 20 });
    expect(content.style.transform).toContain("translate(30px, 20px)");

    // Zoom in by 1.2 at cursor (100, 80). Keeping the point under the cursor
    // stationary gives x = 100 - 1.2*(100-30) = 16 and y = 80 - 1.2*(80-20) = 8.
    fireEvent.wheel(surface, { deltaY: -100, clientX: 100, clientY: 80 });
    expect(content.style.transform).toContain("translate(16px, 8px)");
    expect(content.style.transform).toContain("scale(1.2)");
  });

  it("clamps wheel zoom to the [0.25, 8] bounds", () => {
    const { container } = render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={vi.fn()} />);
    const content = container.querySelector<HTMLElement>(`.${ZOOM_CONTENT_CLASS}`)!;
    const surface = content.parentElement!;
    for (let i = 0; i < 20; i++) fireEvent.wheel(surface, { deltaY: -100, clientX: 0, clientY: 0 });
    expect(content.style.transform).toContain("scale(8)");
    for (let i = 0; i < 40; i++) fireEvent.wheel(surface, { deltaY: 100, clientX: 0, clientY: 0 });
    expect(content.style.transform).toContain("scale(0.25)");
  });
});
