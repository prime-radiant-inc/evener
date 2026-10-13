import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { InlineNotice } from "./index";

test("a neutral notice is a polite status message", () => {
  render(<InlineNotice>Saved.</InlineNotice>);
  expect(screen.getByRole("status").textContent).toBe("Saved.");
  expect(screen.queryByRole("alert")).toBeNull();
});

test("a danger notice is an alert", () => {
  render(<InlineNotice tone="danger">Could not save.</InlineNotice>);
  expect(screen.getByRole("alert").textContent).toBe("Could not save.");
  expect(screen.queryByRole("status")).toBeNull();
});

test("live overrides the tone's default announcement", () => {
  const { rerender } = render(
    <InlineNotice tone="danger" live="status">
      Credential rejected.
    </InlineNotice>,
  );
  expect(screen.getByRole("status").textContent).toBe("Credential rejected.");

  rerender(<InlineNotice live="alert">Choose a model to start.</InlineNotice>);
  expect(screen.getByRole("alert").textContent).toBe("Choose a model to start.");
});

test('live="off" announces nothing, for a notice inside a region that already does', () => {
  render(
    <InlineNotice tone="danger" live="off">
      Failed to load.
    </InlineNotice>,
  );
  expect(screen.getByText("Failed to load.")).toBeTruthy();
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("status")).toBeNull();
});

test("a title heads the message", () => {
  render(
    <InlineNotice title="Warnings">
      <ul>
        <li>Duplicate binding</li>
      </ul>
    </InlineNotice>,
  );
  const notice = screen.getByRole("status");
  expect(notice.textContent).toBe("WarningsDuplicate binding");
  expect(screen.getByRole("listitem").textContent).toBe("Duplicate binding");
});

test("the action slot renders inside the notice and stays operable", async () => {
  const onRetry = vi.fn();
  render(
    <InlineNotice
      tone="danger"
      action={
        <button type="button" onClick={onRetry}>
          Retry
        </button>
      }
    >
      Could not check provider configuration.
    </InlineNotice>,
  );
  const retry = screen.getByRole("button", { name: "Retry" });
  expect(screen.getByRole("alert").contains(retry)).toBe(true);
  await userEvent.click(retry);
  expect(onRetry).toHaveBeenCalledTimes(1);
});

test("without an action, the notice holds no control", () => {
  render(<InlineNotice>Nothing to do.</InlineNotice>);
  expect(screen.queryByRole("button")).toBeNull();
});

test("native attributes reach the notice: an accessible name and a test id", () => {
  render(
    <InlineNotice tone="danger" live="status" aria-label="Credential error" data-testid="credential-error">
      The key was rejected.
    </InlineNotice>,
  );
  const notice = screen.getByRole("status", { name: "Credential error" });
  expect(notice).toBe(screen.getByTestId("credential-error"));
});

test("forwards a ref so a caller can move focus onto an error", () => {
  const ref = createRef<HTMLDivElement>();
  render(
    <InlineNotice tone="danger" ref={ref} tabIndex={-1}>
      Sign-in failed.
    </InlineNotice>,
  );
  ref.current?.focus();
  expect(document.activeElement).toBe(screen.getByRole("alert"));
});
