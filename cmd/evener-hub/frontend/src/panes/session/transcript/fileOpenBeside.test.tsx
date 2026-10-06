import type { ThreadModel } from "@evener/appwire-client";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, test } from "vitest";
import { documentPaneState, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { FileOpenBesideButton, fileDocParams } from "./fileOpenBeside";
import "../../doc";

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

beforeEach(() => {
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
});

test("renders an accessible Open beside button that opens a bound doc owned by the actual source pane", () => {
  seedThreadCwd("ref_a", "/home/proj");
  const sourcePaneId = workspaceStore.getState().openPane("transcript", { ref: "ref_a" });
  const model = threadsStore.getState().threads.get("ref_a");
  render(
    <TranscriptRenderProvider thread={model} sourcePaneId={sourcePaneId}>
      <FileOpenBesideButton absPath="/home/proj/src/a.ts" sessionRef="ref_a" />
    </TranscriptRenderProvider>,
  );
  const button = screen.getByRole("button", { name: "Open beside: src/a.ts" });
  fireEvent.click(button);
  const source = workspaceStore.getState().panes.find((pane) => pane.id === sourcePaneId);
  const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!source || !document) throw new Error("actual opening action did not create source and document panes");
  expect(document.params).toEqual({ session: "ref_a", path: "src/a.ts", kind: "file" });
  expect(documentPaneState(document)?.origin).toBe(source);
  expect(documentPaneState(document)?.reference).toEqual({
    path: "src/a.ts",
    cwd: "/home/proj",
    readTarget: "/home/proj/src/a.ts",
    provenance: "absolute",
  });
});

// kata 3qnd: an icon-only control (surrounding pane chrome - Pop out, Fork
// from here - is all icons; this was the one text label among them). The
// accessible name and native tooltip carry what the visible text used to
// (including the path, the way the old title already did) now that there is
// no visible text to read it from - not the drawn glyph, which is
// decorative (aria-hidden, per ForkGlyph's own precedent).
test("Open beside is icon-only: no visible text label, but keeps its accessible name and a title tooltip", () => {
  seedThreadCwd("ref_a", "/home/proj");
  const sourcePaneId = workspaceStore.getState().openPane("transcript", { ref: "ref_a" });
  render(
    <TranscriptRenderProvider thread={threadsStore.getState().threads.get("ref_a")} sourcePaneId={sourcePaneId}>
      <FileOpenBesideButton absPath="/home/proj/src/a.ts" sessionRef="ref_a" />
    </TranscriptRenderProvider>,
  );
  const button = screen.getByRole("button", { name: "Open beside: src/a.ts" });
  expect(button.textContent).toBe(""); // icon only - the SVG carries no text, aria-hidden
  // The tooltip is the one word everywhere; the PATH stays in the aria-label.
  expect(button.getAttribute("title")).toBe("Open");
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
