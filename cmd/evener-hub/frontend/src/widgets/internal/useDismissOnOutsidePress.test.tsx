import { renderHook } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { useDismissOnOutsidePress } from "./useDismissOnOutsidePress";

function press(target: EventTarget) {
  target.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
}

test("a press outside dismisses", () => {
  const panel = document.createElement("div");
  const outside = document.createElement("button");
  document.body.append(panel, outside);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        isInside: (target) => target === panel,
        onDismiss,
      }),
    );
    press(outside);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  } finally {
    panel.remove();
    outside.remove();
  }
});

test("a press inside does not dismiss", () => {
  const panel = document.createElement("div");
  const inner = document.createElement("span");
  panel.append(inner);
  document.body.append(panel);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        // The predicate sees the actual event target, so containment is the
        // caller's job - exactly how Menu, Popover and the notes panel use it.
        isInside: (target) => target instanceof Node && panel.contains(target),
        onDismiss,
      }),
    );
    press(inner);
    expect(onDismiss).not.toHaveBeenCalled();
  } finally {
    panel.remove();
  }
});

test("a disabled surface listens to nothing", () => {
  const outside = document.createElement("button");
  document.body.append(outside);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({ ownerDocument: document, enabled: false, isInside: () => false, onDismiss }),
    );
    press(outside);
    expect(onDismiss).not.toHaveBeenCalled();
  } finally {
    outside.remove();
  }
});

test("a press in another document does not dismiss", () => {
  const outside = document.createElement("button");
  document.body.append(outside);
  const { defaultView } = document;
  const other = defaultView?.document.implementation.createHTMLDocument("other");
  if (other === undefined) throw new Error("no document implementation");
  const otherTarget = other.createElement("button");
  other.body.append(otherTarget);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({ ownerDocument: document, enabled: true, isInside: () => false, onDismiss }),
    );
    // A popout window's own document owns its presses; the panel listens on
    // whichever document it lives in, not on both.
    press(otherTarget);
    expect(onDismiss).not.toHaveBeenCalled();
    press(outside);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  } finally {
    outside.remove();
  }
});

test("a fresh predicate each render does not re-subscribe the listener", () => {
  const outside = document.createElement("button");
  document.body.append(outside);
  const onDismiss = vi.fn();
  const add = vi.spyOn(document, "addEventListener");
  const remove = vi.spyOn(document, "removeEventListener");
  try {
    const { rerender } = renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        // A new function identity every render: callers pass inline closures.
        isInside: (target) => target instanceof Node && outside.contains(target),
        onDismiss,
      }),
    );
    const mousedownAdds = () => add.mock.calls.filter(([type]) => type === "mousedown").length;
    const adds = mousedownAdds();
    rerender();
    rerender();
    expect(mousedownAdds()).toBe(adds);
    expect(remove.mock.calls.filter(([type]) => type === "mousedown")).toHaveLength(0);
  } finally {
    add.mockRestore();
    remove.mockRestore();
    outside.remove();
  }
});
