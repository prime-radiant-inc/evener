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
  expect(decoded.diagnostics).toEqual([diagnostic]);
  const [reported] = decoded.diagnostics;
  expect(reported && Object.keys(reported)).toEqual(["code", "path"]);
  expect(JSON.stringify(decoded.diagnostics)).not.toContain(
    "bad-payload-marker",
  );
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
});
