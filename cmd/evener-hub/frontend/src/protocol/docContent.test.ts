// @vitest-environment node
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  DOC_FILE_MAX_BYTES,
  type DocFetch,
  DocFileError,
  type DocPort,
  docFileRawURL,
  docImageURL,
  readDocFile,
} from "./docContent";

// The same-origin base the browser adapter supplies: the web has a cookie jar
// and serves /doc itself, so its URLs stay the bare paths they have always been.
const SAME_ORIGIN = "";
const HUB = "https://hub.test";

afterEach(() => {
  vi.restoreAllMocks();
});

test("docImageURL builds the /doc/image href with escaped session and path", () => {
  expect(docImageURL(SAME_ORIGIN, "sess_1", "out/pic.png")).toBe("/doc/image?session=sess_1&path=out%2Fpic.png");
});

test("docImageURL hangs the /doc/image href off an absolute base origin", () => {
  // What the native adapter supplies: native has no page origin to be relative
  // to, so every doc URL names the hub it came from.
  expect(docImageURL(HUB, "sess_1", "out/pic.png")).toBe(
    "https://hub.test/doc/image?session=sess_1&path=out%2Fpic.png",
  );
});

test("a trailing slash on the base origin does not double up the /doc separator", () => {
  // Hub origins are typed by hand on native, so "https://hub.test/" reaches
  // these builders as readily as "https://hub.test".
  expect(docImageURL("https://hub.test/", "s1", "p.png")).toBe("https://hub.test/doc/image?session=s1&path=p.png");
  expect(docFileRawURL("https://hub.test/", "s1", "p.md")).toBe(
    "https://hub.test/doc/file?format=raw&session=s1&path=p.md",
  );
});

test("docImageURL escapes query-hostile characters in both values", () => {
  // Matches the Go handler's url.QueryEscape of sessionID and rel - a bare
  // '&', space, or '/' in either would otherwise corrupt the query string.
  expect(docImageURL(SAME_ORIGIN, "a b&c", "dir/one two.png")).toBe(
    "/doc/image?session=a%20b%26c&path=dir%2Fone%20two.png",
  );
});

test("docFileRawURL builds the raw variant with format=raw and both values escaped", () => {
  // Mirrors handleDocFile's ?format=raw branch (cmd/evener-hub/doc_serve.go:75)
  // and its url query params, escaped exactly as the image href is.
  expect(docFileRawURL(SAME_ORIGIN, "a b&c", "dir/one two.md")).toBe(
    "/doc/file?format=raw&session=a%20b%26c&path=dir%2Fone%20two.md",
  );
});

function headers(contentType: string): Record<string, string> {
  return { "Content-Type": contentType };
}

// A DocPort that records what its fetch was asked for and answers with a
// canned response. A real fetch Response satisfies DocResponseLike
// structurally, so the fake is the real shape both adapters hand back.
function recordingPort(origin: string, response: Response): { port: DocPort; urls: string[] } {
  const urls: string[] = [];
  const fetch: DocFetch = async (url) => {
    urls.push(url);
    return response;
  };
  return { urls, port: { origin, fetch } };
}

function respondWith(response: Response): DocPort {
  return recordingPort(SAME_ORIGIN, response).port;
}

describe("readDocFile", () => {
  test("asks the injected fetch for the raw variant and never touches the global fetch", async () => {
    // The host supplies the port, so the package carries no browser global and
    // no credentials policy; the web's browserDocPort adapter owns both.
    const global = vi.spyOn(globalThis, "fetch");
    const doc = recordingPort(SAME_ORIGIN, new Response("x", { headers: headers("text/plain; charset=utf-8") }));
    await readDocFile("s1", "notes.txt", doc.port);
    expect(doc.urls).toEqual(["/doc/file?format=raw&session=s1&path=notes.txt"]);
    expect(global).not.toHaveBeenCalled();
  });

  test("composes the request against the port's base origin, not a bare same-origin path", async () => {
    // The origin travels with the port rather than as a parameter every caller
    // would have to remember, which is what keeps origin policy out of panes.
    const doc = recordingPort(HUB, new Response("x", { headers: headers("text/plain; charset=utf-8") }));
    await readDocFile("s1", "dir/notes.txt", doc.port);
    expect(doc.urls).toEqual(["https://hub.test/doc/file?format=raw&session=s1&path=dir%2Fnotes.txt"]);
  });

  test("a text file yields decoded text, not binary, with the charset stripped off the mediaType", async () => {
    const port = respondWith(new Response("hello world", { headers: headers("text/plain; charset=utf-8") }));
    expect(await readDocFile("s1", "notes.txt", port)).toEqual({
      text: "hello world",
      binary: false,
      mediaType: "text/plain",
      truncated: false,
      sizeBytes: 11,
    });
  });

  test("markdown source is returned verbatim as text - mode selection is the pane's job, not the data layer's", async () => {
    const port = respondWith(new Response("# Title\n\nBody", { headers: headers("text/plain; charset=utf-8") }));
    const doc = await readDocFile("s1", "README.md", port);
    expect(doc.text).toBe("# Title\n\nBody");
    expect(doc.binary).toBe(false);
  });

  test("an octet-stream response is binary with empty text and the octet-stream mediaType", async () => {
    const body = new Uint8Array([0x00, 0x01, 0x02, 0x03]);
    const port = respondWith(new Response(body, { headers: headers("application/octet-stream") }));
    expect(await readDocFile("s1", "blob.bin", port)).toEqual({
      text: "",
      binary: true,
      mediaType: "application/octet-stream",
      truncated: false,
      sizeBytes: 4,
    });
  });

  test("truncation is read from the X-Doc-Truncated header, with the true total from X-Doc-Total-Size", async () => {
    const body = "a".repeat(DOC_FILE_MAX_BYTES);
    const port = respondWith(
      new Response(body, {
        headers: {
          "Content-Type": "text/plain; charset=utf-8",
          "X-Doc-Truncated": "true",
          "X-Doc-Total-Size": "2097152",
        },
      }),
    );
    const doc = await readDocFile("s1", "big.log", port);
    expect(doc.truncated).toBe(true);
    expect(doc.sizeBytes).toBe(DOC_FILE_MAX_BYTES); // received (head) bytes
    expect(doc.totalBytes).toBe(2097152); // the file's true size, from the header
  });

  test("no X-Doc-Truncated header means not truncated - even for a body exactly at the cap (no false positive)", async () => {
    // A file of exactly the cap serves its whole self and sends no truncation
    // header; the old body>=cap derivation wrongly flagged this boundary.
    const body = "a".repeat(DOC_FILE_MAX_BYTES);
    const port = respondWith(new Response(body, { headers: headers("text/plain; charset=utf-8") }));
    const doc = await readDocFile("s1", "exact.log", port);
    expect(doc.truncated).toBe(false);
    expect(doc.totalBytes).toBeUndefined();
    expect(doc.sizeBytes).toBe(DOC_FILE_MAX_BYTES);
  });

  test("a 403 (path escapes the session cwd) rejects with a forbidden DocFileError", async () => {
    const port = respondWith(new Response("forbidden", { status: 403 }));
    await expect(readDocFile("s1", "../etc/passwd", port)).rejects.toMatchObject({
      kind: "forbidden",
      status: 403,
    });
  });

  test("a 404 (missing file / unknown or non-local session) rejects with a not-found DocFileError", async () => {
    const port = respondWith(new Response("not found", { status: 404 }));
    await expect(readDocFile("s1", "gone.txt", port)).rejects.toMatchObject({ kind: "not-found", status: 404 });
  });

  test("any other non-ok status rejects with a generic error DocFileError carrying the status", async () => {
    const port = respondWith(new Response("boom", { status: 500 }));
    await expect(readDocFile("s1", "x.txt", port)).rejects.toMatchObject({ kind: "error", status: 500 });
  });

  test("the rejection is a DocFileError instance so the pane can switch on kind", async () => {
    const port = respondWith(new Response(null, { status: 404 }));
    await expect(readDocFile("s1", "gone.txt", port)).rejects.toBeInstanceOf(DocFileError);
  });
});
