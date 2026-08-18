import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ToastRegion } from "./ToastRegion";

afterEach(cleanup);

test("renders widgets/toast's aria-live=polite region", () => {
  render(<ToastRegion />);
  const region = screen.getByRole("region", { name: "Notifications" });
  expect(region.getAttribute("aria-live")).toBe("polite");
});
