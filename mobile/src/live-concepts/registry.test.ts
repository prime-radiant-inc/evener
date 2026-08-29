import { describe, expect, expectTypeOf, it } from "vitest";
import type {
  ConversationFrameAction,
  LiveConceptIntent,
  RootOwnedIntent,
  RosterIntent,
  WorkIntent,
} from "./contract";
import { liveConceptRegistry } from "./registry";

describe("liveConceptRegistry", () => {
  it("contains exactly the three production concepts in display order", () => {
    expect(Object.keys(liveConceptRegistry)).toEqual([
      "stillwater",
      "constellation",
      "field-notes",
    ]);
  });

  it("keeps each module keyed by its own concept ID", () => {
    expect(liveConceptRegistry.stillwater.id).toBe("stillwater");
    expect(liveConceptRegistry.constellation.id).toBe("constellation");
    expect(liveConceptRegistry["field-notes"].id).toBe("field-notes");
  });

  it("requires Stillwater's shared conversation skin", () => {
    expect(liveConceptRegistry.stillwater.conversationSkin.id).toBe(
      "stillwater",
    );
  });

  it("requires Constellation's shared conversation skin", () => {
    expect(liveConceptRegistry.constellation.conversationSkin.id).toBe(
      "constellation",
    );
  });

  it("requires Field Notes' shared conversation skin", () => {
    expect(liveConceptRegistry["field-notes"].conversationSkin.id).toBe(
      "field-notes",
    );
  });
});

describe("live concept intent boundary", () => {
  it("is exactly the readonly neutral-frame and parent-owned unions", () => {
    type Expected =
      | ConversationFrameAction
      | RootOwnedIntent
      | RosterIntent
      | WorkIntent;
    expectTypeOf<LiveConceptIntent>().toEqualTypeOf<Expected>();
    expectTypeOf<
      Extract<ConversationFrameAction, { type: "toggleTool" }>
    >().toEqualTypeOf<{ readonly type: "toggleTool"; readonly key: string }>();
  });
});
