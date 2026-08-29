import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, it } from "node:test";
import { checkLiveConceptBoundary } from "./check-live-concepts-boundary.mjs";

const GOOD_CONTRACT = `
import type { ConversationSkin } from "./conversation/contract";
import type { ConversationFrameAction } from "./conversation/primitives";
export type ContractSentinel = ConversationSkin | ConversationFrameAction;
`;
const GOOD_MODULE = `
import { skin } from "./ConversationSkin";
export const module = { id: "stillwater", conversationSkin: skin };
`;
const GOOD_SKIN = `
export const skin = {
  renderNarrativeItem({ body }) { return <div>{body}</div>; },
  renderActivityMarker({ item }) { return <span>{item.state}</span>; },
  renderConversationChrome({ title }) { return <h1>{title.text}</h1>; },
};
`;
const GOOD_PLATFORM = `
export function install(viewport, root) {
  viewport.visualViewport?.addEventListener("resize", () => {});
  root.style.setProperty("--viewport-height", "1px");
  root.style.setProperty("--keyboard-inset", "0px");
}
`;

async function createTree(files = {}, { contract = GOOD_CONTRACT } = {}) {
  const root = await mkdtemp(join(tmpdir(), "live-concepts-boundary-"));
  const defaults = {
    "src/live-concepts/contract.ts": contract,
    "src/live-concepts/stillwater/index.ts": GOOD_MODULE,
    "src/live-concepts/stillwater/ConversationSkin.tsx": GOOD_SKIN,
    "src/live-concepts/constellation/index.ts": GOOD_MODULE,
    "src/live-concepts/constellation/ConversationSkin.tsx": GOOD_SKIN,
    "src/live-concepts/field-notes/index.ts": GOOD_MODULE,
    "src/live-concepts/field-notes/ConversationSkin.tsx": GOOD_SKIN,
    "src/ui/platformPresentation.ts": GOOD_PLATFORM,
    ...files,
  };
  for (const [relative, content] of Object.entries(defaults)) {
    const target = join(root, relative);
    await mkdir(dirname(target), { recursive: true });
    await writeFile(target, `${content}\n`, "utf8");
  }
  return root;
}

async function violationsFor(files = {}, options = {}) {
  const root = await createTree(files, options);
  try {
    return await checkLiveConceptBoundary(root);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

function expectViolation(violations, expected) {
  assert.ok(
    violations.some(
      (violation) =>
        violation.code === expected.code &&
        violation.file === expected.file &&
        violation.detail === expected.detail,
    ),
    `expected ${JSON.stringify(expected)}, got ${JSON.stringify(violations, null, 2)}`,
  );
}

describe("checkLiveConceptBoundary", () => {
  it("rejects every ConversationSkin structural ownership pattern", async () => {
    const cases = [
      {
        name: "virtualizer",
        source: 'import { useVirtualizer } from "@tanstack/react-virtual";',
        detail: "ConversationSkin must not import @tanstack/react-virtual",
      },
      {
        name: "mobile-item",
        source: "const item: MobileTimelineItem = value;",
        detail: "ConversationSkin must not reference MobileTimelineItem",
      },
      {
        name: "store",
        source:
          'import { createConversationStore } from "../../state/conversation";',
        detail:
          "ConversationSkin must not import a conversation store or service",
      },
      {
        name: "service",
        source:
          'import type { ConversationService } from "../../services/conversation";',
        detail:
          "ConversationSkin must not import a conversation store or service",
      },
      {
        name: "items-map",
        source: "const rows = items.map((item) => item.key);",
        detail: "ConversationSkin must not map conversation items",
      },
      {
        name: "button",
        source:
          'export const View = () => <button type="button">Send</button>;',
        detail: "ConversationSkin must not render interactive controls",
      },
      {
        name: "anchor",
        source: 'export const View = () => <a href="/work">Work</a>;',
        detail: "ConversationSkin must not render interactive controls",
      },
      {
        name: "input",
        source: 'export const View = () => <input aria-label="Message" />;',
        detail: "ConversationSkin must not render interactive controls",
      },
      {
        name: "textarea",
        source: 'export const View = () => <textarea aria-label="Message" />;',
        detail: "ConversationSkin must not render interactive controls",
      },
      {
        name: "select",
        source: 'export const View = () => <select aria-label="Mode" />;',
        detail: "ConversationSkin must not render interactive controls",
      },
      {
        name: "callback",
        source: "interface Props { onSend(value: string): void }",
        detail: "ConversationSkin must not declare control callback props",
      },
      {
        name: "scroller",
        source:
          'export const View = () => <div data-live-concept-scroller="true" />;',
        detail: "ConversationSkin must not declare data-live-concept-scroller",
      },
    ];
    const files = Object.fromEntries(
      cases.map(({ name, source }) => [
        `src/live-concepts/rejected-${name}/ConversationSkin.tsx`,
        source,
      ]),
    );
    const violations = await violationsFor(files);
    for (const { name, detail } of cases) {
      expectViolation(violations, {
        code: "conversation-skin-boundary",
        file: `src/live-concepts/rejected-${name}/ConversationSkin.tsx`,
        detail,
      });
    }
  });

  it("rejects only conversation-owned CSS scrolling and composer positioning", async () => {
    const violations = await violationsFor({
      "src/live-concepts/stillwater/rejected.css": `
        .concept-stillwater.sw-conversation-skin .composer { position: fixed; }
        .concept-stillwater[data-surface="conversation"] .transcript { overflow-y: auto; }
        .concept-stillwater.sw-conversation-skin [data-live-concept-scroller="true"] { color: red; }
      `,
      "src/live-concepts/stillwater/accepted.css": `
        .concept-stillwater .sessions-list { overflow-y: auto; }
        .concept-stillwater .work-list { overflow-y: scroll; }
        .concept-stillwater .work-toolbar { position: sticky; }
      `,
    });
    for (const detail of [
      "conversation composer must not use fixed/sticky positioning",
      "active conversation must not own overflow-y",
      "conversation skin must not declare data-live-concept-scroller",
    ]) {
      expectViolation(violations, {
        code: "conversation-css-boundary",
        file: "src/live-concepts/stillwater/rejected.css",
        detail,
      });
    }
    assert.equal(
      violations.find((violation) => violation.file.endsWith("accepted.css")),
      undefined,
    );
  });

  it("allows Sessions/Work mapping and shell scrolling while rejecting conversation items.map", async () => {
    const violations = await violationsFor({
      "src/live-concepts/stillwater/Renderer.tsx": `
        export function Sessions({ groups }) {
          return <main data-live-concept-scroller="true">{groups.map((group) => group.label)}</main>;
        }
        export function Work({ work }) {
          return <section>{work.map((item) => item.label)}</section>;
        }
      `,
    });
    assert.equal(
      violations.find((violation) =>
        violation.file.endsWith("stillwater/Renderer.tsx"),
      ),
      undefined,
      JSON.stringify(violations, null, 2),
    );
  });

  it("requires conversationSkin on every registered concept module", async () => {
    const violations = await violationsFor({
      "src/live-concepts/field-notes/index.ts":
        'export const fieldNotesModule = { id: "field-notes", Renderer: () => null };',
    });
    expectViolation(violations, {
      code: "missing-conversation-skin",
      file: "src/live-concepts/field-notes/index.ts",
      detail: "live concept module must provide conversationSkin",
    });
  });

  it("enforces neutral import direction for every production conversation file", async () => {
    const cases = [
      {
        name: "parent-import",
        source: 'import type { X } from "../contract";',
        detail: "conversation primitive must not import ../contract",
      },
      {
        name: "parent-state",
        source: "type ChildState = LiveConceptState;",
        detail: "conversation primitive must not reference LiveConceptState",
      },
      {
        name: "parent-intent",
        source: "type ChildIntent = LiveConceptIntent;",
        detail: "conversation primitive must not reference LiveConceptIntent",
      },
    ];
    const files = Object.fromEntries(
      cases.map(({ name, source }) => [
        `src/live-concepts/conversation/${name}.tsx`,
        source,
      ]),
    );
    const violations = await violationsFor(files);
    for (const { name, detail } of cases) {
      expectViolation(violations, {
        code: "conversation-import-direction",
        file: `src/live-concepts/conversation/${name}.tsx`,
        detail,
      });
    }
  });

  it("requires the parent contract to import both neutral conversation modules", async () => {
    const violations = await violationsFor(
      {},
      {
        contract:
          'import type { ConversationSkin } from "./conversation/contract"; export type X = ConversationSkin;',
      },
    );
    expectViolation(violations, {
      code: "conversation-import-direction",
      file: "src/live-concepts/contract.ts",
      detail: "parent contract must import ./conversation/primitives",
    });
  });

  it("strips comments before parsing imports, symbols, and selectors", async () => {
    const violations = await violationsFor({
      "src/live-concepts/stillwater/comments.tsx": `
        // import { useVirtualizer } from "@tanstack/react-virtual";
        /* items.map((item) => item); <button>Bad</button>; LiveConceptIntent */
        export const harmless = "MobileTimelineItem";
      `,
      "src/live-concepts/stillwater/comments.css": `
        /* .concept-stillwater.sw-conversation-skin .composer { position: fixed; } */
        .concept-stillwater .sessions { overflow-y: auto; }
      `,
    });
    assert.equal(
      violations.find((violation) => violation.file.includes("comments.")),
      undefined,
      JSON.stringify(violations, null, 2),
    );
  });

  it("makes platformPresentation the only viewport listener and token writer", async () => {
    const violations = await violationsFor({
      "src/live-concepts/stillwater/viewport.ts": `
        window.visualViewport?.addEventListener("resize", update);
        root.style.setProperty("--viewport-height", height);
        root.style.setProperty("--keyboard-inset", inset);
      `,
      "src/ui/legacyViewport.ts": `
        viewport.visualViewport.addEventListener("scroll", update);
      `,
    });
    for (const detail of [
      "only src/ui/platformPresentation.ts may listen to visualViewport",
      "only src/ui/platformPresentation.ts may write --viewport-height",
      "only src/ui/platformPresentation.ts may write --keyboard-inset",
    ]) {
      assert.ok(
        violations.some(
          (violation) =>
            violation.code === "viewport-ownership" &&
            violation.detail === detail,
        ),
        JSON.stringify(violations, null, 2),
      );
    }
  });

  it("forbids --visual-viewport-height even in platformPresentation", async () => {
    const violations = await violationsFor({
      "src/ui/platformPresentation.ts": `${GOOD_PLATFORM}\nroot.style.setProperty("--visual-viewport-height", "1px");`,
    });
    expectViolation(violations, {
      code: "forbidden-viewport-token",
      file: "src/ui/platformPresentation.ts",
      detail: "--visual-viewport-height is forbidden",
    });
  });

  it("retains existing isolation, transport, and scoped CSS rules", async () => {
    const violations = await violationsFor({
      "src/live-concepts/rejected-fixture.ts":
        'import "../../../mobile-concepts/src/core/fixtures";',
      "src/live-concepts/rejected-symbol.ts": "type X = PrototypeState;",
      "src/live-concepts/rejected-transport.ts": "new WebSocket(url);",
      "src/live-concepts/rejected.css": "body { color: red; }",
      "src/live-concepts/accepted.css":
        ".concept-stillwater .sessions {}\n.live-conversation-frame__status {}",
    });
    assert.ok(
      violations.some((violation) => violation.code === "forbidden-import"),
    );
    assert.ok(
      violations.some((violation) => violation.code === "forbidden-symbol"),
    );
    assert.ok(
      violations.some((violation) => violation.code === "forbidden-transport"),
    );
    assert.ok(
      violations.some((violation) => violation.code === "unscoped-css"),
    );
    assert.equal(
      violations.find((violation) => violation.file.endsWith("accepted.css")),
      undefined,
    );
  });

  it("returns diagnostics sorted by exact path then rule", async () => {
    const violations = await violationsFor({
      "src/live-concepts/z.ts": "new WebSocket(url); PrototypeState;",
      "src/live-concepts/a.css": "body { color: red; }",
    });
    const keys = violations.map(
      (violation) =>
        `${violation.file}\0${violation.code}\0${violation.detail}`,
    );
    assert.deepEqual(keys, [...keys].sort());
  });
});
