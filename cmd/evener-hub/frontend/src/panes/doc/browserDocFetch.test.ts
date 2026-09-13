import { afterEach, expect, test, vi } from "vitest";
import { browserDocFetch } from "./browserDocFetch";

afterEach(() => {
  vi.restoreAllMocks();
});

test("fetches the given URL with the auth cookie (same-origin credentials)", async () => {
  const spy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("x"));
  await browserDocFetch("/doc/file?format=raw&session=s1&path=notes.txt");
  expect(spy).toHaveBeenCalledWith("/doc/file?format=raw&session=s1&path=notes.txt", {
    credentials: "same-origin",
  });
});
