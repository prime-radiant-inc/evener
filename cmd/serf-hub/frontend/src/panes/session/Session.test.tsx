import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import Session from "./Session";

afterEach(cleanup);

test("shows the session ref", () => {
  render(<Session params={{ ref: "ref_abc123" }} paneId="session-1" focused={true} />);
  expect(screen.getByText("ref_abc123")).toBeTruthy();
});

test('shows the "Transcript arrives in wave 4" placeholder message', () => {
  render(<Session params={{ ref: "ref_abc123" }} paneId="session-1" focused={true} />);
  expect(screen.getByText("Transcript arrives in wave 4")).toBeTruthy();
});

test("shows a different ref for a different pane", () => {
  render(<Session params={{ ref: "ref_xyz789" }} paneId="session-2" focused={true} />);
  expect(screen.getByText("ref_xyz789")).toBeTruthy();
});
