import { describe, expect, it, vi } from "vitest";
import { WireError } from "./errors";
import { decodeJobOutputText, forEachJobOutputScalar, jobOutputPrunedBounds, parseJobOutputPage } from "./jobOutput";

const page = {
  offsetBytes: 91,
  bytesReturned: 9,
  totalBytes: 100,
  retainedStartBytes: 10,
  encoding: "utf8",
  data: "日本語",
};

describe("job output raw pages", () => {
  it("preserves source offsets and explicit zero bounds", () => {
    expect(parseJobOutputPage(page)).toEqual({
      ...page,
      bytes: new Uint8Array([0xe6, 0x97, 0xa5, 0xe6, 0x9c, 0xac, 0xe8, 0xaa, 0x9e]),
    });
    expect(
      parseJobOutputPage({
        offsetBytes: 0,
        bytesReturned: 0,
        totalBytes: 0,
        retainedStartBytes: 0,
        encoding: "utf8",
        data: "",
      })?.bytes,
    ).toEqual(new Uint8Array());
    expect(parseJobOutputPage({ ...page, totalBytes: Number.MAX_SAFE_INTEGER })).not.toBeNull();
  });

  it("accepts exact split bytes without repairing or dropping them", () => {
    expect(
      parseJobOutputPage({
        offsetBytes: 3,
        bytesReturned: 3,
        totalBytes: 6,
        retainedStartBytes: 0,
        encoding: "base64",
        data: "qWNk",
      })?.bytes,
    ).toEqual(new Uint8Array([0xa9, 0x63, 0x64]));
    expect(
      parseJobOutputPage({
        offsetBytes: 0,
        bytesReturned: 0,
        totalBytes: 6,
        retainedStartBytes: 0,
        encoding: "utf8",
        data: "",
      })?.bytes,
    ).toEqual(new Uint8Array());
  });

  it.each(["offsetBytes", "bytesReturned", "totalBytes", "retainedStartBytes"])("rejects malformed %s", (field) => {
    for (const value of [
      -1,
      1.5,
      Number.NaN,
      Number.POSITIVE_INFINITY,
      Number.MAX_SAFE_INTEGER + 1,
      null,
      "0",
      undefined,
    ]) {
      expect(parseJobOutputPage({ ...page, [field]: value })).toBeNull();
    }
  });

  it.each([
    { bytesReturned: 8 },
    { offsetBytes: 92 },
    { retainedStartBytes: 92 },
    { retainedStartBytes: 101 },
    { encoding: "gzip" },
    { encoding: null },
    { data: "\ud800" },
    { data: null },
    { bytesReturned: 65537, totalBytes: 100000, data: "a".repeat(65537) },
  ])("rejects fabricated counts or invalid bytes, case %#", (change) => {
    expect(parseJobOutputPage({ ...page, ...change })).toBeNull();
  });

  it("rejects old tails, missing fields and arrays", () => {
    expect(parseJobOutputPage({ tail: "日本語", totalBytes: 100, retainedStart: 91, truncated: true })).toBeNull();
    for (const field of Object.keys(page)) {
      const incomplete: Record<string, unknown> = { ...page };
      delete incomplete[field];
      expect(parseJobOutputPage(incomplete)).toBeNull();
    }
    expect(parseJobOutputPage(Object.assign([], page))).toBeNull();
    expect(parseJobOutputPage(null)).toBeNull();
  });

  it.each(["a", "YQ=", "YQ===", "YR==", "YWJ=", "YQ--", "YQ==\n", "====", "YQ==YQ=="])(
    "rejects noncanonical base64 %j",
    (data) => {
      expect(
        parseJobOutputPage({
          offsetBytes: 0,
          bytesReturned: 1,
          totalBytes: 1,
          retainedStartBytes: 0,
          encoding: "base64",
          data,
        }),
      ).toBeNull();
    },
  );

  it("rejects oversized encoded payloads even if the count is short", () => {
    expect(parseJobOutputPage({ ...page, encoding: "base64", data: "AAAA".repeat(22000) })).toBeNull();
    expect(parseJobOutputPage({ ...page, data: "😀".repeat(20000) })).toBeNull();
    const data = "a".repeat(65536);
    expect(
      parseJobOutputPage({
        offsetBytes: 0,
        bytesReturned: 65536,
        totalBytes: 65536,
        retainedStartBytes: 0,
        encoding: "utf8",
        data,
      })?.bytes.length,
    ).toBe(65536);
  });

  it("decodes without browser or Node byte globals", () => {
    for (const name of ["atob", "Buffer", "TextDecoder", "TextEncoder"]) vi.stubGlobal(name, undefined);
    try {
      const decoded = parseJobOutputPage({
        offsetBytes: 0,
        bytesReturned: 4,
        totalBytes: 4,
        retainedStartBytes: 0,
        encoding: "base64",
        data: "8J+YgA==",
      });
      expect(decoded?.bytes).toEqual(new Uint8Array([0xf0, 0x9f, 0x98, 0x80]));
      expect(decodeJobOutputText(decoded?.bytes ?? new Uint8Array())).toBe("😀");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("reports original byte spans for valid and malformed scalars", () => {
    const bytes = new Uint8Array([0x61, 0xc3, 0xa9, 0xf0, 0x9f, 0x98, 0x80, 0xff, 0xe2, 0x82, 0x62]);
    const values: Array<[string, number, number]> = [];
    forEachJobOutputScalar(bytes, (value, start, end) => values.push([value, start, end]));
    expect(values).toEqual([
      ["a", 0, 1],
      ["é", 1, 3],
      ["😀", 3, 7],
      ["�", 7, 8],
      ["�", 8, 10],
      ["b", 10, 11],
    ]);
    expect(decodeJobOutputText(bytes)).toBe("aé😀��b");
  });

  it.each([
    [[0xc0, 0x80], "��"],
    [[0xed, 0xa0, 0x80], "���"],
    [[0xf4, 0x90, 0x80, 0x80], "����"],
    [[0xe2, 0x82], "�"],
    [[0xe2, 0x28, 0xa1], "�(�"],
  ] as const)("decodes malformed UTF8 %j", (bytes, text) => {
    expect(decodeJobOutputText(new Uint8Array(bytes))).toBe(text);
  });

  it("recognizes only typed pruning with coherent bounds", () => {
    expect(
      jobOutputPrunedBounds(
        new WireError("no longer retained", -32014, {
          evenerErrorInfo: "jobOutputPruned",
          retainedStartBytes: 0,
          totalBytes: 0,
        }),
      ),
    ).toEqual({ retainedStartBytes: 0, totalBytes: 0 });
    expect(
      jobOutputPrunedBounds(
        new WireError("no longer retained", -32014, {
          evenerErrorInfo: "jobOutputPruned",
          retainedStartBytes: 100,
          totalBytes: 200,
        }),
      ),
    ).toEqual({ retainedStartBytes: 100, totalBytes: 200 });
    for (const error of [
      new Error("job output is no longer retained"),
      new WireError("job output is no longer retained", -32014),
      new WireError("pruned", -32602, { evenerErrorInfo: "jobOutputPruned", retainedStartBytes: 0, totalBytes: 0 }),
      ...[-1, 201, 0.5, Number.MAX_SAFE_INTEGER + 1, "100", undefined].map(
        (retainedStartBytes) =>
          new WireError("pruned", -32014, { evenerErrorInfo: "jobOutputPruned", retainedStartBytes, totalBytes: 200 }),
      ),
    ])
      expect(jobOutputPrunedBounds(error)).toBeNull();
  });
});
