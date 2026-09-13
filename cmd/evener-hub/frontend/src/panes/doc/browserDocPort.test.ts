import { afterEach, expect, test, vi } from "vitest";
import { docImageURL } from "../../protocol/docContent";
import { browserDocPort } from "./browserDocPort";

afterEach(() => {
  vi.restoreAllMocks();
});

test("fetches the given URL with the auth cookie (same-origin credentials)", async () => {
  const spy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("x"));
  await browserDocPort.fetch("/doc/file?format=raw&session=s1&path=notes.txt");
  expect(spy).toHaveBeenCalledWith("/doc/file?format=raw&session=s1&path=notes.txt", {
    credentials: "same-origin",
  });
});

test("its base origin leaves doc URLs as the same-origin paths the hub serves", () => {
  // The page comes from the hub, so a bare path is already authenticated and
  // correct; anything absolute here would be a behavior change, not a move.
  expect(browserDocPort.origin).toBe("");
  expect(docImageURL(browserDocPort.origin, "s1", "out/pic.png")).toBe("/doc/image?session=s1&path=out%2Fpic.png");
});
