import { describe, expect, it } from "vitest";
import type { ItemModel } from "./model";
import {
  applyNotification,
  currentTranscriptState,
  decodeCurrentTranscriptState,
  hydrateThread,
  threadModelFromCurrentState,
} from "./reducer";
import { currentRead, textDelta } from "./testing/currentTranscript";
import { notificationWireItem } from "./testing/notificationWireFixtures";
import { appendChunk, chunkViewBackingForTests } from "./testing/reducerHooks";
import { subagentCallItems } from "./testing/subagentWireFixtures";
import { toolWireItems } from "./testing/toolWireFixtures";
import { applyReadModel, mergeOlderItemPage } from "./transcriptHistory";
import { itemTextPresence, markItemIdentityOnly, markItemTextOmitted } from "./transcriptItems";
import {
  type EvenerDelegateInfo,
  THREAD_ITEM_EVENT_KINDS,
  type ThreadItem,
  type ThreadTurnsListResponse,
  type Turn,
} from "./types.gen";

function readWithItems(ref: string, items: ThreadItem[]) {
  const read = currentRead(ref);
  return {
    ...read,
    thread: {
      ...read.thread,
      turns: [{ id: "turn_1", status: "completed", itemsView: "full" as const, items }],
    },
  };
}

describe("current transcript checkpoints", () => {
  it("preserves a live stream so the restored reducer can continue its fold", () => {
    const original = hydrateThread(currentRead("local:one"), "local:one", 10);
    const encoded = currentTranscriptState(original);
    expect(encoded).toBeDefined();
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded, {
      capabilities: original.capabilities,
      capabilitySource: original.capabilitySource,
      instanceId: original.instanceId,
      resumeRequired: original.resumeRequired,
      resumeOnlyFoldable: original.resumeOnlyFoldable,
    });
    const next = textDelta("local:one", "-beta");
    const resumed = applyNotification(restored, next, 11);
    const uninterrupted = applyNotification(original, next, 11);

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(resumed.turns).toHaveLength(1);
    expect(resumed.turns[0]?.items).toHaveLength(1);
    expect(resumed.turns[0]?.items[0]?.text).toBe("alpha-beta");
    expect(resumed.turns[0]?.status).toBe("inProgress");
    expect(restored.turns[0]?.status).toBe("inProgress");
    expect(restored.turns[0]?.items[0]?.text).toBe("alpha");
    expect(restored.olderCursor).toBe("older-40");
    const diskOnly = threadModelFromCurrentState(decoded);
    expect(Object.values(diskOnly.capabilities).every((enabled) => !enabled)).toBe(true);
    expect(diskOnly.pendingMutations).toBeUndefined();
    expect(encoded).not.toHaveProperty("turns");
    expect(encoded?.fields).not.toHaveProperty("pendingMutations");
  });

  it("keeps current-state disk data from restoring RAM-only admission", () => {
    const original = hydrateThread(currentRead("local:admission"), "local:admission", 10);
    original.instanceId = "instance-admission-1";
    original.capabilitySource = "read";
    original.resumeRequired = true;
    original.resumeOnlyFoldable = true;
    original.capabilities = { ...original.capabilities, send: true };
    original.pendingMutations = [
      {
        clientMutationId: "mutation-admission-1",
        method: "turn/start",
        executionState: "queued",
        projectionState: "pending",
      },
    ];

    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("populated current state did not encode");
    expect(original.instanceId).toBe("instance-admission-1");
    expect(original.capabilitySource).toBe("read");
    expect(original.resumeRequired).toBe(true);
    expect(original.resumeOnlyFoldable).toBe(true);
    expect(original.pendingMutations).toHaveLength(1);
    expect(encoded.fields).not.toHaveProperty("instanceId");
    expect(encoded.fields).not.toHaveProperty("capabilitySource");
    expect(encoded.fields).not.toHaveProperty("resumeRequired");
    expect(encoded.fields).not.toHaveProperty("resumeOnlyFoldable");
    expect(encoded.fields).not.toHaveProperty("pendingMutations");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid populated current state did not decode");

    const diskOnly = threadModelFromCurrentState(decoded);
    expect(diskOnly.instanceId).toBeUndefined();
    expect(diskOnly.capabilitySource).toBeUndefined();
    expect(diskOnly.resumeRequired).toBeUndefined();
    expect(diskOnly.resumeOnlyFoldable).toBeUndefined();
    expect(Object.values(diskOnly.capabilities).every((enabled) => !enabled)).toBe(true);
    expect(diskOnly.pendingMutations).toBeUndefined();

    const uninterruptedOwner = threadModelFromCurrentState(decoded, {
      capabilities: original.capabilities,
      capabilitySource: original.capabilitySource,
      instanceId: original.instanceId,
      resumeRequired: original.resumeRequired,
      resumeOnlyFoldable: original.resumeOnlyFoldable,
    });
    expect(uninterruptedOwner.capabilities).toEqual(original.capabilities);
    expect(uninterruptedOwner.capabilitySource).toBe("read");
    expect(uninterruptedOwner.instanceId).toBe("instance-admission-1");
    expect(uninterruptedOwner.resumeRequired).toBe(true);
    expect(uninterruptedOwner.resumeOnlyFoldable).toBe(true);
    expect(uninterruptedOwner.pendingMutations).toBeUndefined();
  });

  it("retains producer-backed tool and delegate history, omitted text and structured payloads", () => {
    for (const [ref, items] of [
      ["local:tools", toolWireItems()],
      ["local:delegates", subagentCallItems()],
    ] as const) {
      const original = hydrateThread(readWithItems(ref, items), ref, 10);
      const itemWithText = original.history?.turns.flatMap((turn) => turn.items)[0];
      if (!itemWithText) throw new Error("producer fixture had no hydrated history item");
      markItemTextOmitted(itemWithText);
      const encoded = currentTranscriptState(original);
      if (!encoded) throw new Error("versioned fixture history did not encode");
      const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
      if (!decoded) throw new Error("producer-backed current state did not decode");
      const restored = threadModelFromCurrentState(decoded);

      expect(restored.history?.turns).toEqual(original.history?.turns);
      if (ref === "local:tools") {
        const toolItems = restored.history?.turns.flatMap((turn) => turn.items) ?? [];
        expect(toolItems.find((item) => item.callId === "call_shell_failed" && item.argumentsJSON)?.argumentsJSON).toBe(
          '{"command":"test -f agent/missing.go"}',
        );
        expect(toolItems.find((item) => item.callId === "call_shell_failed" && item.raw)?.output).toBe("[exit 1]");
        expect(toolItems.find((item) => item.callId === "call_shell_failed" && item.raw)?.error).toBeUndefined();
        expect(toolItems.find((item) => item.callId === "call_shell_failed" && item.raw)?.raw).toEqual({
          type: "shell",
          status: "command_exited_nonzero",
          reason: "exit_nonzero",
          mode: "foreground",
          timed_out: false,
          exit_code: 1,
          output: "",
          truncated: false,
          output_status: "all_retained",
        });
      }
      const restoredItem = restored.history?.turns
        .flatMap((turn) => turn.items)
        .find((item) => item.id === itemWithText.id);
      expect(restoredItem && itemTextPresence(restoredItem)).toBe("omitted");
      expect(restored.olderCursor).toBe("older-40");
      expect(restored.overlay?.["stream:s1:agentMessage"]?.item.text).toBe("alpha");
    }
  });

  it("continues a checkpoint through a producer-backed steering history update", () => {
    const original = hydrateThread(currentRead("local:steering"), "local:steering", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const steering = notificationWireItem("delegate-reported");
    const update = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_local:steering",
        ref: "local:steering",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 41 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "full" as const }],
        items: [{ ...steering, version: 41, position: { entry: 40, item: 0 } }],
      },
    };

    const resumed = applyNotification(restored, update, 11);
    const uninterrupted = applyNotification(original, update, 11);

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(resumed.turns).toHaveLength(1);
    expect(resumed.turns[0]?.status).toBe("completed");
    expect(resumed.turns[0]?.items.map((item) => item.text)).toEqual([
      '<delegate-notification delegate_id="dlg_1" name="Fix race in tree settle" kind="report">{"kind":"reported","message":"Done: the settle pass now waits for the drain.\\n\\nTests: go test ./agent/... passes.","metadata":{"outcome":"completed","name":"Fix race in tree settle","task":"Find and fix the race between tree settle and the drain.","description":"Fix race in tree settle","agent_type":"general-purpose","run_started_at":"2026-09-28T20:00:00Z","run_ended_at":"2026-09-28T20:02:00Z"}}</delegate-notification>',
      "alpha",
    ]);
  });

  it("continues a structured tool result through a history notification", () => {
    const original = hydrateThread(
      readWithItems("local:tool-continuation", toolWireItems()),
      "local:tool-continuation",
      10,
    );
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned tool fixture did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid tool checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const producerResult = toolWireItems().find((item) => item.callId === "call_shell_failed" && item.raw);
    if (!producerResult) throw new Error("producer fixture's structured failed result missing");
    const update = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_local:tool-continuation",
        ref: "local:tool-continuation",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 41 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "full" as const }],
        items: [
          {
            ...producerResult,
            id: "error-result",
            callId: "call_denied",
            argumentsJson: '{"command":"test -f agent/missing.go"}',
            error: "permission denied",
            version: 41,
            position: { entry: 40, item: 0 },
          },
        ],
      },
    };

    const resumed = applyNotification(restored, update, 11);
    const uninterrupted = applyNotification(original, update, 11);
    const result = resumed.history?.turns.flatMap((turn) => turn.items).find((item) => item.id === "error-result");

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(result?.argumentsJSON).toBe('{"command":"test -f agent/missing.go"}');
    expect(result?.output).toBe("[exit 1]");
    expect(result?.error).toBe("permission denied");
    expect(result?.raw).toEqual({
      type: "shell",
      status: "command_exited_nonzero",
      reason: "exit_nonzero",
      mode: "foreground",
      timed_out: false,
      exit_code: 1,
      output: "",
      truncated: false,
      output_status: "all_retained",
    });
  });

  it("keeps multiple loaded older turns and the cursor through later stream updates", () => {
    const read = currentRead("local:pages");
    read.thread.turns = [
      {
        id: "turn_older_1",
        status: "completed",
        itemsView: "full",
        items: [{ id: "old-1", turnId: "turn_older_1", type: "agentMessage", text: "older one" }],
      },
      {
        id: "turn_older_2",
        status: "completed",
        itemsView: "full",
        items: [{ id: "old-2", turnId: "turn_older_2", type: "agentMessage", text: "older two" }],
      },
    ];
    const original = hydrateThread(read, "local:pages", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);

    const resumed = applyNotification(restored, textDelta("local:pages", "-tail"), 11);
    const uninterrupted = applyNotification(original, textDelta("local:pages", "-tail"), 11);

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(resumed.turns.map((turn) => turn.id)).toEqual(["turn_older_1", "turn_older_2", "turn_1"]);
    expect(resumed.turns.map((turn) => turn.items[0]?.text)).toEqual(["older one", "older two", "alpha-tail"]);
    expect(resumed.olderCursor).toBe("older-40");
    expect(resumed.history?.incarnation).toBe("inc-1");
  });

  it("round-trips an identity-only item and adopts its real later page payload", () => {
    const original = hydrateThread(
      readWithItems("local:identity-only", [
        {
          id: "recovered-tool",
          turnId: "turn_1",
          type: "toolCall",
          text: "",
          position: { entry: 39, item: 0 },
        },
      ]),
      "local:identity-only",
      10,
    );
    const skeleton = original.history?.turns[0]?.items[0];
    if (!skeleton) throw new Error("identity-only fixture item missing");
    markItemIdentityOnly(skeleton);

    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned identity-only history did not encode");
    expect(encoded.history.turns[0]?.items[0]?.identityOnly).toBe(true);
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid identity-only checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const restoredCheckpoint = currentTranscriptState(restored);
    expect(restoredCheckpoint?.history.turns[0]?.items[0]?.identityOnly).toBe(true);

    const page: ThreadTurnsListResponse = {
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 40 },
      data: [
        {
          id: "turn_1",
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: "recovered-tool",
              turnId: "turn_1",
              type: "toolCall",
              text: "run pwd",
              toolName: "shell",
              callId: "call_recovered",
              argumentsJson: '{"command":"pwd"}',
              position: { entry: 39, item: 0 },
            },
          ],
        },
      ],
      nextCursor: "older-39",
    };
    const resumed = mergeOlderItemPage(restored, page);
    const uninterrupted = mergeOlderItemPage(original, page);
    const items = resumed.history?.turns.flatMap((turn) => turn.items) ?? [];

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(resumed.history?.turns.map((turn) => turn.id)).toEqual(["turn_1"]);
    expect(items.map((item) => item.id)).toEqual(["recovered-tool"]);
    expect(items[0]).toMatchObject({
      text: "run pwd",
      toolName: "shell",
      callId: "call_recovered",
      argumentsJSON: '{"command":"pwd"}',
      position: { entry: 39, item: 0 },
    });
    expect(resumed.olderCursor).toBe("older-39");
  });

  it("continues paging after checkpointing two pages loaded by the reducer", () => {
    const original = hydrateThread(
      readWithItems("local:page-continuation", [
        {
          id: "latest-item",
          turnId: "turn_1",
          type: "agentMessage",
          text: "latest window",
          position: { entry: 39, item: 0 },
        },
      ]),
      "local:page-continuation",
      10,
    );
    const page = (turnId: string, itemId: string, entry: number, cursor: string): ThreadTurnsListResponse => ({
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 40 },
      data: [
        {
          id: turnId,
          status: "completed",
          itemsView: "full",
          items: [
            {
              id: itemId,
              turnId,
              type: "agentMessage",
              text: itemId,
              position: { entry, item: 0 },
            },
          ],
        },
      ],
      nextCursor: cursor,
    });
    const firstPage = page("turn_old_1", "item_old_1", 30, "older-20");
    const secondPage = page("turn_old_2", "item_old_2", 20, "older-10");
    const loadedTwice = mergeOlderItemPage(mergeOlderItemPage(original, firstPage), secondPage);
    expect(loadedTwice.history?.turns.map((turn) => turn.id)).toEqual(["turn_old_2", "turn_old_1", "turn_1"]);
    expect(loadedTwice.olderCursor).toBe("older-10");

    const encoded = currentTranscriptState(loadedTwice);
    if (!encoded) throw new Error("versioned history with loaded pages did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid multi-page checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const thirdPage = page("turn_old_3", "item_old_3", 10, "older-0");
    const resumed = mergeOlderItemPage(restored, thirdPage);
    const uninterrupted = mergeOlderItemPage(loadedTwice, thirdPage);

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(resumed.history?.turns.map((turn) => turn.id)).toEqual(["turn_old_3", "turn_old_2", "turn_old_1", "turn_1"]);
    expect(resumed.history?.turns.flatMap((turn) => turn.items).map((item) => item.id)).toEqual([
      "item_old_3",
      "item_old_2",
      "item_old_1",
      "latest-item",
    ]);
    expect(resumed.olderCursor).toBe("older-0");
    expect(resumed.history).toMatchObject({
      bootGeneration: "1",
      epoch: 1,
      incarnation: "inc-1",
      length: 40,
      appliedGeneration: 1,
      issuedGeneration: 1,
    });
  });

  it("rejects unknown versions, malformed positions and mismatched overlay keys", () => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:invalid"), "local:invalid", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");

    expect(decodeCurrentTranscriptState({ ...encoded, version: 2 })).toBeUndefined();
    expect(
      decodeCurrentTranscriptState({ ...encoded, overlay: { wrong: encoded.overlay["stream:s1:agentMessage"] } }),
    ).toBeUndefined();
    expect(
      decodeCurrentTranscriptState({
        ...encoded,
        history: {
          ...encoded.history,
          turns: [
            {
              id: "turn_1",
              status: "completed",
              items: [{ id: "a", turnId: "turn_1", type: "text", text: "x", position: { entry: -1, item: 0 } }],
            },
          ],
        },
      }),
    ).toBeUndefined();
  });

  it("refuses to encode a legacy history without a versioned incarnation", () => {
    const model = hydrateThread(currentRead("local:legacy"), "local:legacy", 10);
    if (!model.history) throw new Error("fixture history did not hydrate");
    model.history = { ...model.history, incarnation: undefined };

    expect(currentTranscriptState(model)).toBeUndefined();
  });

  it("does not leak unknown fields from decoded state into the model", () => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:unknown"), "local:unknown", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState({
      ...encoded,
      fields: { ...encoded.fields, unexpected: "not model state" },
    });
    if (!decoded) throw new Error("valid state with an unknown field did not decode");
    const restored = threadModelFromCurrentState(decoded);

    expect(restored).not.toHaveProperty("unexpected");
  });

  it.each([
    [
      "overlay images",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        const overlay = state.overlay["stream:s1:agentMessage"];
        if (!overlay) throw new Error("fixture overlay missing");
        overlay.item.images = {} as never;
      },
    ],
    [
      "display queue",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.queue = {} as never;
      },
    ],
    [
      "queue extra fields",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.queue = { revision: 0, unexpected: "disk-extra" } as never;
      },
    ],
    [
      "usage",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.usage = { inputTokens: "invalid" } as never;
      },
    ],
    [
      "access",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.access = { sandbox: "workspace" } as never;
      },
    ],
    [
      "goal",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.goal = {} as never;
      },
    ],
    [
      "delegate records",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.delegates = [{}] as never;
      },
    ],
    [
      "deferred page turns",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.history.deferredPages = [{ data: [null] }] as never;
      },
    ],
    [
      "history turn usage",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.history.turns = [{ id: "turn_bad", status: "completed", items: [], usage: [] }] as never;
      },
    ],
    [
      "history turn timestamp",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.history.turns = [{ id: "turn_bad", status: "completed", items: [], startedAt: 4 }] as never;
      },
    ],
  ])("rejects malformed nested %s before restoration", (_name, mutate) => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:malformed"), "local:malformed", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const malformed = structuredClone(encoded);
    mutate(malformed);

    expect(decodeCurrentTranscriptState(malformed)).toBeUndefined();
  });

  it("does not leak unknown schema-owned fields from nested overlay items", () => {
    const encoded = currentTranscriptState(
      hydrateThread(currentRead("local:nested-unknown"), "local:nested-unknown", 10),
    );
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const checkpoint = structuredClone(encoded);
    const overlay = checkpoint.overlay["stream:s1:agentMessage"];
    if (!overlay) throw new Error("fixture overlay missing");
    (overlay.item as unknown as Record<string, unknown>).unexpected = "disk-extra";

    const decoded = decodeCurrentTranscriptState(checkpoint);
    if (!decoded) return;
    const restored = threadModelFromCurrentState(decoded);

    expect(restored.overlay?.["stream:s1:agentMessage"]?.item).not.toHaveProperty("unexpected");
  });

  it("preserves arbitrary structured raw data in a valid overlay item", () => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:raw"), "local:raw", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const checkpoint = structuredClone(encoded);
    const overlay = checkpoint.overlay["stream:s1:agentMessage"];
    if (!overlay) throw new Error("fixture overlay missing");
    overlay.item.raw = { opaque: { values: ["kept", 7, null] } };

    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(checkpoint)));
    if (!decoded) throw new Error("valid structured raw payload did not decode");

    expect(threadModelFromCurrentState(decoded).overlay?.["stream:s1:agentMessage"]?.item.raw).toEqual({
      opaque: { values: ["kept", 7, null] },
    });
  });

  it.each([
    [
      "delegate task object",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.delegates = [
          {
            runGeneration: 1,
            delegateId: "dlg_schema",
            ownerSessionId: "root",
            rootSessionId: "root",
            childSessionId: "child",
            transcriptRef: "local:child",
            type: "delegate",
            lifecycle: "ended",
            phase: "ended",
            status: "completed",
            resumable: true,
            needsAttention: false,
            projectionRevision: 1,
            task: {},
          },
        ] as never;
      },
    ],
    [
      "skills object",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.skills = {} as never;
      },
    ],
    [
      "delegate worktree extra key",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.delegates = [
          {
            runGeneration: 1,
            delegateId: "dlg_schema",
            ownerSessionId: "root",
            rootSessionId: "root",
            childSessionId: "child",
            transcriptRef: "local:child",
            type: "delegate",
            lifecycle: "ended",
            phase: "ended",
            status: "completed",
            resumable: true,
            needsAttention: false,
            projectionRevision: 1,
            worktree: {
              path: "/tmp/w",
              branch: "topic",
              headSha: "abc",
              ahead: 0,
              dirty: false,
              unexpected: "disk-extra",
            },
          },
        ] as never;
      },
    ],
    [
      "delegate usage null",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.delegates = [
          {
            runGeneration: 1,
            delegateId: "dlg_schema",
            ownerSessionId: "root",
            rootSessionId: "root",
            childSessionId: "child",
            transcriptRef: "local:child",
            type: "delegate",
            lifecycle: "ended",
            phase: "ended",
            status: "completed",
            resumable: true,
            needsAttention: false,
            projectionRevision: 1,
            usage: null,
          },
        ] as never;
      },
    ],
    [
      "deferred turn error extra key",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.history.deferredPages = [
          {
            data: [
              {
                id: "old",
                itemsView: "full",
                status: "failed",
                items: [],
                error: { message: "failed", unexpected: "disk-extra" },
              },
            ],
          },
        ] as never;
      },
    ],
    [
      "diagnostics command missing required name",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.diagnostics = { commands: [{}] } as never;
      },
    ],
    [
      "skill missing required booleans",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.skills = [{ name: "skill" }] as never;
      },
    ],
    [
      "escalation missing required identity",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.pendingEscalations = [{}] as never;
      },
    ],
    [
      "session URL malformed element",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.sessionUrls = [null] as never;
      },
    ],
    [
      "model retry missing required state",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.fields.modelRetry = {} as never;
      },
    ],
    [
      "deferred turn error unknown cause field",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.history.deferredPages = [
          {
            data: [
              {
                id: "old",
                itemsView: "full",
                status: "failed",
                items: [],
                error: { message: "failed", cause: { kind: "provider", unexpected: "disk-extra" } },
              },
            ],
          },
        ] as never;
      },
    ],
    [
      "cached item warning malformed source",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.history.turns = [
          {
            id: "turn_bad",
            status: "completed",
            items: [{ id: "item", turnId: "turn_bad", type: "warning", text: "warning", warning: { source: 7 } }],
          },
        ] as never;
      },
    ],
    [
      "history awaited unknown field",
      (state: NonNullable<ReturnType<typeof currentTranscriptState>>) => {
        state.history.awaited = { bootGeneration: "1", epoch: 2, unexpected: "disk-extra" } as never;
      },
    ],
  ])("rejects schema-owned invalid structure: %s", (_name, mutate) => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:schema"), "local:schema", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const malformed = structuredClone(encoded);
    mutate(malformed);

    expect(decodeCurrentTranscriptState(malformed)).toBeUndefined();
  });

  it("rejects unknown keys on schema-owned deferred turn errors", () => {
    const encoded = currentTranscriptState(hydrateThread(currentRead("local:error-key"), "local:error-key", 10));
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const malformed = structuredClone(encoded);
    malformed.history.deferredPages = [
      {
        data: [
          {
            id: "old",
            itemsView: "full",
            status: "failed",
            items: [],
            error: { message: "failed", unexpected: "disk-extra" } as never,
          },
        ],
      },
    ];

    expect(decodeCurrentTranscriptState(malformed)).toBeUndefined();
  });

  it.each(["overlay", "deferred"] as const)("rejects undeclared wire eventKind in %s items", (location) => {
    const encoded = currentTranscriptState(
      hydrateThread(currentRead(`local:event-kind-${location}`), `local:event-kind-${location}`, 10),
    );
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const malformed = structuredClone(encoded);
    if (location === "overlay") {
      const overlay = malformed.overlay["stream:s1:agentMessage"];
      if (!overlay) throw new Error("fixture overlay missing");
      overlay.item.eventKind = "not-a-declared-event-kind" as never;
    } else {
      malformed.history.deferredPages = [
        {
          data: [
            {
              id: "old",
              itemsView: "full",
              status: "completed",
              items: [
                {
                  id: "event",
                  type: "systemMessage",
                  eventKind: "not-a-declared-event-kind" as never,
                },
              ],
            },
          ],
        },
      ];
    }

    expect(decodeCurrentTranscriptState(JSON.parse(JSON.stringify(malformed)))).toBeUndefined();
  });

  it.each(THREAD_ITEM_EVENT_KINDS)("accepts declared wire eventKind %s in overlay and deferred items", (eventKind) => {
    const encoded = currentTranscriptState(
      hydrateThread(currentRead("local:event-kind-valid"), "local:event-kind-valid", 10),
    );
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const overlayState = structuredClone(encoded);
    const overlay = overlayState.overlay["stream:s1:agentMessage"];
    if (!overlay) throw new Error("fixture overlay missing");
    overlay.item.eventKind = eventKind;
    expect(decodeCurrentTranscriptState(JSON.parse(JSON.stringify(overlayState)))).toBeDefined();

    const deferredState = structuredClone(encoded);
    deferredState.history.deferredPages = [
      {
        data: [
          {
            id: "old",
            itemsView: "full",
            status: "completed",
            items: [{ id: "event", type: "systemMessage", eventKind }],
          },
        ],
      },
    ];
    expect(decodeCurrentTranscriptState(JSON.parse(JSON.stringify(deferredState)))).toBeDefined();
  });

  it("keeps cached item eventKind open-ended", () => {
    const encoded = currentTranscriptState(
      hydrateThread(currentRead("local:cached-event-kind"), "local:cached-event-kind", 10),
    );
    if (!encoded) throw new Error("versioned fixture history did not encode");
    encoded.history.turns = [
      {
        id: "turn_cached",
        status: "completed",
        items: [
          {
            id: "item_cached",
            turnId: "turn_cached",
            type: "systemMessage",
            text: "cached",
            eventKind: "custom-cached-event-kind",
          },
        ],
      },
    ];

    expect(decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)))).toBeDefined();
  });

  it("round-trips valid optional and empty schemas without restricting opaque payloads", () => {
    const original = hydrateThread(currentRead("local:valid-schema"), "local:valid-schema", 10);
    original.diagnostics = { commands: [], plugins: [] };
    original.delegates = [
      {
        runGeneration: 1,
        delegateId: "dlg_valid",
        ownerSessionId: "root",
        rootSessionId: "root",
        childSessionId: "child",
        transcriptRef: "local:child",
        type: "delegate",
        lifecycle: "ended",
        phase: "ended",
        status: "completed",
        resumable: true,
        needsAttention: false,
        projectionRevision: 1,
        task: "",
        warnings: [],
        diagnostics: [],
        message: { arbitrary: [1, null, { nested: true }] },
        structuredResult: { arbitrary: { result: "kept" } },
        usage: {},
        worktree: { path: "", branch: "", headSha: "", ahead: 0, dirty: false },
      },
    ];
    original.skills = [
      {
        name: "",
        disableModelInvocation: false,
        userInvocable: false,
        available: false,
        allowedTools: [],
      },
    ];
    original.pendingEscalations = [
      {
        threadId: original.threadId,
        ref: original.ref,
        escalationId: "esc-valid",
        mode: "read",
        tool: "Read",
        kind: "path",
        deniedPath: "",
        partiallyRan: false,
      },
    ];
    original.sessionUrls = [{ id: "url-valid", url: "", label: "", addedBy: "", addedAt: 0 }];
    original.modelRetry = {
      attempt: 0,
      maxAttempts: 0,
      delayMs: 0,
      groupElapsedMs: 0,
      attemptCap: 0,
      receivedAt: 0,
      errorClass: "",
      statusCode: 0,
      turnId: "",
      model: "",
    };
    if (!original.history) throw new Error("versioned fixture history did not hydrate");
    original.history = {
      ...original.history,
      deferredPages: [
        {
          data: [
            {
              id: "old-error",
              itemsView: "full",
              status: "failed",
              items: [],
              error: {
                message: "failed",
                additionalDetails: "",
                codexErrorInfo: { arbitrary: [null, { retained: true }] },
                cause: { kind: "provider", provider: "p", model: "m", status: 0 },
              },
            },
          ],
        },
      ],
    };
    original.goal = null;
    original.usage = null;
    original.cost = null;
    original.turnSlots = null;
    original.tasks = null;

    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("valid populated checkpoint did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid populated checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);

    expect(restored.diagnostics).toEqual({ commands: [], plugins: [] });
    expect(restored.skills).toEqual(original.skills);
    expect(restored.pendingEscalations).toEqual(original.pendingEscalations);
    expect(restored.sessionUrls).toEqual(original.sessionUrls);
    expect(restored.modelRetry).toEqual(original.modelRetry);
    expect(restored.delegates?.[0]?.message).toEqual({ arbitrary: [1, null, { nested: true }] });
    expect(restored.delegates?.[0]?.structuredResult).toEqual({ arbitrary: { result: "kept" } });
    expect(restored.delegates?.[0]?.worktree).toEqual({ path: "", branch: "", headSha: "", ahead: 0, dirty: false });
    expect(restored.history?.deferredPages[0]?.data[0]?.error?.codexErrorInfo).toEqual({
      arbitrary: [null, { retained: true }],
    });
    expect(restored.goal).toBeNull();
    expect(restored.usage).toBeNull();
    expect(restored.cost).toBeNull();
    expect(restored.turnSlots).toBeNull();
    expect(restored.tasks).toBeNull();
    expect(restored.history?.deferredPages).toHaveLength(1);
  });

  it("round-trips a delegate carrying every generated field", () => {
    // Required<EvenerDelegateInfo> makes tsc fail this literal when the wire
    // type gains a field, so the validator cannot drift without a red build.
    const everyField: Required<EvenerDelegateInfo> = {
      runGeneration: 2,
      delegateId: "dlg_full",
      ownerSessionId: "root",
      logicalOwnerSessionId: "mid",
      rootSessionId: "root",
      childSessionId: "child",
      transcriptRef: "local:child",
      parentDelegateId: "dlg_parent",
      type: "delegate",
      lifecycle: "ended",
      phase: "ended",
      status: "completed",
      outcome: "completed",
      reason: "done",
      error: "none",
      terminal: true,
      resumable: true,
      needsAttention: false,
      notResumableReason: "n/a",
      projectionRevision: 3,
      reportPreview: "report text",
      reportPreviewTruncated: true,
      task: "task",
      description: "description",
      agentType: "explorer",
      requestedModel: "fast",
      resolvedProfileId: "profile",
      resolvedModel: "model",
      model: "provider/model",
      reasoningEffort: "high",
      originTurnId: "turn_1",
      originToolCallId: "call_1",
      originItemId: "item_1",
      runStartedAt: "2026-10-25T10:00:00Z",
      runEndedAt: "2026-10-25T10:01:00Z",
      latestActivityAt: "2026-10-25T10:00:59Z",
      latestIntent: "Reading the board.",
      runningForMs: 1,
      quietForMs: 2,
      durationMs: 3,
      packetKind: "communicate",
      message: { any: "thing" },
      structuredResult: { any: "result" },
      structuredResultValid: true,
      structuredResultReason: "ok",
      warnings: ["w"],
      diagnostics: ["d"],
      exhaustionBudget: "turns",
      exhaustionLimit: 12,
      exhaustionResumable: false,
      delegationAllowance: 2,
      parentWatchGranted: true,
      usage: { inputTokens: 1, outputTokens: 2, cacheReadTokens: 3, totalTokens: 6 },
      worktree: { path: "/tmp/w", branch: "b", headSha: "abc", ahead: 1, dirty: false },
    };
    const original = hydrateThread(currentRead("local:every-field"), "local:every-field", 10);
    original.delegates = [everyField];

    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("populated checkpoint did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("a delegate with every generated field was rejected");

    expect(threadModelFromCurrentState(decoded).delegates?.[0]).toEqual(everyField);
  });

  it("round-trips a deferred turn carrying every generated field", () => {
    // Required<> makes tsc fail these literals when the wire types gain a
    // field, so the deferred-page validators cannot drift without a red build.
    const everyItemField: Required<ThreadItem> = {
      type: "toolCall",
      id: "item_full",
      transcriptKey: "key_full",
      position: { entry: 1, item: 2, sub: 3 },
      turnId: "turn_full",
      transcriptEntryIndex: 4,
      text: "text",
      delta: "delta",
      images: [
        {
          type: "image",
          text: "alt",
          mentions: [{ kind: "file", name: "a.go", offset: 1 }],
          url: "https://example.test/i.png",
          mediaType: "image/png",
          data: "AAAA",
          name: "i.png",
          path: "/tmp/i.png",
          metadata: { k: "v" },
        },
      ],
      toolName: "Read",
      callId: "call_full",
      argumentsJson: "{}",
      description: "description",
      output: "output",
      error: "error",
      outputImages: [
        { source: "tool", name: "o.png", mediaType: "image/png", size: 5, url: "u", sha: "abc", path: "/tmp/o.png" },
      ],
      status: "completed",
      prevalOnly: true,
      startedAt: 1,
      completedAt: 2,
      durationMs: 3,
      exitCode: 0,
      raw: { any: "thing" },
      eventKind: THREAD_ITEM_EVENT_KINDS[0],
      source: "source",
      steeringKind: "interrupt",
      clientMutationId: "mutation",
      version: 5,
      roundId: "round",
      completedAtEntry: 6,
    };
    const everyTurnField: Required<Turn> = {
      id: "turn_full",
      items: [everyItemField],
      itemsView: "full",
      status: "completed",
      error: { message: "failed" },
      version: 7,
      hasEarlierItems: true,
      hasLaterItems: false,
      startedAt: 1,
      completedAt: 2,
      durationMs: 3,
      usage: { inputTokens: 1, outputTokens: 2, cacheReadTokens: 3, totalTokens: 6 },
      cost: "0.01",
    };
    const original = hydrateThread(currentRead("local:every-turn-field"), "local:every-turn-field", 10);
    if (!original.history) throw new Error("fixture history did not hydrate");
    original.history = { ...original.history, deferredPages: [{ data: [everyTurnField] }] };

    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("populated checkpoint did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("a turn with every generated field was rejected");

    expect(threadModelFromCurrentState(decoded).history?.deferredPages[0]?.data[0]).toEqual(everyTurnField);
  });

  it("keeps omitted text distinct from explicit empty text", () => {
    const read = readWithItems("local:text", [
      { id: "omitted", turnId: "turn_1", type: "agentMessage" },
      { id: "empty", turnId: "turn_1", type: "agentMessage", text: "" },
    ]);
    const original = hydrateThread(read, "local:text", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const items = restored.history?.turns.flatMap((turn) => turn.items) ?? [];

    expect(items.find((item) => item.id === "omitted" && itemTextPresence(item) === "omitted")?.text).toBe("");
    expect(items.find((item) => item.id === "empty" && itemTextPresence(item) === "provided")?.text).toBe("");
  });

  it("continues omitted and empty text into history while resolving later images to the same session", () => {
    const original = hydrateThread(
      readWithItems("local:text-image-continuation", [
        { id: "omitted", turnId: "turn_1", type: "agentMessage" },
        { id: "empty", turnId: "turn_1", type: "agentMessage", text: "" },
      ]),
      "local:text-image-continuation",
      10,
    );
    const omitted = original.history?.turns[0]?.items.find((item) => item.id === "omitted");
    if (!omitted) throw new Error("omitted-text fixture item missing");
    markItemTextOmitted(omitted);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid text/image checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const update = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_local:text-image-continuation",
        ref: "local:text-image-continuation",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 43 },
        turns: [{ id: "turn_1", status: "completed", itemsView: "full" as const }],
        items: [
          {
            id: "omitted",
            turnId: "turn_1",
            type: "agentMessage",
            text: "continued",
            version: 41,
            position: { entry: 40, item: 0 },
          },
          {
            id: "empty",
            turnId: "turn_1",
            type: "agentMessage",
            text: "",
            version: 42,
            position: { entry: 41, item: 0 },
          },
          {
            id: "image-next",
            turnId: "turn_1",
            type: "userMessage",
            text: "image next",
            images: [{ type: "image", metadata: { sha: "b".repeat(64) } }],
            version: 43,
            position: { entry: 42, item: 0 },
          },
        ],
      },
    };

    const resumed = applyNotification(restored, update, 11);
    const uninterrupted = applyNotification(original, update, 11);
    const items = resumed.history?.turns.flatMap((turn) => turn.items) ?? [];

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(items.find((item) => item.id === "omitted")?.text).toBe("continued");
    expect(items.find((item) => item.id === "empty")?.text).toBe("");
    expect(resumed.turns.flatMap((turn) => turn.items).find((item) => item.id === "image-next")?.images?.[0]?.src).toBe(
      `/s/sess_local%3Atext-image-continuation/images/${"b".repeat(64)}`,
    );
  });

  it("keeps image routes bound to the serving session through JSON", () => {
    const image = {
      id: "image",
      turnId: "turn_1",
      type: "userMessage",
      text: "image",
      images: [{ type: "image", metadata: { sha: "a".repeat(64) } }],
    };
    const original = hydrateThread(readWithItems("local:image", [image]), "local:image", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);

    expect(restored.history?.turns[0]?.items[0]?.images?.[0]?.src).toBe(
      `/s/sess_local%3Aimage/images/${"a".repeat(64)}`,
    );
  });

  it("keeps reasoning chunks and delegate projection revisions through JSON", () => {
    const original = hydrateThread(
      readWithItems("local:reasoning", [{ id: "reason", turnId: "turn_1", type: "reasoning", text: "first chunk" }]),
      "local:reasoning",
      10,
    );
    original.delegates = [
      {
        runGeneration: 1,
        delegateId: "dlg_fixture",
        ownerSessionId: "session-root",
        rootSessionId: "session-root",
        childSessionId: "session-child",
        transcriptRef: "local:child",
        type: "delegate",
        lifecycle: "ended",
        phase: "ended",
        status: "completed",
        resumable: true,
        needsAttention: false,
        projectionRevision: 2,
      } satisfies EvenerDelegateInfo,
    ];
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const item = restored.history?.turns[0]?.items[0];

    expect(item?.text).toBe("first chunk");
    expect(item?.reasoningSummaries).toEqual([["first chunk"]]);
    expect(restored.delegates?.map((delegate) => delegate.projectionRevision)).toEqual(
      original.delegates?.map((delegate) => delegate.projectionRevision),
    );
  });

  it("continues a live reasoning stream as real chunks after restoration", () => {
    const original = hydrateThread(currentRead("local:reasoning-live"), "local:reasoning-live", 10);
    const started = applyNotification(
      original,
      {
        method: "overlay/upserted",
        params: {
          threadId: "thr_local:reasoning-live",
          ref: "local:reasoning-live",
          item: {
            key: "stream:s2:reasoning",
            kind: "stream",
            streamId: "s2",
            roundId: "r1",
            turnId: "turn_1",
            item: { id: "reasoning-live", turnId: "turn_1", type: "reasoning", text: "first" },
          },
        },
      },
      11,
    );
    const accumulated = applyNotification(
      applyNotification(
        started,
        {
          method: "overlay/delta",
          params: {
            threadId: "thr_local:reasoning-live",
            ref: "local:reasoning-live",
            key: "stream:s2:reasoning",
            field: "text",
            delta: " chunk-1",
          },
        },
        12,
      ),
      {
        method: "overlay/delta",
        params: {
          threadId: "thr_local:reasoning-live",
          ref: "local:reasoning-live",
          key: "stream:s2:reasoning",
          field: "text",
          delta: " chunk-2",
        },
      },
      13,
    );
    expect(accumulated.overlay?.["stream:s2:reasoning"]?.item.text).toBe("first chunk-1 chunk-2");
    const encoded = currentTranscriptState(accumulated);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid reasoning checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const next = {
      method: "overlay/delta" as const,
      params: {
        threadId: "thr_local:reasoning-live",
        ref: "local:reasoning-live",
        key: "stream:s2:reasoning",
        field: "text",
        delta: " chunk-3",
      },
    };

    const resumed = applyNotification(restored, next, 14);
    const uninterrupted = applyNotification(accumulated, next, 14);
    const reasoning = resumed.turns[0]?.items.find((item) => item.id === "reasoning-live");

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(reasoning?.text).toBe("first chunk-1 chunk-2 chunk-3");
    expect(reasoning?.reasoningSummaries).toEqual([["first chunk-1 chunk-2 chunk-3"]]);
    expect(resumed.turns[0]?.items.filter((item) => item.id === "reasoning-live")).toHaveLength(1);
  });

  it("materializes an accumulated chunk view in a checkpoint and continues through a read", () => {
    const ref = "local:reasoning-chunk-view";
    const original = hydrateThread(
      readWithItems(ref, [
        {
          id: "reasoning-chunks",
          turnId: "turn_1",
          type: "reasoning",
          text: "reasoning chunks",
          position: { entry: 39, item: 0 },
        },
      ]),
      ref,
      10,
    );
    const chunkedItem: ItemModel | undefined = original.history?.turns
      .flatMap((turn) => turn.items)
      .find((item) => item.id === "reasoning-chunks");
    if (!chunkedItem) throw new Error("typed reasoning history fixture missing");
    chunkedItem.pendingText = appendChunk(appendChunk(undefined, "reasoning "), "chunks");
    chunkedItem.reasoningSummaries = [appendChunk(appendChunk(undefined, "summary "), "chunks")];
    const pendingTextBacking = chunkViewBackingForTests(chunkedItem.pendingText);
    const summaryChunks = chunkedItem.reasoningSummaries[0];
    if (!summaryChunks) throw new Error("reasoning summary chunks missing");
    const summaryBacking = chunkViewBackingForTests(summaryChunks);
    expect(pendingTextBacking).toBeDefined();
    expect(pendingTextBacking).toEqual(["reasoning ", "chunks"]);
    expect(summaryBacking).toBeDefined();
    expect(summaryBacking).toEqual(["summary ", "chunks"]);

    const beforeCheckpointRead = readWithItems(ref, [
      {
        id: "read-before-checkpoint",
        turnId: "turn_1",
        type: "agentMessage",
        text: "read before checkpoint",
        position: { entry: 40, item: 0 },
      },
    ]);
    beforeCheckpointRead.snapshot = { incarnation: "inc-1", length: 41 };
    const checkpointSource = applyReadModel(original, hydrateThread(beforeCheckpointRead, ref, 11));
    const sourceItem = checkpointSource.history?.turns
      .flatMap((turn) => turn.items)
      .find((item) => item.id === "reasoning-chunks");
    expect(sourceItem?.pendingText).toEqual(["reasoning ", "chunks"]);
    expect(sourceItem?.reasoningSummaries).toEqual([["summary ", "chunks"]]);

    const encoded = currentTranscriptState(checkpointSource);
    if (!encoded) throw new Error("chunk-view history did not encode");
    const encodedItem = encoded.history.turns
      .flatMap((turn) => turn.items)
      .find((item) => item.id === "reasoning-chunks");
    if (!encodedItem?.pendingText || !encodedItem.reasoningSummaries?.[0]) {
      throw new Error("encoded reasoning chunk fields missing");
    }
    expect(Array.isArray(encodedItem.pendingText)).toBe(true);
    expect(encodedItem.pendingText).toEqual(["reasoning ", "chunks"]);
    expect(Array.isArray(encodedItem.reasoningSummaries[0])).toBe(true);
    expect(encodedItem.reasoningSummaries).toEqual([["summary ", "chunks"]]);
    expect(chunkViewBackingForTests(encodedItem.pendingText)).toBeUndefined();
    expect(chunkViewBackingForTests(encodedItem.reasoningSummaries[0])).toBeUndefined();

    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid chunk-view checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const afterCheckpointRead = readWithItems(ref, [
      {
        id: "read-after-checkpoint",
        turnId: "turn_1",
        type: "agentMessage",
        text: "read after checkpoint",
        position: { entry: 41, item: 0 },
      },
    ]);
    afterCheckpointRead.snapshot = { incarnation: "inc-1", length: 42 };
    const fresh = hydrateThread(afterCheckpointRead, ref, 12);
    const resumed = applyReadModel(restored, fresh);
    const uninterrupted = applyReadModel(checkpointSource, fresh);
    const resumedReasoning = resumed.history?.turns
      .flatMap((turn) => turn.items)
      .find((item) => item.id === "reasoning-chunks");

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(resumed.history?.turns.flatMap((turn) => turn.items).map((item) => item.id)).toEqual([
      "reasoning-chunks",
      "read-before-checkpoint",
      "read-after-checkpoint",
    ]);
    expect(resumedReasoning?.text).toBe("reasoning chunks");
    expect(resumedReasoning?.pendingText).toEqual(["reasoning ", "chunks"]);
    expect(resumedReasoning?.pendingText?.join("")).toBe("reasoning chunks");
    expect(resumedReasoning?.reasoningSummaries).toEqual([["summary ", "chunks"]]);
    expect(resumedReasoning?.reasoningSummaries?.[0]?.join("")).toBe("summary chunks");
    expect(
      resumed.history?.turns.flatMap((turn) => turn.items).filter((item) => item.id === "reasoning-chunks"),
    ).toHaveLength(1);
  });

  it("keeps delegate projection fencing for newer and stale updates after restoration", () => {
    const delegate = {
      runGeneration: 1,
      delegateId: "dlg_fence",
      ownerSessionId: "session-root",
      rootSessionId: "session-root",
      childSessionId: "session-child",
      transcriptRef: "local:child",
      type: "delegate",
      lifecycle: "ended",
      phase: "ended",
      status: "completed",
      resumable: true,
      needsAttention: false,
      projectionRevision: 2,
    } satisfies EvenerDelegateInfo;
    const original = hydrateThread(currentRead("local:delegate-fence"), "local:delegate-fence", 10);
    original.delegates = [delegate];
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid delegate checkpoint did not decode");
    const restored = threadModelFromCurrentState(decoded);
    const newer = { ...delegate, lifecycle: "running", phase: "running", status: "running", projectionRevision: 3 };
    const stale = { ...delegate, lifecycle: "failed", phase: "failed", status: "failed", projectionRevision: 1 };
    const update = (next: EvenerDelegateInfo) => ({
      method: "evener/delegate/updated" as const,
      params: { threadId: "thr_local:delegate-fence", ref: "local:delegate-fence", delegate: next },
    });

    const resumed = applyNotification(applyNotification(restored, update(newer), 11), update(stale), 12);
    const uninterrupted = applyNotification(applyNotification(original, update(newer), 11), update(stale), 12);

    expect(resumed.delegates).toEqual(uninterrupted.delegates);
    expect(resumed.delegates?.map(({ projectionRevision, status }) => ({ projectionRevision, status }))).toEqual([
      { projectionRevision: 3, status: "running" },
    ]);
  });

  it("retains invalidated history state and deferred pages without restoring local authority", () => {
    const original = hydrateThread(currentRead("local:deferred"), "local:deferred", 10);
    if (!original.history) throw new Error("versioned fixture history did not hydrate");
    original.history = {
      ...original.history,
      invalidatedAtGeneration: 2,
      awaited: { bootGeneration: "1", epoch: 2 },
      pendingIncarnation: "inc-2",
      deferredPages: [],
    };
    const deferredPage: ThreadTurnsListResponse = {
      data: [
        {
          id: "turn_old",
          itemsView: "full",
          status: "completed",
          items: [
            {
              id: "old-item",
              turnId: "turn_old",
              type: "agentMessage",
              text: "deferred page",
              position: { entry: 0, item: 0 },
            },
          ],
        },
      ],
      nextCursor: "older-20",
      bootGeneration: "1",
      epoch: 2,
      snapshot: { incarnation: "inc-2", length: 42 },
    };
    const withDeferredPage = mergeOlderItemPage(original, deferredPage);
    if (!withDeferredPage.history) throw new Error("deferred page lost versioned history");
    withDeferredPage.history = { ...withDeferredPage.history, failed: "read failed" };
    expect(withDeferredPage.history?.deferredPages).toHaveLength(1);
    expect(withDeferredPage.history?.invalidatedAtGeneration).toBe(2);
    expect(withDeferredPage.history?.awaited).toEqual({ bootGeneration: "1", epoch: 2 });
    expect(withDeferredPage.history?.pendingIncarnation).toBe("inc-2");
    expect(withDeferredPage.history?.failed).toBe("read failed");
    withDeferredPage.pendingMutations = [
      { clientMutationId: "local-only", method: "turn/start", executionState: "queued", projectionState: "pending" },
    ];
    const encoded = currentTranscriptState(withDeferredPage);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);
    expect(restored.history).toMatchObject({
      invalidatedAtGeneration: 2,
      awaited: { bootGeneration: "1", epoch: 2 },
      pendingIncarnation: "inc-2",
      failed: "read failed",
      deferredPages: [
        {
          data: [
            {
              id: "turn_old",
              items: [{ id: "old-item", text: "deferred page", position: { entry: 0, item: 0 } }],
            },
          ],
          nextCursor: "older-20",
          snapshot: { incarnation: "inc-2", length: 42 },
        },
      ],
    });
    const latest = currentRead("local:deferred");
    latest.bootGeneration = "1";
    latest.epoch = 2;
    latest.snapshot = { incarnation: "inc-2", length: 42 };
    latest.thread.turns = [
      {
        id: "turn_current",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "current-item",
            turnId: "turn_current",
            type: "agentMessage",
            text: "current window",
            position: { entry: 42, item: 0 },
          },
        ],
      },
    ];
    const authoritative = hydrateThread(latest, "local:deferred", 11);
    const reconciled = applyReadModel(restored, authoritative);
    const uninterrupted = applyReadModel(withDeferredPage, authoritative);

    expect(reconciled.turns).toEqual(uninterrupted.turns);
    expect(reconciled.history).toEqual(uninterrupted.history);
    expect(reconciled.olderCursor).toBe(uninterrupted.olderCursor);
    expect(reconciled.history?.deferredPages).toEqual([]);
    expect(reconciled.history?.incarnation).toBe("inc-2");
    expect(reconciled.history?.invalidatedAtGeneration).toBeUndefined();
    expect(reconciled.history?.awaited).toBeUndefined();
    expect(reconciled.history?.pendingIncarnation).toBeUndefined();
    expect(reconciled.history?.failed).toBeUndefined();
    expect(reconciled.history?.turns.map((turn) => turn.id)).toEqual(["turn_old", "turn_current"]);
    expect(reconciled.history?.turns[0]?.items[0]?.text).toBe("deferred page");
    expect(reconciled.history?.turns.flatMap((turn) => turn.items).map((item) => item.id)).toEqual([
      "old-item",
      "current-item",
    ]);
    expect(reconciled.olderCursor).toBe("older-40");
    expect(restored.pendingMutations).toBeUndefined();
    expect(Object.values(restored.capabilities).every((enabled) => !enabled)).toBe(true);
  });

  it("retires a checkpointed overlay when the same history update reaches both reducers", () => {
    const original = hydrateThread(currentRead("local:retire"), "local:retire", 10);
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded, { capabilities: original.capabilities });
    const update = {
      method: "history/updated" as const,
      params: {
        threadId: "thr_local:retire",
        ref: "local:retire",
        bootGeneration: "1",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 41 },
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "",
          },
        ],
        items: [
          {
            id: "a1",
            turnId: "turn_1",
            type: "agentMessage",
            text: "alpha",
            roundId: "r1",
            version: 41,
            position: { entry: 39, item: 0 },
          },
        ],
      },
    };

    const continued = applyNotification(original, update, 11);
    const resumed = applyNotification(restored, update, 11);
    expect(resumed.turns).toEqual(continued.turns);
    expect(resumed.overlay).toEqual({});
    expect(resumed.turns.flatMap((turn) => turn.items).map((item) => item.text)).toEqual(["alpha"]);
  });
});
