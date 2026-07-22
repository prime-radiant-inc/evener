import { expect, test } from "vitest";
import { docImageURL, readDocFile } from "./docContent";

test("docImageURL builds the /doc/image href with escaped session and path", () => {
  expect(docImageURL("sess_1", "out/pic.png")).toBe("/doc/image?session=sess_1&path=out%2Fpic.png");
});

test("docImageURL escapes query-hostile characters in both values", () => {
  // Matches the Go handler's url.QueryEscape of sessionID and rel - a bare
  // '&', space, or '/' in either would otherwise corrupt the query string.
  expect(docImageURL("a b&c", "dir/one two.png")).toBe("/doc/image?session=a%20b%26c&path=dir%2Fone%20two.png");
});

test("readDocFile rejects until T5 fills it against the raw endpoint", async () => {
  await expect(readDocFile("sess_1", "README.md")).rejects.toThrow(/not implemented until wave 8 T5/);
});
