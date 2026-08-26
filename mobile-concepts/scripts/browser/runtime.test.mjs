import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  classifyAuditedRequest,
  summarizeAuditedUrl,
} from "./audit.mjs";
import { writeResultsJson } from "./evidence.mjs";

const origin = "http://127.0.0.1:4173";

test("data request audit is bounded deterministic and retains no payload", () => {
  const payload = "PRIVATE_PAYLOAD_MARKER_".repeat(20_000);
  const url = `data:image/png;base64,${payload}`;
  const first = summarizeAuditedUrl(url);
  const second = summarizeAuditedUrl(url);
  assert.deepEqual(first, second);
  assert.deepEqual(first, {
    scheme: "data:",
    mime: "image/png",
    encoding: "base64",
    encodedLength: payload.length,
    sha256: first.sha256,
  });
  assert.match(first.sha256, /^[0-9a-f]{64}$/);
  const serialized = JSON.stringify(first);
  assert.equal(serialized.includes("PRIVATE_PAYLOAD_MARKER"), false);
  assert.ok(serialized.length < 256);
});

test("data request audit remains bounded for hostile metadata", () => {
  const marker = "PRIVATE_METADATA_MARKER_".repeat(1_000);
  const summary = summarizeAuditedUrl(`data:${marker}/png;base64,AQ==`);
  assert.equal(summary.mime, "invalid");
  assert.equal(summary.encodedLength, 4);
  assert.equal(JSON.stringify(summary).includes("PRIVATE_METADATA_MARKER"), false);
  assert.ok(JSON.stringify(summary).length < 256);
});

test("blob request audit is bounded deterministic and retains no opaque payload", () => {
  const opaque = "PRIVATE_BLOB_MARKER_".repeat(10_000);
  const url = `blob:${origin}/${opaque}`;
  const summary = summarizeAuditedUrl(url);
  assert.deepEqual(summary, summarizeAuditedUrl(url));
  assert.equal(summary.scheme, "blob:");
  assert.equal(summary.mime, null);
  assert.equal(summary.encodedLength, url.length - "blob:".length);
  assert.match(summary.sha256, /^[0-9a-f]{64}$/);
  assert.equal(JSON.stringify(summary).includes("PRIVATE_BLOB_MARKER"), false);
  assert.ok(JSON.stringify(summary).length < 256);
});

test("classification uses exact original URL before data/blob normalization", () => {
  const data = classifyAuditedRequest(
    "data:text/plain;charset=utf-8,hello%20world",
    origin,
  );
  assert.equal(data.offOrigin, false);
  assert.deepEqual(data.audit, {
    scheme: "data:",
    mime: "text/plain",
    encoding: "percent",
    encodedLength: "hello%20world".length,
    sha256: data.audit.sha256,
  });

  const blob = classifyAuditedRequest(`blob:${origin}/fixture-id`, origin);
  assert.equal(blob.offOrigin, false);
  assert.equal(blob.audit.scheme, "blob:");

  const local = classifyAuditedRequest(`${origin}/assets/app.js`, origin);
  assert.deepEqual(local, {
    offOrigin: false,
    audit: `${origin}/assets/app.js`,
  });

  const foreignUrl = "http://127.0.0.1:4173@evil.example/payload";
  const foreign = classifyAuditedRequest(foreignUrl, origin);
  assert.equal(foreign.offOrigin, true);
  assert.equal(foreign.audit, foreignUrl);

  const malformed = classifyAuditedRequest("not a URL", origin);
  assert.equal(malformed.offOrigin, true);
  assert.equal(malformed.audit, "not a URL");
});

test("results writer streams many large results and parses back exactly", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "results-stream-test-"));
  const file = path.join(directory, "results.json");
  try {
    const results = Array.from({ length: 96 }, (_, index) => ({
      index,
      payload: String(index).padStart(3, "0").repeat(50_000),
      nested: { values: [index, null, true, `case-${index}`] },
    }));
    const value = {
      origin,
      results,
      csp: { violations: ["connect-src", "img-src"], connections: 0 },
    };
    const evidence = await writeResultsJson(file, value);
    assert.equal(evidence, file);
    assert.deepEqual(JSON.parse(await readFile(file, "utf8")), value);
    assert.deepEqual(await readdir(directory), ["results.json"]);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("results writer is atomic when result serialization fails", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "results-atomic-test-"));
  const file = path.join(directory, "results.json");
  try {
    await writeFile(file, "previous-complete-evidence\n");
    await assert.rejects(
      writeResultsJson(file, {
        origin,
        results: [{ invalid: 1n }],
        csp: {},
      }),
      /BigInt/,
    );
    assert.equal(await readFile(file, "utf8"), "previous-complete-evidence\n");
    assert.deepEqual(await readdir(directory), ["results.json"]);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("results writer preserves primary and cleanup failures", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "results-cleanup-test-"));
  const file = path.join(directory, "results.json");
  const calls = [];
  try {
    await assert.rejects(
      writeResultsJson(
        file,
        { origin, results: [], csp: {} },
        {
          async renameFile() {
            calls.push("rename");
            throw new Error("rename failed");
          },
          async removeFile() {
            calls.push("remove");
            throw new Error("remove failed");
          },
        },
      ),
      (error) => {
        assert(error instanceof AggregateError);
        assert.deepEqual(
          error.errors.map(({ message }) => message),
          ["rename failed", "remove failed"],
        );
        return true;
      },
    );
    assert.deepEqual(calls, ["rename", "remove"]);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
