import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, test, vi } from "vitest";
import { Button } from "./widgets/button";

test("the test environment supports React act through render, update, and unmount", async () => {
  const errors = vi.spyOn(console, "error").mockImplementation(() => {});
  const warnings = vi.spyOn(console, "warn").mockImplementation(() => {});
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  try {
    await act(async () => {
      root.render(<Button>Run</Button>);
    });
    expect(container.querySelector("button")?.disabled).toBe(false);
    await act(async () => {
      root.render(<Button disabled>Run</Button>);
    });
    expect(container.querySelector("button")?.disabled).toBe(true);
  } finally {
    await act(async () => root.unmount());
    container.remove();
    try {
      expect(errors.mock.calls).toEqual([]);
      expect(warnings.mock.calls).toEqual([]);
    } finally {
      errors.mockRestore();
      warnings.mockRestore();
    }
  }
});
