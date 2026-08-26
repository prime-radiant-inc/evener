// Boundary tests for the live-concepts isolation checker. Creates an owned
// temporary src/live-concepts tree with one fixture per rejection, plus
// accepted scoped CSS inside live-concepts and a production type-only import
// outside live-concepts. Calls exported checkLiveConceptBoundary(root).

import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { checkLiveConceptBoundary } from "./check-live-concepts-boundary.mjs";

// One fixture for each rejection: [name, content, expectedCode, extension]
const rejections = [
  ["fixture import", 'import "../../../mobile-concepts/src/core/fixtures"', "forbidden-import", "ts"],
  ["prototype type", "type X = PrototypeState", "forbidden-symbol", "ts"],
  ["fixture access", "state.projection.fixture.sessions", "forbidden-symbol", "ts"],
  ["synthetic control", "syntheticTurn", "forbidden-symbol", "ts"],
  ["lab hook", "onOpenLabControls()", "forbidden-symbol", "ts"],
  ["transport", "new WebSocket(url)", "forbidden-transport", "ts"],
  ["tauri", 'import { invoke } from "@tauri-apps/api/core"', "forbidden-transport", "ts"],
  ["global css", "body { color: red; }", "unscoped-css", "css"],
  // Narrow-scope negatives: selectors that look like .live-concept-* but are
  // NOT scoped under the exact .live-concept-switcher root.
  ["evil prefix", ".live-concept-evil { color: red; }", "unscoped-css", "css"],
  ["mixed unscoped", ".live-concept-switcher__row, body { color: red; }", "unscoped-css", "css"],
  ["unscoped descendant", ".live-concept-switcher body { color: red; }", "unscoped-css", "css"],
];

function slug(name) {
  return name.replace(/\s+/g, "-");
}

async function buildTempTree() {
  const root = await mkdtemp(join(tmpdir(), "live-concepts-boundary-"));
  const liveConcepts = join(root, "src", "live-concepts");
  await mkdir(liveConcepts, { recursive: true });

  for (const [name, content, _code, ext] of rejections) {
    await writeFile(join(liveConcepts, `${slug(name)}.${ext}`), `${content}\n`, "utf8");
  }

  // Accepted: .concept-* scoped CSS inside live-concepts
  await writeFile(
    join(liveConcepts, "good-scoped.css"),
    ".concept-stillwater .sw-row {}\n",
    "utf8",
  );

  // Accepted: .live-concept-switcher root-scoped CSS inside live-concepts.
  // Every selector list must start with the .live-concept-switcher root.
  await writeFile(
    join(liveConcepts, "good-switcher-scoped.css"),
    ".live-concept-switcher .live-concept-switcher__dialog {}\n" +
    ".live-concept-switcher__overlay {}\n" +
    ".live-concept-switcher__choice[data-concept-selected=\"true\"] {}\n",
    "utf8",
  );

  // Accepted: production type-only import outside live-concepts (would be
  // forbidden-import if it were inside live-concepts, but the boundary only
  // governs live-concepts files).
  await writeFile(
    join(root, "src", "production-type-only.ts"),
    'import type { SessionRecord } from "../../mobile-concepts/src/core/model";\nexport type { SessionRecord };\n',
    "utf8",
  );

  return root;
}

describe("checkLiveConceptBoundary", () => {
  it("rejects one fixture for each forbidden pattern with the expected code", async () => {
    const root = await buildTempTree();
    try {
      const violations = await checkLiveConceptBoundary(root);
      assert.equal(
        violations.length,
        rejections.length,
        `expected ${rejections.length} violations, got ${JSON.stringify(violations, null, 2)}`,
      );
      for (const [name, _content, code, ext] of rejections) {
        const filePart = `${slug(name)}.${ext}`;
        const found = violations.find(
          (v) => v.code === code && v.file.includes(filePart),
        );
        assert.ok(
          found,
          `expected violation code=${code} file~${filePart}, got ${JSON.stringify(violations, null, 2)}`,
        );
      }
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it("accepts scoped CSS inside live-concepts", async () => {
    const root = await buildTempTree();
    try {
      const violations = await checkLiveConceptBoundary(root);
      assert.equal(
        violations.find((v) => v.file.includes("good-scoped.css")),
        undefined,
        "scoped .concept-* CSS should not be flagged",
      );
      assert.equal(
        violations.find((v) => v.file.includes("good-switcher-scoped.css")),
        undefined,
        "scoped .live-concept-switcher CSS should not be flagged",
      );
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it("rejects .live-concept-evil and unscoped selectors mixed with switcher", async () => {
    const root = await buildTempTree();
    try {
      const violations = await checkLiveConceptBoundary(root);
      // .live-concept-evil must be rejected — not a valid switcher root.
      assert.ok(
        violations.find((v) => v.file.includes("evil-prefix.css") && v.code === "unscoped-css"),
        ".live-concept-evil should be flagged as unscoped",
      );
      // Mixed selector list containing body should be rejected.
      assert.ok(
        violations.find((v) => v.file.includes("mixed-unscoped.css") && v.code === "unscoped-css"),
        "mixed unscoped selector list should be flagged",
      );
      // Unscoped descendant outside the switcher root should be rejected.
      assert.ok(
        violations.find((v) => v.file.includes("unscoped-descendant.css") && v.code === "unscoped-css"),
        "unscoped descendant outside root should be flagged",
      );
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it("accepts production type-only imports outside live-concepts", async () => {
    const root = await buildTempTree();
    try {
      const violations = await checkLiveConceptBoundary(root);
      assert.equal(
        violations.find((v) => v.file.includes("production-type-only.ts")),
        undefined,
        "production file outside live-concepts should not be flagged",
      );
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it("returns sorted violations", async () => {
    const root = await buildTempTree();
    try {
      const violations = await checkLiveConceptBoundary(root);
      const keys = violations.map((v) => `${v.file}\0${v.code}\0${v.detail}`);
      const sorted = [...keys].sort();
      assert.deepEqual(keys, sorted, "violations should be sorted by file, code, detail");
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
});
