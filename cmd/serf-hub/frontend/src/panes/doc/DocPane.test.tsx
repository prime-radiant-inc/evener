import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeEach, expect, type MockInstance, test, vi } from "vitest";
import * as docContentModule from "../../protocol/docContent";
import { DOC_FILE_MAX_BYTES, type DocFileContent, DocFileError, docImageURL } from "../../protocol/docContent";
import type { ThreadModel } from "../../protocol/model";
import { registerPaneForTests } from "../../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import DocPane from "./DocPane";
// Side-effect import: registers the real "doc" pane type (index.tsx), needed
// below so workspaceStore.openPane("doc", ...) can seed an already-open doc
// pane to focus away from before testing that Back moves focus back.
import "./index";

// Only readDocFile is mocked; docImageURL / DocFileError / DOC_FILE_MAX_BYTES
// stay real so the pane's URL building and error-kind switch run for real.
//
// vi.spyOn, not vi.mock: see ModelField.test.tsx's own comment on this exact
// pattern - under a shared module registry (isolate:false), a vi.mock()
// factory registered here only replaces what THIS file's own import
// resolves to. If docContent.ts's module instance was already evaluated by
// an earlier file (e.g. register.test.ts's "./openDoc" import, or any other
// file that renders DocPane), DocPane.tsx's own `readDocFile` binding is
// already resolved to the real function by the time this mock registers -
// spying on the real module's own export patches the one binding every
// importer actually shares, regardless of import order.
let mockRead: MockInstance<typeof docContentModule.readDocFile>;

// A minimal, test-only "session" pane registration - mirrors
// Transcript.test.tsx's own precedent: real registerPane/paneFor/openPane
// machinery, without pulling in the actual (heavier) panes/session module.
afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

beforeEach(() => {
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  mockRead = vi.spyOn(docContentModule, "readDocFile");
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  registerDockviewApi(null); // never leak a fake dockview host to another test
});

function textContent(over: Partial<DocFileContent> = {}): DocFileContent {
  return { text: "", binary: false, mediaType: "text/plain", truncated: false, sizeBytes: 0, ...over };
}

function renderFile(path: string) {
  return render(<DocPane params={{ session: "s1", path, kind: "file" }} paneId="doc-1" focused={true} />);
}

test("a text file renders its content inside a <pre>, titled by the filename", async () => {
  mockRead.mockResolvedValue(textContent({ text: "plain body line", sizeBytes: 15 }));
  renderFile("src/notes.txt");
  const body = await screen.findByText("plain body line");
  expect(body.tagName).toBe("PRE");
  expect(screen.getByRole("heading", { name: "notes.txt" })).toBeTruthy();
});

test("a markdown file renders through the sanitizing Markdown widget, not a raw <pre>", async () => {
  mockRead.mockResolvedValue(textContent({ text: "# Heading\n\nbody" }));
  renderFile("docs/README.md");
  // The Markdown widget turns `# Heading` into a real <h1>; a <pre> never would.
  const heading = await screen.findByRole("heading", { name: "Heading" });
  expect(heading.tagName).toBe("H1");
});

test("a markdown file with raw HTML in the source has it neutralized (sanitizer posture, beyond legacy)", async () => {
  mockRead.mockResolvedValue(textContent({ text: "before <img src=x onerror=alert(1)> after" }));
  const { container } = renderFile("docs/EVIL.md");
  await screen.findByText(/before/);
  // The raw <img> must not survive as a live element - marked's html() override
  // escapes it to text and DOMPurify drops it either way.
  expect(container.querySelector("img")).toBeNull();
});

test("a binary file shows a not-shown notice with the filename and human size", async () => {
  mockRead.mockResolvedValue(textContent({ binary: true, mediaType: "application/octet-stream", sizeBytes: 2048 }));
  renderFile("out/blob.bin");
  expect(await screen.findByText("Binary file not shown")).toBeTruthy();
  // filename + human size in one hint; the pane title also shows the filename,
  // so match the combined hint string to stay unambiguous.
  expect(screen.getByText(/blob\.bin \(2 KiB\)/)).toBeTruthy();
});

test("a truncated file shows the exact truncation notice (first cap of true total) AND the partial content", async () => {
  mockRead.mockResolvedValue(
    textContent({ text: "partial head", truncated: true, sizeBytes: DOC_FILE_MAX_BYTES, totalBytes: 2 * 1024 * 1024 }),
  );
  renderFile("logs/huge.log");
  expect(await screen.findByText("partial head")).toBeTruthy();
  expect(screen.getByText("Truncated")).toBeTruthy(); // the attention chip
  // The notice is exact now: the cap shown, and the file's true total.
  expect(screen.getByText("Showing the first 512 KiB of 2 MiB.")).toBeTruthy();
});

test("an untruncated file shows no truncation notice", async () => {
  mockRead.mockResolvedValue(textContent({ text: "small", sizeBytes: 5 }));
  renderFile("small.txt");
  await screen.findByText("small");
  expect(screen.queryByText(/truncated/i)).toBeNull();
});

test("a 404 (missing file / unknown session) shows the not-found empty state", async () => {
  mockRead.mockRejectedValue(new DocFileError("not-found", 404));
  renderFile("gone.txt");
  expect(await screen.findByText("File not available")).toBeTruthy();
});

test("a 403 (path escapes the cwd) shows the access-denied empty state", async () => {
  mockRead.mockRejectedValue(new DocFileError("forbidden", 403));
  renderFile("../secret");
  expect(await screen.findByText("Access denied")).toBeTruthy();
});

test("any other failure shows a generic couldn't-load empty state", async () => {
  mockRead.mockRejectedValue(new DocFileError("error", 500));
  renderFile("x.txt");
  expect(await screen.findByText("Couldn't load file")).toBeTruthy();
});

test("shows a loading placeholder while the fetch is in flight", () => {
  mockRead.mockReturnValue(new Promise<DocFileContent>(() => {})); // never resolves
  renderFile("slow.txt");
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

test("an image pane renders an <img> at the /doc/image URL and never fetches raw file bytes", () => {
  render(<DocPane params={{ session: "s1", path: "out/pic.png", kind: "image" }} paneId="doc-2" focused={true} />);
  const img = screen.getByTestId("doc-image") as HTMLImageElement;
  expect(img.getAttribute("src")).toBe(docImageURL("s1", "out/pic.png"));
  expect(img.getAttribute("alt")).toBe("pic.png");
  expect(mockRead).not.toHaveBeenCalled();
});

test("the click-to-zoom control announces its action, not the image filename", () => {
  render(<DocPane params={{ session: "s1", path: "out/pic.png", kind: "image" }} paneId="doc-zoom" focused={true} />);
  // Without an explicit label the button's accessible name falls through to the
  // nested <img alt> (the filename), which reads as a duplicate of the pane
  // title rather than an action. The control announces what it DOES instead.
  expect(screen.getByRole("button", { name: "Zoom image" })).toBeTruthy();
  // The image itself keeps its filename alt for the picture it shows.
  expect((screen.getByTestId("doc-image") as HTMLImageElement).getAttribute("alt")).toBe("pic.png");
});

test("clicking an image opens a full-size lightbox dialog", async () => {
  const user = userEvent.setup();
  render(<DocPane params={{ session: "s1", path: "out/pic.png", kind: "image" }} paneId="doc-3" focused={true} />);
  expect(screen.queryByRole("dialog")).toBeNull();
  await user.click(screen.getByTestId("doc-image"));
  const dialog = screen.getByRole("dialog");
  expect(dialog).toBeTruthy();
  const lightbox = screen.getByTestId("doc-lightbox-img") as HTMLImageElement;
  expect(lightbox.getAttribute("src")).toBe(docImageURL("s1", "out/pic.png"));
});

test("an image that fails to load shows an unavailable notice instead of a broken image", () => {
  render(<DocPane params={{ session: "s1", path: "out/missing.png", kind: "image" }} paneId="doc-4" focused={true} />);
  fireEvent.error(screen.getByTestId("doc-image"));
  expect(screen.getByText("Image not available")).toBeTruthy();
});

// --- kata 9br8: "Back to parent" — same no-way-back gap as the subagent
// transcript pane (kata 0pzz), same fix shape. Unlike the transcript pane's
// optional parentRef, DocParams.session is ALREADY the parent session ref
// and is never optional (openDocBeside's only producer, FileOpenBesideButton,
// always has a real sessionRef) - so the back action renders unconditionally,
// for both file and image panes. ---------------------------------------------

test("shows a 'Back to <parent name>' action naming the live session, for a file pane", async () => {
  mockRead.mockResolvedValue(textContent({ text: "body" }));
  threadsStore.setState((s) => {
    const threads = new Map(s.threads);
    threads.set("s1", { ref: "s1", name: "fix the flaky test", turns: [] } as unknown as ThreadModel);
    return { ...s, threads };
  });
  renderFile("notes.txt");
  expect(await screen.findByRole("button", { name: /back to fix the flaky test/i })).toBeTruthy();
});

test("falls back to the raw session ref when no cached name is available yet", async () => {
  mockRead.mockResolvedValue(textContent({ text: "body" }));
  renderFile("notes.txt");
  expect(await screen.findByRole("button", { name: /back to s1/i })).toBeTruthy();
});

test("shows the back action on an image pane too", () => {
  render(<DocPane params={{ session: "s1", path: "out/pic.png", kind: "image" }} paneId="doc-back" focused={true} />);
  expect(screen.getByRole("button", { name: /back to s1/i })).toBeTruthy();
});

test("clicking 'Back to parent' focuses (or reopens) the parent session pane", async () => {
  mockRead.mockResolvedValue(textContent({ text: "body" }));
  renderFile("notes.txt");

  const back = await screen.findByRole("button", { name: /back to/i });
  fireEvent.click(back);

  const panes = workspaceStore.getState().panes;
  const parentPane = panes.find((p) => p.type === "session");
  expect(parentPane?.params).toEqual({ ref: "s1" });
  expect(workspaceStore.getState().focusedPaneId).toBe(parentPane?.id);
});

test("clicking 'Back to parent' re-focuses an ALREADY-OPEN parent pane rather than opening a duplicate", async () => {
  mockRead.mockResolvedValue(textContent({ text: "body" }));
  const existingId = workspaceStore.getState().openPane("session", { ref: "s1" });
  // Focus something else first (the doc pane itself), so clicking Back has
  // to move focus back.
  workspaceStore.getState().openPane("doc", { session: "s1", path: "notes.txt", kind: "file" });

  renderFile("notes.txt");
  const back = await screen.findByRole("button", { name: /back to/i });
  fireEvent.click(back);

  const panes = workspaceStore.getState().panes;
  expect(panes.filter((p) => p.type === "session")).toHaveLength(1);
  expect(workspaceStore.getState().focusedPaneId).toBe(existingId);
});
