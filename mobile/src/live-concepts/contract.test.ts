import { describe, expect, expectTypeOf, it } from "vitest";
import type {
  LiveConceptHostProps,
  LiveConceptIntent,
  LiveConceptModule,
  LiveConceptState,
} from "./contract";

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
});
