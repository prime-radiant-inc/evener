import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "./App";

describe("App", () => {
  it("renders the dedicated mobile fixture shell", () => {
    render(<App fixture />);
    expect(screen.getByRole("navigation", { name: "Primary" })).toBeVisible();
    expect(screen.queryByText("Dockview")).not.toBeInTheDocument();
  });
});
