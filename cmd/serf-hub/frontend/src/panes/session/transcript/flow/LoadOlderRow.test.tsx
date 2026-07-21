import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { LoadOlderRow } from "./LoadOlderRow";

afterEach(() => {
  cleanup();
});

test("is a real, keyboard-focusable button reading 'Load older'", () => {
  render(<LoadOlderRow onClick={() => {}} loading={false} />);
  const el = screen.getByTestId("load-older-row");
  expect(el.tagName).toBe("BUTTON");
  expect(el.textContent).toMatch(/load older/i);
});

test("clicking calls onClick", () => {
  const onClick = vi.fn();
  render(<LoadOlderRow onClick={onClick} loading={false} />);

  fireEvent.click(screen.getByTestId("load-older-row"));

  expect(onClick).toHaveBeenCalledTimes(1);
});

test("while loading, reads a quiet loading state and is disabled (no overlapping click can fire a second fetch)", () => {
  const onClick = vi.fn();
  render(<LoadOlderRow onClick={onClick} loading={true} />);

  const el = screen.getByTestId("load-older-row");
  expect(el.textContent).toMatch(/loading/i);
  expect((el as HTMLButtonElement).disabled).toBe(true);

  fireEvent.click(el);
  expect(onClick).not.toHaveBeenCalled();
});
