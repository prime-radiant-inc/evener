import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import * as guard from "./comicguard/run.mjs";

const streamSource =
  "COMIC_STREAM_BEFORE **bold**\n\n```text\n  COMIC_STREAM_CODE\n\tCOMIC_STREAM_END  \n```\n\n| key | value |\n| --- | --- |\n| COMIC_STREAM_CELL | **settled** |\n\nCOMIC_STREAM_AFTER *done*";
const streamCuts = [
  0,
  streamSource.indexOf("```text"),
  streamSource.indexOf("| key"),
  streamSource.indexOf("COMIC_STREAM_AFTER"),
  streamSource.length,
];
function streamDOM(mutate = () => {}) {
  const dom = new JSDOM("<main><div data-comic-scroll><div></div></div></main>", { runScripts: "outside-only" });
  const { window } = dom;
  window.__comicRoot = () => window.document.querySelector("main");
  const port = window.document.querySelector("[data-comic-scroll]").firstElementChild;
  const html = [
    "<p>COMIC_STREAM_BEFORE <strong>bold</strong></p>",
    "<pre><code>  COMIC_STREAM_CODE\n\tCOMIC_STREAM_END  </code></pre>",
    "<table><thead><tr><th>key</th><th>value</th></tr></thead><tbody><tr><td>COMIC_STREAM_CELL</td><td><strong>settled</strong></td></tr></tbody></table>",
    "<p>COMIC_STREAM_AFTER <em>done</em></p>",
  ];
  for (const [index, content] of html.entries()) {
    const node = window.document.createElement("article");
    Object.assign(node.dataset, {
      comicEntry: "source",
      comicStart: String(streamCuts[index]),
      comicEnd: String(streamCuts[index + 1]),
    });
    node.innerHTML = `<div data-comic-text><div data-comic-markdown="${["prose", "code", "table", "prose"][index]}">${content}</div></div>`;
    port.append(node);
  }
  mutate(port);
  const observation = window.eval(`(${guard.observeFormattedStream.toString()})()`);
  dom.window.close();
  return JSON.parse(JSON.stringify(observation));
}
function streamValue(final = streamDOM(), settled = streamDOM()) {
  const partial = streamDOM((port) => {
    port.children[3].remove();
    port.children[2].remove();
    port.children[1].dataset.comicEnd = String(streamSource.indexOf("\n\tCOMIC_STREAM_END"));
    port.querySelector("code").textContent = "  COMIC_STREAM_CODE";
  });
  const table = streamDOM((port) => {
    port.children[3].remove();
    port.children[2].dataset.comicEnd = String(streamSource.indexOf(" **settled**"));
    port.querySelector("td:last-child").textContent = "";
  });
  return {
    ids: ["source", "source", "source"],
    partialCode: "  COMIC_STREAM_CODE",
    ...final,
    snapshots: { partial, table, final, settled },
  };
}
test("stream observer accepts complete content before and after settlement", () => {
  assert.doesNotThrow(() => guard.verifyFormattedStream(streamValue()));
});
test("stream observer excludes the earlier unrelated stream input", () => {
  const final = streamDOM((port) => {
    const unrelated = port.ownerDocument.createElement("article");
    unrelated.dataset.comicEntry = "earlier-input";
    unrelated.textContent = "COMIC_STREAM_end";
    port.prepend(unrelated);
  });
  assert.doesNotThrow(() => guard.verifyFormattedStream(streamValue(final)));
});
for (const [name, mutate] of [
  ["missing prefix", (port) => port.firstElementChild.remove()],
  ["duplicate code", (port) => port.children[1].after(port.children[1].cloneNode(true))],
  [
    "shifted source range",
    (port) => {
      port.children[1].dataset.comicStart = String(streamCuts[1] + 1);
    },
  ],
  [
    "changed prefix text",
    (port) => {
      port.querySelector("p").textContent = "COMIC_STREAM_BEFORE replacement";
    },
  ],
  [
    "duplicate whole-source identity",
    (port) => {
      for (const node of [...port.children]) {
        const copy = node.cloneNode(true);
        copy.dataset.comicEntry = "duplicate";
        port.append(copy);
      }
    },
  ],
])
  test(`stream observer rejects ${name}`, () => {
    assert.throws(() => guard.verifyFormattedStream(streamValue(streamDOM(mutate))), Error);
  });
for (const [name, mutate] of [
  ["drop", (port) => port.firstElementChild.remove()],
  [
    "replacement",
    (port) => {
      for (const node of port.children) node.dataset.comicEntry = "replacement";
    },
  ],
])
  test(`stream observer rejects completion-time ${name}`, () => {
    assert.throws(() => guard.verifyFormattedStream(streamValue(streamDOM(), streamDOM(mutate))), Error);
  });
function relocationValue(relocation) {
  const expected = {
    mode: "away",
    sessionRef: "child",
    path: ["parent", "child"],
    entryId: "source",
    offset: 7,
    viewportOffset: 24,
  };
  const before = { saved: expected, point: { ...expected, y: 24 } };
  return {
    grandchild: { key: "child" },
    relocated: { key: "child", expanded: "true" },
    lifetime: [],
    relocation,
    expected,
    before,
    after: structuredClone(before),
  };
}

function relocationDOM(placed, mutate = () => {}) {
  const dom = new JSDOM("<main><div data-comic-scroll><div></div></div></main>", { runScripts: "outside-only" });
  const { window } = dom;
  window.__comicRoot = () => window.document.querySelector("main");
  const port = window.document.querySelector("[data-comic-scroll]").firstElementChild;
  const row = (content) => `<div data-index>${content}</div>`;
  const before = row(
    '<section data-comic-row="before" data-comic-session="parent" data-comic-path="[]"><div data-comic-entry="input" data-comic-session="parent" data-comic-path="[]">COMIC_SUBCHAT_CHILD</div></section>',
  );
  const strip = row(
    '<div data-comic-flow="strip" data-comic-session="parent" data-comic-path="[]"><div data-comic-entry="child" data-comic-session="parent" data-comic-path="[]"><div data-comic-delegate="child" data-comic-child-ref="grandchild"><button aria-expanded="true"></button></div></div></div>',
  );
  const after = row(
    '<section data-comic-row="after" data-comic-session="parent" data-comic-path="[]"><div data-comic-entry="continuation" data-comic-session="parent" data-comic-path="[]">COMIC_CHILD_SOURCE_00</div></section>',
  );
  const marker = row('<div data-comic-flow="unplaced-group" data-comic-session="parent" data-comic-path="[]"></div>');
  port.innerHTML = placed ? before + strip + after : marker + strip + before + after;
  mutate(port);
  const observation = window.eval(
    `(${guard.observeDelegateRelocation.toString()})('parent','grandchild',{beforeId:'input',afterId:'continuation'})`,
  );
  dom.window.close();
  return JSON.parse(JSON.stringify(observation));
}

test("relocation observer rejects a pending strip after its sibling unplaced marker", () => {
  const observation = relocationDOM(false);
  assert.throws(() => guard.verifyDelegateRelocation(relocationValue(observation)), Error);
  assert.equal(observation.unplaced, true);
  assert.equal(observation.atLaunch, false);
});

for (const [name, mutate] of [
  [
    "wrong preceding source",
    (port) => {
      port.querySelector('[data-comic-entry="input"]').dataset.comicEntry = "other";
    },
  ],
  [
    "wrong following source",
    (port) => {
      port.querySelector('[data-comic-entry="continuation"]').dataset.comicEntry = "other";
    },
  ],
  [
    "strip before launch input without group marker",
    (port) => {
      port.prepend(port.children[1]);
    },
  ],
  [
    "strip after continuation",
    (port) => {
      port.append(port.children[1]);
    },
  ],
  [
    "missing heading",
    (port) => {
      port.querySelector("[data-comic-delegate]").remove();
    },
  ],
  [
    "wrong reader path",
    (port) => {
      port.querySelector('[data-comic-entry="input"]').dataset.comicPath = '["other"]';
    },
  ],
])
  test(`relocation observer rejects ${name}`, () => {
    assert.throws(() => guard.verifyDelegateRelocation(relocationValue(relocationDOM(true, mutate))), Error);
  });

test("relocation observer ignores nested-reader rows between launch neighbors", () => {
  const observation = relocationDOM(true, (port) => {
    const nested = port.ownerDocument.createElement("div");
    nested.innerHTML =
      '<section data-comic-row="nested" data-comic-session="grandchild" data-comic-path="[&quot;child&quot;]"><div data-comic-entry="nested-source" data-comic-session="grandchild" data-comic-path="[&quot;child&quot;]">nested source</div></section>';
    port.children[1].after(nested);
  });
  assert.doesNotThrow(() => guard.verifyDelegateRelocation(relocationValue(observation)));
  assert.equal(observation.beforeId, "input");
  assert.equal(observation.afterId, "continuation");
});

function recordedOrigin() {
  return {
    request: { params: { ref: "parent" } },
    response: {
      result: {
        data: [
          {
            items: [
              { id: "input", type: "userMessage", text: "COMIC_SUBCHAT_CHILD", position: { entry: 2 } },
              {
                id: "launch",
                callId: "comic_delegate_GRANDCHILD",
                toolName: "delegate",
                output: JSON.stringify({ delegate_id: "delegate", transcript_ref: "grandchild" }),
                position: { entry: 3 },
              },
              {
                id: "answer",
                roundId: "r_fixture",
                type: "agentMessage",
                text: "COMIC_CHILD_SOURCE_00 fixture",
                position: { entry: 5 },
              },
            ],
          },
        ],
      },
    },
  };
}
test("recorded relocation binds the receipt and both launch-neighbor identities", () => {
  const value = guard.recordedRelocationOrigin(recordedOrigin(), "parent", {
    ref: "grandchild",
    key: '["parent","delegate"]',
  });
  assert.equal(value.creationId, "launch");
  assert.equal(value.beforeId, "input");
  assert.equal(value.afterId, "round:r_fixture:agentMessage");
});
for (const [name, mutate] of [
  [
    "wrong request",
    (value) => {
      value.request.params.ref = "other";
    },
  ],
  [
    "wrong receipt",
    (value) => {
      value.response.result.data[0].items[1].output = JSON.stringify({
        delegate_id: "other",
        transcript_ref: "grandchild",
      });
    },
  ],
  [
    "wrong source order",
    (value) => {
      value.response.result.data[0].items[2].position.entry = 1;
    },
  ],
])
  test(`recorded relocation rejects ${name}`, () => {
    const value = recordedOrigin();
    mutate(value);
    assert.throws(
      () => guard.recordedRelocationOrigin(value, "parent", { ref: "grandchild", key: '["parent","delegate"]' }),
      Error,
    );
  });

test("relocation rejects shifted glyph with unchanged identity and lifetime", () => {
  const value = relocationValue(relocationDOM(true));
  assert.doesNotThrow(() => guard.verifyDelegateRelocation(value));
  value.after.point.y = 224;
  assert.throws(() => guard.verifyDelegateRelocation(value), Error);
});
test("chronology rejects the same strip moved after the continuation on completion", () => {
  const before = {
    before: 0,
    launch: 1,
    after: 2,
    entries: [{ id: "input" }, { id: "strip" }, { id: "continuation" }],
  };
  const after = { before: 0, launch: 2, after: 1, entries: [{ id: "input" }, { id: "continuation" }, { id: "strip" }] };
  assert.doesNotThrow(() => guard.verifyCreationChronology(before, structuredClone(before)));
  assert.throws(() => guard.verifyCreationChronology(before, after), Error);
  const replaced = structuredClone(before);
  replaced.entries[2].id = "different-continuation";
  assert.throws(() => guard.verifyCreationChronology(before, replaced), Error);
});

const transition = {
  width: 850,
  availableWidth: 835,
  rootFontSize: 16,
  readerLeft: 15,
  readerRight: 850,
  single: { panel: 416, probe: 416, left: 15, right: 431 },
  paired: { rowId: "real-pair-1", panel: 410, probe: 410, left: 15, firstRight: 425, secondLeft: 440, right: 850 },
};

test("rejects a paired panel checked against a reused single probe", () => {
  assert.throws(
    () => guard.verifyPanelWidths({ ...transition, paired: { ...transition.paired, probe: 416 } }, "transition"),
    { name: "Error" },
  );
});

test("accepts independently measured single and real paired geometry at the transition width", () => {
  assert.doesNotThrow(() => guard.verifyPanelWidths(transition, "transition", true));
  assert.throws(() => guard.verifyPanelWidths({ ...transition, paired: null }, "transition", true), { name: "Error" });
});

test("requires the canonical error at mounted width and mode checkpoints", () => {
  const id = "provider-item:turn-error";
  assert.doesNotThrow(() =>
    guard.verifyCanonicalErrorMounted({ mounted: 3, parts: [{ entryId: id }], errorGroups: [] }, id, "width-390"),
  );
  assert.throws(
    () =>
      guard.verifyCanonicalErrorMounted(
        { mounted: 3, parts: [{ entryId: "another-item" }], errorGroups: [] },
        id,
        "plain-mode",
      ),
    { name: "Error" },
  );
});

test("rejects a duplicate source range owned by a different virtual row", () => {
  const covered = new Map();
  const first = { rowId: "row-a", entryId: "source", start: 0, end: 4, text: "abcd" };
  const duplicate = { ...first, rowId: "row-b" };
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [first], "abcd"));
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [first], "abcd"));
  assert.throws(() => guard.collectSourceSlices(covered, [duplicate], "abcd"), { name: "Error" });
  assert.equal(covered.size, 1);
});

test("formatted fixture coverage retains raw UTF16 ranges while checking rendered glyphs", () => {
  const source = "A *word* and `code` 😀.";
  const part = {
    rowId: "formatted",
    entryId: "source",
    start: 0,
    end: source.length,
    formatted: true,
    text: "A word and code 😀.\n",
  };
  const covered = new Map();
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [part], source));
  assert.throws(() => guard.collectSourceSlices(new Map(), [{ ...part, text: source }], source), Error);
  assert.throws(() => guard.collectSourceSlices(covered, [{ ...part, rowId: "duplicate" }], source), Error);
});

test("independent inline fixture oracle pins visible UTF16 offsets, not Markdown delimiters", () => {
  const source = "😀 *word* and `code`";
  const glyphs = guard.inlineFixtureGlyphs(source);
  assert.equal(glyphs.map((glyph) => glyph.char).join(""), "😀 word and code");
  assert.equal(glyphs.find((glyph) => glyph.char === "w").source, 4);
  assert.equal(glyphs.find((glyph) => glyph.char === "c").source, 15);
});

const inserts = () => ({
  width: 390,
  rootFontSize: 16,
  availableWidth: 360,
  overflow: 0,
  kinds: ["prose", "code", "table", "prose"],
  semantics: {
    heading: true,
    strong: true,
    emphasis: true,
    quote: true,
    listStart: 7,
    inlineCode: true,
    safeLink: true,
    unsafeLinks: 0,
  },
  code: {
    text: "  alpha\n\tbeta  ",
    width: 360,
    height: 200,
    clientHeight: 200,
    scrollHeight: 200,
    scrollWidth: 900,
    clientWidth: 330,
    scrollLeft: 400,
    stage: false,
    size: 14,
  },
  table: {
    cells: ["one", "two"],
    width: 360,
    height: 200,
    clientHeight: 200,
    scrollHeight: 200,
    scrollWidth: 900,
    clientWidth: 330,
    scrollLeft: 400,
    stage: false,
    size: 18,
  },
});
const insertExpected = { code: "  alpha\n\tbeta  ", cells: ["one", "two"] };
test("formatted boundaries reject shifted or duplicated original source ranges", () => {
  const source = "before\n\n```text\n  alpha\n\tbeta  \n```\n\n| one | two |\n| --- | --- |\n\nafter";
  const code = source.indexOf("```text");
  const table = source.indexOf("| one");
  const after = source.indexOf("after");
  const value = {
    ...inserts(),
    parts: [
      { start: 0, end: code, kind: "prose" },
      { start: code, end: table, kind: "code" },
      { start: table, end: after, kind: "table" },
      { start: after, end: source.length, kind: "prose" },
    ],
  };
  const expected = { ...insertExpected, source, tableStart: table, proseStart: after };
  assert.doesNotThrow(() => guard.verifyFormattedObservation(value, expected));
  for (const mutation of [(v) => v.parts[1].start++, (v) => (v.parts[2].start = code), (v) => v.parts[3].end--]) {
    const bad = structuredClone(value);
    mutation(bad);
    assert.throws(() => guard.verifyFormattedObservation(bad, expected), Error);
  }
});
test("accepts complete semantic inserts with usable horizontal scrolling", () => {
  assert.doesNotThrow(() => guard.verifyFormattedObservation(inserts(), insertExpected));
});
for (const [name, mutate] of [
  ["wrong source order", (v) => v.kinds.reverse()],
  ["literal Markdown", (v) => (v.semantics.strong = false)],
  ["absent leading heading", (v) => (v.semantics.heading = false)],
  ["unsafe link", (v) => (v.semantics.unsafeLinks = 1)],
  ["lost ordered-list start", (v) => (v.semantics.listStart = 1)],
  ["code whitespace loss", (v) => (v.code.text = v.code.text.trim())],
  ["table cell omission", (v) => v.table.cells.pop()],
  ["character stage", (v) => (v.code.stage = true)],
  ["stretched insert", (v) => (v.code.width = 641)],
  ["nested vertical reader", (v) => (v.code.scrollHeight = 400)],
  ["unreadable horizontal overflow", (v) => (v.table.scrollLeft = 0)],
  ["shrunk code", (v) => (v.code.size = 8)],
  ["page overflow", (v) => (v.overflow = 10)],
])
  test(`formatted oracle rejects ${name}`, () => {
    const value = inserts();
    mutate(value);
    assert.throws(() => guard.verifyFormattedObservation(value, insertExpected), Error);
  });

test("stream oracle requires one stable source and completed semantic blocks", () => {
  const value = {
    snapshots: streamValue().snapshots,
    ids: ["source", "source", "source"],
    partialCode: "  COMIC_STREAM_CODE",
    code: "  COMIC_STREAM_CODE\n\tCOMIC_STREAM_END  ",
    cells: ["key", "value", "COMIC_STREAM_CELL", "settled"],
    after: true,
    literalFence: false,
  };
  assert.doesNotThrow(() => guard.verifyFormattedStream(value));
  for (const mutation of [
    (v) => (v.ids[2] = "replacement"),
    (v) => (v.partialCode = ""),
    (v) => v.cells.pop(),
    (v) => (v.literalFence = true),
  ]) {
    const bad = structuredClone(value);
    mutation(bad);
    assert.throws(() => guard.verifyFormattedStream(bad), Error);
  }
});

test("collapse oracle requires retirement without losing the mounted sibling", () => {
  const value = {
    collapsed: "a",
    sibling: "b",
    mountedCollapsed: true,
    siblingExpanded: true,
    retired: ["a"],
    reread: [],
  };
  assert.doesNotThrow(() => guard.verifyBranchRetirement(value));
  for (const mutation of [
    (v) => (v.retired = []),
    (v) => v.retired.push("b"),
    (v) => (v.siblingExpanded = false),
    (v) => (v.mountedCollapsed = false),
    (v) => v.reread.push("b"),
  ]) {
    const bad = structuredClone(value);
    mutation(bad);
    assert.throws(() => guard.verifyBranchRetirement(bad), Error);
  }
});
