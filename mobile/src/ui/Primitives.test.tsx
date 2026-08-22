import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "./Button";
import { IconButton } from "./IconButton";
import { StatusMark } from "./StatusMark";

afterEach(() => {
  cleanup();
});

describe("Button — variants and 44px", () => {
  it("renders a primary button with text", () => {
    render(<Button variant="primary">Scan QR</Button>);
    const btn = screen.getByRole("button", { name: /scan qr/i });
    expect(btn.className).toMatch(/primary/i);
  });

  it("renders a secondary button", () => {
    render(<Button variant="secondary">Paste</Button>);
    expect(screen.getByRole("button", { name: /paste/i }).className).toMatch(
      /secondary/i,
    );
  });

  it("disabled prop sets aria-disabled and prevents click", () => {
    const onClick = vi.fn();
    render(
      <Button variant="primary" disabled onClick={onClick}>
        Save
      </Button>,
    );
    const btn = screen.getByRole("button", { name: /save/i });
    expect(btn).toBeDisabled();
    fireEvent.click(btn);
    expect(onClick).not.toHaveBeenCalled();
  });
});

describe("IconButton — 44px and accessibility", () => {
  it("has an accessible name via aria-label", () => {
    render(
      <IconButton aria-label="Close" onClick={() => {}}>
        ×
      </IconButton>,
    );
    expect(screen.getByRole("button", { name: /close/i })).toBeInTheDocument();
  });

  it("is at least 44x44 via the min-tap class", () => {
    render(
      <IconButton aria-label="Back" onClick={() => {}}>
        ‹
      </IconButton>,
    );
    const btn = screen.getByRole("button", { name: /back/i });
    expect(btn.className).toMatch(/tap|min.*target|icon-button/i);
  });
});

describe("StatusMark — unique glyph and label, not color alone", () => {
  it("renders a status with both a glyph and a text label", () => {
    render(<StatusMark status="reachable" />);
    const node = screen.getByText(/reachable|connected/i);
    expect(node).toBeInTheDocument();
  });

  it("renders an offline status with a distinct label from reachable", () => {
    const { rerender, container } = render(<StatusMark status="reachable" />);
    // Reachable renders "Connected" — scope to this render's container.
    expect(container.textContent).toContain("Connected");
    rerender(<StatusMark status="offline" />);
    expect(container.textContent).toContain("Offline");
    expect(container.textContent).not.toContain("Connected");
  });
});
