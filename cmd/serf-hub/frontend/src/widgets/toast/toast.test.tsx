import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Toast, type ToastKind, useToasts } from "./index";
import { resetToastStoreForTests } from "./store";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetToastStoreForTests();
});

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

function PushButton({ kind, text }: { kind: ToastKind; text: string }) {
  const { push } = useToasts();
  return <button onClick={() => push(kind, text)}>{`Push: ${text}`}</button>;
}

test("renders an aria-live=polite region even with no toasts", () => {
  render(<Toast />);
  const region = screen.getByRole("region", { name: "Notifications" });
  expect(region.getAttribute("aria-live")).toBe("polite");
});

test("push() surfaces a toast with its text in the region", () => {
  render(
    <>
      <Toast />
      <PushButton kind="info" text="Saved" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: Saved" }));
  expect(within(screen.getByRole("region")).getByText("Saved")).toBeTruthy();
});

test("multiple pushes render multiple toasts, oldest first", () => {
  render(
    <>
      <Toast />
      <PushButton kind="info" text="First" />
      <PushButton kind="info" text="Second" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: First" }));
  fireEvent.click(screen.getByRole("button", { name: "Push: Second" }));
  const region = screen.getByRole("region");
  const texts = within(region)
    .getAllByText(/First|Second/)
    .map((el) => el.textContent);
  expect(texts).toEqual(["First", "Second"]);
});

test("each kind maps to a distinct tone class", () => {
  render(
    <>
      <Toast />
      <PushButton kind="success" text="ok" />
      <PushButton kind="error" text="bad" />
      <PushButton kind="warning" text="careful" />
      <PushButton kind="info" text="fyi" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: ok" }));
  fireEvent.click(screen.getByRole("button", { name: "Push: bad" }));
  fireEvent.click(screen.getByRole("button", { name: "Push: careful" }));
  fireEvent.click(screen.getByRole("button", { name: "Push: fyi" }));

  const classes = [
    screen.getByText("ok").parentElement!.className,
    screen.getByText("bad").parentElement!.className,
    screen.getByText("careful").parentElement!.className,
    screen.getByText("fyi").parentElement!.className,
  ];
  expect(new Set(classes).size).toBe(4);
});

test("auto-dismisses after 5s", () => {
  vi.useFakeTimers();
  render(
    <>
      <Toast />
      <PushButton kind="info" text="Saved" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: Saved" }));
  expect(screen.getByText("Saved")).toBeTruthy();

  advance(4999);
  expect(screen.getByText("Saved")).toBeTruthy();

  advance(1);
  expect(screen.queryByText("Saved")).toBeNull();
});

test("hovering pauses the auto-dismiss timer", () => {
  vi.useFakeTimers();
  render(
    <>
      <Toast />
      <PushButton kind="info" text="Saved" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: Saved" }));
  const toast = screen.getByText("Saved").parentElement!;

  advance(4000);
  fireEvent.mouseEnter(toast);
  advance(2000); // would have dismissed by now (4000+2000 > 5000) if not paused
  expect(screen.getByText("Saved")).toBeTruthy();
});

// fix-wave (item 4 adjudication): unhovering used to resume a fresh
// AUTO_DISMISS_MS window regardless of how much of it had already
// elapsed before the hover, so a toast hovered right before it would
// have dismissed got a full bonus 5s instead of the ~0s it actually had
// left. True pause/resume (remaining time is tracked and only THAT much
// is rescheduled) is the standard pattern most toast libraries implement
// and the less surprising one, so this fixes the behavior rather than
// documenting the restart as intentional.
test("unhovering resumes with the time that was actually remaining, not a fresh window", () => {
  vi.useFakeTimers();
  render(
    <>
      <Toast />
      <PushButton kind="info" text="Saved" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: Saved" }));
  const toast = screen.getByText("Saved").parentElement!;

  advance(4000); // 1000ms of the 5000ms budget left
  fireEvent.mouseEnter(toast);
  advance(2000); // paused - elapsed time here must not count against the budget
  fireEvent.mouseLeave(toast); // resumes with exactly the 1000ms that was left

  advance(999);
  expect(screen.getByText("Saved")).toBeTruthy();
  advance(1);
  expect(screen.queryByText("Saved")).toBeNull();
});

test("repeated pause/resume cycles keep subtracting from the same budget, not resetting it", () => {
  vi.useFakeTimers();
  render(
    <>
      <Toast />
      <PushButton kind="info" text="Saved" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: Saved" }));
  const toast = screen.getByText("Saved").parentElement!;

  advance(2000); // 3000ms left
  fireEvent.mouseEnter(toast);
  advance(1000); // paused
  fireEvent.mouseLeave(toast); // resumes with 3000ms left
  advance(1000); // 2000ms left
  fireEvent.mouseEnter(toast);
  advance(5000); // paused for way longer than the remaining budget - must not dismiss while paused
  fireEvent.mouseLeave(toast); // resumes with 2000ms left

  advance(1999);
  expect(screen.getByText("Saved")).toBeTruthy();
  advance(1);
  expect(screen.queryByText("Saved")).toBeNull();
});

test("dismissing one toast leaves an independently-timed toast alone", () => {
  vi.useFakeTimers();
  render(
    <>
      <Toast />
      <PushButton kind="info" text="Early" />
      <PushButton kind="info" text="Late" />
    </>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Push: Early" }));
  advance(3000);
  fireEvent.click(screen.getByRole("button", { name: "Push: Late" }));

  advance(2000); // Early's clock: 5000 elapsed -> dismissed. Late's clock: 2000 elapsed.
  expect(screen.queryByText("Early")).toBeNull();
  expect(screen.getByText("Late")).toBeTruthy();

  advance(3000); // Late's clock: 5000 elapsed.
  expect(screen.queryByText("Late")).toBeNull();
});
