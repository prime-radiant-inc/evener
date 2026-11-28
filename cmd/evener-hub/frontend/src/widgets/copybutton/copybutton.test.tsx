import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { CopyButton } from "./index";

const originalExecCommand = document.execCommand;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
  Object.defineProperty(document, "execCommand", { configurable: true, writable: true, value: originalExecCommand });
});

// jsdom has no Clipboard API of its own; userEvent.setup() installs an
// in-memory one, so the tests that click through user-event spy on THAT
// stub's writeText rather than installing a competing one (codeblock.test.tsx
// explains how a second stub ends up shadowed). The fake-timer tests use
// fireEvent instead, because user-event's click hangs under fake timers, and
// so provide their own clipboard.
function installClipboard(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
}

test("is an icon-only button named Copy by default", () => {
  render(<CopyButton text="hello" />);
  const button = screen.getByRole("button", { name: "Copy" });
  expect(button.textContent).toBe("");
  expect(button.querySelector("svg")).toBeTruthy();
});

test("a specific label names what it copies", () => {
  render(<CopyButton text="{}" label="Copy raw JSON" />);
  expect(screen.getByRole("button", { name: "Copy raw JSON" })).toBeTruthy();
});

test("text presentation renders its label as visible text, not an icon", () => {
  render(<CopyButton text="ABCD-1234" label="Copy code" presentation="text" />);
  const button = screen.getByRole("button", { name: "Copy code" });
  expect(button.textContent).toBe("Copy code");
  expect(button.querySelector("svg")).toBeNull();
});

test("clicking writes exactly its text to the clipboard", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  render(<CopyButton text="git status --short" label="Copy command" />);
  await user.click(screen.getByRole("button", { name: "Copy command" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith("git status --short");
});

test("text presentation shows Copied ✓ after a successful write and reports it", async () => {
  installClipboard(() => Promise.resolve());
  const onResult = vi.fn();
  render(<CopyButton text="ABCD-1234" label="Copy code" presentation="text" onResult={onResult} />);

  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy code" }));
  });
  expect(onResult).toHaveBeenCalledExactlyOnceWith(true);
  expect(screen.getByRole("button", { name: "Copied ✓" })).toBeTruthy();
});

test("after a successful write it is named Copied for two seconds, then reverts to its label", async () => {
  installClipboard(() => Promise.resolve());
  vi.useFakeTimers();
  render(<CopyButton text="hello" label="Copy output" />);

  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy output" }));
  });
  expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(1_999);
  });
  expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(screen.getByRole("button", { name: "Copy output" })).toBeTruthy();
});

test("a rejected write is a silent no-op: the name never claims Copied", async () => {
  installClipboard(() => Promise.reject(new Error("denied")));
  render(<CopyButton text="hello" />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
  });
  expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Copied" })).toBeNull();
});

test("a failed copy calls onResult(false) and never claims Copied", async () => {
  installClipboard(() => Promise.reject(new Error("denied")));
  Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn().mockReturnValue(false) });
  const onResult = vi.fn();
  render(<CopyButton text="hello" label="Copy code" presentation="text" onResult={onResult} />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy code" }));
  });
  expect(onResult).toHaveBeenCalledExactlyOnceWith(false);
  expect(screen.getByRole("button", { name: "Copy code" })).toBeTruthy();
});

test("falls back to execCommand when the Clipboard API is unavailable", async () => {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
  const execCommand = vi.fn().mockReturnValue(true);
  Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
  render(<CopyButton text="hello" />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
  });
  expect(execCommand).toHaveBeenCalledExactlyOnceWith("copy");
  expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();
});

test("a missing Clipboard API and no working fallback is a silent no-op", async () => {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
  render(<CopyButton text="hello" />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
  });
  expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
});

test("native attributes pass through to the button", () => {
  render(<CopyButton text="hello" data-testid="copy-output" title="Copy the whole output" />);
  const button = screen.getByTestId("copy-output");
  expect(button.getAttribute("title")).toBe("Copy the whole output");
  expect(button).toBe(screen.getByRole("button", { name: "Copy" }));
});

test("disabled blocks the copy", async () => {
  const writeText = vi.fn(() => Promise.resolve());
  installClipboard(writeText);
  render(<CopyButton text="hello" disabled />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
  });
  expect(writeText).not.toHaveBeenCalled();
});
