import { type EntityIdMatch, findEntityIds } from "@evener/appwire-client";
import { type FileReferenceSpan, findFileReferences, type ServedRoots } from "@evener/appwire-client/docContent";

// The two enhancement passes scan TEXT, not DOM: what a block of prose names is
// a pure function of that text and the roots it binds against. A streamed delta
// replaces the whole rendered subtree - React writes the sanitized HTML again,
// so every node is new and every reference must be wrapped again - but the scan
// that FINDS the references has nothing new to say about text that did not
// change. This memo keeps that answer, which is the per-delta scan over every
// settled block that issue #6455 names.
//
// Exactness: an entry is keyed on the exact text plus every other input of the
// scan (cwd and the served roots), so a hit returns what the scan would have
// returned. A delta that splits a token produces new text and therefore a miss,
// never a half-token answer derived from the previous frame.

// One message's blocks: an exhaustive assistant answer runs to a few hundred
// blocks, so this keeps every settled block of the message being streamed
// while staying proportional to the text that message already renders. A
// streamed tail is a new string every delta, so its entries are dead on
// arrival; the character budget stops them accumulating there, and stops one
// huge paragraph from pinning its own growing prefixes for the whole stream.
const MAX_ENTRIES = 256;
const MAX_KEY_CHARACTERS = 128 * 1024;
const MAX_CONTEXTS = 8;

/** An insertion-ordered map that keeps the most recently used entries. */
class RecentEntries<Value> {
  private readonly entries = new Map<string, Value>();
  private characters = 0;

  get(key: string): Value | undefined {
    const value = this.entries.get(key);
    if (value === undefined) return undefined;
    this.entries.delete(key);
    this.entries.set(key, value);
    return value;
  }

  set(key: string, value: Value): void {
    if (this.entries.delete(key)) this.characters -= key.length;
    this.entries.set(key, value);
    this.characters += key.length;
    // Keep the entry just written even when it alone exceeds the budget:
    // dropping it would make the memo a no-op for a long block.
    while (this.entries.size > 1 && (this.entries.size > MAX_ENTRIES || this.characters > MAX_KEY_CHARACTERS)) {
      const oldest = this.entries.keys().next().value;
      if (oldest === undefined) return;
      this.entries.delete(oldest);
      this.characters -= oldest.length;
    }
  }
}

/** The analysis one enhancement pass reuses from the pass before it. */
export interface AnalysisCache {
  fileSpans(text: string, cwd: string, served: ServedRoots | undefined): FileReferenceSpan[];
  entityIds(text: string): EntityIdMatch[];
}

/** One cache per rendered prose root, so its entries live and die with the
 * message that produced them. */
export function createAnalysisCache(): AnalysisCache {
  const entities = new RecentEntries<EntityIdMatch[]>();
  const byContext = new Map<string, RecentEntries<FileReferenceSpan[]>>();
  // A pass runs block by block under one context, so the last one is memoized
  // and the composite key is built only when the context actually changes.
  let lastCwd: string | undefined;
  let lastArtifacts: string | undefined;
  let lastScratch: string | undefined;
  let lastSpans: RecentEntries<FileReferenceSpan[]> | undefined;

  function spansFor(cwd: string, served: ServedRoots | undefined): RecentEntries<FileReferenceSpan[]> {
    const artifacts = served?.artifactsDir ?? "";
    const scratch = served?.scratchDir ?? "";
    if (lastSpans !== undefined && cwd === lastCwd && artifacts === lastArtifacts && scratch === lastScratch) {
      return lastSpans;
    }
    const context = JSON.stringify([cwd, artifacts, scratch]);
    let entries = byContext.get(context);
    if (entries === undefined) {
      entries = new RecentEntries<FileReferenceSpan[]>();
      byContext.set(context, entries);
      if (byContext.size > MAX_CONTEXTS) {
        const oldest = byContext.keys().next().value;
        if (oldest !== undefined) byContext.delete(oldest);
      }
    }
    lastCwd = cwd;
    lastArtifacts = artifacts;
    lastScratch = scratch;
    lastSpans = entries;
    return entries;
  }

  return {
    fileSpans(text, cwd, served) {
      const entries = spansFor(cwd, served);
      const cached = entries.get(text);
      if (cached !== undefined) return cached;
      const computed = findFileReferences(text, cwd, served);
      entries.set(text, computed);
      return computed;
    },
    entityIds(text) {
      const cached = entities.get(text);
      if (cached !== undefined) return cached;
      const computed = findEntityIds(text);
      entities.set(text, computed);
      return computed;
    },
  };
}
