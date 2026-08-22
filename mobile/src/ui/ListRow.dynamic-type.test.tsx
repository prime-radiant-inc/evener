import { readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ListRow } from "./ListRow";

const css = readFileSync(path.join(__dirname, "global.css"), "utf8");
let style: HTMLStyleElement;

beforeEach(() => {
  style = document.createElement("style");
  style.textContent = css;
  document.head.append(style);
});

afterEach(() => {
  cleanup();
  style.remove();
  delete document.documentElement.dataset.contentSize;
});

describe("ListRow — Dynamic Type layout", () => {
  it("stacks the title and subtitle in the shared row body", () => {
    render(
      <ListRow
        title="laptop"
        subtitle="https://hub.example.com:8443"
        ariaLabel="active server"
      />,
    );

    const row = screen.getByRole("button", { name: "active server" });
    const main = row.querySelector<HTMLElement>(".evener-list-row__main");

    expect(main).not.toBeNull();
    expect(getComputedStyle(main as HTMLElement).display).toBe("flex");
    expect(getComputedStyle(main as HTMLElement).flexDirection).toBe("column");
  });

  it("keeps standard-size row text on one line", () => {
    document.documentElement.dataset.contentSize = "large";
    render(
      <ListRow
        title="laptop"
        subtitle="https://hub.example.com:8443"
        ariaLabel="active server"
      />,
    );

    const row = screen.getByRole("button", { name: "active server" });
    const title = row.querySelector<HTMLElement>(".evener-list-row__title");
    const subtitle = row.querySelector<HTMLElement>(
      ".evener-list-row__subtitle",
    );

    for (const text of [title, subtitle]) {
      expect(text).not.toBeNull();
      expect(getComputedStyle(text as HTMLElement).whiteSpace).toBe("nowrap");
      expect(getComputedStyle(text as HTMLElement).overflowWrap).toBe("normal");
    }
  });

  it("wraps title and subtitle text at accessibility sizes", () => {
    document.documentElement.dataset.contentSize =
      "accessibilityExtraExtraExtraLarge";
    render(
      <ListRow
        title="A very long active server profile name"
        subtitle="https://hub.example.com:8443"
        ariaLabel="active server"
      />,
    );

    const row = screen.getByRole("button", { name: "active server" });
    const title = row.querySelector<HTMLElement>(".evener-list-row__title");
    const subtitle = row.querySelector<HTMLElement>(
      ".evener-list-row__subtitle",
    );

    for (const text of [title, subtitle]) {
      expect(text).not.toBeNull();
      expect(getComputedStyle(text as HTMLElement).whiteSpace).toBe("normal");
      expect(getComputedStyle(text as HTMLElement).overflowWrap).toBe(
        "anywhere",
      );
    }
  });
});
