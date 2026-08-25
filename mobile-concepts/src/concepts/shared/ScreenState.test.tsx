import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ScreenState } from "./ScreenState";
import { StatusLabel, type StatusLabelState } from "./StatusLabel";

describe("ScreenState", () => {
  it("announces loading as status", () => {
    render(
      <ScreenState state={{ kind: "loading", title: "Loading sessions" }} />,
    );
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it.each(["empty", "offline"] as const)(
    "renders %s as a named region without an implicit retry",
    (kind) => {
      render(
        <ScreenState
          state={{ kind, title: `${kind} title`, detail: `${kind} detail` }}
        />,
      );
      expect(
        screen.getByRole("region", { name: `${kind} title` }),
      ).toHaveAttribute("data-screen-state", kind);
      expect(screen.queryByRole("button")).not.toBeInTheDocument();
    },
  );

  it("exposes recoverable errors as alerts and invokes retry", () => {
    const retry = vi.fn();
    render(
      <ScreenState
        state={{ kind: "error", title: "Could not load", detail: "Try again" }}
        retryAction={retry}
      />,
    );
    expect(screen.getByRole("alert")).toHaveAttribute(
      "data-screen-state",
      "error",
    );
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledOnce();
  });
});

describe("StatusLabel", () => {
  it.each(["attention", "running", "complete", "waiting", "failed"] as const)(
    "renders visible, machine-readable %s state",
    (state) => {
      const { container } = render(<StatusLabel state={state} />);
      const status = container.querySelector(`[data-status-state="${state}"]`);
      expect(status).toBeVisible();
      expect(status).toHaveTextContent(/\S/);
      expect(status?.querySelector("svg")).toBeInTheDocument();
    },
  );

  it("uses a distinct shape for every state", () => {
    const states: readonly StatusLabelState[] = [
      "attention",
      "running",
      "complete",
      "waiting",
      "failed",
    ];
    const { container } = render(
      states.map((state) => <StatusLabel key={state} state={state} />),
    );
    const shapes = [...container.querySelectorAll("svg")].map((svg) =>
      svg.innerHTML.replaceAll(/\s/g, ""),
    );
    expect(new Set(shapes).size).toBe(states.length);
  });
});
