import {
  buildEntityView,
  delegateSendEarlierLabel,
  delegateSendEarlierResponses,
  delegateSendResponse,
  delegateSendTarget,
  hydrateThread,
  MEMORY_CONTEXT_EVENT_KIND,
  markItemTextOmitted,
  parseActivityTree,
  shippedConfig,
  type Thread,
  type ThreadItem,
  type ThreadModel,
} from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import {
  subagentOutcomesDelegatesResponse,
  subagentOutcomesResponse,
  subagentResumedDelegatesResponse,
  subagentWireStep,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { describe, expect, test } from "vitest";
import { type ComicItemEntry, projectComicConversation } from "./projection";

const inputText = "  **Literal** <script>never execute</script>\nline 2 👩🏽‍🚀  ";
const responseText = "First reply\n```ts\nconst x = 1;\n```";
const hiddenToolText = "INTERNAL TOOL BODY MUST STAY HIDDEN";
const inputImages = [{ src: "/s/root/images/u1", name: "drawing.png" }];
const toolImages = [{ src: "/s/root/images/t1", name: "screenshot.png", source: "read-file" }];
const humanID = "human-1";
const toolID = "tool-read";
const base = (turns: Thread["turns"]): ThreadModel =>
  hydrateThread({ thread: wireThread("local:root", { turns }) }, "local:root", 0);

const fixture = () =>
  base([
    {
      id: "turn-one",
      status: "completed",
      itemsView: "full",
      items: [
        {
          type: "userMessage",
          id: humanID,
          turnId: "turn-one",
          text: inputText,
          images: [{ type: "image", url: inputImages[0]!.src, name: inputImages[0]!.name }],
        },
        { type: "reasoning", id: "thought-one", turnId: "turn-one", text: "Thought A  🧭" },
        {
          type: "commandExecution",
          id: toolID,
          turnId: "turn-one",
          toolName: "read_file",
          callId: "call-read",
          output: hiddenToolText,
          outputImages: [{ url: toolImages[0]!.src, name: toolImages[0]!.name, source: toolImages[0]!.source }],
          status: "completed",
        },
        { type: "agentMessage", id: "reply-one", turnId: "turn-one", text: responseText, roundId: "r1" },
      ],
    },
    {
      id: "turn-two",
      status: "completed",
      itemsView: "full",
      items: [
        { type: "steering", id: "user-steer", turnId: "turn-two", source: "user", text: "  Please check again.  " },
        {
          type: "steering",
          id: "user-note",
          turnId: "turn-two",
          source: "user",
          steeringKind: "human-note",
          text: "Whiteboard: 🧪",
        },
        {
          type: "steering",
          id: "parent-instruction",
          turnId: "turn-two",
          source: "parent",
          text: "Parent asks child to check tests.",
        },
        { type: "systemMessage", id: "harness-notice", turnId: "turn-two", text: "Harness paused." },
        {
          type: "agentMessage",
          id: "communicate",
          turnId: "turn-two",
          callId: "call-communicate",
          text: "Update: ready.",
        },
        {
          type: "userMessage",
          id: "image-only",
          turnId: "turn-two",
          images: [{ type: "image", url: "/s/root/images/u2", name: "scan.png" }],
        },
      ],
    },
  ]);

describe("comic conversation projection", () => {
  test("keeps unknown system notices as source captions instead of dropping forward-compatible evidence", () => {
    const unknown = "Unrecognized harness notice.";
    const model = base([
      {
        id: "unknown",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "future",
            turnId: "unknown",
            type: "systemMessage",
            eventKind: "future_notice" as ThreadItem["eventKind"],
            text: unknown,
          },
        ],
      },
    ]);
    expect(projectComicConversation(model)).toMatchObject([
      { sourceItemId: "future", speaker: "source", kind: "caption", text: unknown },
    ]);
  });

  test("suppresses memory-context content but keeps harness and actionable notices", () => {
    const privateIndex = "Personal memory index: private page links.";
    const harness = "Harness paused while waiting for consent.";
    const warning = "Permission requires attention.";
    const model = base([
      {
        id: "notices",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "memory",
            turnId: "notices",
            type: "systemMessage",
            eventKind: MEMORY_CONTEXT_EVENT_KIND,
            text: privateIndex,
          },
          { id: "harness", turnId: "notices", type: "systemMessage", text: harness },
          { id: "warning", turnId: "notices", type: "systemMessage", eventKind: "warning", text: warning },
        ],
      },
    ]);
    const entries = projectComicConversation(model);
    expect(entries.map((entry) => [entry.sourceItemId, entry.text])).toEqual([
      ["harness", harness],
      ["warning", warning],
    ]);
    expect(JSON.stringify(entries)).not.toContain(privateIndex);
  });

  test("interleaved turns follow document positions instead of regrouping by turn", () => {
    const model = base([
      {
        id: "a",
        status: "completed",
        itemsView: "full",
        items: [
          { id: "a-first", turnId: "a", type: "userMessage", text: "First", position: { entry: 1, item: 0 } },
          { id: "a-last", turnId: "a", type: "agentMessage", text: "Last", position: { entry: 3, item: 0 } },
        ],
      },
      {
        id: "b",
        status: "completed",
        itemsView: "full",
        items: [{ id: "b-middle", turnId: "b", type: "userMessage", text: "Between", position: { entry: 2, item: 0 } }],
      },
    ]);
    expect(projectComicConversation(model).map((entry) => [entry.sourceItemId, entry.sourceIndex])).toEqual([
      ["a-first", 0],
      ["b-middle", 1],
      ["a-last", 2],
    ]);
  });

  test("preserves dialogue and tool images without tool text", () => {
    const model = fixture();
    const original = JSON.stringify(model);
    const preferences = JSON.stringify(shippedConfig());
    const entries = projectComicConversation(model);
    expect(entries.find((e) => e.sourceItemId === humanID)?.text).toBe(inputText);
    expect(entries.find((e) => e.sourceItemId === toolID)?.images).toEqual(toolImages);
    expect(entries.find((e) => e.sourceItemId === toolID)?.id).toBe(toolID);
    expect(entries.find((e) => e.sourceItemId === "image-only")?.images).toEqual([
      { src: "/s/root/images/u2", name: "scan.png" },
    ]);
    expect(entries.find((e) => e.sourceItemId === humanID)?.images).toEqual(inputImages);
    expect(entries.some((e) => e.text.includes(hiddenToolText))).toBe(false);
    expect(entries.map((e) => e.sourceItemId)).toEqual([
      humanID,
      "thought-one",
      toolID,
      "reply-one",
      "user-steer",
      "user-note",
      "parent-instruction",
      "harness-notice",
      "communicate",
      "image-only",
    ]);
    const itemEntries = entries.filter((entry): entry is ComicItemEntry => entry.sourceTurnId === undefined);
    expect(itemEntries.map((e) => e.sourceIndex)).toEqual(
      [...itemEntries.map((e) => e.sourceIndex)].sort((a, b) => a - b),
    );
    expect(entries.find((e) => e.sourceItemId === "user-steer")).toMatchObject({
      speaker: "human",
      kind: "dialogue",
      text: "  Please check again.  ",
    });
    for (const id of ["user-note", "parent-instruction", "harness-notice"]) {
      expect(entries.find((e) => e.sourceItemId === id)).toMatchObject({ kind: "caption", speaker: "source" });
    }
    expect(entries.find((e) => e.sourceItemId === "communicate")).toMatchObject({
      speaker: "assistant",
      text: "Update: ready.",
    });
    expect(JSON.stringify(model)).toBe(original);
    expect(JSON.stringify(shippedConfig())).toBe(preferences);
  });

  test("joins exact live thought paragraphs, replaces reset, preserves first thought/reply IDs", () => {
    const model = fixture();
    const firstTurn = model.turns[0]!;
    const thought = firstTurn.items.find((item) => item.id === "thought-one")!;
    const reply = firstTurn.items.find((item) => item.id === "reply-one")!;
    thought.roundId = "r1";
    thought.id = "stream:r1/0:reasoning";
    thought.reasoningSummaries = [["  alpha", " 👩🏽‍🚀"], ["  "], ["beta\n", "gamma  "]];
    thought.status = "inProgress";
    reply.id = "stream:r1/0:agentMessage";
    reply.pendingText = ["First", " reply"];
    const live = projectComicConversation(model);
    expect(live.find((e) => e.kind === "thought")?.text).toBe("  alpha 👩🏽‍🚀\n\nbeta\ngamma  ");
    const thoughtID = live.find((e) => e.kind === "thought")?.id;
    const replyID = live.find((e) => e.kind === "dialogue" && e.speaker === "assistant")?.id;
    thought.id = "stream:r1/1:reasoning";
    thought.reasoningSummaries = [["Replacement 🧭"]];
    reply.id = "stream:r1/1:agentMessage";
    expect(projectComicConversation(model).find((e) => e.kind === "thought")).toMatchObject({
      id: thoughtID,
      text: "Replacement 🧭",
    });
    thought.id = "recorded-thought";
    thought.status = "completed";
    reply.id = "recorded-reply";
    reply.pendingText = undefined;
    expect(projectComicConversation(model).find((e) => e.kind === "thought")?.id).toBe(thoughtID);
    expect(projectComicConversation(model).find((e) => e.sourceItemId === "recorded-reply")?.id).toBe(replyID);
    firstTurn.items.push({ ...thought, id: "second-thought", reasoningSummaries: [["second"]] });
    const settled = projectComicConversation(model);
    expect(settled.find((e) => e.sourceItemId === "second-thought")?.id).toBe("second-thought");
    thought.reasoningSummaries = [[" "], []];
    thought.text = "";
    expect(projectComicConversation(model).find((e) => e.sourceItemId === "recorded-thought")?.text).toBe("");
  });

  test("unavailable thought contents never leak from stale retained summaries", () => {
    const model = fixture();
    const thought = model.turns[0]!.items.find((item) => item.id === "thought-one")!;
    thought.reasoningSummaries = [["stale private content"]];
    markItemTextOmitted(thought);
    const projected = projectComicConversation(model).find((entry) => entry.sourceItemId === thought.id);
    expect(projected).toMatchObject({ kind: "thought", text: "" });
    expect(JSON.stringify(projected)).not.toContain("stale private content");
  });

  test("failed tools show typed failure without revealing descriptions and keep attributed images", () => {
    const description = "shell -c 'private command and arguments'";
    const image = { src: "/s/root/images/failed", name: "failure.png", source: "read-file" };
    const model = base([
      {
        id: "failed-tools",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "failed-shell",
            turnId: "failed-tools",
            type: "commandExecution",
            text: "",
            toolName: "shell",
            status: "failed",
            description,
          },
          {
            id: "failed-read",
            turnId: "failed-tools",
            type: "commandExecution",
            text: "",
            toolName: "read_file",
            status: "interrupted",
            description,
            outputImages: [{ url: image.src, name: image.name, source: image.source }],
          },
        ],
      },
    ]);
    const entries = projectComicConversation(model);
    const failed = entries.filter((entry) => entry.kind === "caption");
    expect(failed.map((entry) => entry.sourceItemId)).toEqual(["failed-shell", "failed-read"]);
    expect(failed.every((entry) => entry.text.trim().length > 0)).toBe(true);
    expect(entries.find((entry) => entry.sourceItemId === "failed-read" && entry.kind === "images")?.images).toEqual([
      image,
    ]);
    expect(JSON.stringify(entries)).not.toContain(description);
  });

  test("turn-level typed failure survives when its only ordinary tool text is suppressed", () => {
    const model = fixture();
    model.turns = [
      {
        id: "failed-turn",
        status: "failed",
        error: { message: "Runtime interrupted." },
        items: [
          {
            id: "routine",
            turnId: "failed-turn",
            type: "commandExecution",
            text: "",
            status: "completed",
            toolName: "shell",
            output: hiddenToolText,
          },
        ],
      },
    ];
    const entries = projectComicConversation(model);
    expect(entries).toMatchObject([
      { sourceItemId: "routine", kind: "caption", speaker: "source", text: "Runtime interrupted." },
    ]);
    expect(entries.some((entry) => entry.text.includes(hiddenToolText))).toBe(false);
  });

  test("turn-level typed failure remains visible after earlier dialogue in its turn", () => {
    const model = fixture();
    model.turns = [
      {
        id: "failed-after-dialogue",
        status: "failed",
        error: { message: "Runtime interrupted after request." },
        items: [
          { id: "request", turnId: "failed-after-dialogue", type: "userMessage", text: inputText },
          {
            id: "suppressed-tool",
            turnId: "failed-after-dialogue",
            type: "commandExecution",
            text: "",
            status: "completed",
            toolName: "shell",
            output: hiddenToolText,
          },
        ],
      },
    ];
    expect(projectComicConversation(model)).toMatchObject([
      { sourceItemId: "request", kind: "dialogue", text: inputText },
      { sourceItemId: "suppressed-tool", kind: "caption", text: "Runtime interrupted after request." },
    ]);
  });

  test("empty failed turn retains real turn ownership without inventing item position", () => {
    const error = "Failed before any item was recorded.";
    const model = base([
      {
        id: "earlier",
        status: "completed",
        itemsView: "full",
        items: [{ id: "earlier-item", turnId: "earlier", type: "userMessage", text: inputText }],
      },
      { id: "empty-failure", status: "failed", error: { message: error }, itemsView: "full", items: [] },
      {
        id: "later",
        status: "completed",
        itemsView: "full",
        items: [{ id: "later-item", turnId: "later", type: "agentMessage", text: responseText }],
      },
    ]);
    const entries = projectComicConversation(model);
    expect(entries.map((entry) => entry.sourceItemId).filter(Boolean)).toEqual(["earlier-item", "later-item"]);
    const turnErrors = entries.filter((entry) => entry.sourceTurnId === "empty-failure");
    expect(turnErrors).toHaveLength(1);
    expect(turnErrors[0]).toMatchObject({ kind: "caption", speaker: "source", text: error });
    expect(turnErrors[0]).not.toHaveProperty("sourceItemId");
    expect(turnErrors[0]).not.toHaveProperty("sourceIndex");
  });

  test("delegate send keeps separately labeled typed earlier update, failed reply and current failure", () => {
    const recorded = subagentWireStep("call_send_2");
    const update = "Check the drain before I finish.";
    const previousFailure = "Earlier generation failed.";
    const currentFailure = "Send itself failed.";
    const raw = {
      ...(recorded.raw as Record<string, unknown>),
      earlier_results: [
        { action: "update", output: update },
        { action: "reported", status: "failed", reason: previousFailure },
      ],
    };
    const item = { ...recorded, id: "recorded-send", raw, error: currentFailure, status: "failed" };
    const model = base([{ id: "send-turn", status: "completed", itemsView: "full", items: [] }]);
    model.turns[0]!.items.push(item);
    const earlier = delegateSendEarlierResponses(item);
    const entries = projectComicConversation(model);
    expect(entries.filter((entry) => entry.sourceItemId === item.id).map((entry) => entry.text)).toEqual([
      update,
      previousFailure,
      delegateSendResponse(recorded),
      currentFailure,
    ]);
    for (const [index, expected] of earlier.entries()) {
      expect(entries[index]?.sourceLabel).toContain(delegateSendEarlierLabel(expected, index, earlier.length));
    }
    expect(entries[0]?.delegateResult).toMatchObject({ kind: "earlierUpdate" });
    expect(entries[1]?.delegateResult).toMatchObject({ kind: "earlierReply", status: "failed" });
    expect(entries[2]?.id).toBe(item.id);
    expect(entries[3]?.delegateResult).toMatchObject({ kind: "failure", status: "failed" });
    expect(delegateSendTarget(item)).not.toBe("");
    expect(entries.every((entry) => entry.sourceLabel.includes(delegateSendTarget(item)))).toBe(true);
    expect(entries.map((entry) => entry.sourceIndex)).toEqual([0, 0, 0, 0]);
    expect(entries.every((entry) => entry.speaker === "source")).toBe(true);
  });

  test("attributes recorded delegate sends using actual entity evidence without borrowing a newer report", () => {
    const recorded = [
      subagentWireStep("call_delegate_1"),
      subagentWireStep("call_send_1"),
      subagentWireStep("call_send_2"),
    ];
    const model = fixture();
    model.turns[0]!.items.push(...recorded);
    const old = subagentOutcomesDelegatesResponse();
    const fresh = subagentResumedDelegatesResponse();
    const tree = parseActivityTree(subagentOutcomesResponse().data);
    expect(tree).not.toBeNull();
    const entities = buildEntityView({
      sessionRef: old.context.ref,
      tree: tree!,
      turns: model.turns,
      stale: false,
      ended: false,
    });
    const projected = projectComicConversation(model, { entities });
    const sends = projected.filter((entry) => recorded.slice(1).some((item) => item.id === entry.sourceItemId));
    expect(sends).toHaveLength(2);
    expect(sends.every((entry) => entry.kind === "caption" && entry.speaker === "source")).toBe(true);
    // A new domain snapshot cannot turn a historical send into the current report.
    const freshEntities = buildEntityView({
      sessionRef: fresh.context.ref,
      tree: (() => {
        const resumedTree = structuredClone(tree!);
        for (const recorded of fresh.delegates) {
          const entry = resumedTree.root.entries.find(
            (candidate) => candidate.kind === "delegate" && candidate.delegate.delegateId === recorded.delegateId,
          );
          if (entry?.kind === "delegate") Object.assign(entry.delegate, recorded);
        }
        return resumedTree;
      })(),
      turns: model.turns,
      stale: false,
      ended: false,
    });
    expect(fresh.delegates.find((d) => d.delegateId === "dlg_reported")?.runGeneration).toBeGreaterThan(
      old.delegates.find((d) => d.delegateId === "dlg_reported")?.runGeneration ?? 0,
    );
    const resumedEntity = [...freshEntities.values()].find(
      (entity) => entity.kind === "delegate" && entity.logicalId === "dlg_reported",
    );
    expect(resumedEntity?.kind === "delegate" && resumedEntity.row?.delegate.runGeneration).toBe(
      fresh.delegates.find((delegate) => delegate.delegateId === "dlg_reported")?.runGeneration,
    );
    const reprojected = projectComicConversation(model, { entities: freshEntities });
    expect(
      reprojected
        .filter((entry) => sends.some((send) => send.sourceItemId === entry.sourceItemId))
        .map((entry) => [entry.sourceItemId, entry.text]),
    ).toEqual(sends.map((entry) => [entry.sourceItemId, entry.text]));
    expect(
      reprojected.some(
        (entry) => entry.text === fresh.delegates.find((d) => d.delegateId === "dlg_reported")?.reportPreview,
      ),
    ).toBe(false);
  });

  test("historical delegate notifications retain attributed updates and earlier-generation reports", () => {
    const old = subagentOutcomesDelegatesResponse();
    const resumed = subagentResumedDelegatesResponse();
    const tree = parseActivityTree(subagentOutcomesResponse().data)!;
    const before = "Mid-work: inspect drain order.";
    const report = old.delegates.find((delegate) => delegate.delegateId === "dlg_reported")!.reportPreview!;
    const model = base([
      {
        id: "child-updates",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "old-update",
            type: "steering",
            turnId: "child-updates",
            source: "daemon",
            steeringKind: "notification",
            text: `<delegate-notification delegate_id="dlg_reported" kind="update">${before}</delegate-notification>`,
          },
          {
            id: "old-report",
            type: "steering",
            turnId: "child-updates",
            source: "daemon",
            steeringKind: "notification",
            text: `<delegate-notification delegate_id="dlg_reported" name="reported-delegate" kind="report">${JSON.stringify({ kind: "reported", message: report, metadata: { outcome: "completed" } })}</delegate-notification>`,
          },
        ],
      },
    ]);
    const entities = buildEntityView({
      sessionRef: old.context.ref,
      tree,
      turns: model.turns,
      stale: false,
      ended: false,
    });
    const entries = projectComicConversation(model, { entities });
    expect(entries.map((entry) => entry.text)).toEqual([before, report]);
    expect(entries.map((entry) => entry.persona?.badge)).toEqual(["dlg_reported", "dlg_reported"]);
    expect(entries.map((entry) => entry.persona?.ownerRef)).toEqual(["local:root", "local:root"]);
    expect(resumed.delegates.find((delegate) => delegate.delegateId === "dlg_reported")?.runGeneration).toBe(2);
    expect(projectComicConversation(model, { entities }).map((entry) => entry.text)).toEqual([before, report]);
  });
});
