// @vitest-environment node
import { afterEach, describe, expect, test, vi } from "vitest";
import { DOC_FILE_MAX_BYTES, DocFileError, docFileRawURL, docImageURL, readDocFile } from "./docContent";

afterEach(() => {
  vi.restoreAllMocks();
});

test("docImageURL builds the /doc/image href with escaped session and path", () => {
  expect(docImageURL("sess_1", "out/pic.png")).toBe("/doc/image?session=sess_1&path=out%2Fpic.png");
});

test("docImageURL escapes query-hostile characters in both values", () => {
  // Matches the Go handler's url.QueryEscape of sessionID and rel - a bare
  // '&', space, or '/' in either would otherwise corrupt the query string.
  expect(docImageURL("a b&c", "dir/one two.png")).toBe("/doc/image?session=a%20b%26c&path=dir%2Fone%20two.png");
});

test("docFileRawURL builds the raw variant with format=raw and both values escaped", () => {
  // Mirrors handleDocFile's ?format=raw branch (cmd/serf-hub/doc_serve.go:75)
  // and its url query params, escaped exactly as the image href is.
  expect(docFileRawURL("a b&c", "dir/one two.md")).toBe(
    "/doc/file?format=raw&session=a%20b%26c&path=dir%2Fone%20two.md",
  );
});

function headers(contentType: string): Record<string, string> {
  return { "Content-Type": contentType };
}

describe("readDocFile", () => {
  test("fetches the raw variant with the auth cookie (same-origin credentials)", async () => {
    const spy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response("x", { headers: headers("text/plain; charset=utf-8") }));
    await readDocFile("s1", "notes.txt");
    expect(spy).toHaveBeenCalledWith("/doc/file?format=raw&session=s1&path=notes.txt", { credentials: "same-origin" });
  });

  test("a text file yields decoded text, not binary, with the charset stripped off the mediaType", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("hello world", { headers: headers("text/plain; charset=utf-8") }),
    );
    expect(await readDocFile("s1", "notes.txt")).toEqual({
      text: "hello world",
      binary: false,
      mediaType: "text/plain",
      truncated: false,
      sizeBytes: 11,
    });
  });

  test("markdown source is returned verbatim as text - mode selection is the pane's job, not the data layer's", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("# Title\n\nBody", { headers: headers("text/plain; charset=utf-8") }),
    );
    const doc = await readDocFile("s1", "README.md");
    expect(doc.text).toBe("# Title\n\nBody");
    expect(doc.binary).toBe(false);
  });

  test("an octet-stream response is binary with empty text and the octet-stream mediaType", async () => {
    const body = new Uint8Array([0x00, 0x01, 0x02, 0x03]);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(body, { headers: headers("application/octet-stream") }),
    );
    expect(await readDocFile("s1", "blob.bin")).toEqual({
      text: "",
      binary: true,
      mediaType: "application/octet-stream",
      truncated: false,
      sizeBytes: 4,
    });
  });

  test("a body that reaches the server cap is reported truncated (the only honest signal the raw endpoint gives)", async () => {
    const body = "a".repeat(DOC_FILE_MAX_BYTES);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(body, { headers: headers("text/plain; charset=utf-8") }),
    );
    const doc = await readDocFile("s1", "big.log");
    expect(doc.truncated).toBe(true);
    expect(doc.sizeBytes).toBe(DOC_FILE_MAX_BYTES);
  });

  test("a body one byte under the cap is NOT truncated - pins the >= boundary, not >", async () => {
    const body = "a".repeat(DOC_FILE_MAX_BYTES - 1);
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(body, { headers: headers("text/plain; charset=utf-8") }),
    );
    const doc = await readDocFile("s1", "big.log");
    expect(doc.truncated).toBe(false);
    expect(doc.sizeBytes).toBe(DOC_FILE_MAX_BYTES - 1);
  });

  test("a 403 (path escapes the session cwd) rejects with a forbidden DocFileError", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("forbidden", { status: 403 }));
    await expect(readDocFile("s1", "../etc/passwd")).rejects.toMatchObject({ kind: "forbidden", status: 403 });
  });

  test("a 404 (missing file / unknown or non-local session) rejects with a not-found DocFileError", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("not found", { status: 404 }));
    await expect(readDocFile("s1", "gone.txt")).rejects.toMatchObject({ kind: "not-found", status: 404 });
  });

  test("any other non-ok status rejects with a generic error DocFileError carrying the status", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("boom", { status: 500 }));
    await expect(readDocFile("s1", "x.txt")).rejects.toMatchObject({ kind: "error", status: 500 });
  });

  test("the rejection is a DocFileError instance so the pane can switch on kind", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 404 }));
    await expect(readDocFile("s1", "gone.txt")).rejects.toBeInstanceOf(DocFileError);
  });
});
