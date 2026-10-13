import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { FormRow } from "./index";

test("associates the visible label with the control via htmlFor/id", () => {
  render(
    <FormRow label="Hub address" htmlFor="hub-addr">
      <input id="hub-addr" />
    </FormRow>,
  );
  expect(screen.getByLabelText("Hub address")).toBeTruthy();
});

test("without htmlFor the row is a group named by its label", () => {
  const { container } = render(
    <FormRow label="Model fallbacks">
      <input aria-label="first" />
      <button type="button">second</button>
    </FormRow>,
  );
  const group = screen.getByRole("group", { name: "Model fallbacks" });
  expect(group.contains(screen.getByRole("textbox", { name: "first" }))).toBe(true);
  expect(group.contains(screen.getByRole("button", { name: "second" }))).toBe(true);
  expect(container.querySelector("label")).toBeNull();
});

test("a group's help describes the group", () => {
  const help = "group help";
  render(
    <FormRow label="Model fallbacks" help={help}>
      <input aria-label="first" />
    </FormRow>,
  );
  const describedBy = screen.getByRole("group").getAttribute("aria-describedby");
  expect(document.getElementById(describedBy ?? "")?.textContent).toBe(help);
});

test("a group's error is an alert that describes the group", () => {
  render(
    <FormRow label="Model fallbacks" help="group help" error="group error">
      <input aria-label="first" />
    </FormRow>,
  );
  const describedBy = screen.getByRole("group").getAttribute("aria-describedby");
  expect(document.getElementById(describedBy ?? "")).toBe(screen.getByRole("alert"));
});

test("a group with neither help nor error has no aria-describedby", () => {
  render(
    <FormRow label="Model fallbacks">
      <input aria-label="first" />
    </FormRow>,
  );
  expect(screen.getByRole("group").hasAttribute("aria-describedby")).toBe(false);
});

test("a row with htmlFor is not a group", () => {
  render(
    <FormRow label="Hub address" htmlFor="hub-addr">
      <input id="hub-addr" />
    </FormRow>,
  );
  expect(screen.queryByRole("group")).toBeNull();
});

test("renders children inside the row", () => {
  render(
    <FormRow label="Hub address" htmlFor="hub-addr">
      <input id="hub-addr" data-testid="the-control" />
    </FormRow>,
  );
  expect(screen.getByTestId("the-control")).toBeTruthy();
});

test("renders help text when provided and no error", () => {
  render(
    <FormRow label="Spawn timeout" htmlFor="spawn-timeout" help="Applies to every new session.">
      <input id="spawn-timeout" />
    </FormRow>,
  );
  expect(screen.getByText("Applies to every new session.")).toBeTruthy();
});

test("renders no help/error paragraph when neither is provided", () => {
  const { container } = render(
    <FormRow label="Hub address" htmlFor="hub-addr">
      <input id="hub-addr" />
    </FormRow>,
  );
  expect(container.querySelectorAll("p")).toHaveLength(0);
});

test("error replaces help when both are provided", () => {
  render(
    <FormRow
      label="Plugin directory"
      htmlFor="plugin-dir"
      help="Absolute path on the hub's filesystem."
      error="Path does not exist."
    >
      <input id="plugin-dir" />
    </FormRow>,
  );
  expect(screen.getByText("Path does not exist.")).toBeTruthy();
  expect(screen.queryByText("Absolute path on the hub's filesystem.")).toBeNull();
});

test("the error message carries role=alert so it's announced", () => {
  render(
    <FormRow label="Plugin directory" htmlFor="plugin-dir" error="Path does not exist.">
      <input id="plugin-dir" />
    </FormRow>,
  );
  expect(screen.getByRole("alert").textContent).toBe("Path does not exist.");
});

test("the row carries a distinct error-state class when error is present", () => {
  const { container, rerender } = render(
    <FormRow label="Plugin directory" htmlFor="plugin-dir">
      <input id="plugin-dir" />
    </FormRow>,
  );
  const cleanClassName = container.firstElementChild?.className ?? "";

  rerender(
    <FormRow label="Plugin directory" htmlFor="plugin-dir" error="Path does not exist.">
      <input id="plugin-dir" />
    </FormRow>,
  );
  const errorClassName = container.firstElementChild?.className ?? "";

  expect(errorClassName).not.toBe(cleanClassName);
});

test("only reaches for --danger/--attention/--alive via tokens.css-mixed vars, and only for the error state (token-contract's allowlist)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "formrow.module.css"), "utf8");
  expect(css).toContain("var(--danger");
});
