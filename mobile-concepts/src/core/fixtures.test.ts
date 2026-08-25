import { describe, expect, it } from "vitest";
import { canonicalFixture } from "./fixtures";
import type { PrototypeFixture } from "./model";

function expectDeeplyFrozen(value: unknown): void {
  if (value === null || typeof value !== "object") return;
  expect(Object.isFrozen(value)).toBe(true);
  for (const child of Object.values(value)) expectDeeplyFrozen(child);
}

function unique(values: readonly string[]): boolean {
  return new Set(values).size === values.length;
}

function collectKeys(value: unknown, keys: string[] = []): string[] {
  if (value === null || typeof value !== "object") return keys;
  for (const [key, child] of Object.entries(value)) {
    keys.push(key);
    collectKeys(child, keys);
  }
  return keys;
}

function collectStrings(value: unknown, strings: string[] = []): string[] {
  if (typeof value === "string") strings.push(value);
  else if (Array.isArray(value)) {
    value.forEach((item) => {
      collectStrings(item, strings);
    });
  } else if (value !== null && typeof value === "object") {
    Object.values(value).forEach((item) => {
      collectStrings(item, strings);
    });
  }
  return strings;
}

describe("canonicalFixture", () => {
  it("has the exact deterministic foundation inventory", () => {
    expect(canonicalFixture.version).toBe(1);
    expect(canonicalFixture.sessions).toHaveLength(5);
    expect(canonicalFixture.transcript).toHaveLength(9);
    expect(canonicalFixture.questions).toHaveLength(2);
    expect(canonicalFixture.work).toHaveLength(7);
    expect(canonicalFixture.search).toHaveLength(5);
    expect(canonicalFixture.recentProjects).toHaveLength(3);
    expect(canonicalFixture.models).toHaveLength(3);
    expect(canonicalFixture.efforts).toEqual(["low", "medium", "high"]);
    expect(canonicalFixture.voiceSteps).toHaveLength(8);
    expect(canonicalFixture.hubs).toHaveLength(2);
  });

  it("uses unique collection IDs and valid cross references", () => {
    const fixture: PrototypeFixture = canonicalFixture;
    for (const collection of [
      fixture.sessions,
      fixture.transcript,
      fixture.questions,
      fixture.work,
      fixture.search,
      fixture.recentProjects,
      fixture.models,
      fixture.voiceSteps,
      fixture.hubs,
    ]) {
      expect(unique(collection.map(({ id }) => id))).toBe(true);
    }
    const sessions = new Set(fixture.sessions.map(({ id }) => id));
    const transcript = new Set(fixture.transcript.map(({ id }) => id));
    const questions = new Set(fixture.questions.map(({ id }) => id));
    const work = new Set(fixture.work.map(({ id }) => id));
    for (const item of fixture.transcript) {
      expect(sessions.has(item.sessionId)).toBe(true);
      if (item.kind === "question") {
        expect(questions.has(item.questionId)).toBe(true);
      }
    }
    const questionReferences = fixture.transcript.filter(
      (item) => item.kind === "question",
    );
    expect(
      questionReferences.map(({ sessionId, questionId }) => ({
        sessionId,
        questionId,
      })),
    ).toEqual([
      {
        sessionId: "session-mobile-release",
        questionId: "question-release-focus",
      },
      {
        sessionId: "session-mobile-release",
        questionId: "question-release-checks",
      },
    ]);
    for (const question of fixture.questions) {
      expect(
        questionReferences.filter(
          ({ questionId }) => questionId === question.id,
        ),
      ).toHaveLength(1);
    }
    for (const node of fixture.work) {
      expect(sessions.has(node.sessionId)).toBe(true);
      if (node.parentId !== null) expect(work.has(node.parentId)).toBe(true);
    }
    for (const document of fixture.search) {
      expect(sessions.has(document.sessionId)).toBe(true);
      if (document.itemId !== null) {
        expect(
          transcript.has(document.itemId) || work.has(document.itemId),
        ).toBe(true);
      }
    }
    for (const question of fixture.questions) {
      expect(unique(question.options.map(({ id }) => id))).toBe(true);
      expect(["single", "multiple"]).toContain(question.mode);
      expect(question.options.some(({ recommended }) => recommended)).toBe(
        true,
      );
    }
  });

  it("contains the required neutral content and no sensitive or ambient data", () => {
    expect(
      canonicalFixture.sessions.filter(({ state }) =>
        state.startsWith("needs-"),
      ),
    ).toHaveLength(2);
    expect(
      canonicalFixture.sessions.filter(({ state }) => state === "running"),
    ).toHaveLength(2);
    expect(canonicalFixture.transcript.map(({ kind }) => kind)).toEqual([
      "question",
      "question",
      "user",
      "assistant",
      "tool",
      "tool",
      "error",
      "attachment",
      "assistant",
    ]);
    expect(
      canonicalFixture.transcript.some(
        (item) => item.kind === "tool" && item.output.length > 300,
      ),
    ).toBe(true);
    expect(canonicalFixture.voiceSteps.map(({ state }) => state)).toEqual([
      "idle",
      "ready",
      "listening",
      "processing",
      "speaking",
      "interrupted",
      "denied",
      "error",
    ]);
    const keys = collectKeys(canonicalFixture);
    expect(
      keys.some((key) =>
        /^(?:token|authorizationurl|apikey|secret)$/i.test(key),
      ),
    ).toBe(false);
    for (const value of collectStrings(canonicalFixture)) {
      expect(value).not.toMatch(/(?:https?:)?\/\//i);
      expect(value).not.toMatch(/^(?:~\/|\/Users\/|\/home\/)/);
      expect(value).not.toMatch(/^(?:bearer\s+|basic\s+)/i);
      if (value.startsWith("/"))
        expect(value).toMatch(/^\/workspace\/(?:aurora|harbor)(?:\/|$)/);
    }
  });

  it("is deeply frozen", () => {
    expectDeeplyFrozen(canonicalFixture);
  });
});
