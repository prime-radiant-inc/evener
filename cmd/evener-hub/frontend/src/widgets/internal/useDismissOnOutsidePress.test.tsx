import { renderHook } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { useDismissOnOutsidePress } from "./useDismissOnOutsidePress";

function press(target: EventTarget) {
  target.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
}

function release(target: EventTarget) {
  target.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
  target.dispatchEvent(new MouseEvent("click", { bubbles: true }));
}

/** Await the next macrotask, where the capture fallback's dismissal lands. */
function nextTask(): Promise<void> {
  return new Promise<void>((resolve) => setTimeout(resolve, 0));
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

test("click mode waits for the completed click to dismiss", () => {
  // An in-flow surface must not collapse on the press: the reflow would move
  // whatever is below it out from under the pointer before mouseup.
  const panel = document.createElement("div");
  const outside = document.createElement("button");
  document.body.append(panel, outside);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        when: "click",
        isInside: (target) => target instanceof Node && panel.contains(target),
        onDismiss,
      }),
    );
    press(outside);
    expect(onDismiss).not.toHaveBeenCalled();
    release(outside);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  } finally {
    panel.remove();
    outside.remove();
  }
});

test("click mode ignores a click that began inside the surface", () => {
  const panel = document.createElement("div");
  const inside = document.createElement("span");
  panel.append(inside);
  const outside = document.createElement("button");
  document.body.append(panel, outside);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        when: "click",
        isInside: (target) => target instanceof Node && panel.contains(target),
        onDismiss,
      }),
    );
    // Press inside, drag out, release: a selection gesture, not a dismissal.
    press(inside);
    release(outside);
    expect(onDismiss).not.toHaveBeenCalled();
    // A normal press-and-click outside still dismisses.
    press(outside);
    release(outside);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  } finally {
    panel.remove();
    outside.remove();
  }
});

test("click mode dismisses for a control that stops its click's propagation", async () => {
  // A transcript control that consumes its own click (a file-reference link, a
  // delegate activity toggle) must still dismiss an in-flow surface. Reading
  // the click in the bubble phase never sees that event at all.
  const panel = document.createElement("div");
  const outside = document.createElement("button");
  outside.addEventListener("click", (event) => event.stopPropagation());
  document.body.append(panel, outside);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        when: "click",
        isInside: (target) => target instanceof Node && panel.contains(target),
        onDismiss,
      }),
    );
    press(outside);
    release(outside);
    // The fallback never dismisses inside dispatch: the target's own handlers
    // run first, and the surface collapses on a later task.
    expect(onDismiss).not.toHaveBeenCalled();
    await nextTask();
    expect(onDismiss).toHaveBeenCalledTimes(1);
  } finally {
    panel.remove();
    outside.remove();
  }
});

test("click mode runs the pressed control's own handler before it dismisses", async () => {
  const panel = document.createElement("div");
  const outside = document.createElement("button");
  const order: string[] = [];
  outside.addEventListener("click", () => order.push("control"));
  document.body.append(panel, outside);
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        when: "click",
        isInside: (target) => target instanceof Node && panel.contains(target),
        onDismiss: () => order.push("dismiss"),
      }),
    );
    press(outside);
    release(outside);
    expect(order).toEqual(["control", "dismiss"]);
  } finally {
    panel.remove();
    outside.remove();
  }
});

test("a stopped click still runs the control's own handler before the fallback dismisses", async () => {
  // The captured click is read before dispatch reaches the target, so the
  // fallback must not dismiss there: the control's handler runs first, and the
  // surface collapses only on the later task.
  const panel = document.createElement("div");
  const outside = document.createElement("button");
  const order: string[] = [];
  outside.addEventListener("click", (event) => {
    order.push("control");
    event.stopPropagation();
  });
  document.body.append(panel, outside);
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        when: "click",
        isInside: (target) => target instanceof Node && panel.contains(target),
        onDismiss: () => order.push("dismiss"),
      }),
    );
    press(outside);
    release(outside);
    expect(order).toEqual(["control"]);
    await nextTask();
    expect(order).toEqual(["control", "dismiss"]);
  } finally {
    panel.remove();
    outside.remove();
  }
});

test("a stopped click's spent press does not dismiss a later click", async () => {
  // The fallback consumes the press. A second click with no press of its own
  // (a script-dispatched click, a synthetic activation) is not a dismissal.
  const panel = document.createElement("div");
  const outside = document.createElement("button");
  outside.addEventListener("click", (event) => event.stopPropagation());
  document.body.append(panel, outside);
  const onDismiss = vi.fn();
  try {
    renderHook(() =>
      useDismissOnOutsidePress({
        ownerDocument: document,
        enabled: true,
        when: "click",
        isInside: (target) => target instanceof Node && panel.contains(target),
        onDismiss,
      }),
    );
    press(outside);
    release(outside);
    await nextTask();
    expect(onDismiss).toHaveBeenCalledTimes(1);
    // No press precedes this click, so there is nothing to complete.
    outside.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await nextTask();
    expect(onDismiss).toHaveBeenCalledTimes(1);
  } finally {
    panel.remove();
    outside.remove();
  }
});
