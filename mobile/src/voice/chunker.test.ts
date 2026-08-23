/**
 * Chunker tests — pure sentence chunking for text-to-speech.
 */

import { describe, expect, it } from "vitest";
import { chunkAssistantProse } from "./chunker";

const OPTS = {
  voiceSessionId: "vs1",
  turnId: "t1",
  itemIndex: 0,
  baseOffset: 0,
};

describe("chunker — punctuation splitting", () => {
  it("splits on . ! ?", () => {
    const { chunks } = chunkAssistantProse(
      "Hello world. Goodbye! What now?",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual([
      "Hello world.",
      "Goodbye!",
      "What now?",
    ]);
  });
});

describe("chunker — abbreviations", () => {
  it("does not split on Mr. Dr. etc.", () => {
    const { chunks } = chunkAssistantProse(
      "Mr. Smith met Dr. Jones. They agreed.",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual([
      "Mr. Smith met Dr. Jones.",
      "They agreed.",
    ]);
  });

  it("does not split on decimals like 3.14", () => {
    const { chunks } = chunkAssistantProse(
      "Pi is 3.14. That is correct.",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual([
      "Pi is 3.14.",
      "That is correct.",
    ]);
  });

  it("does not split on ellipsis ...", () => {
    const { chunks } = chunkAssistantProse("Wait... then go.", OPTS);
    expect(chunks.map((c) => c.text)).toEqual(["Wait... then go."]);
  });
});

describe("chunker — code fences", () => {
  it("skips code fences entirely", () => {
    const { chunks } = chunkAssistantProse(
      "Here is code. \n```js\nconsole.log('hi');\n```\nDone now.",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual(["Here is code.", "Done now."]);
  });

  it("holds an open code fence (not complete)", () => {
    const { chunks } = chunkAssistantProse(
      "Before. \n```js\nconsole.log('hi');\n",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual(["Before."]);
  });
});

describe("chunker — Markdown links", () => {
  it("extracts link text, not URL", () => {
    const { chunks } = chunkAssistantProse(
      "See [the docs](https://example.com). Now go.",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual(["See the docs.", "Now go."]);
  });

  it("does not speak image alt text", () => {
    const { chunks } = chunkAssistantProse(
      "Look ![a chart](x.png). Done.",
      OPTS,
    );
    // The image is skipped entirely; the trailing "." ends the "Look" sentence.
    expect(chunks.map((c) => c.text)).toEqual(["Look .", "Done."]);
  });
});

describe("chunker — lists", () => {
  it("speak list items individually", () => {
    const { chunks } = chunkAssistantProse(
      "Steps:\n- Item one.\n- Item two.",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual([
      "Steps:",
      "Item one.",
      "Item two.",
    ]);
  });

  it("handles numbered lists", () => {
    const { chunks } = chunkAssistantProse("1. First.\n2. Second.", OPTS);
    expect(chunks.map((c) => c.text)).toEqual(["First.", "Second."]);
  });
});

describe("chunker — incomplete sentence", () => {
  it("holds incomplete trailing text by default", () => {
    const { chunks } = chunkAssistantProse("Done. This is incomplete", OPTS);
    expect(chunks.map((c) => c.text)).toEqual(["Done."]);
  });

  it("emits incomplete text when flush=true", () => {
    const { chunks } = chunkAssistantProse("Done. This is incomplete", {
      ...OPTS,
      flush: true,
    });
    expect(chunks.map((c) => c.text)).toEqual(["Done.", "This is incomplete"]);
  });
});

describe("chunker — 180-character fallback boundary", () => {
  it("splits long sentences at word boundaries", () => {
    const longWord = "word ".repeat(50); // 250 chars
    const text = `Lead in. ${longWord}after.`;
    const { chunks } = chunkAssistantProse(text, OPTS);
    expect(chunks[0]?.text).toBe("Lead in.");
    // The long run is split into multiple sub-chunks each <= 180 chars.
    for (const c of chunks.slice(1)) {
      expect(c.text.length).toBeLessThanOrEqual(180);
    }
    expect(chunks.length).toBeGreaterThan(2);
    // The last sub-chunk ends with "after."
    expect(chunks[chunks.length - 1]?.text).toMatch(/after\.$/);
  });
});

describe("chunker — whitespace handling", () => {
  it("collapses extra whitespace", () => {
    const { chunks } = chunkAssistantProse(
      "Hello    world.   New   sentence.",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual([
      "Hello world.",
      "New sentence.",
    ]);
  });
});

describe("chunker — Unicode", () => {
  it("handles CJK text", () => {
    const { chunks } = chunkAssistantProse("你好世界。再见！现在怎样？", OPTS);
    expect(chunks.map((c) => c.text)).toEqual([
      "你好世界。",
      "再见！",
      "现在怎样？",
    ]);
  });

  it("handles emoji", () => {
    const { chunks } = chunkAssistantProse(
      "Hello 🎉 world. Emoji test 🚀!",
      OPTS,
    );
    expect(chunks.map((c) => c.text)).toEqual([
      "Hello 🎉 world.",
      "Emoji test 🚀!",
    ]);
  });
});

describe("chunker — no raw tool/reasoning content", () => {
  it("chunker is pure prose — caller filters by kind", () => {
    // The chunker itself is a pure text function; the coordinator only feeds it
    // assistant `markdown`. Reasoning/tools/notices never reach it. Verify it
    // does not invent content from prose alone.
    const { chunks } = chunkAssistantProse("Just prose here.", OPTS);
    expect(chunks).toHaveLength(1);
    expect(chunks[0]?.text).toBe("Just prose here.");
  });
});

describe("chunker — deterministic chunk IDs", () => {
  it("IDs are {voiceSessionId}:{turnId}:{itemIndex}:{rangeStart}-{rangeEnd}", () => {
    const { chunks } = chunkAssistantProse("First sentence. Second sentence.", {
      voiceSessionId: "vs9",
      turnId: "t3",
      itemIndex: 2,
      baseOffset: 100,
    });
    expect(chunks).toHaveLength(2);
    expect(chunks[0]?.id).toMatch(/^vs9:t3:2:100-115$/);
    expect(chunks[1]?.id).toMatch(/^vs9:t3:2:\d+-\d+$/);
    // IDs are stable across calls with identical input.
    const again = chunkAssistantProse("First sentence. Second sentence.", {
      voiceSessionId: "vs9",
      turnId: "t3",
      itemIndex: 2,
      baseOffset: 100,
    });
    expect(again.chunks.map((c) => c.id)).toEqual(chunks.map((c) => c.id));
  });

  it("range offsets track baseOffset", () => {
    const { chunks } = chunkAssistantProse("Hi.", {
      voiceSessionId: "vs1",
      turnId: "t1",
      itemIndex: 0,
      baseOffset: 50,
    });
    expect(chunks).toHaveLength(1);
    expect(chunks[0]?.rangeStart).toBe(50);
    expect(chunks[0]?.rangeEnd).toBe(53);
  });
});
