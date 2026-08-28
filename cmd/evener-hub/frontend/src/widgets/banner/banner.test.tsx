import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { Banner } from "./index";

afterEach(() => {
  cleanup();
});

describe("Banner", () => {
  test("renders the message", () => {
    render(<Banner tone="attention" message="Reconnecting to the server…" />);
    expect(screen.getByText("Reconnecting to the server…")).toBeTruthy();
  });

  test("does not render an action button when none is provided", () => {
    render(<Banner tone="attention" message="hi" />);
    expect(screen.queryByRole("button")).toBeNull();
  });

  test("renders and wires an action button when provided", async () => {
    const onClick = vi.fn();
    render(<Banner tone="danger" message="Connection closed." action={{ label: "Retry", onClick }} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  test("renders the in-flight (disabled) button with an ellipsis label", () => {
    render(
      <Banner
        tone="danger"
        message="Connection closed."
        action={{ label: "Retry", onClick: vi.fn(), inFlight: true }}
      />,
    );
    const button = screen.getByRole("button", { name: /Retry…/ });
    expect(button.hasAttribute("disabled")).toBe(true);
  });

  test("exposes a polite live region so screen readers announce status changes", () => {
    render(<Banner tone="attention" message="Reconnecting…" />);
    expect(screen.getByRole("status")).toBeTruthy();
  });
});
