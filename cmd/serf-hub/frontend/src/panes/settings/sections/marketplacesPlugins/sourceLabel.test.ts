import { expect, test } from "vitest";
import { sourceLabel } from "./sourceLabel";

test("github: shows the repo prefixed with 'github: '", () => {
  expect(sourceLabel({ kind: "github", repo: "acme/plugins" })).toBe("github: acme/plugins");
});

test("url: shows the raw url", () => {
  expect(sourceLabel({ kind: "url", url: "https://example.com/x.git" })).toBe("https://example.com/x.git");
});

test("directory: shows the raw path", () => {
  expect(sourceLabel({ kind: "directory", path: "/opt/plugins-src" })).toBe("/opt/plugins-src");
});

test("git-subdir: shows the url followed by the path in parens", () => {
  expect(sourceLabel({ kind: "git-subdir", url: "https://example.com/x.git", path: "sub/dir" })).toBe(
    "https://example.com/x.git (sub/dir)",
  );
});

test("an unknown kind falls back to the raw kind string", () => {
  expect(sourceLabel({ kind: "mystery" })).toBe("mystery");
});
