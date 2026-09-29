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

  it("offers a Copy source control wired to the source text", () => {
    render(<DiagramViewer open svg={FIXTURE_SVG} source={"graph TD; A-->B"} onClose={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Copy source" })).toBeTruthy();
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
});
