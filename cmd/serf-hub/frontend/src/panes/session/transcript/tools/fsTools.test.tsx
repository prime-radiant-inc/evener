import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
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

// --- read_file body: image/document marker (kata 1nr4) --------------------
// agent/internal/tool/registry.go's ParseImageResult cuts a ReadFile image
// response at its first "\n", keeping only the header
// ("[image: png, N bytes, base64 data follows]") as the tool result's own
// Output text - the base64 itself goes into a separate ImageData field that
// never reaches item.output. So the base64 never actually "follows" in the
// transcript text a reader sees; that phrase is left over from the
// agent-internal string this was cut from. The thumbnail this now renders
// alongside (ToolCallItem's <ImageGallery images={item.outputImages} />)
// carries the real image; the body just needs to stop promising more text.

test("read_file: an image read's body shows the header without the misleading 'base64 data follows' phrase", () => {
  const d = toolRendererFor("read_file");
  const Body = d.body!;
  render(
    <Body
      item={item({ toolName: "read_file", output: "[image: png, 178337 bytes, base64 data follows]" })}
      live={false}
    />,
  );
  expect(screen.getByText("[image: png, 178337 bytes]")).toBeTruthy();
  expect(screen.queryByText(/base64 data follows/)).toBeNull();
});

test("read_file: a document (PDF) read's body is cleaned up the same way", () => {
  const d = toolRendererFor("read_file");
  const Body = d.body!;
  render(
    <Body
      item={item({ toolName: "read_file", output: "[document: pdf, 90210 bytes, base64 data follows]" })}
      live={false}
    />,
  );
  expect(screen.getByText("[document: pdf, 90210 bytes]")).toBeTruthy();
  expect(screen.queryByText(/base64 data follows/)).toBeNull();
});

test("read_file: a base64 payload that DID make it into output (older daemon, or the pre-parse shape) is still dropped, not dumped as text", () => {
  const d = toolRendererFor("read_file");
  const Body = d.body!;
  render(
    <Body
      item={item({ toolName: "read_file", output: "[image: png, 3 bytes, base64 data follows]\nQUJD" })}
      live={false}
    />,
  );
  expect(screen.getByText("[image: png, 3 bytes]")).toBeTruthy();
  expect(screen.queryByText(/QUJD/)).toBeNull();
});

test("read_file: plain text output (not the image/document marker) renders exactly as before, unfolded and unclipped", () => {
  const d = toolRendererFor("read_file");
  const Body = d.body!;
  render(<Body item={item({ toolName: "read_file", output: "[not-a-marker] just text" })} live={false} />);
  expect(screen.getByText("[not-a-marker] just text")).toBeTruthy();
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

// --- args at settle (protocol/reducer.ts preserves argumentsJSON through
// item/completed - see R2) -------------------------------------------------
// Ground truth: internal/appprojector/appwire_projection.go's
// EventToolCallEnd case never sets argumentsJson on the completed item
// itself, but the reducer now carries the prior item's argumentsJSON
// forward rather than dropping it - so a settled ItemModel's own
// argumentsJSON is intact by the time it reaches this descriptor.

test("read_file: the target reads straight from a settled item's own argumentsJSON", () => {
  const d = toolRendererFor("read_file");
  const settled = item({
    toolName: "read_file",
    argumentsJSON: JSON.stringify({ file_path: "settled.ts" }),
    output: "x\n",
  });
  expect(d.summary(settled)).toBe("Read settled.ts · lines 1-1");
});
