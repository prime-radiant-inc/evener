// @vitest-environment node
import { expect, test } from "vitest";
import { marketplaceSourceLabel } from "./marketplaceSourceLabel";

test("github: shows the repo prefixed with 'github: '", () => {
  expect(marketplaceSourceLabel({ kind: "github", repo: "acme/plugins" })).toBe("github: acme/plugins");
});

test("url: shows the raw url", () => {
  expect(marketplaceSourceLabel({ kind: "url", url: "https://example.com/x.git" })).toBe("https://example.com/x.git");
});

test("directory: shows the raw path", () => {
  expect(marketplaceSourceLabel({ kind: "directory", path: "/opt/plugins-src" })).toBe("/opt/plugins-src");
});

test("git-subdir: shows the url followed by the path in parens", () => {
  expect(marketplaceSourceLabel({ kind: "git-subdir", url: "https://example.com/x.git", path: "sub/dir" })).toBe(
    "https://example.com/x.git (sub/dir)",
  );
});

test("a missing optional field renders as empty rather than 'undefined'", () => {
  expect(marketplaceSourceLabel({ kind: "github" })).toBe("github: ");
  expect(marketplaceSourceLabel({ kind: "git-subdir", url: "https://example.com/x.git" })).toBe(
    "https://example.com/x.git ()",
  );
  expect(marketplaceSourceLabel({ kind: "url" })).toBe("");
  expect(marketplaceSourceLabel({ kind: "directory" })).toBe("");
});

test("an unknown kind falls back to the raw kind string", () => {
  expect(marketplaceSourceLabel({ kind: "mystery" })).toBe("mystery");
});
