import { expect, test, vi } from "vitest";
import { installPinchVeto, installTouchPinchVeto } from "./pinchVeto";

// Safari begins every pinch with a cancelable gesturestart (and follows with
// gesturechange); vetoing them is the one mechanism iOS Safari respects for
// blocking viewport pinch — see pinchVeto.ts's header. jsdom dispatches are a
// stand-in for the device's GestureEvent, so these tests pin what the install
// functions do, not engine behavior.
function dispatchSafariGesture(type: "gesturestart" | "gesturechange"): boolean {
  const event = new Event(type, { cancelable: true });
  document.dispatchEvent(event);
  return event.defaultPrevented;
}

// A window whose pointer answer the caller picks; the gate reads
// matchMedia("(any-pointer: coarse)") and nothing else.
function fakeWindow(coarse: boolean): { matchMedia(query: string): { matches: boolean }; document: Document } {
  return {
    matchMedia: (query: string) => ({ matches: coarse && query === "(any-pointer: coarse)" }),
    document,
  };
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

test("installTouchPinchVeto installs the veto on a touch screen", () => {
  const remove = installTouchPinchVeto(fakeWindow(true));
  expect(dispatchSafariGesture("gesturestart")).toBe(true);
  remove();
  expect(dispatchSafariGesture("gesturestart")).toBe(false);
});

test("installTouchPinchVeto stays off without a touch screen, so desktop zoom keeps working", () => {
  // Safari fires gesture events for trackpad pinches as well (the sources
  // disagree on when; the gate does not care), and a desktop Mac has no touch
  // screen, so the coarse query excludes exactly those machines.
  const remove = installTouchPinchVeto(fakeWindow(false));
  expect(dispatchSafariGesture("gesturestart")).toBe(false);
  remove();
  expect(dispatchSafariGesture("gesturestart")).toBe(false);
});
