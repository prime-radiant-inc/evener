import { expect, test } from "vitest";
import { comicSessionURL, parseComicRoute } from "./route";

test("round-trips the complete ref and rejects malformed encoding", () => {
  expect(comicSessionURL("remote:host/session")).toBe("/comic/s/remote%3Ahost%2Fsession");
  expect(parseComicRoute(comicSessionURL("remote:host/session"))).toEqual({
    kind: "session",
    ref: "remote:host/session",
  });
  expect(parseComicRoute("/comic/s/%ZZ")).toEqual({ kind: "invalid" });
  expect(parseComicRoute("/s/local%3Asession")).toBeNull();
});

test("recognizes only complete comic routes", () => {
  expect(parseComicRoute("/comic")).toEqual({ kind: "picker" });
  expect(parseComicRoute("/comic/s/local%3Afirst%20session")).toEqual({ kind: "session", ref: "local:first session" });
  expect(parseComicRoute("/comic/s/")).toEqual({ kind: "invalid" });
  expect(parseComicRoute("/comic/unknown")).toEqual({ kind: "invalid" });
  expect(parseComicRoute("/comics")).toBeNull();
});
