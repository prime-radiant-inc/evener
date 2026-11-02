import {
  buildEntityView,
  type DelegateEntityView,
  hydrateThread,
  parseActivityTree,
  type ThreadItem,
} from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { notificationWireItem } from "@evener/appwire-client/testing/notificationWireFixtures";
import { subagentCallItems, subagentOutcomesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { expect, test } from "vitest";
import { projectComicDelegates } from "./delegates";
import { projectComicConversation } from "./projection";

const delegateId = "dlg_02wMz5TxvSettleRace001";
const childId = "02wMz5TxvChildSession1";

function activity(ownerRef: string, id: string, childRef: string): DelegateEntityView {
  const entities = buildEntityView({
    sessionRef: "local:root",
    tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
    turns: [],
    stale: false,
    ended: false,
  });
  const entity = [...entities.values()].find((entity) => entity.kind === "delegate")!;
  return { ...entity, id: `${ownerRef}/${id}`, logicalId: id, ownerRef, open: { ref: childRef, parentRef: ownerRef } };
}

function recordedItems(kind: "launch" | "reply" | "notification", transcriptRef: string): ThreadItem[] {
  if (kind === "notification") {
    const notification = notificationWireItem("delegate-reported");
    // Modern packets carry no ref. Replay their recorded message in the SDK's
    // supported ref-bearing notification envelope, without inventing reply text.
    const text = notification.text!;
    const bodyStart = text.indexOf(">") + 1;
    const bodyEnd = text.lastIndexOf("</delegate-notification>");
    const packet = JSON.parse(text.slice(bodyStart, bodyEnd));
    return [
      {
        ...notification,
        steeringKind: "notification",
        text: `<delegate-notification delegate_id="dlg_1" transcript_ref="${transcriptRef}">${packet.message}</delegate-notification>`,
      },
    ];
  }
  const callId = kind === "launch" ? "call_delegate_1" : "call_send_2";
  return subagentCallItems()
    .filter((item) => item.callId === callId)
    .map((item) => {
      if (kind === "launch" && item.output) {
        return { ...item, output: item.output.replace(`local:${childId}`, transcriptRef) };
      }
      if (kind === "reply" && item.raw) return { ...item, raw: { ...item.raw, transcript_ref: transcriptRef } };
      return item;
    });
}

const cases = [
  { label: "local", host: "local", ref: `local:${childId}`, matches: true },
  { label: "source-local remote", host: "east", ref: `local:${childId}`, matches: true },
  { label: "qualified remote", host: "east", ref: `east:${childId}`, matches: true },
  { label: "wrong host", host: "east", ref: `west:${childId}`, matches: false },
  { label: "stale child", host: "east", ref: "local:previous-child", matches: false },
  { label: "wrong-host owner context", host: "east", ownerHost: "west", ref: `local:${childId}`, matches: false },
];

for (const kind of ["launch", "reply", "notification"] as const) {
  test.each(cases)(
    `${kind} resolves $label evidence only within its canonical owner`,
    ({ host, ownerHost = host, ref, matches }) => {
      const ownerRef = `${ownerHost}:root`;
      const id = kind === "notification" ? "dlg_1" : delegateId;
      const items = recordedItems(kind, ref);
      const model = hydrateThread(
        {
          thread: wireThread(`${host}:routing-alias`, {
            sessionId: "root",
            turns: [{ id: "turn_1", status: "completed", itemsView: "full", items }],
          }),
        },
        `${host}:routing-alias`,
        0,
      );
      const entity = activity(ownerRef, id, `${ownerHost}:${childId}`);
      const otherOwner = activity(`${host}:unrelated`, id, `${host}:${childId}`);
      const entities = new Map([
        [otherOwner.id, otherOwner],
        [entity.id, entity],
      ]);
      const before = JSON.stringify(model);
      const source = model.turns[0]!.items[0]!;

      if (kind !== "reply") {
        const strips = projectComicDelegates(model, entities, ownerRef);
        expect(strips).toHaveLength(1);
        expect(strips[0]!.entity).toBe(entity);
        expect
          .soft(strips[0]!.source)
          .toEqual(kind === "launch" && matches ? { itemId: source.id, sourceIndex: 0 } : undefined);
        expect(projectComicDelegates(model, new Map([[otherOwner.id, otherOwner]]))).toEqual([]);
      }
      if (kind !== "launch") {
        const entries = projectComicConversation(model, { ownerRef, entities });
        expect(entries).toHaveLength(1);
        expect(entries[0]!.persona?.key).toBe(matches ? JSON.stringify([ownerRef, id]) : undefined);
        expect(
          projectComicConversation(model, { ownerRef, entities: new Map([[otherOwner.id, otherOwner]]) })[0]!.persona,
        ).toBeUndefined();
      }
      // Qualification is comparison-only: literal output, notification text, raw
      // results, authorship and source coordinates all remain unchanged.
      expect(JSON.stringify(model)).toBe(before);
    },
  );
}

test.each(["local", "east"])("recorded modern notifications retain owned identity on %s", (host) => {
  const ownerRef = `${host}:root`;
  const notification = notificationWireItem("delegate-reported");
  const model = hydrateThread(
    {
      thread: wireThread(ownerRef, {
        sessionId: "root",
        turns: [
          {
            id: "turn_1",
            status: "completed",
            itemsView: "full",
            items: [
              { ...notification, steeringKind: "notification" },
              {
                id: "original-author",
                type: "agentMessage",
                text: "AUTHOR_PAYLOAD",
                authorship: {
                  author: {
                    kind: "agent",
                    sessionRef: "west:child",
                    ownerRef: "west:original",
                    delegateId: "dlg_author",
                  },
                  inherited: true,
                },
              },
            ],
          },
        ],
      }),
    },
    ownerRef,
    0,
  );
  const entity = activity(ownerRef, "dlg_1", `${host}:${childId}`);
  const entities = new Map([[entity.id, entity]]);
  const before = JSON.stringify(model);
  expect(projectComicDelegates(model, entities)[0]!.source).toBeUndefined();
  const entries = projectComicConversation(model, { ownerRef, entities });
  expect(entries[0]!.persona?.key).toBe(JSON.stringify([ownerRef, "dlg_1"]));
  expect(entries[1]).toMatchObject({
    persona: { key: '["west:original","dlg_author"]' },
    inherited: true,
    sourceItemId: "original-author",
  });
  expect(model.turns[0]!.items[0]!.text).toBe(notification.text);
  expect(JSON.stringify(model)).toBe(before);
});
