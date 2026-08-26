import { fireEvent, render, screen } from "@testing-library/react";
import { type ReactElement, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { RecoveryBoundary } from "./RecoveryBoundary";

function Broken(): ReactElement {
  throw new Error("renderer failed");
}

function Harness({ reset }: { reset: () => void }) {
  const [broken, setBroken] = useState(true);
  return (
    <RecoveryBoundary
      resetPrototype={() => {
        reset();
        setBroken(false);
      }}
    >
      {broken ? <Broken /> : <p>Recovered locally</p>}
    </RecoveryBoundary>
  );
}

describe("RecoveryBoundary", () => {
  it("catches renderer errors and resets locally", () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    const reset = vi.fn();
    try {
      render(<Harness reset={reset} />);

      const main = screen.getByRole("main");
      expect(main.querySelector("main")).toBeNull();
      expect(screen.getAllByRole("main")).toHaveLength(1);
      expect(screen.getAllByRole("alert")).toHaveLength(1);
      expect(screen.getByRole("alert")).toHaveTextContent(
        "This concept could not be rendered.",
      );
      fireEvent.click(screen.getByRole("button", { name: "Reset prototype" }));
      expect(reset).toHaveBeenCalledTimes(1);
      expect(screen.getByText("Recovered locally")).toBeVisible();
    } finally {
      consoleError.mockRestore();
    }
  });
});
