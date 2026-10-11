// @vitest-environment node
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  cwdRelative,
  DOC_FILE_MAX_BYTES,
  type DocFetch,
  DocFileError,
  type DocPort,
  docFileRawURL,
  docImageReadURL,
  docImageURL,
  filenameOf,
  fileURLToPath,
  isImagePath,
  isMarkdownPath,
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

test("docImageReadURL appends an escaped read generation to the ordinary image URL", () => {
  expect(docImageReadURL(HUB, "a b&c", "dir/one two.png", "viewer seed/2")).toBe(
    "https://hub.test/doc/image?session=a%20b%26c&path=dir%2Fone%20two.png&read=viewer%20seed%2F2",
  );
});

test("adding read generations does not change ordinary transcript-image preview URLs", () => {
  const ordinary = docImageURL(SAME_ORIGIN, "sess_1", "out/pic.png");
  expect(docImageReadURL(SAME_ORIGIN, "sess_1", "out/pic.png", "first")).toBe(`${ordinary}&read=first`);
  expect(docImageReadURL(SAME_ORIGIN, "sess_1", "out/pic.png", "second")).toBe(`${ordinary}&read=second`);
  expect(docImageURL(SAME_ORIGIN, "sess_1", "out/pic.png")).toBe("/doc/image?session=sess_1&path=out%2Fpic.png");
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

  test("the revision comes from the ETag and the modification time from X-Doc-Modified-At (S9)", async () => {
    const port = respondWith(
      new Response("# Plan", {
        headers: {
          "Content-Type": "text/plain; charset=utf-8",
          ETag: '"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"',
          "X-Doc-Modified-At": "1790000000123",
        },
      }),
    );
    const doc = await readDocFile("s1", "plan.md", port);
    expect(doc.revision).toBe("9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08");
    expect(doc.modifiedAt).toBe(1790000000123);
  });

  test("only a strong sha256 ETag names a revision; any other tag is no information", async () => {
    // The hub sends the whole file's sha256 as a strong tag. A weak tag, or one
    // that is not a sha256, could name a version the file does not have, so
    // the caller falls back to comparing what it was shown.
    const sha = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";
    for (const etag of [`W/"${sha}"`, '"abc123"', sha, `"${sha.toUpperCase()}"`]) {
      const port = respondWith(
        new Response("x", { headers: { "Content-Type": "text/plain; charset=utf-8", ETag: etag } }),
      );
      expect("revision" in (await readDocFile("s1", "x.txt", port))).toBe(false);
    }
  });

  test("a hub without S9 names no revision and no time", async () => {
    // Absent keys, not empty ones: the phone's fallback keys on absence.
    const port = respondWith(new Response("x", { headers: headers("text/plain; charset=utf-8") }));
    const doc = await readDocFile("s1", "x.txt", port);
    expect("revision" in doc).toBe(false);
    expect("modifiedAt" in doc).toBe(false);
  });

  test("the revision and the time are read independently", async () => {
    // A file past the hash limit has a time and no ETag; each key stands alone.
    const sha = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";
    const timeOnly = await readDocFile(
      "s1",
      "x.txt",
      respondWith(
        new Response("x", {
          headers: { "Content-Type": "text/plain; charset=utf-8", "X-Doc-Modified-At": "1790000000123" },
        }),
      ),
    );
    expect("revision" in timeOnly).toBe(false);
    expect(timeOnly.modifiedAt).toBe(1790000000123);
    const revisionOnly = await readDocFile(
      "s1",
      "x.txt",
      respondWith(new Response("x", { headers: { "Content-Type": "text/plain; charset=utf-8", ETag: `"${sha}"` } })),
    );
    expect(revisionOnly.revision).toBe(sha);
    expect("modifiedAt" in revisionOnly).toBe(false);
  });

  test("a malformed ETag or modification time is no information", async () => {
    const port = respondWith(
      new Response("x", {
        headers: { "Content-Type": "text/plain; charset=utf-8", ETag: '""', "X-Doc-Modified-At": "yesterday" },
      }),
    );
    const doc = await readDocFile("s1", "x.txt", port);
    expect("revision" in doc).toBe(false);
    expect("modifiedAt" in doc).toBe(false);
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

  test("a 501 (the session's host predates S7 document reads) rejects with a host-unsupported DocFileError", async () => {
    const port = respondWith(new Response("remote document unavailable", { status: 501 }));
    await expect(readDocFile("h1:s1", "plan.md", port)).rejects.toMatchObject({
      kind: "host-unsupported",
      status: 501,
    });
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

describe("filenameOf", () => {
  test("returns the last path segment, the whole name at the top level, and the raw path with no segment", () => {
    expect(filenameOf("src/panes/doc/DocPane.tsx")).toBe("DocPane.tsx");
    expect(filenameOf("README.md")).toBe("README.md");
    expect(filenameOf("")).toBe("");
  });
});

describe("isMarkdownPath", () => {
  test.each(["README.md", "notes.MARKDOWN", "a/b/Guide.Md", "x.markdown"])("treats %s as markdown", (path) => {
    expect(isMarkdownPath(path)).toBe(true);
  });

  test.each(["notes.txt", "script.ts", "a.md.txt", "mdfile", "Makefile"])("does not treat %s as markdown", (path) => {
    expect(isMarkdownPath(path)).toBe(false);
  });
});

describe("isImagePath", () => {
  test.each(["out/shot.png", "a/b.JPEG", "c.jpg", "d.gif", "e.WebP"])(
    "treats %s as an image /doc/image serves",
    (path) => {
      expect(isImagePath(path)).toBe(true);
    },
  );

  test.each(["logo.svg", "notes.md", "png", "a.png.txt"])("does not treat %s as one", (path) => {
    expect(isImagePath(path)).toBe(false);
  });
});

describe("fileURLToPath", () => {
  test("decodes a session link's file URL into the path a document read takes", () => {
    expect(fileURLToPath("file:///tmp/with%20space.md")).toBe("/tmp/with space.md");
    expect(fileURLToPath("file://localhost/home/jesse/plan.md")).toBe("/home/jesse/plan.md");
    expect(fileURLToPath("file:///home/jesse/plan.md?x=1#top")).toBe("/home/jesse/plan.md");
  });

  test("names no path for a malformed escape, another machine, or something that isn't a file URL", () => {
    // A malformed escape could name a different file than the entry means;
    // canonical file URLs always encode "%", so this only refuses input this
    // system never produced.
    expect(fileURLToPath("file:///tmp/bad%zz.md")).toBe("");
    expect(fileURLToPath("file://server/share/plan.md")).toBe("");
    expect(fileURLToPath("https://example.test/plan.md")).toBe("");
    expect(fileURLToPath("not a url")).toBe("");
  });
});

describe("cwdRelative", () => {
  test("relativizes an absolute path inside the session's folder, with or without a trailing slash", () => {
    expect(cwdRelative("/home/proj/src/a.ts", "/home/proj")).toBe("src/a.ts");
    expect(cwdRelative("/home/proj/src/a.ts", "/home/proj/")).toBe("src/a.ts");
  });

  test("names nothing outside the folder, for the folder itself, or for empty input", () => {
    expect(cwdRelative("/etc/passwd", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/project-other/a.ts", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/proj", "/home/proj")).toBeUndefined();
    expect(cwdRelative("", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/proj/a.ts", "")).toBeUndefined();
  });

  test("names nothing for an absolute path that climbs out, or for the folder itself with a trailing slash", () => {
    expect(cwdRelative("/home/proj/../secret.ts", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/proj/src/../../secret.ts", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/proj/", "/home/proj")).toBeUndefined();
    expect(cwdRelative("/home/proj/", "/home/proj/")).toBeUndefined();
    expect(cwdRelative("/home/proj/src/..hidden.ts", "/home/proj")).toBe("src/..hidden.ts");
  });

  test("takes a relative path as relative unless it climbs out", () => {
    expect(cwdRelative("src/a.ts", "/home/proj")).toBe("src/a.ts");
    expect(cwdRelative("a.ts", "/home/proj")).toBe("a.ts");
    expect(cwdRelative("../secret.ts", "/home/proj")).toBeUndefined();
    expect(cwdRelative("src/../../secret.ts", "/home/proj")).toBeUndefined();
  });
});
