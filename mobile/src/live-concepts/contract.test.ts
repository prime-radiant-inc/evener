import { describe, expect, expectTypeOf, it } from "vitest";
import type {
  LiveComposerView,
  LiveConceptHostProps,
  LiveConceptIntent,
  LiveConceptModule,
  LiveConceptState,
} from "./contract";
import type {
  BoundedDisplayText,
  DisplayTone,
  LiveConversationView,
  NarrativeDisplayItem,
} from "./model";

const concepts = ["stillwater", "constellation", "field-notes"] as const;
const surfaces = ["sessions", "conversation", "work"] as const;

describe("live concept contract", () => {
  it("keeps the exact concept and milestone surface sets", () => {
    expect(concepts).toEqual(["stillwater", "constellation", "field-notes"]);
    expect(surfaces).toEqual(["sessions", "conversation", "work"]);
  });

  it("requires RootShell callbacks at the host boundary", () => {
    expectTypeOf<LiveConceptHostProps>().toHaveProperty(
      "onOpenConceptSwitcher",
    );
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onBack");
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onOpenNew");
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onOpenSettings");
    expectTypeOf<LiveConceptHostProps>().toHaveProperty("onOpenVoice");
  });

  it("has no scenario or synthetic-turn contract", () => {
    expectTypeOf<LiveConceptState>().not.toHaveProperty("scenario");
    expectTypeOf<LiveConceptState>().not.toHaveProperty("sourceFixture");
    expectTypeOf<LiveConceptState>().not.toHaveProperty("syntheticTurn");
  });

  it("keeps switcher opening distinct from concept selection", () => {
    const open: LiveConceptIntent = { type: "openConceptSwitcher" };
    const select: LiveConceptIntent = {
      type: "switchConcept",
      concept: "stillwater",
    };
    expect(open.type).not.toBe(select.type);
  });

  it("defines a module for one live renderer", () => {
    expectTypeOf<LiveConceptModule>().toHaveProperty("Renderer");
    expectTypeOf<LiveConceptModule>().toHaveProperty("id");
    expectTypeOf<LiveConceptModule>().toHaveProperty("label");
    expectTypeOf<LiveConceptModule["id"]>().toEqualTypeOf<
      (typeof concepts)[number]
    >();
  });

  it("exposes a setComposerMode intent for local composer-mode selection", () => {
    const intent: LiveConceptIntent = { type: "setComposerMode", mode: "send" };
    expect(intent.type).toBe("setComposerMode");
    type SetComposerMode = Extract<
      LiveConceptIntent,
      { type: "setComposerMode" }
    >;
    expectTypeOf<SetComposerMode["mode"]>().toEqualTypeOf<
      "send" | "steer" | "queue"
    >();
  });

  it("annotates the conversation view with display tone and nullable updated label", () => {
    expectTypeOf<LiveConversationView>().toHaveProperty("tone");
    expectTypeOf<LiveConversationView>().toHaveProperty("updatedLabel");
    expectTypeOf<LiveConversationView["tone"]>().toEqualTypeOf<DisplayTone>();
    expectTypeOf<
      LiveConversationView["updatedLabel"]
    >().toEqualTypeOf<BoundedDisplayText | null>();
  });

  it("requires mutation errors to use bounded display text", () => {
    expectTypeOf<
      LiveComposerView["error"]
    >().toEqualTypeOf<BoundedDisplayText | null>();
  });

  it("annotates transcript items with a question link and stable sequence label", () => {
    expectTypeOf<NarrativeDisplayItem>().toHaveProperty("questionKey");
    expectTypeOf<NarrativeDisplayItem>().toHaveProperty("sequence");
    expectTypeOf<NarrativeDisplayItem["questionKey"]>().toEqualTypeOf<
      string | null
    >();
    expectTypeOf<NarrativeDisplayItem["sequence"]>().toEqualTypeOf<string>();
  });

  it("defines a loadOlder intent distinct from openConversation", () => {
    const loadOlder: LiveConceptIntent = { type: "loadOlder" };
    const open: LiveConceptIntent = { type: "openConversation", key: "t1" };
    expect(loadOlder.type).toBe("loadOlder");
    expect(open.type).toBe("openConversation");
    expect(loadOlder.type).not.toBe(open.type);
    expectTypeOf<
      Extract<LiveConceptIntent, { type: "loadOlder" }>
    >().toEqualTypeOf<{ type: "loadOlder" }>();
  });
});
