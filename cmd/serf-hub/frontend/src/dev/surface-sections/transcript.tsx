// The transcript surface: a realistic turn rendered through the REAL
// TurnBlock skeleton (panes/session/transcript/TurnBlock.tsx), which is
// itself the thing that dispatches every item through the real item- and
// tool-renderer registries (types.ts/toolRenderers.ts) - so this section
// exercises the actual dispatch path, not a hand-picked subset of
// renderers. Fixture data only: a fabricated TurnModel/ItemModel[], no
// store, no network. TurnBlock's own side-effect imports
// ("./ToolCallItem", "./tools", "./messages" transitively) register every
// renderer the moment TurnBlock itself is imported, exactly the way a real
// SessionPane gets them.

import { TurnBlock } from "../../panes/session/transcript/TurnBlock";
import type { ItemModel, TurnModel } from "../../protocol/model";
import styles from "../gallery-section.module.css";
import { ThemeFlip } from "../ThemeFlip";

const SESSION_REF = "dev-surface-transcript";

function item(overrides: Partial<ItemModel>): ItemModel {
  return { id: "item", turnId: "turn_1", type: "commandExecution", text: "", status: "completed", ...overrides };
}

// A settled thought with two headed sections, so ThinkBlock's expanded body
// renders the new step rail (segmentReasoningTrace only splits on top-level
// markdown headings - see that function's own header comment) instead of the
// single-document fallback.
const thinkItem = item({
  id: "think_1",
  type: "reasoning",
  status: "completed",
  startedAt: "2026-08-13T15:04:00Z",
  completedAt: "2026-08-13T15:04:07Z",
  reasoningSummaries: [
    ["## Investigate\nThe prune test fails intermittently under `-race`. Reading `jobstore_test.go` around the "],
    ["mid-prune snapshot read - the tail replacement isn't atomic yet."],
    ["## Fix\nSwap the tail write for a rename so a concurrent reader always sees either the old or the new "],
    ["file, never a partial one."],
  ],
});

const userItem = item({
  id: "user_1",
  type: "userMessage",
  status: "completed",
  text: "Can you look at the flaky prune test and fix it?",
  startedAt: "2026-08-13T15:03:55Z",
  transcriptEntryIndex: 1,
});

const agentOpenerItem = item({
  id: "agent_1",
  type: "agentMessage",
  status: "completed",
  text: "Found it - the prune step wrote the output tail in place, so a reader mid-prune could see a half-written file. I switched it to an atomic rename.",
  startedAt: "2026-08-13T15:04:08Z",
});

const shellItem = item({
  id: "tool_shell_1",
  toolName: "shell",
  status: "completed",
  callId: "call_shell_1",
  argumentsJSON: JSON.stringify({ command: "go test ./internal/jobstore/... -run TestPrune -race -count=5" }),
  output: "ok  \tprime-radiant-inc/serf/internal/jobstore\t2.104s\n",
});

const readItem = item({
  id: "tool_read_1",
  toolName: "read_file",
  status: "completed",
  callId: "call_read_1",
  argumentsJSON: JSON.stringify({ file_path: "internal/jobstore/prune.go", offset: 40, limit: 12 }),
  output:
    "func (s *Store) prune(ctx context.Context) error {\n\ttail, err := s.buildTail(ctx)\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn os.Rename(tail, s.tailPath())\n}\n",
});

// A subagent module row (the delegate descriptor, subagentModule.tsx):
// completed, so the module renders its finished summary rather than a live
// cadence dot.
const delegateItem = item({
  id: "tool_delegate_1",
  toolName: "delegate",
  status: "completed",
  callId: "call_delegate_1",
  description: "Write a regression test for the mid-prune race",
  argumentsJSON: JSON.stringify({ task: "Write a regression test for the mid-prune race" }),
  output: JSON.stringify({
    job_id: "job_dev_1",
    status: "completed",
    transcript_ref: "ref_dev_child_1",
    summary: "Added TestPruneConcurrentSnapshotRead, which fails without the atomic-rename fix.",
  }),
});

// A completed-but-unanswered ask_user call: the read-only transcript card
// (tools/askUser.tsx). The composer section seeds the SAME shape into
// threadsStore so AskDock's interactive answering dock can be shown too -
// this card is the honest read-only twin that lives in the transcript.
const askItem = item({
  id: "tool_ask_1",
  toolName: "ask_user",
  status: "completed",
  callId: "call_ask_1",
  argumentsJSON: JSON.stringify({
    questions: [
      {
        header: "Backport?",
        question: "Should this fix also land on the release/1.4 branch?",
        options: [
          { label: "Yes, backport", detail: "Cherry-pick once main is green.", recommended: true },
          { label: "No, main only", detail: "The release branch doesn't hit this path." },
        ],
      },
    ],
  }),
});

const agentContinuationItem = item({
  id: "agent_2",
  type: "agentMessage",
  status: "completed",
  text: "I'll wait for your call on the backport before opening a PR.",
  startedAt: "2026-08-13T15:04:20Z",
});

const turn: TurnModel = {
  id: "turn_1",
  status: "completed",
  startedAt: "2026-08-13T15:03:55Z",
  completedAt: "2026-08-13T15:04:20Z",
  items: [userItem, thinkItem, agentOpenerItem, shellItem, readItem, delegateItem, askItem, agentContinuationItem],
};

const EXCHANGE_OPENERS = new Set(["agent_1"]);

export default function TranscriptSurfaceSection() {
  return (
    <section>
      <h2>Transcript</h2>
      <p className={styles.note}>
        One turn: a user message, a settled thought with a headed (step-rail) body, a shell call, a file read, a
        finished subagent module, an unanswered ask_user card, and a follow-up agent message. Rendered through the real
        TurnBlock skeleton with a fabricated TurnModel - no store, no network.
      </p>
      <ThemeFlip>
        <TurnBlock turn={turn} sessionRef={SESSION_REF} exchangeOpeners={EXCHANGE_OPENERS} agentLabel="claude" />
      </ThemeFlip>
    </section>
  );
}
