import { describe, expect, it } from "vitest";
import {
  applyNotification,
  applyReadModel,
  currentTranscriptState,
  decodeCurrentTranscriptState,
  hydrateThread,
  itemTextPresence,
  markItemTextOmitted,
  threadModelFromCurrentState,
} from "./reducer";
import { currentRead, textDelta } from "./testing/currentTranscript";
import { notificationWireItem } from "./testing/notificationWireFixtures";
import { subagentCallItems } from "./testing/subagentWireFixtures";
import { toolWireItems } from "./testing/toolWireFixtures";
import type { EvenerDelegateInfo, ThreadItem } from "./types.gen";

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
      '<delegate-notification delegate_id="dlg_1" name="Fix race in tree settle">{"kind":"reported","message":"Done: the settle pass now waits for the drain.\\n\\nTests: go test ./agent/... passes.","metadata":{"outcome":"completed","name":"Fix race in tree settle","task":"Find and fix the race between tree settle and the drain.","description":"Fix race in tree settle","agent_type":"general-purpose","run_started_at":"2026-09-28T20:00:00Z","run_ended_at":"2026-09-28T20:02:00Z"}}</delegate-notification>',
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
    const encoded = currentTranscriptState(started);
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
        delta: " chunk",
      },
    };

    const resumed = applyNotification(restored, next, 12);
    const uninterrupted = applyNotification(started, next, 12);
    const reasoning = resumed.turns[0]?.items.find((item) => item.id === "reasoning-live");

    expect(resumed.turns).toEqual(uninterrupted.turns);
    expect(reasoning?.text).toBe("first chunk");
    expect(reasoning?.reasoningSummaries).toEqual([["first chunk"]]);
    expect(resumed.turns[0]?.items.filter((item) => item.id === "reasoning-live")).toHaveLength(1);
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
      failed: "read failed",
      deferredPages: [
        {
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
        },
      ],
    };
    original.pendingMutations = [
      { clientMutationId: "local-only", method: "turn/start", executionState: "queued", projectionState: "pending" },
    ];
    const encoded = currentTranscriptState(original);
    if (!encoded) throw new Error("versioned fixture history did not encode");
    const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(encoded)));
    if (!decoded) throw new Error("valid current state did not decode");
    const restored = threadModelFromCurrentState(decoded);
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
    const reconciled = applyReadModel(restored, hydrateThread(latest, "local:deferred", 11));

    expect(reconciled.history?.deferredPages).toEqual([]);
    expect(reconciled.history?.incarnation).toBe("inc-2");
    expect(reconciled.history?.turns.map((turn) => turn.id)).toEqual(["turn_old", "turn_current"]);
    expect(reconciled.history?.turns[0]?.items[0]?.text).toBe("deferred page");
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
