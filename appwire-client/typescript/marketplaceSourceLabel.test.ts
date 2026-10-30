// @vitest-environment node
import { expect, test } from "vitest";
import { marketplaceSourceLabel, marketplaceSourcePin, marketplaceSourceTakesRef } from "./marketplaceSourceLabel";

test("only git-backed kinds take a ref", () => {
  expect(marketplaceSourceTakesRef("github")).toBe(true);
  expect(marketplaceSourceTakesRef("url")).toBe(true);
  expect(marketplaceSourceTakesRef("git-subdir")).toBe(true);
  expect(marketplaceSourceTakesRef("directory")).toBe(false);
  expect(marketplaceSourceTakesRef("mystery")).toBe(false);
});

test("the pin is the sha when the source carries one", () => {
  expect(marketplaceSourcePin({ kind: "github", repo: "acme/plugins", ref: "main" })).toBe("main");
  expect(marketplaceSourcePin({ kind: "github", repo: "acme/plugins", ref: "main", sha: "abc1234" })).toBe("abc1234");
  expect(marketplaceSourcePin({ kind: "github", repo: "acme/plugins" })).toBe("");
  expect(marketplaceSourcePin({ kind: "directory", path: "/srv/plugins", ref: "main" })).toBe("");
});

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

test("a ref pins the label to a branch or tag", () => {
  expect(marketplaceSourceLabel({ kind: "github", repo: "acme/plugins", ref: "release/next" })).toBe(
    "github: acme/plugins@release/next",
  );
  expect(marketplaceSourceLabel({ kind: "url", url: "https://example.com/x.git", ref: "v2" })).toBe(
    "https://example.com/x.git@v2",
  );
  expect(
    marketplaceSourceLabel({ kind: "git-subdir", url: "https://example.com/x.git", path: "sub/dir", ref: "v2" }),
  ).toBe("https://example.com/x.git@v2 (sub/dir)");
});

test("a sha is the pin shown when the source carries both", () => {
  expect(marketplaceSourceLabel({ kind: "github", repo: "acme/plugins", sha: "abc1234" })).toBe(
    "github: acme/plugins@abc1234",
  );
  expect(marketplaceSourceLabel({ kind: "github", repo: "acme/plugins", ref: "main", sha: "abc1234" })).toBe(
    "github: acme/plugins@abc1234",
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
