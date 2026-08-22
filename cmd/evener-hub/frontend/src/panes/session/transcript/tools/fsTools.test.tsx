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
// An image read's body renders nothing: the image displays below via
// ToolCallItem's ImageGallery at read_file's up-to-600px size, so the
// "[image: png, N bytes]" header is noise. A PDF read keeps its header
// (minus the stale "base64 data follows" phrase - registry.go's
// ParseImageResult routes the payload elsewhere) since no in-transcript
// PDF preview duplicates it.

test("read_file: an image read's body renders nothing - the gallery carries the image, so the '[image: png, N bytes]' header is noise", () => {
  const d = toolRendererFor("read_file");
  const Body = d.body!;
  const { container } = render(
    <Body
      item={item({ toolName: "read_file", output: "[image: png, 178337 bytes, base64 data follows]" })}
      live={false}
    />,
  );
  expect(container.textContent).toBe("");
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
  const { container } = render(
    <Body
      item={item({ toolName: "read_file", output: "[image: png, 3 bytes, base64 data follows]\nQUJD" })}
      live={false}
    />,
  );
  expect(container.textContent).toBe("");
});

test("read_file: plain text output (not the image/document marker) renders exactly as before, unfolded and unclipped", () => {
  const d = toolRendererFor("read_file");
  const Body = d.body!;
  render(<Body item={item({ toolName: "read_file", output: "[not-a-marker] just text" })} live={false} />);
  expect(screen.getByText("[not-a-marker] just text")).toBeTruthy();
});

// --- read_file autoExpand (image reads open by default) --------------------
// The picture is an image read's whole output, so the row auto-expands to
// show it without a click. A text or PDF read keeps the usual collapsed
// default. autoExpand shares the body's own isImageRead detection, so the
// auto-open and the empty body answer the same question.

test("read_file: autoExpand is defined (parity with edit_file's own undefined check)", () => {
  const d = toolRendererFor("read_file");
  expect(d.autoExpand).toBeDefined();
});

test("read_file: an image read auto-expands - the picture is the call's whole output", () => {
  const d = toolRendererFor("read_file");
  expect(
    d.autoExpand!(item({ toolName: "read_file", output: "[image: png, 178337 bytes, base64 data follows]" })),
  ).toBe(true);
});

test("read_file: a document (PDF) read does NOT auto-expand - no in-transcript preview duplicates its header", () => {
  const d = toolRendererFor("read_file");
  expect(
    d.autoExpand!(item({ toolName: "read_file", output: "[document: pdf, 90210 bytes, base64 data follows]" })),
  ).toBe(false);
});

test("read_file: plain text output does NOT auto-expand", () => {
  const d = toolRendererFor("read_file");
  expect(d.autoExpand!(item({ toolName: "read_file", output: "just text" }))).toBe(false);
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
