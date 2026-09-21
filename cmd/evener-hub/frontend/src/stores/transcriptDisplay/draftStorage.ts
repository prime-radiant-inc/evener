// The browser's transcript draft checkpoint port: one namespaced localStorage
// record holding the single checkpoint the draft editor persists - the
// durable intent a save must name before its request can leave, and what the
// next instance (this tab after a reload, or another tab of the same profile)
// restores. The package's repository owns every identity and classification
// decision; this port only stores, reads, and compares, by the same canonical
// JSON a byte-aware port would.
import { canonicalJson, type TranscriptDraftCheckpoint, type TranscriptDraftStorage } from "@evener/appwire-client";
import { makeSourceId } from "./crossTabSync";

const DRAFT_KEY = "evener.prefs.transcriptDisplay.draft";

/** A record this build cannot even parse is still a record: handed back raw
 * so the package's decoder classifies it unreadable, keeping the one
 * recovery (discard) available rather than reporting the port itself
 * failed. A missing record is null; a blocked storage is a throw, which is
 * the port-failure signal the package's recovery machinery keys on. */
function readRecord(): unknown {
  if (typeof localStorage === "undefined") throw new Error("localStorage is unavailable");
  const raw = localStorage.getItem(DRAFT_KEY);
  if (raw === null) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

function writeRecord(value: TranscriptDraftCheckpoint): void {
  if (typeof localStorage === "undefined") throw new Error("localStorage is unavailable");
  const encoded = JSON.stringify(value);
  localStorage.setItem(DRAFT_KEY, encoded);
  if (localStorage.getItem(DRAFT_KEY) !== encoded) throw new Error("localStorage did not retain the value");
}

/** The draft port the web hands the package store - stateless on purpose:
 * every piece of state lives in the record, so each package store instance
 * (one per client identity) reads the same durable checkpoint. */
export function browserDraftStorage(): TranscriptDraftStorage {
  return {
    createId: makeSourceId,
    load: readRecord,
    save: writeRecord,
    insertIfAbsent(checkpoint) {
      const stored = readRecord();
      if (stored !== null) return false;
      writeRecord(checkpoint);
      return true;
    },
    removeIf(identity) {
      const stored = readRecord();
      if (stored === null || canonicalJson(identity) !== canonicalJson(stored)) return false;
      localStorage.removeItem(DRAFT_KEY);
      if (localStorage.getItem(DRAFT_KEY) !== null) throw new Error("localStorage retained the value");
      return true;
    },
    replaceIf(expected, next) {
      const stored = readRecord();
      if (stored === null || canonicalJson(expected) !== canonicalJson(stored)) return false;
      writeRecord(next);
      return true;
    },
  };
}
