// Focused semantic tests for the shared presentation primitives.
// Covers disclosure expansion/ARIA, decorative versus labeled icons, status
// text plus non-color marker, and formatter output.

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Disclosure } from "./Disclosure";
import {
  basename,
  formatDuration,
  formatRelativeTime,
  formatUsage,
} from "./format";
import { Icon, type IconName } from "./Icon";
import { StatusLabel } from "./StatusLabel";

afterEach(() => {
  cleanup();
});

describe("Disclosure", () => {
  it("exposes disclosure state through a button", () => {
    render(
      <Disclosure summary="Read output" expanded={false} onToggle={() => {}}>
        Body
      </Disclosure>,
    );
    expect(screen.getByRole("button", { name: "Read output" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });

  it("uses stable controls and renders its region only while expanded", () => {
    const { rerender } = render(
      <Disclosure summary="Read output" expanded={false} onToggle={() => {}}>
        Body
      </Disclosure>,
    );
    const button = screen.getByRole("button", { name: "Read output" });
    const controls = button.getAttribute("aria-controls");
    expect(controls).toBeTruthy();
    expect(screen.queryByRole("region")).not.toBeInTheDocument();

    rerender(
      <Disclosure summary="Read output" expanded onToggle={() => {}}>
        Body
      </Disclosure>,
    );
    expect(button).toHaveAttribute("aria-controls", controls);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("region")).toHaveAttribute("id", controls);
    expect(screen.getByText("Body")).toBeVisible();
  });

  it("requests its next controlled state", () => {
    const onToggle = vi.fn();
    render(
      <Disclosure summary="Read output" expanded={false} onToggle={onToggle}>
        Body
      </Disclosure>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Read output" }));
    expect(onToggle).toHaveBeenCalledWith(true);
  });
});

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
    const { container } = render(
      iconNames.map((name) => <Icon key={name} name={name} decorative />),
    );
    const shapes = [...container.querySelectorAll("svg")].map((svg) =>
      svg.innerHTML.replaceAll(/\s/g, ""),
    );
    expect(new Set(shapes).size).toBe(iconNames.length);
  });
});

describe("StatusLabel", () => {
  it("shows status text plus a non-color data attribute marker", () => {
    const { container } = render(<StatusLabel state="running" />);
    expect(screen.getByText("Running")).toBeInTheDocument();
    expect(container.querySelector("[data-status-state]")).toHaveAttribute(
      "data-status-state",
      "running",
    );
  });

  it("distinguishes states through the data marker alone", () => {
    const states = [
      "attention",
      "running",
      "success",
      "failed",
      "idle",
      "unknown",
    ] as const;
    const markers: string[] = [];
    for (const state of states) {
      const { container } = render(<StatusLabel state={state} />);
      const el = container.querySelector("[data-status-state]");
      markers.push(el?.getAttribute("data-status-state") ?? "");
      cleanup();
    }
    expect(new Set(markers).size).toBe(states.length);
  });
});

describe("formatDuration", () => {
  it.each([
    [0, "0s"],
    [999, "0s"],
    [1_000, "1s"],
    [59_000, "59s"],
    [60_000, "1m"],
    [61_000, "1m 1s"],
    [3_600_000, "1h"],
    [3_661_000, "1h 1m"],
  ])("formats %i milliseconds as %s", (value, expected) => {
    expect(formatDuration(value)).toBe(expected);
  });

  it.each([-1, Number.NaN, Number.POSITIVE_INFINITY])(
    "rejects invalid duration %s",
    (value) => expect(() => formatDuration(value)).toThrow(RangeError),
  );
});

describe("formatUsage", () => {
  it.each([
    [0, "0 tokens"],
    [1, "1 token"],
    [999, "999 tokens"],
    [1_000, "1K tokens"],
    [1_500, "1.5K tokens"],
    [1_000_000, "1M tokens"],
  ])("formats %i tokens as %s", (value, expected) => {
    expect(formatUsage(value)).toBe(expected);
  });

  it.each([-1, 1.5, Number.NaN])("rejects invalid usage %s", (value) => {
    expect(() => formatUsage(value)).toThrow(RangeError);
  });
});

describe("basename", () => {
  it.each([
    ["", ""],
    ["/", "/"],
    ["/workspace/evener", "evener"],
    ["/workspace/evener/", "evener"],
    ["C:\\workspace\\evener", "evener"],
    ["/工作/原型", "原型"],
  ])("extracts %s as %s", (path, expected) => {
    expect(basename(path)).toBe(expected);
  });
});

describe("formatRelativeTime", () => {
  it("returns the deterministic fixture label", () => {
    expect(formatRelativeTime("2 minutes ago")).toBe("2 minutes ago");
  });

  it("rejects an empty fixture label", () => {
    expect(() => formatRelativeTime("   ")).toThrow(RangeError);
  });
});
