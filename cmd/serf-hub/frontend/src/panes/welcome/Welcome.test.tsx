import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Welcome from "./Welcome";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test('shows "No session open"', () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByText("No session open")).toBeTruthy();
});

test('offers a "New session" action', () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test('clicking "New session" navigates to /new', async () => {
  const user = userEvent.setup();
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await user.click(screen.getByRole("button", { name: "New session" }));
  expect(window.location.pathname).toBe("/new");
});

test("shows params.note as a hint when provided", () => {
  render(<Welcome params={{ note: "Starting a new session isn't available yet." }} paneId="welcome" focused={true} />);
  expect(screen.getByText("Starting a new session isn't available yet.")).toBeTruthy();
});

test("shows no hint when params.note is absent", () => {
  const { container } = render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(container.textContent).not.toMatch(/available yet/i);
});
