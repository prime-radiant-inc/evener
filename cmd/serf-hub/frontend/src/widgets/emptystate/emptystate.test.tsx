import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { EmptyState } from "./index";

afterEach(cleanup);

test("renders the title", () => {
  render(<EmptyState title="No sessions yet" />);
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});

test("renders the hint when provided", () => {
  render(<EmptyState title="No sessions yet" hint="Start one from the command palette." />);
  expect(screen.getByText("Start one from the command palette.")).toBeTruthy();
});

test("omits the hint when not provided", () => {
  const { container } = render(<EmptyState title="No sessions yet" />);
  // the title is the only text node this widget renders on its own -
  // nothing else should appear when hint/action are both absent
  expect(container.textContent).toBe("No sessions yet");
});

test("renders the action slot when provided", () => {
  render(<EmptyState title="No sessions yet" action={<button>New session</button>} />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test("omits the action slot when not provided", () => {
  render(<EmptyState title="No sessions yet" />);
  expect(screen.queryByRole("button")).toBeNull();
});

test("renders title, hint, and action together", () => {
  render(
    <EmptyState title="No sessions yet" hint="Start one from the command palette." action={<button>New session</button>} />,
  );
  expect(screen.getByText("No sessions yet")).toBeTruthy();
  expect(screen.getByText("Start one from the command palette.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});
