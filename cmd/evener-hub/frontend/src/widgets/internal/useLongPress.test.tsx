// @vitest-environment jsdom
// useLongPress's own contract: the timer fires only at the threshold, slop
// cancels without poisoning the next click, `enabled` arms nothing, and the
// visibility watch exists only where the caller asks for it.
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { LONG_PRESS_MS } from "../hovercard";
import { POINTER_CLICK_WINDOW_MS } from "../hovercard/useFloatingLabel";
import { type LongPressOptions, useLongPress } from "./useLongPress";

function PressTarget({ onPress, ...options }: Partial<LongPressOptions> & { onPress?: () => void }): ReactElement {
  const longPress = useLongPress({ onLongPress: () => {}, ...options });
  return (
    <button
      data-testid="target"
      {...longPress.handlers}
      onClick={(event) => {
        if (longPress.consumeClick(event)) return;
        onPress?.();
      }}
    />
  );
}

function target(): HTMLElement {
  return screen.getByTestId("target");
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

test("a press fires the hold only after the threshold", () => {
  vi.useFakeTimers();
  const onLongPress = vi.fn();
  render(<PressTarget onLongPress={onLongPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 1 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS - 1));
  expect(onLongPress).not.toHaveBeenCalled();

  act(() => vi.advanceTimersByTime(1));
  expect(onLongPress).toHaveBeenCalledTimes(1);
});

test.each([
  { drift: 10, fires: true },
  { drift: 11, fires: false },
])("a $drift px drift fires the hold: $fires", ({ drift, fires }) => {
  vi.useFakeTimers();
  const onLongPress = vi.fn();
  render(<PressTarget onLongPress={onLongPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 2 });
  fireEvent.pointerMove(target(), { clientX: 10 + drift, clientY: 10, pointerId: 2 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

  expect(onLongPress).toHaveBeenCalledTimes(fires ? 1 : 0);
});

test("disabled arms no hold", () => {
  vi.useFakeTimers();
  const onLongPress = vi.fn();
  render(<PressTarget enabled={false} onLongPress={onLongPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 3 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

  expect(onLongPress).not.toHaveBeenCalled();
});

test.each([
  { cancelOnVisibilityHidden: true, fires: false },
  { cancelOnVisibilityHidden: false, fires: true },
])(
  "cancelOnVisibilityHidden=$cancelOnVisibilityHidden fires the hold: $fires",
  ({ cancelOnVisibilityHidden, fires }) => {
    vi.useFakeTimers();
    const onLongPress = vi.fn();
    const visibility = vi.spyOn(document, "visibilityState", "get");
    render(<PressTarget onLongPress={onLongPress} cancelOnVisibilityHidden={cancelOnVisibilityHidden} />);

    fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 4 });
    act(() => vi.advanceTimersByTime(200));
    visibility.mockReturnValue("hidden");
    act(() => fireEvent(document, new Event("visibilitychange")));
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

    expect(onLongPress).toHaveBeenCalledTimes(fires ? 1 : 0);
  },
);

test("a completed hold consumes exactly its trailing click", () => {
  vi.useFakeTimers();
  const onPress = vi.fn();
  render(<PressTarget onPress={onPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 5 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  fireEvent.pointerUp(target(), { clientX: 10, clientY: 10, pointerId: 5 });
  fireEvent.click(target());
  expect(onPress).not.toHaveBeenCalled();

  fireEvent.click(target());
  expect(onPress).toHaveBeenCalledTimes(1);
});

test("unmount clears a pending hold", () => {
  vi.useFakeTimers();
  const onLongPress = vi.fn();
  const view = render(<PressTarget onLongPress={onLongPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 6 });
  view.unmount();
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

  expect(onLongPress).not.toHaveBeenCalled();
});

test("holdMs carries a caller's own threshold", () => {
  vi.useFakeTimers();
  const onLongPress = vi.fn();
  render(<PressTarget holdMs={120} onLongPress={onLongPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 7 });
  act(() => vi.advanceTimersByTime(119));
  expect(onLongPress).not.toHaveBeenCalled();
  act(() => vi.advanceTimersByTime(1));
  expect(onLongPress).toHaveBeenCalledTimes(1);
});

test.each(["scroll", "resize"] as const)("cancelOnViewportChange ends a pending hold on %s", (event) => {
  vi.useFakeTimers();
  const onLongPress = vi.fn();
  render(<PressTarget cancelOnViewportChange onLongPress={onLongPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 8 });
  act(() => vi.advanceTimersByTime(100));
  act(() => fireEvent(window, new Event(event)));
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

  expect(onLongPress).not.toHaveBeenCalled();
});

test("cancelOnPointerLeave ends a pending hold", () => {
  vi.useFakeTimers();
  const onLongPress = vi.fn();
  render(<PressTarget cancelOnPointerLeave onLongPress={onLongPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 9 });
  fireEvent.pointerLeave(target(), { pointerId: 9 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));

  expect(onLongPress).not.toHaveBeenCalled();
});

test("a leave after the hold bounds its click suppression", () => {
  vi.useFakeTimers();
  const onPress = vi.fn();
  render(<PressTarget cancelOnPointerLeave onPress={onPress} />);

  fireEvent.pointerDown(target(), { button: 0, clientX: 10, clientY: 10, pointerId: 10 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  fireEvent.pointerLeave(target(), { pointerId: 10 });
  act(() => vi.advanceTimersByTime(POINTER_CLICK_WINDOW_MS + 1));
  fireEvent.click(target());

  expect(onPress).toHaveBeenCalledTimes(1);
});
