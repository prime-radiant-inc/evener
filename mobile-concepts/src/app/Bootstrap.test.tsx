import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Bootstrap } from "./Bootstrap";

describe("Bootstrap", () => {
  it("identifies the package as an offline concept app", () => {
    render(<Bootstrap />);
    expect(screen.getByRole("main")).toHaveAttribute(
      "data-network-mode",
      "offline",
    );
    expect(screen.getByRole("heading", { level: 1 })).toHaveAccessibleName(
      "Evener Concepts",
    );
  });
});
