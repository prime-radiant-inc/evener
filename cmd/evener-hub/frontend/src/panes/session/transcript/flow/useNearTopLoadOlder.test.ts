import { renderHook } from "@testing-library/react";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import type { VirtualListHandle } from "../../../../widgets/virtuallist";
import { useNearTopLoadOlder } from "./useNearTopLoadOlder";

function listHandleWith(el: HTMLDivElement | null): React.RefObject<VirtualListHandle | null> {
  const ref = createRef<VirtualListHandle>() as React.RefObject<VirtualListHandle | null>;
  (ref as { current: VirtualListHandle | null }).current =
    el === null ? null : { scrollToIndex: vi.fn(), getScrollElement: () => el, getVisibleRange: () => null };
  return ref;
}

// jsdom's scrollTop is a plain read/write slot; pinning it with defineProperty
// keeps the value independent of any scrollability heuristic.
function portAt(scrollTop: number): HTMLDivElement {
  const el = document.createElement("div");
  Object.defineProperty(el, "scrollTop", { value: scrollTop, configurable: true, writable: true });
  return el;
}

test("loads older turns when a scroll leaves the port near the top", () => {
  const port = portAt(0);
  const onLoad = vi.fn();
  renderHook(() => useNearTopLoadOlder({ listRef: listHandleWith(port), loadOlder: onLoad, enabled: true }));

  port.dispatchEvent(new Event("scroll"));

  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("loads nothing while the reader is away from the top", () => {
  const port = portAt(1000);
  const onLoad = vi.fn();
  renderHook(() => useNearTopLoadOlder({ listRef: listHandleWith(port), loadOlder: onLoad, enabled: true }));

  port.dispatchEvent(new Event("scroll"));

  expect(onLoad).not.toHaveBeenCalled();
});

test("attaches nothing until the list is enabled", () => {
  const port = portAt(0);
  const onLoad = vi.fn();
  const listRef = listHandleWith(port);
  const { rerender } = renderHook(({ enabled }) => useNearTopLoadOlder({ listRef, loadOlder: onLoad, enabled }), {
    initialProps: { enabled: false },
  });

  port.dispatchEvent(new Event("scroll"));
  expect(onLoad).not.toHaveBeenCalled();

  rerender({ enabled: true });
  port.dispatchEvent(new Event("scroll"));
  expect(onLoad).toHaveBeenCalledTimes(1);
});

test("the listener is torn down on unmount", () => {
  const port = portAt(0);
  const onLoad = vi.fn();
  const { unmount } = renderHook(() =>
    useNearTopLoadOlder({ listRef: listHandleWith(port), loadOlder: onLoad, enabled: true }),
  );
  port.dispatchEvent(new Event("scroll"));
  expect(onLoad).toHaveBeenCalledTimes(1);

  unmount();
  port.dispatchEvent(new Event("scroll"));

  expect(onLoad).toHaveBeenCalledTimes(1);
});
