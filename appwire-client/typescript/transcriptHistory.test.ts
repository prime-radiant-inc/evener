import { expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "./model";
import { itemAt, turnAt } from "./testing/modelAccessors";
import {
  applyHistory,
  CAPABILITIES,
  hydrateWithShaImage,
  SHA_IMAGE,
  testEscalation,
  testHydrate,
  testHydrateV6,
  testThread,
} from "./testing/reducerTestKit";
import {
  applyReadModel,
  applyReadResponse,
  hydrateThread,
  mergeTranscriptItemVersioned,
  mergeTranscriptTurnVersioned,
} from "./transcriptHistory";
import { markItemTextOmitted } from "./transcriptItems";
import type { ThreadReadResponse } from "./types.gen";

test("versioned entry merge rejects older input and preserves richer equal-version text", () => {
  const held: ItemModel = { id: "item-1", turnId: "turn-1", type: "assistant", text: "known text" };
  const stale: ItemModel = { ...held, text: "stale text" };
  const omitted = markItemTextOmitted({ ...held, text: "" });

  expect(mergeTranscriptItemVersioned(held, 4, stale, 3)).toBe(held);
  expect(mergeTranscriptItemVersioned(held, 4, omitted, 4)).toMatchObject({ text: "known text" });
});

test("versioned turn merge rejects older and equal metadata without copying item arrays", () => {
  const item: ItemModel = { id: "item-1", turnId: "turn-1", type: "assistant", text: "body" };
  const held: TurnModel = { id: "turn-1", status: "completed", version: 8, items: [item] };
  expect(mergeTranscriptTurnVersioned(held, { ...held, status: "stale", version: 7, items: [] })).toBe(held);
  expect(mergeTranscriptTurnVersioned(held, { ...held, status: "equal", items: [] })).toBe(held);
  const newer = mergeTranscriptTurnVersioned(held, { ...held, status: "new", version: 9, items: [] });
  expect(newer).toMatchObject({ status: "new", version: 9 });
  expect(newer.items).toBe(held.items);
});

test("hydrateThread carries the snapshot plugin diagnostics into ThreadModel", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: {},
      diagnostics: {
        plugins: [
          { name: "enabled", skillCount: 1, agentCount: 0, hookCount: 0, mcpCount: 0 },
          { name: "another", skillCount: 0, agentCount: 1, hookCount: 0, mcpCount: 0 },
        ],
      },
    },
  });

  expect(model.diagnostics?.plugins?.map((plugin) => plugin.name)).toEqual(["enabled", "another"]);
});

test("hydrateThread preserves an explicit empty plugin inventory", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: {},
      diagnostics: { plugins: [] },
    },
  });

  expect(model.diagnostics?.plugins).toEqual([]);
});

test("hydrateThread leaves diagnostics unavailable when the wire omits them", () => {
  expect(testHydrate().diagnostics).toBeUndefined();
});

test("hydrateThread retains and copies the owning command inventory, including authoritative empty", () => {
  const commands = [{ name: "project-only", source: "project", description: "owner" }];
  const model = testHydrate({ evener: { diagnostics: { commands } } });
  expect(model.diagnostics?.commands).toEqual(commands);
  commands[0]!.description = "changed source";
  expect(model.diagnostics?.commands?.[0]?.description).toBe("owner");
  expect(testHydrate({ evener: { diagnostics: { commands: [] } } }).diagnostics?.commands).toEqual([]);
  expect(testHydrate({ evener: { diagnostics: {} } }).diagnostics?.commands).toBeUndefined();
});

test("hydrateThread retains canonical skill descriptors", () => {
  const model = testHydrate({
    evener: {
      diagnostics: {
        skills: [
          {
            name: "plugin:simplify",
            description: "rewrite",
            disableModelInvocation: false,
            userInvocable: true,
            available: true,
          },
        ],
      },
    },
  });
  expect(model.skills).toEqual([
    {
      name: "plugin:simplify",
      description: "rewrite",
      disableModelInvocation: false,
      userInvocable: true,
      available: true,
    },
  ]);
});

test("hydrateThread defaults missing skills and copies wire descriptors", () => {
  expect(testHydrate().skills).toEqual([]);

  const skills = [
    {
      name: "plugin:simplify",
      description: "rewrite",
      disableModelInvocation: false,
      userInvocable: true,
      available: true,
    },
  ];
  const model = testHydrate({ evener: { diagnostics: { skills } } });
  expect(model.skills).not.toBe(skills);
  expect(model.skills?.[0]).not.toBe(skills[0]);
});

test("stable delegate diagnostics preserve lossless fields and omit call-scoped wait reasons", () => {
  const thread = testThread();
  (thread.evener as unknown as Record<string, unknown>).diagnostics = {
    delegates: [
      {
        delegateId: "dlg_lossless",
        ownerSessionId: "sess_t",
        rootSessionId: "sess_t",
        childSessionId: "sess_child",
        transcriptRef: "local:sess_child",
        parentDelegateId: "dlg_parent",
        type: "delegate",
        lifecycle: "idle",
        phase: "idle",
        status: "idle",
        outcome: "exhausted",
        reason: "turn_budget_exhausted",
        terminal: true,
        resumable: false,
        notResumableReason: "turn_budget_exhausted",
        projectionRevision: 9,
        task: "Inspect the repo",
        description: "repository inspection",
        agentType: "explorer",
        requestedModel: "fast",
        resolvedProfileId: "anthropic",
        resolvedModel: "claude-sonnet",
        model: "anthropic/claude-sonnet",
        reasoningEffort: "high",
        originTurnId: "turn_1",
        originToolCallId: "call_1",
        originItemId: "item_1",
        runStartedAt: "2026-08-15T10:00:00Z",
        runEndedAt: "2026-08-15T10:01:00Z",
        latestActivityAt: "2026-08-15T10:00:59Z",
        runningForMs: null,
        quietForMs: 1000,
        durationMs: 60000,
        packetKind: "communicate",
        message: null,
        structuredResult: null,
        structuredResultValid: true,
        structuredResultReason: "explicit null",
        warnings: ["warning one"],
        diagnostics: ["observer armed"],
        exhaustionBudget: "turns",
        exhaustionLimit: 12,
        exhaustionResumable: false,
        delegationAllowance: 2,
        parentWatchGranted: true,
        usage: { inputTokens: 41, outputTokens: 7, cacheReadTokens: 3, totalTokens: 48 },
        worktree: { path: "/tmp/wt", branch: "delegate/dlg_lossless", headSha: "abc", ahead: 2, dirty: true },
        waitIgnoredReason: "must never enter stable state",
      },
    ],
    turnSlots: { inUse: 1, cap: 4, jobs: 2, driveTurns: 1 },
  };

  const model = hydrateThread({ thread }, thread.evener.ref, 1000) as ThreadModel & {
    delegates?: Array<Record<string, unknown>>;
    turnSlots?: Record<string, unknown>;
  };

  expect(model.delegates).toHaveLength(1);
  expect(model.delegates?.[0]).toMatchObject({
    delegateId: "dlg_lossless",
    message: null,
    structuredResult: null,
    structuredResultValid: true,
    structuredResultReason: "explicit null",
    exhaustionBudget: "turns",
    exhaustionLimit: 12,
    exhaustionResumable: false,
    runningForMs: null,
    quietForMs: 1000,
    durationMs: 60000,
    usage: { inputTokens: 41, outputTokens: 7, cacheReadTokens: 3, totalTokens: 48 },
    worktree: { path: "/tmp/wt", branch: "delegate/dlg_lossless", headSha: "abc", ahead: 2, dirty: true },
    warnings: ["warning one"],
    diagnostics: ["observer armed"],
    delegationAllowance: 2,
    parentWatchGranted: true,
  });
  expect(model.delegates?.[0]).not.toHaveProperty("waitIgnoredReason");
  expect(model.turnSlots).toEqual({ inUse: 1, cap: 4, jobs: 2, driveTurns: 1 });
});

// transcriptEntryIndex is the item's 1-based position in the parent
// transcript's entry list (appwire.ThreadItem.TranscriptEntryIndex), and it is
// the ONLY field that names a fork divergence position: thread/fork's
// sourceTurnId is read as that index, while a LIVE turn id is numbered off a
// different counter entirely. The model must therefore carry it through
// verbatim rather than leaving renderers to reach for turnId.
test("wire items carry transcriptEntryIndex into the model - it is what thread/fork's divergence position is read from", () => {
  let model = testHydrateV6();
  model = applyHistory(
    model,
    {
      turns: [{ id: "turn_2", status: "inProgress", itemsView: "" }],
    },
    1001,
  );
  model = applyHistory(
    model,
    {
      items: [
        {
          ...{
            type: "userMessage",
            id: "item_user",
            turnId: "turn_2",
            transcriptEntryIndex: 5,
            text: "second task",
            status: "completed",
          },
          turnId: "turn_2",
        },
      ],
    },
    1002,
  );
  expect(itemAt(turnAt(model, 0), 0).transcriptEntryIndex).toBe(5);

  // Absent on the wire stays absent in the model: a missing index means "this
  // entry has no persisted transcript position", never entry 0.
  const hydrated = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [{ id: "item_a", turnId: "turn_1", type: "userMessage", text: "hello" }],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );
  expect(itemAt(turnAt(hydrated, 0), 0).transcriptEntryIndex).toBeUndefined();
});

test("hydrateThread resolves a sha-bearing image without a stamped url to the serving session's image route", () => {
  const model = hydrateWithShaImage();
  expect(model.imageSessionId).toBe("sess_t");
  expect(itemAt(turnAt(model, 0), 0).images).toEqual([
    { src: `/s/sess_t/images/${SHA_IMAGE}`, name: "photo.png", path: undefined },
  ]);
});

test("hydrateThread trims a whitespace-padded sessionId and falls back to the trimmed thread id", () => {
  // stampThreadImageURLs (output_images.go) trims both (strings.TrimSpace):
  // a padded-but-blank "  " session id must fall back to the thread id, and
  // a clean thread id must survive — neither may escape to a /s/%20... route.
  const blankPadded = testHydrate({ id: "thr_t", sessionId: "   " });
  expect(blankPadded.imageSessionId).toBe("thr_t");
  const padded = testHydrate({ id: "  thr_t  ", sessionId: "  sess_t  " });
  expect(padded.imageSessionId).toBe("sess_t");
});

// A hydrate reads an empty input-images list the same way a live frame does: as
// absence. The wire rarely sends one — `appwire.ThreadItem.Images` is
// `json:",omitempty"` and the producers send nil — but a real fixture does
// (`fixtures/tool-and-jobs.jsonl:4`), and folding it to absent is what keeps an
// older page's images from being erased on merge.
test("an empty input-images list on the wire leaves the item's images unset", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "default",
        items: [
          {
            id: "user-item",
            turnId: "turn_1",
            type: "userMessage",
            text: "look",
            status: "completed",
            images: [],
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).images).toBeUndefined();
});

test("hydrateThread maps a settled item's error onto the model (snapshot path)", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_tool",
            turnId: "turn_1",
            toolName: "run_tests",
            callId: "call_1",
            error: "exit status 1",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).error).toBe("exit status 1");
});

test("hydrateThread maps a settled item's exitCode onto the model (snapshot path)", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            type: "commandExecution",
            id: "item_tool",
            turnId: "turn_1",
            toolName: "shell",
            callId: "call_1",
            output: "ok",
            exitCode: 0,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  // A real typed 0 must round-trip as 0, never collapse to undefined — the
  // descriptor distinguishes "ran, exit 0" from "no code (backgrounded)".
  expect(itemAt(turnAt(model, 0), 0).exitCode).toBe(0);
});

// A tool-call's intent crosses the wire as ThreadItem.description (set
// server-side, e.g. delegate's mandate); wireItemToModel historically dropped
// it. The model must carry it so the subagent Activity feed can render each
// child tool-call's intent (§4.2). Both hydrate and live paths fold through
// wireItemToModel, so the snapshot path proves the carry.
test("wireItemToModel carries the wire description (tool-call intent) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "delegate",
            callId: "c1",
            description: "audit the reducer",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).description).toBe("audit the reducer");
});

// systemMessage items carry a stable typed discriminator, ThreadItem.eventKind
// (appwire.ThreadItemEventKind*), naming what happened — "system_prompt",
// "compaction", etc. wireItemToModel historically dropped it, forcing the
// transcript renderer to guess scaffold items from their char count. The model
// must carry it so classification is by wire type, not a heuristic (kata ckgw).
test("wireItemToModel carries the wire eventKind (scaffold/system discriminator) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_system",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_system_prompt",
            type: "systemMessage",
            text: "You are Evener.",
            eventKind: "system_prompt",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).eventKind).toBe("system_prompt");
});

// A system item can attach structured detail behind its prose text, e.g. a
// round_timings item's per-phase durations (ThreadItem.raw; kata 7zkv) or a
// compaction item's before/after counts. wireItemToModel historically dropped
// it, forcing a renderer to re-parse numbers out of human-readable text. The
// model must carry it so a renderer can read the real numbers instead.
test("wireItemToModel carries the wire raw (structured system-item detail) onto the item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_timings",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_round_timings_1",
            type: "systemMessage",
            text: "Round 0 total=1.5s llm=1.2s",
            eventKind: "round_timings",
            raw: { roundTimings: { round: 0, total_round_ns: 1_500_000_000, llm_call_ns: 1_200_000_000 } },
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).raw).toEqual({
    roundTimings: { round: 0, total_round_ns: 1_500_000_000, llm_call_ns: 1_200_000_000 },
  });
});

// A reloaded transcript must label a steer the same way the live one did.
// internal/apptranscript persists ThreadItem.steeringKind alongside the
// steering item (Task 2); wireItemToModel must carry it through on the
// snapshot path exactly as it does for description/eventKind/raw above, or
// the label would work live and vanish on refresh.
test("a reloaded steering item carries steeringKind from the snapshot", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_0",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_steering_0",
            type: "steering",
            text: "done",
            steeringKind: "tasks-done",
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);
  expect(itemAt(turnAt(model, 0), 0).steeringKind).toBe("tasks-done");
});

// On reload, apptranscript.TurnsFromFile mints one wire turn per transcript
// entry, so a tool CALL (assistant entry) and its RESULT (tool-results entry)
// arrive as two items sharing a callId, with different ids, in separate turns.
// The Go contract says "the client merges the two by call id"; the reducer must
// collapse them into the single item the live path already produces, and drop
// the now-empty result turn so its TurnSeparator disappears. (zrzr)
test("reload merges a tool CALL and its RESULT (separate turns, same callId) into one item", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "shell",
            callId: "call_A",
            argumentsJson: JSON.stringify({ command: "make test" }),
            startedAt: 1,
            status: "inProgress",
          },
        ],
      },
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_result_2_0",
            type: "commandExecution",
            toolName: "shell",
            callId: "call_A",
            output: "ok",
            exitCode: 0,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 0);

  // Exactly one tool item survives, carrying both halves.
  const items = model.turns.flatMap((t) => t.items).filter((i) => i.callId === "call_A");
  expect(items).toHaveLength(1);
  const merged = items[0];
  if (!merged) throw new Error("expected merged item");
  expect(merged.id).toBe("item_tool_1_0"); // keeps the CALL id
  expect(merged.argumentsJSON).toBe(JSON.stringify({ command: "make test" })); // from the CALL
  expect(merged.output).toBe("ok"); // from the RESULT
  expect(merged.exitCode).toBe(0); // from the RESULT
  expect(merged.status).toBe("completed"); // settled from the RESULT
  expect(merged.startedAt).toBeTruthy(); // carried from the CALL half
  expect(merged.completedAt).toBeTruthy(); // carried from the RESULT half

  // The now-empty result turn is gone, so only one turn (and one separator) remains.
  expect(model.turns).toHaveLength(1);
});

test("reload merges a tool RESULT's raw state into the CALL item (hydration preserves structured raw)", () => {
  const delegateRaw = { id: "dlg_42", type: "delegate", status: "running", task: "do work" };
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_1_0",
            type: "commandExecution",
            toolName: "job_status",
            callId: "call_B",
            argumentsJson: JSON.stringify({ target: "dlg_42" }),
            startedAt: 1,
            status: "inProgress",
          },
        ],
      },
      {
        id: "turn_2",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_tool_result_2_0",
            type: "commandExecution",
            toolName: "job_status",
            callId: "call_B",
            output: JSON.stringify(delegateRaw),
            raw: delegateRaw,
            completedAt: 2,
            status: "completed",
          },
        ],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 0);
  const items = model.turns.flatMap((t) => t.items).filter((i) => i.callId === "call_B");
  expect(items).toHaveLength(1);
  expect(items[0]?.raw).toEqual(delegateRaw); // raw from the RESULT survives the merge
});

test("hydrateThread carries visionModel and defaults an absent wire value", () => {
  expect(testHydrate().visionModel).toBe("");
  expect(
    testHydrate({
      evener: {
        ref: "ref_t",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        visionModel: "anthropic/claude-haiku-4-5",
      },
    }).visionModel,
  ).toBe("anthropic/claude-haiku-4-5");
});

// S15: the session's sandbox mode and network setting ride the read, for the
// Session sheet's Access section. Absent (an older daemon or hub) stays
// absent: the phone leaves the section out rather than guess.
test("hydrateThread carries the session's access and leaves an absent one out", () => {
  expect(testHydrate().access).toBeUndefined();
  expect(
    testHydrate({
      evener: {
        ref: "ref_t",
        capabilities: CAPABILITIES,
        queue: { revision: 0 },
        access: { sandbox: "workspace-write", network: false },
      },
    }).access,
  ).toEqual({ sandbox: "workspace-write", network: false });
});

// Observed reasoning timing: the wire never carries reasoning timestamps at
// all (neither the live projector nor the historical reader sets
// ThreadItem.StartedAt/CompletedAt for reasoning items), so the reducer
// stamps its own client-observed arrival times from `now` — see
// ItemModel.observedStartedAt/observedCompletedAt's doc comment in
// model.ts and the reducer's appendReasoningDelta/mergeObservedTiming/
// settleItem comments for the full rationale.

test("hydrated items never carry observed timing fields", () => {
  const thread = testThread({
    turns: [
      {
        id: "turn_1",
        status: "completed",
        itemsView: "full",
        items: [{ type: "reasoning", id: "item_r", turnId: "turn_1", text: "done thinking", status: "completed" }],
      },
    ],
  });
  const model = hydrateThread({ thread }, thread.evener.ref, 1000);

  const item = itemAt(turnAt(model, 0), 0);
  expect(item.observedStartedAt).toBeUndefined();
  expect(item.observedCompletedAt).toBeUndefined();
});

// pendingEscalations (M7): appwire/types_thread.go's ThreadEvener.PendingEscalations
// doc comment calls it the "surface-on-entry snapshot ... so a client
// entering / reconnecting to / not-having-seen-live this session surfaces
// the card(s)" and rules it a HUMAN-CLIENT field only, never part of the
// transcript. hydrateThread must therefore carry it verbatim (or default it
// to [], per the Go wire-nullable-array rule: omitempty absent means empty)
// as a THREAD-level ThreadModel field, not a turn item.

test("hydrateThread maps evener.pendingEscalations verbatim into pendingEscalations", () => {
  const escalation = testEscalation();
  const model = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, pendingEscalations: [escalation] },
  });
  expect(model.pendingEscalations).toEqual([escalation]);
});

test("hydrateThread defaults pendingEscalations to an empty array when evener.pendingEscalations is absent", () => {
  const model = testHydrate();
  expect(model.pendingEscalations).toEqual([]);
});

// EvenerThread.Cost is the session-level estimated dollar total (the sibling of
// per-turn Turn.Cost), snapshot-authoritative like usage/workMillis: only a
// wire snapshot (hydrateThread) sets it, and everything else preserves it via
// the reducer's ...model spread. It is null when the daemon omits it (no usage,
// or an uncataloged model) — an honest "unknown" the status row renders as no
// chip, never a misleading ~$0.00.
test("hydrateThread maps evener.cost into the model, null when absent", () => {
  const withCost = testHydrate({
    evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, cost: "~$1.23" },
  });
  expect(withCost.cost).toBe("~$1.23");

  expect(testHydrate().cost).toBeNull();
});

test("hydrateThread keeps absent task aggregate null and distinguishes an authoritative zero", () => {
  expect(testHydrate().tasks).toBeNull();
  expect(
    testHydrate({
      evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 }, tasks: { total: 0, done: 0 } },
    }).tasks,
  ).toEqual({ total: 0, done: 0 });
});

// Wave 5 T1: ThreadModel gains capabilities/goal/context*/usage/workMillis/
// activeTurnStartedAt/reasoningEffortLevels/supportsReasoning, all hydrated
// from thread.evener (appwire/types_thread.go's EvenerThread). None of
// these except reasoningEffortLevels/supportsReasoning (via
// thread/model/changed, tested above) and reasoningEffort (via
// thread/reasoning-effort/changed, tested above) ever get a live push - see
// EvenerThread's own doc comment ("read on demand ... rather than pushed on
// every event") and appwire/protocol.go's Notifications catalog, which has
// no capabilities/goal/context/usage entry at all.
test("hydrateThread maps capabilities/goal/context*/usage/workMillis/activeTurnStartedAt/reasoningEffortLevels/supportsReasoning verbatim from thread.evener", () => {
  const model = testHydrate({
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      queue: { revision: 0 },
      goal: { status: "active", iterations: 2 },
      contextUsed: 12_000,
      contextWindow: 200_000,
      contextPressure: 0.06,
      contextAssumed: true,
      usage: { inputTokens: 100, outputTokens: 50, cacheReadTokens: 10, totalTokens: 160 },
      workMillis: 45_000,
      activeTurnStartedAt: 1_000,
      reasoningEffortLevels: ["low", "medium", "high"],
      supportsReasoning: true,
    },
  });

  expect(model.capabilities).toEqual(CAPABILITIES);
  expect(model.goal).toEqual({ status: "active", iterations: 2 });
  expect(model.contextUsed).toBe(12_000);
  expect(model.contextWindow).toBe(200_000);
  expect(model.contextPressure).toBe(0.06);
  expect(model.contextAssumed).toBe(true);
  // Absent on the wire — a known window, or an older daemon — reads as a
  // real window, never as an assumption.
  const unassumed = testHydrate({ evener: { ref: "ref_t", capabilities: CAPABILITIES, queue: { revision: 0 } } });
  expect(unassumed.contextAssumed).toBe(false);
  expect(model.usage).toEqual({ inputTokens: 100, outputTokens: 50, cacheReadTokens: 10, totalTokens: 160 });
  expect(model.workMillis).toBe(45_000);
  expect(model.activeTurnStartedAt).toBe(new Date(1_000).toISOString());
  expect(model.reasoningEffortLevels).toEqual(["low", "medium", "high"]);
  expect(model.supportsReasoning).toBe(true);
});

test("hydrateThread defaults the wave 5 snapshot-only fields when thread.evener omits them (old daemon / source-backed thread)", () => {
  const model = testHydrate(); // testThread()'s default evener carries none of these

  expect(model.goal).toBeNull();
  expect(model.contextUsed).toBe(0);
  expect(model.contextWindow).toBe(0);
  expect(model.contextPressure).toBe(0);
  expect(model.usage).toBeNull();
  expect(model.workMillis).toBe(0);
  expect(model.activeTurnStartedAt).toBeUndefined();
  expect(model.reasoningEffortLevels).toEqual([]);
  expect(model.supportsReasoning).toBe(false);
});

// Thread.createdAt/updatedAt are top-level wire fields in Unix SECONDS
// (hubcore.UnixSeconds for a past session, entry.StartedAt.Unix() for a live
// one), converted to the model's ISO-string convention like every other
// timestamp on ThreadModel. The session-details panel is what reads them.
test("hydrateThread maps the thread's created/updated wire seconds to ISO instants", () => {
  const model = testHydrate({ createdAt: 1_784_829_766, updatedAt: 1_784_872_877 });

  expect(model.createdAt).toBe(new Date(1_784_829_766_000).toISOString());
  expect(model.updatedAt).toBe(new Date(1_784_872_877_000).toISOString());
});

test("hydrateThread treats Go's zero created/updated stamps as absent rather than 1970", () => {
  const model = testHydrate({ createdAt: 0, updatedAt: 0 });

  expect(model.createdAt).toBeUndefined();
  expect(model.updatedAt).toBeUndefined();
});

test("hydrateThread preserves clientMutationId on authoritative transcript items", () => {
  const model = hydrateThread(
    {
      thread: testThread({
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: "item_a",
                turnId: "turn_1",
                type: "userMessage",
                text: "hello",
                clientMutationId: "mutation-a",
              },
            ],
          },
        ],
      }),
    },
    "ref_t",
    1000,
  );

  expect(model.turns[0]?.items[0]).toMatchObject({ clientMutationId: "mutation-a" });
});

test("hydrateThread maps humanNote/agentNote/sessionUrls from thread.evener", () => {
  const model = testHydrate({
    evener: {
      humanNote: "human hello",
      agentNote: "agent hello",
      sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x", addedBy: "agent", addedAt: 7 }],
    },
  });
  expect(model.humanNote).toBe("human hello");
  expect(model.agentNote).toBe("agent hello");
  expect(model.sessionUrls).toEqual([{ id: "u1", url: "https://x.test/y", label: "x", addedBy: "agent", addedAt: 7 }]);
});

test("hydrateThread defaults notes/urls when thread.evener omits them (old daemon / source-backed thread)", () => {
  const model = testHydrate();
  expect(model.humanNote).toBe("");
  expect(model.agentNote).toBe("");
  expect(model.sessionUrls).toEqual([]);
});

// The notes write gate keys off the recovery fence, which rides the snapshot
// as thread.evener.resumeRequired (absent means not fenced). Without this the
// model cannot tell a fenced live+idle session from an editable one, since the
// SharedNotes capability is deliberately retained for reading.
test("hydrateThread carries the recovery fence from thread.evener.resumeRequired", () => {
  expect(testHydrate().resumeRequired).toBe(false);
  expect(testHydrate({ evener: { resumeRequired: true } }).resumeRequired).toBe(true);
});

// The hub's own admission answer, carried straight from the wire. It is what
// the client now keys Send on, so the model must mirror it (an absent wire
// value means not foldable, never a guess).
test("hydrateThread carries the resume-only foldable bit from thread.evener.resumeOnlyFoldable", () => {
  expect(testHydrate().resumeOnlyFoldable).toBe(false);
  expect(testHydrate({ evener: { resumeOnlyFoldable: true } }).resumeOnlyFoldable).toBe(true);
});

// The artifacts directory is snapshot state: a fresh read that withdraws it
// must clear the model's value, not keep the previous one. Fields merge over
// the existing model, so an omitted key would leave file-link actions aimed at
// a directory the hub no longer advertises.
test("applyReadModel also clears a withdrawn artifactsDir", () => {
  const withRoot = testHydrate({ evener: { artifactsDir: "/state/a/artifacts" } });
  const withdrawn = applyReadModel(withRoot, testHydrate({ evener: {} }));
  expect(withdrawn.artifactsDir).toBeUndefined();
});

test("hydrateThread clears artifactsDir when a snapshot omits it", () => {
  const withRoot = testHydrate({ evener: { artifactsDir: "/state/a/artifacts" } });
  expect(withRoot.artifactsDir).toBe("/state/a/artifacts");
  // A fresh snapshot for the same thread that no longer advertises a root.
  const wire = testThread({ evener: {} });
  const withdrawn = applyReadResponse(
    withRoot,
    {
      thread: wire,
      requestGeneration: 1,
    } as ThreadReadResponse,
    2000,
  );
  expect(withdrawn.artifactsDir).toBeUndefined();
});
