import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Disclosure } from "./Disclosure";

describe("Disclosure", () => {
  it("exposes disclosure state through a button", () => {
    render(
      <Disclosure summary="Read output" expanded={false} onToggle={() => {}}>
        Body
      </Disclosure>,
    );
    expect(screen.getByRole("button", { name: "Read output" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });

  it("uses stable controls and renders its region only while expanded", () => {
    const { rerender } = render(
      <Disclosure summary="Read output" expanded={false} onToggle={() => {}}>
        Body
      </Disclosure>,
    );
    const button = screen.getByRole("button", { name: "Read output" });
    const controls = button.getAttribute("aria-controls");
    expect(controls).toBeTruthy();
    expect(screen.queryByRole("region")).not.toBeInTheDocument();

    rerender(
      <Disclosure summary="Read output" expanded onToggle={() => {}}>
        Body
      </Disclosure>,
    );
    expect(button).toHaveAttribute("aria-controls", controls);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("region")).toHaveAttribute("id", controls);
    expect(screen.getByText("Body")).toBeVisible();
  });

  it("requests its next controlled state", () => {
    const onToggle = vi.fn();
    render(
      <Disclosure summary="Read output" expanded={false} onToggle={onToggle}>
        Body
      </Disclosure>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Read output" }));
    expect(onToggle).toHaveBeenCalledWith(true);
  });
});
