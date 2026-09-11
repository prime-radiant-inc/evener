import { describe, expect, expectTypeOf, test } from "vitest";
import type {
  EvenerDiagnostics,
  EvenerSkillDiagnostic,
  EvenerSkillInfo,
  InputItem,
  ThreadCapabilities,
} from "./types.gen";

// Canonical skill selections are shape-only: a skill item rides the existing
// InputItem.name field with nothing else on the wire, and the server rejects
// any raw body/path/text smuggled beside it (appwire's InputItem.UnmarshalJSON
// allows exactly "type" and "name" for skill items). These tests pin the
// serialized shape the composer must produce and the generated capability and
// catalog types it reads before offering selection.

describe("canonical skill input serialization", () => {
  test("a skill selection serializes to exactly type and name", () => {
    const selection: InputItem = { type: "skill", name: "pkg:probe" };
    expect(JSON.stringify(selection)).toBe('{"type":"skill","name":"pkg:probe"}');
  });

  test("a mixed payload keeps original text alongside the canonical skill selection", () => {
    const items: InputItem[] = [
      { type: "text", text: "REQUEST_55a" },
      { type: "skill", name: "pkg:probe" },
      { type: "image", mediaType: "image/png", data: "AQI=" },
    ];
    const wire = JSON.parse(JSON.stringify(items));
    expect(wire).toEqual([
      { type: "text", text: "REQUEST_55a" },
      { type: "skill", name: "pkg:probe" },
      { type: "image", mediaType: "image/png", data: "AQI=" },
    ]);
  });
});

describe("generated skill input contract types", () => {
  test("ThreadCapabilities.skillInput is an optional boolean", () => {
    expectTypeOf<ThreadCapabilities>().toHaveProperty("skillInput").toEqualTypeOf<boolean | undefined>();
  });

  test("EvenerSkillInfo carries the invocation controls without omission", () => {
    expectTypeOf<EvenerSkillInfo>().toHaveProperty("disableModelInvocation").toEqualTypeOf<boolean>();
    expectTypeOf<EvenerSkillInfo>().toHaveProperty("userInvocable").toEqualTypeOf<boolean>();
    expectTypeOf<EvenerSkillInfo>().toHaveProperty("available").toEqualTypeOf<boolean>();
    expectTypeOf<EvenerSkillInfo>().toHaveProperty("allowedTools").toEqualTypeOf<string[] | undefined>();
  });

  test("EvenerSkillDiagnostic carries the discovery detail fields", () => {
    expectTypeOf<EvenerSkillDiagnostic>().toHaveProperty("category").toEqualTypeOf<string>();
    expectTypeOf<EvenerSkillDiagnostic>().toHaveProperty("name").toEqualTypeOf<string | undefined>();
    expectTypeOf<EvenerSkillDiagnostic>().toHaveProperty("source").toEqualTypeOf<string>();
    expectTypeOf<EvenerSkillDiagnostic>().toHaveProperty("otherSource").toEqualTypeOf<string | undefined>();
    expectTypeOf<EvenerSkillDiagnostic>().toHaveProperty("field").toEqualTypeOf<string | undefined>();
    expectTypeOf<EvenerSkillDiagnostic>().toHaveProperty("message").toEqualTypeOf<string>();
    expectTypeOf<EvenerDiagnostics>()
      .toHaveProperty("skillDiagnostics")
      .toEqualTypeOf<EvenerSkillDiagnostic[] | undefined>();
  });
});
