import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./fsTools";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- read_file --------------------------------------------------------

test("read_file: summary leads with the target path and a derived line range", () => {
  const d = toolRendererFor("read_file");
  const args = JSON.stringify({ file_path: "src/app.ts" });
  expect(d.summary(item({ toolName: "read_file", argumentsJSON: args, output: "a\nb\nc\n" }))).toBe(
    "Read src/app.ts · lines 1-3",
  );
});

test("read_file: falls back to `path` when `file_path` is absent", () => {
  const d = toolRendererFor("read_file");
  expect(d.summary(item({ toolName: "read_file", argumentsJSON: JSON.stringify({ path: "b.ts" }) }))).toBe(
    "Read b.ts · lines 1",
  );
});

test("read_file: an explicit offset/limit wins over the output's own newline count", () => {
  const d = toolRendererFor("read_file");
  const args = JSON.stringify({ file_path: "a.ts", offset: 10, limit: 5 });
  expect(d.summary(item({ toolName: "read_file", argumentsJSON: args, output: "only one line" }))).toBe(
    "Read a.ts · lines 10-14",
  );
});

test("read_file: a non-positive offset defaults to 1", () => {
  const d = toolRendererFor("read_file");
  const args = JSON.stringify({ file_path: "a.ts", offset: 0 });
  expect(d.summary(item({ toolName: "read_file", argumentsJSON: args, output: "x" }))).toBe("Read a.ts · lines 1");
});

test("read_file: no derivable count renders a bare start line, not a range", () => {
  const d = toolRendererFor("read_file");
  const args = JSON.stringify({ file_path: "a.ts" });
  expect(d.summary(item({ toolName: "read_file", argumentsJSON: args, output: "" }))).toBe("Read a.ts · lines 1");
});

test("read_file: body renders the output text", () => {
  const d = toolRendererFor("read_file");
  const Body = d.body!;
  render(<Body item={item({ toolName: "read_file", output: "file contents here" })} live={false} />);
  expect(screen.getByText("file contents here")).toBeTruthy();
});

// --- grep / grep_files / grep_search -----------------------------------

test("grep: summary composes pattern, path, and hit count", () => {
  const d = toolRendererFor("grep");
  const args = JSON.stringify({ pattern: "TODO", path: "src" });
  expect(d.summary(item({ toolName: "grep", argumentsJSON: args, output: "a\nb\n" }))).toBe(
    'Searched "TODO" in src · 2 hits',
  );
});

test("grep: path defaults to the cwd marker when absent", () => {
  const d = toolRendererFor("grep");
  const args = JSON.stringify({ pattern: "x" });
  expect(d.summary(item({ toolName: "grep", argumentsJSON: args, output: "" }))).toBe('Searched "x" in . · 0 hits');
});

test("grep: a glob_filter is appended in parens when present", () => {
  const d = toolRendererFor("grep");
  const args = JSON.stringify({ pattern: "x", path: "src", glob_filter: "*.ts" });
  expect(d.summary(item({ toolName: "grep", argumentsJSON: args, output: "a\n" }))).toBe(
    'Searched "x" in src (*.ts) · 1 hits',
  );
});

test("grep: a long pattern is clipped to 50 chars", () => {
  const d = toolRendererFor("grep");
  const longPattern = "x".repeat(60);
  const args = JSON.stringify({ pattern: longPattern });
  const summary = d.summary(item({ toolName: "grep", argumentsJSON: args, output: "" }));
  expect(summary.startsWith(`Searched "${"x".repeat(50)}…"`)).toBe(true);
});

test("grep_files and grep_search alias to the same descriptor as grep", () => {
  const grep = toolRendererFor("grep");
  expect(toolRendererFor("grep_files")).toBe(grep);
  expect(toolRendererFor("grep_search")).toBe(grep);
});

// --- list_dir / list_directory ------------------------------------------

test("list_dir: summary composes path and entry count", () => {
  const d = toolRendererFor("list_dir");
  const args = JSON.stringify({ path: "src/widgets" });
  expect(d.summary(item({ toolName: "list_dir", argumentsJSON: args, output: "a\nb\nc\n" }))).toBe(
    "Listed src/widgets · 3 entries",
  );
});

test("list_dir: path defaults to the cwd marker, and a pattern arg is parenthesized", () => {
  const d = toolRendererFor("list_dir");
  const args = JSON.stringify({ pattern: "*.css" });
  expect(d.summary(item({ toolName: "list_dir", argumentsJSON: args, output: "" }))).toBe(
    "Listed . (*.css) · 0 entries",
  );
});

test("list_directory aliases to the same descriptor as list_dir", () => {
  expect(toolRendererFor("list_directory")).toBe(toolRendererFor("list_dir"));
});

// --- glob -----------------------------------------------------------------

test("glob: summary composes the pattern and match count", () => {
  const d = toolRendererFor("glob");
  const args = JSON.stringify({ pattern: "**/*.test.ts" });
  expect(d.summary(item({ toolName: "glob", argumentsJSON: args, output: "a\nb\n" }))).toBe(
    "Matched **/*.test.ts · 2 matches",
  );
});

test("glob: falls back to a `glob` arg key when `pattern` is absent", () => {
  const d = toolRendererFor("glob");
  const args = JSON.stringify({ glob: "*.ts" });
  expect(d.summary(item({ toolName: "glob", argumentsJSON: args, output: "" }))).toBe("Matched *.ts · 0 matches");
});

// --- shared cheap body ------------------------------------------------

test("cheap body (grep/ls/glob) head-clips long output at 8000 chars, no elision note", () => {
  const d = toolRendererFor("glob");
  const Body = d.body!;
  const longOutput = "y".repeat(9000);
  render(<Body item={item({ toolName: "glob", output: longOutput })} live={false} />);
  expect(screen.getByText(`${"y".repeat(8000)}…`)).toBeTruthy();
});

test("cheap body renders nothing when output is blank", () => {
  const d = toolRendererFor("glob");
  const Body = d.body!;
  const { container } = render(<Body item={item({ toolName: "glob", output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});
