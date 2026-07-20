import { test, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { App } from "./App";

test("renders the shell placeholder", () => {
  render(<App />);
  expect(screen.getByText(/workspace shell/i)).toBeTruthy();
});
