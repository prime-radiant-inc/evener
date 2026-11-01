import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import RailSurfaceSection from "./rail";

afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
});

test("the rail surface shows its live sessions and the session nested under its project", async () => {
  render(<RailSurfaceSection />);

  expect((await screen.findAllByText("Fix the flaky prune test")).length).toBeGreaterThan(0);
  expect((await screen.findAllByText("Add the surfaces gallery")).length).toBeGreaterThan(0);
});
