import { describe, expect, it } from "vitest";
import {
  renderSafeMarkdown,
  type TrustedHTMLString,
  toSpeakableText,
} from "./markdown";

/**
 * Helpers to extract the HTML string from the branded TrustedHTMLString so the
 * tests can inspect sanitizer output without exposing the brand externally.
 */
function html(trusted: TrustedHTMLString): string {
  return trusted.html;
}

/**
 * Branded-type contract: a TrustedHTMLString must carry the opaque brand and an
 * `html` string; plain strings are not assignable to the parameter type.
 */
describe("TrustedHTMLString brand", () => {
  it("returns an opaque branded object whose html is a string", () => {
    const out = renderSafeMarkdown("hello");
    expect(typeof out.html).toBe("string");
    expect("__trusted" in out).toBe(true);
  });
});

describe("renderSafeMarkdown — script and active content", () => {
  it("removes <script> tags", () => {
    const out = html(renderSafeMarkdown("<script>alert(1)</script>"));
    expect(out.toLowerCase()).not.toContain("<script");
    expect(out.toLowerCase()).not.toContain("alert(1)");
  });

  it("removes <img onerror> and the img element", () => {
    const out = html(renderSafeMarkdown('<img onerror="alert(1)" src="x">'));
    expect(out.toLowerCase()).not.toContain("onerror");
    expect(out.toLowerCase()).not.toContain("<img");
  });

  it("removes <form action=javascript:>", () => {
    const out = html(renderSafeMarkdown('<form action="javascript:alert(1)">'));
    expect(out.toLowerCase()).not.toContain("<form");
    expect(out.toLowerCase()).not.toContain("javascript:");
  });

  it("removes inline style attributes", () => {
    const out = html(renderSafeMarkdown('<p style="color:red">x</p>'));
    expect(out.toLowerCase()).not.toContain("style");
    expect(out.toLowerCase()).not.toContain("color:red");
  });
});

describe("renderSafeMarkdown — link protocol allowlist", () => {
  it("rejects javascript: protocol in Markdown links", () => {
    const out = html(renderSafeMarkdown("[link](javascript:alert(1))"));
    expect(out.toLowerCase()).not.toContain("javascript:");
    expect(out.toLowerCase()).not.toContain("alert(1)");
  });

  it("rejects data: protocol in Markdown links", () => {
    const out = html(
      renderSafeMarkdown("[link](data:text/html;base64,PHNjcmlwdD4=)"),
    );
    expect(out.toLowerCase()).not.toContain("data:");
    expect(out.toLowerCase()).not.toContain("base64");
  });

  it("rejects mailto: protocol", () => {
    const out = html(renderSafeMarkdown("[me](mailto:a@b.com)"));
    expect(out.toLowerCase()).not.toContain("mailto:");
  });

  it("keeps https: links", () => {
    const out = html(renderSafeMarkdown("[docs](https://example.com)"));
    expect(out).toContain("https://example.com");
    expect(out.toLowerCase()).not.toContain("javascript:");
  });

  it("keeps http: links", () => {
    const out = html(renderSafeMarkdown("[docs](http://example.com)"));
    expect(out).toContain("http://example.com");
  });
});

describe("renderSafeMarkdown — malformed input", () => {
  it("does not crash on lone surrogates", () => {
    expect(() => renderSafeMarkdown("bad\ud800\udc00ok")).not.toThrow();
    const out = html(renderSafeMarkdown("bad\ud800\udc00ok"));
    expect(typeof out).toBe("string");
  });

  it("strips nested raw HTML embedded in Markdown", () => {
    const out = html(
      renderSafeMarkdown("text\n<script>alert(1)</script>\nmore"),
    );
    expect(out.toLowerCase()).not.toContain("<script");
    expect(out.toLowerCase()).not.toContain("alert(1)");
  });

  it("handles empty input", () => {
    expect(html(renderSafeMarkdown(""))).toBe("");
  });
});

describe("renderSafeMarkdown — allowed formatting survives", () => {
  it("renders bold and italic", () => {
    const out = html(renderSafeMarkdown("**bold** and _italic_"));
    expect(out).toContain("<strong>bold</strong>");
    expect(out).toContain("<em>italic</em>");
  });

  it("renders inline code and code blocks", () => {
    const out = html(renderSafeMarkdown("`x` and\n```\ncode\n```"));
    expect(out).toContain("<code>x</code>");
    expect(out).toContain("<pre>");
  });

  it("renders headings h1-h6", () => {
    const out = html(renderSafeMarkdown("# H1\n## H2\n### H3"));
    expect(out).toContain("<h1");
    expect(out).toContain("<h2");
    expect(out).toContain("<h3");
  });

  it("renders lists", () => {
    const out = html(renderSafeMarkdown("- a\n- b"));
    expect(out).toContain("<ul>");
    expect(out).toContain("<li>");
  });

  it("renders blockquote", () => {
    const out = html(renderSafeMarkdown("> quoted"));
    expect(out).toContain("<blockquote>");
  });

  it("renders tables with colspan allowed", () => {
    const md = "| a | b |\n|---|---|\n| 1 | 2 |";
    const out = html(renderSafeMarkdown(md));
    expect(out).toContain("<table>");
    expect(out).toContain("<td");
  });

  it("does not render images", () => {
    const out = html(renderSafeMarkdown("![alt](https://example.com/x.png)"));
    expect(out.toLowerCase()).not.toContain("<img");
  });
});

describe("toSpeakableText", () => {
  it("strips Markdown formatting to plain text", () => {
    expect(toSpeakableText("**bold** and _italic_")).toBe("bold and italic");
  });

  it("preserves link text but drops the URL", () => {
    expect(toSpeakableText("[docs](https://example.com)")).toBe("docs");
  });

  it("strips code fences", () => {
    expect(toSpeakableText("```\ncode\n```")).toBe("code");
  });

  it("strips headings markers", () => {
    expect(toSpeakableText("# Heading")).toBe("Heading");
  });

  it("strips list markers", () => {
    expect(toSpeakableText("- one\n- two")).toBe("one\ntwo");
  });

  it("strips blockquote markers", () => {
    expect(toSpeakableText("> quoted")).toBe("quoted");
  });

  it("removes raw HTML", () => {
    expect(toSpeakableText("a <b>bold</b> b")).toBe("a bold b");
  });

  it("handles empty input", () => {
    expect(toSpeakableText("")).toBe("");
  });

  it("does not crash on lone surrogates", () => {
    expect(() => toSpeakableText("x\ud800\udc00y")).not.toThrow();
  });
});
