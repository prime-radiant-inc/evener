import { renderHook } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { installPinchVeto, usePinchVeto } from "./usePinchVeto";

// Safari begins every pinch with a cancelable gesturestart (and follows with
// gesturechange); vetoing them is the one mechanism iOS Safari respects for
// blocking viewport pinch — see usePinchVeto.ts's header. The dispatches here
// use cancelable Events exactly like a real gesture would arrive.
function dispatchSafariGesture(type: "gesturestart" | "gesturechange"): boolean {
  const event = new Event(type, { cancelable: true });
  document.dispatchEvent(event);
  return event.defaultPrevented;
}

test("installPinchVeto vetoes Safari's pinch gestures while installed", () => {
  const remove = installPinchVeto(document);
  expect(dispatchSafariGesture("gesturestart")).toBe(true);
  expect(dispatchSafariGesture("gesturechange")).toBe(true);
  remove();
  expect(dispatchSafariGesture("gesturestart")).toBe(false);
  expect(dispatchSafariGesture("gesturechange")).toBe(false);
});

test("the veto listens non-passively, or preventDefault would be ignored", () => {
  // A document-level listener the browser treats as passive cannot
  // preventDefault at all, and the pinch would silently still zoom.
  const spy = vi.spyOn(document, "addEventListener");
  const remove = installPinchVeto(document);
  for (const type of ["gesturestart", "gesturechange"] as const) {
    const call = spy.mock.calls.find(([name]) => name === type);
    expect(call, `${type} must be registered on the document`).toBeDefined();
    expect(call![2]).toMatchObject({ passive: false });
  }
  remove();
  spy.mockRestore();
});

test("usePinchVeto installs the veto for the component's lifetime", () => {
  // If the effect forgot to return installPinchVeto's remover, React would
  // never run it and the veto would survive the unmount below.
  const { result, unmount } = renderHook(() => usePinchVeto());
  expect(result.current).toBeUndefined();
  expect(dispatchSafariGesture("gesturestart")).toBe(true);
  unmount();
  expect(dispatchSafariGesture("gesturestart")).toBe(false);
});

test("a remount leaves a working veto: removal round-trips the exact listeners", () => {
  // React StrictMode double-invokes effects (mount, clean up, remount); a
  // removal that misses the registered listener would stack duplicates.
  const { unmount } = renderHook(() => usePinchVeto());
  unmount();
  const { unmount: unmountAgain } = renderHook(() => usePinchVeto());
  expect(dispatchSafariGesture("gesturestart")).toBe(true);
  unmountAgain();
  expect(dispatchSafariGesture("gesturestart")).toBe(false);
});
