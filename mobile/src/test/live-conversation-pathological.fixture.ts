/**
 * Pathological conversation fixtures for live conversation frame browser and
 * simulator evidence.
 *
 * These fixtures exist to exercise the shared frame's hard geometry bounds:
 * exactly 39 items (just under the 48-row mount cap) and 500 variable-height
 * items (well past the 48-row cap and 49-row evidence virtualization threshold).
 * The 44,700-byte raw system prelude is a deterministic sentinel whose
 * complete absence from DOM/AX/artifacts is asserted by every browser and
 * simulator gate.
 *
 * No credential, raw URL, or token is ever held in fixture state. Every text
 * field is deterministic plain text; fixture output is reproducible across
 * runs.
 */

import type {
  MobileCapabilities,
  MobileConversation,
  MobileTimelineItem,
  MobileUsage,
} from "../conversation/model";

export interface PathologicalConversationFixture {
  readonly conversation: MobileConversation;
  readonly rawSystemPrelude: string;
  readonly rawSystemSentinel: string;
}

const ALL_TRUE_CAPS: MobileCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  rename: true,
};

const SAMPLE_USAGE: MobileUsage = {
  inputTokens: 12500,
  outputTokens: 8200,
  cacheReadTokens: 3100,
  totalTokens: 23800,
  cost: "$0.042",
  contextUsed: 41000,
  contextWindow: 200000,
  contextRemaining: 159000,
  contextPressure: 0.21,
};

const SYSTEM_PRELUDE_BYTES = 44_700;

/**
 * Raw system prelude: exactly 44,700 UTF-8 bytes of deterministic sentinel
 * text. This is the value whose complete absence from the DOM, accessibility
 * tree, and every artifact is asserted. It must never be rendered or exposed.
 */
function buildRawSystemPrelude(): string {
  // A deterministic, repeating sentinel. Each line is a fixed width so the
  // total byte length is exact and reproducible. We build with ASCII so UTF-8
  // byte length equals character count.
  const line = "S".repeat(89); // 89 chars/bytes
  const fullLines = Math.floor(SYSTEM_PRELUDE_BYTES / line.length); // 502 lines
  const remainder = SYSTEM_PRELUDE_BYTES - fullLines * line.length; // 700 bytes
  let prelude = "";
  for (let i = 0; i < fullLines; i++) {
    prelude += line;
  }
  prelude += "S".repeat(remainder);
  if (prelude.length !== SYSTEM_PRELUDE_BYTES) {
    throw new Error(
      `raw system prelude length ${prelude.length} != ${SYSTEM_PRELUDE_BYTES}`,
    );
  }
  return prelude;
}

const RAW_SYSTEM_SENTINEL = "EVENER_FIXTURE_SYSTEM_PRELUDE_SENTINEL";

function buildTimelineItems(count: number): MobileTimelineItem[] {
  const items: MobileTimelineItem[] = [];
  // First item is always a system-prelude notice — the projection surface that
  // must never expose rawSystemPrelude. The notice text is a bounded redacted
  // label, never the raw prelude.
  items.push({
    kind: "notice",
    id: "fixture-system-prelude",
    origin: "system",
    family: "system-prelude",
    tone: "system",
    text: "System instructions applied",
  });
  for (let i = 1; i < count; i++) {
    const phase = i % 9;
    if (phase === 0) {
      // reasoning activity
      items.push({
        kind: "activity",
        id: `fixture-reasoning-${i}`,
        label: `Reasoning ${i}`,
        family: "reasoning",
        state: "completed",
        detail: { arguments: `reasoning trace ${i}` },
      });
    } else if (phase === 4) {
      // tool activity with variable-height output
      items.push({
        kind: "activity",
        id: `fixture-tool-${i}`,
        label: `read_file ${i}`,
        family: "tool",
        state: "completed",
        detail: {
          output: `safe fixture output ${i} ${"variable-height ".repeat(i % 7)}`,
          callId: `call-${i}`,
        },
      });
    } else if (phase === 5) {
      // user message
      items.push({
        kind: "user",
        id: `fixture-user-${i}`,
        text: `Fixture user message ${i}`,
      });
    } else if (phase === 6) {
      // question
      items.push({
        kind: "question",
        id: `fixture-question-${i}`,
        batch: {
          callId: `ask-${i}`,
          questions: [
            {
              key: `ask-${i}:0`,
              header: `Question ${i}`,
              question: `Continue with step ${i}?`,
              options: [
                { label: "Yes", detail: "Proceed" },
                { label: "No", detail: "Stop" },
              ],
              multiSelect: false,
            },
          ],
        },
      });
    } else if (phase === 7) {
      // warning notice
      items.push({
        kind: "notice",
        id: `fixture-warning-${i}`,
        origin: "steering",
        family: "warning",
        tone: "warning",
        text: `Loop guard ${i}`,
      });
    } else {
      // assistant markdown with variable height
      const height = (i % 7) + 1;
      items.push({
        kind: "assistant",
        id: `fixture-assistant-${i}`,
        markdown: `Fixture response ${i} ${"line ".repeat(height)}`,
        streaming: false,
      });
    }
  }
  return items;
}

function buildConversation(
  count: number,
  id: string,
  preview: string,
): MobileConversation {
  return {
    id,
    sessionId: id,
    name: `Pathological ${count}`,
    preview,
    modelProvider: "simulator-scripted",
    status: "idle",
    items: buildTimelineItems(count),
    capabilities: ALL_TRUE_CAPS,
    queue: { depth: 0, preview: [] },
    usage: SAMPLE_USAGE,
    reasoningEffort: "medium",
    reasoningEffortLevels: ["low", "medium", "high"],
    supportsReasoning: true,
    askPending: false,
  };
}

export function makePathological39ItemFixture(): PathologicalConversationFixture {
  return {
    conversation: buildConversation(
      39,
      "simulator-pathological-39",
      "Simulator fixture with 39 items",
    ),
    rawSystemPrelude: buildRawSystemPrelude(),
    rawSystemSentinel: RAW_SYSTEM_SENTINEL,
  };
}

export function makeVariableHeight500ItemFixture(): PathologicalConversationFixture {
  return {
    conversation: buildConversation(
      500,
      "simulator-variable-500",
      "Simulator fixture with 500 items",
    ),
    rawSystemPrelude: buildRawSystemPrelude(),
    rawSystemSentinel: RAW_SYSTEM_SENTINEL,
  };
}
