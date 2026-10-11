// The browser's transcript draft checkpoint port: one namespaced localStorage
// record holding the single checkpoint the draft editor persists - the
// durable intent a save must name before its request can leave, and what the
// next instance (this tab after a reload, or another tab of the same profile)
// restores. The package's repository owns every identity and classification
// decision; this port only stores, reads, and compares, by the same canonical
// JSON a byte-aware port would.
//
// Multi-tab note: every mutation reads the record and compares against what
// the storage holds at decision time, then mutates in the adjacent
// statement. A competing tab's write landing inside that inter-statement gap
// cannot be fenced away: localStorage has no test-and-set, IndexedDB's
// transactions are async-only, and the Web Locks API is async while the
// package's port contract is synchronous throughout. Every replacement
// visible at call time is handled the protocol's way - the compare refuses
// and the package adopts the replacement - so the residual window can only
// lose one in-progress draft record to a same-microsecond collision between
// tabs of the same user, which is the pre-A9 status quo (the web had no
// draft persistence at all).
import { canonicalJson, type TranscriptDraftCheckpoint, type TranscriptDraftStorage } from "@evener/appwire-client";
import { makeSourceId } from "./crossTabSync";

const DRAFT_KEY = "evener.prefs.transcriptDisplay.draft";

/** The identity of a present-but-unreadable record, preserving the exact
 * bytes it was read from. A class instance, not a plain object: JSON can
 * produce an object with any shape, but never an instance of this class, so
 * a record another writer stored can never impersonate an unreadable
 * record's identity and be deleted by a stale discard - and the raw bytes it
 * names make one record distinct from another that happens to decode to the
 * same value (the invalid bytes `broken` and the valid JSON string
 * `"broken"` are different records). A stored `null` stays a present record
 * instead of reading as absent. */
class UnreadableRecord {
  constructor(readonly raw: string) {}
}

function isUnreadableRecord(value: unknown): value is UnreadableRecord {
  return value instanceof UnreadableRecord;
}

/** A record this build cannot even parse is still a record: handed back raw
 * inside a tagged identity so the package's decoder classifies it
 * unreadable, keeping the one recovery (discard) available rather than
 * reporting the port itself failed. Only a MISSING record is null; a blocked
 * storage is a throw, which is the port-failure signal the package's
 * recovery machinery keys on. */
function readRecord(): unknown {
  if (typeof localStorage === "undefined") throw new Error("localStorage is unavailable");
  const raw = localStorage.getItem(DRAFT_KEY);
  if (raw === null) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    parsed = null;
  }
  return parsed === null ? new UnreadableRecord(raw) : parsed;
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
      if (isUnreadableRecord(identity)) {
        // An unreadable identity names the exact bytes it was read from: it
        // matches only a record those bytes still back, never a replacement
        // that merely parses to a similar value.
        if (!isUnreadableRecord(stored) || stored.raw !== identity.raw) return false;
      } else if (stored === null || isUnreadableRecord(stored) || canonicalJson(identity) !== canonicalJson(stored)) {
        return false;
      }
      localStorage.removeItem(DRAFT_KEY);
      if (localStorage.getItem(DRAFT_KEY) !== null) throw new Error("localStorage retained the value");
      return true;
    },
    replaceIf(expected, next) {
      const stored = readRecord();
      if (isUnreadableRecord(expected)) {
        if (!isUnreadableRecord(stored) || stored.raw !== expected.raw) return false;
      } else if (stored === null || isUnreadableRecord(stored) || canonicalJson(expected) !== canonicalJson(stored)) {
        return false;
      }
      writeRecord(next);
      return true;
    },
  };
}
