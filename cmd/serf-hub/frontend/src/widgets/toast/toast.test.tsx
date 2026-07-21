import { afterEach, test, expect, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { Toast, useToasts, type ToastKind } from "./index";
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

test("unhovering resumes a fresh 5s window", () => {
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
  advance(2000);
  fireEvent.mouseLeave(toast);

  advance(4999);
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
