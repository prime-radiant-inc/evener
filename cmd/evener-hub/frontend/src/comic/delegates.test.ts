import {
  buildEntityView,
  hydrateThread,
  parseActivityTree,
  projectSessionActivity,
  type SessionActivityContext,
  SessionActivityStore,
  type ThreadModel,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import {
  subagentOutcomesDelegatesResponse,
  subagentOutcomesResponse,
  subagentWireStep,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { expect, test } from "vitest";
import { comicDelegatePersona } from "./cast";
import { comicActivityContext, projectComicDelegates } from "./delegates";
import { projectComicConversation } from "./projection";

const model = (): ThreadModel =>
  hydrateThread({ thread: wireThread("local:root", { sessionId: "root" }) }, "local:root", 0);
const entityMap = (m: ThreadModel) =>
  buildEntityView({
    sessionRef: m.ref,
    tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
    delegates: m.delegates,
    turns: m.turns,
    stale: false,
    ended: false,
  });

test("projects recorded owned outcomes once and rejects ambiguous and wrong-owner identities", () => {
  const m = model();
  const entities = entityMap(m);
  const strips = projectComicDelegates(m, entities);
  expect(strips.map((s) => s.delegateId).sort()).toEqual(["dlg_failed", "dlg_reported", "dlg_stopped"]);
  expect(strips.every((s) => !s.source && s.ownerRef === "local:root")).toBe(true);
  const reported = strips.find((s) => s.delegateId === "dlg_reported")!;
  entities.set("ambiguous", {
    ...reported.entity,
    id: "ambiguous",
    open: { ref: "remote:other/child", parentRef: "local:root" },
  });
  expect(projectComicDelegates(m, entities).map((s) => s.delegateId)).not.toContain("dlg_reported");
  expect(projectComicDelegates({ ...m, ref: "remote:else/root" }, entities)).toEqual([]);
});

test("positions the first loaded canonical receipt without treating its status as current", () => {
  const m = model();
  const receipt = subagentWireStep("call_delegate_1");
  const payload = JSON.parse(receipt.output!);
  const recorded = entityMap(m).values().next().value!;
  if (recorded.kind !== "delegate") throw new Error("fixture kind");
  const entity = {
    ...recorded,
    id: "receipt-identity",
    logicalId: payload.delegate_id,
    open: { ref: payload.transcript_ref, parentRef: m.ref },
  };
  const entities = new Map([[entity.id, entity]]);
  m.turns = [
    {
      id: "receipt-turn",
      status: "completed",
      items: [
        { ...receipt, id: "first-receipt" },
        { ...receipt, id: "repeat-receipt" },
      ],
    },
  ];
  const strips = projectComicDelegates(m, entities);
  expect(strips).toHaveLength(1);
  expect(strips[0]?.source).toEqual({ itemId: "first-receipt", sourceIndex: 0 });
  expect(strips[0]?.entity).toBe(entity);
  expect(projectComicDelegates(m, new Map())).toEqual([]);
});

test("locates multiple launches in one turn but not a later completion or completion-only history", () => {
  const m = model();
  const receipt = subagentWireStep("call_delegate_1");
  const first = entityMap(m).values().next().value!;
  if (first.kind !== "delegate") throw new Error("fixture kind");
  const firstEntity = {
    ...first,
    id: "first",
    logicalId: "dlg_first",
    open: { ref: "local:child-first", parentRef: m.ref },
  };
  const secondEntity = {
    ...first,
    id: "second",
    logicalId: "dlg_second",
    open: { ref: "local:child-second", parentRef: m.ref },
  };
  const entities = new Map([
    [firstEntity.id, firstEntity],
    [secondEntity.id, secondEntity],
  ]);
  const launch = (id: string, delegateId: string, childRef: string) => ({
    ...receipt,
    id,
    output: JSON.stringify({ ...JSON.parse(receipt.output!), delegate_id: delegateId, transcript_ref: childRef }),
  });
  const firstLaunch = launch("first-launch", firstEntity.logicalId, firstEntity.open.ref);
  const secondLaunch = launch("second-launch", secondEntity.logicalId, secondEntity.open.ref);
  const completion = {
    id: "completion",
    turnId: "turn",
    type: "steering",
    steeringKind: "notification",
    text: `<delegate-notification delegate_id="${firstEntity.logicalId}" transcript_ref="${firstEntity.open.ref}">done</delegate-notification>`,
  };
  m.turns = [{ id: "turn", status: "completed", items: [firstLaunch, secondLaunch, completion] }];
  const strips = projectComicDelegates(m, entities);
  expect(strips.find((strip) => strip.delegateId === firstEntity.logicalId)?.source?.itemId).toBe(firstLaunch.id);
  expect(strips.find((strip) => strip.delegateId === secondEntity.logicalId)?.source?.itemId).toBe(secondLaunch.id);
  m.turns = [{ id: "later", status: "completed", items: [completion] }];
  expect(projectComicDelegates(m, entities).every((strip) => strip.source === undefined)).toBe(true);
});

test("bounded creation result retains its child ref while identity-only data cannot locate a strip", () => {
  const m = model();
  const receipt = subagentWireStep("call_delegate_1");
  const recorded = entityMap(m).values().next().value!;
  if (recorded.kind !== "delegate") throw new Error("fixture kind");
  const id = "dlg_bounded";
  const childRef = "local:bounded-child";
  const entity = { ...recorded, id: "bounded", logicalId: id, open: { ref: childRef, parentRef: m.ref } };
  const minimal = {
    delegate_id: id,
    child_session_id: "bounded-child",
    type: "delegate",
    status: "running",
    transcript_ref: childRef,
  };
  const result = { ...receipt, id: "bounded-result", output: JSON.stringify(minimal) };
  m.turns = [{ id: "turn", status: "completed", items: [result] }];
  const entities = new Map([[entity.id, entity]]);
  expect(projectComicDelegates(m, entities)[0]?.source?.itemId).toBe(result.id);
  m.turns = [{ id: "turn", status: "completed", items: [{ ...result, output: JSON.stringify({ delegate_id: id }) }] }];
  expect(projectComicDelegates(m, entities)[0]?.source).toBeUndefined();
});

test("a resumed generation never inherits the recorded previous report", () => {
  const m = model();
  const recorded = subagentOutcomesDelegatesResponse();
  const empty = new SessionActivityStore(new FakeClient("ready"), m.ref).getSnapshot();
  const tree = projectSessionActivity({
    ...empty,
    context: recorded.context,
    delegates: { ...empty.delegates, context: recorded.context, rows: recorded.delegates, complete: true },
  }).tree!;
  const entities = () =>
    buildEntityView({ sessionRef: m.ref, tree, delegates: m.delegates, turns: m.turns, stale: false, ended: false });
  const previous = projectComicDelegates(m, entities()).find((strip) => strip.delegateId === "dlg_reported")!;
  expect(previous.entity.row?.delegate.reportPreview).toBeTruthy();
  const resumed = {
    delegateId: previous.delegateId,
    transcriptRef: previous.childRef,
    ownerSessionId: "root",
    rootSessionId: "root",
    childSessionId: previous.childRef.slice("local:".length),
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    resumable: true,
    needsAttention: false,
    projectionRevision: 1,
    runGeneration: 3,
  };
  m.delegates = [resumed];
  const current = projectComicDelegates(m, entities()).find((strip) => strip.key === previous.key)!;
  expect(current.entity.row).toBeUndefined();
  expect(current.entity.stable).toBe(resumed);
  expect(current.entity.stable?.runGeneration).toBe(3);
  expect(JSON.stringify(current.entity)).not.toContain(previous.entity.row?.delegate.reportPreview);
});

test("adopts only proven direct ancestry and attributes instructions to the immediate parent", () => {
  const context: SessionActivityContext = {
    ref: "remoteHost:leaf",
    sessionId: "leaf",
    rootRef: "remoteHost:root",
    parentRef: "remoteHost:parent",
    delegateId: "dlg_leaf",
    delegateName: "CANONICAL_LEAF",
    ancestors: [
      { ref: "remoteHost:root", sessionId: "root", title: "ROOT" },
      {
        ref: "remoteHost:parent",
        sessionId: "parent",
        delegateId: "dlg_parent",
        delegateName: "CANONICAL_PARENT",
        title: "DESCRIPTION_NOT_A_NAME",
      },
    ],
    ancestryKnown: true,
    epoch: "fixture",
    availability: "live",
  };
  const entities = new Map();
  expect(comicActivityContext(context.ref, { ...context, ancestryKnown: false }, entities).assistant).toBeUndefined();
  expect(comicActivityContext("local:leaf", context, entities).assistant).toBeUndefined();
  const resolved = comicActivityContext(context.ref, context, entities);
  expect.soft(resolved.assistant).toEqual(comicDelegatePersona(context.parentRef!, "dlg_leaf", "CANONICAL_LEAF"));
  expect.soft(resolved.parent).toEqual(comicDelegatePersona(context.rootRef, "dlg_parent", "CANONICAL_PARENT"));
  const unnamed = comicActivityContext(
    context.ref,
    {
      ...context,
      delegateName: undefined,
      ancestors: context.ancestors.map((ancestor) => ({ ...ancestor, delegateName: undefined })),
    },
    entities,
  );
  expect(unnamed.assistant).toEqual(comicDelegatePersona(context.parentRef!, "dlg_leaf"));
  expect.soft(unnamed.parent).toEqual(comicDelegatePersona(context.rootRef, "dlg_parent"));
  const m = hydrateThread(
    {
      thread: wireThread(context.ref, {
        turns: [
          {
            id: "t",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: "instruction",
                type: "steering",
                source: "parent",
                text: "INSTRUCTION_SENTINEL",
                authorship: {
                  author: {
                    kind: "agent",
                    sessionRef: "remoteHost:parent",
                    ownerRef: "remoteHost:root",
                    delegateId: "dlg_parent",
                    delegateName: "CANONICAL_PARENT",
                  },
                  inherited: false,
                },
              },
              {
                id: "answer",
                type: "agentMessage",
                text: "ANSWER_SENTINEL",
                authorship: {
                  author: {
                    kind: "agent",
                    sessionRef: "remoteHost:leaf",
                    ownerRef: "remoteHost:parent",
                    delegateId: "dlg_leaf",
                    delegateName: "CANONICAL_LEAF",
                  },
                  inherited: false,
                },
              },
            ],
          },
        ],
      }),
    },
    context.ref,
    0,
  );
  const entries = projectComicConversation(m, resolved);
  expect(entries[0]).toMatchObject({ speaker: "source", kind: "caption", persona: resolved.parent });
  expect(entries[1]?.persona).toEqual(resolved.assistant);
});

test("attributes an idle child's user-message instruction to its proven parent, never the human", () => {
  const ref = "local:leaf";
  const context: SessionActivityContext = {
    ref,
    sessionId: "leaf",
    rootRef: "local:root",
    parentRef: "local:parent",
    delegateId: "dlg_leaf",
    ancestors: [
      { ref: "local:root", sessionId: "root", title: "ROOT" },
      {
        ref: "local:parent",
        sessionId: "parent",
        delegateId: "dlg_parent",
        delegateName: "NAMED_PARENT",
        title: "DESCRIPTION_NOT_A_NAME",
      },
    ],
    ancestryKnown: true,
    epoch: "fixture",
    availability: "live",
  };
  const m = hydrateThread(
    {
      thread: wireThread(ref, {
        turns: [
          {
            id: "parent-input",
            status: "completed",
            itemsView: "full",
            items: [
              {
                id: "instruction",
                type: "userMessage",
                text: "COMIC_PARENT_INSTRUCTION literal parent steering",
                authorship: {
                  author: {
                    kind: "agent",
                    sessionRef: "local:parent",
                    ownerRef: "local:root",
                    delegateId: "dlg_parent",
                    delegateName: "NAMED_PARENT",
                  },
                  inherited: false,
                },
              },
            ],
          },
        ],
      }),
    },
    ref,
    0,
  );
  const proven = comicActivityContext(ref, context, new Map());
  expect(projectComicConversation(m, proven)[0]).toMatchObject({
    kind: "caption",
    speaker: "source",
    persona: comicDelegatePersona("local:root", "dlg_parent", "NAMED_PARENT"),
    text: "COMIC_PARENT_INSTRUCTION literal parent steering",
  });
  expect(projectComicConversation(m)[0]?.persona).toEqual(
    comicDelegatePersona("local:root", "dlg_parent", "NAMED_PARENT"),
  );
  m.turns[0]!.items[0]!.authorship = undefined;
  expect(projectComicConversation(m, proven)[0]).toMatchObject({ kind: "caption", speaker: "source" });
  expect(projectComicConversation(m, proven)[0]?.persona).toBeUndefined();
});

test("same names and unnamed delegates keep distinct owned identities, unresolved refs stay captions", () => {
  const m = model();
  const entities = entityMap(m);
  for (const [key, entity] of entities) {
    if (entity.kind === "delegate") entities.set(key, { ...entity, name: "SAME_NAME" });
  }
  const strips = projectComicDelegates(m, entities);
  expect(new Set(strips.map((strip) => strip.key)).size).toBe(3);
  expect(new Set(strips.map((strip) => strip.persona.badge)).size).toBe(3);
  expect(strips.every((strip) => strip.persona.name === "SAME_NAME")).toBe(true);
  const first = strips[0]!;
  entities.set(first.entity.id, { ...first.entity, name: "", open: { ref: "", parentRef: m.ref } });
  expect(projectComicDelegates(m, entities).some((strip) => strip.delegateId === first.delegateId)).toBe(false);
  m.turns = hydrateThread(
    {
      thread: wireThread(m.ref, {
        turns: [
          {
            id: "unresolved",
            status: "completed",
            itemsView: "full",
            items: [notificationWireItem("delegate-failed-unnamed")],
          },
        ],
      }),
    },
    m.ref,
    0,
  ).turns;
  const captions = projectComicConversation(m, { entities: new Map() });
  expect(captions.length).toBeGreaterThan(0);
  expect(captions.every((entry) => entry.kind === "caption" && entry.speaker === "source")).toBe(true);
  expect(comicDelegatePersona("remote:host/root", first.delegateId).key).not.toBe(first.key);
});
