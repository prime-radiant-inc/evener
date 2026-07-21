import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { NewContentPill } from "./NewContentPill";

afterEach(() => {
  cleanup();
});

test("renders nothing when count is 0 and not needsYou", () => {
  render(<NewContentPill count={0} needsYou={false} onClick={() => {}} />);
  expect(screen.queryByTestId("new-content-pill")).toBeNull();
});

test("renders nothing when count is 0 even if needsYou is (defensively) true", () => {
  render(<NewContentPill count={0} needsYou={true} onClick={() => {}} />);
  expect(screen.queryByTestId("new-content-pill")).toBeNull();
});

test("shows the plain count via Badge when not needsYou", () => {
  render(<NewContentPill count={5} needsYou={false} onClick={() => {}} />);
  const pill = screen.getByTestId("new-content-pill");
  expect(pill.textContent).toContain("5");
  expect(pill.textContent!.toLowerCase()).toContain("new");
  expect(pill.textContent!.toLowerCase()).not.toContain("needs you");
});

test("reads 'needs you' instead of the plain count when needsYou is true, per the pinned pill-upgrade contract", () => {
  render(<NewContentPill count={5} needsYou={true} onClick={() => {}} />);
  const pill = screen.getByTestId("new-content-pill");
  expect(pill.textContent!.toLowerCase()).toContain("needs you");
  // The raw count is not shown once upgraded - matches
  // docs/web-ui/parity/contracts-transcript-scroll-liveness.md: "New content
  // arriving under an awaiting/attention state reads '↓ needs you' instead
  // of a plain count".
  expect(pill.textContent).not.toContain("5");
});

test('is a real, keyboard-focusable <button type="button">, not a clickable span', () => {
  render(<NewContentPill count={3} needsYou={false} onClick={() => {}} />);
  const pill = screen.getByTestId("new-content-pill");
  expect(pill.tagName).toBe("BUTTON");
  expect(pill.getAttribute("type")).toBe("button");
});

test("clicking calls onClick", () => {
  const onClick = vi.fn();
  render(<NewContentPill count={3} needsYou={false} onClick={onClick} />);

  fireEvent.click(screen.getByTestId("new-content-pill"));

  expect(onClick).toHaveBeenCalledTimes(1);
});

test("caps a very large count via Badge's own 99+ display, without the pill inventing its own cap", () => {
  render(<NewContentPill count={500} needsYou={false} onClick={() => {}} />);
  expect(screen.getByTestId("new-content-pill").textContent).toContain("99+");
});
