import { render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { FlowOverlay } from "./FlowOverlay";

test("always renders children", () => {
  render(
    <FlowOverlay>
      <div data-testid="content">the list</div>
    </FlowOverlay>,
  );
  expect(screen.getByTestId("content")).toBeTruthy();
});

test("renders the top slot when provided", () => {
  render(
    <FlowOverlay top={<span data-testid="top-content">top</span>}>
      <div />
    </FlowOverlay>,
  );
  expect(screen.getByTestId("top-content")).toBeTruthy();
});

test("renders no top wrapper at all when top is omitted", () => {
  render(
    <FlowOverlay>
      <div />
    </FlowOverlay>,
  );
  expect(screen.queryByTestId("flow-overlay-top")).toBeNull();
});

test("renders the pill slot when provided", () => {
  render(
    <FlowOverlay pill={<span data-testid="pill-content">pill</span>}>
      <div />
    </FlowOverlay>,
  );
  expect(screen.getByTestId("pill-content")).toBeTruthy();
});

test("renders no pill wrapper at all when pill is omitted", () => {
  render(
    <FlowOverlay>
      <div />
    </FlowOverlay>,
  );
  expect(screen.queryByTestId("flow-overlay-pill")).toBeNull();
});

test("puts the attention overlay below the new-content pill in one stack", () => {
  render(
    <FlowOverlay
      pill={<span data-testid="pill-content">new content</span>}
      attentionOverlay={<span data-testid="attention-content">next</span>}
    >
      <div />
    </FlowOverlay>,
  );
  const strip = screen.getByTestId("flow-overlay-pill");
  const attention = screen.getByTestId("flow-overlay-attention");
  expect(screen.getByTestId("flow-overlay-stack")).toBeTruthy();
  expect(strip.contains(screen.getByTestId("pill-content"))).toBe(true);
  expect(attention.contains(screen.getByTestId("attention-content"))).toBe(true);
  // New Content stays above Next at the trailing bottom edge.
  expect(strip.compareDocumentPosition(attention) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

test("reports the reader's bottom-to-stack-top clearance, inset included, and clears it when the stack goes away", () => {
  const onOverlayHeightChange = vi.fn();
  // The real composition floats the 96 px stack 12 px above the reader's own
  // bottom edge (`.stack { bottom: var(--space-3) }`), so the reader needs
  // 108 px of trailing clearance: everything from its bottom up to the
  // stack's top.
  const rect = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    if (this.dataset.testid === "flow-overlay-stack") {
      return { top: 392, bottom: 488, height: 96 } as DOMRect;
    }
    return { top: 0, bottom: 500, height: 500 } as DOMRect;
  });
  try {
    const view = render(
      <FlowOverlay
        attentionOverlay={<span data-testid="attention-content">next</span>}
        onOverlayHeightChange={onOverlayHeightChange}
      >
        <div />
      </FlowOverlay>,
    );
    expect(onOverlayHeightChange).toHaveBeenLastCalledWith(108);
    expect(screen.getByTestId("flow-overlay-attention")).toBeTruthy();

    view.rerender(
      <FlowOverlay onOverlayHeightChange={onOverlayHeightChange}>
        <div />
      </FlowOverlay>,
    );
    expect(screen.queryByTestId("flow-overlay-attention")).toBeNull();
    expect(onOverlayHeightChange).toHaveBeenLastCalledWith(0);
  } finally {
    rect.mockRestore();
  }
});
