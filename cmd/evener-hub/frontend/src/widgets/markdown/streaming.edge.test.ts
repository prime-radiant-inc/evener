import { expect, test } from "vitest";
import { closeOpenMarkdown } from "./streaming";

test("a nested quoted heading resets the parent list emphasis and closes only its own emphasis", () => {
  const source = "> - text **parent\n> > ## heading *child";

  expect(closeOpenMarkdown(source)).toBe(`${source}*`);
  expect(closeOpenMarkdown("> - text **parent")).toBe("> - text **parent**");
});

test("an empty nested quote ends emphasis from the parent quoted list paragraph", () => {
  const source = "> - text **parent\n> > ";

  expect(closeOpenMarkdown(source)).toBe(source);
  expect(closeOpenMarkdown("> - text **parent")).toBe("> - text **parent**");
});

test("a new top-level heading discards prior paragraph emphasis but closes emphasis opened in the heading", () => {
  const source = "**abandoned\n## heading *active";

  expect(closeOpenMarkdown(source)).toBe(`${source}*`);
  expect(closeOpenMarkdown("## heading *active")).toBe("## heading *active*");
});
