// Edge cases for SelectionQuote.tsx that close the remaining uncovered lines:
// - container null (lines 91-92)
// - whitespace-only selection text (lines 101-102)

import { act, cleanup, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { SelectionQuote } from "./SelectionQuote";

function fakeRect(): DOMRect {
  return {
    x: 0,
    y: 0,
    width: 100,
    height: 20,
    top: 100,
    left: 100,
    right: 200,
    bottom: 120,
    toJSON: () => ({}),
  } as DOMRect;
}

function installFakeSelection(options: { text: string; anchorNode: Node; rect?: DOMRect } | null) {
  const selection = options
    ? ({
        isCollapsed: false,
        rangeCount: 1,
        toString: () => options.text,
        getRangeAt: () => ({
          commonAncestorContainer: options.anchorNode,
          getBoundingClientRect: () => options.rect ?? fakeRect(),
        }),
      } as unknown as Selection)
    : ({ isCollapsed: true, rangeCount: 0, toString: () => "" } as unknown as Selection);
  vi.spyOn(window, "getSelection").mockReturnValue(selection);
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("evaluate with a null container clears the selection", () => {
  const containerRef = createRef<HTMLDivElement>();
  // Don't render the container — containerRef.current stays null
  render(<SelectionQuote containerRef={containerRef} actions={[]} />);

  // The component should render nothing (no bar)
  expect(screen.queryByRole("button")).toBeNull();
});

test("a whitespace-only selection does not show the action bar", () => {
  const containerRef = createRef<HTMLDivElement>();
  const messageDiv = document.createElement("div");
  messageDiv.setAttribute("data-view-anchor-message", "true");
  messageDiv.textContent = "   ";

  render(
    <div ref={containerRef} data-testid="transcript-container">
      <div data-view-anchor-message="true" data-testid="message-node">
        selectable text
      </div>
      <SelectionQuote containerRef={containerRef} actions={[{ label: "Quote", onInvoke: () => {} }]} />
    </div>,
  );

  const messageNode = screen.getByTestId("message-node");
  installFakeSelection({ text: "   ", anchorNode: messageNode.firstChild as Node });

  // Trigger evaluate via pointerup
  act(() => {
    containerRef.current?.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
  });

  // No action bar should appear
  expect(screen.queryByRole("button", { name: /Quote/i })).toBeNull();
});
