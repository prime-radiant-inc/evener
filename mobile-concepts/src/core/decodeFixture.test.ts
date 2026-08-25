import { describe, expect, it } from "vitest";
import { decodeFixture } from "./decodeFixture";
import { canonicalFixture } from "./fixtures";
import type { FixtureDiagnostic, PrototypeFixture } from "./model";

function decode(value: unknown) {
  const diagnostics: FixtureDiagnostic[] = [];
  const result = decodeFixture(value, canonicalFixture, {
    report: (diagnostic) => diagnostics.push(diagnostic),
  });
  return { diagnostics, result };
}

function expectRejected(value: unknown, diagnostic: FixtureDiagnostic): void {
  const decoded = decode(value);
  expect(decoded.result).toBe(canonicalFixture);
  expect(decoded.diagnostics).toHaveLength(1);
  expect(decoded.diagnostics).toEqual([diagnostic]);
  const [reported] = decoded.diagnostics;
  expect(reported && Object.keys(reported)).toEqual(["code", "path"]);
  expect(reported?.code).toMatch(/^fixture-(?:invalid|version)$/);
  expect(reported?.path).toMatch(/^\$(?:\.[A-Za-z]+|\[\d+\])*$/);
  expect(JSON.stringify(decoded.diagnostics)).not.toContain(
    "bad-payload-marker",
  );
}

function mutableFixture(): Record<string, unknown> {
  return structuredClone(canonicalFixture) as unknown as Record<
    string,
    unknown
  >;
}

describe("decodeFixture", () => {
  it("passes the complete canonical shape through by identity", () => {
    expect(decode(canonicalFixture)).toEqual({
      diagnostics: [],
      result: canonicalFixture,
    });
    const clone = structuredClone(canonicalFixture) as PrototypeFixture;
    expect(decode(clone)).toEqual({ diagnostics: [], result: clone });
  });

  it.each([null, [], "bad-payload-marker", 1])(
    "rejects a non-record root",
    (value) => {
      expectRejected(value, { code: "fixture-invalid", path: "$" });
    },
  );

  it("rejects missing collections and wrong discriminants", () => {
    const { sessions: _sessions, ...missing } =
      structuredClone(canonicalFixture);
    expectRejected(missing, { code: "fixture-invalid", path: "$.sessions" });

    const wrong = structuredClone(canonicalFixture) as unknown as Record<
      string,
      unknown
    >;
    const transcript = wrong.transcript as Array<Record<string, unknown>>;
    Object.assign(transcript[0] ?? {}, { kind: "bad-payload-marker" });
    expectRejected(wrong, {
      code: "fixture-invalid",
      path: "$.transcript[0].kind",
    });
  });

  it("rejects dangling transcript, work, and search references", () => {
    const transcript = structuredClone(canonicalFixture) as unknown as Record<
      string,
      unknown
    >;
    Object.assign(
      (transcript.transcript as Array<Record<string, unknown>>)[0] ?? {},
      { sessionId: "bad-payload-marker" },
    );
    expectRejected(transcript, {
      code: "fixture-invalid",
      path: "$.transcript[0].sessionId",
    });

    const work = structuredClone(canonicalFixture) as unknown as Record<
      string,
      unknown
    >;
    Object.assign((work.work as Array<Record<string, unknown>>)[1] ?? {}, {
      parentId: "bad-payload-marker",
    });
    expectRejected(work, {
      code: "fixture-invalid",
      path: "$.work[1].parentId",
    });

    const search = structuredClone(canonicalFixture) as unknown as Record<
      string,
      unknown
    >;
    Object.assign((search.search as Array<Record<string, unknown>>)[0] ?? {}, {
      itemId: "bad-payload-marker",
    });
    expectRejected(search, {
      code: "fixture-invalid",
      path: "$.search[0].itemId",
    });
  });

  it.each([0, 2, 99])("rejects unsupported version %s", (version) => {
    const value = { ...structuredClone(canonicalFixture), version };
    expectRejected(value, { code: "fixture-version", path: "$.version" });
  });

  it("rejects unknown and credential-shaped extra fields without echoing them", () => {
    const unknown = {
      ...structuredClone(canonicalFixture),
      "bad-payload-marker": "bad-payload-marker",
    };
    expectRejected(unknown, { code: "fixture-invalid", path: "$" });

    const credential = structuredClone(canonicalFixture) as unknown as Record<
      string,
      unknown
    >;
    (credential.usage as Record<string, unknown>).apiKey = "bad-payload-marker";
    expectRejected(credential, {
      code: "fixture-invalid",
      path: "$.usage",
    });
  });

  it("accepts a null-prototype plain root record", () => {
    const value = Object.assign(Object.create(null), mutableFixture());
    expect(decode(value)).toEqual({ diagnostics: [], result: value });
  });

  it("rejects inherited and custom-prototype records", () => {
    const inherited = Object.assign(
      Object.create({ "bad-payload-marker": "bad-payload-marker" }),
      mutableFixture(),
    );
    expectRejected(inherited, { code: "fixture-invalid", path: "$" });

    const value = mutableFixture();
    const sessions = value.sessions as Array<Record<string, unknown>>;
    Object.setPrototypeOf(sessions[0] ?? {}, { inherited: true });
    expectRejected(value, {
      code: "fixture-invalid",
      path: "$.sessions[0]",
    });
  });

  it("rejects accessor and throwing getter records without invoking them", () => {
    const accessor = mutableFixture();
    Object.defineProperty(accessor, "sessions", {
      enumerable: true,
      get: () => canonicalFixture.sessions,
    });
    expectRejected(accessor, { code: "fixture-invalid", path: "$" });

    let invoked = false;
    const throwing = mutableFixture();
    Object.defineProperty(throwing, "sessions", {
      enumerable: true,
      get: () => {
        invoked = true;
        throw new Error("bad-payload-marker");
      },
    });
    expectRejected(throwing, { code: "fixture-invalid", path: "$" });
    expect(invoked).toBe(false);
  });

  it("contains throwing proxy reflection and property access", () => {
    const ownKeysProxy = new Proxy(mutableFixture(), {
      ownKeys: () => {
        throw new Error("bad-payload-marker");
      },
    });
    expectRejected(ownKeysProxy, { code: "fixture-invalid", path: "$" });

    const propertyProxy = new Proxy(mutableFixture(), {
      get: (_target, key) => {
        if (key === "sessions") throw new Error("bad-payload-marker");
        return Reflect.get(_target, key);
      },
    });
    expectRejected(propertyProxy, { code: "fixture-invalid", path: "$" });
  });

  it("rejects non-enumerable and symbol record keys", () => {
    const hidden = mutableFixture();
    Object.defineProperty(hidden, "bad-payload-marker", {
      enumerable: false,
      value: "bad-payload-marker",
    });
    expectRejected(hidden, { code: "fixture-invalid", path: "$" });

    const symbol = mutableFixture();
    Object.defineProperty(symbol, Symbol("bad-payload-marker"), {
      enumerable: true,
      value: "bad-payload-marker",
    });
    expectRejected(symbol, { code: "fixture-invalid", path: "$" });
  });

  it("rejects arrays with extra, sparse, accessor, or custom-prototype shape", () => {
    const extra = mutableFixture();
    Object.assign(extra.sessions as unknown[], {
      "bad-payload-marker": "bad-payload-marker",
    });
    expectRejected(extra, { code: "fixture-invalid", path: "$.sessions" });

    const sparse = mutableFixture();
    delete (sparse.sessions as unknown[])[0];
    expectRejected(sparse, { code: "fixture-invalid", path: "$.sessions" });

    const accessor = mutableFixture();
    Object.defineProperty(accessor.sessions as unknown[], "0", {
      enumerable: true,
      get: () => canonicalFixture.sessions[0],
    });
    expectRejected(accessor, { code: "fixture-invalid", path: "$.sessions" });

    const custom = mutableFixture();
    Object.setPrototypeOf(
      custom.sessions as unknown[],
      Object.create(Array.prototype),
    );
    expectRejected(custom, { code: "fixture-invalid", path: "$.sessions" });
  });

  it("rejects duplicate collection and question option IDs", () => {
    const collection = mutableFixture();
    const sessions = collection.sessions as Array<Record<string, unknown>>;
    Object.assign(sessions[1] ?? {}, { id: sessions[0]?.id });
    expectRejected(collection, {
      code: "fixture-invalid",
      path: "$.sessions[1].id",
    });

    const option = mutableFixture();
    const questions = option.questions as Array<Record<string, unknown>>;
    const options = questions[0]?.options as Array<Record<string, unknown>>;
    Object.assign(options[1] ?? {}, { id: options[0]?.id });
    expectRejected(option, {
      code: "fixture-invalid",
      path: "$.questions[0].options[1].id",
    });
  });

  it("rejects work cycles and dangling question references", () => {
    const cycle = mutableFixture();
    const work = cycle.work as Array<Record<string, unknown>>;
    Object.assign(work[0] ?? {}, { parentId: work[1]?.id });
    expectRejected(cycle, {
      code: "fixture-invalid",
      path: "$.work[0].parentId",
    });

    const question = mutableFixture();
    const transcript = question.transcript as Array<Record<string, unknown>>;
    Object.assign(transcript[0] ?? {}, {
      kind: "question",
      questionId: "bad-payload-marker",
    });
    delete transcript[0]?.body;
    expectRejected(question, {
      code: "fixture-invalid",
      path: "$.transcript[0].questionId",
    });
  });

  it("rejects orphaned and multiply referenced questions", () => {
    const orphaned = mutableFixture();
    const orphanedTranscript = orphaned.transcript as Array<
      Record<string, unknown>
    >;
    const orphanedIndex = orphanedTranscript.findIndex(
      (item) => item.questionId === "question-release-checks",
    );
    orphanedTranscript.splice(orphanedIndex, 1);
    expectRejected(orphaned, {
      code: "fixture-invalid",
      path: "$.questions[1].id",
    });

    const duplicated = mutableFixture();
    const duplicatedTranscript = duplicated.transcript as Array<
      Record<string, unknown>
    >;
    duplicatedTranscript.push({
      ...(duplicatedTranscript.find(
        (item) => item.questionId === "question-release-focus",
      ) ?? {}),
      id: "item-question-release-focus-duplicate",
    });
    expectRejected(duplicated, {
      code: "fixture-invalid",
      path: `$.transcript[${duplicatedTranscript.length - 1}].questionId`,
    });
  });

  it("rejects nested ordinary unknown fields and wrong scalar or array types", () => {
    const nested = mutableFixture();
    Object.assign(nested.usage as object, {
      ordinaryExtra: "bad-payload-marker",
    });
    expectRejected(nested, { code: "fixture-invalid", path: "$.usage" });

    const scalar = mutableFixture();
    Object.assign(scalar.usage as object, { tokens: "bad-payload-marker" });
    expectRejected(scalar, {
      code: "fixture-invalid",
      path: "$.usage.tokens",
    });

    const array = mutableFixture();
    array.sessions = {};
    expectRejected(array, { code: "fixture-invalid", path: "$.sessions" });
  });

  it.each([Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])(
    "rejects non-finite usage number %s",
    (number) => {
      const value = mutableFixture();
      Object.assign(value.usage as object, { contextPercent: number });
      expectRejected(value, {
        code: "fixture-invalid",
        path: "$.usage.contextPercent",
      });
    },
  );

  it.each([
    ["contextPercent", -1, "$.usage.contextPercent"],
    ["contextPercent", 101, "$.usage.contextPercent"],
    ["tokens", -1, "$.usage.tokens"],
    ["tokens", 1.5, "$.usage.tokens"],
  ])("rejects usage bound %s=%s", (field, number, path) => {
    const value = mutableFixture();
    Object.assign(value.usage as object, { [field]: number });
    expectRejected(value, { code: "fixture-invalid", path });
  });

  it.each([-1, 101])("rejects voice level bound %s", (level) => {
    const value = mutableFixture();
    const voiceSteps = value.voiceSteps as Array<Record<string, unknown>>;
    Object.assign(voiceSteps[0] ?? {}, { level });
    expectRejected(value, {
      code: "fixture-invalid",
      path: "$.voiceSteps[0].level",
    });
  });
});
