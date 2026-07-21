import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ToastRegion } from "./ToastRegion";

afterEach(cleanup);

test("renders widgets/toast's aria-live=polite region", () => {
  render(<ToastRegion />);
  const region = screen.getByRole("region", { name: "Notifications" });
  expect(region.getAttribute("aria-live")).toBe("polite");
});
