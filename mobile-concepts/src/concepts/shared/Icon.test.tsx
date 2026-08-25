import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Icon, type IconName } from "./Icon";

const iconNames: readonly IconName[] = [
  "sessions",
  "search",
  "new",
  "settings",
  "back",
  "switch",
  "lab",
  "warning",
  "running",
  "complete",
  "waiting",
  "failed",
  "tool",
  "work",
  "voice",
  "send",
  "stop",
  "close",
  "chevron",
];

describe("Icon", () => {
  it("keeps decorative icons out of the accessibility tree", () => {
    const { container } = render(<Icon name="sessions" decorative />);
    expect(container.querySelector("svg")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });

  it("labels non-decorative icons", () => {
    render(<Icon name="warning" decorative={false} label="Needs attention" />);
    expect(
      screen.getByRole("img", { name: "Needs attention" }),
    ).toBeInTheDocument();
  });

  it("provides a distinct local shape for every supported icon", () => {
    const { container } = render(
      iconNames.map((name) => <Icon key={name} name={name} decorative />),
    );
    const shapes = [...container.querySelectorAll("svg")].map((svg) =>
      svg.innerHTML.replaceAll(/\s/g, ""),
    );
    expect(new Set(shapes).size).toBe(iconNames.length);
  });
});
