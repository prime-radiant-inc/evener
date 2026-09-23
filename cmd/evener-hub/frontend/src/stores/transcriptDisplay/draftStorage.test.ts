// The browser draft port's own contract: what load() hands the package for
// absent, readable, and unreadable records, and that the compare-and-swap
// primitives compare the record's identity - never conflating one record
// with another that decodes to the same value, and never deciding on state
// other than what the storage holds at decision time.
import type { TranscriptDraftCheckpoint } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { MemoryStorage } from "../../storageTestUtils";
import { browserDraftStorage } from "./draftStorage";

const DRAFT_KEY = "evener.prefs.transcriptDisplay.draft";

const storage = new MemoryStorage();

const checkpoint: TranscriptDraftCheckpoint = {
  id: "d1",
  layout: "mobile",
  baseRevision: 2,
  config: makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
  writeUncertain: false,
};

beforeEach(() => {
  storage.clear();
  vi.stubGlobal("localStorage", storage);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the browser transcript draft storage port", () => {
  test("absent, readable, and unreadable records load as distinct identities", () => {
    const port = browserDraftStorage();
    expect(port.load()).toBeNull();
    storage.setItem(DRAFT_KEY, JSON.stringify(checkpoint));
    expect(port.load()).toEqual(checkpoint);
    // Unparseable bytes, and bytes that parse to null, are still PRESENT
    // records: identities carrying their raw bytes, never reading as
    // absent.
    storage.setItem(DRAFT_KEY, "not json");
    const unparseable = port.load();
    expect(unparseable).toMatchObject({ raw: "not json" });
    storage.setItem(DRAFT_KEY, "null");
    expect(port.load()).toMatchObject({ raw: "null" });
  });

  test("a stored record cannot impersonate an unreadable record's identity", () => {
    storage.setItem(DRAFT_KEY, "not json");
    const port = browserDraftStorage();
    const stale = port.load();
    // Another writer replaces the record with the JSON object carrying the
    // same raw bytes: no stored value can forge the unreadable identity, so
    // a stale discard must refuse.
    storage.setItem(DRAFT_KEY, JSON.stringify({ raw: "not json" }));
    expect(port.removeIf(stale)).toBe(false);
    expect(port.replaceIf(stale, checkpoint)).toBe(false);
    expect(storage.getItem(DRAFT_KEY)).toBe(JSON.stringify({ raw: "not json" }));
    // The same bytes back, and the stale identity matches again.
    storage.setItem(DRAFT_KEY, "not json");
    expect(port.removeIf(stale)).toBe(true);
    expect(storage.getItem(DRAFT_KEY)).toBeNull();
  });

  test("a stored null is a present record insertIfAbsent refuses to overwrite", () => {
    storage.setItem(DRAFT_KEY, "null");
    const port = browserDraftStorage();
    expect(port.insertIfAbsent(checkpoint)).toBe(false);
    expect(storage.getItem(DRAFT_KEY)).toBe("null");
  });

  test("unreadable bytes and a JSON string decoding to the same text stay distinct records", () => {
    storage.setItem(DRAFT_KEY, "broken");
    const port = browserDraftStorage();
    const stale = port.load();
    // Another writer replaces the record with the VALID JSON string
    // "broken": a stale discard naming the old bytes must not match it.
    storage.setItem(DRAFT_KEY, JSON.stringify("broken"));
    expect(port.removeIf(stale)).toBe(false);
    expect(storage.getItem(DRAFT_KEY)).toBe(JSON.stringify("broken"));
  });

  test("a tagged unreadable record is removed by its own identity", () => {
    storage.setItem(DRAFT_KEY, "not json");
    const port = browserDraftStorage();
    expect(port.removeIf(port.load())).toBe(true);
    expect(storage.getItem(DRAFT_KEY)).toBeNull();
  });

  test("compare-and-swap refuses a record another writer replaced before the call", () => {
    const port = browserDraftStorage();
    port.save(checkpoint);
    // The competing-writer window the port CAN see: another tab's record
    // landing before this port's call reads the storage. Every mutation
    // compares against what the storage holds at decision time, so the
    // stale identity refuses both ways and the replacement survives.
    const competing: TranscriptDraftCheckpoint = { ...checkpoint, id: "other", baseRevision: 3 };
    storage.setItem(DRAFT_KEY, JSON.stringify(competing));
    expect(port.removeIf(checkpoint)).toBe(false);
    expect(port.replaceIf(checkpoint, { ...checkpoint, writeUncertain: true })).toBe(false);
    expect(port.load()).toEqual(competing);
  });

  test("save verifies retention, and a blocked storage is a port failure", () => {
    const port = browserDraftStorage();
    port.save(checkpoint);
    expect(port.load()).toEqual(checkpoint);
    expect(port.removeIf(checkpoint)).toBe(true);
    expect(port.insertIfAbsent(checkpoint)).toBe(true);
    expect(port.replaceIf(checkpoint, { ...checkpoint, writeUncertain: true })).toBe(true);
    expect(storage.getItem(DRAFT_KEY)).toBe(JSON.stringify({ ...checkpoint, writeUncertain: true }));

    vi.stubGlobal("localStorage", {
      getItem: () => {
        throw new Error("storage blocked");
      },
      setItem: () => {
        throw new Error("storage blocked");
      },
      removeItem: () => {
        throw new Error("storage blocked");
      },
    });
    expect(() => port.load()).toThrow("storage blocked");
    expect(() => port.save(checkpoint)).toThrow("storage blocked");
  });
});
