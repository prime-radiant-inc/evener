import { describe, expect, it } from "vitest";
import { externalMarkdownLink } from "./markdownLinks";

describe("Markdown link destinations", () => {
  it("opens complete web targets without losing path, query or fragment", () => {
    expect(
      externalMarkdownLink("https://example.com/a?q=two%20words#code"),
    ).toBe("https://example.com/a?q=two%20words#code");
    expect(externalMarkdownLink("http://m5.local:9180/session/one")).toBe(
      "http://m5.local:9180/session/one",
    );
  });
  it("keeps host files and unsupported schemes out of the phone's URL launcher", () => {
    for (const target of [
      "/Users/jesse/work.ts:12",
      "src/main.ts",
      "file:///private/data",
      "javascript:alert(1)",
      "evener://send",
      "data:text/html,test",
      "//example.com",
      "https://",
    ]) {
      expect(externalMarkdownLink(target)).toBeNull();
    }
  });
  it("rejects credentials and hidden control characters in displayed targets", () => {
    expect(
      externalMarkdownLink("https://user:password@example.com"),
    ).toBeNull();
    expect(externalMarkdownLink("https://exam\nple.com")).toBeNull();
  });
});
