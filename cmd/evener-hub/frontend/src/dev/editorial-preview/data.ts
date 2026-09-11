import type { EvenerDelegateInfo, ThreadItem, ThreadReadResponse } from "../../protocol/types.gen";

export const PARENT = "local:editorial-parent";
export const CHILD = "local:editorial-child";
export const QUESTION = "local:editorial-question";
export const RESUMED = "local:editorial-resumed";
export const GRANDCHILD = "local:editorial-grandchild";
export const GREAT_GRANDCHILD = "local:editorial-great-grandchild";
export const parentRefs: Record<string, string> = {
  [CHILD]: PARENT,
  [RESUMED]: PARENT,
  [GRANDCHILD]: CHILD,
  [GREAT_GRANDCHILD]: GRANDCHILD,
};
const TURN = "editorial-evidence";
const capabilities = {
  send: true,
  steer: true,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: false,
};
export const summaries = [
  { ref: PARENT, title: "Editorial fixture parent", kind: "session", state: "idle", live: true, children: [] },
  { ref: CHILD, title: "Editorial fixture child", kind: "subagent", state: "idle", live: true, children: [] },
  { ref: QUESTION, title: "Editorial fixture question", kind: "session", state: "awaiting", live: true, children: [] },
  { ref: RESUMED, title: "Editorial fixture resumed", kind: "subagent", state: "active", live: true, children: [] },
  { ref: GRANDCHILD, title: "Editorial fixture grandchild", kind: "subagent", state: "idle", live: true, children: [] },
  {
    ref: GREAT_GRANDCHILD,
    title: "Editorial fixture great-grandchild",
    kind: "subagent",
    state: "idle",
    live: true,
    children: [],
  },
];
const tool = (
  id: string,
  toolName: string,
  description: string,
  status: string,
  args: object,
  output?: string,
): ThreadItem => ({
  id,
  turnId: TURN,
  type: "commandExecution",
  callId: `call-${id}`,
  toolName,
  description,
  status,
  argumentsJson: JSON.stringify(args),
  ...(output === undefined ? {} : { output }),
});
const nestedDelegates: EvenerDelegateInfo[] = Object.entries(parentRefs)
  .filter(([ref]) => ref === GRANDCHILD || ref === GREAT_GRANDCHILD)
  .map(([ref, parentRef]) => ({
    delegateId: `dlg_${ref.slice(6).replaceAll("-", "_")}`,
    ownerSessionId: parentRef.slice(6),
    rootSessionId: PARENT.slice(6),
    childSessionId: ref.slice(6),
    transcriptRef: ref,
    type: "delegate",
    lifecycle: "idle",
    phase: "idle",
    status: "idle",
    outcome: "completed",
    terminal: true,
    resumable: true,
    needsAttention: false,
    projectionRevision: 1,
  }));
const delegates: EvenerDelegateInfo[] = [
  {
    delegateId: "dlg_editorial_report",
    ownerSessionId: "editorial-parent",
    rootSessionId: "editorial-parent",
    childSessionId: "editorial-child",
    transcriptRef: CHILD,
    type: "delegate",
    lifecycle: "idle",
    phase: "idle",
    status: "idle",
    outcome: "completed",
    terminal: true,
    resumable: true,
    needsAttention: false,
    projectionRevision: 2,
  },
  {
    delegateId: "dlg_editorial_resumed",
    ownerSessionId: "editorial-parent",
    rootSessionId: "editorial-parent",
    childSessionId: "editorial-resumed",
    transcriptRef: RESUMED,
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    outcome: "completed",
    terminal: false,
    resumable: false,
    needsAttention: false,
    projectionRevision: 3,
  },
];
const parentItems: ThreadItem[] = [
  {
    id: "parent-user",
    turnId: TURN,
    type: "userMessage",
    text: "Review the evidence and collaborators. This is a deterministic frontend fixture; no command or provider is executed.",
  },
  { id: "parent-analysis", turnId: TURN, type: "agentMessage", text: "Parent analysis: fixture-only evidence." },
  tool(
    "success",
    "shell",
    "Verifying the fixture's successful check",
    "completed",
    { command: "printf 'fixture check passed\\n'" },
    "fixture check passed\n[exit 0]",
  ),
  tool(
    "failure",
    "shell",
    "Inspecting a deliberately failed check",
    "failed",
    { command: "fixture-check --fail" },
    "AssertionError: expected aligned evidence columns\n[exit 1]",
  ),
  tool(
    "long",
    "read_file",
    "Reading long native source evidence",
    "completed",
    { file_path: "/fixture/long-evidence.ts" },
    Array.from(
      { length: 120 },
      (_, i) =>
        `${i + 1}\tconst fixtureLine${i + 1} = "${i === 30 ? "long-path/".repeat(90) : "retained native source evidence"}";`,
    ).join("\n"),
  ),
  tool("active", "shell", "Observing active fixture work (not live execution)", "inProgress", {
    command: "fixture-check --wait",
  }),
  {
    id: "parent-continuation",
    turnId: TURN,
    type: "agentMessage",
    text: "The failed check remains distinct from successful invocation. Current collaborator state comes from the owner projection below.",
  },
  tool(
    "reported",
    "delegate",
    "Inspect the independent child transcript",
    "completed",
    { prompt: "Inspect the independent child transcript" },
    JSON.stringify({ delegate_id: "dlg_editorial_report", status: "running", transcript_ref: CHILD }),
  ),
  tool(
    "resumed",
    "delegate",
    "Continue the same collaborator after its previous report",
    "completed",
    { prompt: "Continue the same collaborator after its previous report" },
    JSON.stringify({ delegate_id: "dlg_editorial_resumed", status: "completed", transcript_ref: RESUMED }),
  ),
  tool(
    "unknown",
    "delegate",
    "Inspect unavailable collaborator state without guessing",
    "completed",
    { prompt: "Inspect unavailable collaborator state without guessing" },
    JSON.stringify({ delegate_id: "dlg_editorial_unknown", status: "running" }),
  ),
];

export function initialThreads(): Map<string, ThreadReadResponse> {
  return new Map(
    summaries.map((summary) => {
      const ref = summary.ref;
      const owned = nestedDelegates.filter((delegate) => delegate.ownerSessionId === ref.slice(6));
      const items: ThreadItem[] =
        ref === PARENT
          ? parentItems
          : ref === QUESTION
            ? [
                {
                  id: "question-intro",
                  type: "agentMessage",
                  turnId: TURN,
                  text: "Fixture question: choose a review scope. Your answer stays in this browser.",
                },
                tool("question", "ask_user", "Choose a fixture review scope", "completed", {
                  questions: [
                    {
                      header: "Review scope",
                      question: "Which fixture evidence should we review?",
                      options: [
                        { label: "Tool evidence", detail: "Review retained output" },
                        { label: "Collaborators", detail: "Review current lifecycle" },
                      ],
                    },
                  ],
                }),
              ]
            : [
                {
                  id: `message-${ref}`,
                  type: "agentMessage",
                  turnId: TURN,
                  text:
                    ref === CHILD
                      ? "Child report: independent transcript, not the parent snapshot."
                      : ref === GRANDCHILD
                        ? "Grandchild report: evidence authored by the independent grandchild."
                        : ref === GREAT_GRANDCHILD
                          ? "Great-grandchild report: deeper independent evidence."
                          : "Previous report: first review completed. Resumed work is now checking the same evidence again.",
                },
                ...owned.map((delegate) =>
                  tool(
                    `nested-${delegate.childSessionId}`,
                    "delegate",
                    `Inspect ${delegate.childSessionId} independently`,
                    "completed",
                    { prompt: `Inspect ${delegate.childSessionId} independently` },
                    JSON.stringify({
                      delegate_id: delegate.delegateId,
                      status: "completed",
                      transcript_ref: delegate.transcriptRef,
                    }),
                  ),
                ),
              ];
      return [
        ref,
        {
          thread: {
            id: ref.slice(6),
            sessionId: ref.slice(6),
            name: summary.title,
            preview: "Deterministic fixture only — no live hub or provider",
            ephemeral: false,
            modelProvider: "fixture/scripted",
            createdAt: 1000,
            updatedAt: 1000,
            status: { type: ref === QUESTION || ref === RESUMED ? "active" : "idle" },
            cwd: "/fixture/editorial",
            cliVersion: "fixture",
            source: "local",
            evener: {
              ref,
              instanceId: `instance-${ref}`,
              mutationStateAuthoritative: true,
              capabilities,
              ...(parentRefs[ref] ? { parentRef: parentRefs[ref], kind: "subagent" } : {}),
              ...(ref === PARENT ? { diagnostics: { delegates } } : {}),
              ...(owned.length ? { diagnostics: { delegates: owned } } : {}),
              queue: { revision: 0, depth: 0 },
              ...(ref === QUESTION ? { askPending: true } : {}),
            },
            turns: [
              {
                id: TURN,
                itemsView: "full",
                startedAt: 1700000000000,
                completedAt: ref === QUESTION ? undefined : 1700000030000,
                status: ref === QUESTION ? "inProgress" : "completed",
                items: structuredClone(items).map((item, index) => ({
                  ...item,
                  startedAt: 1700000000000 + index * 1000,
                })),
              },
            ],
          },
        },
      ];
    }),
  );
}
