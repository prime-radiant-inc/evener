import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import * as paneActions from "../../../shell/paneActions";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { cwdRelative, FileOpenBesideButton, fileDocParams } from "./fileOpenBeside";

afterEach(cleanup);

// --- cwdRelative: a file arg made relative to the session cwd, or undefined
// (no affordance) when it is out of the cwd. Handles BOTH an absolute arg
// (legacy renderer.js:2204-2210) and an already-relative arg (execenv resolve()
// joins it against the cwd root, local.go:1533). --------------------------

test("an absolute path inside the cwd is relativized (prefix stripped)", () => {
  expect(cwdRelative("/home/proj/src/a.ts", "/home/proj")).toBe("src/a.ts");
  expect(cwdRelative("/home/proj/src/a.ts", "/home/proj/")).toBe("src/a.ts"); // trailing slash tolerated
});

test("an absolute path OUTSIDE the cwd earns no affordance", () => {
  expect(cwdRelative("/etc/passwd", "/home/proj")).toBeUndefined();
  expect(cwdRelative("/home/project-other/a.ts", "/home/proj")).toBeUndefined(); // sibling prefix, not inside
});

test("the cwd directory itself is not a file", () => {
  expect(cwdRelative("/home/proj", "/home/proj")).toBeUndefined();
});

test("an already-relative arg is accepted as cwd-relative", () => {
  expect(cwdRelative("src/a.ts", "/home/proj")).toBe("src/a.ts");
  expect(cwdRelative("a.ts", "/home/proj")).toBe("a.ts");
});

test("a relative arg that escapes the cwd earns no affordance", () => {
  expect(cwdRelative("../secret.ts", "/home/proj")).toBeUndefined();
  expect(cwdRelative("src/../../secret.ts", "/home/proj")).toBeUndefined();
});

test("empty inputs earn no affordance", () => {
  expect(cwdRelative("", "/home/proj")).toBeUndefined();
  expect(cwdRelative("/home/proj/a.ts", "")).toBeUndefined();
});

// --- fileDocParams: builds a file DocParams, or undefined when anything the
// affordance needs is missing (no ref, no cwd, or out-of-cwd path) ----------

test("fileDocParams builds a file DocParams for an in-cwd path", () => {
  expect(fileDocParams("/home/proj/src/a.ts", "ref_a", "/home/proj")).toEqual({
    session: "ref_a",
    path: "src/a.ts",
    kind: "file",
  });
});

test("fileDocParams is undefined when the ref, cwd, or path is missing/out-of-cwd", () => {
  expect(fileDocParams(undefined, "ref_a", "/home/proj")).toBeUndefined();
  expect(fileDocParams("/home/proj/a.ts", undefined, "/home/proj")).toBeUndefined();
  expect(fileDocParams("/home/proj/a.ts", "ref_a", undefined)).toBeUndefined();
  expect(fileDocParams("/home/proj/a.ts", "ref_a", "")).toBeUndefined();
  expect(fileDocParams("/etc/passwd", "ref_a", "/home/proj")).toBeUndefined();
});

test("fileDocParams picks kind:image for an image-extension path (DECISION C), kind:file otherwise", () => {
  for (const ext of ["png", "jpg", "jpeg", "gif", "webp", "PNG"]) {
    expect(fileDocParams(`/home/proj/pic.${ext}`, "ref_a", "/home/proj")?.kind).toBe("image");
  }
  expect(fileDocParams("/home/proj/a.ts", "ref_a", "/home/proj")?.kind).toBe("file");
  // SVG is excluded from /doc/image (XSS guard) - opens as a file, not an image.
  expect(fileDocParams("/home/proj/icon.svg", "ref_a", "/home/proj")?.kind).toBe("file");
});

// --- FileOpenBesideButton: reads the session cwd from the threads store (by
// ref) and routes a click through openDocBeside; renders nothing (no
// affordance) for an out-of-cwd path. --------------------------------------

function seedThreadCwd(ref: string, cwd: string): void {
  const model = { ref, cwd, turns: [] } as unknown as ThreadModel;
  threadsStore.setState({ threads: new Map([[ref, model]]) });
}

beforeEach(() => resetThreadsStoreForTests());

test("renders an accessible Open beside button that opens a doc pane beside with the cwd-relative path", () => {
  seedThreadCwd("ref_a", "/home/proj");
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  render(<FileOpenBesideButton absPath="/home/proj/src/a.ts" sessionRef="ref_a" />);
  const button = screen.getByRole("button", { name: "Open beside: src/a.ts" });
  fireEvent.click(button);
  expect(spy).toHaveBeenCalledWith({ type: "doc", params: { session: "ref_a", path: "src/a.ts", kind: "file" } });
  spy.mockRestore();
});

// kata 3qnd: an icon-only control (surrounding pane chrome - Pop out, Fork
// from here - is all icons; this was the one text label among them). The
// accessible name and native tooltip carry what the visible text used to
// (including the path, the way the old title already did) now that there is
// no visible text to read it from - not the drawn glyph, which is
// decorative (aria-hidden, per ForkGlyph's own precedent).
test("Open beside is icon-only: no visible text label, but keeps its accessible name and a title tooltip", () => {
  seedThreadCwd("ref_a", "/home/proj");
  render(<FileOpenBesideButton absPath="/home/proj/src/a.ts" sessionRef="ref_a" />);
  const button = screen.getByRole("button", { name: "Open beside: src/a.ts" });
  expect(button.textContent).toBe(""); // icon only - the SVG carries no text, aria-hidden
  expect(button.getAttribute("title")).toBe("Open beside: src/a.ts");
});

test("renders nothing for an out-of-cwd path (no affordance)", () => {
  seedThreadCwd("ref_a", "/home/proj");
  const { container } = render(<FileOpenBesideButton absPath="/etc/passwd" sessionRef="ref_a" />);
  expect(container.firstChild).toBeNull();
});

test("renders nothing until the thread's cwd has hydrated", () => {
  const { container } = render(<FileOpenBesideButton absPath="/home/proj/a.ts" sessionRef="ref_unhydrated" />);
  expect(container.firstChild).toBeNull();
});
