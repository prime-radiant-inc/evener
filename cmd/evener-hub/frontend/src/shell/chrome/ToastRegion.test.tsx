import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { ToastRegion } from "./ToastRegion";

test("renders widgets/toast's aria-live=polite region", () => {
  render(<ToastRegion />);
  const region = screen.getByRole("region", { name: "Notifications" });
  expect(region.getAttribute("aria-live")).toBe("polite");
});
