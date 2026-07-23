import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import {
  DOC_FILE_MAX_BYTES,
  type DocFileContent,
  DocFileError,
  docImageURL,
  readDocFile,
} from "../../protocol/docContent";
import DocPane from "./DocPane";

// Only readDocFile is mocked; docImageURL / DocFileError / DOC_FILE_MAX_BYTES
// stay real so the pane's URL building and error-kind switch run for real.
vi.mock("../../protocol/docContent", async (importActual) => {
  const actual = await importActual<typeof import("../../protocol/docContent")>();
  return { ...actual, readDocFile: vi.fn() };
});

const mockRead = vi.mocked(readDocFile);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
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

test("a truncated file shows a truncation notice AND the partial content (beyond-parity honesty fix)", async () => {
  mockRead.mockResolvedValue(textContent({ text: "partial head", truncated: true, sizeBytes: DOC_FILE_MAX_BYTES }));
  renderFile("logs/huge.log");
  expect(await screen.findByText("partial head")).toBeTruthy();
  expect(screen.getByText("Truncated")).toBeTruthy(); // the attention chip
  expect(screen.getByText(/Showing the first 512 KiB/)).toBeTruthy(); // the explanatory notice
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
