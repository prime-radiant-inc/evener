import { beforeEach, describe, expect, test } from "vitest";
import { conversationPaneLifetime, disposeConversationPaneLifetime } from "../../../shell/paneLifetime";
import { workspaceStore } from "../../../shell/workspace";
import { readRefComposerSeat, writeRefComposerSeat } from "./draft";
import { retainedConversationComposer } from "./retainedComposer";

// Composer retention mirrors retainedTranscriptReadView: a conversation
// rendered in a cascade column keeps its composer state (draft, staged
// attachments, edit state) across the column collapsing to a spine and
// re-expanding, and dies with the pane lifetime that owns it.
describe("retainedConversationComposer", () => {
  beforeEach(() => {
    workspaceStore.setState({ panes: [], focusedPaneId: null });
  });

  function freshLifetime() {
    const record = { id: "p1", type: "session" as const, params: { ref: "local:owner" }, slot: "main" as const };
    workspaceStore.setState({ panes: [record], focusedPaneId: "p1" });
    return { lifetime: conversationPaneLifetime(record), record };
  }

  function composerOrThrow(lifetime: ReturnType<typeof conversationPaneLifetime>, ref: string) {
    const composer = retainedConversationComposer(lifetime, ref);
    if (composer === undefined) throw new Error(`expected a retained composer for ${ref}`);
    return composer;
  }

  test("returns the same live composer for a ref across calls", () => {
    const { lifetime } = freshLifetime();
    const first = composerOrThrow(lifetime, "local:child");
    expect(first.alive).toBe(true);
    expect(retainedConversationComposer(lifetime, "local:child")).toBe(first);
    const other = composerOrThrow(lifetime, "local:other");
    expect(other).not.toBe(first);
    expect(other.ref).toBe("local:other");
  });

  test("a disposed composer is not retained, and disposing the lifetime retires every ref", () => {
    const { lifetime, record } = freshLifetime();
    const child = composerOrThrow(lifetime, "local:child");
    child.dispose();
    expect(child.alive).toBe(false);
    expect(retainedConversationComposer(lifetime, "local:child")).not.toBe(child);

    const kept = composerOrThrow(lifetime, "local:kept");
    disposeConversationPaneLifetime(record);
    expect(kept.alive).toBe(false);
    expect(retainedConversationComposer(lifetime, "local:kept")).toBeUndefined();
  });
});

// A column composer writes under its lifetime's seat, so the column's
// conversation mapping must name that seat: a mapping left absent or on a
// seat no live pane holds (displaced by a collision, or closed) would hand a
// reloaded route a fresh seat and restore nothing. A mapping a live pane
// still holds is that owner's and stays.
describe("retained column seat mapping reconciliation", () => {
  test("adopts the lifetime seat when the stored mapping is absent or dead", () => {
    workspaceStore.setState({ panes: [], focusedPaneId: null });
    const record = { id: "p1", type: "session" as const, params: { ref: "local:owner" }, slot: "main" as const };
    workspaceStore.setState({ panes: [record], focusedPaneId: "p1" });
    const lifetime = conversationPaneLifetime(record);

    writeRefComposerSeat("local:child", "seat-displaced");
    expect(retainedConversationComposer(lifetime, "local:child")).toBeDefined();
    expect(readRefComposerSeat("local:child")).toBe(lifetime.compositionOwnerId);

    expect(retainedConversationComposer(lifetime, "local:unset")).toBeDefined();
    expect(readRefComposerSeat("local:unset")).toBe(lifetime.compositionOwnerId);
  });

  test("leaves a mapping a live pane holds to that owner", () => {
    workspaceStore.setState({ panes: [], focusedPaneId: null });
    const record = { id: "p1", type: "session" as const, params: { ref: "local:owner" }, slot: "main" as const };
    const holder = {
      id: "p2",
      type: "session" as const,
      params: { ref: "local:held" },
      slot: "secondary" as const,
      composerSeatId: "seat-live",
    };
    workspaceStore.setState({ panes: [record, holder], focusedPaneId: "p1" });
    const lifetime = conversationPaneLifetime(record);

    writeRefComposerSeat("local:held", "seat-live");
    expect(retainedConversationComposer(lifetime, "local:held")).toBeDefined();
    expect(readRefComposerSeat("local:held")).toBe("seat-live");
  });
});
