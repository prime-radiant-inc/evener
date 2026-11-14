import { Marked } from "marked";
import { describe, expect, it } from "vitest";
import { markdownLexer } from "./lexer";
import { gateStats, resetGateStatsForTests } from "./setextGate";

// Issue #5002: Marked's pinned `lheading` (setext) block rule uses a lazy
// multi-line capture that must reach the END of the frame to prove no
// underline follows. Inside a list item the lexer emits one text token per
// continuation line, probing the rule once per line, so a
// many-thousand-line item is quadratic. The gate precomputes, once per
// frame, every line that could possibly serve as a setext underline and
// skips the expensive probe whenever none lies at or after the probe -
// never suppressing a probe that could match.

// The unmodified lexer: the gate's behavioral reference. Token output must
// be identical, so every corpus case is also what the perf shape must keep
// producing.
const UNGATED = new Marked({ gfm: true });

function expectSameTokens(source: string): void {
  expect(markdownLexer.lexer(source)).toEqual(UNGATED.lexer(source));
}

describe("setext gate", () => {
  it("lexes a sixteen-thousand-line list item without the per-line setext rescan", () => {
    resetGateStatsForTests();
    const source = "- head\n" + "plain continuation line\n".repeat(16_000);
    const startedAt = performance.now();
    const tokens = markdownLexer.lexer(source);
    const elapsedMs = performance.now() - startedAt;
    // One list token wrapping the item. Ungated, the same lex takes ~8 s on
    // this class of host (the per-line lheading probes rescan the remaining
    // frame); gated, the lheading rule never runs in this shape at all and
    // what remains is Marked's own linear-ish per-line loop machinery.
    expect(tokens).toHaveLength(1);
    expect(tokens[0]?.type).toBe("list");
    expect(gateStats.pinnedProbes).toBe(0);
    expect(gateStats.skippedProbes).toBeGreaterThan(16_000);
    expect(elapsedMs).toBeLessThan(5000);
  });

  it("skips probes past the last possible underline but still runs the pinned rule at one", () => {
    resetGateStatsForTests();
    // A top-level setext heading: the underline is a candidate from the
    // first probe, so the pinned rule runs and produces the heading.
    const heading = markdownLexer.lexer("Title\n====\n");
    expect(heading[0]).toMatchObject({ type: "heading", depth: 1, text: "Title" });
    expect(gateStats.pinnedProbes).toBeGreaterThan(0);

    // Inside a list item, "head\n===" is a setext heading; the underline
    // candidate lies BEHIND every later continuation line's probe, so those
    // probes are skipped while the first one still runs the pinned rule.
    resetGateStatsForTests();
    const source = "- head\n===\n" + "plain continuation line\n".repeat(50);
    const tokens = markdownLexer.lexer(source);
    expect(tokens[0]?.type).toBe("list");
    expect(JSON.stringify(tokens)).toContain('"depth":1');
    expect(gateStats.pinnedProbes).toBeGreaterThan(0);
    expect(gateStats.skippedProbes).toBeGreaterThan(40);
    // And the gated output still equals the ungated lexer's.
    expect(tokens).toEqual(new Marked({ gfm: true }).lexer(source));
  });

  it("keeps token output identical to ungated Marked across setext-adjacent shapes", () => {
    const corpus: string[] = [
      // Plain setext headings at top level.
      "Title\n====\n",
      "Sub\n---\n",
      // Setext headings inside list items - the gated frame.
      "- item\nTitle\n====\n- second item\n",
      "- head\nplain continuation\n",
      // Long item with a setext heading deep inside: the candidate scan
      // finds it and the pinned rule still runs for the probes that reach.
      "- head\n" + "plain continuation line\n".repeat(500) + "Late title\n====\n",
      // The lazy-continuation blockquote shape from the issue.
      "> quoted\nlazy\n> quoted\nlazy\n",
      // Paragraphs end before a candidate beyond them - and a candidate
      // before the probe position must not read as reachable.
      "plain\n\nafter blank\n",
      "====\nnot an underline at frame start\n",
      "text\n= mixed - not an underline\nplain\n",
      "text\n===\n- list after the heading\n",
      "text with a - dash and an = equals inline\nplain continuation\n",
      "para\n  == indented too far\nplain\n",
      "para\n\t== tabbed underline\nplain\n",
      "  Title\n  ====\n",
      "Title\n====   \ntrailing spaces then end\n",
      // Dense candidates: a rule-of-dashes list separator document.
      Array.from({ length: 200 }, (_, i) => `row ${i}\n---\ntail ${i}`).join("\n"),
    ];
    for (const source of corpus) {
      expectSameTokens(source);
    }
  });
});
