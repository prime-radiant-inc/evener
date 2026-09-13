import { afterEach, expect, it, vi } from "vitest";
import { readDocFile } from "../../cmd/evener-hub/frontend/src/protocol/docContent";
import { nativeDocImageSource, nativeDocPort } from "./nativeDocPort";

afterEach(() => {
  vi.restoreAllMocks();
});

it("reads doc files from the hub's own origin with the bearer token attached", async () => {
  // Native has no page origin and no cookie jar: the port carries both halves,
  // so readDocFile composes an absolute hub URL and the request is authorized.
  const fetch = vi
    .spyOn(globalThis, "fetch")
    .mockResolvedValue(
      new Response("# notes", { headers: { "Content-Type": "text/plain; charset=utf-8" } }),
    );
  const content = await readDocFile(
    "s1",
    "docs/notes.md",
    nativeDocPort("https://hub.test", "secret"),
  );
  expect(fetch).toHaveBeenCalledWith(
    "https://hub.test/doc/file?format=raw&session=s1&path=docs%2Fnotes.md",
    { headers: { Authorization: "Bearer secret" } },
  );
  expect(content.text).toBe("# notes");
});

it("sends no Authorization header for a hub that needs no token", async () => {
  const fetch = vi
    .spyOn(globalThis, "fetch")
    .mockResolvedValue(new Response("x", { headers: { "Content-Type": "text/plain" } }));
  await readDocFile("s1", "notes.txt", nativeDocPort("https://hub.test", ""));
  expect(fetch).toHaveBeenCalledWith("https://hub.test/doc/file?format=raw&session=s1&path=notes.txt", {
    headers: {},
  });
});

it("builds an image source that names the hub URL and carries the same credentials", () => {
  // An <Image> never goes through readDocFile, so the doc image needs the
  // bearer header on the source itself - the same header the port's fetch uses.
  expect(nativeDocImageSource("https://hub.test", "secret", "s1", "out/pic.png")).toEqual({
    uri: "https://hub.test/doc/image?session=s1&path=out%2Fpic.png",
    headers: { Authorization: "Bearer secret" },
  });
  expect(nativeDocImageSource("https://hub.test", "", "s1", "out/pic.png")).toEqual({
    uri: "https://hub.test/doc/image?session=s1&path=out%2Fpic.png",
    headers: {},
  });
});
