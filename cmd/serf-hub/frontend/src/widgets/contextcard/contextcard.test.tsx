import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import rawStyles from "./contextcard.module.css";
import { ContextCard } from "./index";

afterEach(cleanup);

const styles = {
  card: requireClass(rawStyles.card, "contextcard.module.css", "card"),
};

test("renders source and snippet", () => {
  render(<ContextCard source="agent-notes.md" snippet="The deploy pipeline retries three times." />);
  expect(screen.getByText("agent-notes.md")).toBeTruthy();
  expect(screen.getByText("The deploy pipeline retries three times.")).toBeTruthy();
});

test("renders meta when given", () => {
  render(<ContextCard source="agent-notes.md" snippet="s" meta="1.2k chars" />);
  expect(screen.getByText("1.2k chars")).toBeTruthy();
});

test("omits meta when not given", () => {
  render(<ContextCard source="agent-notes.md" snippet="s" />);
  expect(screen.queryByText(/chars/)).toBeNull();
});

test("renders as a plain container when no href is given", () => {
  const { container } = render(<ContextCard source="agent-notes.md" snippet="s" />);
  expect(container.querySelector("a")).toBeNull();
  expect(container.firstElementChild?.tagName).toBe("DIV");
  expect(container.firstElementChild?.classList.contains(styles.card)).toBe(true);
});

test("renders as a link carrying the card class when href is given", () => {
  const { container } = render(<ContextCard source="agent-notes.md" snippet="s" href="https://example.com/doc" />);
  const link = container.querySelector("a");
  expect(link).toBeTruthy();
  expect(link?.getAttribute("href")).toBe("https://example.com/doc");
  expect(link?.classList.contains(styles.card)).toBe(true);
});

test("renders a leading glyph before the source line", () => {
  const { container } = render(<ContextCard source="agent-notes.md" snippet="s" />);
  expect(container.querySelector("svg")).toBeTruthy();
});

test("carries the source as its accessible name when it's a link", () => {
  render(<ContextCard source="agent-notes.md" snippet="s" href="https://example.com/doc" />);
  expect(screen.getByRole("link", { name: /agent-notes\.md/ })).toBeTruthy();
});
