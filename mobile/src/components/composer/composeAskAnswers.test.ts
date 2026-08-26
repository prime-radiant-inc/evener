// Pure-module tests for composeAskAnswers — every exact-output vector from
// AskComposer.test.tsx copied verbatim, plus focused quoting/control-character
// cases. AskComposer.tsx itself only asserts the same composed [answers]
// payloads through the component; this module owns the byte-exact composition
// ported from the Hub's askCompose.ts.

import { describe, expect, it } from "vitest";
import type { AskAnswerItem } from "./composeAskAnswers";
import { composeAskAnswers } from "./composeAskAnswers";

describe("composeAskAnswers — single select", () => {
  it("composes a single selected option", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["Yes"] },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe('[answers]\n1. [Deploy?] → "Yes"');
  });

  it("switching selection replaces the prior choice", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["No"] },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe('[answers]\n1. [Deploy?] → "No"');
  });
});

describe("composeAskAnswers — multi select", () => {
  it("composes multiple selected options joined by comma", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["A", "C"] },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe('[answers]\n1. [Deploy?] → "A", "C"');
  });

  it("composes a single remaining option after deselect", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["B"] },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe('[answers]\n1. [Deploy?] → "B"');
  });
});

describe("composeAskAnswers — free text", () => {
  it("composes a free-text resolution", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Notes",
        resolution: { kind: "free", text: "ship it Friday" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Notes] → free text: "ship it Friday"',
    );
  });
});

describe("composeAskAnswers — decide", () => {
  it("composes you decide without a leaning", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Choose",
        resolution: { kind: "decide", leaning: "" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      "[answers]\n1. [Choose] → you decide",
    );
  });

  it("composes you decide with a leaning when text is provided", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Choose",
        resolution: { kind: "decide", leaning: "probably yes" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Choose] → you decide — leaning: "probably yes"',
    );
  });
});

describe("composeAskAnswers — fallback", () => {
  it("composes the stated fallback", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "fallback" },
        note: "",
        ifUnanswered: "assume yes",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Deploy?] → do your stated fallback ("assume yes")',
    );
  });
});

describe("composeAskAnswers — skip", () => {
  it("composes skipped (no answer) for an explicit skip", () => {
    const items: readonly AskAnswerItem[] = [
      { header: "Deploy?", resolution: { kind: "skip" }, note: "" },
    ];
    expect(composeAskAnswers(items)).toBe(
      "[answers]\n1. [Deploy?] → skipped (no answer)",
    );
  });

  it("composes an unresolved (null) question identically to an explicit skip", () => {
    const items: readonly AskAnswerItem[] = [
      { header: "Deploy?", resolution: null, note: "" },
    ];
    expect(composeAskAnswers(items)).toBe(
      "[answers]\n1. [Deploy?] → skipped (no answer)",
    );
  });
});

describe("composeAskAnswers — notes", () => {
  it("attaches a note to a resolution, suffixed after a dash", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["Yes"] },
        note: "please double check first",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Deploy?] → "Yes" — note: "please double check first"',
    );
  });

  it("a whitespace-only note is treated as no note", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["Yes"] },
        note: "   ",
      },
    ];
    expect(composeAskAnswers(items)).toBe('[answers]\n1. [Deploy?] → "Yes"');
  });
});

describe("composeAskAnswers — several questions in one batch", () => {
  it("composes all answers numbered globally in posting order", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "First",
        resolution: { kind: "option", labels: ["Yes"] },
        note: "",
      },
      {
        header: "Second",
        resolution: { kind: "option", labels: ["B"] },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [First] → "Yes"\n2. [Second] → "B"',
    );
  });

  it("composes an unanswered question in the second position as skip", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "First",
        resolution: { kind: "option", labels: ["Yes"] },
        note: "",
      },
      { header: "Second", resolution: null, note: "" },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [First] → "Yes"\n2. [Second] → skipped (no answer)',
    );
  });
});

describe("composeAskAnswers — header fallback", () => {
  it("uses Question N when header is absent", () => {
    const items: readonly AskAnswerItem[] = [
      { resolution: { kind: "option", labels: ["Yes"] }, note: "" },
    ];
    expect(composeAskAnswers(items)).toBe('[answers]\n1. [Question 1] → "Yes"');
  });
});

describe("composeAskAnswers — quoting and control characters", () => {
  it("escapes a newline in free text", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Notes",
        resolution: { kind: "free", text: "line1\nline2" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Notes] → free text: "line1\\nline2"',
    );
  });

  it("escapes a tab in free text", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Notes",
        resolution: { kind: "free", text: "a\tb" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Notes] → free text: "a\\tb"',
    );
  });

  it("escapes a double quote in free text", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Notes",
        resolution: { kind: "free", text: 'a"b' },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Notes] → free text: "a\\"b"',
    );
  });

  it("escapes a backslash in free text", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Notes",
        resolution: { kind: "free", text: "a\\b" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Notes] → free text: "a\\\\b"',
    );
  });

  it("escapes a C0 control byte as \\xHH in free text", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Notes",
        resolution: { kind: "free", text: "a\x01b" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Notes] → free text: "a\\x01b"',
    );
  });

  it("escapes a control byte inside an option label", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["a\x02b"] },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Deploy?] → "a\\x02b"',
    );
  });

  it("escapes a control byte inside a note", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Deploy?",
        resolution: { kind: "option", labels: ["Yes"] },
        note: "check\tthis\nnow",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Deploy?] → "Yes" — note: "check\\tthis\\nnow"',
    );
  });

  it("quotes a leaning containing a control byte", () => {
    const items: readonly AskAnswerItem[] = [
      {
        header: "Choose",
        resolution: { kind: "decide", leaning: "lean\rhere" },
        note: "",
      },
    ];
    expect(composeAskAnswers(items)).toBe(
      '[answers]\n1. [Choose] → you decide — leaning: "lean\\rhere"',
    );
  });
});
